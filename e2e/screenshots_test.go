//go:build screenshots

package e2e

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	playwright "github.com/mxschmitt/playwright-go"
	"github.com/redis/go-redis/v9"
)

// imagesDir is where TestScreenshots writes, relative to e2e/.
const imagesDir = "../docs/images"

// The demo connections: one logical DB each on the same Redis, plus one
// on a port nothing listens on.
const (
	demoPayments      = "payments"
	demoNotifications = "notifications"
	demoReports       = "reports"
	demoDown          = "analytics"
)

// Screenshot geometry.
const (
	shotWidth  = 1440
	shotHeight = 900
	shotScale  = 2
)

// TestScreenshots regenerates the README images in docs/images: four
// pages in light and dark, and the demo GIF. It seeds a throwaway Redis
// (ASYNQUI_E2E_REDIS, DBs 0 to 2 are flushed, or testcontainers) with
// demo data through asynq's client, inspector, workers and schedulers.
//
//	go test -tags screenshots -run TestScreenshots ./e2e/
func TestScreenshots(t *testing.T) {
	addr := startRedis(t)
	demo := seedDemo(t, addr)

	bin := buildApp(t)
	cfg := writeConfig(t, t.TempDir(), "demo.yaml", demoConfig(addr, deadAddr(t)))
	app := startApp(t, bin, cfg)

	pw, err := playwright.Run()
	if err != nil {
		t.Fatalf("start playwright (install Chromium first, see docs/contributing.md): %v", err)
	}
	t.Cleanup(func() { _ = pw.Stop() })
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: new(true)})
	if err != nil {
		t.Fatalf("launch chromium: %v", err)
	}
	t.Cleanup(func() { _ = browser.Close() })

	if err := os.MkdirAll(imagesDir, 0o750); err != nil {
		t.Fatal(err)
	}
	shots := []shot{
		{name: "overview", path: "/"},
		{name: "connection", path: "/c/" + demoPayments, height: 980},
		{name: "queue", path: "/c/" + demoPayments + "/queues/" + demo.queue + "?state=retry", prepare: selectRows(3)},
		{name: "task", path: demo.taskPath},
	}
	for _, theme := range []string{"light", "dark"} {
		for _, s := range shots {
			capture(t, browser, app.url, theme, s, filepath.Join(imagesDir, s.name+"-"+theme+".png"))
		}
	}
	recordDemo(t, browser, app.url, demo, filepath.Join(imagesDir, "demo.gif"))
}

// demoConfig is auth none with admin everywhere and the default refresh.
func demoConfig(redisAddr, downAddr string) string {
	var b strings.Builder
	b.WriteString(`server:
  shutdown_delay: 0s
update_check: false
auth:
  provider: none
authz:
  default: admin
connections:
`)
	for _, c := range demoConns() {
		fmt.Fprintf(&b, "  - name: %s\n    addr: %q\n    db: %d\n", c.name, redisAddr, c.db)
	}
	fmt.Fprintf(&b, "  - name: %s\n    addr: %q\n    dial_timeout: 1s\n", demoDown, downAddr)
	return b.String()
}

// deadAddr is a loopback address with nothing listening on it.
func deadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// ---- capture ----

// newShotPage opens a tab with the theme cookie set, as the theme control
// sets it.
func newShotPage(t *testing.T, browser playwright.Browser, baseURL, theme string, w, h int, scale float64) (playwright.BrowserContext, playwright.Page) {
	t.Helper()
	bctx, err := browser.NewContext(playwright.BrowserNewContextOptions{
		BaseURL:           new(baseURL),
		Locale:            new("en-US"),
		TimezoneId:        new("UTC"),
		Viewport:          &playwright.Size{Width: w, Height: h},
		DeviceScaleFactor: new(scale),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bctx.Close() })
	if err := bctx.AddCookies([]playwright.OptionalCookie{{Name: "asynqui_theme", Value: theme, URL: new(baseURL)}}); err != nil {
		t.Fatal(err)
	}
	pg, err := bctx.NewPage()
	if err != nil {
		t.Fatal(err)
	}
	pg.SetDefaultTimeout(15000)
	return bctx, pg
}

