package web

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/crypto/bcrypt"

	"github.com/BarkinBalci/asynqui/internal/auth"
	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/conn"
)

// fakeConns is a hand-written ConnectionSource: its connections are
// fakeConn views of the maps below, by name.
type fakeConns struct {
	mu        sync.Mutex
	conns     []connInfo
	summaries map[string]conn.Summary
	errs      map[string]error
	// block makes Summary of these names wait until ctx ends, like a slow
	// Redis behind conn's cache.
	block     map[string]bool
	recheck   map[string]conn.Health // result of Recheck by name
	rechecked []string

	// queues and history by connection; queueErrs and historyErrs fail
	// those reads.
	queues      map[string][]conn.Queue
	queueErrs   map[string]error
	history     map[string][]conn.DailyStat
	historyErrs map[string]error
	// historyBlock makes History of these names wait until ctx ends.
	historyBlock map[string]bool
	// historyAsked records the queue and days of each History call.
	historyAsked []string
	// onQueues runs at each Queues read, outside the lock.
	onQueues func()
	// actErrs fails actions on a queue; acted records "verb conn/queue".
	actErrs map[string]error
	acted   []string

	// infra are the Servers, Schedulers and Info reads (infra_test.go).
	infra fakeInfra
	// tasks backs the TaskSource methods (tasks_helpers_test.go).
	tasks *fakeTasks
}

func (c fakeConn) Queues(ctx context.Context) ([]conn.Queue, error) {
	f, name := c.f, c.name
	f.mu.Lock()
	block, err, qs, hook := f.block[name], f.queueErrs[name], f.queues[name], f.onQueues
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return qs, err
}

func (c fakeConn) History(ctx context.Context, queue string, days int) ([]conn.DailyStat, error) {
	f, name := c.f, c.name
	f.mu.Lock()
	block := f.historyBlock[name]
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.historyAsked = append(f.historyAsked, queue+"/"+strconv.Itoa(days))
	if err := f.historyErrs[name]; err != nil {
		return nil, err
	}
	h := f.history[name]
	return h[max(len(h)-days, 0):], nil
}

func (f *fakeConns) act(verb, name, queue string, apply func(*conn.Queue)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acted = append(f.acted, verb+" "+name+"/"+queue)
	if err := f.actErrs[queue]; err != nil {
		return err
	}
	qs := f.queues[name]
	for i := range qs {
		if qs[i].Name == queue {
			if apply == nil {
				f.queues[name] = append(qs[:i:i], qs[i+1:]...)
				return nil
			}
			apply(&qs[i])
			return nil
		}
	}
	return conn.ErrQueueNotFound
}

func (c fakeConn) PauseQueue(_ context.Context, queue string) error {
	f, name := c.f, c.name
	return f.act("pause", name, queue, func(q *conn.Queue) { q.Paused = true })
}

func (c fakeConn) UnpauseQueue(_ context.Context, queue string) error {
	f, name := c.f, c.name
	return f.act("resume", name, queue, func(q *conn.Queue) { q.Paused = false })
}

func (c fakeConn) DeleteQueue(_ context.Context, queue string) error {
	f, name := c.f, c.name
	return f.act("delete", name, queue, nil)
}

func (f *fakeConns) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.acted...)
}

// connInfo is a fake connection's name, address, health and backend.
type connInfo struct {
	Name   string
	Addr   string
	Health conn.Health
	Valkey bool
}

// fakeConn is one connection of fakeConns.
type fakeConn struct {
	f    *fakeConns
	name string
}

func (f *fakeConns) Connections() []Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Conn, len(f.conns))
	for i, c := range f.conns {
		out[i] = fakeConn{f: f, name: c.Name}
	}
	return out
}

func (c fakeConn) info() connInfo {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	for _, i := range c.f.conns {
		if i.Name == c.name {
			return i
		}
	}
	return connInfo{}
}

func (c fakeConn) Name() string        { return c.name }
func (c fakeConn) Addr() string        { return c.info().Addr }
func (c fakeConn) Health() conn.Health { return c.info().Health }

func (c fakeConn) Backend() (conn.Backend, bool) {
	if c.info().Valkey {
		return conn.BackendValkey, true
	}
	return conn.BackendRedis, false
}

func (c fakeConn) Summary(ctx context.Context) (conn.Summary, error) {
	f, name := c.f, c.name
	f.mu.Lock()
	block, err, s := f.block[name], f.errs[name], f.summaries[name]
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return conn.Summary{}, ctx.Err()
	}
	if err != nil {
		return conn.Summary{}, err
	}
	return s, nil
}

func (c fakeConn) Recheck(_ context.Context) (conn.Health, error) {
	f, name := c.f, c.name
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rechecked = append(f.rechecked, name)
	h := f.recheck[name]
	for i := range f.conns {
		if f.conns[i].Name == name {
			f.conns[i].Health = h
		}
	}
	return h, nil
}

// unreachable is a network error: conn.Unreachable reports it.
func unreachable(msg string) error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New(msg)}
}

// defaultConns: main is up, analytics down, local still unknown.
func defaultConns() *fakeConns {
	return &fakeConns{
		conns: []connInfo{
			{Name: "main", Addr: "redis-main.internal:6379/0", Health: conn.Health{State: conn.StateUp}},
			{Name: "analytics", Addr: "redis-analytics.internal:6380/0", Health: conn.Health{State: conn.StateDown, Err: errors.New("dial tcp 10.0.4.12:6380: i/o timeout")}},
			{Name: "local", Addr: "localhost:6379/1"},
		},
		summaries: map[string]conn.Summary{
			"main": {Queues: 7, Pending: 1284, Active: 46, Retry: 37, ProcessedToday: 482190, FailedToday: 1204, OldestPending: 4*time.Minute + 12*time.Second},
		},
		queues:  map[string][]conn.Queue{"main": defaultQueues()},
		history: map[string][]conn.DailyStat{"main": testHistory(90)},
		actErrs: map[string]error{},
		tasks:   defaultTasks(),
	}
}

