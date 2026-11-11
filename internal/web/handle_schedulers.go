package web

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BarkinBalci/asynqui/internal/web/format"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// SchedulerSource is what the Schedulers pages need from the connection
// registry.
type SchedulerSource interface {
	// SchedulerEntries lists the entries of the connection's running
	// schedulers. It may be served from a short-lived cache.
	SchedulerEntries(ctx context.Context, name string) ([]SchedulerEntry, error)
	// SchedulerEnqueueEvents returns page page (from 1) of size enqueue
	// events of an entry, newest first. queue is the entry's queue, where
	// each event's task is looked up.
	SchedulerEnqueueEvents(ctx context.Context, name, entryID, queue string, page, size int) (EnqueuePage, error)
}

// SchedulerEntry is a periodic task registered by a running scheduler.
// Its ID is regenerated when the scheduler restarts.
type SchedulerEntry struct {
	ID       string
	Spec     string
	TaskType string
	Payload  []byte
	// Options are as asynq writes them: "Queue(\"reports\")", "MaxRetry(3)".
	Options []string
	Queue   string
	Next    time.Time
	// Prev is zero before the first enqueue.
	Prev time.Time
}

// EnqueueEvent is a task an entry enqueued.
type EnqueueEvent struct {
	TaskID     string
	EnqueuedAt time.Time
	// TaskExists reports whether the task is still stored.
	TaskExists bool
}

// EnqueuePage is a page of enqueue events, newest first.
type EnqueuePage struct {
	Events  []EnqueueEvent
	HasNext bool
}

// historyPageSize is the number of enqueue events per page.
const historyPageSize = 20

// entriesSort is the schedulers table's sort: every column but Payload and
// Options, by Next run ascending by default; every column sorts ascending
// first.
func entriesSort() sortSpec {
	return sortSpec{
		cols:      []string{"id", "spec", "type", "next", "last"},
		def:       int(pages.EntColNext),
		firstDesc: func(int) bool { return false },
	}
}

// handleSchedulers serves GET /c/{conn}/schedulers. With HX-Request (a
// poll or a sort) it answers with out-of-band swaps.
func handleSchedulers(d *deps) http.Handler {
	spec := entriesSort()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		m := d.schedulersModel(r, a, spec, liveAnswer(r, pages.SchedulersPollID))
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.SchedulersPoll(m))
			return
		}
		d.render(w, r, http.StatusOK, pages.SchedulersPage(m))
	})
}

func (d *deps) schedulersModel(r *http.Request, a access, spec sortSpec, ans answer) pages.Schedulers {
	name := a.conn.Name
	st := spec.parse(r.URL.Query())
	q := url.Values{}
	spec.set(q, st)
	entries, rd := readLive(d, r, a, func(ctx context.Context) ([]SchedulerEntry, error) { return d.conns.SchedulerEntries(ctx, name) })
	m := pages.Schedulers{
		Layout: d.liveLayout(r, a, components.SectionSchedulers, rd, sectionCrumb(d, components.SectionSchedulers, "c", name, "schedulers")),
		Live:   d.live(ans, pages.SchedulersPageID, pages.SchedulersPollID, rd, q, "c", name, "schedulers"),
	}
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(x, y SchedulerEntry) int { return compareEntries(x, y, pages.EntryColumn(st.col), st.desc) })
	for _, e := range sorted {
		m.Rows = append(m.Rows, pages.EntryRow{
			ID:       e.ID,
			ShortID:  format.ShortID(e.ID),
			URL:      d.url("c", name, "schedulers", e.ID),
			LinkID:   components.HexID("elink-", e.ID),
			Spec:     e.Spec,
			TaskType: e.TaskType,
			Payload:  strings.ToValidUTF8(string(e.Payload), "�"),
			Options:  e.Options,
			Next:     e.Next,
			Prev:     e.Prev,
		})
	}
	heads := spec.heads(st, url.Values{}, func(int) bool { return false },
		func(q url.Values) string { return d.urlQuery(q, "c", name, "schedulers") })
	copy(m.Heads[:], heads)
	return m
}

// compareEntries orders two entries by column c, ties by ID.
func compareEntries(x, y SchedulerEntry, c pages.EntryColumn, desc bool) int {
	var r int
	switch c {
	case pages.EntColID:
		r = strings.Compare(x.ID, y.ID)
	case pages.EntColSpec:
		r = strings.Compare(x.Spec, y.Spec)
	case pages.EntColType:
		r = strings.Compare(x.TaskType, y.TaskType)
	case pages.EntColNext:
		r = x.Next.Compare(y.Next)
	case pages.EntColPrev:
		r = x.Prev.Compare(y.Prev)
	}
	if desc {
		r = -r
	}
	return cmp.Or(r, strings.Compare(x.ID, y.ID))
}

