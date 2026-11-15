package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/a-h/templ"

	"github.com/BarkinBalci/asynqui/internal/config"
)

func TestLoginPage(t *testing.T) {
	t.Run("none and proxy have no login page", func(t *testing.T) {
		for _, p := range []config.Provider{config.ProviderNone, config.ProviderProxy} {
			h, _ := newTestServer(t, testOpts{provider: p})
			if rec := do(t, h, request{path: "/login"}); rec.Code != http.StatusNotFound {
				t.Errorf("%s: GET /login = %d", p, rec.Code)
			}
			if rec := do(t, h, request{path: "/login/sso"}); rec.Code != http.StatusNotFound {
				t.Errorf("%s: GET /login/sso = %d", p, rec.Code)
			}
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
		if doc.Find(".alert, [aria-invalid]").Length() != 0 {
			t.Error("alert or invalid fields without a failure")
		}
		if doc.Find("main h1.login-head").Length() != 1 {
			t.Error("login page has no h1")
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
		alert := doc.Find(".alert[role=alert]")
		if got := text(alert); got != "Invalid username or password." {
			t.Errorf("alert = %q", got)
		}
		id, _ := alert.Attr("id")
		doc.Find("input[name=username], input[name=password]").Each(func(_ int, s *goquery.Selection) {
			if v, _ := s.Attr("aria-invalid"); v != "true" {
				t.Error("field not aria-invalid")
			}
			if v, _ := s.Attr("aria-describedby"); v != id || id == "" {
				t.Errorf("aria-describedby = %q, alert id %q", v, id)
			}
		})
		if v, _ := doc.Find(`input[name=next]`).Attr("value"); v != "/c/main" {
			t.Errorf("next kept = %q", v)
		}
	})
	t.Run("bad request", func(t *testing.T) {
		h, _ := newTestServer(t, testOpts{provider: config.ProviderBasic})
		rec := do(t, h, request{method: http.MethodPost, path: "/login", form: "%zz"})
		if rec.Code != http.StatusBadRequest || text(parse(t, rec).Find(".alert")) != "Invalid request. Try again." {
			t.Errorf("bad form = %d %q", rec.Code, text(parse(t, rec).Find(".alert")))
		}
	})
	t.Run("sign in, account menu, signed-in login redirects", func(t *testing.T) {
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
		cookies := rec.Result().Cookies()
		doc := parse(t, do(t, h, request{path: "/c/main", cookies: cookies}))
		menu := doc.Find("details.account")
		if got := text(menu.Find("summary")); got != "alice operator" {
			t.Errorf("account = %q", got)
		}
		if a, _ := menu.Find("form[method=post]").Attr("action"); a != "/logout" {
			t.Errorf("sign out action = %q", a)
		}
		for next, want := range map[string]string{"/c/main": "/c/main", "https://evil.example/": "/", "": "/"} {
			rec := do(t, h, request{path: "/login?" + url.Values{"next": {next}}.Encode(), cookies: cookies})
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
				t.Errorf("signed-in GET /login?next=%q = %d %q, want %q", next, rec.Code, rec.Header().Get("Location"), want)
			}
		}
	})
}

// TestOIDCLoginPage: "Sign in with SSO" is a link to GET /login/sso, never
// a form, so the CSP form-action 'self' cannot block the redirect to the
// IdP; /login/sso starts the flow.
func TestOIDCLoginPage(t *testing.T) {
	idp := fakeIDP(t)
	h, _ := newTestServer(t, testOpts{provider: config.ProviderOIDC, idp: idp, basePath: "/asynq"})
	rec := do(t, h, request{path: "/asynq/login?next=%2Fasynq%2Fc%2Fmain"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login = %d", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	doc := parse(t, rec)
	link := doc.Find("a.btn")
	if text(link) != "Sign in with SSO" {
		t.Fatalf("SSO link = %q", text(link))
	}
	href, _ := link.Attr("href")
	if href != "/asynq/login/sso?next=%2Fasynq%2Fc%2Fmain" {
		t.Errorf("SSO href = %q", href)
	}
	if doc.Find("input[name=password]").Length() != 0 {
		t.Error("oidc login shows the password form")
	}
	doc.Find("form").Each(func(_ int, s *goquery.Selection) {
		action, _ := s.Attr("action")
		if u, err := url.Parse(action); err != nil || u.Host != "" || !strings.HasPrefix(action, "/") {
			t.Errorf("form posts off-origin: %q", action)
		}
	})
	if doc.Find(`form[action$="/login"]`).Length() != 0 {
		t.Error("SSO still posts a form")
	}

	start := do(t, h, request{path: href})
	loc, err := url.Parse(start.Header().Get("Location"))
	if start.Code != http.StatusSeeOther || err != nil || loc.Host != strings.TrimPrefix(idp.URL, "https://") || loc.Path != "/authorize" {
		t.Fatalf("GET %s = %d %q", href, start.Code, start.Header().Get("Location"))
	}
	if loc.Query().Get("code_challenge_method") != "S256" {
		t.Errorf("no PKCE: %s", loc)
	}
	var flow *http.Cookie
	for _, c := range start.Result().Cookies() {
		if c.Name == "asynqui_oidc" {
			flow = c
		}
	}
	if flow == nil || flow.Path != "/asynq/auth/callback" {
		t.Errorf("flow cookie = %+v", flow)
	}
	if start.Header().Get("Cache-Control") != "no-store" {
		t.Error("SSO start is cacheable")
	}
	// POST /login is the basic form only.
	if rec := do(t, h, request{method: http.MethodPost, path: "/asynq/login", form: "next=%2Fasynq%2F"}); rec.Code != http.StatusNotFound {
		t.Errorf("oidc POST /login = %d, want 404", rec.Code)
	}
}

func TestPrefs(t *testing.T) {
	for _, secure := range []bool{false, true} {
		h, _ := newTestServer(t, testOpts{basePath: "/asynq", cookieSecure: secure})
		rec := do(t, h, request{method: http.MethodPost, path: "/asynq/prefs", form: url.Values{
			"theme": {"dark"}, "refresh": {"10s"}, "next": {"/asynq/c/main"},
		}.Encode()})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/asynq/c/main" {
			t.Fatalf("prefs = %d %q", rec.Code, rec.Header().Get("Location"))
		}
		got := map[string]string{}
		for _, c := range rec.Result().Cookies() {
			got[c.Name] = c.Value
			if c.Path != "/asynq" || c.MaxAge != 365*24*3600 || c.SameSite != http.SameSiteLaxMode || c.HttpOnly || c.Secure != secure {
				t.Errorf("secure=%v: %s attributes = %+v", secure, c.Name, c)
			}
		}
		if got[themeCookie] != "dark" || got[refreshCookie] != "10s" {
			t.Errorf("cookies = %v", got)
		}
	}

	h, _ := newTestServer(t, testOpts{basePath: "/asynq"})
	cookies := []*http.Cookie{{Name: themeCookie, Value: "dark"}, {Name: refreshCookie, Value: "10s"}}
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
	rec := do(t, h, request{method: http.MethodPost, path: "/asynq/prefs", form: "theme=neon&refresh=1s"})
	if len(rec.Result().Cookies()) != 0 {
		t.Error("unknown values set cookies")
	}

	// The redirect never leaves the app (auth.SafeNext).
	for _, next := range []string{"https://evil.example/", "//evil.example/", "/other/", "/asynq/../x", `/asynq\x`, "/asynq/%2e%2e/x", ""} {
		rec := do(t, h, request{method: http.MethodPost, path: "/asynq/prefs", form: url.Values{"next": {next}}.Encode()})
		if loc := rec.Header().Get("Location"); loc != "/asynq/" {
			t.Errorf("next %q → %q", next, loc)
		}
	}
}

func TestErrorPages(t *testing.T) {
	t.Run("catch-all inside the shell", func(t *testing.T) {
		h, _ := newTestServer(t, testOpts{})
		rec := do(t, h, request{path: "/no/such/page"})
		if rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET /no/such/page = %d %q", rec.Code, rec.Header().Get("Cache-Control"))
		}
		doc := parse(t, rec)
		if got := text(doc.Find("main h1")); got != "Not found" {
			t.Errorf("h1 = %q", got)
		}
		var crumbs []string
		doc.Find(".crumbs li").Each(func(_ int, s *goquery.Selection) { crumbs = append(crumbs, text(s)) })
		if strings.Join(crumbs, " / ") != "Overview / Not found" {
			t.Errorf("crumbs = %q", crumbs)
		}
		if href, _ := doc.Find(".crumbs li a").Attr("href"); href != "/" {
			t.Errorf("Overview crumb href = %q", href)
		}
		if doc.Find(".crumbs [aria-current=page]").Length() != 1 {
			t.Error("last crumb not current")
		}
	})
	t.Run("forbidden POST is plain text", func(t *testing.T) {
		// Every GET route needs only viewer, so a 403 is always a POST.
		h, _ := newTestServer(t, testOpts{provider: config.ProviderProxy})
		rec := do(t, h, request{method: http.MethodPost, path: "/c/main/queues/pause", user: "x"})
		if rec.Code != http.StatusForbidden || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Errorf("POST 403 = %d %q", rec.Code, rec.Header().Get("Content-Type"))
		}
	})
	t.Run("without identity no shell", func(t *testing.T) {
		h, _ := newTestServer(t, testOpts{provider: config.ProviderBasic})
		rec := do(t, h, request{path: "/no/such/page"})
		if rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "<aside") || strings.Contains(body, "redis-main") {
			t.Error("anonymous 404 shows the shell or connections")
		}
		if got := text(parse(t, rec).Find("h1.h1")); got != "Not found" {
			t.Errorf("h1 = %q", got)
		}
	})
	t.Run("htmx and non-GET errors are plain", func(t *testing.T) {
		h, _ := newTestServer(t, testOpts{authz: config.Authz{Default: config.RoleViewer}})
		for _, rq := range []request{
			{path: "/c/nope", htmx: true},
			{path: "/no/such", htmx: true},
			{method: http.MethodPost, path: "/c/nope/recheck", htmx: true},
			{method: http.MethodPost, path: "/c/main/queues/pause", htmx: true},
			{method: http.MethodPost, path: "/no/such"},
		} {
			rec := do(t, h, rq)
			if rec.Code < 400 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") ||
				rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "<") {
				t.Errorf("%s %s (htmx=%v) = %d %q %q", rq.method, rq.path, rq.htmx, rec.Code,
					rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
			}
		}
	})
}

