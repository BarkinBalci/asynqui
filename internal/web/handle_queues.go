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
	"time"

	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/web/chart"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// latencyWarn is the queue latency the table highlights.
const latencyWarn = 30 * time.Second

// defaultPageBudget bounds how long a connection page waits for its data.
// A read that misses it renders "Checking"; it goes on in conn's cache, so
// the next poll usually has it.
const defaultPageBudget = 5 * time.Second

// maxActionForm bounds the body of a queue action post.
const maxActionForm = 64 << 10

// historyRanges are the chart's day ranges; defaultRange is the default.
var historyRanges = [...]int{7, 14, 30, 90}

const defaultRange = 14

// Chart geometry, in SVG user units; the SVG stretches to its box.
const (
	chartW = 1000
	chartH = 240
)

// sortParams are the ?sort= values, by column.
var sortParams = [pages.NumQueueColumns]string{
	pages.ColName:      "name",
	pages.ColState:     "state",
	pages.ColSize:      "size",
	pages.ColPending:   "pending",
	pages.ColActive:    "active",
	pages.ColScheduled: "scheduled",
	pages.ColRetry:     "retry",
	pages.ColArchived:  "archived",
	pages.ColLatency:   "latency",
	pages.ColMemory:    "memory",
	pages.ColProcessed: "processed",
	pages.ColFailed:    "failed",
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
	if i := slices.Index(sortParams[:], q.Get("sort")); i >= 0 {
		v.sort = pages.QueueColumn(i)
		v.desc = defaultDesc(v.sort)
	}
	switch q.Get("dir") {
	case "asc":
		v.desc = false
	case "desc":
		v.desc = true
	}
	if n, ok := strings.CutSuffix(q.Get("range"), "d"); ok {
		if days, err := strconv.Atoi(n); err == nil && slices.Contains(historyRanges[:], days) {
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
		q.Set("sort", sortParams[v.sort])
		q.Set("dir", map[bool]string{true: "desc", false: "asc"}[v.desc])
	}
	if v.days != defaultRange {
		q.Set("range", strconv.Itoa(v.days)+"d")
	}
	if v.queue != "" {
		q.Set("queue", v.queue)
	}
	return q
}

// pageURL is the connection page URL showing state v.
func (d *deps) queuesURL(conn string, v queuesView) string {
	return d.urlQuery(v.values(), "c", conn)
}

// handleQueues serves GET /c/{conn}, the Connection page. With HX-Request
// (a poll, a sort or a chart change) it answers with out-of-band swaps of
// every region instead (pages.QueuesPoll).
func handleQueues(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		m := d.queuesModel(r, a, parseQueuesView(r.URL.Query()))
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.QueuesPoll(m))
			return
		}
		d.render(w, r, http.StatusOK, pages.QueuesPage(m))
	})
}

