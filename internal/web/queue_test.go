package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/go-cmp/cmp"

	"github.com/BarkinBalci/asynqui/internal/config"
)

func adminOpts() testOpts { return testOpts{authz: config.Authz{Default: config.RoleAdmin}} }

// crumbText is the breadcrumbs as read, "Overview / main / …".
func crumbText(doc *goquery.Document) string {
	var out []string
	doc.Find(".crumbs li").Each(func(_ int, s *goquery.Selection) { out = append(out, text(s)) })
	return strings.Join(out, " / ")
}

func TestQueuePage(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	rec := do(t, h, request{path: "/c/main/queues/default"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	doc := parse(t, rec)

	if got := text(doc.Find("h1")); got != "default" {
		t.Errorf("h1 = %q", got)
	}
	if got := text(doc.Find("#queue-badge")); got != "Running" {
		t.Errorf("badge = %q", got)
	}
	if got := text(doc.Find("title")); !strings.HasPrefix(got, "default") {
		t.Errorf("title = %q", got)
	}
	if got := crumbText(doc); got != "Overview / main / default" {
		t.Errorf("crumbs = %q", got)
	}
	if doc.Find(`.subnav a.on`).Length() != 1 || text(doc.Find(`.subnav a.on`)) != "Queues" {
		t.Errorf("sidebar section = %q", text(doc.Find(`.subnav a.on`)))
	}
	var stats []string
	doc.Find("#queue-stats .kpi").Each(func(_ int, s *goquery.Selection) { stats = append(stats, text(s)) })
	if diff := cmp.Diff([]string{"Size 1,402", "Latency 12.6s", "Memory 41.0 MB", "Processed today 231,775", "Failed today 540"}, stats); diff != "" {
		t.Errorf("stats (-want +got):\n%s", diff)
	}

	var tabs []string
	doc.Find(".tabs a.tab").Each(func(_ int, s *goquery.Selection) { tabs = append(tabs, text(s)) })
	if diff := cmp.Diff([]string{"Active 2", "Pending 25", "Aggregating 4", "Scheduled 2", "Retry 1", "Archived 1", "Completed 1"}, tabs); diff != "" {
		t.Errorf("tabs (-want +got):\n%s", diff)
	}
	if on := doc.Find(`.tabs a.on[aria-current=page]`); on.AttrOr("id", "") != "tab-pending" || on.AttrOr("href", "") != "/c/main/queues/default" {
		t.Errorf("current tab = %v", on.AttrOr("id", ""))
	}

	var heads []string
	doc.Find("#tasks thead th").Each(func(_ int, s *goquery.Selection) { heads = append(heads, text(s)) })
	if diff := cmp.Diff([]string{"Select all tasks on this page", "Task ID", "Type", "Payload", "Retried"}, heads); diff != "" {
		t.Errorf("headers (-want +got):\n%s", diff)
	}
	if n := doc.Find("#tasks [aria-sort], #tasks a.sort").Length(); n != 0 {
		t.Errorf("%d sortable headers; asynq's order is fixed", n)
	}
	rows := doc.Find("#tasks-rows tr")
	if rows.Length() != 20 {
		t.Fatalf("%d rows, want 20", rows.Length())
	}
	first := rows.First()
	link := first.Find("a.row-link")
	if link.AttrOr("href", "") != "/c/main/queues/default/tasks/p00" || link.AttrOr("id", "") != "tlink-703030" || text(link) != "p00" {
		t.Errorf("row link = %v %v", link.AttrOr("href", ""), link.AttrOr("id", ""))
	}
	if cb := first.Find("input.cb"); cb.AttrOr("id", "") != "tsel-703030" || cb.AttrOr("form", "") != "tasks-form" ||
		cb.AttrOr("name", "") != "id" || cb.AttrOr("value", "") != "p00" || cb.AttrOr("hx-preserve", "") != "true" {
		h, _ := goquery.OuterHtml(cb)
		t.Errorf("checkbox = %s", h)
	}
	if got := text(first.Find("td.payload-cell")); got != `{"user_id":0}` {
		t.Errorf("payload = %q", got)
	}

	if got := text(doc.Find("#tasks-pager span").First()); got != "1–20 of 25" {
		t.Errorf("range = %q", got)
	}
	if doc.Find("#tasks-pager span.pager-btn[aria-disabled=true][aria-label='Previous page']").Length() != 1 || doc.Find("a#page-next").AttrOr("href", "") != "/c/main/queues/default?page=2" {
		t.Error("pager links")
	}
	if v := doc.Find("#tasks-size option[selected]").AttrOr("value", ""); v != "20" {
		t.Errorf("size = %q", v)
	}

	banner := doc.Find("#tasks-banner")
	if banner.AttrOr("data-total", "") != "25" || !strings.Contains(text(banner), "All 20 on this page are selected.") ||
		!strings.Contains(text(banner), "Select all 25 pending tasks") || !strings.Contains(text(banner), "All 25 pending tasks are selected.") {
		t.Errorf("banner = %q", text(banner))
	}
	if doc.Find(`#tasks-all[name=all][form=tasks-form][data-everything=tasks-form]`).Length() != 1 {
		t.Error("no all field")
	}
	if doc.Find("#act-archive.danger[data-confirm=tasks-archive][data-confirm-all=tasks-all-archive]").Length() != 1 ||
		!strings.Contains(text(doc.Find("dialog#tasks-archive .dlg-warn")), "asynq keeps at most 10,000 archived tasks for 90 days") ||
		!strings.Contains(text(doc.Find("dialog#tasks-all-archive .dlg-warn")), "10,000") || doc.Find("dialog#tasks-delete .dlg-warn").Length() != 0 ||
		doc.Find("#act-delete[data-confirm=tasks-delete][data-confirm-all=tasks-all-delete]").Length() != 1 {
		t.Error("toolbar buttons")
	}
	if got := text(doc.Find("#tasks-all-delete-title")); got != "Delete all 25 pending tasks" {
		t.Errorf("all dialog title = %q", got)
	}

	// Find task: a link to the page without JS, the dialog with JS.
	find := doc.Find("#find-open")
	if find.AttrOr("href", "") != "/c/main/queues/default/find" || find.AttrOr("data-dialog-open", "") != "find-task" {
		t.Error("find button")
	}
	dlg := doc.Find("dialog#find-task[data-find-opener=find-open]")
	if text(dlg.Find("h2")) != "Find task in default" || dlg.Find(`form[method=get][action="/c/main/queues/default/find"] input#find-id[name=id][autofocus]`).Length() != 1 {
		t.Error("find dialog")
	}
	toggle := doc.Find("#queue-toggle form")
	if toggle.AttrOr("action", "") != "/c/main/queues/pause" || toggle.Find(`input[name=from][value=queue]`).Length() != 1 ||
		toggle.Find(`input[name=queue][value=default]`).Length() != 1 || text(toggle.Find("button")) != "Pause queue" {
		t.Error("toggle")
	}
}

func TestQueueTabs(t *testing.T) {
	tests := []struct {
		state   string
		heads   []string
		actions []string
		banner  bool
		row     string
	}{
		{"active", []string{"Started", "Running for"}, []string{"act-cancel"}, false, "a0|email:digest|�bin�|Nov 8, 00:41:48|4s"},
		{"scheduled", []string{"Process at", "In"}, []string{"act-run", "act-archive", "act-delete"}, false, "s0|report||Nov 8, 01:26:52|Nov 8, 01:26:52"},
		{"retry", []string{"Next retry", "Last error"}, []string{"act-run", "act-archive", "act-delete"}, false, ""},
		{"archived", []string{"Archived at", "Last error"}, []string{"act-run", "act-delete"}, false, ""},
		{"completed", []string{"Completed at", "Result kept until"}, []string{"act-delete"}, false, "c0|email:welcome||Nov 8, 00:40:52|Nov 9, 00:40:52"},
	}
	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			h, _ := newTestServer(t, adminOpts())
			doc := parse(t, do(t, h, request{path: "/c/main/queues/default?state=" + tt.state}))
			var heads []string
			doc.Find("#tasks thead th").Slice(4, goquery.ToEnd).Each(func(_ int, s *goquery.Selection) { heads = append(heads, text(s)) })
			if diff := cmp.Diff(tt.heads, heads); diff != "" {
				t.Errorf("state headers (-want +got):\n%s", diff)
			}
			var acts []string
			doc.Find(".tbar button").Each(func(_ int, s *goquery.Selection) { acts = append(acts, s.AttrOr("id", "")) })
			if diff := cmp.Diff(tt.actions, acts); diff != "" {
				t.Errorf("actions (-want +got):\n%s", diff)
			}
			if got := doc.Find("#tasks-banner[data-select-banner]").Length() == 1; got != tt.banner {
				t.Errorf("banner = %v", got)
			}
			if tt.row != "" {
				var cells []string
				doc.Find("#tasks-rows tr").First().Find("td").Slice(1, goquery.ToEnd).Each(func(_ int, s *goquery.Selection) { cells = append(cells, text(s)) })
				if got := strings.Join(cells, "|"); got != tt.row {
					t.Errorf("row = %q, want %q", got, tt.row)
				}
			}
			if tt.state == "active" && doc.Find("[data-confirm-all], dialog#tasks-delete").Length() != 0 {
				t.Error(`active offers "all" actions or delete`)
			}
		})
	}
}

