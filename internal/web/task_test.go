package web

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/go-cmp/cmp"

	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/conn"
)

const uuidPath = "/c/main/queues/default/tasks/" + uuidTask

func details(doc *goquery.Document) map[string]string {
	out := map[string]string{}
	doc.Find("#task-details dt").Each(func(_ int, dt *goquery.Selection) {
		out[text(dt)] = text(dt.Next())
	})
	return out
}

func TestTaskPage(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	rec := do(t, h, request{path: uuidPath})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	doc := parse(t, rec)
	if got := crumbText(doc); got != "Overview / main / default / Retry / c3e81b90-04aa…" {
		t.Errorf("crumbs = %q", got)
	}
	if href := doc.Find(".crumbs li").Eq(3).Find("a").AttrOr("href", ""); href != "/c/main/queues/default?state=retry" {
		t.Errorf("state crumb = %q", href)
	}
	if got := text(doc.Find("title")); !strings.HasPrefix(got, "c3e81b90-04aa…") {
		t.Errorf("title = %q", got)
	}
	if text(doc.Find("h1")) != "email:receipt" || text(doc.Find("#task-badge")) != "Retry" {
		t.Errorf("head = %q %q", text(doc.Find("h1")), text(doc.Find("#task-badge")))
	}
	if text(doc.Find(".id-full")) != uuidTask || doc.Find(`button[data-copy="`+uuidTask+`"]`).Length() != 1 {
		t.Error("full ID and Copy ID")
	}
	var acts []string
	doc.Find("#task-actions > button").Each(func(_ int, s *goquery.Selection) { acts = append(acts, s.AttrOr("id", "")) })
	if diff := cmp.Diff([]string{"task-act-run", "task-act-archive", "task-act-delete"}, acts); diff != "" {
		t.Errorf("actions (-want +got):\n%s", diff)
	}
	if doc.Find("#task-act-run.btn.primary:not(.sm)").Length() != 1 || doc.Find("#task-act-delete[data-confirm=task-delete]").Length() != 1 ||
		doc.Find("#task-act-archive.danger[data-confirm=task-archive]").Length() != 1 {
		t.Error("action styles")
	}
	if dlg := doc.Find("dialog#task-archive"); text(dlg.Find("h2")) != "Archive task" || text(dlg.Find(".dlg-id")) != uuidTask ||
		!strings.Contains(text(dlg.Find(".dlg-warn")), "10,000 archived tasks for 90 days") {
		t.Errorf("archive dialog = %q", text(dlg))
	}
	form := doc.Find("#task-actions form#task-form[hidden]")
	if form.Find(`input[name=id][value="`+uuidTask+`"]`).Length() != 1 || form.Find(`input[name=from][value=task]`).Length() != 1 ||
		form.Find(`input[name=state][value=retry]`).Length() != 1 {
		t.Error("action form")
	}

	if got := doc.Find("pre#payload").Text(); got != "{\n  \"order_id\": \"ORD-88213\",\n  \"amount\": 4200\n}" {
		t.Errorf("payload = %q", got)
	}
	if doc.Find(`#payload-json.on[aria-current=page]`).Length() != 1 || doc.Find("#payload-raw").AttrOr("href", "") != uuidPath+"?payload=raw" {
		t.Error("JSON/Raw toggle")
	}
	if got := text(doc.Find("#task-error")); !strings.Contains(got, "Last error") || !strings.Contains(got, "attempt 3") ||
		!strings.Contains(got, "smtp: 450 mailbox unavailable") {
		t.Errorf("last error = %q", got)
	}
	if _, hidden := doc.Find("#task-result").Attr("hidden"); !hidden {
		t.Error("result card without a result")
	}
	want := map[string]string{
		"Queue": "default", "State": "Retry", "Retried": "3 / 25", "Last failed": "Nov 8, 00:40:52",
		"Next process at": "Nov 8, 00:44:32(Nov 8, 00:44:32)", "Timeout": "30s", "Deadline": "—", "Group": "—",
		"Headers": "a: btrace: t-1",
	}
	if diff := cmp.Diff(want, details(doc)); diff != "" {
		t.Errorf("details (-want +got):\n%s", diff)
	}
	if doc.Find(`#task-details time[datetime="2026-11-08T00:44:32Z"]`).Length() != 2 {
		t.Error("next process at is not a <time>")
	}

	// Raw view.
	doc = parse(t, do(t, h, request{path: uuidPath + "?payload=raw"}))
	if got := doc.Find("pre#payload").Text(); got != `{"order_id":"ORD-88213","amount":4200}` {
		t.Errorf("raw payload = %q", got)
	}
	if doc.Find(`#payload-raw.on`).Length() != 1 || doc.Find(`#task-form input[name=next]`).AttrOr("value", "") != uuidPath+"?payload=raw" {
		t.Error("raw toggle state")
	}
}