// shot is one page to capture: height overrides the viewport height,
// and prepare sets the page up after it has settled.
type shot struct {
	name, path string
	height     int
	prepare    func(*testing.T, playwright.Page)
}

// capture opens the page, waits until every region has data and one
// round of polls has landed, stops the page clock so relative times and
// polls hold still, and saves a screenshot.
func capture(t *testing.T, browser playwright.Browser, baseURL, theme string, s shot, out string) {
	t.Helper()
	bctx, pg := newShotPage(t, browser, baseURL, theme, shotWidth, cmp.Or(s.height, shotHeight), shotScale)
	if err := pg.Clock().Install(); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Goto(s.path); err != nil {
		t.Fatalf("open %s: %v", s.path, err)
	}
	settle(t, pg)
	if s.prepare != nil {
		s.prepare(t, pg)
	}
	now, err := pg.Evaluate(`() => Date.now()`)
	if err != nil {
		t.Fatal(err)
	}
	var ms int64
	switch v := now.(type) {
	case int:
		ms = int64(v)
	case float64:
		ms = int64(v)
	}
	if err := pg.Clock().PauseAt(ms + 200); err != nil {
		t.Fatalf("pause clock: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := pg.Screenshot(playwright.PageScreenshotOptions{
		Path:       new(out),
		Animations: playwright.ScreenshotAnimationsDisabled,
		Caret:      playwright.ScreenshotCaretHide,
	}); err != nil {
		t.Fatalf("screenshot %s: %v", out, err)
	}
	_ = bctx.Close()
	t.Logf("wrote %s", out)
}

// settle waits until no region is still checking, then fires one poll
// on every polled region and waits for the answers.
func settle(t *testing.T, pg playwright.Page) {
	t.Helper()
	if _, err := pg.WaitForFunction(`() => window.htmx && document.fonts.status === "loaded" &&
		!document.querySelector(".checking, .conn-checking, .dot-unknown")`, nil,
		playwright.PageWaitForFunctionOptions{Timeout: new(30000.0)}); err != nil {
		t.Fatalf("waiting for data on %s: %v", pg.URL(), err)
	}
	if _, err := pg.Evaluate(`() => new Promise((resolve, reject) => {
		// A region that replaces itself out of band reports through the
		// swap, since its request events fire on the detached element.
		const regions = Array.from(document.querySelectorAll("[data-poll]"));
		const left = new Set(regions);
		if (!left.size) { resolve(); return; }
		const timer = setTimeout(() => reject(new Error("no poll answer")), 10000);
		const done = (r) => {
			if (left.delete(r) && !left.size) {
				clearTimeout(timer);
				setTimeout(resolve, 250);
			}
		};
		document.addEventListener("htmx:afterRequest", (e) => done(e.detail.elt));
		document.addEventListener("htmx:oobAfterSwap", (e) => {
			const id = e.detail.target && e.detail.target.id;
			regions.forEach((r) => { if (id && r.id === id) { done(r); } });
		});
		regions.forEach((r) => htmx.trigger(r, "asynqui-refresh"));
	})`); err != nil {
		t.Fatalf("poll %s: %v", pg.URL(), err)
	}
	if _, err := pg.WaitForFunction(`() => !document.querySelector(".checking, .conn-checking, .dot-unknown")`, nil); err != nil {
		t.Fatalf("waiting for data after the poll on %s: %v", pg.URL(), err)
	}
}

