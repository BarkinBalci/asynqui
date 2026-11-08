package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func (f *fakeIDP) signOrRaw(c map[string]any) string {
	if f.signRaw != nil {
		return f.signRaw(c)
	}
	return f.sign(c)
}

func (f *fakeIDP) algList() []string {
	if f.algs != nil {
		return f.algs
	}
	return []string{"RS256"}
}

func b64JSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// TestOIDCAlgConfusion: an IdP (or attacker) advertising "none" or HS256
// must not get unsigned or secret-keyed tokens accepted.
func TestOIDCAlgConfusion(t *testing.T) {
	for _, alg := range []string{"none", "HS256"} {
		t.Run(alg, func(t *testing.T) {
			idp := newFakeIDP(t)
			idp.algs = []string{"RS256", "HS256", "none"}
			idp.signRaw = func(c map[string]any) string {
				signing := b64JSON(t, map[string]any{"alg": alg, "typ": "JWT", "kid": "k1"}) + "." + b64JSON(t, c)
				if alg == "none" {
					return signing + "."
				}
				m := hmac.New(sha256.New, []byte(testClientSecret))
				m.Write([]byte(signing))
				return signing + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
			}
			h := app(mustNew(t, idp.options("")))
			res := idp.flow(t, h, "/", nil)
			if res.callback.status != http.StatusUnauthorized || res.callback.cookies[sessionCookie] != nil {
				t.Errorf("alg %s: callback = %d, session=%v", alg, res.callback.status, res.callback.cookies[sessionCookie] != nil)
			}
		})
	}
}

// TestOIDCNoSubjectAnywhere: neither the ID token nor userinfo may supply
// an identity without a subject.
func TestOIDCNoSubjectAnywhere(t *testing.T) {
	idp := newFakeIDP(t)
	idp.claims = func(c map[string]any) { delete(c, "sub"); delete(c, "groups") }
	idp.userinfo = map[string]any{"groups": []string{"platform"}}
	h := app(mustNew(t, idp.options("")))
	res := idp.flow(t, h, "/", nil)
	if res.callback.status != http.StatusUnauthorized || res.callback.cookies[sessionCookie] != nil {
		t.Errorf("callback = %d, session=%v", res.callback.status, res.callback.cookies[sessionCookie] != nil)
	}
}

// TestOIDCErrorNotReflected: the IdP's error parameters reach the log, quoted,
// never the response.
func TestOIDCErrorNotReflected(t *testing.T) {
	idp := newFakeIDP(t)
	var logs bytes.Buffer
	o := idp.options("")
	o.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	h := app(mustNew(t, o))
	res := idp.flow(t, h, "/", func(q url.Values, _ **http.Cookie) {
		q.Del("code")
		q.Set("error", "<script>alert(1)</script>")
		q.Set("error_description", "<img src=x onerror=alert(1)>")
	})
	if res.callback.status != http.StatusUnauthorized {
		t.Errorf("status = %d", res.callback.status)
	}
	if strings.Contains(res.callback.body, "<") || !strings.HasPrefix(res.callback.header.Get("Content-Type"), "text/plain") {
		t.Errorf("response reflects input: %q (%s)", res.callback.body, res.callback.header.Get("Content-Type"))
	}
	if strings.Contains(logs.String(), "onerror") {
		t.Error("error_description logged")
	}
	if !strings.Contains(logs.String(), `<script>`) {
		t.Errorf("error not logged escaped: %s", logs.String())
	}
}

// TestOIDCForgedCallbackKeepsLogin: a cross-site top-level GET to
// /auth/callback carries the Lax flow cookie; it must not cancel the
// user's own login in progress.
func TestOIDCForgedCallbackKeepsLogin(t *testing.T) {
	idp := newFakeIDP(t)
	h := app(mustNew(t, idp.options("")))
	res := idp.flow(t, h, "/c/main", func(_ url.Values, f **http.Cookie) {
		// Before the real callback, an attacker's forged one arrives.
		forged := do(t, h, req{target: "/auth/callback?state=forged&code=x", cookies: []*http.Cookie{*f}})
		if forged.status != http.StatusBadRequest {
			t.Errorf("forged callback = %d, want 400", forged.status)
		}
		if _, cleared := forged.cookies[flowCookie]; cleared {
			t.Error("forged callback cleared the flow cookie")
		}
	})
	// The user's real callback still completes.
	if res.callback.status != http.StatusSeeOther || res.callback.header.Get("Location") != "/c/main" {
		t.Errorf("real callback = %d %q", res.callback.status, res.callback.header.Get("Location"))
	}
}

func TestOIDCNextCapped(t *testing.T) {
	idp := newFakeIDP(t)
	h := app(mustNew(t, idp.options("")))
	start := do(t, h, req{method: http.MethodPost, target: "/login", form: url.Values{"next": {"/" + strings.Repeat("a", 5000)}}})
	fc := start.cookies[flowCookie]
	if start.status != http.StatusSeeOther || fc == nil || len(fc.String()) > maxDecodeBytes {
		t.Fatalf("POST /login with 5KB next = %d, flow cookie %d bytes", start.status, len(fc.String()))
	}
	code, state := idp.authorize(start.header.Get("Location"))
	cb := do(t, h, req{target: "/auth/callback?" + url.Values{"code": {code}, "state": {state}}.Encode(), cookies: []*http.Cookie{fc}})
	if cb.header.Get("Location") != "/" {
		t.Errorf("oversized next not dropped: %q", cb.header.Get("Location"))
	}
}

// TestCrossProviderCookie: deployments sharing session keys must not
// accept each other's sessions.
func TestCrossProviderCookie(t *testing.T) {
	users := []BasicUser{{Username: "jdoe@example.com", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}
	hb := app(basicAuth(t, users, nil, ""))
	basicCookie := do(t, hb, login(url.Values{"username": {"jdoe@example.com"}, "password": {"p"}})).cookies[sessionCookie]

	idp := newFakeIDP(t)
	ho := app(mustNew(t, idp.options("")))
	oidcCookie := idp.flow(t, ho, "/", nil).callback.cookies[sessionCookie]

	if _, ok := whoami(t, ho, basicCookie); ok {
		t.Error("oidc accepted a basic session")
	}
	if _, ok := whoami(t, hb, oidcCookie); ok {
		t.Error("basic accepted an oidc session")
	}
}

// TestNoServerSideRevocation documents the spec: logout clears the cookie
// in that browser only; a copied cookie stays valid until it expires.
func TestNoServerSideRevocation(t *testing.T) {
	users := []BasicUser{{Username: "u", PasswordHash: mustHash(t, "p", bcrypt.MinCost)}}
	h := app(basicAuth(t, users, nil, ""))
	sc := do(t, h, login(url.Values{"username": {"u"}, "password": {"p"}})).cookies[sessionCookie]
	out := do(t, h, req{method: http.MethodPost, target: "/logout", cookies: []*http.Cookie{sc}})
	if c := out.cookies[sessionCookie]; c == nil || c.MaxAge >= 0 {
		t.Fatal("logout did not clear the cookie")
	}
	if _, ok := whoami(t, h, sc); !ok {
		t.Error("copied cookie rejected; update the docs if revocation was added")
	}
}
