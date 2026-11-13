package web

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// QueueSource is what the Connection page needs from the connection
// registry.
type QueueSource interface {
	// Queues lists the connection's queues, sorted by name. It may be
	// served from a short-lived cache shared with Summary.
	Queues(ctx context.Context, name string) ([]Queue, error)
	// History returns the daily counters of the last days days, oldest
	// first, for one queue or, with queue empty, summed over all of them.
	// An unknown queue is ErrQueueNotFound.
	History(ctx context.Context, name, queue string, days int) ([]DailyStat, error)
	// PauseQueue, ResumeQueue and DeleteQueue (empty queues only) act on
	// one queue. They return errors wrapping ErrQueueNotFound or
	// ErrQueueNotEmpty where those apply.
	PauseQueue(ctx context.Context, name, queue string) error
	ResumeQueue(ctx context.Context, name, queue string) error
	DeleteQueue(ctx context.Context, name, queue string) error
}

// latencyWarn is the queue latency the table highlights.
const latencyWarn = 30 * time.Second

// defaultPageBudget bounds how long a connection page waits for its data.
// A read that misses it renders "Checking"; it goes on in conn's cache, so
// the next poll usually has it.
const defaultPageBudget = 5 * time.Second

// maxActionForm bounds the body of a queue action post.
const maxActionForm = 64 << 10

// defaultRange is the chart's default range in days.
const defaultRange = 14

// pollInit is the poll URL's ?poll= value for a page without data
// regions: the answer then renders every region.
const pollInit = "init"

// rangeChoices are the chart's ranges in days, in control order.
func rangeChoices() []int { return []int{7, 14, 30, 90} }

// sortParam is a column's ?sort= value.
func sortParam(c pages.QueueColumn) string {
	switch c {
	case pages.ColName:
		return "name"
	case pages.ColState:
		return "state"
	case pages.ColPending:
		return "pending"
	case pages.ColActive:
		return "active"
	case pages.ColScheduled:
		return "scheduled"
	case pages.ColRetry:
		return "retry"
	case pages.ColArchived:
		return "archived"
	case pages.ColLatency:
		return "latency"
	case pages.ColMemory:
		return "memory"
	case pages.ColProcessed:
		return "processed"
	case pages.ColFailed:
		return "failed"
	}
	return "size"
}

// parseSort is the column whose ?sort= value is s.
func parseSort(s string) (pages.QueueColumn, bool) {
	for c := range pages.NumQueueColumns {
		if sortParam(c) == s {
			return c, true
		}
	}
	return 0, false
}

func dirParam(desc bool) string {
	if desc {
		return "desc"
	}
	return "asc"
}

func sortDir(desc bool) components.SortDir {
	if desc {
		return components.SortDesc
	}
	return components.SortAsc
}

// queuesView is the Connection page's state, all of it in the URL query:
// ?sort=&dir= for the table, ?range=&queue= for the chart. Defaults are
// left out of generated URLs.
type queuesView struct {
	sort  pages.QueueColumn
	desc  bool
	days  int
	queue string // chart queue; empty is all queues
}

func defaultQueuesView() queuesView {
	return queuesView{sort: pages.ColSize, desc: true, days: defaultRange}
}

// defaultDesc is the direction a column sorts in when first picked:
// ascending for names and states, descending for numbers.
func defaultDesc(c pages.QueueColumn) bool {
	return c != pages.ColName && c != pages.ColState
}

// parseQueuesView reads the state from a query; unknown values fall back
// to the defaults.
func parseQueuesView(q url.Values) queuesView {
	v := defaultQueuesView()
	if c, ok := parseSort(q.Get("sort")); ok {
		v.sort, v.desc = c, defaultDesc(c)
	}
	switch q.Get("dir") {
	case "asc":
		v.desc = false
	case "desc":
		v.desc = true
	}
	if n, ok := strings.CutSuffix(q.Get("range"), "d"); ok {
		if days, err := strconv.Atoi(n); err == nil && slices.Contains(rangeChoices(), days) {
			v.days = days
		}
	}
	v.queue = q.Get("queue")
	return v
}

// values is the query of v, without defaults.
func (v queuesView) values() url.Values {
	q := url.Values{}
	if v.sort != pages.ColSize || !v.desc {
		q.Set("sort", sortParam(v.sort))
		q.Set("dir", dirParam(v.desc))
	}
	if v.days != defaultRange {
		q.Set("range", rangeParam(v.days))
	}
	if v.queue != "" {
		q.Set("queue", v.queue)
	}
	return q
}

func rangeParam(days int) string { return strconv.Itoa(days) + "d" }

// queuesURL is the connection page URL showing state v.
func (d *deps) queuesURL(conn string, v queuesView) string {
	return d.urlQuery(v.values(), "c", conn)
}

