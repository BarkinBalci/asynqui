//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	playwright "github.com/mxschmitt/playwright-go"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"golang.org/x/crypto/bcrypt"
)

// connName is the one connection every instance shows.
const connName = "local"

// Basic-auth users of the login instance: admin has admin on every
// connection, viewer only the default viewer role.
const (
	adminUser     = "admin"
	viewerUser    = "viewer"
	adminPassword = "admin-secret"
	viewerPass    = "viewer-secret"
)

// harness is what every test shares: the seeded Redis, two asynqui
// instances and the browser.
type harness struct {
	redisAddr string
	rdb       *redis.Client
	insp      *asynq.Inspector
	// open is auth none with admin; basic is basic auth (admin and viewer).
	open, basic *instance
	browser     playwright.Browser
	seed        seeded
}

// instance is one running asynqui process.
type instance struct {
	url string
}

// newHarness starts Redis, seeds it, builds and starts asynqui twice and
// launches Chromium. Everything stops when t's cleanups run.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{redisAddr: startRedis(t)}
	h.rdb = redis.NewClient(&redis.Options{Addr: h.redisAddr})
	t.Cleanup(func() { _ = h.rdb.Close() })
	if err := h.rdb.FlushDB(t.Context()).Err(); err != nil {
		t.Fatalf("flush redis %s: %v", h.redisAddr, err)
	}
	opt := asynq.RedisClientOpt{Addr: h.redisAddr}
	h.insp = asynq.NewInspector(opt)
	t.Cleanup(func() { _ = h.insp.Close() })
	h.seed = seed(t, opt, h.insp)

	bin := buildApp(t)
	dir := t.TempDir()
	h.open = startApp(t, bin, writeConfig(t, dir, "open.yaml", openConfig(h.redisAddr)))
	h.basic = startApp(t, bin, writeConfig(t, dir, "basic.yaml", basicConfig(t, h.redisAddr)))

	pw, err := playwright.Run()
	if err != nil {
		t.Fatalf("start playwright (install Chromium first, see docs/contributing.md): %v", err)
	}
	t.Cleanup(func() { _ = pw.Stop() })
	h.browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: new(true)})
	if err != nil {
		t.Fatalf("launch chromium: %v", err)
	}
	t.Cleanup(func() { _ = h.browser.Close() })
	return h
}

// startRedis returns ASYNQUI_E2E_REDIS, or starts a testcontainers Redis.
func startRedis(t *testing.T) string {
	t.Helper()
	if addr := os.Getenv("ASYNQUI_E2E_REDIS"); addr != "" {
		return addr
	}
	ctr, err := tcredis.Run(t.Context(), "redis:7.4-alpine")
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("no ASYNQUI_E2E_REDIS and no testcontainers Redis: %v", err)
	}
	addr, err := ctr.PortEndpoint(t.Context(), "6379/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

// buildApp builds cmd/asynqui into a temporary directory.
func buildApp(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "asynqui")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd/asynqui")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build asynqui: %v\n%s", err, out)
	}
	return bin
}

