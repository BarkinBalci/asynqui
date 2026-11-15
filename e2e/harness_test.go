//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
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

	playwright "github.com/mxschmitt/playwright-go"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"golang.org/x/crypto/bcrypt"
)

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
	// open is auth none with admin; basic is basic auth (admin and viewer).
	open, basic *instance
	browser     playwright.Browser
	fx          fixture
	look        presentation
	// results is where this test's videos and traces go.
	results string
	// gifTaken is set once a page records the GIF.
	gifTaken bool
}

// instance is one running asynqui process.
type instance struct {
	url string
}

// newHarness empties the test's results directory, starts Redis, seeds
// the fixture, builds and starts asynqui twice and launches Chromium.
// Everything stops when t's cleanups run.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{look: presentationFromEnv(), results: filepath.Join(resultsDir, t.Name())}
	if err := os.RemoveAll(h.results); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(h.results, 0o750); err != nil {
		t.Fatal(err)
	}
	redisAddr := startRedis(t)
	start := time.Now()
	h.fx = seedFixture(t, redisAddr)
	t.Logf("seeded the fixture in %s", time.Since(start).Round(100*time.Millisecond))

	bin := buildApp(t)
	dir := t.TempDir()
	conns := connectionsConfig(redisAddr, deadAddr(t))
	h.open = startApp(t, bin, writeConfig(t, dir, "open.yaml", openConfig(conns)), anyLoopback)
	h.basic = startApp(t, bin, writeConfig(t, dir, "basic.yaml", basicConfig(t, conns)), anyLoopback)

	pw, err := playwright.Run()
	if err != nil {
		t.Fatalf("start playwright (install Chromium first, see CONTRIBUTING.md): %v", err)
	}
	t.Cleanup(func() { _ = pw.Stop() })
	h.browser, err = pw.Chromium.Launch(h.look.launchOptions())
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

// connectionsConfig is the config both instances share: the fixture's
// connections, with polling off unless a test turns it on.
func connectionsConfig(redisAddr, downAddr string) string {
	var b strings.Builder
	b.WriteString(`server:
  shutdown_delay: 0s
update_check: false
refresh:
  default: "off"
connections:
`)
	for _, c := range fixtureConns() {
		fmt.Fprintf(&b, "  - name: %s\n    addr: %q\n    db: %d\n", c.name, redisAddr, c.db)
	}
	fmt.Fprintf(&b, "  - name: %s\n    addr: %q\n    dial_timeout: 1s\n", connDown, downAddr)
	return b.String()
}

func openConfig(conns string) string {
	return conns + `auth:
  provider: none
authz:
  default: admin
`
}

func basicConfig(t *testing.T, conns string) string {
	t.Helper()
	hash := func(pw string) string {
		b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return conns + fmt.Sprintf(`auth:
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

// anyLoopback is a random loopback port.
const anyLoopback = "127.0.0.1:0"

// startApp runs the binary on addr and waits for its "listening" log
// line. Its log is printed if the test fails.
func startApp(t *testing.T, bin, cfg, addr string) *instance {
	t.Helper()
	cmd := exec.Command(bin, "-config", cfg, "-addr", addr)
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
