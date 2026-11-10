package web

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func TestOverviewFullPage(t *testing.T) {
	h, _ := newTestServer(t, testOpts{})
	full := do(t, h, request{path: "/"})
	if full.Code != http.StatusOK {
		t.Fatalf("status %d", full.Code)
	}
	doc := parse(t, full)
	if doc.Find("html").Length() != 1 || doc.Find("aside.side").Length() != 1 || doc.Find("header.top").Length() != 1 {
		t.Error("full page lacks the shell")
	}
	if got := full.Header().Get("Vary"); !strings.Contains(got, "HX-Request") {
		t.Errorf("Vary = %q", got)
	}
	region := doc.Find("[data-poll]")
	if region.Length() != 1 || !region.Is("section.conn-grid") {
		t.Fatalf("poll region = %d %v", region.Length(), region.Nodes)
	}
	for attr, want := range map[string]string{"hx-get": "/", "hx-trigger": "asynqui-refresh", "hx-swap": "none", "hx-disinherit": "*"} {
		if got, _ := region.Attr(attr); got != want {
			t.Errorf("%s = %q, want %q", attr, got, want)
		}
	}
	if got := text(doc.Find("main h1")); got != "All connections" {
		t.Errorf("h1 = %q", got)
	}
	if got := strings.Join(strings.Fields(doc.Find("title").Text()), " "); got != "All connections – asynqui" {
		t.Errorf("title = %q", got)
	}

	// A history restore needs the whole page.
	rec := do(t, h, request{path: "/", htmx: true, header: map[string]string{"HX-History-Restore-Request": "true"}})
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Error("history restore got a fragment")
	}
}

