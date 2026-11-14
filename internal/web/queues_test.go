package web

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/google/go-cmp/cmp"

	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/conn"
)

// rowNames lists the queue names of the table, in order.
func rowNames(doc *goquery.Document) []string {
	var out []string
	doc.Find("#queues-rows tr[data-state] td:nth-child(2) a").Each(func(_ int, s *goquery.Selection) {
		out = append(out, s.Text())
	})
	return out
}

func TestQueuesPage(t *testing.T) {
	h, _ := newTestServer(t, testOpts{authz: config.Authz{Default: config.RoleAdmin}})
	rec := do(t, h, request{path: "/c/main"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	doc := parse(t, rec)
	if doc.Find("aside.side").Length() != 1 {
		t.Fatal("no shell")
	}
	if got := text(doc.Find("main h1")); got != "Queues" {
		t.Errorf("h1 = %q", got)
	}
	if got := text(doc.Find(".head-title .mono")); got != "redis-main.internal:6379/0" {
		t.Errorf("address = %q", got)
	}
	if got := text(doc.Find("#conn-health .badge")); got != "Healthy" {
		t.Errorf("badge = %q", got)
	}
	var crumbs []string
	doc.Find(".crumbs li").Each(func(_ int, s *goquery.Selection) { crumbs = append(crumbs, text(s)) })
	if strings.Join(crumbs, " / ") != "Overview / main" {
		t.Errorf("crumbs = %q", crumbs)
	}
	if got := text(doc.Find(".conn-current .subnav a.on")); got != "Queues" {
		t.Errorf("active sub-nav = %q", got)
	}

	// Default sort: Size desc.
	if diff := cmp.Diff([]string{"default", "low", "image-resize", "critical"}, rowNames(doc)); diff != "" {
		t.Errorf("rows (-want +got):\n%s", diff)
	}
	if v, _ := doc.Find("th:has(#sort-size)").Attr("aria-sort"); v != "descending" {
		t.Errorf("size aria-sort = %q", v)
	}
	if v, _ := doc.Find("th:has(#sort-name)").Attr("aria-sort"); v != "none" {
		t.Errorf("name aria-sort = %q", v)
	}
	var heads []string
	doc.Find("#queues thead th").Each(func(_ int, s *goquery.Selection) { heads = append(heads, text(s)) })
	wantHeads := []string{"Select all queues", "Queue", "State", "Size", "Pending", "Active", "Scheduled", "Retry", "Archived", "Latency", "Memory", "Processed today", "Failed today"}
	if diff := cmp.Diff(wantHeads, heads); diff != "" {
		t.Errorf("headers (-want +got):\n%s", diff)
	}

	// A row: state badge, numbers, latency, memory; colors by class.
	row := doc.Find("#queues-rows tr").FilterFunction(func(_ int, s *goquery.Selection) bool {
		return strings.Contains(s.Find("td:nth-child(2)").Text(), "low")
	})
	var cells []string
	row.Find("td").Each(func(_ int, s *goquery.Selection) { cells = append(cells, text(s)) })
	want := []string{"Select queue low", "low", "Running", "160", "8", "0", "105", "2", "45", "48.0s", "9.0 MB", "10,365", "8"}
	if diff := cmp.Diff(want, cells); diff != "" {
		t.Errorf("low row (-want +got):\n%s", diff)
	}
	for i, class := range map[int]string{8: "c-retry", 9: "c-archived", 10: "c-failed", 13: "c-failed"} {
		if !row.Find(fmt.Sprintf("td:nth-child(%d)", i)).HasClass(class) {
			t.Errorf("low column %d lacks %s", i, class)
		}
	}
	paused := doc.Find(`#queues-rows tr[data-state=paused]`)
	if text(paused.Find(".badge-warn")) != "Paused" || text(paused.Find("td:nth-child(10)")) != "—" {
		t.Errorf("paused row = %q", text(paused))
	}

	// Total in tfoot: sums, latency "—".
	var foot []string
	doc.Find("#queues tfoot tr").Children().Each(func(_ int, s *goquery.Selection) { foot = append(foot, text(s)) })
	wantFoot := []string{"", "Total", "", "1,731", "900", "31", "615", "21", "164", "—", "75.0 MB", "340,542", "759"}
	if diff := cmp.Diff(wantFoot, foot); diff != "" {
		t.Errorf("total (-want +got):\n%s", diff)
	}

	// Polling: a hidden element fetches the page into out-of-band swaps.
	poll := doc.Find("[data-poll]")
	for attr, want := range map[string]string{"id": "queues-poll", "hx-get": "/c/main", "hx-trigger": "asynqui-refresh", "hx-swap": "none"} {
		if got, _ := poll.Attr(attr); got != want {
			t.Errorf("poll %s = %q, want %q", attr, got, want)
		}
	}
	// Selection: checkboxes belong to the action form and survive swaps.
	doc.Find("#queues-rows input.cb, #qsel-all").Each(func(_ int, s *goquery.Selection) {
		if _, ok := s.Attr("hx-preserve"); !ok {
			t.Errorf("%v lacks hx-preserve", s.AttrOr("id", ""))
		}
	})
	if f, _ := doc.Find("#qsel-" + "64656661756c74").Attr("form"); f != "queues-form" {
		t.Errorf("checkbox form = %q", f)
	}
	if v, _ := doc.Find(`input[type=hidden][name=next]`).Attr("value"); v != "/c/main" {
		t.Errorf("next = %q", v)
	}
}

func TestQueuesSort(t *testing.T) {
	h, _ := newTestServer(t, testOpts{})
	tests := []struct {
		query    string
		rows     []string
		sortID   string
		aria     string
		nextHref string // the same column's link: toggles
		sizeHref string
	}{
		{"", []string{"default", "low", "image-resize", "critical"}, "sort-size", "descending", "/c/main?dir=asc&sort=size", "/c/main?dir=asc&sort=size"},
		{"?sort=name", []string{"critical", "default", "image-resize", "low"}, "sort-name", "ascending", "/c/main?dir=desc&sort=name", "/c/main"},
		{"?sort=name&dir=desc", []string{"low", "image-resize", "default", "critical"}, "sort-name", "descending", "/c/main?dir=asc&sort=name", "/c/main"},
		{"?sort=state", []string{"image-resize", "critical", "default", "low"}, "sort-state", "ascending", "/c/main?dir=desc&sort=state", "/c/main"},
		// Paused queues show no latency and sort last either way.
		{"?sort=latency&dir=asc", []string{"critical", "default", "low", "image-resize"}, "sort-latency", "ascending", "/c/main?dir=desc&sort=latency", "/c/main"},
		{"?sort=latency", []string{"low", "default", "critical", "image-resize"}, "sort-latency", "descending", "/c/main?dir=asc&sort=latency", "/c/main"},
		{"?sort=memory&dir=asc", []string{"critical", "low", "image-resize", "default"}, "sort-memory", "ascending", "/c/main?dir=desc&sort=memory", "/c/main"},
		// Ties break by name.
		{"?sort=active&dir=asc", []string{"image-resize", "low", "critical", "default"}, "sort-active", "ascending", "/c/main?dir=desc&sort=active", "/c/main"},
		// Other parameters are kept.
		{"?sort=failed&range=7d&queue=low", []string{"default", "critical", "low", "image-resize"}, "sort-failed", "descending",
			"/c/main?dir=asc&queue=low&range=7d&sort=failed", "/c/main?queue=low&range=7d"},
		// Unknown values fall back to the default.
		{"?sort=bogus&dir=sideways", []string{"default", "low", "image-resize", "critical"}, "sort-size", "descending", "/c/main?dir=asc&sort=size", "/c/main?dir=asc&sort=size"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			doc := parse(t, do(t, h, request{path: "/c/main" + tt.query}))
			if diff := cmp.Diff(tt.rows, rowNames(doc)); diff != "" {
				t.Errorf("rows (-want +got):\n%s", diff)
			}
			link := doc.Find("#" + tt.sortID)
			if aria, _ := link.Parent().Attr("aria-sort"); aria != tt.aria {
				t.Errorf("aria-sort = %q, want %q", aria, tt.aria)
			}
			if doc.Find(`th[aria-sort=ascending], th[aria-sort=descending]`).Length() != 1 {
				t.Error("more than one sorted column")
			}
			if href, _ := link.Attr("href"); href != tt.nextHref {
				t.Errorf("toggle href = %q, want %q", href, tt.nextHref)
			}
			if hx, _ := link.Attr("hx-get"); hx != tt.nextHref {
				t.Errorf("hx-get = %q", hx)
			}
			if tt.sortID != "sort-size" {
				if href, _ := doc.Find("#sort-size").Attr("href"); href != tt.sizeHref {
					t.Errorf("size href = %q, want %q", href, tt.sizeHref)
				}
			}
		})
	}
	// Pushing the URL makes the sort bookmarkable and the poll follows it.
	doc := parse(t, do(t, h, request{path: "/c/main?sort=name"}))
	if push, _ := doc.Find("#sort-name").Attr("hx-push-url"); push != "true" {
		t.Error("sort link does not push its URL")
	}
	if u, _ := doc.Find("#queues-poll").Attr("hx-get"); u != "/c/main?dir=asc&sort=name" {
		t.Errorf("poll URL = %q", u)
	}
}

// TestQueuesFragment: with HX-Request the answer is out-of-band swaps only,
// of regions that exist on the full page; the toolbar, filter and dialog
// are never replaced.
func TestQueuesFragment(t *testing.T) {
	h, _ := newTestServer(t, testOpts{})
	full := parse(t, do(t, h, request{path: "/c/main?sort=name"}))
	rec := do(t, h, request{path: "/c/main?sort=name", htmx: true})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "<html") || strings.Contains(body, "<aside") {
		t.Fatalf("fragment = %d, has shell", rec.Code)
	}
	doc := parse(t, rec)
	var ids []string
	doc.Find("body").Children().Each(func(_ int, s *goquery.Selection) {
		id, _ := s.Attr("id")
		if oob, _ := s.Attr("hx-swap-oob"); oob != "true" {
			t.Errorf("top-level <%s id=%q> is not out of band", goquery.NodeName(s), id)
		}
		if full.Find("#"+id).Length() != 1 {
			t.Errorf("oob id %q is not on the page", id)
		}
		ids = append(ids, id)
	})
	if got := strings.Join(ids, " "); got != "queues-poll conn-health conn-state queues-table throughput updated-at sidebar-conns" {
		t.Errorf("oob ids = %q", got)
	}
	if diff := cmp.Diff([]string{"critical", "default", "image-resize", "low"}, rowNames(doc)); diff != "" {
		t.Errorf("fragment rows (-want +got):\n%s", diff)
	}
	for _, sel := range []string{".tbar", "#queues-filter", "dialog", "form#queues-form"} {
		if doc.Find(sel).Length() != 0 {
			t.Errorf("fragment replaces %s", sel)
		}
	}
	// A history restore gets the full page.
	if r := do(t, h, request{path: "/c/main", htmx: true, header: map[string]string{"HX-History-Restore-Request": "true"}}); !strings.Contains(r.Body.String(), "<html") {
		t.Error("history restore got a fragment")
	}
}

