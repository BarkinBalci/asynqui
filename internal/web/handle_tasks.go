package web

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
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

// maxTaskIDs bounds the selected tasks one action posts: the largest
// page, with room to spare.
const maxTaskIDs = 500

// taskView is the Task page's state in the URL query: ?payload=raw for
// the raw payload view.
type taskView struct {
	raw bool
}

func parseTaskView(q url.Values) taskView { return taskView{raw: q.Get("payload") == "raw"} }

func (v taskView) values() url.Values {
	q := url.Values{}
	if v.raw {
		q.Set("payload", "raw")
	}
	return q
}

func (d *deps) taskURL(conn, queue, id string, v taskView) string {
	return d.urlQuery(v.values(), "c", conn, "queues", queue, "tasks", id)
}

// taskAnswer is what a Task page request is answered with.
type taskAnswer struct {
	answer
	// seen is what the polling page shows (?seen=state.version, see
	// taskVersion): a poll replaces the cards only when the task changed,
	// and the actions only when its state changed.
	seen string
}

func taskAnswerFor(r *http.Request) taskAnswer {
	if !isFragment(r) || r.Header.Get("HX-Trigger") != pages.TaskPollID {
		return taskAnswer{}
	}
	q := r.URL.Query()
	return taskAnswer{answer: answer{poll: true, init: q.Get("poll") == pollInit}, seen: q.Get("seen")}
}

// handleTask serves GET /c/{conn}/queues/{queue}/tasks/{id}, the Task
// page. With HX-Request it answers with out-of-band swaps
// (pages.TaskPoll). A task or queue that does not exist is "Task not
// found" (404 for the full page).
func handleTask(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		m := d.taskModel(r, a, r.PathValue("queue"), r.PathValue("id"), parseTaskView(r.URL.Query()), taskAnswerFor(r))
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.TaskPoll(m))
			return
		}
		status := http.StatusOK
		if m.NotFound {
			status = http.StatusNotFound
		}
		if f, ok := d.takeFlash(w, r, a.conn.Name()); ok {
			m.Layout.Toasts = pages.FlashToasts(f)
		}
		d.render(w, r, status, pages.TaskPage(m))
	})
}