// The scheduled, retry, archived and completed tables sort by their time
// column; the link flips the direction and keeps the page and size.
func TestQueueSort(t *testing.T) {
	tests := []struct {
		path, head, aria, link string
		ids                    []string
	}{
		{"/c/main/queues/default?state=scheduled", "Process at", "ascending", "/c/main/queues/default?dir=desc&state=scheduled", []string{"s0", "s1"}},
		{"/c/main/queues/default?state=scheduled&dir=desc&page=1&size=50", "Process at", "descending", "/c/main/queues/default?size=50&state=scheduled", []string{"s1", "s0"}},
		{"/c/main/queues/default?state=archived", "Archived at", "descending", "/c/main/queues/default?dir=asc&state=archived", []string{"x0"}},
		{"/c/main/queues/default?state=completed", "Result kept until", "descending", "/c/main/queues/default?dir=asc&state=completed", []string{"c0"}},
		{"/c/main/queues/default?state=retry&dir=desc", "Next retry", "descending", "/c/main/queues/default?state=retry", []string{uuidTask}},
	}
	for _, tt := range tests {
		h, _ := newTestServer(t, adminOpts())
		doc := parse(t, do(t, h, request{path: tt.path}))
		th := doc.Find("#tasks thead th[aria-sort]")
		a := th.Find("a#sort-time.sort")
		if th.Length() != 1 || text(th) != tt.head || th.AttrOr("aria-sort", "") != tt.aria || a.AttrOr("href", "") != tt.link ||
			a.AttrOr("hx-get", "") != tt.link || a.AttrOr("hx-push-url", "") != "true" {
			t.Errorf("%s: sort header %q %q -> %q", tt.path, text(th), th.AttrOr("aria-sort", ""), a.AttrOr("href", ""))
		}
		var ids []string
		doc.Find("#tasks-rows tr a.row-link").Each(func(_ int, s *goquery.Selection) { ids = append(ids, s.AttrOr("title", "")) })
		if diff := cmp.Diff(tt.ids, ids); diff != "" {
			t.Errorf("%s: rows (-want +got):\n%s", tt.path, diff)
		}
	}

	// Pending sorts with ?dir= but has no time column to put the link on;
	// active never sorts.
	h, _ := newTestServer(t, adminOpts())
	doc := parse(t, do(t, h, request{path: "/c/main/queues/default?dir=desc&size=50"}))
	if first := doc.Find("#tasks-rows a.row-link").First().AttrOr("title", ""); first != "p24" || doc.Find("[aria-sort]").Length() != 0 {
		t.Errorf("pending newest first starts with %q", first)
	}
	if href := doc.Find("#page-next").AttrOr("href", ""); href != "" {
		t.Errorf("one page has a next link %q", href)
	}
	if doc.Find("#tasks-size-form input[name=dir][value=desc]").Length() != 1 {
		t.Error("the size picker must keep the direction")
	}
	doc = parse(t, do(t, h, request{path: "/c/main/queues/default?state=active&dir=desc"}))
	if doc.Find("[aria-sort]").Length() != 0 || doc.Find("#tasks-rows a.row-link").First().AttrOr("title", "") != "a0" {
		t.Error("active sorts")
	}
	// Tabs reset the direction; pager links keep it.
	doc = parse(t, do(t, h, request{path: "/c/main/queues/default?dir=desc"}))
	if href := doc.Find("#tab-archived").AttrOr("href", ""); href != "/c/main/queues/default?state=archived" {
		t.Errorf("archived tab = %q", href)
	}
	if href := doc.Find("#page-next").AttrOr("href", ""); href != "/c/main/queues/default?dir=desc&page=2" {
		t.Errorf("next page = %q", href)
	}

	// The archived column is the archive time, not the last failure.
	conns := defaultConns()
	conns.tasks.tasks = append(conns.tasks.tasks, Task{ID: "x9", Queue: "default", Type: "t", State: TaskArchived,
		ArchivedAt: fixedNow().Add(-2 * time.Hour)})
	h, _ = newTestServer(t, testOpts{conns: conns, authz: adminOpts().authz})
	doc = parse(t, do(t, h, request{path: "/c/main/queues/default?state=archived&dir=asc"}))
	if got := text(doc.Find("#tasks-rows tr").Last().Find("td").Eq(4)); got != "Nov 7, 22:41:52" {
		t.Errorf("archived at = %q", got)
	}
}