// answer is what a Connection page request is answered with.
type answer struct {
	// poll is a poll: the chart's controls are left alone.
	poll bool
	// init is a poll from a page without data regions.
	init bool
}

// answerFor tells a poll (htmx names the triggering element in
// HX-Trigger) from a user request.
func answerFor(r *http.Request) answer {
	if !isFragment(r) || r.Header.Get("HX-Trigger") != pages.QueuesPollID {
		return answer{}
	}
	return answer{poll: true, init: r.URL.Query().Get("poll") == pollInit}
}

// handleQueues serves GET /c/{conn}, the Connection page. With HX-Request
// (a poll, a sort or a chart change) it answers with out-of-band swaps
// instead (pages.QueuesPoll).
func handleQueues(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		m := d.queuesModel(r, a, parseQueuesView(r.URL.Query()), answerFor(r))
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.QueuesPoll(m))
			return
		}
		if f, ok := d.takeFlash(w, r, a.conn.Name); ok {
			m.Layout.Toasts = pages.FlashToasts(f)
		}
		d.render(w, r, http.StatusOK, pages.QueuesPage(m))
	})
}

// queuesModel reads the connection's queues and history, in parallel and
// within the page budget, and builds the page model for state v.
func (d *deps) queuesModel(r *http.Request, a access, v queuesView, ans answer) pages.Queues {
	name := a.conn.Name
	m := pages.Queues{
		Addr:    a.conn.Addr,
		Health:  healthView(a.conn.Health.State),
		FormURL: d.url("c", name, "queues", "pause"),
		Poll:    ans.poll,
		Init:    ans.init,
	}

	var rows []Queue
	switch a.conn.Health.State {
	case HealthDown:
		m.State, m.Error = pages.DataFailed, errText(a.conn.Health.Err)
	default:
		ctx, cancel := context.WithTimeout(r.Context(), d.pageBudget)
		defer cancel()
		var (
			hist    []DailyStat
			histErr error
			wg      sync.WaitGroup
		)
		wg.Go(func() { hist, histErr = d.conns.History(ctx, name, v.queue, v.days) })
		qs, err := d.conns.Queues(ctx, name)
		wg.Wait()
		switch {
		case err != nil && ctx.Err() != nil:
			m.State = pages.DataChecking
		case err != nil:
			// Reachable at the last PING but failing now: shown failed
			// here, in the badge and in the sidebar, as on the Overview.
			m.State, m.Error, m.Health = pages.DataFailed, errText(err), components.HealthDown
		default:
			rows = qs
			if v.queue != "" && (errors.Is(histErr, ErrQueueNotFound) || !slices.ContainsFunc(rows, func(q Queue) bool { return q.Name == v.queue })) {
				// Deleted, or never there: all queues.
				v.queue = ""
				hist, histErr = d.conns.History(ctx, name, "", v.days)
			}
			m.Throughput = d.throughput(ctx, name, v, rows, hist, histErr)
		}
	}
	// A poll that finds no data keeps the table the page already shows.
	m.KeepTable = ans.poll && !ans.init && m.State != pages.DataReady

	var health map[string]components.Health
	if m.Health == components.HealthDown {
		health = map[string]components.Health{name: components.HealthDown}
	}
	crumbs := []components.Crumb{{Text: name, URL: d.url("c", name)}}
	m.Layout = d.layoutWith(r, a, components.SectionQueues, crumbs, d.visible(a.id), health)

	m.Next = d.queuesURL(name, v)
	pq := v.values()
	if m.State != pages.DataReady && !m.KeepTable {
		pq.Set("poll", pollInit) // the page will have no data regions
	}
	m.PollURL = d.urlQuery(pq, "c", name)
	d.queueRows(&m, name, v, rows)
	d.queueActions(&m, a)
	return m
}