// selectRows ticks the first n task rows, so the toolbar shows its
// actions, and moves the mouse off the table.
func selectRows(n int) func(*testing.T, playwright.Page) {
	return func(t *testing.T, pg playwright.Page) {
		t.Helper()
		boxes := pg.Locator("#tasks-rows input.cb")
		for i := range n {
			if err := boxes.Nth(i).Check(); err != nil {
				t.Fatalf("select row %d: %v", i, err)
			}
		}
		if err := pg.Mouse().Move(shotWidth-8, 8); err != nil {
			t.Fatal(err)
		}
		if err := pg.Locator(".selc").WaitFor(playwright.LocatorWaitForOptions{State: playwright.WaitForSelectorStateVisible}); err != nil {
			t.Fatalf("selection count: %v", err)
		}
	}
}

// ---- demo data ----

// demoConn is one connection's logical DB and what runs on it: the
// worker's concurrency and queue priorities, and each queue's usual
// tasks per day for the history.
type demoConn struct {
	name        string
	db          int
	concurrency int
	queues      map[string]int
	daily       map[string]int
}

// demoConns are the reachable demo connections, in sidebar order.
func demoConns() []demoConn {
	return []demoConn{
		{demoPayments, 0, 10,
			map[string]int{"critical": 6, "invoices": 3, "webhooks": 2, "refunds": 1, "ledger": 1},
			map[string]int{"critical": 640, "invoices": 420, "webhooks": 560, "refunds": 45, "ledger": 96}},
		{demoNotifications, 1, 8,
			map[string]int{"email": 4, "sms": 4, "push": 2, "digest": 1},
			map[string]int{"email": 1100, "sms": 420, "push": 1500}},
		{demoReports, 2, 4,
			map[string]int{"reports": 3, "exports": 2, "low": 1},
			map[string]int{"reports": 70, "exports": 140, "low": 288}},
	}
}

// demo names what the seeder made for the captures.
type demo struct {
	queue    string // the payments queue whose Retry tab is shown
	taskPath string // the Task page of a retried task
}

// outcome is what the demo worker does with one task.
type outcome struct {
	block bool          // hold a worker until shutdown
	err   string        // fail with this error
	tries int           // fail fast until the task has been retried this often
	next  time.Duration // then the delay before the next retry
}

// demoError carries the delay the retry delay function returns.
type demoError struct {
	msg   string
	delay time.Duration
}

func (e *demoError) Error() string { return e.msg }

// worker runs the demo handler for one connection.
type worker struct {
	mu   sync.Mutex
	plan map[string]outcome
}

func (w *worker) set(id string, o outcome) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.plan[id] = o
}

func (w *worker) handle(ctx context.Context, _ *asynq.Task) error {
	id, _ := asynq.GetTaskID(ctx)
	w.mu.Lock()
	o := w.plan[id]
	w.mu.Unlock()
	switch {
	case o.block:
		<-ctx.Done()
		return ctx.Err()
	case o.err != "":
		n, _ := asynq.GetRetryCount(ctx)
		delay := 100 * time.Millisecond
		if n+1 >= o.tries {
			delay = o.next
		}
		return &demoError{msg: o.err, delay: delay}
	}
	return nil
}

func retryDelay(_ int, err error, _ *asynq.Task) time.Duration {
	if de, ok := errors.AsType[*demoError](err); ok {
		return de.delay
	}
	return time.Hour
}

// seeder enqueues into one connection's DB with stable IDs and payloads.
type seeder struct {
	t    *testing.T
	name string
	c    *asynq.Client
	insp *asynq.Inspector
	w    *worker
	rng  *rand.Rand
	// want counts the retry and archived tasks planned per queue.
	retry, archived map[string]int
}