func TestQueuesFocusablesHaveIDs(t *testing.T) {
	conns := defaultConns()
	conns.queues["main"] = append(conns.queues["main"], conn.Queue{Name: "a/b c"})
	h, _ := newTestServer(t, testOpts{conns: conns, authz: config.Authz{Default: config.RoleAdmin}})
	doc := parse(t, do(t, h, request{path: "/c/main"}))
	seen := map[string]bool{}
	doc.Find("[id]").Each(func(_ int, s *goquery.Selection) {
		id, _ := s.Attr("id")
		if seen[id] {
			t.Errorf("duplicate id %q", id)
		}
		seen[id] = true
	})
	poll := parse(t, do(t, h, request{path: "/c/main", htmx: true}))
	n := 0
	poll.Find("[hx-swap-oob]").Each(func(_ int, region *goquery.Selection) {
		id, _ := region.Attr("id")
		doc.Find(`[id="` + id + `"]`).Find("a[href], button, input:not([type=hidden]), select, textarea, summary, [tabindex]").Each(func(_ int, s *goquery.Selection) {
			n++
			if v, ok := s.Attr("id"); !ok || v == "" {
				h, _ := goquery.OuterHtml(s)
				t.Errorf("focusable in #%s without id: %s", id, h)
			}
		})
	})
	if n < 30 {
		t.Errorf("only %d focusables found", n)
	}
}