func TestQueueAggregating(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	doc := parse(t, do(t, h, request{path: "/c/main/queues/default?state=aggregating"}))
	if got := text(doc.Find("#groups tbody td").Eq(1)) + "|" + text(doc.Find("#groups tbody td").Last()); got != "g1|4" {
		t.Errorf("groups = %q", got)
	}
	link := doc.Find("#groups tbody a.row-link")
	if href, _ := link.Attr("href"); href != "/c/main/queues/default?group=g1&state=aggregating" {
		t.Errorf("group link = %q", href)
	}
	box := doc.Find("#groups tbody input.cb")
	if n, _ := box.Attr("name"); n != "group" || box.AttrOr("value", "") != "g1" || box.AttrOr("form", "") != "tasks-form" {
		t.Errorf("group checkbox = %v", box.Nodes)
	}
	if doc.Find(`input[form=tasks-form][name=all][value="1"]`).Length() != 1 || doc.Find(`input[form=tasks-form][name=state][value=aggregating]`).Length() != 1 {
		t.Error("the group form must post all=1&state=aggregating")
	}
	var buttons []string
	doc.Find("#task-card .tbar button").Each(func(_ int, s *goquery.Selection) {
		buttons = append(buttons, text(s)+"/"+s.AttrOr("data-confirm", "")+"/"+s.AttrOr("class", ""))
	})
	want := []string{"Run all/groups-run/btn sm primary", "Archive all/groups-archive/btn sm danger", "Delete all/groups-delete/btn sm danger"}
	if diff := cmp.Diff(want, buttons); diff != "" {
		t.Errorf("group buttons (-want +got):\n%s", diff)
	}
	if !strings.Contains(text(doc.Find("dialog#groups-archive .dlg-warn")), "10,000 archived tasks for 90 days") ||
		doc.Find("dialog#groups-run .dlg-warn, dialog#groups-delete .dlg-warn").Length() != 0 {
		t.Error("only the archive dialog warns")
	}

	// A poll swaps the group list, not a task table.
	frag := parse(t, do(t, h, request{path: "/c/main/queues/default?state=aggregating", htmx: true, header: map[string]string{"HX-Trigger": "queue-poll"}}))
	if frag.Find("#tasks-table[hx-swap-oob] #groups").Length() != 1 || frag.Find("#tasks-pager").Length() != 0 {
		t.Errorf("group list poll = %s", frag.Text())
	}

	// A group's tasks: selection, single actions and "select all N".
	doc = parse(t, do(t, h, request{path: "/c/main/queues/default?state=aggregating&group=g1"}))
	var ids []string
	doc.Find("#tasks-rows tr a.row-link").Each(func(_ int, s *goquery.Selection) { ids = append(ids, text(s)) })
	if diff := cmp.Diff([]string{"g1-0", "g1-1", "g1-2", "g1-3"}, ids); diff != "" {
		t.Errorf("group tasks (-want +got):\n%s", diff)
	}
	if doc.Find(`input[form=tasks-form][name=group][value=g1]`).Length() != 1 || text(doc.Find(".group-nav .mono")) != "g1" {
		t.Error("the group view must post its group and name it")
	}
	if href, _ := doc.Find("#group-back").Attr("href"); href != "/c/main/queues/default?state=aggregating" {
		t.Errorf("back to groups = %q", href)
	}
	buttons = nil
	doc.Find("#task-card .tbar button").Each(func(_ int, s *goquery.Selection) { buttons = append(buttons, s.AttrOr("id", "")) })
	if diff := cmp.Diff([]string{"act-run", "act-archive", "act-delete"}, buttons); diff != "" {
		t.Errorf("group task buttons (-want +got):\n%s", diff)
	}
	if text(doc.Find("#tasks-pager .num")) != "1–4 of 4" {
		t.Errorf("pager = %q", text(doc.Find("#tasks-pager .num")))
	}
	if doc.Find("#tasks-size-form input[name=group][value=g1]").Length() != 1 {
		t.Error("the size picker must keep the group")
	}

	doc = parse(t, do(t, h, request{path: "/c/main/queues/low?state=aggregating"}))
	if got := text(doc.Find("#tasks-table .empty-state")); got != "No aggregating tasks" || doc.Find("#tasks-table .empty-state svg").Length() != 1 {
		t.Errorf("empty = %q", got)
	}
}