// taskModel reads the task within the page budget and builds the page.
func (d *deps) taskModel(r *http.Request, a access, queue, id string, v taskView, ans taskAnswer) pages.Task {
	name, health := a.conn.Name(), a.conn.Health()
	m := pages.Task{Queue: queue, ID: id, Poll: ans.poll, Init: ans.init}
	var (
		t        conn.Task
		override map[string]components.Health
	)
	switch health.State {
	case conn.StateDown:
		m.State, m.Error, m.Down = pages.DataFailed, errText(health.Err), true
	default:
		ctx, cancel := context.WithTimeout(r.Context(), d.pageBudget)
		defer cancel()
		var err error
		t, err = a.conn.Task(ctx, queue, id)
		switch {
		case errors.Is(err, conn.ErrTaskNotFound), errors.Is(err, conn.ErrQueueNotFound):
			m.NotFound = true
		case err != nil && ctx.Err() != nil:
			m.State = pages.DataChecking
		case err != nil:
			// Only a server that cannot be reached is down; an error
			// reply (NOPERM, LOADING) shows as itself.
			m.State, m.Error, m.Down = pages.DataFailed, errText(err), conn.Unreachable(err)
			if m.Down {
				override = map[string]components.Health{name: components.HealthDown}
			}
		}
	}
	m.KeepPage = ans.poll && !ans.init && !m.NotFound && m.State != pages.DataReady

	queueURL := d.url("c", name, "queues", queue)
	crumbs := []components.Crumb{{Text: name, URL: d.url("c", name)}, {Text: queue, URL: queueURL}}
	if m.State == pages.DataReady && !m.NotFound {
		tv := tabView(t.State, pageSizes()[0])
		if t.State == conn.TaskAggregating {
			tv.group = t.Group
		}
		tab := d.queueURL(name, queue, tv)
		m.TabURL = tab
		crumbs = append(crumbs, components.Crumb{Label: pages.StateName(pages.TaskState(t.State)), URL: tab})
	}
	crumbs = append(crumbs, components.Crumb{Text: format.ShortID(format.VisibleID(id)), Mono: true, Title: id})
	m.Layout = d.layoutWith(r, a, components.SectionQueues, crumbs, d.visible(a.id), override)
	m.QueueURL = queueURL

	pq := v.values()
	if m.State != pages.DataReady && !m.KeepPage {
		pq.Set("poll", pollInit)
	}
	if m.State != pages.DataReady || m.NotFound {
		m.PollURL = d.urlQuery(pq, "c", name, "queues", queue, "tasks", id)
		return m
	}
	state := pages.StateParam(pages.TaskState(t.State))
	seen := state + "." + taskVersion(t)
	pq.Set("seen", seen)
	m.PollURL = d.urlQuery(pq, "c", name, "queues", queue, "tasks", id)
	seenState, _, _ := strings.Cut(ans.seen, ".")
	m.Changed = ans.poll && ans.seen != seen
	m.StateChanged = ans.poll && seenState != state

	m.Type, m.Status = t.Type, pages.TaskState(t.State)
	// A poll answer has no payload card, and has the result card only
	// when the task changed: skip the formatting otherwise.
	if !ans.poll || ans.init {
		if pretty, ok := format.PrettyJSON(t.Payload); ok {
			m.PayloadJSON = true
			m.Payload = pretty
			m.JSONURL = d.taskURL(name, queue, id, taskView{})
			m.RawURL = d.taskURL(name, queue, id, taskView{raw: true})
		}
		if !m.PayloadJSON || v.raw {
			m.Raw = m.PayloadJSON
			m.Payload = format.Text(t.Payload)
		}
	}
	if len(t.Result) > 0 {
		m.HasResult = true
		if !ans.poll || ans.init || m.Changed {
			m.Result = format.Text(t.Result)
			if pretty, ok := format.PrettyJSON(t.Result); ok {
				m.Result = pretty
			}
		}
	}
	m.LastErr, m.LastFailedAt = t.LastErr, t.LastFailedAt
	m.Attempt = attempt(t)
	m.Retried, m.MaxRetry = t.Retried, t.MaxRetry
	m.NextProcessAt, m.Timeout, m.Deadline, m.Group = t.NextProcessAt, t.Timeout, t.Deadline, t.Group
	m.CompletedAt, m.Orphaned = t.CompletedAt, t.IsOrphaned
	if !t.CompletedAt.IsZero() {
		m.KeptUntil = t.CompletedAt.Add(t.Retention)
	}
	keys := make([]string, 0, len(t.Headers))
	for k := range t.Headers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		m.Headers = append(m.Headers, pages.Param{Name: k, Value: t.Headers[k]})
	}

	m.FormURL = d.url("c", name, "queues", queue, "tasks", "run")
	for _, verb := range tabVerbs(t.State) {
		act := d.verbAction(a, queue, verb, pages.TaskFormID)
		act.ID, act.Large, act.Conn = "task-act-"+pages.VerbParam(pages.TaskVerb(verb)), true, name
		switch verb {
		case conn.TaskDelete:
			act.Confirm = pages.TaskDeleteDialogID
			m.DeleteDialog = components.Dialog{ID: pages.TaskDeleteDialogID, ConfirmURL: act.URL, Form: pages.TaskFormID, Danger: true}
		case conn.TaskArchive:
			act.Confirm = pages.TaskArchiveDialogID
			m.ArchiveDialog = components.Dialog{ID: pages.TaskArchiveDialogID, ConfirmURL: act.URL, Form: pages.TaskFormID, Danger: true}
		}
		m.Actions = append(m.Actions, pages.TaskActionButton{Verb: pages.TaskVerb(verb), Action: act})
	}
	m.Hidden = []pages.Param{
		{Name: "id", Value: id},
		{Name: "state", Value: state},
		{Name: "from", Value: "task"},
		{Name: "next", Value: d.taskURL(name, queue, id, v)},
	}
	return m
}