func (s *seeder) id() string {
	b := make([]byte, 16)
	for i := range b {
		b[i] = byte(s.rng.Uint32())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// add enqueues one task with outcome o and returns its ID.
func (s *seeder) add(queue, typ, payload string, o outcome, opts ...asynq.Option) string {
	s.t.Helper()
	id := s.id()
	s.w.set(id, o)
	opts = append([]asynq.Option{asynq.Queue(queue), asynq.TaskID(id)}, opts...)
	if _, err := s.c.Enqueue(asynq.NewTask(typ, []byte(payload)), opts...); err != nil {
		s.t.Fatalf("%s: enqueue %s on %s: %v", s.name, typ, queue, err)
	}
	return id
}

// ok enqueues n tasks that succeed.
func (s *seeder) ok(queue, typ string, n int, payload func() string) {
	for range n {
		s.add(queue, typ, payload(), outcome{})
	}
}

// fail enqueues a task that ends in the retry set after tries attempts,
// next before its next retry.
func (s *seeder) fail(queue, typ, payload, msg string, tries int, next time.Duration) string {
	s.retry[queue]++
	return s.add(queue, typ, payload, outcome{err: msg, tries: tries, next: next})
}

// dead enqueues a task that is archived after tries retries.
func (s *seeder) dead(queue, typ, payload, msg string, tries int) {
	s.archived[queue]++
	s.add(queue, typ, payload, outcome{err: msg, next: 100 * time.Millisecond}, asynq.MaxRetry(tries))
}

// busy enqueues a task that holds a worker.
func (s *seeder) busy(queue, typ, payload string) {
	s.add(queue, typ, payload, outcome{block: true})
}

// later enqueues a scheduled task.
func (s *seeder) later(queue, typ, payload string, in time.Duration) {
	s.add(queue, typ, payload, outcome{}, asynq.ProcessIn(in))
}

// settled waits until the worker has drained every queue and each
// planned failure has reached its last retry or the archive.
func (s *seeder) settled() {
	s.t.Helper()
	waitFor(s.t, 60*time.Second, s.name+" to process the seeded tasks", func() bool {
		qs, err := s.insp.Queues()
		if err != nil {
			return false
		}
		for _, q := range qs {
			info, err := s.insp.GetQueueInfo(q)
			if err != nil {
				return false
			}
			if (!info.Paused && info.Pending > 0) || info.Active > 0 ||
				info.Retry != s.retry[q] || info.Archived != s.archived[q] {
				return false
			}
			if info.Retry > 0 {
				ts, err := s.insp.ListRetryTasks(q, asynq.PageSize(100))
				if err != nil {
					return false
				}
				for _, ti := range ts {
					if time.Until(ti.NextProcessAt) < 20*time.Second {
						return false
					}
				}
			}
		}
		return true
	})
}

// active waits until n tasks are being processed on the connection.
func (s *seeder) active(n int) {
	s.t.Helper()
	waitFor(s.t, 30*time.Second, s.name+" workers to pick up the long tasks", func() bool {
		qs, err := s.insp.Queues()
		if err != nil {
			return false
		}
		got := 0
		for _, q := range qs {
			info, err := s.insp.GetQueueInfo(q)
			if err != nil {
				return false
			}
			got += info.Active
		}
		return got == n
	})
}

// seedDemo flushes the demo DBs and fills them: a worker and a
// scheduler per connection, tasks processed today, retries and archived
// tasks with errors, busy workers with a backlog, scheduled tasks, a
// paused queue and 14 days of throughput history.
func seedDemo(t *testing.T, addr string) demo {
	t.Helper()
	rng := rand.New(rand.NewPCG(7, 42))
	seeders := map[string]*seeder{}
	for _, dc := range demoConns() {
		opt := asynq.RedisClientOpt{Addr: addr, DB: dc.db}
		rdb := redis.NewClient(&redis.Options{Addr: addr, DB: dc.db})
		t.Cleanup(func() { _ = rdb.Close() })
		if err := rdb.FlushDB(t.Context()).Err(); err != nil {
			t.Fatalf("flush redis %s db %d: %v", addr, dc.db, err)
		}
		c := asynq.NewClient(opt)
		t.Cleanup(func() { _ = c.Close() })
		insp := asynq.NewInspector(opt)
		t.Cleanup(func() { _ = insp.Close() })
		s := &seeder{
			t: t, name: dc.name, c: c, insp: insp, rng: rng,
			w:     &worker{plan: map[string]outcome{}},
			retry: map[string]int{}, archived: map[string]int{},
		}
		seeders[dc.name] = s
		seedDemoHistory(t, rdb, rng, dc.daily)
	}
	p := &payloads{rng: rng, now: time.Now().UTC()}

	// The paused queue fills before any worker runs.
	n := seeders[demoNotifications]
	for range 214 {
		n.add("digest", "digest:weekly", p.digest(), outcome{})
	}
	if err := n.insp.PauseQueue("digest"); err != nil {
		t.Fatal(err)
	}

	for _, dc := range demoConns() {
		s := seeders[dc.name]
		srv := asynq.NewServer(asynq.RedisClientOpt{Addr: addr, DB: dc.db}, asynq.Config{
			Concurrency:              dc.concurrency,
			Queues:                   dc.queues,
			RetryDelayFunc:           retryDelay,
			DelayedTaskCheckInterval: 200 * time.Millisecond,
			ShutdownTimeout:          time.Second,
			LogLevel:                 asynq.FatalLevel,
		})
		if err := srv.Start(asynq.HandlerFunc(s.w.handle)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(srv.Shutdown)
	}

	// Today's traffic, with the failures that stay in Retry and Archived.
	s := seeders[demoPayments]
	s.ok("critical", "payment:capture", 388, p.capture)
	s.ok("critical", "payment:authorize", 142, p.capture)
	s.ok("invoices", "invoice:generate", 176, p.invoice)
	s.ok("invoices", "invoice:send", 171, p.invoiceSend)
	s.ok("webhooks", "webhook:deliver", 463, p.webhook)
	s.ok("refunds", "refund:process", 31, p.refund)
	s.ok("ledger", "ledger:reconcile", 24, p.ledger)
	hooks := []struct {
		err   string
		tries int
		next  time.Duration
	}{
		{"POST https://shop.example.com/hooks/payments: 503 Service Unavailable", 4, 7*time.Minute + 12*time.Second},
		{"POST https://erp.example.net/api/webhooks: context deadline exceeded (Client.Timeout exceeded while awaiting headers)", 2, 3*time.Minute + 40*time.Second},
		{"dial tcp 203.0.113.24:443: connect: connection refused", 6, 41 * time.Minute},
		{"POST https://crm.example.org/inbound/payments: 429 Too Many Requests", 1, 58 * time.Second},
		{"POST https://shop.example.com/hooks/payments: 503 Service Unavailable", 3, 4*time.Minute + 5*time.Second},
		{"tls: failed to verify certificate: x509: certificate has expired or is not yet valid", 8, 2*time.Hour + 16*time.Minute},
		{"POST https://erp.example.net/api/webhooks: 502 Bad Gateway", 2, 2*time.Minute + 31*time.Second},
		{"dial tcp 203.0.113.24:443: connect: connection refused", 5, 26*time.Minute + 30*time.Second},
		{"POST https://crm.example.org/inbound/payments: 429 Too Many Requests", 1, 1*time.Minute + 44*time.Second},
	}
	for _, h := range hooks {
		s.fail("webhooks", "webhook:deliver", p.webhook(), h.err, h.tries, h.next)
	}
	taskID := s.fail("invoices", "invoice:generate", p.invoice(), "render invoice pdf: chromium: navigation timeout of 30000 ms exceeded", 3, 11*time.Minute+20*time.Second)
	s.fail("invoices", "invoice:send", p.invoiceSend(), "smtp: 421 4.7.0 Try again later, closing connection", 2, 6*time.Minute)
	s.fail("critical", "payment:capture", p.capture(), "psp: 503 upstream unavailable (request_id req_8f2k1LqZ)", 1, 45*time.Second)
	s.dead("refunds", "refund:process", p.refund(), "psp: refund rejected: charge has already been refunded", 3)
	s.dead("refunds", "refund:process", p.refund(), "psp: refund rejected: amount exceeds the captured amount", 3)
	s.dead("webhooks", "webhook:deliver", p.webhook(), "POST https://legacy.example.com/hooks: 410 Gone", 5)
	s.dead("invoices", "invoice:generate", p.invoice(), `template "invoice_de_v2" not found`, 2)

	s = seeders[demoNotifications]
	s.ok("email", "email:welcome", 268, p.email)
	s.ok("email", "email:receipt", 512, p.email)
	s.ok("email", "email:password_reset", 87, p.email)
	s.ok("sms", "sms:otp", 341, p.sms)
	s.ok("push", "push:send", 1205, p.push)
	s.fail("email", "email:receipt", p.email(), "smtp: 421 4.7.0 Try again later, closing connection", 2, 9*time.Minute)
	s.fail("email", "email:welcome", p.email(), "smtp: 452 4.2.2 mailbox full", 4, 52*time.Minute)
	s.fail("email", "email:receipt", p.email(), "smtp: 421 4.7.0 Try again later, closing connection", 1, 3*time.Minute)
	s.fail("sms", "sms:otp", p.sms(), "sms gateway: 429 Too Many Requests", 1, 30*time.Second+time.Minute)
	for range 6 {
		s.dead("push", "push:send", p.push(), "push: device token is no longer registered", 0)
	}
	s.dead("email", "email:welcome", p.email(), "smtp: 550 5.1.1 recipient address rejected: user unknown", 2)
	s.dead("email", "email:receipt", p.email(), "smtp: 550 5.1.1 recipient address rejected: user unknown", 2)

	s = seeders[demoReports]
	s.ok("reports", "report:daily", 64, p.report)
	s.ok("exports", "export:csv", 118, p.export)
	s.ok("low", "analytics:rollup", 288, p.rollup)
	s.fail("exports", "export:csv", p.export(), "s3: PutObject: RequestTimeout: your socket connection to the server was not read from or written to within the timeout period", 2, 14*time.Minute)
	s.fail("reports", "report:daily", p.report(), "pq: canceling statement due to statement timeout", 1, 18*time.Minute)
	s.dead("reports", "report:daily", p.report(), `template "churn_cohorts" not found`, 1)

	for _, dc := range demoConns() {
		seeders[dc.name].settled()
	}

	// Long tasks hold every payments and reports worker and a few
	// notifications ones; what comes after them waits as a backlog.
	s = seeders[demoPayments]
	for range 4 {
		s.busy("invoices", "invoice:generate", p.invoice())
	}
	for range 3 {
		s.busy("webhooks", "webhook:deliver", p.webhook())
	}
	s.busy("critical", "payment:capture", p.capture())
	s.busy("critical", "payment:authorize", p.capture())
	s.busy("ledger", "ledger:reconcile", p.ledger())
	s.active(10)
	s = seeders[demoReports]
	s.busy("reports", "report:monthly", p.report())
	s.busy("exports", "export:csv", p.export())
	s.busy("exports", "export:csv", p.export())
	s.busy("low", "analytics:rollup", p.rollup())
	s.active(4)
	s = seeders[demoNotifications]
	s.busy("email", "email:receipt", p.email())
	s.busy("push", "push:send", p.push())
	s.busy("push", "push:send", p.push())
	s.active(3)

	s = seeders[demoPayments]
	for range 19 {
		s.add("invoices", "invoice:generate", p.invoice(), outcome{})
		s.add("invoices", "invoice:send", p.invoiceSend(), outcome{})
	}
	for range 64 {
		s.add("webhooks", "webhook:deliver", p.webhook(), outcome{})
	}
	for range 5 {
		s.add("critical", "payment:capture", p.capture(), outcome{})
	}
	for i := range 12 {
		s.later("invoices", "invoice:send", p.invoiceSend(), time.Duration(20+i*37)*time.Minute)
	}
	for i := range 6 {
		s.later("refunds", "refund:process", p.refund(), time.Duration(3+i*5)*time.Hour)
	}
	for i := range 4 {
		s.later("critical", "payment:capture", p.capture(), time.Duration(35+i*12)*time.Minute)
	}
	s = seeders[demoReports]
	for range 23 {
		s.add("exports", "export:csv", p.export(), outcome{})
	}
	for range 41 {
		s.add("low", "analytics:rollup", p.rollup(), outcome{})
	}
	for i := range 3 {
		s.later("reports", "report:daily", p.report(), time.Duration(5+i*8)*time.Hour)
	}
	s = seeders[demoNotifications]
	for i := range 18 {
		s.later("email", "email:trial_ending", p.email(), time.Duration(1+i*4)*time.Hour)
	}

	// Spec, type, queue and payload of each scheduler entry.
	schedule := map[string][][4]string{
		demoPayments:      {{"@every 15m", "ledger:reconcile", "ledger", `{"accounts":"all"}`}},
		demoNotifications: {{"0 8 * * 1", "digest:weekly", "digest", `{"segment":"weekly_active"}`}},
		demoReports:       {{"0 6 * * *", "report:daily", "reports", `{"report":"revenue_by_region","format":"pdf"}`}},
	}
	for _, dc := range demoConns() {
		sch := asynq.NewScheduler(asynq.RedisClientOpt{Addr: addr, DB: dc.db}, &asynq.SchedulerOpts{LogLevel: asynq.FatalLevel})
		for _, e := range schedule[dc.name] {
			if _, err := sch.Register(e[0], asynq.NewTask(e[1], []byte(e[3])), asynq.Queue(e[2])); err != nil {
				t.Fatal(err)
			}
		}
		if err := sch.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sch.Shutdown)
	}
	return demo{
		queue:    "webhooks",
		taskPath: "/c/" + demoPayments + "/queues/invoices/tasks/" + taskID,
	}
}

// seedDemoHistory writes demo data only: asynq's daily processed and
// failed counters for the 29 days before today, so the Connection
// page's throughput chart has a believable shape. The keys follow
// asynq v0.26.0 internal/base (ProcessedKey and FailedKey,
// "asynq:{<queue>}:processed:<UTC date>"), with asynq's 90-day expiry.
// Today's counters come from the demo workers.
func seedDemoHistory(t *testing.T, rdb *redis.Client, rng *rand.Rand, daily map[string]int) {
	t.Helper()
	today := time.Now().UTC()
	pipe := rdb.Pipeline()
	for _, q := range slices.Sorted(maps.Keys(daily)) {
		base := float64(daily[q])
		for back := 1; back < 30; back++ {
			day := today.AddDate(0, 0, -back)
			f := 0.85 + 0.3*rng.Float64()
			if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
				f *= 0.55
			}
			processed := int(base * f * (1 + 0.012*float64(30-back)))
			failed := int(float64(processed) * (0.004 + 0.012*rng.Float64()))
			if back == 6 {
				failed = processed / 9 // an incident
			}
			key := "asynq:{" + q + "}:"
			date := day.Format("2006-01-02")
			pipe.Set(t.Context(), key+"processed:"+date, processed, 90*24*time.Hour)
			pipe.Set(t.Context(), key+"failed:"+date, failed, 90*24*time.Hour)
		}
	}
	if _, err := pipe.Exec(t.Context()); err != nil {
		t.Fatalf("write demo history: %v", err)
	}
}

// payloads makes believable JSON payloads, the same on every run.
type payloads struct {
	rng *rand.Rand
	now time.Time
}

const alnum = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func (p *payloads) token(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alnum[p.rng.IntN(len(alnum))]
	}
	return string(b)
}

