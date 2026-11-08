// Command asynqui is a web dashboard for asynq that connects to several Redis
// instances and logical databases at once.
//
// See https://github.com/BarkinBalci/asynqui for installation and
// configuration.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// shutdownTimeout bounds http.Server.Shutdown. server.shutdown_delay plus
// this must stay below the orchestrator's grace period
// (terminationGracePeriodSeconds on Kubernetes).
const shutdownTimeout = 10 * time.Second

func main() {
	err := run(context.Background(), os.Args, os.LookupEnv, os.Stdout, os.Stderr)
	var uerr usageError
	switch {
	case err == nil:
	case errors.As(err, &uerr):
		os.Exit(2) // the flag package already printed the error and usage
	default:
		fmt.Fprintf(os.Stderr, "asynqui: %v\n", err)
		os.Exit(1)
	}
}

// run starts the server and blocks until ctx is canceled or SIGINT/SIGTERM
// arrives, and the server has drained. All dependencies are built here and
// passed down explicitly. Startup is fail-fast: anything that can fail runs
// before the listener opens, and its error is returned.
func run(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts, err := parseFlags(args, lookupEnv, stderr)
	if err != nil {
		return err
	}
	if opts.help {
		return nil
	}

	cfg, err := config.Load(opts.configPath, lookupEnv, opts.overrides)
	if err != nil {
		return err // Load names the file and every problem
	}
	logger := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: cfg.Log.SlogLevel()}))
	logger.Info("config loaded", slog.String("path", opts.configPath), slog.Any("config", cfg))
	for _, w := range cfg.Warnings() {
		logger.Warn("config", slog.String("warning", w))
	}
	if cfg.Auth.Provider == config.ProviderNone && !isLoopback(cfg.Server.Addr) {
		logger.Warn("authentication is disabled (auth.provider: none) on a non-loopback address",
			slog.String("addr", cfg.Server.Addr), slog.String("authz_default", string(cfg.Authz.Default)))
	}

	// Auth initialisation and OIDC discovery are added here, before Listen,
	// as their packages land.
	shutdownDelay := cfg.Server.ShutdownDelay

	var ready web.Readiness
	httpServer := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           web.NewServer(logger, &ready),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		// In-flight requests must survive the signal; Shutdown drains them.
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
		ErrorLog:    slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Server.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	errc := make(chan error, 1)
	go func() {
		defer close(errc)
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	logger.Info("listening", slog.String("addr", ln.Addr().String()), slog.String("version", version))

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	// Restore default signal handling so a second Ctrl-C force-quits.
	stop()

	ready.Drain()
	logger.Info("shutting down", slog.Duration("shutdown_delay", shutdownDelay))
	if shutdownDelay > 0 {
		time.Sleep(shutdownDelay)
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		_ = httpServer.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	logger.Info("stopped")
	return nil
}

// isLoopback reports whether a listen address accepts only local
// connections. An empty host listens on every interface.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
