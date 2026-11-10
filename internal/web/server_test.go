package web

import (
	"net/http"
	"net/http/httptest"
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
			request{path: "/c/hidden", user: "alice"}, http.StatusNotFound, map[string]string{"Cache-Control": "no-store"}},
		{"hidden sub-page is 404", config.ProviderProxy,
			request{path: "/c/hidden/servers", user: "alice"}, http.StatusNotFound, nil},
		{"unknown connection is 404", config.ProviderProxy,
			request{path: "/c/nope", user: "alice"}, http.StatusNotFound, nil},
		{"hidden action is 404, not 403", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/hidden/queues/delete", user: "carol"}, http.StatusNotFound, nil},
		{"viewer may recheck", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/main/recheck", user: "alice"}, http.StatusSeeOther, nil},
		{"viewer cannot pause", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/main/queues/pause", user: "alice"}, http.StatusForbidden, map[string]string{"Cache-Control": "no-store"}},
		{"operator may pause", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/main/queues/pause", user: "bob"}, http.StatusNotImplemented, map[string]string{"Cache-Control": "no-store"}},
		{"operator cannot delete", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/main/queues/q1/tasks/delete", user: "bob"}, http.StatusForbidden, nil},
		{"admin may delete", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/main/queues/q1/tasks/delete", user: "carol"}, http.StatusNotImplemented, nil},
		{"scheduler run now is not a route", config.ProviderProxy,
			request{method: http.MethodPost, path: "/c/main/schedulers/run", user: "carol"}, http.StatusNotFound, nil},
		{"none: everyone gets the default", config.ProviderNone,
			request{path: "/c/main"}, http.StatusNotFound, nil}, // default none hides
		{"probes need no identity", config.ProviderBasic,
			request{path: "/health/readiness"}, http.StatusOK, nil},
		{"static needs no identity", config.ProviderBasic,
			request{path: "/static/nope.css"}, http.StatusNotFound, map[string]string{"Cache-Control": "no-store"}},
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

func TestCrossOriginProtection(t *testing.T) {
	tests := []struct {
		name     string
		provider config.Provider
		path     string
		form     string
		sameWant int
	}{
		{"recheck", config.ProviderNone, "/c/main/recheck", "", http.StatusSeeOther},
		{"prefs", config.ProviderNone, "/prefs", "theme=dark", http.StatusSeeOther},
		{"login", config.ProviderBasic, "/login", "username=alice&password=x", http.StatusUnauthorized},
		{"logout", config.ProviderBasic, "/logout", "", http.StatusSeeOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newTestServer(t, testOpts{provider: tt.provider})
			cross := do(t, h, request{method: http.MethodPost, path: tt.path, form: tt.form, header: map[string]string{"Sec-Fetch-Site": "cross-site"}})
			if cross.Code != http.StatusForbidden {
				t.Errorf("cross-site POST %s = %d, want 403", tt.path, cross.Code)
			}
			if len(cross.Result().Cookies()) != 0 {
				t.Errorf("cross-site POST %s set cookies", tt.path)
			}
			origin := do(t, h, request{method: http.MethodPost, path: tt.path, form: tt.form, header: map[string]string{"Origin": "https://evil.example"}})
			if origin.Code != http.StatusForbidden {
				t.Errorf("foreign-Origin POST %s = %d, want 403", tt.path, origin.Code)
			}
			same := do(t, h, request{method: http.MethodPost, path: tt.path, form: tt.form, header: map[string]string{"Sec-Fetch-Site": "same-origin"}})
			if same.Code != tt.sameWant {
				t.Errorf("same-origin POST %s = %d, want %d", tt.path, same.Code, tt.sameWant)
			}
		})
	}
}

func TestSecurityHeadersEverywhere(t *testing.T) {
	h, _ := newTestServer(t, testOpts{provider: config.ProviderBasic})
	css := staticPath(t, h, "stylesheet", "/login")
	for _, rq := range []request{
		{path: "/"}, {path: "/login"}, {path: "/health/liveness"}, {path: css}, {path: "/static/missing"},
		{path: "/nope"}, {path: "/c/main", htmx: true}, {method: http.MethodPost, path: "/prefs"},
	} {
		rec := do(t, h, rq)
		hd := rec.Header()
		if csp := hd.Get("Content-Security-Policy"); csp != contentSecurityPolicy {
			t.Errorf("%s: CSP = %q", rq.path, csp)
		}
		if hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "same-origin" ||
			hd.Get("Cross-Origin-Opener-Policy") != "same-origin" || hd.Get("Permissions-Policy") != permissionsPolicy {
			t.Errorf("%s: missing a security header: %v", rq.path, hd)
		}
	}
	if !strings.HasPrefix(contentSecurityPolicy, "default-src 'none';") || !strings.Contains(contentSecurityPolicy, "form-action 'self'") {
		t.Errorf("CSP = %q", contentSecurityPolicy)
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
	etag := do(t, h, request{path: urls[1]}).Header().Get("ETag")
	if rec := do(t, h, request{path: urls[1], header: map[string]string{"If-None-Match": etag}}); rec.Code != http.StatusNotModified {
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
		{"/asynq/nope", http.StatusNotFound},
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
			if !ok || strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "#") {
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
	css := staticPath(t, h, "stylesheet", "/asynq/")
	if rec := do(t, h, request{path: css}); rec.Code != http.StatusOK {
		t.Errorf("GET %s = %d", css, rec.Code)
	}
}

func TestURLHelpers(t *testing.T) {
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
	if got := d.urlQuery(map[string][]string{"next": {""}}, "login", "sso"); got != "/asynq/login/sso" {
		t.Errorf("urlQuery without values = %q", got)
	}
	if got := d.urlQuery(map[string][]string{"next": {"/asynq/c/a b"}}, "login", "sso"); got != "/asynq/login/sso?next=%2Fasynq%2Fc%2Fa+b" {
		t.Errorf("urlQuery = %q", got)
	}
}

// staticPath finds the hashed URL of a page's <link rel=…>.
func staticPath(t *testing.T, h http.Handler, rel, page string) string {
	t.Helper()
	doc := parse(t, do(t, h, request{path: page}))
	href, ok := doc.Find(`link[rel=` + rel + `]`).Attr("href")
	if !ok {
		t.Fatalf("no %s link on %s", rel, page)
	}
	return href
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}