func TestQueuesPathEscaping(t *testing.T) {
	conns := defaultConns()
	conns.queues["main"] = []conn.Queue{{Name: "a/b c"}, {Name: "x?y#z"}}
	h, _ := newTestServer(t, testOpts{conns: conns, basePath: "/asynq"})
	doc := parse(t, do(t, h, request{path: "/asynq/c/main?sort=name"}))
	var hrefs, ids []string
	doc.Find("#queues-rows a.row-link").Each(func(_ int, s *goquery.Selection) {
		hrefs = append(hrefs, s.AttrOr("href", ""))
		ids = append(ids, s.AttrOr("id", ""))
	})
	if diff := cmp.Diff([]string{"/asynq/c/main/queues/a%2Fb%20c", "/asynq/c/main/queues/x%3Fy%23z"}, hrefs); diff != "" {
		t.Errorf("hrefs (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"qlink-612f622063", "qlink-783f79237a"}, ids); diff != "" {
		t.Errorf("ids (-want +got):\n%s", diff)
	}
	if v, _ := doc.Find("#qsel-612f622063").Attr("value"); v != "a/b c" {
		t.Errorf("checkbox value = %q", v)
	}
	// The picked chart queue round-trips through the query.
	doc = parse(t, do(t, h, request{path: "/asynq/c/main?queue=" + url.QueryEscape("a/b c")}))
	if v, _ := doc.Find("#tp-queue option[selected]").Attr("value"); v != "a/b c" {
		t.Errorf("picked = %q", v)
	}
	if v, _ := doc.Find(`#tp-range input[name=queue]`).Attr("value"); v != "a/b c" {
		t.Errorf("range form queue = %q", v)
	}
}

func TestQueuesToolbarRoles(t *testing.T) {
	tests := []struct {
		role                 config.Role
		pause, resume, del   bool
		pauseTitle, delTitle string
	}{
		{config.RoleViewer, false, false, false, "Requires operator", "Requires admin"},
		{config.RoleOperator, true, true, false, "", "Requires admin"},
		{config.RoleAdmin, true, true, true, "", ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			h, _ := newTestServer(t, testOpts{authz: config.Authz{Default: tt.role}})
			doc := parse(t, do(t, h, request{path: "/c/main"}))
			check := func(id string, allowed bool, title string) {
				t.Helper()
				b := doc.Find("#" + id)
				_, disabled := b.Attr("disabled")
				if disabled == allowed {
					t.Errorf("%s disabled = %v", id, disabled)
				}
				if got, _ := b.Attr("title"); got != title {
					t.Errorf("%s title = %q, want %q", id, got, title)
				}
				if _, ok := b.Attr("data-enable"); ok != allowed {
					t.Errorf("%s data-enable present = %v", id, ok)
				}
				if b.Find(".lock").Length() != 0 || b.Find("svg").Length() != 1 {
					t.Errorf("%s icons = %d", id, b.Find("svg").Length())
				}
			}
			check("act-pause", tt.pause, tt.pauseTitle)
			check("act-resume", tt.resume, tt.pauseTitle)
			check("act-delete", tt.del, tt.delTitle)
			if tt.del {
				d := doc.Find("#act-delete")
				if c, _ := d.Attr("data-confirm"); c != "queues-delete" {
					t.Errorf("delete confirm = %q", c)
				}
				if _, ok := d.Attr("hx-post"); ok {
					t.Error("delete posts without the dialog")
				}
				if a, _ := d.Attr("formaction"); a != "/c/main/queues/delete" {
					t.Errorf("delete formaction = %q", a)
				}
				// Only empty queues: every selected row must be data-empty.
				for attr, want := range map[string]string{"data-enable": "every", "data-flag": "empty", "data-blocked-title": "Queue is not empty"} {
					if got, _ := d.Attr(attr); got != want {
						t.Errorf("delete %s = %q, want %q", attr, got, want)
					}
				}
				confirm := doc.Find("dialog#queues-delete #queues-delete-confirm")
				for attr, want := range map[string]string{"hx-post": "/c/main/queues/delete", "name": "confirm", "value": "1", "form": "queues-form"} {
					if got, _ := confirm.Attr(attr); got != want {
						t.Errorf("confirm %s = %q, want %q", attr, got, want)
					}
				}
			}
			if tt.pause {
				p := doc.Find("#act-pause")
				for attr, want := range map[string]string{"hx-post": "/c/main/queues/pause", "data-enable": "state", "data-state": "running", "form": "queues-form"} {
					if got, _ := p.Attr(attr); got != want {
						t.Errorf("pause %s = %q, want %q", attr, got, want)
					}
				}
				if s, _ := doc.Find("#act-resume").Attr("data-state"); s != "paused" {
					t.Errorf("resume data-state = %q", s)
				}
			}
		})
	}
}