func TestQueuePaging(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	tests := []struct {
		path      string
		rangeText string
		rows      int
		first     string
	}{
		{"/c/main/queues/default?page=2", "21–25 of 25", 5, "p20"},
		{"/c/main/queues/default?page=9", "21–25 of 25", 5, "p20"}, // past the end: the last page
		{"/c/main/queues/default?size=50", "1–25 of 25", 25, "p00"},
		{"/c/main/queues/default?size=7&page=x", "1–20 of 25", 20, "p00"},
		{"/c/main/queues/default?state=bogus", "1–20 of 25", 20, "p00"},
		{"/c/main/queues/low?state=retry", "0 of 0", 0, ""},
		{"/c/main/queues/low?state=retry&page=5", "0 of 0", 0, ""}, // no tasks: page 1
		{"/c/main/queues/default?page=99999999999999999", "21–25 of 25", 5, "p20"},
	}
	for _, tt := range tests {
		doc := parse(t, do(t, h, request{path: tt.path}))
		if got := text(doc.Find("#tasks-pager span").First()); got != tt.rangeText {
			t.Errorf("%s: range = %q, want %q", tt.path, got, tt.rangeText)
		}
		rows := doc.Find("#tasks-rows tr:not(.empty-row)")
		if rows.Length() != tt.rows {
			t.Errorf("%s: %d rows, want %d", tt.path, rows.Length(), tt.rows)
		}
		if tt.first != "" && text(rows.First().Find("a.row-link")) != tt.first {
			t.Errorf("%s: first = %q", tt.path, text(rows.First().Find("a.row-link")))
		}
	}
	doc := parse(t, do(t, h, request{path: "/c/main/queues/low?state=retry"}))
	if got := text(doc.Find("#tasks-rows")); got != "No retry tasks" {
		t.Errorf("empty = %q", got)
	}
	// The tabs keep the size and reset the page.
	doc = parse(t, do(t, h, request{path: "/c/main/queues/default?size=50&page=2"}))
	if href := doc.Find("#tab-retry").AttrOr("href", ""); href != "/c/main/queues/default?size=50&state=retry" {
		t.Errorf("tab href = %q", href)
	}
	if doc.Find(`#tasks-size-form input[type=hidden]`).Length() != 0 {
		t.Error("size form carries the default state")
	}
}

