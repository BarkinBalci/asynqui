package web

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web/format"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// SchedulerSource is what the Schedulers pages need from a connection.
type SchedulerSource interface {
	// SchedulerEntries lists the entries of the connection's running
	// schedulers. It may be served from a short-lived cache.
	SchedulerEntries(ctx context.Context) ([]conn.SchedulerEntry, error)
	// SchedulerEnqueueEvents returns page page (from 1) of size enqueue
	// events of an entry, newest first. queue is the entry's queue, where
	// each event's task is looked up; empty (unknown) looks none up.
	SchedulerEnqueueEvents(ctx context.Context, entryID, queue string, page, size int) (conn.EnqueuePage, error)
}

// historyPageSize is the number of enqueue events per page.
const historyPageSize = 20

// maxEnqueueEvents is how many enqueue events asynq keeps per entry.
const maxEnqueueEvents = 1000

// historyMaxPage is the last history page; pages past it are out of range.
const historyMaxPage = (maxEnqueueEvents + historyPageSize - 1) / historyPageSize

// Payload previews in the schedulers table: the cell and its title.
const (
	payloadCellRunes  = 60
	payloadTitleRunes = 300
)

// entriesSort is the schedulers table's sort.
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
	name := a.conn.Name()
	st := spec.parse(r.URL.Query())
	q := url.Values{}
	spec.set(q, st)
	ctx, cancel := d.pageContext(r.Context())
	defer cancel()
	entries, rd := readLive(ctx, a, a.conn.SchedulerEntries)
	m := pages.Schedulers{
		Layout: d.liveLayout(r, a, components.SectionSchedulers, rd, sectionCrumb(d, components.SectionSchedulers, "c", name, "schedulers")),
		Live:   d.live(ans, pages.SchedulersPageID, pages.SchedulersPollID, rd, rd.state == pages.DataReady, q, "c", name, "schedulers"),
	}
	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(x, y conn.SchedulerEntry) int { return compareEntries(x, y, pages.EntryColumn(st.col), st.desc) })
	for _, e := range sorted {
		text := format.Text(e.Payload)
		m.Rows = append(m.Rows, pages.EntryRow{
			ID:           e.ID,
			ShortID:      format.ShortID(e.ID),
			URL:          d.url("c", name, "schedulers", e.ID),
			LinkID:       components.HexID("elink-", e.ID),
			Spec:         e.Spec,
			TaskType:     e.TaskType,
			Payload:      format.Preview(text, payloadCellRunes),
			PayloadTitle: format.Preview(text, payloadTitleRunes),
			Options:      optionTokens(e.Options, false),
			Next:         e.Next,
			Prev:         e.Prev,
		})
	}
	heads := spec.heads(st, url.Values{}, func(int) bool { return false },
		func(q url.Values) string { return d.urlQuery(q, "c", name, "schedulers") })
	copy(m.Heads[:], heads)
	return m
}

// optionTokens are an entry's options for display; withoutQueue leaves
// the queue option out (the details show it on its own row).
func optionTokens(opts []conn.EntryOption, withoutQueue bool) []pages.OptionToken {
	var out []pages.OptionToken
	for _, o := range opts {
		if withoutQueue && o.Name == "queue" {
			continue
		}
		out = append(out, pages.OptionToken{Name: o.Name, Value: o.Value})
	}
	return out
}