func TestTaskPageStates(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	tests := []struct {
		id      string
		actions []string
		check   func(t *testing.T, doc *goquery.Document)
	}{
		{"a0", []string{"task-act-cancel"}, func(t *testing.T, doc *goquery.Document) {
			if got := details(doc)["Orphaned"]; got != "No" {
				t.Errorf("orphaned = %q", got)
			}
			// Not JSON: shown as text, no toggle, never interpreted.
			if doc.Find("#payload-json").Length() != 0 || doc.Find("pre#payload").Text() != "�bin�" {
				t.Errorf("binary payload = %q", doc.Find("pre#payload").Text())
			}
		}},
		{"x0", []string{"task-act-run", "task-act-delete"}, func(t *testing.T, doc *goquery.Document) {
			if !strings.Contains(text(doc.Find("#task-error")), "attempt 26") {
				t.Errorf("archived attempt = %q", text(doc.Find("#task-error")))
			}
			if doc.Find("pre#payload script").Length() != 0 || doc.Find("pre#payload").Text() != "<script>alert(1)</script>" {
				t.Error("payload is not escaped text")
			}
		}},
		{"c0", []string{"task-act-delete"}, func(t *testing.T, doc *goquery.Document) {
			d := details(doc)
			if d["Completed at"] != "Nov 8, 00:40:52" || d["Result kept until"] != "Nov 9, 00:40:52" {
				t.Errorf("completed details = %v", d)
			}
			if got := doc.Find("pre#result").Text(); got != "{\n  \"sent\": true\n}" {
				t.Errorf("result = %q", got)
			}
			if doc.Find("#task-error").Children().Length() != 0 {
				t.Error("last error card without an error")
			}
		}},
		{"p00", []string{"task-act-archive", "task-act-delete"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			doc := parse(t, do(t, h, request{path: "/c/main/queues/default/tasks/" + tt.id}))
			var acts []string
			doc.Find("#task-actions > button").Each(func(_ int, s *goquery.Selection) { acts = append(acts, s.AttrOr("id", "")) })
			if diff := cmp.Diff(tt.actions, acts); diff != "" {
				t.Errorf("actions (-want +got):\n%s", diff)
			}
			if tt.check != nil {
				tt.check(t, doc)
			}
		})
	}
}

func TestTaskPageRoles(t *testing.T) {
	tests := []struct {
		role     config.Role
		run, del string
	}{
		{config.RoleViewer, "Requires operator on main", "Requires admin on main"},
		{config.RoleOperator, "", "Requires admin on main"},
		{config.RoleAdmin, "", ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			h, _ := newTestServer(t, testOpts{authz: config.Authz{Default: tt.role}})
			doc := parse(t, do(t, h, request{path: uuidPath}))
			for sel, title := range map[string]string{"#task-act-run": tt.run, "#task-act-delete": tt.del} {
				b := doc.Find(sel)
				_, disabled := b.Attr("disabled")
				if disabled != (title != "") || b.AttrOr("title", "") != title {
					h, _ := goquery.OuterHtml(b)
					t.Errorf("%s", h)
				}
			}
		})
	}
}

func TestTaskNotFound(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	for _, path := range []string{"/c/main/queues/default/tasks/nope", "/c/main/queues/nope/tasks/p00"} {
		rec := do(t, h, request{path: path})
		doc := parse(t, rec)
		if rec.Code != http.StatusNotFound || text(doc.Find("h1")) != "Task not found" {
			t.Errorf("%s = %d %q", path, rec.Code, text(doc.Find("h1")))
		}
		if doc.Find("[data-poll]").Length() != 0 {
			t.Errorf("%s polls", path)
		}
	}
}