// queueRows fills the table: sorted rows, the Total row and the sort
// headers.
func (d *deps) queueRows(m *pages.Queues, conn string, v queuesView, rows []Queue) {
	sorted := slices.Clone(rows)
	slices.SortStableFunc(sorted, func(x, y Queue) int { return compareQueues(x, y, v.sort, v.desc) })
	for _, q := range sorted {
		row := pages.QueueRow{
			Name:   q.Name,
			URL:    d.url("c", conn, "queues", q.Name),
			LinkID: components.HexID("qlink-", q.Name),
			Select: components.RowSelect{
				ID: components.HexID("qsel-", q.Name), Form: pages.QueuesFormID, Name: "queue", Value: q.Name,
			},
			Paused:      q.Paused,
			Size:        int64(q.Size),
			Pending:     int64(q.Pending),
			Active:      int64(q.Active),
			Scheduled:   int64(q.Scheduled),
			Retry:       int64(q.Retry),
			Archived:    int64(q.Archived),
			Processed:   int64(q.ProcessedToday),
			Failed:      int64(q.FailedToday),
			Memory:      q.MemoryUsage,
			Latency:     q.Latency,
			LatencyHigh: q.Latency > latencyWarn,
		}
		m.Rows = append(m.Rows, row)
		t := &m.Total
		t.Size += row.Size
		t.Pending += row.Pending
		t.Active += row.Active
		t.Scheduled += row.Scheduled
		t.Retry += row.Retry
		t.Archived += row.Archived
		t.Processed += row.Processed
		t.Failed += row.Failed
		t.Memory += row.Memory
	}
	for c := range pages.NumQueueColumns {
		next := v
		h := components.SortHead{ID: "sort-" + sortParam(c), Num: c >= pages.ColSize}
		if c == v.sort {
			h.Dir = sortDir(v.desc)
			next.desc = !v.desc
		} else {
			next.sort, next.desc = c, defaultDesc(c)
		}
		h.URL = d.queuesURL(conn, next)
		m.Heads[c] = h
	}
}

// compareQueues orders two queues by column c, ties by name. A paused
// queue's latency is shown as "—" and sorts last either way.
func compareQueues(x, y Queue, c pages.QueueColumn, desc bool) int {
	byName := strings.Compare(x.Name, y.Name)
	if c == pages.ColLatency && x.Paused != y.Paused {
		if x.Paused {
			return 1
		}
		return -1
	}
	var r int
	switch c {
	case pages.ColName:
		r = byName
	case pages.ColState:
		r = cmp.Compare(stateKey(x.Paused), stateKey(y.Paused))
	case pages.ColSize:
		r = cmp.Compare(x.Size, y.Size)
	case pages.ColPending:
		r = cmp.Compare(x.Pending, y.Pending)
	case pages.ColActive:
		r = cmp.Compare(x.Active, y.Active)
	case pages.ColScheduled:
		r = cmp.Compare(x.Scheduled, y.Scheduled)
	case pages.ColRetry:
		r = cmp.Compare(x.Retry, y.Retry)
	case pages.ColArchived:
		r = cmp.Compare(x.Archived, y.Archived)
	case pages.ColLatency:
		r = cmp.Compare(x.Latency, y.Latency)
	case pages.ColMemory:
		r = cmp.Compare(x.MemoryUsage, y.MemoryUsage)
	case pages.ColProcessed:
		r = cmp.Compare(x.ProcessedToday, y.ProcessedToday)
	case pages.ColFailed:
		r = cmp.Compare(x.FailedToday, y.FailedToday)
	}
	if desc {
		r = -r
	}
	if r == 0 && c != pages.ColName {
		return byName
	}
	return r
}

// stateKey orders states as their labels do: Paused before Running.
func stateKey(paused bool) int {
	if paused {
		return 0
	}
	return 1
}

// queueActions fills the toolbar buttons and the delete dialog for the
// viewer's role.
func (d *deps) queueActions(m *pages.Queues, a access) {
	name := a.conn.Name
	pause, resume, del := d.url("c", name, "queues", "pause"), d.url("c", name, "queues", "resume"), d.url("c", name, "queues", "delete")
	op, admin := a.role >= authz.RoleOperator, a.role >= authz.RoleAdmin
	m.Pause = components.Action{ID: "act-pause", URL: pause, Form: pages.QueuesFormID, Allowed: op, Need: components.RoleOperator,
		Enable: components.EnableState, State: "running"}
	m.Resume = components.Action{ID: "act-resume", URL: resume, Form: pages.QueuesFormID, Allowed: op, Need: components.RoleOperator,
		Enable: components.EnableState, State: "paused"}
	m.Delete = components.Action{ID: "act-delete", URL: del, Form: pages.QueuesFormID, Allowed: admin, Need: components.RoleAdmin,
		Enable: components.EnableAny, Danger: true, Confirm: pages.DeleteDialogID}
	m.DeleteDialog = components.Dialog{ID: pages.DeleteDialogID, ConfirmURL: del, Form: pages.QueuesFormID, Danger: true}
}