func TestQueueFragments(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	oob := func(doc *goquery.Document) []string {
		var ids []string
		doc.Find("[hx-swap-oob]").Each(func(_ int, s *goquery.Selection) { ids = append(ids, s.AttrOr("id", "")) })
		return ids
	}
	user := parse(t, do(t, h, request{path: "/c/main/queues/default?state=retry", htmx: true}))
	if diff := cmp.Diff([]string{"queue-poll", "queue-badge", "queue-toggle", "queue-banner", "queue-stats", "task-card", "updated-at", "sidebar-conns"}, oob(user)); diff != "" {
		t.Errorf("user request regions (-want +got):\n%s", diff)
	}
	if user.Find("#task-card .tabs a").Length() != 7 || user.Find(".side, header.top").Length() != 0 {
		t.Error("user answer is not the card fragment")
	}
	poll := parse(t, do(t, h, request{path: "/c/main/queues/default", htmx: true, header: map[string]string{"HX-Trigger": "queue-poll"}}))
	ids := oob(poll)
	for _, want := range []string{"tab-count-pending", "tasks-table", "tasks-banner", "tasks-pager"} {
		if !strings.Contains(strings.Join(ids, " "), want) {
			t.Errorf("poll lacks %s", want)
		}
	}
	for _, not := range []string{"task-card"} {
		if strings.Contains(strings.Join(ids, " "), not) {
			t.Errorf("poll replaces %s", not)
		}
	}
	if poll.Find(".tabs, .tbar, dialog, #tasks-size").Length() != 0 {
		t.Error("poll replaces user controls")
	}
	if poll.Find("#queue-poll").AttrOr("hx-get", "") != "/c/main/queues/default" {
		t.Error("poll URL")
	}
}