// handleFindTask serves GET /c/{conn}/queues/{queue}/find?id=, the Find
// task lookup (GetTaskInfo of an exact ID in this queue). A match
// redirects to the Task page (HX-Redirect for the dialog). A miss answers
// the form again with "Task not found": the dialog's body for htmx, the
// Find task page without JS.
func handleFindTask(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		name, queue := a.conn.Name(), r.PathValue("queue")
		f := pages.Find{Action: d.url("c", name, "queues", queue, "find"), Queue: queue, ID: strings.TrimSpace(r.URL.Query().Get("id"))}
		if f.ID != "" {
			ctx, cancel := context.WithTimeout(r.Context(), d.pageBudget)
			_, err := a.conn.Task(ctx, queue, f.ID)
			cancel()
			switch {
			case err == nil:
				target := d.url("c", name, "queues", queue, "tasks", f.ID)
				if isFragment(r) {
					w.Header().Set("HX-Redirect", target)
					w.Header().Set("Cache-Control", "no-store")
					w.WriteHeader(http.StatusOK)
					return
				}
				http.Redirect(w, r, target, http.StatusSeeOther) //nolint:gosec // G710: built by d.url for this connection.
				return
			case errors.Is(err, conn.ErrTaskNotFound), errors.Is(err, conn.ErrQueueNotFound):
				f.Missed = true
			default:
				f.Failed, f.Down = errText(err), conn.Unreachable(err)
			}
		}
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.FindBody(f, ""))
			return
		}
		queueURL := d.url("c", name, "queues", queue)
		crumbs := []components.Crumb{{Text: name, URL: d.url("c", name)}, {Text: queue, URL: queueURL}, {Label: pages.FindCrumb()}}
		d.render(w, r, http.StatusOK, pages.FindTaskPage(pages.FindPage{
			Layout: d.layout(r, a, components.SectionQueues, crumbs), Find: f, QueueURL: queueURL,
		}))
	})
}

// taskForm is a parsed task action post.
type taskForm struct {
	ids []string
	// all applies the verb to every task of state; of each of groups for
	// aggregating tasks.
	all     bool
	groups  []string
	state   conn.TaskState
	stateOK bool
	confirm bool
	// fromTask: posted by the Task page for its one task.
	fromTask bool
	// queueView and taskView are the pages to answer with, from next.
	queueView queueView
	taskView  taskView
}

func parseTaskForm(r *http.Request) taskForm {
	f := taskForm{
		ids:       uniqueValues(r.PostForm["id"]),
		groups:    uniqueValues(r.PostForm["group"]),
		all:       r.PostForm.Get("all") == "1",
		confirm:   r.PostForm.Get("confirm") == "1",
		queueView: defaultQueueView(),
	}
	s, ok := pages.ParseState(r.PostForm.Get("state"))
	f.state, f.stateOK = conn.TaskState(s), ok
	f.fromTask = r.PostForm.Get("from") == "task" && len(f.ids) == 1 && !f.all
	// Only the state of next is used; the answer is always this queue's
	// page (or its task's), so it cannot redirect elsewhere.
	if u, err := url.Parse(r.PostForm.Get("next")); err == nil {
		f.queueView = parseQueueView(u.Query())
		f.taskView = parseTaskView(u.Query())
	}
	return f
}