// handleQueueAction serves POST /c/{conn}/queues/{pause,resume,delete}
// with a queue= list. The route's wrapper has checked the role.
//
// With htmx it answers like a user request (the table shows the result)
// plus a toast per failed queue. Without JS it redirects back to the page
// (post, redirect, get), carrying any failures in a short-lived cookie
// that the page shows as toasts, so a reload never posts again. A delete
// needs confirm=1: the dialog sends it; without JS the first post answers
// with a confirmation page that does.
func handleQueueAction(d *deps, verb pages.QueueVerb) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		r.Body = http.MaxBytesReader(w, r.Body, maxActionForm)
		if err := r.ParseForm(); err != nil {
			plainError(w, http.StatusBadRequest)
			return
		}
		names := uniqueValues(r.PostForm["queue"])
		// Only the state of next is used; the path is always this
		// connection's page, so it cannot redirect elsewhere.
		v := defaultQueuesView()
		next, nextErr := url.Parse(r.PostForm.Get("next"))
		if nextErr == nil {
			v = parseQueuesView(next.Query())
		}
		back := d.queuesURL(a.conn.Name, v)
		// The Queue page's Pause/Resume button posts one queue with
		// from=queue: the answer is that page's.
		if from := r.PostForm.Get("from"); from == "queue" && len(names) == 1 && verb != pages.VerbDelete {
			qv := defaultQueueView()
			if nextErr == nil {
				qv = parseQueueView(next.Query())
			}
			d.queuePageAction(w, r, a, verb, names[0], qv)
			return
		}

		if verb == pages.VerbDelete && r.PostForm.Get("confirm") != "1" {
			switch {
			case isFragment(r):
				plainError(w, http.StatusBadRequest)
			case len(names) == 0:
				http.Redirect(w, r, back, http.StatusSeeOther) //nolint:gosec // G710: back is built by d.url for this connection.
			default:
				crumbs := []components.Crumb{{Text: a.conn.Name, URL: back}, {Label: pages.DeleteQueuesCrumb()}}
				l := d.layout(r, a, components.SectionQueues, crumbs)
				l.TopBar.Next = back
				d.render(w, r, http.StatusOK, pages.DeleteQueuesPage(pages.DeleteQueues{
					Layout: l, Names: names, Action: d.url("c", a.conn.Name, "queues", "delete"), Next: back, CancelURL: back,
				}))
			}
			return
		}

		failures := d.runQueueAction(r.Context(), a.conn.Name, verb, names)
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.QueuesActionResult(d.queuesModel(r, a, v, answer{}), failures))
			return
		}
		d.setFlash(w, a.conn.Name, failures)
		http.Redirect(w, r, back, http.StatusSeeOther) //nolint:gosec // G710: back is built by d.url for this connection.
	})
}

// uniqueValues drops blank and repeated form values, keeping the order.
func uniqueValues(in []string) []string {
	var out []string
	for _, q := range in {
		if strings.TrimSpace(q) != "" && !slices.Contains(out, q) {
			out = append(out, q)
		}
	}
	return out
}

// runQueueAction applies verb to each queue in turn and returns the
// failures. Pause skips queues already paused and Resume queues already
// running (the toolbar acts on the selection's matching queues), going by
// the connection's cached queue list; a queue it does not know is left to
// the source to reject.
//
// Cost: one Queues read (usually from the 1s cache the page just filled),
// then per queue one Inspector call, and for pause and resume one more
// SMEMBERS for conn's existence check. Since the list may be up to a
// second old, a queue paused meanwhile by someone else reaches asynq,
// which refuses it; that shows as a "Could not pause" toast with asynq's
// error text.
func (d *deps) runQueueAction(ctx context.Context, conn string, verb pages.QueueVerb, names []string) []pages.ActionFailure {
	state := map[string]bool{} // paused, by name
	if verb != pages.VerbDelete {
		if qs, err := d.conns.Queues(ctx, conn); err == nil {
			for _, q := range qs {
				state[q.Name] = q.Paused
			}
		}
	}
	var failures []pages.ActionFailure
	for _, q := range names {
		var err error
		switch verb {
		case pages.VerbPause:
			if paused, ok := state[q]; ok && paused {
				continue
			}
			err = d.conns.PauseQueue(ctx, conn, q)
		case pages.VerbResume:
			if paused, ok := state[q]; ok && !paused {
				continue
			}
			err = d.conns.ResumeQueue(ctx, conn, q)
		default:
			err = d.conns.DeleteQueue(ctx, conn, q)
		}
		if err == nil {
			continue
		}
		f := pages.ActionFailure{Verb: verb, Queue: q}
		switch {
		case errors.Is(err, ErrQueueNotFound):
			f.Kind = pages.FailureNotFound
		case errors.Is(err, ErrQueueNotEmpty):
			f.Kind = pages.FailureNotEmpty
		default:
			f.Detail = errText(err)
			d.logger.WarnContext(ctx, "queue action failed", slog.String("conn", conn), slog.String("queue", q),
				slog.String("verb", verbName(verb)), slog.Any("error", err))
		}
		failures = append(failures, f)
	}
	return failures
}

// verbName names a verb in logs.
func verbName(v pages.QueueVerb) string {
	switch v {
	case pages.VerbPause:
		return "pause"
	case pages.VerbResume:
		return "resume"
	}
	return "delete"
}