// defaultQueues are main's queues, sorted by name as the source returns
// them.
func defaultQueues() []conn.Queue {
	return []conn.Queue{
		{Name: "critical", Size: 58, Pending: 12, Active: 9, Scheduled: 30, Retry: 7, ProcessedToday: 98402, FailedToday: 211, Latency: 400 * time.Millisecond, MemoryUsage: 3 << 20},
		{Name: "default", Size: 1402, Pending: 880, Active: 22, Scheduled: 480, Retry: 12, Archived: 8, ProcessedToday: 231775, FailedToday: 540, Latency: 12600 * time.Millisecond, MemoryUsage: 41 << 20},
		{Name: "image-resize", Paused: true, Size: 111, Archived: 111, Latency: time.Hour, MemoryUsage: 22 << 20},
		{Name: "low", Size: 160, Pending: 8, Scheduled: 105, Retry: 2, Archived: 45, ProcessedToday: 10365, FailedToday: 8, Latency: 48 * time.Second, MemoryUsage: 9 << 20},
	}
}

// testHistory is days of counters ending on fixedNow's day: day i (from
// the oldest) processed 1000+i, failed i%7.
func testHistory(days int) []conn.DailyStat {
	out := make([]conn.DailyStat, days)
	end := fixedNow().Truncate(24 * time.Hour)
	for i := range out {
		out[i] = conn.DailyStat{Date: end.AddDate(0, 0, i-days+1), Processed: 1000 + i, Failed: i % 7}
	}
	return out
}

// testOpts configures newTestServer. The zero value is auth none, default
// viewer, no base path.
type testOpts struct {
	provider config.Provider
	basePath string
	authz    config.Authz
	conns    *fakeConns
	// proxyLogout sets auth.proxy.logout_url.
	proxyLogout  string
	ready        *Readiness
	cookieSecure bool
	budget       time.Duration
	// idp is the fake OIDC provider for provider oidc.
	idp *httptest.Server
	// updates is the update check; nil means none.
	updates UpdateSource
}

const (
	proxyPeer     = "192.0.2.10"
	testUser      = "alice"
	testPassword  = "correct horse"
	fixedNowEpoch = 1794098512 // 2026-11-08T00:01:52Z
)

func fixedNow() time.Time { return time.Unix(fixedNowEpoch, 0).UTC() }

func newTestServer(t *testing.T, o testOpts) (http.Handler, *fakeConns) {
	t.Helper()
	_, h, c := newTestDeps(t, o)
	return h, c
}

func newTestDeps(t *testing.T, o testOpts) (*deps, http.Handler, *fakeConns) {
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
		Auth:    config.Auth{Provider: o.provider, Session: config.Session{CookieSecure: o.cookieSecure}},
		Authz:   o.authz,
	}
	policy := mustPolicy(t, o.authz)
	session := config.Session{
		HashKey:      config.Secret(hex.EncodeToString([]byte(strings.Repeat("h", 32)))),
		BlockKey:     config.Secret(hex.EncodeToString([]byte(strings.Repeat("b", 32)))),
		TTL:          config.DefaultSessionTTL,
		CookieSecure: o.cookieSecure,
	}
	ctx := t.Context()
	switch o.provider {
	case config.ProviderProxy:
		cfg.Auth.Proxy = config.Proxy{UserHeader: config.DefaultUserHeader, TrustedProxies: []string{proxyPeer}, LogoutURL: o.proxyLogout}
	case config.ProviderBasic:
		hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Auth.Basic = config.Basic{Users: []config.BasicUser{{Username: testUser, PasswordHash: config.Secret(hash)}}}
		cfg.Auth.Session = session
	case config.ProviderOIDC:
		if o.idp == nil {
			o.idp = fakeIDP(t)
		}
		cfg.Auth.Session = session
		cfg.Auth.OIDC = config.OIDC{
			Issuer:        o.idp.URL,
			ClientID:      "asynqui",
			RedirectURL:   "https://asynqui.example.com" + o.basePath + "/auth/callback",
			Scopes:        config.DefaultScopes(),
			UsernameClaim: config.DefaultUsernameClaim,
			GroupsClaim:   config.DefaultGroupsClaim,
		}
		ctx = oidc.ClientContext(ctx, o.idp.Client())
	}
	authn, err := auth.New(ctx, cfg, policy.Groups(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d, h, err := newServer(Deps{
		Logger:        slog.New(slog.DiscardHandler),
		Config:        cfg,
		Auth:          authn,
		Policy:        policy,
		Conns:         o.conns,
		Ready:         o.ready,
		Version:       "0.1.0",
		Updates:       o.updates,
		Now:           fixedNow,
		SummaryBudget: o.budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d, h, o.conns
}

// fakeIDP serves OIDC discovery only: enough for auth.New and for starting
// the flow. The callback is covered by package auth.
func fakeIDP(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/authorize",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mustPolicy(t *testing.T, a config.Authz) *authz.Policy {
	t.Helper()
	p, err := authz.New(a)
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
	header  map[string]string
}

func do(t *testing.T, h http.Handler, rq request) *httptest.ResponseRecorder {
	t.Helper()
	if rq.method == "" {
		rq.method = http.MethodGet
	}
	r := httptest.NewRequest(rq.method, rq.path, strings.NewReader(rq.form))
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
	for k, v := range rq.header {
		r.Header.Set(k, v)
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

func text(s *goquery.Selection) string { return strings.Join(strings.Fields(s.Text()), " ") }
