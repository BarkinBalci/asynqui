package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/crypto/bcrypt"

	"github.com/BarkinBalci/asynqui/internal/config"
)

func TestNewUnknownProvider(t *testing.T) {
	if _, err := newTest(context.Background(), testOpts{Provider: "ldap"}); err == nil || !strings.Contains(err.Error(), `unknown provider "ldap"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestNoneAndProxyNeedNoKeys(t *testing.T) {
	for _, k := range []Kind{KindNone, KindProxy} {
		a := mustNew(t, testOpts{Provider: config.Provider(k), Proxy: config.Proxy{TrustedProxies: []string{"10.0.0.1"}}})
		if a.Kind() != k || a.SignOutVisible() {
			t.Errorf("%s: Kind=%s SignOutVisible=%v", k, a.Kind(), a.SignOutVisible())
		}
	}
}

func TestNoneProvider(t *testing.T) {
	a := mustNew(t, testOpts{Provider: config.ProviderNone})
	h := app(a)
	id, ok := whoami(t, h)
	if !ok || !cmp.Equal(id, Identity{}) {
		t.Errorf("none identity = %+v, %v; want zero, true", id, ok)
	}
	for _, rq := range []req{
		{method: http.MethodPost, target: "/login"},
		{target: "/auth/callback"},
		{method: http.MethodPost, target: "/logout"},
	} {
		if res := do(t, h, rq); res.status != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", rq.method, rq.target, res.status)
		}
	}
}

func TestIdentityContext(t *testing.T) {
	if _, ok := IdentityFrom(context.Background()); ok {
		t.Error("empty context has an identity")
	}
	want := Identity{Subject: "s", Name: "n", Email: "e", Groups: []string{"g"}}
	got, ok := IdentityFrom(WithIdentity(context.Background(), want))
	if diff := cmp.Diff(want, got); !ok || diff != "" {
		t.Errorf("IdentityFrom ok=%v (-want +got):\n%s", ok, diff)
	}
}

func TestSafeNext(t *testing.T) {
	tests := []struct {
		base string
		next string
		want string
	}{
		{"", "", "/"},
		{"", "/", "/"},
		{"", "/c/main", "/c/main"},
		{"", "/c/main/queues/default?state=retry&page=2", "/c/main/queues/default?state=retry&page=2"},
		{"", "c/main", "/"},
		{"", "//evil.example", "/"},
		{"", "///evil.example", "/"},
		{"", "/\\evil.example", "/"},
		{"", "https://evil.example/", "/"},
		{"", "javascript:alert(1)", "/"},
		{"", "/ok\r\nSet-Cookie: x", "/"},
		{"", "/%zz", "/"},
		{"/asynq", "/asynq", "/asynq"},
		{"/asynq", "/asynq/", "/asynq/"},
		{"/asynq", "/asynq/c/main", "/asynq/c/main"},
		{"/asynq", "/asynqx", "/asynq/"},
		{"/asynq", "/other", "/asynq/"},
		{"/asynq", "", "/asynq/"},

		// Encoded dots, slashes and backslashes are refused outright.
		{"", "/%2F%2Fevil", "/"},
		{"", "/%2f%2fevil", "/"},
		{"", "/%5Cevil", "/"},
		{"", "/%5c%5cevil", "/"},
		{"", "/asynq%2fx", "/"},
		{"", "/asynq/%2e%2e/evil", "/"},
		{"", "/asynq/..%2fevil", "/"},
		{"", "/asynq/%2E%2E%2Fevil", "/"},
		{"/asynq", "/asynq%2fx", "/asynq/"},
		{"/asynq", "/asynq/%2e%2e/evil", "/asynq/"},
		{"/asynq", "/asynq/..%2fevil", "/asynq/"},
		{"/asynq", "/%2F%2Fevil", "/asynq/"},
		{"/asynq", "/%5Cevil", "/asynq/"},

		// The prefix is checked on the cleaned path, and the cleaned path
		// is what is returned.
		{"", "/asynq/../evil", "/evil"},
		{"", "/./asynq/../evil", "/evil"},
		{"", "/asynq/./../../evil", "/evil"},
		{"/asynq", "/asynq/../evil", "/asynq/"},
		{"/asynq", "/./asynq/../evil", "/asynq/"},
		{"/asynq", "/asynq/./../../evil", "/asynq/"},
		{"/asynq", "/asynq/c/../c/main", "/asynq/c/main"},
		{"/asynq", "/asynq/c/main/", "/asynq/c/main/"},
		{"/asynq", "/asynq//c", "/asynq/c"},

		// Re-serialized: odd characters are escaped, fragments dropped,
		// queries kept.
		{"", "/@evil", "/@evil"},
		{"", "/ /evil", "/%20/evil"},
		{"/asynq", "/ /evil", "/asynq/"},
		{"/asynq", "/@evil", "/asynq/"},
		{"/asynq", "/asynq/x#//evil", "/asynq/x"},
		{"/asynq", "/asynq?next=//evil", "/asynq?next=//evil"},
		{"/asynq", "/asynq/c/%E2%9C%93", "/asynq/c/%E2%9C%93"},
		{"/asynq", "/asynq/%00", "/asynq/"},
		{"/asynq", "/asynq/" + strings.Repeat("a", 1100), "/asynq/"},
	}
	for _, tt := range tests {
		a := &Authenticator{basePath: tt.base}
		got := a.SafeNext(tt.next)
		if got != tt.want {
			t.Errorf("base %q safeNext(%q) = %q, want %q", tt.base, tt.next, got, tt.want)
		}
		// http.Redirect must pass it through unchanged.
		w := httptest.NewRecorder()
		http.Redirect(w, httptest.NewRequest(http.MethodPost, "/login", nil), got, http.StatusSeeOther)
		if loc := w.Header().Get("Location"); loc != got {
			t.Errorf("base %q next %q: Location = %q, want %q", tt.base, tt.next, loc, got)
		}
	}
}

func TestChallenge(t *testing.T) {
	user := []config.BasicUser{{Username: "u", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}
	tests := []struct {
		name         string
		a            *Authenticator
		rq           req
		wantStatus   int
		wantLocation string
		wantHX       string
	}{
		{"page", basicAuth(t, user, nil, ""), req{target: "/whoami?x=1"}, 303, "/login?next=%2Fwhoami%3Fx%3D1", ""},
		{"root needs no next", basicAuth(t, user, nil, ""), req{target: "/"}, 303, "/login", ""},
		{"base path", basicAuth(t, user, nil, "/asynq"), req{target: "/asynq/c/main"}, 303, "/asynq/login?next=%2Fasynq%2Fc%2Fmain", ""},
		{"post loses next", basicAuth(t, user, nil, ""), req{method: http.MethodPost, target: "/c/main/queues/pause"}, 303, "/login", ""},
		{"htmx fragment uses current page", basicAuth(t, user, nil, ""), req{target: "/c/main?fragment=1", header: http.Header{
			"Hx-Request": {"true"}, "Hx-Current-Url": {"http://example.com/c/main/servers"}}}, 401, "", "/login?next=%2Fc%2Fmain%2Fservers"},
		{"proxy", proxyAuth(t, config.Proxy{TrustedProxies: []string{"10.0.0.1"}}), req{target: "/c/main"}, 401, "", ""},
		{"none", mustNew(t, testOpts{Provider: config.ProviderNone}), req{target: "/c/main"}, 401, "", ""},
		{"absolute-form request target", basicAuth(t, user, nil, "/asynq"), req{target: "http://evil.example/asynq/c/main"}, 303, "/asynq/login", ""},
		{"htmx without current url goes to root", basicAuth(t, user, nil, "/asynq"), req{target: "/asynq/c/main", header: http.Header{"Hx-Request": {"true"}}}, 401, "", "/asynq/login"},
		{"htmx current url with ..", basicAuth(t, user, nil, "/asynq"), req{target: "/asynq/c/main", header: http.Header{
			"Hx-Request": {"true"}, "Hx-Current-Url": {"https://x/asynq/../evil"}}}, 401, "", "/asynq/login"},
		{"htmx current url on another host keeps only the path", basicAuth(t, user, nil, "/asynq"), req{target: "/asynq/c/main", header: http.Header{
			"Hx-Request": {"true"}, "Hx-Current-Url": {"https://evil.example/asynq/c/main"}}}, 401, "", "/asynq/login?next=%2Fasynq%2Fc%2Fmain"},
		{"htmx current url doubled host", basicAuth(t, user, nil, "/asynq"), req{target: "/asynq/c/main", header: http.Header{
			"Hx-Request": {"true"}, "Hx-Current-Url": {"https://evil.example//evil.example/x"}}}, 401, "", "/asynq/login"},
		{"htmx current url outside base", basicAuth(t, user, nil, "/asynq"), req{target: "/asynq/c/main", header: http.Header{
			"Hx-Request": {"true"}, "Hx-Current-Url": {"https://x/asynqx"}}}, 401, "", "/asynq/login"},
		{"htmx javascript url", basicAuth(t, user, nil, "/asynq"), req{target: "/asynq/c/main", header: http.Header{
			"Hx-Request": {"true"}, "Hx-Current-Url": {"javascript:alert(1)"}}}, 401, "", "/asynq/login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.HandlerFunc(tt.a.Challenge)
			res := do(t, h, tt.rq)
			if res.status != tt.wantStatus || res.header.Get("Location") != tt.wantLocation || res.header.Get("HX-Redirect") != tt.wantHX {
				t.Errorf("Challenge = %d Location=%q HX-Redirect=%q; want %d %q %q",
					res.status, res.header.Get("Location"), res.header.Get("HX-Redirect"), tt.wantStatus, tt.wantLocation, tt.wantHX)
			}
			if cc := res.header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", cc)
			}
		})
	}
}

func TestMiddlewareClearsBadCookie(t *testing.T) {
	user := []config.BasicUser{{Username: "u", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}
	h := app(basicAuth(t, user, nil, ""))
	res := do(t, h, req{target: "/whoami", cookies: []*http.Cookie{{Name: sessionCookie, Value: "forged"}}})
	if res.status != http.StatusSeeOther {
		t.Errorf("status = %d, want 303 to login", res.status)
	}
	if c := res.cookies[sessionCookie]; c == nil || c.MaxAge >= 0 {
		t.Errorf("bad cookie not cleared: %+v", c)
	}
	if res := do(t, h, req{target: "/whoami"}); len(res.cookies) != 0 {
		t.Error("cookie written for a request without one")
	}
}