func TestQueueActionsRoles(t *testing.T) {
	az := config.Authz{Default: config.RoleNone, Bindings: []config.Binding{
		{Users: []string{"vic"}, Role: config.RoleViewer, Connections: []string{"main"}},
		{Users: []string{"olga"}, Role: config.RoleOperator, Connections: []string{"main"}},
		{Users: []string{"ada"}, Role: config.RoleAdmin, Connections: []string{"main"}},
	}}
	tests := []struct {
		user, path, form string
		want             int
		acted            []string
	}{
		{"vic", "/c/main/queues/pause", "queue=default", http.StatusForbidden, nil},
		{"vic", "/c/main/queues/resume", "queue=image-resize", http.StatusForbidden, nil},
		{"olga", "/c/main/queues/pause", "queue=default", http.StatusSeeOther, []string{"pause main/default"}},
		{"olga", "/c/main/queues/resume", "queue=image-resize", http.StatusSeeOther, []string{"resume main/image-resize"}},
		{"olga", "/c/main/queues/delete", "queue=image-resize&confirm=1", http.StatusForbidden, nil},
		{"ada", "/c/main/queues/delete", "queue=image-resize&confirm=1", http.StatusSeeOther, []string{"delete main/image-resize"}},
		{"ada", "/c/analytics/queues/pause", "queue=default", http.StatusNotFound, nil}, // hidden
	}
	for _, tt := range tests {
		t.Run(tt.user+" "+tt.path, func(t *testing.T) {
			h, conns := newTestServer(t, testOpts{provider: config.ProviderProxy, authz: az})
			rec := do(t, h, request{method: http.MethodPost, path: tt.path, user: tt.user, form: tt.form})
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d", rec.Code, tt.want)
			}
			if diff := cmp.Diff(tt.acted, conns.actions()); diff != "" {
				t.Errorf("acted (-want +got):\n%s", diff)
			}
		})
	}
}

