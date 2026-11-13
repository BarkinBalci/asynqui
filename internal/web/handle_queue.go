package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"

	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// Rows per page of the task tables; the first is the default.
func pageSizes() []int { return []int{20, 50, 100} }

// maxPage bounds ?page=, so page*size cannot overflow; a page past the
// end shows the last one anyway.
const maxPage = 1 << 20

// queueView is the Queue page's state, all of it in the URL query:
// ?state=&page=&size=. Defaults are left out of generated URLs.
type queueView struct {
	state TaskState
	page  int
	size  int
}

func defaultQueueView() queueView {
	return queueView{state: TaskPending, page: 1, size: pageSizes()[0]}
}

// parseQueueView reads the state from a query; unknown values fall back
// to the defaults.
func parseQueueView(q url.Values) queueView {
	v := defaultQueueView()
	if s, ok := parseState(q.Get("state")); ok {
		v.state = s
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 1 {
		v.page = min(n, maxPage)
	}
	if n, err := strconv.Atoi(q.Get("size")); err == nil && slices.Contains(pageSizes(), n) {
		v.size = n
	}
	return v
}

func (v queueView) values() url.Values {
	q := url.Values{}
	if v.state != TaskPending {
		q.Set("state", stateParam(v.state))
	}
	if v.page > 1 {
		q.Set("page", strconv.Itoa(v.page))
	}
	if v.size != pageSizes()[0] {
		q.Set("size", strconv.Itoa(v.size))
	}
	return q
}

// queueURL is the Queue page URL showing state v.
func (d *deps) queueURL(conn, queue string, v queueView) string {
	return d.urlQuery(v.values(), "c", conn, "queues", queue)
}

// queueAnswer is what a Queue page request is answered with.
type queueAnswer struct {
	answer
	// fresh renders the row checkboxes without hx-preserve: the answer
	// to an action, after which the selection clears.
	fresh  bool
	notice *pages.TaskNotice
}

func queueAnswerFor(r *http.Request) queueAnswer {
	if !isFragment(r) || r.Header.Get("HX-Trigger") != pages.QueuePollID {
		return queueAnswer{}
	}
	return queueAnswer{answer: answer{poll: true, init: r.URL.Query().Get("poll") == pollInit}}
}

// handleQueue serves GET /c/{conn}/queues/{queue}, the Queue page. With
// HX-Request (a poll, a tab, page or size change) it answers with
// out-of-band swaps instead (pages.QueuePoll).
func handleQueue(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		m := d.queueModel(r, a, r.PathValue("queue"), parseQueueView(r.URL.Query()), queueAnswerFor(r))
		status := http.StatusOK
		if m.NotFound {
			status = http.StatusNotFound
		}
		if isFragment(r) {
			// A queue deleted under an open page: the answer replaces the
			// page region with "Queue not found" (200, so htmx swaps it).
			d.render(w, r, http.StatusOK, pages.QueuePoll(m))
			return
		}
		if f, ok := d.takeFlash(w, r, a.conn.Name); ok {
			m.Layout.Toasts = pages.FlashToasts(f)
			if f.Notice != nil && !m.NotFound {
				m.Notice = f.Notice
			}
		}
		d.render(w, r, status, pages.QueuePage(m))
	})
}

