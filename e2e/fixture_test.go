//go:build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// The fixture's connections: one logical DB each on the same Redis, plus
// one on a port nothing listens on.
const (
	connPayments      = "payments"
	connNotifications = "notifications"
	connReports       = "reports"
	connDown          = "analytics"
)

// Queues the tests use by name.
const (
	qCritical = "critical" // payments
	qInvoices = "invoices" // payments: pending, scheduled, retry, archived
	qWebhooks = "webhooks" // payments: webhookRetries retry tasks
	qRefunds  = "refunds"  // payments: scheduled and archived only
	qPayouts  = "payouts"  // payments: empty, every task processed
	qPush     = "push"     // notifications: pushActive active tasks
	qDigest   = "digest"   // notifications: paused
	qLow      = "low"      // reports: lowBacklog pending behind busy workers
)

// Counts the tests assert.
const (
	webhookRetries = 9
	pushActive     = 22 // more than the smallest page (20), so "select all N" would apply
	lowBacklog     = 41
)

// fixture names what the seeder made for the tests to use.
type fixture struct {
	// insp is an inspector per reachable connection.
	insp map[string]*asynq.Inspector
	// refund is a scheduled task in qRefunds; archived is an archived
	// one, with an error.
	refund, archived seededTask
}

type seededTask struct {
	conn, queue, id, payload string
}

// path is the task's Task page.
func (s seededTask) path() string {
	return "/c/" + s.conn + "/queues/" + s.queue + "/tasks/" + s.id
}

// fixtureConn is one connection's logical DB and what runs on it: the
// worker's concurrency and queue priorities, and each queue's usual
// tasks per day for the history.
type fixtureConn struct {
	name        string
	db          int
	concurrency int
	queues      map[string]int
	daily       map[string]int
}

// fixtureConns are the reachable connections, in sidebar order.
func fixtureConns() []fixtureConn {
	return []fixtureConn{
		{connPayments, 0, 10,
			map[string]int{qCritical: 6, qInvoices: 3, qWebhooks: 2, qRefunds: 1, "ledger": 1, qPayouts: 1},
			map[string]int{qCritical: 640, qInvoices: 420, qWebhooks: 560, qRefunds: 45, "ledger": 96, qPayouts: 24}},
		{connNotifications, 1, 30,
			map[string]int{"email": 4, "sms": 4, qPush: 2, qDigest: 1},
			map[string]int{"email": 1100, "sms": 420, qPush: 1500}},
		{connReports, 2, 4,
			map[string]int{"reports": 3, "exports": 2, qLow: 1},
			map[string]int{"reports": 70, "exports": 140, qLow: 288}},
	}
}

// outcome is what the fixture's worker does with one task.
type outcome struct {
	block bool          // hold a worker until shutdown
	err   string        // fail with this error
	tries int           // fail fast until the task has been retried this often
	next  time.Duration // then the delay before the next retry
}

// fixtureError carries the delay the retry delay function returns.
type fixtureError struct {
	msg   string
	delay time.Duration
}

func (e *fixtureError) Error() string { return e.msg }

// worker runs the fixture's handler for one connection.
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
		return &fixtureError{msg: o.err, delay: delay}
	}
	return nil
}