func TestQueueUnavailable(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		h, _ := newTestServer(t, adminOpts())
		rec := do(t, h, request{path: "/c/main/queues/nope"})
		if rec.Code != http.StatusNotFound || !strings.Contains(text(parse(t, rec).Find("main")), "Queue not found") {
			t.Errorf("unknown queue = %d %q", rec.Code, text(parse(t, rec).Find("main")))
		}
		// A poll of a queue deleted meanwhile replaces the page region and
		// stops polling.
		rec = do(t, h, request{path: "/c/main/queues/nope", htmx: true, header: map[string]string{"HX-Trigger": "queue-poll"}})
		frag := parse(t, rec)
		if rec.Code != http.StatusOK || !strings.Contains(text(frag.Find("#queue-page[hx-swap-oob]")), "Queue not found") ||
			frag.Find("[data-poll], [hx-get='']").Length() != 0 {
			t.Errorf("fragment = %d %q", rec.Code, rec.Body)
		}
		// So does an action on it, which shows its toasts too.
		rec = do(t, h, request{method: http.MethodPost, path: "/c/main/queues/nope/tasks/archive", form: "id=p00&confirm=1", htmx: true})
		frag = parse(t, rec)
		if !strings.Contains(text(frag.Find("#queue-page")), "Queue not found") || frag.Find("[hx-get='']").Length() != 0 ||
			!strings.Contains(text(frag.Find(".toast-body")), "not found") {
			t.Errorf("action = %q", rec.Body)
		}
	})
	t.Run("connection down", func(t *testing.T) {
		conns := defaultConns()
		conns.queues["analytics"] = defaultQueues()
		h, _ := newTestServer(t, testOpts{conns: conns})
		doc := parse(t, do(t, h, request{path: "/c/analytics/queues/default"}))
		if got := text(doc.Find("#task-card .conn-error")); !strings.Contains(got, "Connection failed") || !strings.Contains(got, "dial tcp") {
			t.Errorf("down = %q", got)
		}
		if !strings.Contains(doc.Find("#queue-poll").AttrOr("hx-get", ""), "poll=init") {
			t.Error("a page without data must poll with poll=init")
		}
	})
	t.Run("poll keeps the table", func(t *testing.T) {
		conns := defaultConns()
		conns.tasks.readErr = errors.New("READONLY")
		h, _ := newTestServer(t, testOpts{conns: conns})
		poll := parse(t, do(t, h, request{path: "/c/main/queues/default", htmx: true, header: map[string]string{"HX-Trigger": "queue-poll"}}))
		if poll.Find("#tasks-table, #task-card").Length() != 0 {
			t.Error("a failing poll replaced the table")
		}
		if got := text(poll.Find("#queue-banner")); !strings.Contains(got, "READONLY") {
			t.Errorf("banner = %q", got)
		}
	})
	t.Run("checking", func(t *testing.T) {
		conns := defaultConns()
		conns.tasks.block = true
		_, h, _ := newTestDepsBudget(t, testOpts{conns: conns})
		doc := parse(t, do(t, h, request{path: "/c/main/queues/default"}))
		if got := text(doc.Find("#task-card")); got != "Checking" {
			t.Errorf("checking = %q", got)
		}
	})
}