func TestTaskPoll(t *testing.T) {
	h, conns := newTestServer(t, adminOpts())
	poll := func(path string) *goquery.Document {
		return parse(t, do(t, h, request{path: path, htmx: true, header: map[string]string{"HX-Trigger": "task-poll"}}))
	}
	pollURL := func(doc *goquery.Document) string { return doc.Find("#task-poll").AttrOr("hx-get", "") }
	doc := parse(t, do(t, h, request{path: uuidPath}))
	first := pollURL(doc)
	if !strings.HasPrefix(first, uuidPath+"?seen=retry.") {
		t.Fatalf("poll URL = %q", first)
	}
	// Nothing changed: only the poll element, the time and the sidebar.
	same := poll(first)
	if same.Find("#task-badge, #task-details, #task-error, #task-result, #task-actions, #task-main, pre").Length() != 0 {
		t.Error("a poll of an unchanged task replaced its cards")
	}
	if pollURL(same) != first {
		t.Error("poll URL changed without a change")
	}
	// The content changed, the state did not: the cards, not the actions.
	conns.tasks.mu.Lock()
	for i := range conns.tasks.tasks {
		if conns.tasks.tasks[i].ID == uuidTask {
			conns.tasks.tasks[i].LastErr = "smtp: 421"
		}
	}
	conns.tasks.mu.Unlock()
	content := poll(first)
	if !strings.Contains(text(content.Find("#task-error[hx-swap-oob]")), "smtp: 421") || content.Find("#task-details[hx-swap-oob]").Length() != 1 ||
		content.Find("#task-actions").Length() != 0 {
		t.Error("a content change must replace the cards and leave the actions")
	}
	second := pollURL(content)
	if second == first || !strings.HasPrefix(second, uuidPath+"?seen=retry.") {
		t.Errorf("poll URL after a content change = %q", second)
	}
	// The state changed (run elsewhere): the actions follow, with the
	// action form's state and the delete dialog.
	if err := conns.taskAction("default", conn.TaskRun, uuidTask); err != nil {
		t.Fatal(err)
	}
	changed := poll(second)
	actions := changed.Find("#task-actions[hx-swap-oob]")
	if text(changed.Find("#task-badge")) != "Pending" || actions.Find("#task-act-archive").Length() != 1 || changed.Find("#task-act-run").Length() != 0 ||
		actions.Find(`form#task-form input[name=state][value=pending]`).Length() != 1 || actions.Find("dialog#task-delete").Length() != 1 {
		t.Error("a poll after a state change must replace the actions, their form and dialog")
	}
	if !strings.HasPrefix(pollURL(changed), uuidPath+"?seen=pending.") {
		t.Error("poll URL does not follow the state")
	}
	// Gone: the page says so and stops polling.
	if err := conns.taskAction("default", conn.TaskDelete, uuidTask); err != nil {
		t.Fatal(err)
	}
	gone := poll(second)
	if text(gone.Find("#task-main h1")) != "Task not found" || gone.Find("#task-poll[data-poll]").Length() != 0 {
		t.Error("a poll of a deleted task")
	}
}

// Every focusable element in a region an answer swaps has an id, so htmx
// can give focus back after the swap.
func TestTaskFocusablesHaveIDs(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	for _, id := range []string{uuidTask, "a0", "c0", "x0"} {
		path := "/c/main/queues/default/tasks/" + id
		doc := parse(t, do(t, h, request{path: path}))
		seen := map[string]bool{}
		doc.Find("[id]").Each(func(_ int, s *goquery.Selection) {
			v := s.AttrOr("id", "")
			if seen[v] {
				t.Errorf("%s: duplicate id %q", id, v)
			}
			seen[v] = true
		})
		n := 0
		doc.Find("#task-page").Find("a[href], button, input:not([type=hidden]), select, textarea, summary, [tabindex]").Each(func(_ int, s *goquery.Selection) {
			n++
			if s.AttrOr("id", "") == "" {
				h, _ := goquery.OuterHtml(s)
				t.Errorf("%s: focusable without id: %s", id, h)
			}
		})
		if n < 4 {
			t.Errorf("%s: only %d focusables", id, n)
		}
	}
}

