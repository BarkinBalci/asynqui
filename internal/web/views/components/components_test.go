package components

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/a-h/templ"
)

func renderString(t *testing.T, c templ.Component) string {
	t.Helper()
	var b strings.Builder
	if err := c.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func render(t *testing.T, c templ.Component) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(renderString(t, c)))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func text(s *goquery.Selection) string { return strings.Join(strings.Fields(s.Text()), " ") }

func TestConnCardStates(t *testing.T) {
	base := ConnCard{Name: "main", Addr: "redis-main.internal:6379/0", Role: RoleOperator, OpenURL: "/c/main", RecheckURL: "/c/main/recheck"}
	tests := []struct {
		name       string
		card       func(ConnCard) ConnCard
		wantHealth string
		wantBadge  string
		present    []string
		absent     []string
	}{
		{
			name: "reachable",
			card: func(c ConnCard) ConnCard {
				c.Health, c.Queues, c.Pending, c.OldestPending = HealthUp, 7, 1284, 65*time.Second
				return c
			},
			wantHealth: "up", wantBadge: "Healthy",
			present: []string{".stats-4", ".stats-3", ".conn-foot a#open-main.conn-open"},
			absent:  []string{".conn-error", ".conn-checking", "form"},
		},
		{
			name: "unreachable",
			card: func(c ConnCard) ConnCard {
				c.Health, c.Error = HealthDown, "dial tcp: i/o timeout"
				return c
			},
			wantHealth: "down", wantBadge: "Unreachable",
			present: []string{".conn-error .conn-error-text",
				`form[method=post][action="/c/main/recheck"][hx-post="/c/main/recheck"][hx-target="closest article"][hx-swap=outerHTML]`,
				"button#retry-main"},
			absent: []string{".stat", ".conn-checking", "a.conn-open"},
		},
		{
			name:       "unknown",
			card:       func(c ConnCard) ConnCard { return c },
			wantHealth: "unknown", wantBadge: "",
			present: []string{".conn-checking"},
			absent:  []string{".badge", ".stat", ".conn-error", ".conn-foot"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := render(t, ConnCardView(tt.card(base), false))
			card := doc.Find("article.conn-card#card-main")
			if h, _ := card.Attr("data-health"); h != tt.wantHealth {
				t.Errorf("data-health = %q", h)
			}
			if _, oob := card.Attr("hx-swap-oob"); oob {
				t.Error("in-place card marked out of band")
			}
			if got := text(card.Find("h2")); got != "main" {
				t.Errorf("title = %q", got)
			}
			if got := text(card.Find(".chip-role")); got != "operator" {
				t.Errorf("role = %q", got)
			}
			if got := text(card.Find(".conn-addr.mono")); got != "redis-main.internal:6379/0" {
				t.Errorf("addr = %q", got)
			}
			if got := text(card.Find(".badge")); got != tt.wantBadge {
				t.Errorf("badge = %q, want %q", got, tt.wantBadge)
			}
			for _, sel := range tt.present {
				if card.Find(sel).Length() == 0 {
					t.Errorf("missing %s", sel)
				}
			}
			for _, sel := range tt.absent {
				if card.Find(sel).Length() != 0 {
					t.Errorf("unexpected %s", sel)
				}
			}
		})
	}
	doc := render(t, ConnCardView(ConnCard{Name: "main"}, true))
	if v, _ := doc.Find("article#card-main").Attr("hx-swap-oob"); v != "true" {
		t.Error("oob card lacks hx-swap-oob")
	}
}

func TestConnCardFormatsNumbers(t *testing.T) {
	doc := render(t, ConnCardView(ConnCard{Name: "x", Health: HealthUp, Processed: 482190, OldestPending: 65 * time.Second}, false))
	vals := map[string]string{}
	doc.Find(".stat").Each(func(_ int, s *goquery.Selection) { vals[text(s.Find(".stat-l"))] = text(s.Find(".stat-v")) })
	if vals["Processed today"] != "482,190" || vals["Oldest pending"] != "1m 05s" || vals["Retry"] != "0" {
		t.Errorf("stats = %v", vals)
	}
}