// newTestDepsBudget is newTestDeps with a short page budget.
func newTestDepsBudget(t *testing.T, o testOpts) (*deps, http.Handler, *fakeConns) {
	t.Helper()
	d, h, c := newTestDeps(t, o)
	d.pageBudget = 50e6 // 50ms
	return d, h, c
}

func TestQueueRoles(t *testing.T) {
	tests := []struct {
		role         config.Role
		archive, del string // "" enabled, else the disabled title
		toggle       string
	}{
		{config.RoleViewer, "Requires operator", "Requires admin", "Requires operator"},
		{config.RoleOperator, "", "Requires admin", ""},
		{config.RoleAdmin, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			h, _ := newTestServer(t, testOpts{authz: config.Authz{Default: tt.role}})
			doc := parse(t, do(t, h, request{path: "/c/main/queues/default"}))
			check := func(sel, title string) {
				t.Helper()
				b := doc.Find(sel)
				_, disabled := b.Attr("disabled")
				if disabled != (title != "") || b.AttrOr("title", "") != title {
					h, _ := goquery.OuterHtml(b)
					t.Errorf("%s: %s", sel, h)
				}
			}
			check("#act-archive", tt.archive)
			check("#act-delete", tt.del)
			check("#queue-toggle-btn", tt.toggle)
		})
	}
}