func TestInternalErrors(t *testing.T) {
	d, _, _ := newTestDeps(t, testOpts{})
	t.Run("panic", func(t *testing.T) {
		var logs bytes.Buffer
		h := recoverPanics(slog.New(slog.NewJSONHandler(&logs, nil)), d.internalError, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		}))
		rec := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusInternalServerError || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("panic = %d %q", rec.Code, rec.Header().Get("Cache-Control"))
		}
		if got := text(parse(t, rec).Find("h1.h1")); got != "Something went wrong" {
			t.Errorf("h1 = %q", got)
		}
	})
	t.Run("render failure", func(t *testing.T) {
		broken := templ.ComponentFunc(func(context.Context, io.Writer) error { return errors.New("broken") })
		rec := httptest.NewRecorder()
		d.render(rec, httptest.NewRequest(http.MethodGet, "/", nil), http.StatusOK, broken)
		if rec.Code != http.StatusInternalServerError || text(parse(t, rec).Find("h1.h1")) != "Something went wrong" {
			t.Errorf("render failure = %d %q", rec.Code, rec.Body)
		}
		rec = httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("HX-Request", "true")
		d.render(rec, r, http.StatusOK, broken)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "<") {
			t.Errorf("htmx render failure = %d %q", rec.Code, rec.Body)
		}
	})
}

