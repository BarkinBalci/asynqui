package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// clock is a settable time source shared by a test and the code under test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Now()} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func testKeys(b byte) KeyPair {
	return KeyPair{HashKey: bytes.Repeat([]byte{b}, 32), BlockKey: bytes.Repeat([]byte{b + 1}, 32)}
}

func testSession() SessionOptions { return SessionOptions{KeyPair: testKeys(1)} }

func mustHash(t *testing.T, password string, cost int) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

func mustNew(t *testing.T, o Options) *Authenticator {
	t.Helper()
	a, err := New(context.Background(), o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// app wires the authenticator the way web will.
func app(a *Authenticator) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /login", a.LoginHandler(nil))
	mux.Handle("GET /login/sso", a.SSOHandler(nil))
	mux.Handle("GET /auth/callback", a.CallbackHandler(nil))
	mux.Handle("POST /logout", a.LogoutHandler())
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFrom(r.Context())
		if !ok {
			a.Challenge(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(id)
	})
	return a.Middleware(mux)
}

type req struct {
	method  string
	target  string
	form    url.Values
	cookies []*http.Cookie
	header  http.Header
	remote  string
}

type resp struct {
	status  int
	header  http.Header
	cookies map[string]*http.Cookie
	body    string
}

func do(t *testing.T, h http.Handler, rq req) resp {
	t.Helper()
	var body io.Reader
	if rq.form != nil {
		body = strings.NewReader(rq.form.Encode())
	}
	method := rq.method
	if method == "" {
		method = http.MethodGet
	}
	r := httptest.NewRequest(method, rq.target, body)
	if rq.form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, vs := range rq.header {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	if rq.remote != "" {
		r.RemoteAddr = rq.remote
	}
	for _, c := range rq.cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	res := w.Result()
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, cookies: map[string]*http.Cookie{}, body: strings.TrimSpace(string(b))}
	for _, c := range res.Cookies() {
		out.cookies[c.Name] = c
	}
	return out
}

// whoami returns the identity the middleware attaches for these cookies.
func whoami(t *testing.T, h http.Handler, cookies ...*http.Cookie) (Identity, bool) {
	t.Helper()
	res := do(t, h, req{target: "/whoami", cookies: cookies})
	if res.status != http.StatusOK {
		return Identity{}, false
	}
	return decodeIdentity(t, res.body), true
}

func decodeIdentity(t *testing.T, body string) Identity {
	t.Helper()
	var id Identity
	if err := json.Unmarshal([]byte(body), &id); err != nil {
		t.Fatalf("decode whoami %q: %v", body, err)
	}
	return id
}