// TestIDPrefixesDistinct: every generated id kind has its own prefix, so a
// connection name can never make two kinds collide.
func TestIDPrefixesDistinct(t *testing.T) {
	ids := map[string]string{
		"card": CardID("x"), "open": CardOpenID("x"), "retry": CardRetryID("x"), "nav": NavConnID("x"),
	}
	fixed := []string{UpdatedID, UnreachableID, SidebarListID, ContentID, "sidebar-filter",
		SubNavID(SectionQueues), SubNavID(SectionServers), SubNavID(SectionSchedulers), SubNavID(SectionInfo)}
	prefixes := map[string]string{}
	for kind, id := range ids {
		p := strings.TrimSuffix(id, "x")
		for other, q := range prefixes {
			if strings.HasPrefix(p, q) || strings.HasPrefix(q, p) {
				t.Errorf("prefixes %s %q and %s %q overlap", kind, p, other, q)
			}
		}
		prefixes[kind] = p
		for _, f := range fixed {
			if strings.HasPrefix(f, p) {
				t.Errorf("fixed id %q starts with the %s prefix %q", f, kind, p)
			}
		}
	}
}

func TestStatusDotText(t *testing.T) {
	for h, want := range map[Health]string{HealthUp: "Reachable", HealthDown: "Unreachable", HealthUnknown: "Checking"} {
		doc := render(t, StatusDot(h))
		dot := doc.Find(".dot")
		if got := text(dot.Find(".sr-only")); got != want {
			t.Errorf("health %d: sr text %q, want %q", h, got, want)
		}
		if title, _ := dot.Attr("title"); title != want {
			t.Errorf("health %d: title %q", h, title)
		}
	}
}

func TestUnreachableBadge(t *testing.T) {
	doc := render(t, UnreachableBadge(0, true))
	w := doc.Find("#" + UnreachableID)
	if w.Length() != 1 || w.Children().Length() != 0 {
		t.Error("empty badge wrapper missing or not empty")
	}
	if v, _ := w.Attr("hx-swap-oob"); v != "true" {
		t.Error("oob wrapper lacks hx-swap-oob")
	}
	doc = render(t, UnreachableBadge(1204, false))
	if got := text(doc.Find("#" + UnreachableID + " .badge-danger")); got != "1,204 unreachable" {
		t.Errorf("badge = %q", got)
	}
}

func TestAccountMenu(t *testing.T) {
	tests := []struct {
		name     string
		a        Account
		wantAny  bool
		wantForm bool
		wantLink string
	}{
		{"hidden (auth none)", Account{}, false, false, ""},
		{"no sign out", Account{Visible: true, User: "jdoe", Role: RoleViewer}, true, false, ""},
		{"sign out form", Account{Visible: true, User: "jdoe", Role: RoleOperator, SignOutURL: "/logout"}, true, true, ""},
		{"sign out link", Account{Visible: true, User: "jdoe", Role: RoleOperator, SignOutURL: "https://sso.example/out", SignOutExternal: true}, true, false, "https://sso.example/out"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := render(t, AccountMenu(tt.a))
			if got := doc.Find(".account").Length() > 0; got != tt.wantAny {
				t.Fatalf("rendered = %v", got)
			}
			if got := doc.Find(`form[method=post][action="/logout"] button`).Length() > 0; got != tt.wantForm {
				t.Errorf("sign out form = %v", got)
			}
			if tt.wantLink != "" {
				if href, _ := doc.Find("a.menu-item").Attr("href"); href != tt.wantLink {
					t.Errorf("sign out link = %q", href)
				}
				if doc.Find("form").Length() != 0 {
					t.Error("external sign out must not be a form")
				}
			}
			if tt.wantAny && !strings.HasPrefix(text(doc.Find(".account-btn")), "jdoe ") {
				t.Errorf("label = %q", text(doc.Find(".account-btn")))
			}
			if doc.Find("summary[aria-label]").Length() != 0 {
				t.Error("summary has an aria-label hiding its visible text")
			}
		})
	}
}