// handleTaskAction serves POST /c/{conn}/queues/{queue}/tasks/{verb}: the
// selected tasks (id=…, one Inspector call each) or, with all=1&state=,
// every task of the state (one *All* call); for aggregating tasks, with
// all=1&state=aggregating&group=…, every task of each group (one
// *AllAggregatingTasks call each). The route's wrapper has checked the
// role. Delete, Archive and "all" need confirm=1: the dialogs send it;
// without JS the first post answers with a confirmation page.
//
// With htmx it answers like a user request of the page it came from (the
// Queue page with a result notice and the selection cleared, or the Task
// page), plus a toast per failure; a task deleted from its own page
// redirects to the queue (HX-Redirect). Without JS it redirects back,
// carrying the notice and failures in the flash cookie.
func handleTaskAction(d *deps, verb conn.TaskVerb) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		name, queue := a.conn.Name(), r.PathValue("queue")
		r.Body = http.MaxBytesReader(w, r.Body, maxActionForm)
		if err := r.ParseForm(); err != nil {
			plainError(w, http.StatusBadRequest)
			return
		}
		if len(r.PostForm["id"]) > maxTaskIDs || len(r.PostForm["group"]) > maxTaskIDs {
			plainError(w, http.StatusRequestEntityTooLarge)
			return
		}
		f := parseTaskForm(r)
		if f.all {
			f.ids = nil
			if !f.stateOK || !allowsAll(f.state, verb) {
				plainError(w, http.StatusBadRequest)
				return
			}
		}
		if !f.all || f.state != conn.TaskAggregating {
			f.groups = nil
		}
		back := d.queueURL(name, queue, f.queueView)
		if f.fromTask {
			back = d.taskURL(name, queue, f.ids[0], f.taskView)
		}
		if (!f.all && len(f.ids) == 0) || (f.all && f.state == conn.TaskAggregating && len(f.groups) == 0) {
			if isFragment(r) {
				plainError(w, http.StatusBadRequest)
				return
			}
			http.Redirect(w, r, back, http.StatusSeeOther) //nolint:gosec // G710: built by d.url for this connection.
			return
		}
		if (verb == conn.TaskDelete || verb == conn.TaskArchive || f.all) && !f.confirm {
			if isFragment(r) {
				plainError(w, http.StatusBadRequest)
				return
			}
			d.confirmTasks(w, r, a, queue, verb, f, back)
			return
		}

		// A task deleted from its own page goes back to its tab: the state
		// it is in now, not the one the page showed.
		tabState, tabGroup := conn.TaskPending, ""
		if f.stateOK {
			tabState = f.state
		}
		if f.fromTask && verb == conn.TaskDelete {
			ctx, cancel := context.WithTimeout(r.Context(), d.pageBudget)
			if t, err := a.conn.Task(ctx, queue, f.ids[0]); err == nil && t.State != 0 {
				tabState, tabGroup = t.State, t.Group
			}
			cancel()
		}
		notice, failures := d.runTaskAction(r.Context(), a.conn, queue, verb, f)
		switch {
		case f.fromTask && verb == conn.TaskDelete && len(failures) == 0:
			tab := tabView(tabState, pageSizes()[0])
			if tabState == conn.TaskAggregating {
				tab.group = tabGroup
			}
			target := d.queueURL(name, queue, tab)
			d.setFlashAll(w, name, pages.Flash{Notice: notice})
			if isFragment(r) {
				w.Header().Set("HX-Redirect", target)
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Redirect(w, r, target, http.StatusSeeOther) //nolint:gosec // G710: built by d.url for this connection.
		case isFragment(r) && f.fromTask:
			m := d.taskModel(r, a, queue, f.ids[0], f.taskView, taskAnswer{})
			d.render(w, r, http.StatusOK, pages.TaskActionResult(m, failures))
		case isFragment(r):
			m := d.queueModel(r, a, queue, f.queueView, queueAnswer{fresh: true, notice: notice})
			d.render(w, r, http.StatusOK, pages.QueueActionResult(m, nil, failures))
		default:
			fl := pages.Flash{TaskFailures: failures}
			if !f.fromTask {
				fl.Notice = notice
			}
			d.setFlashAll(w, name, fl)
			http.Redirect(w, r, back, http.StatusSeeOther) //nolint:gosec // G710: built by d.url for this connection.
		}
	})
}

// confirmTasks answers a delete or "all" action posted without JS with a
// confirmation page that posts it again with confirm=1.
func (d *deps) confirmTasks(w http.ResponseWriter, r *http.Request, a access, queue string, verb conn.TaskVerb, f taskForm, back string) {
	name := a.conn.Name()
	m := pages.ConfirmTasks{
		Verb:      pages.TaskVerb(verb),
		IDs:       f.ids,
		All:       f.all,
		State:     pages.TaskState(f.state),
		Action:    d.url("c", name, "queues", queue, "tasks", pages.VerbParam(pages.TaskVerb(verb))),
		CancelURL: back,
	}
	for _, id := range f.ids {
		m.Hidden = append(m.Hidden, pages.Param{Name: "id", Value: id})
	}
	m.Groups = f.groups
	for _, g := range f.groups {
		m.Hidden = append(m.Hidden, pages.Param{Name: "group", Value: g})
	}
	if f.all {
		m.Hidden = append(m.Hidden, pages.Param{Name: "all", Value: "1"})
		ctx, cancel := context.WithTimeout(r.Context(), d.pageBudget)
		if qi, err := a.conn.QueueInfo(ctx, queue); err == nil {
			m.Total = int64(qi.Count(f.state))
		}
		cancel()
	}
	if f.stateOK {
		m.Hidden = append(m.Hidden, pages.Param{Name: "state", Value: pages.StateParam(m.State)})
	}
	if f.fromTask {
		m.Hidden = append(m.Hidden, pages.Param{Name: "from", Value: "task"})
	}
	m.Hidden = append(m.Hidden, pages.Param{Name: "next", Value: back})
	crumbs := []components.Crumb{
		{Text: name, URL: d.url("c", name)},
		{Text: queue, URL: d.url("c", name, "queues", queue)},
		{Label: pages.ConfirmCrumb(m)},
	}
	m.Layout = d.layout(r, a, components.SectionQueues, crumbs)
	m.Layout.TopBar.Next = back
	d.render(w, r, http.StatusOK, pages.ConfirmTasksPage(m))
}