// TestOverviewPoll: a poll swaps nothing in place; every top-level element
// of the answer is an out-of-band swap whose id exists on the full page.
func TestOverviewPoll(t *testing.T) {
	h, _ := newTestServer(t, testOpts{})
	full := parse(t, do(t, h, request{path: "/"}))
	rec := do(t, h, request{path: "/", htmx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, "<aside") || strings.Contains(body, "<header") {
		t.Errorf("poll contains the shell:\n%s", body)
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
	want := "card-main card-analytics card-local overview-unreachable updated-at sidebar-conns"
	if got := strings.Join(ids, " "); got != want {
		t.Errorf("oob ids = %q, want %q", got, want)
	}
	if got := text(doc.Find("#overview-unreachable")); got != "1 unreachable" {
		t.Errorf("badge = %q", got)
	}
}

// TestPolledFocusablesHaveIDs: htmx refocuses by id after a swap, so every
// focusable element inside a polled region, or replaced by a poll, needs a
// stable, unique id.
func TestPolledFocusablesHaveIDs(t *testing.T) {
	conns := defaultConns()
	conns.conns = append(conns.conns, Connection{Name: "a.b", Addr: "x:1/0", Health: Health{State: HealthDown, Err: errors.New("x")}})
	h, _ := newTestServer(t, testOpts{conns: conns})
	doc := parse(t, do(t, h, request{path: "/"}))
	seen := map[string]bool{}
	doc.Find("[id]").Each(func(_ int, s *goquery.Selection) {
		id, _ := s.Attr("id")
		if seen[id] {
			t.Errorf("duplicate id %q", id)
		}
		seen[id] = true
	})
	poll := parse(t, do(t, h, request{path: "/", htmx: true}))
	var regions []string
	poll.Find("[hx-swap-oob]").Each(func(_ int, s *goquery.Selection) {
		id, _ := s.Attr("id")
		regions = append(regions, `[id="`+id+`"]`)
	})
	regions = append(regions, "[data-poll]")
	focusable := "a[href], button, input, select, textarea, summary, [tabindex]"
	n := 0
	for _, sel := range regions {
		doc.Find(sel).Find(focusable).Each(func(_ int, s *goquery.Selection) {
			n++
			if id, ok := s.Attr("id"); !ok || id == "" {
				h, _ := goquery.OuterHtml(s)
				t.Errorf("focusable in %s without id: %s", sel, h)
			}
		})
	}
	if n < 6 { // open, retry ×2, three sidebar links at least
		t.Errorf("only %d focusables found; selectors wrong?", n)
	}
}

func TestOverviewCards(t *testing.T) {
	conns := defaultConns()
	conns.conns = append(conns.conns, Connection{Name: "flaky", Addr: "flaky:6379/2", Health: Health{State: HealthUp}})
	conns.errs = map[string]error{"flaky": errors.New("READONLY you can't write against a read only replica")}
	h, _ := newTestServer(t, testOpts{conns: conns})
	doc := parse(t, do(t, h, request{path: "/"}))

	if got := text(doc.Find("#overview-unreachable .badge-danger")); got != "2 unreachable" {
		t.Errorf("unreachable badge = %q", got)
	}
	card := func(name string) *goquery.Selection { return doc.Find("article#card-" + name) }

	main := card("main")
	if h, _ := main.Attr("data-health"); h != "up" {
		t.Errorf("main health = %q", h)
	}
	stats := map[string]string{}
	main.Find(".stat").Each(func(_ int, s *goquery.Selection) {
		stats[s.Find(".stat-l").Text()] = s.Find(".stat-v").Text()
	})
	want := map[string]string{
		"Queues": "7", "Pending": "1,284", "Active": "46", "Retry": "37",
		"Processed today": "482,190", "Failed today": "1,204", "Oldest pending": "4m 12s",
	}
	for k, v := range want {
		if stats[k] != v {
			t.Errorf("main %s = %q, want %q", k, stats[k], v)
		}
	}
	if main.Find(".stat-v.failed").Length() != 1 {
		t.Error("Failed today is not marked failed")
	}
	if got := text(main.Find(".conn-foot .badge-ok")); got != "Healthy" {
		t.Errorf("main badge = %q", got)
	}
	if href, _ := main.Find(".conn-foot a#open-main").Attr("href"); href != "/c/main" {
		t.Errorf("Open href = %q", href)
	}
	if got := text(main.Find(".chip-role")); got != "viewer" {
		t.Errorf("role chip = %q", got)
	}

	down := card("analytics")
	if got := text(down.Find(".conn-error-title")); got != "Connection failed" {
		t.Errorf("down title = %q", got)
	}
	if got := down.Find(".conn-error-text").Text(); got != "dial tcp 10.0.4.12:6380: i/o timeout" {
		t.Errorf("down error = %q", got)
	}
	form := down.Find(".conn-foot form")
	if a, _ := form.Attr("action"); a != "/c/analytics/recheck" {
		t.Errorf("recheck action = %q", a)
	}
	if a, _ := form.Attr("hx-post"); a != "/c/analytics/recheck" {
		t.Errorf("recheck hx-post = %q", a)
	}
	if text(down.Find("button#retry-analytics")) != "Retry now" || text(down.Find(".badge-danger")) != "Unreachable" {
		t.Error("down footer wrong")
	}

	unknown := card("local")
	if got := unknown.Find(".conn-checking").Text(); got != "Checking" {
		t.Errorf("unknown = %q", got)
	}
	if unknown.Find(".badge, .conn-foot, .stat").Length() != 0 {
		t.Error("unknown card shows a badge, footer or stats")
	}

	// A failing Summary shows the connection down on the card and in the
	// sidebar alike.
	flaky := card("flaky")
	if h, _ := flaky.Attr("data-health"); h != "down" {
		t.Errorf("summary failure health = %q, want down", h)
	}
	if !strings.Contains(flaky.Find(".conn-error-text").Text(), "READONLY") {
		t.Error("summary error not shown")
	}
	if doc.Find("#nav-flaky .dot-down").Length() != 1 {
		t.Error("sidebar does not show the failing connection down")
	}
	if doc.Find("#nav-main .dot-up").Length() != 1 || doc.Find("#nav-local .dot-unknown").Length() != 1 {
		t.Error("sidebar dots wrong")
	}
}

// TestOverviewBudget: a Summary that blocks costs the page at most the
// budget, and its card renders as "Checking".
func TestOverviewBudget(t *testing.T) {
	conns := defaultConns()
	conns.block = map[string]bool{"main": true}
	h, _ := newTestServer(t, testOpts{conns: conns, budget: 50 * time.Millisecond})
	for _, htmx := range []bool{false, true} {
		start := time.Now()
		rec := do(t, h, request{path: "/", htmx: htmx})
		if el := time.Since(start); el > 2*time.Second {
			t.Fatalf("htmx=%v: page took %v", htmx, el)
		}
		doc := parse(t, rec)
		main := doc.Find("article#card-main")
		if h, _ := main.Attr("data-health"); h != "unknown" || text(main.Find(".conn-checking")) != "Checking" {
			t.Errorf("htmx=%v: slow card health = %q", htmx, h)
		}
		if h, _ := doc.Find("article#card-analytics").Attr("data-health"); h != "down" {
			t.Errorf("htmx=%v: other cards affected: %q", htmx, h)
		}
	}
	if defaultSummaryBudget != 1500*time.Millisecond {
		t.Errorf("default budget = %v", defaultSummaryBudget)
	}
}

func TestRecheck(t *testing.T) {
	h, conns := newTestServer(t, testOpts{})
	conns.recheck = map[string]Health{"analytics": {State: HealthUp}}
	conns.summaries["analytics"] = Summary{Queues: 2}

	rec := do(t, h, request{method: http.MethodPost, path: "/c/analytics/recheck", htmx: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	doc := parse(t, rec)
	first := doc.Find("body").Children().First()
	if !first.Is("article#card-analytics[data-health=up]") {
		t.Fatalf("recheck answer starts with %v:\n%s", first.Nodes, rec.Body)
	}
	if _, oob := first.Attr("hx-swap-oob"); oob {
		t.Error("the swapped card must not be out of band")
	}
	for _, id := range []string{"overview-unreachable", "updated-at", "sidebar-conns"} {
		if v, _ := doc.Find("#" + id).Attr("hx-swap-oob"); v != "true" {
			t.Errorf("no oob %s", id)
		}
	}
	if got := text(doc.Find("#overview-unreachable")); got != "" {
		t.Errorf("badge after recovery = %q", got)
	}
	if doc.Find("#nav-analytics .dot-up").Length() != 1 {
		t.Error("sidebar not updated")
	}
	if strings.Contains(rec.Body.String(), "<aside") {
		t.Error("recheck fragment contains the shell")
	}
	if len(conns.rechecked) != 1 || conns.rechecked[0] != "analytics" {
		t.Errorf("rechecked = %v", conns.rechecked)
	}

	// Without htmx: back to the Overview.
	rec = do(t, h, request{method: http.MethodPost, path: "/c/analytics/recheck"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("no-JS recheck = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