func TestAttempt(t *testing.T) {
	tests := []struct {
		state             conn.TaskState
		retried, maxRetry int
		want              int
	}{
		{conn.TaskRetry, 3, 25, 3},
		{conn.TaskRetry, 25, 25, 25},
		{conn.TaskArchived, 25, 25, 26},
		{conn.TaskArchived, 0, 25, 1},  // SkipRetry on the first attempt
		{conn.TaskPending, 25, 25, 26}, // run back from archived
		{conn.TaskPending, 3, 25, 3},   // run back from retry
		{conn.TaskPending, 0, 25, 1},
	}
	for _, tt := range tests {
		if got := attempt(conn.Task{State: tt.state, Retried: tt.retried, MaxRetry: tt.maxRetry}); got != tt.want {
			t.Errorf("attempt(%v, %d/%d) = %d, want %d", tt.state, tt.retried, tt.maxRetry, got, tt.want)
		}
	}
}

func TestTitles(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	tests := []struct {
		rq    request
		title string
		crumb string
	}{
		{request{path: "/c/main/queues/default"}, "default – asynqui", "default"},
		{request{path: uuidPath}, "c3e81b90-04aa… – asynqui", "c3e81b90-04aa…"},
		{request{path: "/c/main/queues/default/find"}, "Find task in default – asynqui", "Find task"},
		{request{method: http.MethodPost, path: "/c/main/queues/default/tasks/delete", form: "all=1&state=pending"},
			"Delete all 25 pending tasks – asynqui", "Delete all 25 pending tasks"},
		{request{method: http.MethodPost, path: "/c/main/queues/default/tasks/delete", form: "id=p00"}, "Delete task – asynqui", "Delete task"},
		{request{method: http.MethodPost, path: "/c/main/queues/default/tasks/delete", form: "id=p00&id=p01"}, "Delete tasks – asynqui", "Delete tasks"},
		{request{method: http.MethodPost, path: "/c/main/queues/default/tasks/archive", form: "id=p00"}, "Archive task – asynqui", "Archive task"},
		{request{method: http.MethodPost, path: "/c/main/queues/default/tasks/run", form: "all=1&state=aggregating&group=g1"},
			"Run all tasks in g1 – asynqui", "Run all tasks in g1"},
		{request{method: http.MethodPost, path: "/c/main/queues/default/tasks/run", form: "all=1&state=aggregating&group=g1&group=g2"},
			"Run all tasks in 2 groups – asynqui", "Run all tasks in 2 groups"},
	}
	for _, tt := range tests {
		doc := parse(t, do(t, h, tt.rq))
		raw, _ := doc.Find("title").Html()
		if got := doc.Find("title").Text(); got != tt.title || strings.Contains(raw, "&lt;") {
			t.Errorf("%s: title = %q", tt.rq.path, got)
		}
		cur := doc.Find(".crumbs [aria-current]")
		if got := cur.Text(); got != tt.crumb || cur.Children().Length() != 0 {
			t.Errorf("%s: crumb = %q", tt.rq.path, got)
		}
		if strings.Contains(tt.title, "Delete all") && doc.Find("h1").Text() != tt.crumb {
			t.Errorf("heading = %q", doc.Find("h1").Text())
		}
	}
}

func TestTaskDeleteRedirectsToRealState(t *testing.T) {
	h, conns := newTestServer(t, adminOpts())
	// The page showed retry, but the task was run meanwhile: back to
	// pending, where it was deleted from.
	if err := conns.taskAction("default", conn.TaskRun, uuidTask); err != nil {
		t.Fatal(err)
	}
	rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/default/tasks/delete",
		form: "id=" + uuidTask + "&from=task&state=retry&confirm=1"})
	if loc := rec.Header().Get("Location"); loc != "/c/main/queues/default" {
		t.Errorf("Location = %q", loc)
	}
}

func TestUntrustedText(t *testing.T) {
	conns := defaultConns()
	conns.tasks.tasks = append(conns.tasks.tasks, conn.Task{ID: "abc\u202Eexe.txt", Queue: "default", Type: "t\u202Ex", State: conn.TaskPending,
		Payload: []byte("\u202Eevil")})
	h, _ := newTestServer(t, testOpts{conns: conns})
	doc := parse(t, do(t, h, request{path: "/c/main/queues/default?page=2"}))
	row := doc.Find("#tasks-rows tr").Last()
	link := row.Find("a.row-link")
	if link.Find("bdi").Text() != "abc\uFFFDexe.txt" || link.AttrOr("title", "") != "abc\u202Eexe.txt" ||
		row.Find(".ty bdi").Text() != "t\uFFFDx" || row.Find(".payload-cell bdi").Length() != 1 {
		h, _ := goquery.OuterHtml(row)
		t.Errorf("row = %s", h)
	}
}