func (p *payloads) pick(s ...string) string { return s[p.rng.IntN(len(s))] }

func (p *payloads) currency() string { return p.pick("EUR", "USD", "GBP", "EUR", "USD") }

func (p *payloads) capture() string {
	return fmt.Sprintf(`{"payment_id":"pay_%s","order_id":"ord_%d","amount":%d,"currency":%q}`,
		p.token(14), 4100000+p.rng.IntN(90000), 500+p.rng.IntN(48000), p.currency())
}

func (p *payloads) invoice() string {
	return fmt.Sprintf(`{"invoice_id":"INV-%d-%05d","customer_id":"cus_%s","amount":%d,"currency":%q,"line_items":%d}`,
		p.now.Year(), 4000+p.rng.IntN(9000), p.token(10), 1900+p.rng.IntN(240000), p.currency(), 1+p.rng.IntN(9))
}

func (p *payloads) invoiceSend() string {
	return fmt.Sprintf(`{"invoice_id":"INV-%d-%05d","to":"billing@%s.example","attach_pdf":true}`,
		p.now.Year(), 4000+p.rng.IntN(9000), p.pick("northwind", "contoso", "fabrikam", "tailspin", "lakeshore", "brightline"))
}

func (p *payloads) webhook() string {
	return fmt.Sprintf(`{"endpoint_id":"we_%s","event":%q,"event_id":"evt_%s"}`,
		p.token(8), p.pick("payment.succeeded", "payment.failed", "invoice.paid", "refund.created", "payment.succeeded"), p.token(16))
}

