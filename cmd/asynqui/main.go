// Command asynqui is a web dashboard for asynq that connects to several Redis
// instances and logical databases at once.
//
// See https://github.com/BarkinBalci/asynqui for installation and
// configuration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BarkinBalci/asynqui/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const shutdownTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args, os.Getenv, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "asynqui: %v\n", err)
		os.Exit(1)
	}
}

// run starts the server and blocks until ctx is canceled and the server has
// shut down. All dependencies are built here and passed down explicitly.
func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	configPath := fs.String("config", "asynqui.yaml", "path to the configuration file")
	addr := fs.String("addr", ":8080", "listen address")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if v := getenv("ASYNQUI_CONFIG"); v != "" {
		*configPath = v
	}
	if v := getenv("ASYNQUI_ADDR"); v != "" {
		*addr = v
	}

	logger := slog.New(slog.NewJSONHandler(stdout, nil))
	ready := web.NewReadiness("starting")

	// Config loading and auth initialisation arrive in later milestones;
	// readiness must only flip after they succeed.
	_ = *configPath

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           web.NewServer(logger, ready),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", slog.String("addr", ln.Addr().String()), slog.String("version", version))
		if err := httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	ready.SetReady()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	ready.SetNotReady("shutting down")
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
