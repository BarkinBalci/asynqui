package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/crypto/bcrypt"
)

func TestNewErrors(t *testing.T) {
	user := []BasicUser{{Username: "u", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}
	tests := []struct {
		name    string
		o       Options
		wantErr string
	}{
		{"unknown provider", Options{Provider: "ldap"}, `unknown provider "ldap"`},
		{"empty provider", Options{}, `unknown provider ""`},
		{"relative base path", Options{Provider: KindNone, BasePath: "asynq"}, "must start with /"},
		{"basic without keys", Options{Provider: KindBasic, Basic: BasicOptions{Users: user}}, "hash key must be 32 or 64 bytes"},
		{"basic without users", Options{Provider: KindBasic, Session: testSession()}, "no users configured"},
		{"oidc without keys", Options{Provider: KindOIDC}, "hash key"},
		{"oidc without issuer", Options{Provider: KindOIDC, Session: testSession()}, "issuer is required"},
		{"oidc without client", Options{Provider: KindOIDC, Session: testSession(), OIDC: OIDCOptions{Issuer: "https://x"}}, "client id is required"},
		{"oidc without redirect", Options{Provider: KindOIDC, Session: testSession(), OIDC: OIDCOptions{Issuer: "https://x", ClientID: "c"}}, "redirect url is required"},
		{"oidc relative redirect", Options{Provider: KindOIDC, Session: testSession(), OIDC: OIDCOptions{Issuer: "https://x", ClientID: "c", RedirectURL: "/auth/callback"}}, "must be absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(context.Background(), tt.o)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNoneAndProxyNeedNoKeys(t *testing.T) {
	for _, k := range []Kind{KindNone, KindProxy} {
		a := mustNew(t, Options{Provider: k})
		if a.Kind() != k || a.SignOutVisible() {
			t.Errorf("%s: Kind=%s SignOutVisible=%v", k, a.Kind(), a.SignOutVisible())
		}
	}
}

func TestNoneProvider(t *testing.T) {
	a := mustNew(t, Options{Provider: KindNone})
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
	}
	for _, tt := range tests {
		a := &Authenticator{basePath: tt.base}
		if got := a.safeNext(tt.next); got != tt.want {
			t.Errorf("base %q safeNext(%q) = %q, want %q", tt.base, tt.next, got, tt.want)
		}
	}
}

func TestChallenge(t *testing.T) {
	user := []BasicUser{{Username: "u", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}
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
		{"htmx without current url", basicAuth(t, user, nil, ""), req{target: "/c/main", header: http.Header{"Hx-Request": {"true"}}}, 401, "", "/login?next=%2Fc%2Fmain"},
		{"proxy", mustNew(t, Options{Provider: KindProxy}), req{target: "/c/main"}, 401, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.HandlerFunc(tt.a.Challenge)
			res := do(t, h, tt.rq)
			if res.status != tt.wantStatus || res.header.Get("Location") != tt.wantLocation || res.header.Get("HX-Redirect") != tt.wantHX {
				t.Errorf("Challenge = %d Location=%q HX-Redirect=%q; want %d %q %q",
					res.status, res.header.Get("Location"), res.header.Get("HX-Redirect"), tt.wantStatus, tt.wantLocation, tt.wantHX)
			}
		})
	}
}

func TestMiddlewareClearsBadCookie(t *testing.T) {
	user := []BasicUser{{Username: "u", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}
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