func TestSidebarActiveState(t *testing.T) {
	conns := func(current bool) []SidebarConn {
		return []SidebarConn{
			{Name: "main", Addr: "m:6379/0", URL: "/c/main", Health: HealthUp, Current: current,
				Sub: SubNav{QueuesURL: "/c/main", ServersURL: "/c/main/servers", SchedulersURL: "/c/main/schedulers", InfoURL: "/c/main/info", Valkey: true}},
			{Name: "analytics", Addr: "a:6380/0", URL: "/c/analytics", Health: HealthDown},
			{Name: "local", Addr: "l:6379/1", URL: "/c/local"},
		}
	}
	t.Run("overview", func(t *testing.T) {
		doc := render(t, SidebarNav(Sidebar{OverviewURL: "/", Section: SectionOverview, Conns: conns(false)}))
		if got := text(doc.Find(`.nav[aria-label=Global] a.on[aria-current=page]`)); got != "Overview" {
			t.Errorf("active = %q", got)
		}
		if doc.Find(".conn-current, .subnav").Length() != 0 {
			t.Error("a connection is expanded on the Overview")
		}
		if got := text(doc.Find(".navlabel .num")); got != "3" {
			t.Errorf("count = %q", got)
		}
		for name, cls := range map[string]string{"main": "dot-up", "analytics": "dot-down", "local": "dot-unknown"} {
			if doc.Find(`a#nav-`+name+`[href="/c/`+name+`"] .dot.`+cls).Length() != 1 {
				t.Errorf("%s lacks %s", name, cls)
			}
		}
		if doc.Find(`aside[aria-label]`).Length() != 1 {
			t.Error("sidebar landmark has no name")
		}
		if f, _ := doc.Find("input#sidebar-filter").Attr("data-filter"); f != "#"+SidebarListID {
			t.Errorf("filter target = %q", f)
		}
	})
	t.Run("connection sub-page", func(t *testing.T) {
		doc := render(t, SidebarNav(Sidebar{OverviewURL: "/", Section: SectionSchedulers, Conns: conns(true)}))
		if doc.Find(`.nav[aria-label=Global] a.on`).Length() != 0 {
			t.Error("Overview active")
		}
		cur := doc.Find(".conn-current")
		if cur.Length() != 1 || text(cur.Find(".citem-name")) != "main Reachable" {
			t.Fatalf("current = %q", text(cur.Find(".citem-name")))
		}
		if got := text(cur.Find(".subnav a#subnav-schedulers.on[aria-current=page]")); got != "Schedulers" {
			t.Errorf("active sub-item = %q", got)
		}
		if doc.Find("[aria-current]").Length() != 1 {
			t.Error("more than one aria-current")
		}
		if got := text(cur.Find("a#subnav-info")); got != "Valkey info" {
			t.Errorf("info label = %q", got)
		}
		if v, _ := cur.Attr("data-filter-text"); v != "main m:6379/0" {
			t.Errorf("filter text = %q", v)
		}
	})
	t.Run("list out of band", func(t *testing.T) {
		doc := render(t, SidebarConnList(Sidebar{Conns: conns(false)}, true))
		if v, _ := doc.Find("nav#" + SidebarListID).Attr("hx-swap-oob"); v != "true" {
			t.Error("oob list lacks hx-swap-oob")
		}
	})
}

func TestTopBar(t *testing.T) {
	at := time.Date(2026, 11, 8, 0, 11, 52, 0, time.UTC)
	html := renderString(t, TopBarView(TopBar{
		Crumbs: []Crumb{
			{Section: SectionOverview, URL: "/"}, {Text: "main", URL: "/c/main"},
			{Section: SectionInfo, Valkey: true, URL: "/c/main/info"},
		},
		UpdatedAt: at, Theme: ThemeDark, Refresh: RefreshOff, PrefsURL: "/prefs", Next: "/c/main/info",
	}))
	if strings.Contains(html, `class=""`) {
		t.Error("empty class attribute")
	}
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(html))
	var crumbs []string
	doc.Find(".crumbs li").Each(func(_ int, s *goquery.Selection) { crumbs = append(crumbs, text(s)) })
	if got := strings.Join(crumbs, " / "); got != "Overview / main / Valkey info" {
		t.Errorf("crumbs = %q", got)
	}
	tm := doc.Find("time#updated-at")
	if dt, _ := tm.Attr("datetime"); dt != "2026-11-08T00:11:52Z" || text(tm) != "Nov 8, 00:11:52" {
		t.Errorf("time = %q %q", dt, text(tm))
	}
	if got := text(doc.Find(".updated")); got != "Updated Nov 8, 00:11:52" {
		t.Errorf("updated = %q", got)
	}
	if v, _ := doc.Find(`form[data-pref=refresh] button.on[aria-pressed=true]`).Attr("value"); v != "off" {
		t.Errorf("refresh on = %q", v)
	}
	if v, _ := doc.Find(`form[data-pref=theme] button.on[aria-pressed=true]`).Attr("value"); v != "dark" {
		t.Errorf("theme on = %q", v)
	}
	if n := doc.Find(`form[data-pref] button[aria-pressed=true]`).Length(); n != 2 {
		t.Errorf("pressed buttons = %d", n)
	}
	if n := doc.Find(`form[data-pref=theme] button[aria-label][title]`).Length(); n != 3 {
		t.Errorf("labeled theme buttons = %d", n)
	}
	if doc.Find(".account").Length() != 0 {
		t.Error("account shown without Visible")
	}
}

func TestCrumbLabelComponent(t *testing.T) {
	doc := render(t, CrumbLabel(Crumb{Label: templ.Raw("Not found"), Text: "ignored"}))
	if got := text(doc.Find("body")); got != "Not found" {
		t.Errorf("label = %q", got)
	}
}