func TestQueueActions(t *testing.T) {
	admin := testOpts{authz: config.Authz{Default: config.RoleAdmin}}
	t.Run("pause acts on the running ones and keeps the state", func(t *testing.T) {
		h, conns := newTestServer(t, admin)
		rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/pause",
			form: "queue=default&queue=image-resize&queue=default&queue=&next=" + url.QueryEscape("/c/main?sort=name&range=7d")})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/c/main?dir=asc&range=7d&sort=name" {
			t.Fatalf("= %d %q", rec.Code, rec.Header().Get("Location"))
		}
		if diff := cmp.Diff([]string{"pause main/default"}, conns.actions()); diff != "" {
			t.Errorf("acted (-want +got):\n%s", diff)
		}
	})
	t.Run("next cannot redirect elsewhere", func(t *testing.T) {
		h, _ := newTestServer(t, admin)
		for _, next := range []string{"https://evil.example/?sort=name", "//evil.example/c/main", "/c/other?sort=name"} {
			rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/resume", form: "queue=image-resize&next=" + url.QueryEscape(next)})
			if loc := rec.Header().Get("Location"); loc != "/c/main?dir=asc&sort=name" && loc != "/c/main" {
				t.Errorf("next %q → %q", next, loc)
			}
		}
	})
	t.Run("htmx answer re-renders the table", func(t *testing.T) {
		h, _ := newTestServer(t, admin)
		rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/pause", htmx: true, form: "queue=default&next=%2Fc%2Fmain"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		doc := parse(t, rec)
		if s, _ := doc.Find(`#queues-rows tr:has(a:contains("default"))`).Attr("data-state"); s != "paused" {
			t.Errorf("default state after pause = %q", s)
		}
		if doc.Find("#toasts, [hx-swap-oob^=beforeend]").Length() != 0 {
			t.Error("toast on success")
		}
	})
	t.Run("failures are toasts", func(t *testing.T) {
		conns := defaultConns()
		conns.actErrs["default"] = fmt.Errorf("%w: queue=%q", conn.ErrQueueNotEmpty, "default")
		conns.actErrs["low"] = errors.New("READONLY You can't write against a read only replica.") //nolint:revive // Redis's own text
		h, _ := newTestServer(t, testOpts{conns: conns, authz: config.Authz{Default: config.RoleAdmin}})
		form := "confirm=1&queue=default&queue=low&queue=gone&queue=critical"
		rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/delete", htmx: true, form: form})
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		doc := parse(t, rec)
		region := doc.Find(`[hx-swap-oob="beforeend:#toasts"]`)
		var toasts []string
		region.Find(".toast .toast-body").Each(func(_ int, s *goquery.Selection) { toasts = append(toasts, text(s)) })
		want := []string{
			"Queue default is not empty",
			"Could not delete low READONLY You can't write against a read only replica.",
			"Queue gone not found",
		}
		if diff := cmp.Diff(want, toasts); diff != "" {
			t.Errorf("toasts (-want +got):\n%s", diff)
		}
		if region.Find(".toast-detail.mono").Length() != 1 {
			t.Error("error detail not in mono")
		}
		if names := rowNames(doc); slices.Contains(names, "critical") || !slices.Contains(names, "default") {
			t.Errorf("rows after delete = %q", names)
		}

		// Without JS: post, redirect, get. The failures ride in a
		// short-lived cookie that the page shows once.
		conns.actErrs["low"] = errors.New("boom")
		rec = do(t, h, request{method: http.MethodPost, path: "/c/main/queues/delete", form: "confirm=1&queue=low&queue=default&next=%2Fc%2Fmain%3Fsort%3Dname"})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/c/main?dir=asc&sort=name" {
			t.Fatalf("no-JS failure = %d %q", rec.Code, rec.Header().Get("Location"))
		}
		var flash *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == flashCookie {
				flash = c
			}
		}
		if flash == nil || flash.Path != "/c/main" || flash.MaxAge != flashMaxAge || !flash.HttpOnly || flash.SameSite != http.SameSiteLaxMode {
			t.Fatalf("flash cookie = %+v", flash)
		}
		get := do(t, h, request{path: rec.Header().Get("Location"), cookies: []*http.Cookie{flash}})
		page := parse(t, get)
		var shown []string
		page.Find("#toasts .toast-body").Each(func(_ int, s *goquery.Selection) { shown = append(shown, text(s)) })
		if diff := cmp.Diff([]string{"Could not delete low boom", "Queue default is not empty"}, shown); diff != "" {
			t.Errorf("toasts (-want +got):\n%s", diff)
		}
		cleared := false
		for _, c := range get.Result().Cookies() {
			cleared = cleared || (c.Name == flashCookie && c.MaxAge < 0)
		}
		if !cleared {
			t.Error("flash cookie not cleared after showing")
		}
		if page.Find("#toasts [data-toast-close].js-only").Length() != 2 {
			t.Error("toast close buttons are not JS-only")
		}
		// Success sets no cookie.
		rec = do(t, h, request{method: http.MethodPost, path: "/c/main/queues/resume", form: "queue=image-resize"})
		if len(rec.Result().Cookies()) != 0 {
			t.Error("success set a cookie")
		}
	})
	t.Run("flash cookie is display data only", func(t *testing.T) {
		h, _ := newTestServer(t, admin)
		for _, v := range []string{"%%%", base64.RawURLEncoding.EncodeToString([]byte(`[{"v":9,"k":0,"q":"x"}]`)),
			base64.RawURLEncoding.EncodeToString([]byte(`[]`)), strings.Repeat("A", flashMaxBytes+1)} {
			doc := parse(t, do(t, h, request{path: "/c/main", cookies: []*http.Cookie{{Name: flashCookie, Value: v}}}))
			if doc.Find("#toasts .toast").Length() != 0 {
				t.Errorf("cookie %.20q showed a toast", v)
			}
		}
		v := base64.RawURLEncoding.EncodeToString([]byte(`[{"v":0,"k":0,"q":"<b>x</b>","d":"<script>"}]`))
		rec := do(t, h, request{path: "/c/main", cookies: []*http.Cookie{{Name: flashCookie, Value: v}}})
		if strings.Contains(rec.Body.String(), "<b>x</b>") || text(parse(t, rec).Find("#toasts .toast-body")) != "Could not pause <b>x</b> <script>" {
			t.Errorf("flash text not escaped: %q", text(parse(t, rec).Find("#toasts")))
		}
	})
	t.Run("delete without confirm asks on a page", func(t *testing.T) {
		h, conns := newTestServer(t, admin)
		rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/delete", form: "queue=low&queue=a%2Fb&next=%2Fc%2Fmain%3Fsort%3Dname"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		doc := parse(t, rec)
		if got := text(doc.Find("main h1")); got != "Delete queues" {
			t.Errorf("h1 = %q", got)
		}
		var listed, hidden []string
		doc.Find(".confirm-card li").Each(func(_ int, s *goquery.Selection) { listed = append(listed, s.Text()) })
		doc.Find(`.confirm-card input[name=queue]`).Each(func(_ int, s *goquery.Selection) { hidden = append(hidden, s.AttrOr("value", "")) })
		if diff := cmp.Diff([]string{"low", "a/b"}, listed); diff != "" || !slices.Equal(listed, hidden) {
			t.Errorf("listed %q hidden %q", listed, hidden)
		}
		form := doc.Find(".confirm-card form")
		if a, _ := form.Attr("action"); a != "/c/main/queues/delete" || form.Find(`input[name=confirm][value="1"]`).Length() != 1 {
			t.Errorf("confirm form action %q", a)
		}
		if href, _ := doc.Find(".confirm-card a.btn").Attr("href"); href != "/c/main?dir=asc&sort=name" {
			t.Errorf("cancel = %q", href)
		}
		if len(conns.actions()) != 0 {
			t.Errorf("deleted before confirming: %q", conns.actions())
		}
		var crumbs []string
		doc.Find(".crumbs li").Each(func(_ int, s *goquery.Selection) { crumbs = append(crumbs, text(s)) })
		if strings.Join(crumbs, " / ") != "Overview / main / Delete queues" {
			t.Errorf("crumbs = %q", crumbs)
		}

		// htmx always confirms in the dialog; nothing selected goes back.
		if rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/delete", htmx: true, form: "queue=low"}); rec.Code != http.StatusBadRequest {
			t.Errorf("htmx without confirm = %d", rec.Code)
		}
		if rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/delete", form: ""}); rec.Code != http.StatusSeeOther {
			t.Errorf("empty delete = %d", rec.Code)
		}
	})
	t.Run("bad form", func(t *testing.T) {
		h, _ := newTestServer(t, admin)
		if rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/pause", form: "%zz"}); rec.Code != http.StatusBadRequest {
			t.Errorf("= %d", rec.Code)
		}
	})
}

