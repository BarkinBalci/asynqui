package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/crypto/bcrypt"

	"github.com/BarkinBalci/asynqui/internal/auth"
	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/config"
)

// fakeConns is a hand-written ConnectionSource.
type fakeConns struct {
	mu        sync.Mutex
	conns     []Connection
	summaries map[string]Summary
	errs      map[string]error
	recheck   map[string]Health // result of Recheck by name
	rechecked []string
}

func (f *fakeConns) Connections() []Connection {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Connection(nil), f.conns...)
}

func (f *fakeConns) Summary(_ context.Context, name string) (Summary, error) {
	if err := f.errs[name]; err != nil {
		return Summary{}, err
	}
	return f.summaries[name], nil
}

func (f *fakeConns) Recheck(_ context.Context, name string) Health {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rechecked = append(f.rechecked, name)
	h := f.recheck[name]
	for i := range f.conns {
		if f.conns[i].Name == name {
			f.conns[i].Health = h
		}
	}
	return h
}

// defaultConns: main is up, analytics down, local still unknown.
func defaultConns() *fakeConns {
	return &fakeConns{
		conns: []Connection{
			{Name: "main", Addr: "redis-main.internal:6379/0", Health: Health{State: HealthUp}},
			{Name: "analytics", Addr: "redis-analytics.internal:6380/0", Health: Health{State: HealthDown, Err: errors.New("dial tcp 10.0.4.12:6380: i/o timeout")}},
			{Name: "local", Addr: "localhost:6379/1"},
		},
		summaries: map[string]Summary{
			"main": {Queues: 7, Pending: 1284, Active: 46, Retry: 37, ProcessedToday: 482190, FailedToday: 1204, OldestPending: 4*time.Minute + 12*time.Second},
		},
	}
}

// testOpts configures newTestServer. The zero value is auth none, default
// viewer, no base path.
type testOpts struct {
	provider config.Provider
	basePath string
	authz    config.Authz
	conns    *fakeConns
	// proxyLogout sets auth.proxy.logout_url.
	proxyLogout string
	ready       *Readiness
}

const (
	proxyPeer     = "192.0.2.10"
	testUser      = "alice"
	testPassword  = "correct horse"
	testHashKey   = "0000000000000000000000000000000000000000000000000000000000000001"
	testBlockKey  = "0000000000000000000000000000000000000000000000000000000000000002"
	fixedNowEpoch = 1794098512 // 2026-11-08T00:01:52Z
)

func fixedNow() time.Time { return time.Unix(fixedNowEpoch, 0).UTC() }

func newTestServer(t *testing.T, o testOpts) (http.Handler, *fakeConns) {
	t.Helper()
	if o.provider == "" {
		o.provider = config.ProviderNone
	}
	if o.authz.Default == "" {
		o.authz.Default = config.RoleViewer
	}
	if o.conns == nil {
		o.conns = defaultConns()
	}
	if o.ready == nil {
		o.ready = &Readiness{}
	}
	cfg := &config.Config{
		Server:  config.Server{BasePath: o.basePath},
		Refresh: config.Refresh{Default: "5s"},
		Authz:   o.authz,
	}
	policy := mustPolicy(t, o.authz)
	opts := auth.Options{Provider: auth.Kind(o.provider), BasePath: o.basePath}
	switch o.provider {
	case config.ProviderProxy:
		opts.Proxy = auth.ProxyOptions{TrustedProxies: []string{proxyPeer}, LogoutURL: o.proxyLogout}
	case config.ProviderBasic:
		hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		opts.Basic = auth.BasicOptions{Users: []auth.BasicUser{{Username: testUser, PasswordHash: string(hash)}}}
		opts.Session = auth.SessionOptions{
			KeyPair:  auth.KeyPair{HashKey: []byte(testHashKey[:32]), BlockKey: []byte(testBlockKey[:32])},
			Insecure: true,
		}
	}
	authn, err := auth.New(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewServer(Deps{
		Logger:  slog.New(slog.DiscardHandler),
		Config:  cfg,
		Auth:    authn,
		Policy:  policy,
		Conns:   o.conns,
		Ready:   o.ready,
		Version: "0.1.0",
		Now:     fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, o.conns
}

func mustPolicy(t *testing.T, a config.Authz) *authz.Policy {
	t.Helper()
	def, err := authz.ParseRole(string(a.Default))
	if err != nil {
		t.Fatal(err)
	}
	var bs []authz.Binding
	for _, b := range a.Bindings {
		r, err := authz.ParseRole(string(b.Role))
		if err != nil {
			t.Fatal(err)
		}
		bs = append(bs, authz.Binding{Users: b.Users, Groups: b.Groups, Role: r, Connections: b.Connections})
	}
	p, err := authz.New(def, bs)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// request describes one test request.
type request struct {
	method  string
	path    string
	user    string // proxy identity header, sent from the trusted peer
	htmx    bool
	cookies []*http.Cookie
	form    string
}

func do(t *testing.T, h http.Handler, rq request) *httptest.ResponseRecorder {
	t.Helper()
	if rq.method == "" {
		rq.method = http.MethodGet
	}
	var body *strings.Reader
	if rq.form != "" {
		body = strings.NewReader(rq.form)
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(rq.method, rq.path, body)
	if rq.form != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if rq.user != "" {
		r.RemoteAddr = proxyPeer + ":40000"
		r.Header.Set("X-Forwarded-User", rq.user)
	}
	if rq.htmx {
		r.Header.Set("HX-Request", "true")
		r.Header.Set("HX-Current-URL", "http://example.com/")
	}
	for _, c := range rq.cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func parse(t *testing.T, rec *httptest.ResponseRecorder) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(rec.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}