func TestFindTask(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	find := "/c/main/queues/default/find"

	rec := do(t, h, request{path: find + "?id=" + url.QueryEscape(" "+uuidTask+" ")})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != uuidPath {
		t.Errorf("hit = %d %q", rec.Code, loc)
	}
	rec = do(t, h, request{path: find + "?id=" + uuidTask, htmx: true})
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != uuidPath || rec.Body.Len() != 0 {
		t.Errorf("htmx hit = %d %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}

	// A miss re-renders the dialog's body, keeping the ID.
	frag := parse(t, do(t, h, request{path: find + "?id=nope", htmx: true}))
	if frag.Find("#find-body").Length() != 1 || text(frag.Find("#find-miss[role=status]")) != "Task not found" ||
		frag.Find(`#find-id[value=nope][aria-invalid=true]`).Length() != 1 || frag.Find(".side").Length() != 0 {
		t.Error("htmx miss")
	}
	// The ID must be in this queue.
	frag = parse(t, do(t, h, request{path: "/c/main/queues/low/find?id=p00", htmx: true}))
	if text(frag.Find("#find-miss")) != "Task not found" {
		t.Error("a task of another queue was found")
	}

	// Without JS: a page with the same form.
	doc := parse(t, do(t, h, request{path: find}))
	if text(doc.Find("h1")) != "Find task in default" || doc.Find(`form[method=get][action="`+find+`"] #find-id`).Length() != 1 ||
		doc.Find("#find-miss").Length() != 0 || crumbText(doc) != "Overview / main / default / Find task" {
		t.Errorf("find page: %q %q", text(doc.Find("h1")), crumbText(doc))
	}
	if doc.Find(`.find-body a.btn[href="/c/main/queues/default"]`).Length() != 1 {
		t.Error("no-JS cancel link")
	}
	doc = parse(t, do(t, h, request{path: find + "?id=nope"}))
	if text(doc.Find("#find-miss")) != "Task not found" {
		t.Error("no-JS miss")
	}

	// A lookup that fails shows the error, not "not found".
	conns := defaultConns()
	conns.tasks.readErr = errors.New("LOADING")
	h, _ = newTestServer(t, testOpts{conns: conns})
	frag = parse(t, do(t, h, request{path: find + "?id=p00", htmx: true}))
	if !strings.Contains(text(frag.Find(".conn-error")), "LOADING") || frag.Find("#find-miss").Length() != 0 {
		t.Error("failed lookup")
	}
}

func TestTaskActions(t *testing.T) {
	post := func(path, form string, htmx bool) request {
		return request{method: http.MethodPost, path: path, form: form, htmx: htmx}
	}
	const base = "/c/main/queues/default/tasks/"
	retryNext := "&next=" + url.QueryEscape("/c/main/queues/default?state=retry")
	tests := []struct {
		name   string
		rq     request
		status int
		// location is the redirect, acted the source calls.
		location string
		acted    []string
		check    func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{name: "archive needs confirm with htmx", rq: post(base+"archive", "id=p00", true), status: http.StatusBadRequest},
		{name: "archive without JS asks on a page", rq: post(base+"archive", "id=p00&state=pending", false), status: http.StatusOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				doc := parse(t, rec)
				if text(doc.Find("h1")) != "Archive task" || !strings.Contains(text(doc.Find(".dlg-warn")), "beyond that, tasks are removed") ||
					doc.Find(`form button.btn.danger`).Length() != 1 || doc.Find(`input[name=confirm][value="1"]`).Length() != 1 {
					t.Error("archive confirmation page")
				}
			}},
		{name: "archive selected, no JS", rq: post(base+"archive", "id=p00&id=p01&id=p00&id=&state=pending&confirm=1&next=%2Fc%2Fmain%2Fqueues%2Fdefault%3Fpage%3D2", false),
			status: http.StatusSeeOther, location: "/c/main/queues/default?page=2", acted: []string{"archive default/p00", "archive default/p01"},
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				f := flashOf(t, rec)
				if !strings.Contains(f, `"n":2`) || !strings.Contains(f, `"v":1`) {
					t.Errorf("flash = %s", f)
				}
			}},
		{name: "run selected, htmx", rq: post(base+"run", "id=s0&id=gone&state=scheduled&next=%2Fc%2Fmain%2Fqueues%2Fdefault%3Fstate%3Dscheduled", true),
			status: http.StatusOK, acted: []string{"run default/s0", "run default/gone"},
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				doc := parse(t, rec)
				if got := text(doc.Find("#task-card .notice")); got != "Moved 1 task to pending" {
					t.Errorf("notice = %q", got)
				}
				if got := text(doc.Find(`[hx-swap-oob="beforeend:#toasts"] .toast-body`)); got != "Task gone not found" {
					t.Errorf("toast = %q", got)
				}
				if doc.Find("#tab-scheduled.on").Length() != 1 || doc.Find("#tasks-rows input[hx-preserve]").Length() != 0 ||
					doc.Find("#tasks-rows input.cb").Length() != 1 {
					t.Error("the answer must show the same tab with the selection cleared")
				}
			}},
		{name: "delete needs confirm with htmx", rq: post(base+"delete", "id=p00", true), status: http.StatusBadRequest},
		{name: "delete without JS asks on a page", rq: post(base+"delete", "id=p00&id=p01&state=pending", false), status: http.StatusOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				doc := parse(t, rec)
				f := doc.Find(`form[action="/c/main/queues/default/tasks/delete"]`)
				if text(doc.Find("h1")) != "Delete tasks" || doc.Find(".dlg-list li").Length() != 2 ||
					f.Find(`input[name=confirm][value="1"]`).Length() != 1 || f.Find(`input[name=id]`).Length() != 2 {
					t.Error("confirmation page")
				}
			}},
		{name: "delete confirmed", rq: post(base+"delete", "id=p00&confirm=1", false), status: http.StatusSeeOther,
			location: "/c/main/queues/default", acted: []string{"delete default/p00"}},
		{name: "archive all, confirmed", rq: post(base+"archive", "all=1&state=retry&confirm=1&id=p00"+retryNext, true), status: http.StatusOK,
			acted: []string{"archive-all default/retry"},
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if got := text(parse(t, rec).Find(".notice")); got != "Archived 1 task" {
					t.Errorf("notice = %q", got)
				}
			}},
		{name: "all needs confirm", rq: post(base+"run", "all=1&state=retry", true), status: http.StatusBadRequest},
		{name: "all without JS asks on a page", rq: post(base+"run", "all=1&state=scheduled", false), status: http.StatusOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				doc := parse(t, rec)
				if got := text(doc.Find("h1")); got != "Run all 2 scheduled tasks" {
					t.Errorf("h1 = %q", got)
				}
				if doc.Find(`input[name=all][value="1"]`).Length() != 1 || doc.Find(`input[name=state][value=scheduled]`).Length() != 1 {
					t.Error("confirmation fields")
				}
			}},
		{name: "no cancel all", rq: post(base+"cancel", "all=1&state=active&confirm=1", false), status: http.StatusBadRequest},
		{name: "no run all pending", rq: post(base+"run", "all=1&state=pending&confirm=1", false), status: http.StatusBadRequest},
		{name: "all needs a state", rq: post(base+"delete", "all=1&confirm=1", false), status: http.StatusBadRequest},
		{name: "cancel selected", rq: post(base+"cancel", "id=a0&id=p00&state=active", true), status: http.StatusOK,
			acted: []string{"cancel default/a0", "cancel default/p00"},
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				doc := parse(t, rec)
				if got := text(doc.Find(".notice")); got != "Cancel requested for 1 task" {
					t.Errorf("notice = %q", got)
				}
				if got := text(doc.Find(".toast-body")); !strings.Contains(got, "Could not cancel p00") {
					t.Errorf("toast = %q", got)
				}
			}},
		{name: "nothing selected", rq: post(base+"archive", "state=pending", false), status: http.StatusSeeOther, location: "/c/main/queues/default"},
		{name: "task page run, htmx", rq: post(base+"run", "id="+uuidTask+"&from=task&state=retry&next="+url.QueryEscape(uuidPath+"?payload=raw"), true),
			status: http.StatusOK, acted: []string{"run default/" + uuidTask},
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				doc := parse(t, rec)
				if text(doc.Find("#task-badge")) != "Pending" || doc.Find("#task-main[hx-swap-oob] #payload-raw.on").Length() != 1 {
					t.Error("task page answer")
				}
			}},
		{name: "task page run, no JS", rq: post(base+"run", "id="+uuidTask+"&from=task&next="+url.QueryEscape(uuidPath), false),
			status: http.StatusSeeOther, location: uuidPath, acted: []string{"run default/" + uuidTask}},
		{name: "task page delete, htmx", rq: post(base+"delete", "id=x0&from=task&state=archived&confirm=1", true),
			status: http.StatusOK, acted: []string{"delete default/x0"},
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				if got := rec.Header().Get("HX-Redirect"); got != "/c/main/queues/default?state=archived" {
					t.Errorf("HX-Redirect = %q", got)
				}
				if !strings.Contains(flashOf(t, rec), `"n":1`) {
					t.Error("no notice for the queue page")
				}
			}},
		{name: "task page delete failure stays", rq: post(base+"delete", "id=a0&from=task&state=active&confirm=1", false),
			status: http.StatusSeeOther, location: "/c/main/queues/default/tasks/a0", acted: []string{"delete default/a0"}},
		{name: "group run all, htmx", rq: post(base+"run", "all=1&state=aggregating&group=g1&group=g9&confirm=1&next="+url.QueryEscape("/c/main/queues/default?state=aggregating"), true),
			status: http.StatusOK, acted: []string{"run-all default/group:g1", "run-all default/group:g9"},
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				doc := parse(t, rec)
				if got := text(doc.Find(".notice")); got != "Moved 4 tasks to pending" {
					t.Errorf("notice = %q", got)
				}
				if doc.Find("#tab-aggregating.on").Length() != 1 || doc.Find("#groups tbody input[hx-preserve]").Length() != 0 {
					t.Error("the answer must show the group list with the selection cleared")
				}
			}},
		{name: "group all needs a group", rq: post(base+"delete", "all=1&state=aggregating&confirm=1", true), status: http.StatusBadRequest},
		{name: "group all without JS asks on a page", rq: post(base+"archive", "all=1&state=aggregating&group=g1", false), status: http.StatusOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				doc := parse(t, rec)
				if text(doc.Find("h1")) != "Archive all tasks in g1" || text(doc.Find(".dlg-list li")) != "g1" ||
					doc.Find(`input[name=group][value=g1]`).Length() != 1 || doc.Find(".dlg-warn").Length() != 1 {
					t.Errorf("group confirmation page = %q", text(doc.Find("main")))
				}
			}},
		{name: "group task archive", rq: post(base+"archive", "id=g1-0&state=aggregating&confirm=1&next="+url.QueryEscape("/c/main/queues/default?state=aggregating&group=g1"), false),
			status: http.StatusSeeOther, location: "/c/main/queues/default?group=g1&state=aggregating", acted: []string{"archive default/g1-0"}},
		{name: "next cannot leave the queue", rq: post(base+"archive", "confirm=1&id=p00&next="+url.QueryEscape("https://evil.example/x?state=retry"), false),
			status: http.StatusSeeOther, location: "/c/main/queues/default?state=retry", acted: []string{"archive default/p00"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, conns := newTestServer(t, adminOpts())
			rec := do(t, h, tt.rq)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tt.status, rec.Body)
			}
			if tt.location != "" && rec.Header().Get("Location") != tt.location {
				t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), tt.location)
			}
			if diff := cmp.Diff(tt.acted, conns.taskActions()); diff != "" {
				t.Errorf("acted (-want +got):\n%s", diff)
			}
			if tt.check != nil {
				tt.check(t, rec)
			}
		})
	}
}

