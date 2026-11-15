package web

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"

	"github.com/a-h/templ"

	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// Rows per page of the task tables; the first is the default.
func pageSizes() []int { return []int{20, 50, 100} }

// maxPage bounds ?page=, so page*size cannot overflow; a page past the
// end shows the last one anyway.
const maxPage = 1 << 20

// queueView is the Queue page's state, all of it in the URL query:
// ?state=&group=&dir=&page=&size=. Defaults are left out of generated
// URLs.
type queueView struct {
	state conn.TaskState
	// group is the aggregation group whose tasks the Aggregating tab
	// lists; empty lists the groups.
	group string
	// desc lists latest first (sortable states only); its default
	// depends on the state (taskDefaultDesc).
	desc bool
	page int
	size int
}

func defaultQueueView() queueView {
	return queueView{state: conn.TaskPending, page: 1, size: pageSizes()[0]}
}

// tabView is the first page of state s, in its default order.
func tabView(s conn.TaskState, size int) queueView {
	return queueView{state: s, desc: taskDefaultDesc(s), page: 1, size: size}
}

// parseQueueView reads the state from a query; unknown values fall back
// to the defaults.
func parseQueueView(q url.Values) queueView {
	v := defaultQueueView()
	if s, ok := pages.ParseState(q.Get("state")); ok {
		v.state = conn.TaskState(s)
	}
	v.desc = taskDefaultDesc(v.state)
	if sortable(v.state) {
		switch q.Get("dir") {
		case "asc":
			v.desc = false
		case "desc":
			v.desc = true
		}
	}
	if v.state == conn.TaskAggregating {
		v.group = q.Get("group")
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
	if v.state != conn.TaskPending {
		q.Set("state", pages.StateParam(pages.TaskState(v.state)))
	}
	if v.state == conn.TaskAggregating && v.group != "" {
		q.Set("group", v.group)
	}
	if sortable(v.state) && v.desc != taskDefaultDesc(v.state) {
		dir := "asc"
		if v.desc {
			dir = "desc"
		}
		q.Set("dir", dir)
	}
	if v.page > 1 {
		q.Set("page", strconv.Itoa(v.page))
	}
	if v.size != pageSizes()[0] {
		q.Set("size", strconv.Itoa(v.size))
	}
	return q
}

// groupList reports whether the view lists the Aggregating tab's groups.
func (v queueView) groupList() bool { return v.state == conn.TaskAggregating && v.group == "" }

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

// handleQueue serves GET /c/{conn}/queues/{queue}, the Queue page. With
// HX-Request (a poll, a tab, page or size change) it answers with
// out-of-band swaps instead (pages.QueuePoll).
func handleQueue(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		m := d.queueModel(r, a, r.PathValue("queue"), parseQueueView(r.URL.Query()), queueAnswer{answer: liveAnswer(r, pages.QueuePollID)})
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
		if f, ok := d.takeFlash(w, r, a.conn.Name()); ok {
			m.Layout.Toasts = pages.FlashToasts(f)
			m.Notice = f.Notice
		}
		d.render(w, r, status, pages.QueuePage(m))
	})
}

// queueData is what the Queue page reads: the queue's counters, the tab's
// tasks or groups, and the page it shows.
type queueData struct {
	qi     conn.QueueInfo
	tasks  []conn.Task
	groups []conn.Group
	page   int
}

// total is the number of tasks view v pages over: the state's, or the
// group's.
func (q queueData) total(v queueView) int {
	if v.state != conn.TaskAggregating || v.groupList() {
		return q.qi.Count(v.state)
	}
	for _, g := range q.groups {
		if g.Name == v.group {
			return g.Size
		}
	}
	return 0
}

// readQueue reads the queue's counters and the tab's tasks (or groups) in
// parallel. A page past the end (tasks moved on since the link was made,
// or a made-up page) reads the last page instead, or the first when there
// are none.
func readQueue(ctx context.Context, c Conn, queue string, v queueView) (queueData, error) {
	q := queueData{page: v.page}
	var listErr, groupsErr error
	list := func() {
		switch {
		case v.groupList():
		case v.state == conn.TaskAggregating:
			q.tasks, listErr = c.ListGroupTasks(ctx, queue, v.group, q.page, v.size)
		default:
			q.tasks, listErr = c.ListTasks(ctx, queue, v.state, q.page, v.size, v.desc)
		}
	}
	var wg sync.WaitGroup
	wg.Go(list)
	if v.state == conn.TaskAggregating {
		// The group list, or the group's size for the pager.
		wg.Go(func() { q.groups, groupsErr = c.Groups(ctx, queue) })
	}
	var err error
	q.qi, err = c.QueueInfo(ctx, queue)
	wg.Wait()
	if err := cmp.Or(err, listErr, groupsErr); err != nil {
		return q, err
	}
	if !v.groupList() && len(q.tasks) == 0 && q.page > 1 {
		q.page = 1
		if total := q.total(v); total > 0 {
			q.page = (total + v.size - 1) / v.size
			list()
		}
	}
	return q, listErr
}

