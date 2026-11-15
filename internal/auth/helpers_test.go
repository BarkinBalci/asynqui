package auth

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/crypto/bcrypt"

	"github.com/BarkinBalci/asynqui/internal/config"
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

func testKeys(b byte) config.KeyPair {
	return config.KeyPair{
		HashKey:  config.Secret(hex.EncodeToString(bytes.Repeat([]byte{b}, 32))),
		BlockKey: config.Secret(hex.EncodeToString(bytes.Repeat([]byte{b + 1}, 32))),
	}
}

// keySession is a session config with keys k (and previous ones), the
// default TTL and Secure cookies.
func keySession(k config.KeyPair, previous ...config.KeyPair) config.Session {
	return config.Session{HashKey: k.HashKey, BlockKey: k.BlockKey, PreviousKeys: previous, TTL: config.DefaultSessionTTL, CookieSecure: true}
}

func testSession() config.Session { return keySession(testKeys(1)) }

func mustHash(t *testing.T, password string, cost int) config.Secret {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		t.Fatal(err)
	}
	return config.Secret(h)
}

// testOpts is what a test builds an Authenticator from: the auth section,
// with config's defaults for what it leaves empty, and New's other
// arguments.
type testOpts struct {
	Provider      config.Provider
	Session       config.Session
	SessionGroups []string
	Basic         config.Basic
	Proxy         config.Proxy
	OIDC          config.OIDC
	BasePath      string
	Logger        *slog.Logger
	Now           func() time.Time
	// Client talks to the IdP.
	Client *http.Client
}

func newTest(ctx context.Context, o testOpts) (*Authenticator, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Proxy.UserHeader == "" {
		o.Proxy.UserHeader = config.DefaultUserHeader
	}
	if o.OIDC.Scopes == nil {
		o.OIDC.Scopes = config.DefaultScopes()
	}
	if o.OIDC.UsernameClaim == "" {
		o.OIDC.UsernameClaim = config.DefaultUsernameClaim
	}
	if o.OIDC.GroupsClaim == "" {
		o.OIDC.GroupsClaim = config.DefaultGroupsClaim
	}
	if o.Client != nil {
		ctx = oidc.ClientContext(ctx, o.Client)
	}
	cfg := &config.Config{
		Server: config.Server{BasePath: o.BasePath},
		Auth:   config.Auth{Provider: o.Provider, Session: o.Session, Basic: o.Basic, Proxy: o.Proxy, OIDC: o.OIDC},
	}
	return newAuth(ctx, cfg, o.SessionGroups, o.Logger, o.Now)
}

func mustNew(t *testing.T, o testOpts) *Authenticator {
	t.Helper()
	a, err := newTest(context.Background(), o)
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
