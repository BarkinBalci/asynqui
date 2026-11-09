package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"

	"github.com/BarkinBalci/asynqui/internal/config"
)

func TestAccess(t *testing.T) {
	// main: alice viewer, bob operator, carol admin. hidden: nobody.
	az := config.Authz{
		Default: config.RoleNone,
		Bindings: []config.Binding{
			{Users: []string{"alice"}, Role: config.RoleViewer, Connections: []string{"main", "analytics", "local"}},
			{Users: []string{"bob"}, Role: config.RoleOperator, Connections: []string{"main"}},
			{Users: []string{"carol"}, Role: config.RoleAdmin, Connections: []string{"main"}},
		},
	}
	conns := func() *fakeConns {
		c := defaultConns()
		c.conns = append(c.conns, Connection{Name: "hidden", Addr: "h:6379/0"})
		return c
	}
	tests := []struct {
		name       string
		provider   config.Provider
		rq         request
		wantStatus int
		wantHeader map[string]string
	}{
		{"basic: no session redirects to login", config.ProviderBasic,
			request{path: "/c/main?x=1"}, http.StatusSeeOther,
			map[string]string{"Location": "/login?next=%2Fc%2Fmain%3Fx%3D1", "Cache-Control": "no-store"}},
		{"basic: overview without session redirects to bare login", config.ProviderBasic,
			request{path: "/"}, http.StatusSeeOther, map[string]string{"Location": "/login"}},
		{"basic: htmx without session gets HX-Redirect", config.ProviderBasic,
			request{path: "/", htmx: true}, http.StatusUnauthorized, map[string]string{"HX-Redirect": "/login"}},
		{"proxy: no identity is 401", config.ProviderProxy,
			request{path: "/"}, http.StatusUnauthorized, nil},
		{"viewer sees overview", config.ProviderProxy,
			request{path: "/", user: "alice"}, http.StatusOK, nil},
		{"viewer sees connection", config.ProviderProxy,
			request{path: "/c/main", user: "alice"}, http.StatusOK, nil},
		{"hidden connection is 404", config.ProviderProxy,
			request{path: "/c/hidden", user: "alice"}, http.StatusNotFound, nil},
		{"hidden sub-page is 404", config.ProviderProxy,
			request{path: "/c/hidden/servers", user: "alice"}, http.StatusNotFound, nil},
		{"unknown connection is 404", config.ProviderProxy,
			request{path: "/c/nope", user: "alice"}, http.StatusNotFound, nil},
		{"hidden action is 404, not 403", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/hidden/queues/delete", user: "carol"}, http.StatusNotFound, nil},
		{"viewer may recheck", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/main/recheck", user: "alice"}, http.StatusSeeOther, nil},
		{"viewer cannot pause", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/main/queues/pause", user: "alice"}, http.StatusForbidden, nil},
		{"operator may pause", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/main/queues/pause", user: "bob"}, http.StatusNotImplemented, nil},
		{"operator cannot delete", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/main/queues/q1/tasks/delete", user: "bob"}, http.StatusForbidden, nil},
		{"admin may delete", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/main/queues/q1/tasks/delete", user: "carol"}, http.StatusNotImplemented, nil},
		{"none: everyone gets the default", config.ProviderNone,
			request{path: "/c/main"}, http.StatusNotFound, nil}, // default none hides
		{"probes need no identity", config.ProviderBasic,
			request{path: "/health/readiness"}, http.StatusOK, nil},
		{"static needs no identity", config.ProviderBasic,
			request{path: "/static/nope.css"}, http.StatusNotFound, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, _ := newTestServer(t, testOpts{provider: tt.provider, authz: az, conns: conns()})
			rec := do(t, h, tt.rq)
			if rec.Code != tt.wantStatus {
				t.Fatalf("%s %s = %d, want %d\n%s", tt.rq.method, tt.rq.path, rec.Code, tt.wantStatus, rec.Body)
			}
			for k, v := range tt.wantHeader {
				if got := rec.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
		})
	}
}

func TestForbiddenAndNotFoundPages(t *testing.T) {
	az := config.Authz{Default: config.RoleNone, Bindings: []config.Binding{
		{Users: []string{"alice"}, Role: config.RoleViewer, Connections: []string{"main"}},
	}}
	h, _ := newTestServer(t, testOpts{provider: config.ProviderProxy, authz: az})
	rec := do(t, h, request{path: "/c/local", user: "alice"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	doc := parse(t, rec)
	if got := doc.Find("h1").Text(); got != "Not found" {
		t.Errorf("h1 = %q", got)
	}
	// The hidden connection is not listed anywhere.
	if strings.Contains(rec.Body.String(), "localhost:6379/1") {
		t.Error("404 page leaks the hidden connection")
	}
}

func TestOverviewFullPageAndFragment(t *testing.T) {
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
	region := doc.Find("#overview[data-poll]")
	if hx, _ := region.Attr("hx-get"); hx != "/" {
		t.Errorf("hx-get = %q", hx)
	}
	if tr, _ := region.Attr("hx-trigger"); tr != "asynqui-refresh" {
		t.Errorf("hx-trigger = %q", tr)
	}

	frag := do(t, h, request{path: "/", htmx: true})
	if frag.Code != http.StatusOK {
		t.Fatalf("status %d", frag.Code)
	}
	body := frag.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, "<aside") || strings.Contains(body, "<header") {
		t.Errorf("fragment contains the shell:\n%s", body)
	}
	fdoc := parse(t, frag)
	if fdoc.Find("#overview article.conn-card").Length() != 3 {
		t.Errorf("fragment cards = %d", fdoc.Find("#overview article.conn-card").Length())
	}
	if oob, _ := fdoc.Find("time#updated-at").Attr("hx-swap-oob"); oob != "true" {
		t.Error("fragment lacks the out-of-band Updated time")
	}

	// A history restore needs the whole page.
	r := httptestRequest(http.MethodGet, "/")
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-History-Restore-Request", "true")
	rec := serve(h, r)
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Error("history restore got a fragment")
	}
}

func TestOverviewCards(t *testing.T) {
	conns := defaultConns()
	conns.conns = append(conns.conns, Connection{Name: "flaky", Addr: "flaky:6379/2", Health: Health{State: HealthUp}})
	conns.errs = map[string]error{"flaky": errString("READONLY You can't write against a read only replica.")}
	h, _ := newTestServer(t, testOpts{conns: conns})
	doc := parse(t, do(t, h, request{path: "/"}))

	if got := strings.TrimSpace(doc.Find(".page-head .badge-danger").Text()); got != "2 unreachable" {
		t.Errorf("unreachable badge = %q", got)
	}
	card := func(name string) *goquery.Selection { return doc.Find("article#conn-" + name) }

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
	if !main.Find(".stat-v.failed").Is(".failed") {
		t.Error("Failed today is not marked failed")
	}
	if got := strings.TrimSpace(main.Find(".conn-foot .badge-ok").Text()); got != "Healthy" {
		t.Errorf("main badge = %q", got)
	}
	if href, _ := main.Find(".conn-foot a").Attr("href"); href != "/c/main" {
		t.Errorf("Open href = %q", href)
	}
	if got := strings.TrimSpace(main.Find(".chip-role").Text()); got != "viewer" {
		t.Errorf("role chip = %q", got)
	}

	down := card("analytics")
	if got := strings.TrimSpace(down.Find(".conn-error-title").Text()); got != "Connection failed" {
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
	if strings.TrimSpace(down.Find(".conn-foot button").Text()) != "Retry now" ||
		strings.TrimSpace(down.Find(".badge-danger").Text()) != "Unreachable" {
		t.Error("down footer wrong")
	}

	unknown := card("local")
	if got := unknown.Find(".conn-checking").Text(); got != "Checking" {
		t.Errorf("unknown = %q", got)
	}
	if unknown.Find(".badge, .conn-foot, .stat").Length() != 0 {
		t.Error("unknown card shows a badge, footer or stats")
	}

	flaky := card("flaky")
	if h, _ := flaky.Attr("data-health"); h != "down" {
		t.Errorf("summary failure health = %q, want down", h)
	}
	if !strings.Contains(flaky.Find(".conn-error-text").Text(), "READONLY") {
		t.Error("summary error not shown")
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
	if doc.Find("html > body > article#conn-analytics[data-health=up]").Length() != 1 {
		t.Errorf("recheck fragment:\n%s", rec.Body)
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

func TestCrossOriginProtection(t *testing.T) {
	h, _ := newTestServer(t, testOpts{})
	r := httptestRequest(http.MethodPost, "/c/main/recheck")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if rec := serve(h, r); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST = %d, want 403", rec.Code)
	}
	r = httptestRequest(http.MethodPost, "/c/main/recheck")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if rec := serve(h, r); rec.Code != http.StatusSeeOther {
		t.Errorf("same-origin POST = %d, want 303", rec.Code)
	}
}

func TestSecurityHeadersEverywhere(t *testing.T) {
	h, _ := newTestServer(t, testOpts{provider: config.ProviderBasic})
	css := staticPath(t, h, "stylesheet", "/login")
	for _, p := range []string{"/", "/login", "/health/liveness", css, "/static/missing", "/nope"} {
		rec := do(t, h, request{path: p})
		hd := rec.Header()
		if csp := hd.Get("Content-Security-Policy"); csp != contentSecurityPolicy {
			t.Errorf("%s: CSP = %q", p, csp)
		}
		if hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s: missing nosniff or Referrer-Policy", p)
		}
	}
}

func TestStaticAssets(t *testing.T) {
	h, _ := newTestServer(t, testOpts{})
	doc := parse(t, do(t, h, request{path: "/"}))
	hashed := regexp.MustCompile(`^/static/[a-z0-9/_.-]+\.[0-9a-f]{12}\.[a-z0-9]+$`)

	var urls []string
	doc.Find("link[href], script[src]").Each(func(_ int, s *goquery.Selection) {
		u, ok := s.Attr("href")
		if !ok {
			u, _ = s.Attr("src")
		}
		urls = append(urls, u)
	})
	if len(urls) != 4 {
		t.Fatalf("asset URLs = %v", urls)
	}
	wantType := map[string]string{
		".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8",
		".svg": "image/svg+xml", ".woff2": "font/woff2",
	}
	var css string
	for _, u := range urls {
		if !hashed.MatchString(u) {
			t.Errorf("%s is not a hashed URL", u)
		}
		rec := do(t, h, request{path: u})
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", u, rec.Code)
			continue
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Errorf("%s Cache-Control = %q", u, cc)
		}
		ext := u[strings.LastIndex(u, "."):]
		if ct := rec.Header().Get("Content-Type"); ct != wantType[ext] {
			t.Errorf("%s Content-Type = %q", u, ct)
		}
		if ext == ".css" {
			css = rec.Body.String()
		}
	}

	// Fonts referenced by the stylesheet resolve to hashed, servable files.
	fonts := regexp.MustCompile(`url\(([^)]+)\)`).FindAllStringSubmatch(css, -1)
	if len(fonts) != 6 {
		t.Fatalf("stylesheet font urls = %d, want 6", len(fonts))
	}
	for _, m := range fonts {
		u := "/static/" + m[1]
		if !hashed.MatchString(u) {
			t.Errorf("font %s not hashed", u)
		}
		rec := do(t, h, request{path: u})
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "font/woff2" {
			t.Errorf("GET %s = %d %q", u, rec.Code, rec.Header().Get("Content-Type"))
		}
	}

	// Unhashed names are not served; conditional requests are.
	if rec := do(t, h, request{path: "/static/app.css"}); rec.Code != http.StatusNotFound {
		t.Errorf("unhashed app.css = %d", rec.Code)
	}
	r := httptestRequest(http.MethodGet, urls[1])
	r.Header.Set("If-None-Match", serve(h, httptestRequest(http.MethodGet, urls[1])).Header().Get("ETag"))
	if rec := serve(h, r); rec.Code != http.StatusNotModified {
		t.Errorf("conditional GET = %d, want 304", rec.Code)
	}
}

func TestBasePath(t *testing.T) {
	h, _ := newTestServer(t, testOpts{basePath: "/asynq"})
	tests := []struct {
		path string
		want int
	}{
		{"/asynq/health/liveness", http.StatusOK},
		{"/asynq/health/readiness", http.StatusOK},
		{"/asynq/", http.StatusOK},
		{"/asynq/c/main", http.StatusOK},
		{"/asynq", http.StatusTemporaryRedirect}, // ServeMux adds the slash
		{"/", http.StatusNotFound},
		{"/health/liveness", http.StatusNotFound},
		{"/c/main", http.StatusNotFound},
		{"/asynqx/", http.StatusNotFound},
	}
	for _, tt := range tests {
		if rec := do(t, h, request{path: tt.path}); rec.Code != tt.want {
			t.Errorf("GET %s = %d, want %d", tt.path, rec.Code, tt.want)
		}
	}

	rec := do(t, h, request{path: "/asynq/c/main"})
	doc := parse(t, rec)
	doc.Find("[href], [src], [action], [hx-get], [hx-post]").Each(func(_ int, s *goquery.Selection) {
		for _, attr := range []string{"href", "src", "action", "hx-get", "hx-post"} {
			v, ok := s.Attr(attr)
			if !ok || strings.HasPrefix(v, "https://") {
				continue
			}
			if !strings.HasPrefix(v, "/asynq/") {
				t.Errorf("%s=%q is outside the base path", attr, v)
			}
		}
	})
	if base, _ := doc.Find("html").Attr("data-base"); base != "/asynq" {
		t.Errorf("data-base = %q", base)
	}
	if next, _ := doc.Find(`form[data-pref] input[name=next]`).First().Attr("value"); next != "/asynq/c/main" {
		t.Errorf("prefs next = %q", next)
	}
	// Assets resolve under the base path.
	css := staticPath(t, h, "stylesheet", "/asynq/")
	if rec := do(t, h, request{path: css}); rec.Code != http.StatusOK {
		t.Errorf("GET %s = %d", css, rec.Code)
	}
}

func TestURLEscapesSegments(t *testing.T) {
	d := &deps{base: "/asynq"}
	tests := []struct {
		segs []string
		want string
	}{
		{nil, "/asynq/"},
		{[]string{"c", "main"}, "/asynq/c/main"},
		{[]string{"c", "main", "queues", "a/b c?"}, "/asynq/c/main/queues/a%2Fb%20c%3F"},
	}
	for _, tt := range tests {
		if got := d.url(tt.segs...); got != tt.want {
			t.Errorf("url(%q) = %q, want %q", tt.segs, got, tt.want)
		}
	}
	if got := (&deps{}).url(); got != "/" {
		t.Errorf("url() without base = %q", got)
	}
}

func TestPrefs(t *testing.T) {
	h, _ := newTestServer(t, testOpts{basePath: "/asynq"})
	rec := do(t, h, request{method: http.MethodPost, path: "/asynq/prefs", form: url.Values{
		"theme": {"dark"}, "refresh": {"10s"}, "next": {"/asynq/c/main"},
	}.Encode()})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/asynq/c/main" {
		t.Fatalf("prefs = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	got := map[string]string{}
	for _, c := range cookies {
		got[c.Name] = c.Value
		if c.Path != "/asynq" {
			t.Errorf("%s path = %q", c.Name, c.Path)
		}
	}
	if got[themeCookie] != "dark" || got[refreshCookie] != "10s" {
		t.Errorf("cookies = %v", got)
	}

	doc := parse(t, do(t, h, request{path: "/asynq/", cookies: cookies}))
	html := doc.Find("html")
	if v, _ := html.Attr("data-theme"); v != "dark" {
		t.Errorf("data-theme = %q", v)
	}
	if v, _ := html.Attr("data-refresh"); v != "10s" {
		t.Errorf("data-refresh = %q", v)
	}
	if v, _ := doc.Find(`form[data-pref=refresh] button[aria-pressed=true]`).Attr("value"); v != "10s" {
		t.Errorf("pressed refresh = %q", v)
	}
	if v, _ := doc.Find(`form[data-pref=theme] button.on`).Attr("value"); v != "dark" {
		t.Errorf("pressed theme = %q", v)
	}

	// Bad values are ignored; the default refresh comes from config.
	doc = parse(t, do(t, h, request{path: "/asynq/", cookies: []*http.Cookie{
		{Name: themeCookie, Value: "neon"}, {Name: refreshCookie, Value: "1s"},
	}}))
	if v, _ := doc.Find("html").Attr("data-theme"); v != "system" {
		t.Errorf("bad theme cookie → %q", v)
	}
	if v, _ := doc.Find("html").Attr("data-refresh"); v != "5s" {
		t.Errorf("bad refresh cookie → %q", v)
	}

	// The redirect never leaves the app.
	for _, next := range []string{"https://evil.example/", "//evil.example/", "/other/", "/asynq/../x", `/asynq\x`, ""} {
		rec := do(t, h, request{method: http.MethodPost, path: "/asynq/prefs", form: url.Values{"next": {next}}.Encode()})
		if loc := rec.Header().Get("Location"); loc != "/asynq/" {
			t.Errorf("next %q → %q", next, loc)
		}
	}
}

func TestLoginPage(t *testing.T) {
	t.Run("none has no login page", func(t *testing.T) {
		h, _ := newTestServer(t, testOpts{})
		if rec := do(t, h, request{path: "/login"}); rec.Code != http.StatusNotFound {
			t.Errorf("GET /login = %d", rec.Code)
		}
	})
	t.Run("proxy has no login page", func(t *testing.T) {
		h, _ := newTestServer(t, testOpts{provider: config.ProviderProxy})
		if rec := do(t, h, request{path: "/login"}); rec.Code != http.StatusNotFound {
			t.Errorf("GET /login = %d", rec.Code)
		}
	})
	t.Run("basic form", func(t *testing.T) {
		h, _ := newTestServer(t, testOpts{provider: config.ProviderBasic})
		rec := do(t, h, request{path: "/login?next=%2Fc%2Fmain"})
		doc := parse(t, rec)
		if doc.Find(`form input[name=username]`).Length() != 1 || doc.Find(`input[name=password][type=password]`).Length() != 1 {
			t.Error("no username/password form")
		}
		if strings.Contains(rec.Body.String(), "Sign in with SSO") {
			t.Error("basic login shows SSO")
		}
		if v, _ := doc.Find(`input[name=next]`).Attr("value"); v != "/c/main" {
			t.Errorf("next = %q", v)
		}
		if doc.Find(".alert").Length() != 0 {
			t.Error("alert without a failure")
		}
		if got := doc.Find(".login-version").Text(); got != "v0.1.0" {
			t.Errorf("version = %q", got)
		}
	})
	t.Run("invalid credentials", func(t *testing.T) {
		h, _ := newTestServer(t, testOpts{provider: config.ProviderBasic})
		rec := do(t, h, request{method: http.MethodPost, path: "/login", form: "username=alice&password=wrong&next=%2Fc%2Fmain"})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status %d", rec.Code)
		}
		doc := parse(t, rec)
		if got := strings.TrimSpace(doc.Find(".alert").Text()); got != "Invalid username or password." {
			t.Errorf("alert = %q", got)
		}
		if v, _ := doc.Find(`input[name=next]`).Attr("value"); v != "/c/main" {
			t.Errorf("next kept = %q", v)
		}
	})
	t.Run("sign in then account menu and sign out", func(t *testing.T) {
		h, _ := newTestServer(t, testOpts{provider: config.ProviderBasic, authz: config.Authz{
			Default:  config.RoleNone,
			Bindings: []config.Binding{{Users: []string{testUser}, Role: config.RoleOperator, Connections: []string{"*"}}},
		}})
		rec := do(t, h, request{method: http.MethodPost, path: "/login", form: url.Values{
			"username": {testUser}, "password": {testPassword}, "next": {"/c/main"},
		}.Encode()})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/c/main" {
			t.Fatalf("login = %d %q", rec.Code, rec.Header().Get("Location"))
		}
		doc := parse(t, do(t, h, request{path: "/c/main", cookies: rec.Result().Cookies()}))
		menu := doc.Find("details.account")
		if got := strings.Join(strings.Fields(menu.Find("summary").Text()), " "); got != "alice operator" {
			t.Errorf("account = %q", got)
		}
		if a, _ := menu.Find("form").Attr("action"); a != "/logout" {
			t.Errorf("sign out action = %q", a)
		}
	})
}

func TestLoginFailureText(t *testing.T) {
	// Every auth.Failure maps to its own message.
	h, _ := newTestServer(t, testOpts{provider: config.ProviderBasic})
	rec := do(t, h, request{method: http.MethodPost, path: "/login", form: "%zz"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	if got := strings.TrimSpace(parse(t, rec).Find(".alert").Text()); got != "Invalid request. Try again." {
		t.Errorf("alert = %q", got)
	}
}

// TestNoInlineStyleOrScript renders every page and fragment the server has
// and fails on anything the CSP would block.
func TestNoInlineStyleOrScript(t *testing.T) {
	proxyAuthz := config.Authz{Default: config.RoleAdmin}
	type page struct {
		opts testOpts
		rq   request
	}
	var cases []page
	for _, p := range []string{
		"/", "/c/main", "/c/main/queues/default", "/c/main/queues/default/tasks/c3e81b90-04aa-4f1e-9d27-6b52e0c8a113",
		"/c/main/servers", "/c/main/servers/host:1234:abcdef", "/c/main/schedulers", "/c/main/schedulers/abc",
		"/c/main/info", "/c/main/throughput", "/c/nope",
	} {
		cases = append(cases,
			page{testOpts{}, request{path: p}},
			page{testOpts{provider: config.ProviderProxy, authz: proxyAuthz, proxyLogout: "https://sso.example/logout"}, request{path: p, user: "alice"}},
		)
	}
	cases = append(cases,
		page{testOpts{}, request{path: "/", htmx: true}},
		page{testOpts{}, request{method: http.MethodPost, path: "/c/analytics/recheck", htmx: true}},
		page{testOpts{provider: config.ProviderProxy, authz: config.Authz{Default: config.RoleViewer}}, request{method: http.MethodGet, path: "/c/main/servers", user: "x"}},
		page{testOpts{provider: config.ProviderBasic}, request{path: "/login"}},
		page{testOpts{provider: config.ProviderBasic}, request{method: http.MethodPost, path: "/login", form: "username=a&password=b"}},
	)
	onAttr := regexp.MustCompile(`(?i)^(on|hx-on)`)
	for _, c := range cases {
		h, _ := newTestServer(t, c.opts)
		rec := do(t, h, c.rq)
		name := c.rq.method + " " + c.rq.path
		if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: not HTML (%d)", name, rec.Code)
			continue
		}
		doc := parse(t, rec)
		doc.Find("*").Each(func(_ int, s *goquery.Selection) {
			n := s.Get(0)
			if n.Data == "style" {
				t.Errorf("%s: <style> element", name)
			}
			if n.Data == "script" {
				if src, ok := s.Attr("src"); !ok || src == "" || strings.TrimSpace(s.Text()) != "" {
					t.Errorf("%s: inline <script>", name)
				}
			}
			for _, a := range n.Attr {
				switch {
				case a.Key == "style":
					t.Errorf("%s: style attribute on <%s>", name, n.Data)
				case onAttr.MatchString(a.Key):
					t.Errorf("%s: %s attribute on <%s>", name, a.Key, n.Data)
				case strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Val)), "javascript:"):
					t.Errorf("%s: javascript: URL in %s", name, a.Key)
				case strings.HasPrefix(a.Key, "hx-") && strings.Contains(a.Val, "js:"):
					t.Errorf("%s: js: in %s", name, a.Key)
				case a.Key == "hx-trigger" && strings.Contains(a.Val, "["):
					t.Errorf("%s: trigger filter (needs eval) in %s", name, a.Key)
				}
			}
		})
	}
}

func TestAccountMenu(t *testing.T) {
	tests := []struct {
		name        string
		opts        testOpts
		user        string
		path        string
		wantVisible bool
		wantSignOut bool
		wantRole    string
	}{
		{"none hides it", testOpts{}, "", "/", false, false, ""},
		{"proxy without logout url", testOpts{provider: config.ProviderProxy}, "bob", "/", true, false, "viewer"},
		{"proxy with logout url", testOpts{provider: config.ProviderProxy, proxyLogout: "https://sso.example/out"}, "bob", "/", true, true, "viewer"},
		{"highest role on the overview", testOpts{provider: config.ProviderProxy, authz: config.Authz{
			Default:  config.RoleViewer,
			Bindings: []config.Binding{{Users: []string{"bob"}, Role: config.RoleAdmin, Connections: []string{"local"}}},
		}}, "bob", "/", true, false, "admin"},
		{"role on the current connection", testOpts{provider: config.ProviderProxy, authz: config.Authz{
			Default:  config.RoleViewer,
			Bindings: []config.Binding{{Users: []string{"bob"}, Role: config.RoleAdmin, Connections: []string{"local"}}},
		}}, "bob", "/c/main", true, false, "viewer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestServer(t, tt.opts)
			doc := parse(t, do(t, h, request{path: tt.path, user: tt.user}))
			acct := doc.Find(".account")
			if (acct.Length() > 0) != tt.wantVisible {
				t.Fatalf("account visible = %v", acct.Length() > 0)
			}
			if !tt.wantVisible {
				return
			}
			if got := doc.Find("details.account form button").Length() > 0; got != tt.wantSignOut {
				t.Errorf("sign out = %v", got)
			}
			if got := strings.TrimSpace(acct.Find(".account-role").Text()); got != tt.wantRole {
				t.Errorf("role = %q, want %q", got, tt.wantRole)
			}
			if !strings.Contains(acct.Text(), tt.user) {
				t.Errorf("user missing: %q", acct.Text())
			}
		})
	}
}

func TestSidebarAndCrumbs(t *testing.T) {
	h, _ := newTestServer(t, testOpts{})
	doc := parse(t, do(t, h, request{path: "/c/main/servers/abcdefghijklmnop"}))

	cur := doc.Find(".conn-list .conn-current")
	if cur.Length() != 1 || strings.TrimSpace(cur.Find(".citem-name").Text()) != "main" {
		t.Fatalf("current connection = %q", cur.Text())
	}
	on := cur.Find(".subnav a.on")
	if strings.TrimSpace(on.Text()) != "Servers" {
		t.Errorf("active sub-item = %q", on.Text())
	}
	if href, _ := on.Attr("href"); href != "/c/main/servers" {
		t.Errorf("servers href = %q", href)
	}
	if doc.Find(`.nav[aria-label=Global] a.on`).Length() != 0 {
		t.Error("Overview active on a connection page")
	}
	var crumbs []string
	doc.Find(".crumbs li").Each(func(_ int, s *goquery.Selection) { crumbs = append(crumbs, strings.TrimSpace(s.Text())) })
	if got := strings.Join(crumbs, " / "); got != "Overview / main / Servers / abcdefghijkl…" {
		t.Errorf("crumbs = %q", got)
	}
	if doc.Find(".crumbs li a").Length() != 3 || doc.Find(".crumbs li:last-child [aria-current=page]").Length() != 1 {
		t.Error("every crumb but the last must be a link")
	}
	if got := strings.TrimSpace(doc.Find(".side-foot .version").Text()); got != "v0.1.0" {
		t.Errorf("footer version = %q", got)
	}
	if href, _ := doc.Find(".side-foot .version").Attr("href"); href != releasesURL+"/tag/v0.1.0" {
		t.Errorf("release href = %q", href)
	}
}

func errString(s string) error { return stringError(s) }

type stringError string

func (e stringError) Error() string { return string(e) }

func httptestRequest(method, target string) *http.Request {
	return httptest.NewRequest(method, target, nil)
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// staticPath finds the hashed URL of the page's stylesheet.
func staticPath(t *testing.T, h http.Handler, rel string, root ...string) string {
	t.Helper()
	p := "/"
	if len(root) > 0 {
		p = root[0]
	}
	doc := parse(t, do(t, h, request{path: p}))
	href, ok := doc.Find(`link[rel=` + rel + `]`).Attr("href")
	if !ok {
		t.Fatalf("no %s link", rel)
	}
	return href
}