func retryDelay(_ int, err error, _ *asynq.Task) time.Duration {
	if fe, ok := errors.AsType[*fixtureError](err); ok {
		return fe.delay
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
	// retry and archived count the planned failures per queue.
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

// add enqueues one task with outcome o and returns it.
func (s *seeder) add(queue, typ, payload string, o outcome, opts ...asynq.Option) seededTask {
	s.t.Helper()
	id := s.id()
	s.w.set(id, o)
	opts = append([]asynq.Option{asynq.Queue(queue), asynq.TaskID(id)}, opts...)
	if _, err := s.c.Enqueue(asynq.NewTask(typ, []byte(payload)), opts...); err != nil {
		s.t.Fatalf("%s: enqueue %s on %s: %v", s.name, typ, queue, err)
	}
	return seededTask{conn: s.name, queue: queue, id: id, payload: payload}
}

// ok enqueues n tasks that succeed.
func (s *seeder) ok(queue, typ string, n int, payload func() string) {
	for range n {
		s.add(queue, typ, payload(), outcome{})
	}
}

// fail enqueues a task that ends in the retry set after tries attempts,
// next before its next retry.
func (s *seeder) fail(queue, typ, payload, msg string, tries int, next time.Duration) {
	s.retry[queue]++
	s.add(queue, typ, payload, outcome{err: msg, tries: tries, next: next})
}

// dead enqueues a task that is archived after tries retries.
func (s *seeder) dead(queue, typ, payload, msg string, tries int) seededTask {
	s.archived[queue]++
	return s.add(queue, typ, payload, outcome{err: msg, next: 100 * time.Millisecond}, asynq.MaxRetry(tries))
}

// busy enqueues a task that holds a worker.
func (s *seeder) busy(queue, typ, payload string) {
	s.add(queue, typ, payload, outcome{block: true})
}

// later enqueues a scheduled task.
func (s *seeder) later(queue, typ, payload string, in time.Duration) seededTask {
	return s.add(queue, typ, payload, outcome{}, asynq.ProcessIn(in))
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
					if time.Until(ti.NextProcessAt) < 5*time.Minute {
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

// seedFixture flushes the fixture's DBs and fills them through asynq's
// client, inspector, workers and schedulers: tasks processed today,
// retries and archived tasks with errors, busy workers with a backlog,
// scheduled tasks, a paused queue, an empty one and 30 days of
// throughput history. Every retry is due in 8 minutes or more, so
// nothing moves while the tests run.
func seedFixture(t *testing.T, addr string) fixture {
	t.Helper()
	rng := rand.New(rand.NewPCG(7, 42))
	fx := fixture{insp: map[string]*asynq.Inspector{}}
	seeders := map[string]*seeder{}
	for _, fc := range fixtureConns() {
		opt := asynq.RedisClientOpt{Addr: addr, DB: fc.db}
		rdb := redis.NewClient(&redis.Options{Addr: addr, DB: fc.db})
		t.Cleanup(func() { _ = rdb.Close() })
		if err := rdb.FlushDB(t.Context()).Err(); err != nil {
			t.Fatalf("flush redis %s db %d: %v", addr, fc.db, err)
		}
		c := asynq.NewClient(opt)
		t.Cleanup(func() { _ = c.Close() })
		insp := asynq.NewInspector(opt)
		t.Cleanup(func() { _ = insp.Close() })
		fx.insp[fc.name] = insp
		seeders[fc.name] = &seeder{
			t: t, name: fc.name, c: c, insp: insp, rng: rng,
			w:     &worker{plan: map[string]outcome{}},
			retry: map[string]int{}, archived: map[string]int{},
		}
		writeDailyCounters(t, rdb, rng, fc.daily)
	}
	p := &payloads{rng: rng, now: time.Now().UTC()}

	// The paused queue fills before any worker runs.
	n := seeders[connNotifications]
	for range 214 {
		n.add(qDigest, "digest:weekly", p.digest(), outcome{})
	}
	if err := n.insp.PauseQueue(qDigest); err != nil {
		t.Fatal(err)
	}

	for _, fc := range fixtureConns() {
		s := seeders[fc.name]
		srv := asynq.NewServer(asynq.RedisClientOpt{Addr: addr, DB: fc.db}, asynq.Config{
			Concurrency:              fc.concurrency,
			Queues:                   fc.queues,
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
	s := seeders[connPayments]
	s.ok(qCritical, "payment:capture", 388, p.capture)
	s.ok(qCritical, "payment:authorize", 142, p.capture)
	s.ok(qInvoices, "invoice:generate", 176, p.invoice)
	s.ok(qInvoices, "invoice:send", 171, p.invoiceSend)
	s.ok(qWebhooks, "webhook:deliver", 463, p.webhook)
	s.ok(qRefunds, "refund:process", 31, p.refund)
	s.ok("ledger", "ledger:reconcile", 24, p.ledger)
	s.ok(qPayouts, "payout:create", 18, p.payout)
	hooks := []struct {
		err   string
		tries int
		next  time.Duration
	}{
		{"POST https://shop.example.com/hooks/payments: 503 Service Unavailable", 4, 8*time.Minute + 12*time.Second},
		{"POST https://erp.example.net/api/webhooks: context deadline exceeded (Client.Timeout exceeded while awaiting headers)", 2, 9*time.Minute + 40*time.Second},
		{"dial tcp 203.0.113.24:443: connect: connection refused", 6, 41 * time.Minute},
		{"POST https://crm.example.org/inbound/payments: 429 Too Many Requests", 1, 10*time.Minute + 58*time.Second},
		{"POST https://shop.example.com/hooks/payments: 503 Service Unavailable", 3, 12*time.Minute + 5*time.Second},
		{"tls: failed to verify certificate: x509: certificate has expired or is not yet valid", 8, 2*time.Hour + 16*time.Minute},
		{"POST https://erp.example.net/api/webhooks: 502 Bad Gateway", 2, 14*time.Minute + 31*time.Second},
		{"dial tcp 203.0.113.24:443: connect: connection refused", 5, 26*time.Minute + 30*time.Second},
		{"POST https://crm.example.org/inbound/payments: 429 Too Many Requests", 1, 15*time.Minute + 44*time.Second},
	}
	for _, h := range hooks {
		s.fail(qWebhooks, "webhook:deliver", p.webhook(), h.err, h.tries, h.next)
	}
	s.fail(qInvoices, "invoice:generate", p.invoice(), "render invoice pdf: chromium: navigation timeout of 30000 ms exceeded", 3, 11*time.Minute+20*time.Second)
	s.fail(qInvoices, "invoice:send", p.invoiceSend(), "smtp: 421 4.7.0 Try again later, closing connection", 2, 16*time.Minute)
	s.fail(qCritical, "payment:capture", p.capture(), "psp: 503 upstream unavailable (request_id req_8f2k1LqZ)", 1, 8*time.Minute+45*time.Second)
	fx.archived = s.dead(qRefunds, "refund:process", p.refund(), "psp: refund rejected: charge has already been refunded", 3)
	s.dead(qRefunds, "refund:process", p.refund(), "psp: refund rejected: amount exceeds the captured amount", 3)
	s.dead(qWebhooks, "webhook:deliver", p.webhook(), "POST https://legacy.example.com/hooks: 410 Gone", 5)
	s.dead(qInvoices, "invoice:generate", p.invoice(), `template "invoice_de_v2" not found`, 2)

	s = seeders[connNotifications]
	s.ok("email", "email:welcome", 268, p.email)
	s.ok("email", "email:receipt", 512, p.email)
	s.ok("email", "email:password_reset", 87, p.email)
	s.ok("sms", "sms:otp", 341, p.sms)
	s.ok(qPush, "push:send", 1205, p.push)
	s.fail("email", "email:receipt", p.email(), "smtp: 421 4.7.0 Try again later, closing connection", 2, 9*time.Minute)
	s.fail("email", "email:welcome", p.email(), "smtp: 452 4.2.2 mailbox full", 4, 52*time.Minute)
	s.fail("email", "email:receipt", p.email(), "smtp: 421 4.7.0 Try again later, closing connection", 1, 13*time.Minute)
	s.fail("sms", "sms:otp", p.sms(), "sms gateway: 429 Too Many Requests", 1, 9*time.Minute+30*time.Second)
	for range 6 {
		s.dead(qPush, "push:send", p.push(), "push: device token is no longer registered", 0)
	}
	s.dead("email", "email:welcome", p.email(), "smtp: 550 5.1.1 recipient address rejected: user unknown", 2)
	s.dead("email", "email:receipt", p.email(), "smtp: 550 5.1.1 recipient address rejected: user unknown", 2)

	s = seeders[connReports]
	s.ok("reports", "report:daily", 64, p.report)
	s.ok("exports", "export:csv", 118, p.export)
	s.ok(qLow, "analytics:rollup", 288, p.rollup)
	s.fail("exports", "export:csv", p.export(), "s3: PutObject: RequestTimeout: your socket connection to the server was not read from or written to within the timeout period", 2, 14*time.Minute)
	s.fail("reports", "report:daily", p.report(), "pq: canceling statement due to statement timeout", 1, 18*time.Minute)
	s.dead("reports", "report:daily", p.report(), `template "churn_cohorts" not found`, 1)

	for _, fc := range fixtureConns() {
		seeders[fc.name].settled()
	}

	// Long tasks hold every payments and reports worker and some
	// notifications ones; what comes after them waits as a backlog.
	s = seeders[connPayments]
	for range 4 {
		s.busy(qInvoices, "invoice:generate", p.invoice())
	}
	for range 3 {
		s.busy(qWebhooks, "webhook:deliver", p.webhook())
	}
	s.busy(qCritical, "payment:capture", p.capture())
	s.busy(qCritical, "payment:authorize", p.capture())
	s.busy("ledger", "ledger:reconcile", p.ledger())
	s.active(10)
	s = seeders[connReports]
	s.busy("reports", "report:monthly", p.report())
	s.busy("exports", "export:csv", p.export())
	s.busy("exports", "export:csv", p.export())
	s.busy(qLow, "analytics:rollup", p.rollup())
	s.active(4)
	s = seeders[connNotifications]
	s.busy("email", "email:receipt", p.email())
	for range pushActive {
		s.busy(qPush, "push:send", p.push())
	}
	s.active(1 + pushActive)

	s = seeders[connPayments]
	for range 19 {
		s.add(qInvoices, "invoice:generate", p.invoice(), outcome{})
		s.add(qInvoices, "invoice:send", p.invoiceSend(), outcome{})
	}
	for range 64 {
		s.add(qWebhooks, "webhook:deliver", p.webhook(), outcome{})
	}
	for range 5 {
		s.add(qCritical, "payment:capture", p.capture(), outcome{})
	}
	for i := range 12 {
		s.later(qInvoices, "invoice:send", p.invoiceSend(), time.Duration(20+i*37)*time.Minute)
	}
	for i := range 6 {
		task := s.later(qRefunds, "refund:process", p.refund(), time.Duration(3+i*5)*time.Hour)
		if i == 0 {
			fx.refund = task
		}
	}
	for i := range 4 {
		s.later(qCritical, "payment:capture", p.capture(), time.Duration(35+i*12)*time.Minute)
	}
	s = seeders[connReports]
	for range 23 {
		s.add("exports", "export:csv", p.export(), outcome{})
	}
	for range lowBacklog {
		s.add(qLow, "analytics:rollup", p.rollup(), outcome{})
	}
	for i := range 3 {
		s.later("reports", "report:daily", p.report(), time.Duration(5+i*8)*time.Hour)
	}
	s = seeders[connNotifications]
	for i := range 18 {
		s.later("email", "email:trial_ending", p.email(), time.Duration(1+i*4)*time.Hour)
	}

	// Spec, type, queue and payload of each scheduler entry.
	schedule := map[string][][4]string{
		connPayments:      {{"@every 15m", "ledger:reconcile", "ledger", `{"accounts":"all"}`}},
		connNotifications: {{"0 8 * * 1", "digest:weekly", qDigest, `{"segment":"weekly_active"}`}},
		connReports:       {{"0 6 * * *", "report:daily", "reports", `{"report":"revenue_by_region","format":"pdf"}`}},
	}
	for _, fc := range fixtureConns() {
		sch := asynq.NewScheduler(asynq.RedisClientOpt{Addr: addr, DB: fc.db}, &asynq.SchedulerOpts{LogLevel: asynq.FatalLevel})
		for _, e := range schedule[fc.name] {
			if _, err := sch.Register(e[0], asynq.NewTask(e[1], []byte(e[3])), asynq.Queue(e[2])); err != nil {
				t.Fatal(err)
			}
		}
		if err := sch.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sch.Shutdown)
	}
	return fx
}

// writeDailyCounters writes asynq's daily processed and failed counters
// directly, for the 29 days before today, so the Connection page's
// throughput chart has a believable shape. The keys follow asynq v0.26.0
// internal/base (ProcessedKey and FailedKey,
// "asynq:{<queue>}:processed:<UTC date>"), with asynq's 90-day expiry.
// Today's counters come from the fixture's workers.
func writeDailyCounters(t *testing.T, rdb *redis.Client, rng *rand.Rand, daily map[string]int) {
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
		t.Fatalf("write daily counters: %v", err)
	}
}

// deadPort is where the unreachable connection points, so its address
// reads the same on every run.
const deadPort = "localhost:6380"

// deadAddr is deadPort, or another loopback address when something
// listens there.
func deadAddr(t *testing.T) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", deadPort, time.Second)
	if err != nil {
		return deadPort
	}
	_ = c.Close()
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

func (p *payloads) payout() string {
	return fmt.Sprintf(`{"payout_id":"po_%s","account":%q,"amount":%d,"currency":%q}`,
		p.token(12), p.pick("acct_eu_main", "acct_us_main", "acct_uk_main"), 25000+p.rng.IntN(900000), p.currency())
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