// handleSchedulerEntry serves GET /c/{conn}/schedulers/{id}?page=, the
// Scheduler entry page. A stale or unknown ID shows "Entry not found"
// (404 for the page itself; a poll gets the notice out of band).
func handleSchedulerEntry(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		m := d.entryModel(r, a, liveAnswer(r, pages.EntryPollID))
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.SchedulerEntryPoll(m))
			return
		}
		status := http.StatusOK
		if m.Live.State == pages.DataReady && !m.Found {
			status = http.StatusNotFound
		}
		d.render(w, r, status, pages.SchedulerEntryPage(m))
	})
}

// historyPage is the ?page= value, 1 when absent or invalid.
func historyPage(q url.Values) int {
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 1 {
		return n
	}
	return 1
}

// pageQuery is the query of history page n; page 1 is the default.
func pageQuery(n int) url.Values {
	q := url.Values{}
	if n > 1 {
		q.Set("page", strconv.Itoa(n))
	}
	return q
}

func (d *deps) entryModel(r *http.Request, a access, ans answer) pages.SchedulerEntry {
	name, id := a.conn.Name, r.PathValue("id")
	page := historyPage(r.URL.Query())
	deadline := time.Now().Add(d.pageBudget) // shared by both reads
	entries, rd := readLive(d, r, a, func(ctx context.Context) ([]SchedulerEntry, error) { return d.conns.SchedulerEntries(ctx, name) })
	i := slices.IndexFunc(entries, func(e SchedulerEntry) bool { return e.ID == id })

	crumb := components.Crumb{Text: format.ShortID(id), Mono: true, Title: id}
	m := pages.SchedulerEntry{
		Layout: d.liveLayout(r, a, components.SectionSchedulers, rd,
			sectionCrumb(d, components.SectionSchedulers, "c", name, "schedulers"), crumb),
		Live:          d.live(ans, pages.EntryPageID, pages.EntryPollID, rd, pageQuery(page), "c", name, "schedulers", id),
		SchedulersURL: d.url("c", name, "schedulers"),
		Found:         i >= 0,
	}
	if i < 0 {
		return m
	}
	e := entries[i]
	m.ID, m.TaskType, m.Spec, m.Queue = e.ID, e.TaskType, e.Spec, e.Queue
	m.QueueURL = d.url("c", name, "queues", e.Queue)
	m.Payload = payloadText(e.Payload)
	m.Next, m.Prev = e.Next, e.Prev
	for _, o := range e.Options {
		if !strings.HasPrefix(o, "Queue(") { // shown as Queue
			m.Options = append(m.Options, o)
		}
	}

	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	ev, err := d.conns.SchedulerEnqueueEvents(ctx, name, e.ID, e.Queue, page, historyPageSize)
	h := &m.History
	switch {
	case err != nil && ctx.Err() != nil:
		h.State = pages.DataChecking
	case err != nil:
		h.State, h.Error = pages.DataFailed, errText(err)
	default:
		for _, x := range ev.Events {
			row := pages.EventRow{TaskID: x.TaskID, ShortID: format.ShortID(x.TaskID), EnqueuedAt: x.EnqueuedAt}
			if x.TaskExists {
				row.TaskURL = d.url("c", name, "queues", e.Queue, "tasks", x.TaskID)
				row.LinkID = components.HexID("etask-", x.TaskID)
			}
			h.Events = append(h.Events, row)
		}
		if n := len(h.Events); n > 0 {
			h.From = int64((page-1)*historyPageSize + 1)
			h.To = h.From + int64(n) - 1
		}
		if page > 1 {
			h.PrevURL = d.urlQuery(pageQuery(page-1), "c", name, "schedulers", id)
		}
		if ev.HasNext {
			h.NextURL = d.urlQuery(pageQuery(page+1), "c", name, "schedulers", id)
		}
	}
	return m
}

// payloadText is a payload as shown: indented when it is JSON, otherwise
// as text with invalid UTF-8 replaced.
func payloadText(b []byte) string {
	if json.Valid(b) {
		var buf bytes.Buffer
		if err := json.Indent(&buf, b, "", "  "); err == nil {
			return buf.String()
		}
	}
	return strings.ToValidUTF8(string(b), "�")
}