// queuesModel reads the connection's queues and history within the page
// budget and builds the page model for state v.
func (d *deps) queuesModel(r *http.Request, a access, v queuesView) pages.Queues {
	name := a.conn.Name
	m := pages.Queues{
		Addr:    a.conn.Addr,
		Health:  healthView(a.conn.Health.State),
		FormURL: d.url("c", name, "queues", "pause"),
	}

	var rows []Queue
	switch a.conn.Health.State {
	case HealthDown:
		m.State, m.Error = pages.DataFailed, errText(a.conn.Health.Err)
	default:
		ctx, cancel := context.WithTimeout(r.Context(), d.pageBudget)
		defer cancel()
		qs, err := d.conns.Queues(ctx, name)
		switch {
		case err != nil && ctx.Err() != nil:
			m.State = pages.DataChecking
		case err != nil:
			// Reachable at the last PING but failing now: shown failed
			// here, in the badge and in the sidebar, as on the Overview.
			m.State, m.Error, m.Health = pages.DataFailed, errText(err), components.HealthDown
		default:
			rows = qs
			if v.queue != "" && !slices.ContainsFunc(rows, func(q Queue) bool { return q.Name == v.queue }) {
				v.queue = "" // deleted, or never there: all queues
			}
			m.Throughput = d.throughput(ctx, name, v, rows)
		}
	}

	var health map[string]components.Health
	if m.Health == components.HealthDown {
		health = map[string]components.Health{name: components.HealthDown}
	}
	crumbs := []components.Crumb{{Text: name, URL: d.url("c", name)}}
	m.Layout = d.layoutWith(r, a, components.SectionQueues, crumbs, d.visible(a.id), health)

	m.Next = d.queuesURL(name, v)
	m.PollURL = m.Next
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
		h := components.SortHead{ID: "sort-" + sortParams[c], Num: c >= pages.ColSize}
		if c == v.sort {
			h.Dir = map[bool]components.SortDir{true: components.SortDesc, false: components.SortAsc}[v.desc]
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

// throughput builds the chart card: the range links, the queue picker and
// the chart of v's queue and range.
func (d *deps) throughput(ctx context.Context, conn string, v queuesView, rows []Queue) pages.Throughput {
	t := pages.Throughput{Show: true, Queue: v.queue, FormURL: d.url("c", conn)}
	for _, q := range rows {
		t.Queues = append(t.Queues, q.Name)
	}
	for _, days := range historyRanges {
		next := v
		next.days = days
		t.Ranges = append(t.Ranges, pages.RangeLink{
			ID: "range-" + strconv.Itoa(days), Days: days, On: days == v.days, URL: d.queuesURL(conn, next),
		})
	}
	// The picker form submits queue; the rest of the state rides along.
	rest := v
	rest.queue = ""
	for k, vs := range rest.values() {
		t.Hidden = append(t.Hidden, pages.Param{Name: k, Value: vs[0]})
	}
	slices.SortFunc(t.Hidden, func(a, b pages.Param) int { return strings.Compare(a.Name, b.Name) })

	stats, err := d.conns.History(ctx, conn, v.queue, v.days)
	if err != nil {
		t.Error = errText(err)
		return t
	}
	t.Chart = buildChart(pages.ChartID, stats)
	return t
}

// buildChart lays out the daily counters, oldest first and ending today.
func buildChart(id string, stats []DailyStat) components.Chart {
	n := len(stats)
	plot := chart.Plot{Width: chartW, Height: chartH, N: n}
	processed, failed := make([]int64, n), make([]int64, n)
	var maxP, maxF int64
	peak := -1
	for i, s := range stats {
		processed[i], failed[i] = int64(s.Processed), int64(s.Failed)
		maxP = max(maxP, processed[i])
		if failed[i] > maxF {
			maxF, peak = failed[i], i
		}
	}
	ps, fs := chart.NiceScale(maxP), chart.NiceScale(maxF)
	c := components.Chart{
		ID:             id,
		ViewW:          chart.Num(chartW),
		ViewH:          chart.Num(chartH),
		ProcessedTicks: ps.Ticks(),
		FailedTicks:    fs.Ticks(),
		ProcessedLine:  plot.Line(processed, ps),
		ProcessedArea:  plot.Area(processed, ps),
		FailedLine:     plot.Line(failed, fs),
		Peak:           peak,
	}
	for _, v := range ps.Ticks()[1:] {
		c.GridY = append(c.GridY, chart.Num(plot.Y(v, ps)))
	}
	every := labelEvery(n)
	for i, s := range stats {
		back := n - 1 - i
		c.Days = append(c.Days, components.ChartDay{
			Date:      s.Date,
			Processed: processed[i],
			Failed:    failed[i],
			Today:     back == 0,
			Label:     back == 0 || (back%every == 0 && back >= every),
			BandX:     chart.Num(plot.SlotX(i)),
			BandW:     chart.Num(plot.SlotWidth()),
		})
	}
	return c
}

// labelEvery is how many days apart the x labels are, counted back from
// today, so each range shows about seven.
func labelEvery(days int) int {
	switch {
	case days <= 7:
		return 1
	case days <= 14:
		return 2
	case days <= 30:
		return 5
	}
	return max(days/6, 1)
}

// handleQueueAction serves POST /c/{conn}/queues/{pause,resume,delete}
// with a queue= list. The route's wrapper has checked the role.
//
// With htmx it answers like a poll (the table shows the result) plus a
// toast per failed queue. Without JS it redirects back to the page, or
// renders the page with the toasts when something failed. A delete needs
// confirm=1: the dialog sends it; without JS the first post answers with a
// confirmation page that does.
func handleQueueAction(d *deps, verb pages.QueueVerb) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		r.Body = http.MaxBytesReader(w, r.Body, maxActionForm)
		if err := r.ParseForm(); err != nil {
			plainError(w, http.StatusBadRequest)
			return
		}
		names := uniqueQueues(r.PostForm["queue"])
		// Only the state of next is used; the path is always this
		// connection's page, so it cannot redirect elsewhere.
		v := defaultQueuesView()
		if u, err := url.Parse(r.PostForm.Get("next")); err == nil {
			v = parseQueuesView(u.Query())
		}
		back := d.queuesURL(a.conn.Name, v)

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
		switch {
		case isFragment(r):
			d.render(w, r, http.StatusOK, pages.QueuesActionResult(d.queuesModel(r, a, v), failures))
		case len(failures) == 0:
			http.Redirect(w, r, back, http.StatusSeeOther) //nolint:gosec // G710: back is built by d.url for this connection.
		default:
			m := d.queuesModel(r, a, v)
			m.Layout.Toasts = pages.FailureToasts(failures)
			m.Layout.TopBar.Next = back
			d.render(w, r, http.StatusOK, pages.QueuesPage(m))
		}
	})
}

// uniqueQueues drops blank and repeated names, keeping the order.
func uniqueQueues(in []string) []string {
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
// running (the toolbar acts on the selection's matching queues), by the
// cached list; a queue it does not know is left to the source to reject.
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