// runTaskAction applies verb to the selected tasks in turn, or to every
// task of the state, and returns the notice (how many it acted on; nil
// when it acted on none of several) and the failures.
func (d *deps) runTaskAction(ctx context.Context, c Conn, queue string, verb conn.TaskVerb, f taskForm) (*pages.TaskNotice, []pages.TaskFailure) {
	pv := pages.TaskVerb(verb)
	var failures []pages.TaskFailure
	failGroup := func(id, group string, err error) {
		tf := pages.TaskFailure{Verb: pv, ID: id, Group: group}
		if errors.Is(err, conn.ErrTaskNotFound) || errors.Is(err, conn.ErrQueueNotFound) {
			tf.NotFound = true
		} else {
			tf.Detail = errText(err)
			d.logger.WarnContext(ctx, "task action failed", slog.String("conn", c.Name()), slog.String("queue", queue),
				slog.String("task", id), slog.String("group", group), slog.String("verb", pages.VerbParam(pv)), slog.Any("error", err))
		}
		failures = append(failures, tf)
	}
	fail := func(id string, err error) { failGroup(id, "", err) }
	if f.all && f.state == conn.TaskAggregating {
		total, acted := 0, false
		for _, g := range f.groups {
			n, err := c.GroupAction(ctx, queue, g, verb)
			total += n
			if err != nil {
				failGroup("", g, err)
				if n == 0 {
					continue
				}
			}
			acted = true
		}
		if !acted {
			return nil, failures
		}
		return &pages.TaskNotice{Verb: pv, Count: int64(total)}, failures
	}
	if f.all {
		n, err := c.AllAction(ctx, queue, verb, f.state)
		if err != nil {
			fail("", err)
			if n == 0 {
				return nil, failures
			}
		}
		return &pages.TaskNotice{Verb: pv, Count: int64(n)}, failures
	}
	done := 0
	for _, id := range f.ids {
		if err := c.TaskAction(ctx, queue, verb, id); err != nil {
			fail(id, err)
			continue
		}
		done++
	}
	if done == 0 {
		return nil, failures
	}
	return &pages.TaskNotice{Verb: pv, Count: int64(done)}, failures
}

// taskVersion is a short hash of what the Task page's polled cards show,
// so a poll can tell whether the task changed since the page rendered.
func taskVersion(t conn.Task) string {
	h := fnv.New64a()
	w := func(parts ...string) {
		for _, p := range parts {
			_, _ = h.Write([]byte(p))
			_, _ = h.Write([]byte{0})
		}
	}
	keys := make([]string, 0, len(t.Headers))
	for k := range t.Headers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		w(k, t.Headers[k])
	}
	w(pages.StateParam(pages.TaskState(t.State)), t.LastErr, string(t.Result), t.Group,
		strconv.Itoa(t.Retried), strconv.Itoa(t.MaxRetry), strconv.FormatBool(t.IsOrphaned),
		t.LastFailedAt.UTC().String(), t.NextProcessAt.UTC().String(), t.Deadline.UTC().String(),
		t.CompletedAt.UTC().String(), t.Timeout.String(), t.Retention.String())
	return strconv.FormatUint(h.Sum64(), 36)
}

// attempt is the attempt the task's last error came from. asynq counts a
// failure in Retried when it schedules a retry, but not the failure that
// archives the task. So a retry task's last error is attempt Retried, an
// archived task's is Retried+1, and a task run back to pending or
// scheduled keeps the count it had: Retried+1 when its retries were used
// up (it came from archived), Retried otherwise.
func attempt(t conn.Task) int {
	switch {
	case t.State == conn.TaskRetry:
		return max(t.Retried, 1)
	case t.State == conn.TaskArchived, t.Retried >= t.MaxRetry:
		return t.Retried + 1
	}
	return max(t.Retried, 1)
}