// compareEntries orders two entries by column c, ties by ID.
func compareEntries(x, y conn.SchedulerEntry, c pages.EntryColumn, desc bool) int {
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

// handleSchedulerEntry serves GET /c/{conn}/schedulers/{id}?page=&payload=,
// the Scheduler entry page. A stale or unknown ID shows "Entry not found"
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

// entryView is the Scheduler entry page's state in its URL query: ?page=
// (the history page, default 1) and ?payload=raw (the raw payload view).
type entryView struct {
	page int
	raw  bool
}

func parseEntryView(q url.Values) entryView {
	v := entryView{page: 1, raw: q.Get("payload") == "raw"}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 1 {
		v.page = n
	}
	return v
}

// values is the query of v, without defaults.
func (v entryView) values() url.Values {
	q := url.Values{}
	if v.page > 1 {
		q.Set("page", strconv.Itoa(v.page))
	}
	if v.raw {
		q.Set("payload", "raw")
	}
	return q
}

func (d *deps) entryURL(name, id string, v entryView) string {
	return d.urlQuery(v.values(), "c", name, "schedulers", id)
}

func (d *deps) entryModel(r *http.Request, a access, ans answer) pages.SchedulerEntry {
	name, id := a.conn.Name(), r.PathValue("id")
	v := parseEntryView(r.URL.Query())
	ctx, cancel := d.pageContext(r.Context()) // shared by both reads
	defer cancel()
	entries, rd := readLive(ctx, a, a.conn.SchedulerEntries)
	i := slices.IndexFunc(entries, func(e conn.SchedulerEntry) bool { return e.ID == id })

	ready := rd.state == pages.DataReady
	crumb := components.Crumb{Text: format.ShortID(id), Mono: true, Title: id}
	m := pages.SchedulerEntry{
		Layout: d.liveLayout(r, a, components.SectionSchedulers, rd,
			sectionCrumb(d, components.SectionSchedulers, "c", name, "schedulers"), crumb),
		Live:          d.live(ans, pages.EntryPageID, pages.EntryPollID, rd, ready && i >= 0, v.values(), "c", name, "schedulers", id),
		SchedulersURL: d.url("c", name, "schedulers"),
		Found:         i >= 0,
		Whole:         ans.init || !ready || i < 0,
		Now:           d.now(),
	}
	if i < 0 {
		return m
	}
	e := entries[i]
	m.ID, m.TaskType, m.Spec, m.Queue = e.ID, e.TaskType, e.Spec, e.Queue
	if e.Queue != "" {
		m.QueueURL = d.url("c", name, "queues", e.Queue)
	}
	m.Options = optionTokens(e.Options, true)
	m.Next, m.Prev = e.Next, e.Prev
	m.Payload = payloadView(e.Payload, v.raw, func(raw bool) string {
		pv := v
		pv.raw = raw
		return d.entryURL(name, id, pv)
	})
	m.History = d.entryHistory(ctx, a.conn, e, v)
	return m
}

// payloadView is a payload card: indented JSON with a link to the raw
// text, or the raw text (with a link back) when raw; a payload that is not
// JSON is shown raw, without the toggle. link is the page's URL in the raw
// view or not.
func payloadView(payload []byte, raw bool, link func(raw bool) string) pages.Payload {
	p := pages.Payload{Text: format.Text(payload)}
	if pretty, ok := format.PrettyJSON(payload); ok {
		p.JSON, p.Raw, p.JSONURL, p.RawURL = true, raw, link(false), link(true)
		if !raw {
			p.Text = pretty
		}
	}
	return p
}

// entryHistory reads one page of the entry's enqueue history. A page past
// historyMaxPage is out of range: it is not read, and links back to the
// last page.
func (d *deps) entryHistory(ctx context.Context, c Conn, e conn.SchedulerEntry, v entryView) pages.History {
	name := c.Name()
	var h pages.History
	page := v.page
	if page > historyMaxPage {
		last := v
		last.page = historyMaxPage
		h.PrevURL = d.entryURL(name, e.ID, last)
		return h
	}
	ev, err := c.SchedulerEnqueueEvents(ctx, e.ID, e.Queue, page, historyPageSize)
	switch {
	case err != nil && ctx.Err() != nil:
		h.State = pages.DataChecking
		return h
	case err != nil:
		h.State, h.Error = pages.DataFailed, errText(err)
		return h
	}
	for _, x := range ev.Events {
		row := pages.EventRow{TaskID: x.TaskID, ShortID: format.ShortID(x.TaskID), EnqueuedAt: x.EnqueuedAt}
		if x.TaskExists && e.Queue != "" {
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
		prev := v
		prev.page = page - 1
		h.PrevURL = d.entryURL(name, e.ID, prev)
	}
	if ev.HasNext && page < historyMaxPage {
		next := v
		next.page = page + 1
		h.NextURL = d.entryURL(name, e.ID, next)
	}
	return h
}
