//go:build e2e

package e2e

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
)

// TestDemo seeds the fixture, with its workers and schedulers running,
// and serves asynqui on it at the address in ASYNQUI_E2E_DEMO until
// interrupted. It runs only when that variable is set (task demo).
func TestDemo(t *testing.T) {
	addr := os.Getenv("ASYNQUI_E2E_DEMO")
	if addr == "" {
		t.Skip("ASYNQUI_E2E_DEMO is not set")
	}
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	redisAddr := startRedis(t)
	seedFixture(t, redisAddr)
	conns := connectionsConfig(redisAddr, deadAddr(t))
	cfg := writeConfig(t, t.TempDir(), "demo.yaml", openConfig(conns))
	app := startApp(t, buildApp(t), cfg, addr)
	t.Logf("asynqui is serving the fixture on %s; press Ctrl-C to stop", app.url)
	<-ctx.Done()
}