// TestNoInlineStyleOrScript renders every page and fragment the server has
// and fails on anything the CSP would block, or on empty class attributes.
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
		"/c/main/info", "/c/nope", "/no/such/page",
		"/c/main?sort=name&dir=asc&range=90d&queue=low", "/c/analytics", "/c/local",
		"/c/main/queues/default?state=active", "/c/main/queues/default?state=aggregating", "/c/main/queues/default?state=scheduled",
		"/c/main/queues/default?state=retry&size=50", "/c/main/queues/default?state=archived", "/c/main/queues/default?state=completed",
		"/c/main/queues/nope", "/c/main/queues/default/find", "/c/main/queues/default/find?id=nope",
		"/c/main/queues/default/tasks/a0", "/c/main/queues/default/tasks/x0", "/c/main/queues/default/tasks/c0",
		"/c/main/queues/default/tasks/nope", "/c/main/queues/default/tasks/c3e81b90-04aa-4f1e-9d27-6b52e0c8a113?payload=raw",
		"/c/analytics/queues/default", "/c/analytics/queues/default/tasks/p00",
	} {
		cases = append(cases,
			page{testOpts{}, request{path: p}},
			page{testOpts{provider: config.ProviderProxy, authz: proxyAuthz, proxyLogout: "https://sso.example/logout"}, request{path: p, user: "alice"}},
		)
	}
	cases = append(cases,
		page{testOpts{}, request{path: "/", htmx: true}},
		page{testOpts{}, request{path: "/c/main", htmx: true}},
		page{testOpts{authz: proxyAuthz}, request{method: http.MethodPost, path: "/c/main/queues/pause", htmx: true, form: "queue=default&queue=gone"}},
		page{testOpts{authz: proxyAuthz}, request{method: http.MethodPost, path: "/c/main/queues/delete", form: "queue=low"}},
		page{testOpts{}, request{path: "/c/main", cookies: []*http.Cookie{testFlashCookie()}}},
		page{testOpts{}, request{path: "/c/main", htmx: true, header: map[string]string{"HX-Trigger": "queues-poll"}}},
		page{testOpts{}, request{path: "/c/analytics?poll=init", htmx: true, header: map[string]string{"HX-Trigger": "queues-poll"}}},
		page{testOpts{}, request{method: http.MethodPost, path: "/c/analytics/recheck", htmx: true}},
	)
	// The Servers, Schedulers and Info pages with data, and their answers.
	for _, p := range []struct{ path, query, poll string }{
		{"/c/main/servers", "", "servers-poll"},
		{"/c/main/servers/" + srvA, "", "server-poll"},
		{"/c/main/servers/" + srvA, "sort=started", "server-poll"},
		{"/c/main/servers/gone", "", "server-poll"},
		{"/c/main/schedulers", "", "schedulers-poll"},
		{"/c/main/schedulers/" + entryA, "page=2", "entry-poll"},
		{"/c/main/schedulers/" + entryA, "payload=raw", "entry-poll"},
		{"/c/main/schedulers/" + entryB, "", "entry-poll"},
		{"/c/main/schedulers/gone", "", "entry-poll"},
		{"/c/main/info", "", "info-poll"},
	} {
		u := p.path
		if p.query != "" {
			u += "?" + p.query
		}
		poll := map[string]string{"HX-Trigger": p.poll}
		cases = append(cases,
			page{testOpts{conns: infraConns()}, request{path: u}},
			page{testOpts{conns: infraConns()}, request{path: u, htmx: true}},
			page{testOpts{conns: infraConns()}, request{path: u, htmx: true, header: poll}},
			page{testOpts{conns: infraConns()}, request{path: p.path + "?" + strings.TrimPrefix(p.query+"&poll=init", "&"), htmx: true, header: poll}},
		)
	}
	cases = append(cases,
		page{testOpts{}, request{path: "/c/main/queues/default", htmx: true}},
		page{testOpts{}, request{path: "/c/main/queues/default", htmx: true, header: map[string]string{"HX-Trigger": "queue-poll"}}},
		page{testOpts{}, request{path: "/c/main/queues/default/tasks/p00?seen=retry", htmx: true, header: map[string]string{"HX-Trigger": "task-poll"}}},
		page{testOpts{}, request{path: "/c/main/queues/default/find?id=nope", htmx: true}},
		page{testOpts{authz: proxyAuthz}, request{method: http.MethodPost, path: "/c/main/queues/default/tasks/archive", htmx: true, form: "id=p00&id=gone&confirm=1"}},
		page{testOpts{authz: proxyAuthz}, request{method: http.MethodPost, path: "/c/main/queues/default/tasks/archive", form: "id=p00"}},
		page{testOpts{authz: proxyAuthz}, request{method: http.MethodPost, path: "/c/main/queues/default/tasks/run", form: "all=1&state=aggregating&group=g1"}},
		page{testOpts{}, request{path: "/c/main/queues/default?state=aggregating"}},
		page{testOpts{}, request{path: "/c/main/queues/default?state=aggregating&group=g1"}},
		page{testOpts{}, request{path: "/c/main/queues/default?state=archived&dir=asc"}},
		page{testOpts{authz: proxyAuthz}, request{method: http.MethodPost, path: "/c/main/queues/default/tasks/delete", form: "id=p00"}},
		page{testOpts{authz: proxyAuthz}, request{method: http.MethodPost, path: "/c/main/queues/default/tasks/run", form: "all=1&state=retry"}},
		page{testOpts{authz: proxyAuthz}, request{method: http.MethodPost, path: "/c/main/queues/pause", htmx: true, form: "queue=default&from=queue"}},
		page{testOpts{provider: config.ProviderBasic}, request{path: "/login"}},
		page{testOpts{provider: config.ProviderBasic}, request{path: "/no/such/page"}},
		page{testOpts{provider: config.ProviderBasic}, request{method: http.MethodPost, path: "/login", form: "username=a&password=b"}},
		page{testOpts{provider: config.ProviderOIDC}, request{path: "/login"}},
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
		if strings.Contains(rec.Body.String(), `class=""`) {
			t.Errorf("%s: empty class attribute", name)
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
			if n.Data == "form" {
				if a, _ := s.Attr("action"); !strings.HasPrefix(a, "/") || strings.HasPrefix(a, "//") {
					t.Errorf("%s: form action %q is not same-origin", name, a)
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
		name     string
		opts     testOpts
		user     string
		path     string
		visible  bool
		signOut  string // "", "form" or "link"
		wantRole string
	}{
		{"none hides it", testOpts{}, "", "/", false, "", ""},
		{"proxy without logout url", testOpts{provider: config.ProviderProxy}, "bob", "/", true, "", "viewer"},
		{"proxy with logout url links straight to it", testOpts{provider: config.ProviderProxy, proxyLogout: "https://sso.example/out"}, "bob", "/", true, "link", "viewer"},
		{"highest role on the overview", testOpts{provider: config.ProviderProxy, authz: config.Authz{
			Default:  config.RoleViewer,
			Bindings: []config.Binding{{Users: []string{"bob"}, Role: config.RoleAdmin, Connections: []string{"local"}}},
		}}, "bob", "/", true, "", "admin"},
		{"role on the current connection", testOpts{provider: config.ProviderProxy, authz: config.Authz{
			Default:  config.RoleViewer,
			Bindings: []config.Binding{{Users: []string{"bob"}, Role: config.RoleAdmin, Connections: []string{"local"}}},
		}}, "bob", "/c/main", true, "", "viewer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestServer(t, tt.opts)
			doc := parse(t, do(t, h, request{path: tt.path, user: tt.user}))
			acct := doc.Find(".account")
			if (acct.Length() > 0) != tt.visible {
				t.Fatalf("account visible = %v", acct.Length() > 0)
			}
			if !tt.visible {
				return
			}
			switch tt.signOut {
			case "link":
				if href, _ := acct.Find("a.menu-item").Attr("href"); href != tt.opts.proxyLogout {
					t.Errorf("sign out href = %q", href)
				}
				if acct.Find("form").Length() != 0 {
					t.Error("proxy sign out is a form")
				}
			case "":
				if acct.Find(".menu-item").Length() != 0 {
					t.Error("sign out shown")
				}
			}
			if got := text(acct.Find(".account-role")); got != tt.wantRole {
				t.Errorf("role = %q, want %q", got, tt.wantRole)
			}
			if !strings.Contains(acct.Text(), tt.user) {
				t.Errorf("user missing: %q", acct.Text())
			}
		})
	}
}

func TestShellLandmarksAndCrumbs(t *testing.T) {
	h, _ := newTestServer(t, testOpts{})
	doc := parse(t, do(t, h, request{path: "/c/main/servers/abcdefghijklmnop"}))

	if href, _ := doc.Find("body > a.skip-link").Attr("href"); href != "#content" || doc.Find("main#content").Length() != 1 {
		t.Error("skip link or its target missing")
	}
	if doc.Find("aside[aria-label]").Length() != 1 || doc.Find("nav[aria-label=Breadcrumb]").Length() != 1 {
		t.Error("landmarks unnamed")
	}
	cur := doc.Find(".conn-list .conn-current")
	if cur.Length() != 1 || text(cur.Find(".citem-name")) != "main Reachable" {
		t.Fatalf("current connection = %q", text(cur.Find(".citem-name")))
	}
	on := cur.Find(".subnav a.on")
	if text(on) != "Servers" {
		t.Errorf("active sub-item = %q", text(on))
	}
	if href, _ := on.Attr("href"); href != "/c/main/servers" {
		t.Errorf("servers href = %q", href)
	}
	if doc.Find(`.nav[aria-label=Global] a.on`).Length() != 0 {
		t.Error("Overview active on a connection page")
	}
	var crumbs []string
	doc.Find(".crumbs li").Each(func(_ int, s *goquery.Selection) { crumbs = append(crumbs, text(s)) })
	if got := strings.Join(crumbs, " / "); got != "Overview / main / Servers / abcdefghijkl…" {
		t.Errorf("crumbs = %q", got)
	}
	if doc.Find(".crumbs li a").Length() != 3 || doc.Find(".crumbs li:last-child [aria-current=page]").Length() != 1 {
		t.Error("every crumb but the last must be a link")
	}
	if got := strings.Join(strings.Fields(doc.Find("title").Text()), " "); got != "abcdefghijkl… – asynqui" {
		t.Errorf("title = %q", got)
	}
	if got := text(doc.Find(".side-foot .version")); got != "v0.1.0" {
		t.Errorf("footer version = %q", got)
	}
	if href, _ := doc.Find(".side-foot .version").Attr("href"); href != releasesURL+"/tag/v0.1.0" {
		t.Errorf("release href = %q", href)
	}

	doc = parse(t, do(t, h, request{path: "/c/main/info"}))
	if got := strings.Join(strings.Fields(doc.Find("title").Text()), " "); got != "Redis info – asynqui" {
		t.Errorf("section title = %q", got)
	}
}