// TestTaskActionFlash: an action without JS leaves its notice and failures
// for the page it redirects to, once.
func TestTaskActionFlash(t *testing.T) {
	h, conns := newTestServer(t, adminOpts())
	conns.tasks.actErr["p01"] = errors.New("boom")
	rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/default/tasks/archive", form: "id=p00&id=p01&id=zz&confirm=1"})
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Path != "/c/main" {
		t.Fatalf("cookies = %v", cookies)
	}
	doc := parse(t, do(t, h, request{path: "/c/main/queues/default", cookies: cookies}))
	if got := text(doc.Find("#task-card .notice")); got != "Archived 1 task" {
		t.Errorf("notice = %q", got)
	}
	var toasts []string
	doc.Find("#toasts .toast-body").Each(func(_ int, s *goquery.Selection) { toasts = append(toasts, text(s)) })
	if diff := cmp.Diff([]string{"Could not archive p01 boom", "Task zz not found"}, toasts); diff != "" {
		t.Errorf("toasts (-want +got):\n%s", diff)
	}
}

// A failing group "all" action toasts the group, with htmx and through
// the flash cookie without JS.
func TestGroupActionFailure(t *testing.T) {
	h, conns := newTestServer(t, adminOpts())
	conns.tasks.actErr["group:g1"] = errors.New("boom")
	form := "all=1&state=aggregating&group=g1&confirm=1"
	frag := parse(t, do(t, h, request{method: http.MethodPost, path: "/c/main/queues/default/tasks/archive", form: form, htmx: true}))
	if got := text(frag.Find(".toast-body")); got != "Could not archive all tasks in g1 boom" {
		t.Errorf("toast = %q", got)
	}
	rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/default/tasks/archive", form: form})
	doc := parse(t, do(t, h, request{path: "/c/main/queues/default?state=aggregating", cookies: rec.Result().Cookies()}))
	if got := text(doc.Find("#toasts .toast-body")); got != "Could not archive all tasks in g1 boom" {
		t.Errorf("flash toast = %q", got)
	}
}