// queueModel reads the queue within the page budget and builds the page
// model for state v.
func (d *deps) queueModel(r *http.Request, a access, queue string, v queueView, ans queueAnswer) pages.Queue {
	name := a.conn.Name()
	ctx, cancel := d.pageContext(r.Context())
	defer cancel()
	q, rd := readLive(ctx, a, func(ctx context.Context) (queueData, error) { return readQueue(ctx, a.conn, queue, v) })
	m := pages.Queue{Name: queue, Notice: ans.notice, NotFound: readyIfMissing(&rd, conn.ErrQueueNotFound)}
	m.Layout = d.liveLayout(r, a, components.SectionQueues, rd, components.Crumb{Text: queue})
	if m.NotFound {
		return m
	}
	if rd.state == pages.DataReady {
		v.page = q.page
	}
	m.Live = d.live(ans.answer, pages.QueuePageID, pages.QueuePollID, rd, rd.state == pages.DataReady, v.values(), "c", name, "queues", queue)
	m.Live.BannerID = pages.QueueBannerID
	qi, groups, tasks, total := q.qi, q.groups, q.tasks, q.total(v)

	m.Tab = pages.TaskState(v.state)
	m.Group = v.group
	m.GroupsURL = d.queueURL(name, queue, tabView(conn.TaskAggregating, v.size))
	m.Next = d.queueURL(name, queue, v)
	m.FormURL = d.url("c", name, "queues", queue, "tasks", "archive")
	m.FindURL = d.url("c", name, "queues", queue, "find")
	m.Find = pages.Find{Action: m.FindURL, Queue: queue}

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
	for _, p := range pages.TaskStates() {
		s := conn.TaskState(p)
		m.Tabs = append(m.Tabs, pages.Tab{State: p, Count: int64(qi.Count(s)), URL: d.queueURL(name, queue, tabView(s, v.size)), On: s == v.state})
	}
	m.Total = int64(total)
	if v.groupList() {
		for _, g := range groups {
			gv := tabView(conn.TaskAggregating, v.size)
			gv.group = g.Name
			m.Groups = append(m.Groups, pages.GroupRow{
				Name:   g.Name,
				Size:   int64(g.Size),
				URL:    d.queueURL(name, queue, gv),
				LinkID: components.HexID("glink-", g.Name),
				Select: components.RowSelect{
					ID: components.HexID("gsel-", g.Name), Form: pages.TasksFormID, Name: "group", Value: g.Name, Fresh: ans.fresh,
				},
			})
		}
	}
	if sortable(v.state) {
		// Flipping the order starts again at the first page.
		flip := v
		flip.desc, flip.page = !v.desc, 1
		dir := components.SortAsc
		if v.desc {
			dir = components.SortDesc
		}
		m.Sort = components.SortHead{ID: pages.TasksSortID, URL: d.queueURL(name, queue, flip), Dir: dir, Num: true}
	}
	d.taskRows(&m, name, queue, tasks, ans.fresh)
	d.taskActions(&m, a, queue, v)
	d.pager(&m, name, queue, v, len(tasks))
	return m
}