func (p *payloads) refund() string {
	return fmt.Sprintf(`{"refund_id":"re_%s","payment_id":"pay_%s","amount":%d,"reason":%q}`,
		p.token(12), p.token(14), 500+p.rng.IntN(12000), p.pick("requested_by_customer", "duplicate", "fraudulent"))
}

func (p *payloads) ledger() string {
	return fmt.Sprintf(`{"account":%q,"date":%q}`,
		p.pick("acct_eu_main", "acct_us_main", "acct_uk_main"), p.now.AddDate(0, 0, -1).Format("2006-01-02"))
}

func (p *payloads) email() string {
	id := 10000 + p.rng.IntN(80000)
	return fmt.Sprintf(`{"user_id":%d,"to":"user%d@example.com","locale":%q}`, id, id, p.pick("en", "en", "de", "fr", "nl"))
}

func (p *payloads) sms() string {
	return fmt.Sprintf(`{"user_id":%d,"phone":"+1555%07d","ttl_seconds":300}`, 10000+p.rng.IntN(80000), p.rng.IntN(10000000))
}

func (p *payloads) push() string {
	return fmt.Sprintf(`{"user_id":%d,"platform":%q,"title":%q}`, 10000+p.rng.IntN(80000), p.pick("ios", "android"),
		p.pick("Your order has shipped", "Payment received", "New sign-in to your account", "Your report is ready"))
}

func (p *payloads) digest() string {
	y, w := p.now.ISOWeek()
	return fmt.Sprintf(`{"user_id":%d,"week":"%d-W%02d"}`, 10000+p.rng.IntN(80000), y, w)
}

func (p *payloads) report() string {
	return fmt.Sprintf(`{"report":%q,"date":%q,"format":%q}`,
		p.pick("revenue_by_region", "active_users", "refund_rate", "mrr_summary"), p.now.AddDate(0, 0, -1).Format("2006-01-02"), p.pick("pdf", "csv"))
}

func (p *payloads) export() string {
	return fmt.Sprintf(`{"export_id":"exp_%s","dataset":%q,"requested_by":"user_%d"}`,
		p.token(10), p.pick("orders", "customers", "payouts", "invoices"), 1000+p.rng.IntN(9000))
}

func (p *payloads) rollup() string {
	return fmt.Sprintf(`{"metric":%q,"window":"1h","bucket":%q}`,
		p.pick("active_users", "checkout_conversion", "api_latency_p95"), p.now.Truncate(time.Hour).Add(-time.Duration(p.rng.IntN(24))*time.Hour).Format(time.RFC3339))
}