func TestQueueActionsCSRF(t *testing.T) {
	h, conns := newTestServer(t, testOpts{authz: config.Authz{Default: config.RoleAdmin}})
	for _, path := range []string{"/c/main/queues/pause", "/c/main/queues/resume", "/c/main/queues/delete"} {
		for _, hdr := range []map[string]string{{"Sec-Fetch-Site": "cross-site"}, {"Origin": "https://evil.example"}} {
			rec := do(t, h, request{method: http.MethodPost, path: path, form: "queue=default&queue=image-resize&confirm=1", header: hdr})
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s with %v = %d", path, hdr, rec.Code)
			}
		}
	}
	if a := conns.actions(); len(a) != 0 {
		t.Errorf("cross-origin posts acted: %q", a)
	}
}

// TestQueuesUnavailable has the Connection page's own cases; see
// TestInfraUnavailable for the ones every connection page shares.
func TestQueuesUnavailable(t *testing.T) {
	t.Run("connection down", func(t *testing.T) {
		h, _ := newTestServer(t, testOpts{})
		doc := parse(t, do(t, h, request{path: "/c/analytics"}))
		if doc.Find("aside.side").Length() != 1 || text(doc.Find("main h1")) != "Queues" {
			t.Fatal("not inside the layout")
		}
		if text(doc.Find("#conn-health")) != "Unreachable" || doc.Find("#queues").Length() != 0 {
			t.Error("badge or table wrong")
		}
		if _, hidden := doc.Find("#throughput").Attr("hidden"); !hidden {
			t.Error("chart shown without data")
		}
	})
	t.Run("error reply: inline, the connection stays up", func(t *testing.T) {
		conns := defaultConns()
		conns.queueErrs = map[string]error{"main": errors.New("NOAUTH Authentication required.")} //nolint:revive // Redis's own text
		h, _ := newTestServer(t, testOpts{conns: conns})
		doc := parse(t, do(t, h, request{path: "/c/main"}))
		if title, msg := text(doc.Find("#queues-table .conn-error-title")), text(doc.Find("#queues-table .conn-error-text")); title != "Could not load queues" ||
			msg != "NOAUTH Authentication required." {
			t.Errorf("error = %q %q", title, msg)
		}
		if doc.Find("#nav-main .dot-down").Length() != 0 || text(doc.Find("#conn-health")) == "Unreachable" {
			t.Error("an error reply shows the connection down")
		}
	})
	t.Run("unreachable: the badge is down", func(t *testing.T) {
		conns := defaultConns()
		conns.queueErrs = map[string]error{"main": unreachable("connection refused")}
		h, _ := newTestServer(t, testOpts{conns: conns})
		if got := text(parse(t, do(t, h, request{path: "/c/main"})).Find("#conn-health")); got != "Unreachable" {
			t.Errorf("badge = %q", got)
		}
	})
	t.Run("no queues", func(t *testing.T) {
		conns := defaultConns()
		conns.queues["main"] = nil
		conns.history["main"] = nil
		h, _ := newTestServer(t, testOpts{conns: conns})
		doc := parse(t, do(t, h, request{path: "/c/main"}))
		if got := text(doc.Find("#queues-rows td.empty")); got != "No queues" {
			t.Errorf("empty = %q", got)
		}
		if doc.Find("#queues tfoot").Length() != 0 {
			t.Error("Total row without queues")
		}
		if doc.Find("#tp-queue option").Length() != 1 {
			t.Error("picker lists queues")
		}
	})
	t.Run("history fails", func(t *testing.T) {
		conns := defaultConns()
		conns.historyErrs = map[string]error{"main": errors.New("history boom")}
		h, _ := newTestServer(t, testOpts{conns: conns})
		doc := parse(t, do(t, h, request{path: "/c/main"}))
		if got := text(doc.Find("#throughput .conn-error-text")); got != "history boom" {
			t.Errorf("chart error = %q", got)
		}
		if len(rowNames(doc)) != 4 {
			t.Error("table missing")
		}
	})
}