func (d *deps) taskRows(m *pages.Queue, name, queue string, tasks []conn.Task, fresh bool) {
	now := d.now()
	for _, t := range tasks {
		row := pages.TaskRow{
			ID:     t.ID,
			URL:    d.url("c", name, "queues", queue, "tasks", t.ID),
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
			ArchivedAt:    t.ArchivedAt,
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

// taskActions fills the toolbar buttons and their dialogs for the view and the
// viewer's role.
func (d *deps) taskActions(m *pages.Queue, a access, queue string, v queueView) {
	if v.groupList() {
		d.groupActions(m, a, queue)
		return
	}
	s := v.state
	for _, verb := range tabVerbs(s) {
		act := d.verbAction(a, queue, verb, pages.TasksFormID)
		pv := pages.TaskVerb(verb)
		act.ID = "act-" + pages.VerbParam(pv)
		switch verb {
		case conn.TaskDelete:
			act.Confirm = pages.TasksDeleteDialogID
			m.DeleteDialog = components.Dialog{ID: pages.TasksDeleteDialogID, ConfirmURL: act.URL, Form: pages.TasksFormID, Danger: true}
		case conn.TaskArchive:
			act.Confirm = pages.TasksArchiveDialogID
			m.ArchiveDialog = components.Dialog{ID: pages.TasksArchiveDialogID, ConfirmURL: act.URL, Form: pages.TasksFormID, Danger: true}
		}
		if allowsAll(s, verb) {
			act.ConfirmAll = pages.AllDialogID(pv)
			m.AllDialogs = append(m.AllDialogs, pages.AllDialog{Verb: pv, Dialog: components.Dialog{
				ID: act.ConfirmAll, ConfirmURL: act.URL, Form: pages.TasksFormID, Danger: act.Danger,
			}})
			m.AllowAll = true
		}
		m.Actions = append(m.Actions, pages.TaskActionButton{Verb: pv, Action: act})
	}
}

// groupActions fills the Aggregating tab's group list toolbar, each action
// confirmed in a dialog.
func (d *deps) groupActions(m *pages.Queue, a access, queue string) {
	for _, verb := range tabVerbs(conn.TaskAggregating) {
		act := d.verbAction(a, queue, verb, pages.TasksFormID)
		pv := pages.TaskVerb(verb)
		act.ID = "act-all-" + pages.VerbParam(pv)
		act.Confirm = pages.GroupDialogID(pv)
		m.AllDialogs = append(m.AllDialogs, pages.AllDialog{Verb: pv, Dialog: components.Dialog{
			ID: act.Confirm, ConfirmURL: act.URL, Form: pages.TasksFormID, Danger: act.Danger,
		}})
		m.Actions = append(m.Actions, pages.TaskActionButton{Verb: pv, Action: act, All: true})
	}
}

// verbAction is the button posting verb to the task action route, for the
// viewer's role.
func (d *deps) verbAction(a access, queue string, verb conn.TaskVerb, form string) components.Action {
	need, role := components.RoleOperator, authz.RoleOperator
	if verb == conn.TaskDelete {
		need, role = components.RoleAdmin, authz.RoleAdmin
	}
	return components.Action{
		URL:     d.url("c", a.conn.Name(), "queues", queue, "tasks", pages.VerbParam(pages.TaskVerb(verb))),
		Form:    form,
		Allowed: a.role >= role,
		Need:    need,
		Enable:  components.EnableAny,
		Danger:  verb == conn.TaskDelete || verb == conn.TaskArchive,
		Primary: verb == conn.TaskRun,
	}
}

// pager fills the footer: the range shown, the page links and the
// rows-per-page picker.
func (d *deps) pager(m *pages.Queue, name, queue string, v queueView, rows int) {
	p := pages.Pager{Total: m.Total}
	if rows > 0 {
		p.From = int64((v.page-1)*v.size + 1)
		p.To = p.From + int64(rows) - 1
	}
	if v.page > 1 {
		prev := v
		prev.page--
		p.PrevURL = d.queueURL(name, queue, prev)
	}
	if int64(v.page*v.size) < m.Total {
		next := v
		next.page++
		p.NextURL = d.queueURL(name, queue, next)
	}
	m.Pager = p
	s := pages.SizePicker{FormURL: d.url("c", name, "queues", queue), Size: v.size, Choices: pageSizes()}
	q := v.values()
	for _, k := range []string{"state", "group", "dir"} {
		if q.Has(k) {
			s.Hidden = append(s.Hidden, pages.Param{Name: k, Value: q.Get(k)})
		}
	}
	m.Sizes = s
}

// queuePageAction runs the Queue page's Pause or Resume (posted to the
// Connection page's queue action with from=queue) and answers for the Queue
// page: with htmx its user-request answer plus failure toasts, without JS a
// redirect back to it.
func (d *deps) queuePageAction(w http.ResponseWriter, r *http.Request, a access, verb pages.QueueVerb, queue string, v queueView) {
	failures := d.runQueueAction(r.Context(), a.conn, verb, []string{queue})
	d.answerAction(w, r, a.conn.Name(), func() templ.Component {
		return pages.QueueActionResult(d.queueModel(r, a, queue, v, queueAnswer{}), failures, nil)
	}, pages.Flash{Failures: failures}, d.queueURL(a.conn.Name(), queue, v))
}