// queueModel reads the queue's counters and the tab's tasks, in parallel
// and within the page budget, and builds the page model for state v.
func (d *deps) queueModel(r *http.Request, a access, queue string, v queueView, ans queueAnswer) pages.Queue {
	name := a.conn.Name
	m := pages.Queue{Name: queue, Poll: ans.poll, Init: ans.init, Notice: ans.notice}

	var (
		qi     QueueInfo
		tasks  []Task
		groups []Group
	)
	health := map[string]components.Health(nil)
	switch a.conn.Health.State {
	case HealthDown:
		m.State, m.Error, m.Down = pages.DataFailed, errText(a.conn.Health.Err), true
	default:
		ctx, cancel := context.WithTimeout(r.Context(), d.pageBudget)
		defer cancel()
		var (
			listErr error
			wg      sync.WaitGroup
		)
		list := func(page int) {
			if v.state == TaskAggregating {
				groups, listErr = d.conns.Groups(ctx, name, queue)
				return
			}
			tasks, listErr = d.conns.Tasks(ctx, name, queue, v.state, page, v.size)
		}
		wg.Go(func() { list(v.page) })
		var err error
		qi, err = d.conns.QueueInfo(ctx, name, queue)
		wg.Wait()
		if err == nil && listErr == nil && v.state != TaskAggregating && len(tasks) == 0 && v.page > 1 {
			// Past the end (tasks moved on since the link was made, or a
			// made-up page): the last page, or the first when there are
			// none.
			v.page = 1
			if total := qi.Count(v.state); total > 0 {
				v.page = (total + v.size - 1) / v.size
				list(v.page)
			}
		}
		err = firstErr(err, listErr)
		switch {
		case errors.Is(err, ErrQueueNotFound):
			m.NotFound = true
		case err != nil && ctx.Err() != nil:
			m.State = pages.DataChecking
		case err != nil:
			// Only a server that cannot be reached is down; an error
			// reply (NOPERM, LOADING) shows as itself.
			m.State, m.Error, m.Down = pages.DataFailed, errText(err), errors.Is(err, ErrUnreachable)
			if m.Down {
				health = map[string]components.Health{name: components.HealthDown}
			}
		}
	}
	m.KeepTable = ans.poll && !ans.init && m.State != pages.DataReady

	crumbs := []components.Crumb{{Text: name, URL: d.url("c", name)}, {Text: queue}}
	m.Layout = d.layoutWith(r, a, components.SectionQueues, crumbs, d.visible(a.id), health)
	if m.NotFound {
		return m
	}

	m.Tab = pageState(v.state)
	m.Next = d.queueURL(name, queue, v)
	m.FormURL = d.url("c", name, "queues", queue, "tasks", "archive")
	m.FindURL = d.url("c", name, "queues", queue, "find")
	m.Find = pages.Find{Action: m.FindURL, Queue: queue}
	pq := v.values()
	if m.State != pages.DataReady && !m.KeepTable {
		pq.Set("poll", pollInit) // the page will have no data regions
	}
	m.PollURL = d.urlQuery(pq, "c", name, "queues", queue)

	m.Paused = qi.Paused
	m.Stats = pages.QueueStats{
		Size:        int64(qi.Size),
		Processed:   int64(qi.ProcessedToday),
		Failed:      int64(qi.FailedToday),
		Memory:      qi.MemoryUsage,
		Latency:     qi.Latency,
		LatencyHigh: qi.Latency > latencyWarn,
	}
	toggle := "pause"
	if qi.Paused {
		toggle = "resume"
	}
	m.Toggle = pages.QueueToggle{
		URL: d.url("c", name, "queues", toggle), Allowed: a.role >= authz.RoleOperator, Queue: queue, Next: m.Next,
	}
	for _, s := range []TaskState{TaskActive, TaskPending, TaskAggregating, TaskScheduled, TaskRetry, TaskArchived, TaskCompleted} {
		tv := queueView{state: s, page: 1, size: v.size}
		m.Tabs = append(m.Tabs, pages.Tab{State: pageState(s), Count: int64(qi.Count(s)), URL: d.queueURL(name, queue, tv), On: s == v.state})
	}
	m.Total = int64(qi.Count(v.state))
	for _, g := range groups {
		m.Groups = append(m.Groups, pages.GroupRow{Name: g.Name, Size: int64(g.Size)})
	}
	d.taskRows(&m, name, queue, tasks, ans.fresh)
	d.taskActions(&m, a, queue, v.state)
	d.pager(&m, name, queue, v, len(tasks))
	return m
}

// firstErr is the first non-nil error.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *deps) taskRows(m *pages.Queue, conn, queue string, tasks []Task, fresh bool) {
	now := d.now()
	for _, t := range tasks {
		row := pages.TaskRow{
			ID:     t.ID,
			URL:    d.url("c", conn, "queues", queue, "tasks", t.ID),
			LinkID: components.HexID("tlink-", t.ID),
			Select: components.RowSelect{
				ID: components.HexID("tsel-", t.ID), Form: pages.TasksFormID, Name: "id", Value: t.ID, Fresh: fresh,
			},
			Type:          t.Type,
			Payload:       t.Payload,
			Retried:       t.Retried,
			LastErr:       t.LastErr,
			LastFailedAt:  t.LastFailedAt,
			NextProcessAt: t.NextProcessAt,
			CompletedAt:   t.CompletedAt,
			Started:       t.Started,
		}
		if !t.CompletedAt.IsZero() {
			row.KeptUntil = t.CompletedAt.Add(t.Retention)
		}
		if !t.Started.IsZero() {
			row.RunningFor = now.Sub(t.Started)
		}
		m.Rows = append(m.Rows, row)
	}
}

