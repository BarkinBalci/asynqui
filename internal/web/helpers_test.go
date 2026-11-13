package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	// block makes Summary of these names wait until ctx ends, like a slow
	// Redis behind conn's cache.
	block     map[string]bool
	recheck   map[string]Health // result of Recheck by name
	rechecked []string

	// queues and history by connection; queueErrs and historyErrs fail
	// those reads.
	queues      map[string][]Queue
	queueErrs   map[string]error
	history     map[string][]DailyStat
	historyErrs map[string]error
	// historyBlock makes History of these names wait until ctx ends.
	historyBlock map[string]bool
	// historyAsked records the queue and days of each History call.
	historyAsked []string
	// actErrs fails actions on a queue; acted records "verb conn/queue".
	actErrs map[string]error
	acted   []string

	// infra are the Servers, Schedulers and Info reads (infra_test.go).
	infra fakeInfra
	// tasks backs the TaskSource methods (tasks_helpers_test.go).
	tasks *fakeTasks
}

func (f *fakeConns) Queues(ctx context.Context, name string) ([]Queue, error) {
	f.mu.Lock()
	block, err, qs := f.block[name], f.queueErrs[name], f.queues[name]
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return qs, err
}

func (f *fakeConns) History(ctx context.Context, name, queue string, days int) ([]DailyStat, error) {
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

func (f *fakeConns) act(verb, name, queue string, apply func(*Queue)) error {
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
	return ErrQueueNotFound
}

func (f *fakeConns) PauseQueue(_ context.Context, name, queue string) error {
	return f.act("pause", name, queue, func(q *Queue) { q.Paused = true })
}

func (f *fakeConns) ResumeQueue(_ context.Context, name, queue string) error {
	return f.act("resume", name, queue, func(q *Queue) { q.Paused = false })
}

func (f *fakeConns) DeleteQueue(_ context.Context, name, queue string) error {
	return f.act("delete", name, queue, nil)
}

func (f *fakeConns) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.acted...)
}

func (f *fakeConns) Connections() []Connection {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Connection(nil), f.conns...)
}

func (f *fakeConns) Summary(ctx context.Context, name string) (Summary, error) {
	f.mu.Lock()
	block, err, s := f.block[name], f.errs[name], f.summaries[name]
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return Summary{}, ctx.Err()
	}
	if err != nil {
		return Summary{}, err
	}
	return s, nil
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
		queues:  map[string][]Queue{"main": defaultQueues()},
		history: map[string][]DailyStat{"main": testHistory(90)},
		actErrs: map[string]error{},
		tasks:   defaultTasks(),
	}
}

// defaultQueues are main's queues, sorted by name as the source returns
// them.
func defaultQueues() []Queue {
	return []Queue{
		{Name: "critical", Size: 58, Pending: 12, Active: 9, Scheduled: 30, Retry: 7, ProcessedToday: 98402, FailedToday: 211, Latency: 400 * time.Millisecond, MemoryUsage: 3 << 20},
		{Name: "default", Size: 1402, Pending: 880, Active: 22, Scheduled: 480, Retry: 12, Archived: 8, ProcessedToday: 231775, FailedToday: 540, Latency: 12600 * time.Millisecond, MemoryUsage: 41 << 20},
		{Name: "image-resize", Paused: true, Size: 111, Archived: 111, Latency: time.Hour, MemoryUsage: 22 << 20},
		{Name: "low", Size: 160, Pending: 8, Scheduled: 105, Retry: 2, Archived: 45, ProcessedToday: 10365, FailedToday: 8, Latency: 48 * time.Second, MemoryUsage: 9 << 20},
	}
}

// testHistory is days of counters ending on fixedNow's day: day i (from
// the oldest) processed 1000+i, failed i%7.
func testHistory(days int) []DailyStat {
	out := make([]DailyStat, days)
	end := fixedNow().Truncate(24 * time.Hour)
	for i := range out {
		out[i] = DailyStat{Date: end.AddDate(0, 0, i-days+1), Processed: 1000 + i, Failed: i % 7}
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
		Auth:    config.Auth{Session: config.Session{CookieSecure: o.cookieSecure}},
		Authz:   o.authz,
	}
	policy := mustPolicy(t, o.authz)
	opts := auth.Options{Provider: auth.Kind(o.provider), BasePath: o.basePath}
	session := auth.SessionOptions{
		KeyPair:  auth.KeyPair{HashKey: []byte(strings.Repeat("h", 32)), BlockKey: []byte(strings.Repeat("b", 32))},
		Insecure: true,
	}
	switch o.provider {
	case config.ProviderProxy:
		opts.Proxy = auth.ProxyOptions{TrustedProxies: []string{proxyPeer}, LogoutURL: o.proxyLogout}
	case config.ProviderBasic:
		hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		opts.Basic = auth.BasicOptions{Users: []auth.BasicUser{{Username: testUser, PasswordHash: string(hash)}}}
		opts.Session = session
	case config.ProviderOIDC:
		if o.idp == nil {
			o.idp = fakeIDP(t)
		}
		opts.Session = session
		opts.OIDC = auth.OIDCOptions{
			Issuer:      o.idp.URL,
			ClientID:    "asynqui",
			RedirectURL: "https://asynqui.example.com" + o.basePath + "/auth/callback",
			HTTPClient:  o.idp.Client(),
		}
	}
	authn, err := auth.New(t.Context(), opts)
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