func TestQueuePathEscaping(t *testing.T) {
	conns := defaultConns()
	conns.queues["main"] = append(conns.queues["main"], Queue{Name: "a/b c"})
	conns.tasks.tasks = append(conns.tasks.tasks, Task{ID: "x/y?z", Queue: "a/b c", Type: "t", State: TaskPending})
	h, _ := newTestServer(t, testOpts{conns: conns, basePath: "/asynq", authz: config.Authz{Default: config.RoleAdmin}})
	rec := do(t, h, request{path: "/asynq/c/main/queues/a%2Fb%20c"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	doc := parse(t, rec)
	if got := doc.Find("#tasks-rows a.row-link").AttrOr("href", ""); got != "/asynq/c/main/queues/a%2Fb%20c/tasks/x%2Fy%3Fz" {
		t.Errorf("task link = %q", got)
	}
	if got := doc.Find("#find-open").AttrOr("href", ""); got != "/asynq/c/main/queues/a%2Fb%20c/find" {
		t.Errorf("find = %q", got)
	}
	if got := doc.Find("#act-archive").AttrOr("formaction", ""); got != "/asynq/c/main/queues/a%2Fb%20c/tasks/archive" {
		t.Errorf("action = %q", got)
	}
	if got := doc.Find("#tab-retry").AttrOr("href", ""); got != "/asynq/c/main/queues/a%2Fb%20c?state=retry" {
		t.Errorf("tab = %q", got)
	}
	if got := text(doc.Find(".crumbs [aria-current]")); got != "a/b c" {
		t.Errorf("crumb = %q", got)
	}
	// The task page and the lookup work with the escaped segments.
	if rec := do(t, h, request{path: "/asynq/c/main/queues/a%2Fb%20c/tasks/x%2Fy%3Fz"}); rec.Code != http.StatusOK {
		t.Errorf("task page = %d", rec.Code)
	}
	rec = do(t, h, request{path: "/asynq/c/main/queues/a%2Fb%20c/find?id=" + url.QueryEscape("x/y?z")})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != "/asynq/c/main/queues/a%2Fb%20c/tasks/x%2Fy%3Fz" {
		t.Errorf("find = %d %q", rec.Code, loc)
	}
}

func TestQueueFocusablesHaveIDs(t *testing.T) {
	h, _ := newTestServer(t, adminOpts())
	for _, path := range []string{"/c/main/queues/default", "/c/main/queues/default?state=retry"} {
		doc := parse(t, do(t, h, request{path: path}))
		seen := map[string]bool{}
		doc.Find("[id]").Each(func(_ int, s *goquery.Selection) {
			id := s.AttrOr("id", "")
			if seen[id] {
				t.Errorf("%s: duplicate id %q", path, id)
			}
			seen[id] = true
		})
		for _, rq := range []request{
			{path: path, htmx: true, header: map[string]string{"HX-Trigger": "queue-poll"}},
			{path: path, htmx: true},
		} {
			poll := parse(t, do(t, h, rq))
			poll.Find("[hx-swap-oob]").Each(func(_ int, region *goquery.Selection) {
				region.Find("a[href], button, input:not([type=hidden]), select, textarea, summary, [tabindex]").Each(func(_ int, s *goquery.Selection) {
					if s.AttrOr("id", "") == "" {
						h, _ := goquery.OuterHtml(s)
						t.Errorf("%s: focusable in #%s without id: %s", path, region.AttrOr("id", ""), h)
					}
				})
			})
		}
	}
}

func TestQueuePauseFromQueuePage(t *testing.T) {
	h, conns := newTestServer(t, adminOpts())
	form := "queue=default&from=queue&next=" + url.QueryEscape("/c/main/queues/default?state=retry")
	rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/pause", form: form})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != "/c/main/queues/default?state=retry" {
		t.Errorf("no-JS pause = %d %q", rec.Code, loc)
	}
	// Already paused: skipped, as on the Connection page.
	do(t, h, request{method: http.MethodPost, path: "/c/main/queues/pause", form: form})
	if diff := cmp.Diff([]string{"pause main/default"}, conns.actions()); diff != "" {
		t.Errorf("actions (-want +got):\n%s", diff)
	}
	doc := parse(t, do(t, h, request{method: http.MethodPost, path: "/c/main/queues/resume", form: form, htmx: true}))
	if text(doc.Find("#queue-badge")) != "Running" || text(doc.Find("#queue-toggle button")) != "Pause queue" ||
		doc.Find("#tab-retry.on").Length() != 1 {
		t.Error("htmx resume does not answer for the Queue page")
	}
}