// taskActions fills the toolbar buttons and their dialogs for the state
// and the viewer's role.
func (d *deps) taskActions(m *pages.Queue, a access, queue string, s TaskState) {
	for _, verb := range tabVerbs(s) {
		act := d.verbAction(a, queue, verb, pages.TasksFormID)
		act.ID = "act-" + verbParam(verb)
		if verb == TaskDelete {
			act.Confirm = pages.TasksDeleteDialogID
			m.DeleteDialog = components.Dialog{ID: pages.TasksDeleteDialogID, ConfirmURL: act.URL, Form: pages.TasksFormID, Danger: true}
		}
		if allowsAll(s, verb) {
			act.ConfirmAll = pages.AllDialogID(pageVerb(verb))
			m.AllDialogs = append(m.AllDialogs, pages.AllDialog{Verb: pageVerb(verb), Dialog: components.Dialog{
				ID: pages.AllDialogID(pageVerb(verb)), ConfirmURL: act.URL, Form: pages.TasksFormID, Danger: verb == TaskDelete,
			}})
			m.AllowAll = true
		}
		m.Actions = append(m.Actions, pages.TaskActionButton{Verb: pageVerb(verb), Action: act})
	}
}

// verbAction is the button posting verb to the task action route, for
// the viewer's role: Delete needs admin, the others operator.
func (d *deps) verbAction(a access, queue string, verb TaskVerb, form string) components.Action {
	need, role := components.RoleOperator, authz.RoleOperator
	if verb == TaskDelete {
		need, role = components.RoleAdmin, authz.RoleAdmin
	}
	return components.Action{
		URL:     d.url("c", a.conn.Name, "queues", queue, "tasks", verbParam(verb)),
		Form:    form,
		Allowed: a.role >= role,
		Need:    need,
		Enable:  components.EnableAny,
		Danger:  verb == TaskDelete,
		Primary: verb == TaskRun,
	}
}

// pager fills the footer: the range shown, the page links and the
// rows-per-page picker.
func (d *deps) pager(m *pages.Queue, conn, queue string, v queueView, rows int) {
	p := pages.Pager{Total: m.Total}
	if rows > 0 {
		p.From = int64((v.page-1)*v.size + 1)
		p.To = p.From + int64(rows) - 1
	}
	if v.page > 1 {
		prev := v
		prev.page--
		p.PrevURL = d.queueURL(conn, queue, prev)
	}
	if int64(v.page*v.size) < m.Total {
		next := v
		next.page++
		p.NextURL = d.queueURL(conn, queue, next)
	}
	m.Pager = p
	s := pages.SizePicker{FormURL: d.url("c", conn, "queues", queue), Size: v.size, Choices: pageSizes()}
	if v.state != TaskPending {
		s.Hidden = []pages.Param{{Name: "state", Value: stateParam(v.state)}}
	}
	m.Sizes = s
}

// queuePageAction runs the Queue page's Pause or Resume (posted to the
// Connection page's queue action with from=queue) and answers for the
// Queue page: with htmx its user-request answer plus failure toasts,
// without JS a redirect back to it. The M5a semantics stand: Pause skips
// a queue already paused and Resume a running one.
func (d *deps) queuePageAction(w http.ResponseWriter, r *http.Request, a access, verb pages.QueueVerb, queue string, v queueView) {
	failures := d.runQueueAction(r.Context(), a.conn.Name, verb, []string{queue})
	if isFragment(r) {
		m := d.queueModel(r, a, queue, v, queueAnswer{})
		d.render(w, r, http.StatusOK, pages.QueueActionResult(m, failures, nil))
		return
	}
	d.setFlash(w, a.conn.Name, failures)
	http.Redirect(w, r, d.queueURL(a.conn.Name, queue, v), http.StatusSeeOther) //nolint:gosec // G710: built by d.url for this connection.
}