func TestTaskActionRoles(t *testing.T) {
	tests := []struct {
		role   config.Role
		path   string
		status int
	}{
		{config.RoleViewer, "run", http.StatusForbidden},
		{config.RoleViewer, "archive", http.StatusForbidden},
		{config.RoleViewer, "cancel", http.StatusForbidden},
		{config.RoleOperator, "run", http.StatusSeeOther},
		{config.RoleOperator, "cancel", http.StatusSeeOther},
		{config.RoleOperator, "delete", http.StatusForbidden},
		{config.RoleAdmin, "delete", http.StatusSeeOther},
	}
	for _, tt := range tests {
		h, conns := newTestServer(t, testOpts{authz: config.Authz{Default: tt.role}})
		rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/default/tasks/" + tt.path, form: "id=s0&confirm=1"})
		if rec.Code != tt.status {
			t.Errorf("%s %s = %d, want %d", tt.role, tt.path, rec.Code, tt.status)
		}
		if tt.status == http.StatusForbidden && len(conns.taskActions()) != 0 {
			t.Errorf("%s %s acted", tt.role, tt.path)
		}
	}
}

func TestTaskActionsCSRF(t *testing.T) {
	h, conns := newTestServer(t, adminOpts())
	rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/default/tasks/delete", form: "id=p00&confirm=1",
		header: map[string]string{"Sec-Fetch-Site": "cross-site"}})
	if rec.Code != http.StatusForbidden || len(conns.taskActions()) != 0 {
		t.Errorf("cross-site post = %d, acted %v", rec.Code, conns.taskActions())
	}
}

func flashOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == flashCookie {
			b, err := base64.RawURLEncoding.DecodeString(c.Value)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
	}
	t.Fatal("no flash cookie")
	return ""
}

func TestTaskBackLinkAndCrumb(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	doc := parse(t, do(t, h, request{path: uuidPath}))
	back := doc.Find("a#task-back.back-link")
	if back.AttrOr("href", "") != "/c/main/queues/default?state=retry" || text(back) != "default › Retry" || back.Find("svg").Length() != 1 {
		t.Errorf("back link = %q %q", back.AttrOr("href", ""), text(back))
	}
	cur := doc.Find(".crumbs [aria-current]")
	if cur.AttrOr("class", "") != "crumb-mono" || cur.AttrOr("title", "") != uuidTask {
		h, _ := goquery.OuterHtml(cur)
		t.Errorf("crumb = %s", h)
	}
}