func TestQueuesThroughput(t *testing.T) {
	tests := []struct {
		query     string
		asked     []string
		days      int
		on        string
		picked    string
		hiddenQry string // the picker's hidden fields
		rangeQry  string // the range form's
	}{
		{"", []string{"/14"}, 14, "range-14", "", "", ""},
		{"?range=7d", []string{"/7"}, 7, "range-7", "", "range=7d", ""},
		{"?range=90d&queue=low", []string{"low/90"}, 90, "range-90", "low", "range=90d", "queue=low"},
		{"?range=30d&sort=name", []string{"/30"}, 30, "range-30", "", "dir=asc&range=30d&sort=name", "dir=asc&sort=name"},
		// Unknown values fall back: range 14, all queues. History starts
		// with the queues, so an unknown queue costs a second read.
		{"?range=13d&queue=nope", []string{"nope/14", "/14"}, 14, "range-14", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			h, conns := newTestServer(t, testOpts{})
			doc := parse(t, do(t, h, request{path: "/c/main" + tt.query}))
			if diff := cmp.Diff(tt.asked, conns.historyAsked); diff != "" {
				t.Errorf("History asked (-want +got):\n%s", diff)
			}
			card := doc.Find("#throughput")
			if got := text(card.Find("h2")); got != "Throughput" {
				t.Errorf("title = %q", got)
			}
			rf := card.Find("form#tp-range")
			if rf.Children().Filter(".seg[role=group][aria-label=Range]").Length() != 1 {
				t.Errorf("range buttons are not in a labeled group")
			}
			for attr, want := range map[string]string{"method": "get", "action": "/c/main", "hx-get": "/c/main", "hx-swap": "none", "hx-push-url": "true"} {
				if got, _ := rf.Attr(attr); got != want {
					t.Errorf("range form %s = %q, want %q", attr, got, want)
				}
			}
			var ranges []string
			rf.Find("button[type=submit][name=range]").Each(func(_ int, s *goquery.Selection) {
				ranges = append(ranges, text(s)+"="+s.AttrOr("value", "")+"/"+s.AttrOr("aria-pressed", ""))
			})
			want := map[string]string{"range-7": "7d=7d", "range-14": "14d=14d", "range-30": "30d=30d", "range-90": "90d=90d"}
			var wantRanges []string
			for _, id := range []string{"range-7", "range-14", "range-30", "range-90"} {
				wantRanges = append(wantRanges, want[id]+"/"+strconv.FormatBool(id == tt.on))
			}
			if diff := cmp.Diff(wantRanges, ranges); diff != "" {
				t.Errorf("ranges (-want +got):\n%s", diff)
			}
			if rf.Find("button.on").AttrOr("id", "") != tt.on || rf.Find("button.on").Length() != 1 {
				t.Errorf("on range = %q", rf.Find("button.on").AttrOr("id", ""))
			}
			rq := url.Values{}
			rf.Find("input[type=hidden]").Each(func(_ int, s *goquery.Selection) { rq.Add(s.AttrOr("name", ""), s.AttrOr("value", "")) })
			if rq.Encode() != tt.rangeQry {
				t.Errorf("range hidden = %q, want %q", rq.Encode(), tt.rangeQry)
			}
			if v := card.Find("#tp-queue option[selected]").AttrOr("value", "?"); v != tt.picked {
				t.Errorf("picked = %q", v)
			}
			var opts []string
			card.Find("#tp-queue option").Each(func(_ int, s *goquery.Selection) { opts = append(opts, text(s)) })
			if diff := cmp.Diff([]string{"All queues", "critical", "default", "image-resize", "low"}, opts); diff != "" {
				t.Errorf("options (-want +got):\n%s", diff)
			}
			q := url.Values{}
			card.Find("#tp-form input[type=hidden]").Each(func(_ int, s *goquery.Selection) { q.Add(s.AttrOr("name", ""), s.AttrOr("value", "")) })
			if q.Encode() != tt.hiddenQry {
				t.Errorf("picker hidden = %q, want %q", q.Encode(), tt.hiddenQry)
			}
			form := card.Find("#tp-form")
			for attr, want := range map[string]string{"method": "get", "action": "/c/main", "hx-get": "/c/main", "hx-trigger": "change", "hx-swap": "none", "hx-push-url": "true"} {
				if got, _ := form.Attr(attr); got != want {
					t.Errorf("picker %s = %q, want %q", attr, got, want)
				}
			}
			if _, ok := card.Find("#tp-apply").Attr("data-nojs"); !ok {
				t.Error("no-JS apply button missing")
			}
			if n := card.Find("svg rect.band").Length(); n != tt.days {
				t.Errorf("%d day bands, want %d", n, tt.days)
			}
			if n := card.Find("table.sr-only tbody tr").Length(); n != tt.days {
				t.Errorf("%d data rows, want %d", n, tt.days)
			}
		})
	}
}

// Rows of empty queues carry data-empty, which the Delete button needs on
// every selected row; Size counts completed and aggregating tasks too.
func TestQueuesEmptyRows(t *testing.T) {
	conns := defaultConns()
	conns.queues["main"] = []conn.Queue{{Name: "done", Size: 3}, {Name: "empty"}}
	h, _ := newTestServer(t, testOpts{conns: conns, authz: adminOpts().authz})
	doc := parse(t, do(t, h, request{path: "/c/main"}))
	var empty []string
	doc.Find("#queues-rows tr").Each(func(_ int, s *goquery.Selection) {
		if _, ok := s.Attr("data-empty"); ok {
			empty = append(empty, s.AttrOr("data-filter-text", ""))
		}
	})
	if diff := cmp.Diff([]string{"empty"}, empty); diff != "" {
		t.Errorf("empty rows (-want +got):\n%s", diff)
	}
}