func writeConfig(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// common is the config every instance shares: one connection, no update
// check, no shutdown delay, polling off unless a test turns it on.
func common(redisAddr string) string {
	return fmt.Sprintf(`server:
  shutdown_delay: 0s
update_check: false
refresh:
  default: "off"
connections:
  - name: %s
    addr: %q
`, connName, redisAddr)
}

func openConfig(redisAddr string) string {
	return common(redisAddr) + `auth:
  provider: none
authz:
  default: admin
`
}

func basicConfig(t *testing.T, redisAddr string) string {
	t.Helper()
	hash := func(pw string) string {
		b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return common(redisAddr) + fmt.Sprintf(`auth:
  provider: basic
  session:
    hash_key: %q
    block_key: %q
    cookie_secure: false
  basic:
    users:
      - username: %s
        password_hash: %q
      - username: %s
        password_hash: %q
authz:
  default: viewer
  bindings:
    - users: [%s]
      role: admin
      connections: ["*"]
`, strings.Repeat("ab", 32), strings.Repeat("cd", 32),
		adminUser, hash(adminPassword), viewerUser, hash(viewerPass), adminUser)
}

// startApp runs the binary on a random loopback port and waits for its
// "listening" log line. Its log is printed if the test fails.
func startApp(t *testing.T, bin, cfg string) *instance {
	t.Helper()
	cmd := exec.Command(bin, "-config", cfg, "-addr", "127.0.0.1:0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var logMu sync.Mutex
	var logBuf strings.Builder
	cmd.Stderr = &lockedWriter{mu: &logMu, w: &logBuf}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	addrc := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			logMu.Lock()
			logBuf.WriteString(line + "\n")
			logMu.Unlock()
			var rec struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal([]byte(line), &rec) == nil && rec.Msg == "listening" {
				select {
				case addrc <- rec.Addr:
				default:
				}
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			logMu.Lock()
			lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
			lines = lines[max(0, len(lines)-40):]
			t.Logf("asynqui log (%s), last lines:\n%s", filepath.Base(cfg), strings.Join(lines, "\n"))
			logMu.Unlock()
		}
	})
	select {
	case addr := <-addrc:
		return &instance{url: "http://" + addr}
	case <-time.After(30 * time.Second):
		logMu.Lock()
		defer logMu.Unlock()
		t.Fatalf("asynqui did not start:\n%s", logBuf.String())
	}
	return nil
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// ---- seed ----

// Queues the seeder fills, one or more per test so tests do not interfere.
const (
	qSortA   = "conn-a" // 3 pending
	qSortB   = "conn-b" // 1 pending
	qSortC   = "conn-c" // 2 pending
	qEmpty   = "empty"  // exists, no tasks
	qBulk    = "bulk"   // bulkCount pending: "select all N"
	qArchive = "arch"   // 2 pending: the Archive confirmation
	qFind    = "find"   // findID
	qSched   = "sched"  // 3 scheduled
	qWork    = "work"   // activeCount active (a worker holds them)
	qFlaky   = "flaky"  // one archived and one retry task, failed by the worker
)

const (
	bulkCount   = 30
	activeCount = 22 // more than the smallest page (20), so "select all N" would apply
	findID      = "e2e-find-target"
	// copyID is long enough to be shortened in tables; the Task page
	// copies it whole.
	copyID      = "0f8fad5b-d9cb-469f-a165-70867728950e"
	copyPayload = `{"user_id":42,"note":"copy me in full"}`
)

// seeded names what the seeder made for the tests to use.
type seeded struct {
	taskURL     string // the Task page of copyID
	archivedURL string // the Task page of the archived task (with an error)
}

func seed(t *testing.T, opt asynq.RedisClientOpt, insp *asynq.Inspector) seeded {
	t.Helper()
	c := asynq.NewClient(opt)
	defer func() { _ = c.Close() }()
	enqueue := func(q string, n int, opts ...asynq.Option) {
		t.Helper()
		for i := range n {
			task := asynq.NewTask("email:send", fmt.Appendf(nil, `{"queue":%q,"n":%d}`, q, i))
			if _, err := c.Enqueue(task, append([]asynq.Option{asynq.Queue(q)}, opts...)...); err != nil {
				t.Fatalf("enqueue %s: %v", q, err)
			}
		}
	}
	enqueue(qSortA, 3)
	enqueue(qSortB, 1)
	enqueue(qSortC, 2)
	enqueue(qBulk, bulkCount)
	enqueue(qArchive, 2)
	enqueue(qSched, 3, asynq.ProcessIn(10*time.Minute))
	if _, err := c.Enqueue(asynq.NewTask("report:daily", []byte(`{"kind":"find"}`)), asynq.Queue(qFind), asynq.TaskID(findID)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Enqueue(asynq.NewTask("image:resize", []byte(copyPayload)), asynq.Queue(qFind), asynq.TaskID(copyID)); err != nil {
		t.Fatal(err)
	}
	// An empty queue: asynq keeps a queue once it has had a task.
	info, err := c.Enqueue(asynq.NewTask("noop", nil), asynq.Queue(qEmpty))
	if err != nil {
		t.Fatal(err)
	}
	if err := insp.DeleteTask(qEmpty, info.ID); err != nil {
		t.Fatal(err)
	}

	// A worker holds activeCount tasks of qWork and fails qFlaky's.
	for range activeCount {
		if _, err := c.Enqueue(asynq.NewTask("block", nil), asynq.Queue(qWork)); err != nil {
			t.Fatal(err)
		}
	}
	archived, err := c.Enqueue(asynq.NewTask("fail", []byte(`{"final":true}`)), asynq.Queue(qFlaky), asynq.MaxRetry(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Enqueue(asynq.NewTask("fail", []byte(`{"final":false}`)), asynq.Queue(qFlaky), asynq.MaxRetry(5)); err != nil {
		t.Fatal(err)
	}
	srv := asynq.NewServer(opt, asynq.Config{
		Concurrency:     activeCount + 3,
		Queues:          map[string]int{qWork: 1, qFlaky: 1},
		RetryDelayFunc:  func(int, error, *asynq.Task) time.Duration { return time.Hour },
		ShutdownTimeout: time.Second,
		LogLevel:        asynq.ErrorLevel,
	})
	handler := asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		if task.Type() == "block" {
			<-ctx.Done()
			return ctx.Err()
		}
		return errors.New("dial tcp 10.0.3.7:443: i/o timeout")
	})
	if err := srv.Start(handler); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Shutdown)
	sch := asynq.NewScheduler(opt, &asynq.SchedulerOpts{LogLevel: asynq.ErrorLevel})
	if _, err := sch.Register("@every 1h", asynq.NewTask("report:daily", []byte(`{"kind":"summary"}`)), asynq.Queue(qSched)); err != nil {
		t.Fatal(err)
	}
	if err := sch.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sch.Shutdown)

	waitFor(t, 30*time.Second, "the worker to pick up the seeded tasks", func() bool {
		w, err1 := insp.GetQueueInfo(qWork)
		f, err2 := insp.GetQueueInfo(qFlaky)
		return err1 == nil && err2 == nil && w.Active == activeCount && f.Archived == 1 && f.Retry == 1
	})
	return seeded{
		taskURL:     "/c/" + connName + "/queues/" + qFind + "/tasks/" + copyID,
		archivedURL: "/c/" + connName + "/queues/" + qFlaky + "/tasks/" + archived.ID,
	}
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
