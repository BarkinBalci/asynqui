package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func env(kv map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := kv[k]
		return v, ok
	}
}

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want options
	}{
		{
			name: "defaults",
			args: []string{"asynqui"},
			want: options{configPath: "asynqui.yaml", addr: ":8080", logLevel: slog.LevelInfo},
		},
		{
			name: "env beats default",
			args: []string{"asynqui"},
			env:  map[string]string{"ASYNQUI_CONFIG": "/etc/a.yaml", "ASYNQUI_ADDR": ":9090", "ASYNQUI_LOG_LEVEL": "debug"},
			want: options{configPath: "/etc/a.yaml", addr: ":9090", logLevel: slog.LevelDebug},
		},
		{
			name: "flag beats env",
			args: []string{"asynqui", "-addr", ":7070", "-config", "b.yaml", "-log-level", "warn"},
			env:  map[string]string{"ASYNQUI_CONFIG": "/etc/a.yaml", "ASYNQUI_ADDR": ":9090", "ASYNQUI_LOG_LEVEL": "debug"},
			want: options{configPath: "b.yaml", addr: ":7070", logLevel: slog.LevelWarn},
		},
		{
			name: "flag set to its default still beats env",
			args: []string{"asynqui", "-addr", ":8080"},
			env:  map[string]string{"ASYNQUI_ADDR": ":9090"},
			want: options{configPath: "asynqui.yaml", addr: ":8080", logLevel: slog.LevelInfo},
		},
		{
			name: "empty env counts as unset",
			args: []string{"asynqui"},
			env:  map[string]string{"ASYNQUI_ADDR": ""},
			want: options{configPath: "asynqui.yaml", addr: ":8080", logLevel: slog.LevelInfo},
		},
		{
			name: "help",
			args: []string{"asynqui", "-h"},
			want: options{help: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseFlags(tt.args, env(tt.env), io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("parseFlags(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestParseFlagsErrors(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		env       map[string]string
		wantUsage bool
	}{
		{name: "empty args", args: nil},
		{name: "unknown flag", args: []string{"asynqui", "-nope"}, wantUsage: true},
		{name: "positional argument", args: []string{"asynqui", "extra"}, wantUsage: true},
		{name: "bad log level", args: []string{"asynqui", "-log-level", "loud"}},
		{name: "bad log level from env", args: []string{"asynqui"}, env: map[string]string{"ASYNQUI_LOG_LEVEL": "loud"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseFlags(tt.args, env(tt.env), io.Discard)
			if err == nil {
				t.Fatal("want error")
			}
			var uerr usageError
			if got := errors.As(err, &uerr); got != tt.wantUsage {
				t.Errorf("usage error = %v, want %v (err: %v)", got, tt.wantUsage, err)
			}
		})
	}
}

func TestRunHelp(t *testing.T) {
	var stderr bytes.Buffer
	if err := run(t.Context(), []string{"asynqui", "-h"}, env(nil), io.Discard, &stderr); err != nil {
		t.Fatalf("run -h = %v, want nil", err)
	}
	if !strings.Contains(stderr.String(), "-addr") {
		t.Errorf("usage not written to stderr: %q", stderr.String())
	}
}

// TestRunDrain starts the real server, cancels its context and checks the
// shutdown sequence: readiness turns 503 while the listener still serves for
// shutdown_delay, then the listener closes and run returns nil.
func TestRunDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	logs, logw := io.Pipe()
	defer func() { _ = logs.Close() }()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"asynqui", "-addr", "127.0.0.1:0"}, env(nil), logw, io.Discard)
		_ = logw.Close()
	}()

	// Read log lines until "listening" to learn the bound address, then keep
	// draining the pipe so logging never blocks run.
	addrc := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(logs)
		for sc.Scan() {
			var rec struct{ Msg, Addr string }
			if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Msg == "listening" {
				addrc <- rec.Addr
			}
		}
	}()
	var base string
	select {
	case addr := <-addrc:
		base = "http://" + addr
	case err := <-done:
		t.Fatalf("run returned early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}

	client := &http.Client{Timeout: time.Second}
	get := func(path string) (int, string, error) {
		res, err := client.Get(base + path)
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = res.Body.Close() }()
		b, err := io.ReadAll(res.Body)
		return res.StatusCode, strings.TrimSpace(string(b)), err
	}

	if code, body, err := get("/health/readiness"); err != nil || code != http.StatusOK || body != "ok" {
		t.Fatalf("readiness before shutdown = %d %q %v, want 200 ok", code, body, err)
	}

	start := time.Now()
	cancel()

	// During shutdown_delay the listener stays open and readiness is 503.
	deadline := time.Now().Add(time.Second)
	for {
		code, body, err := get("/health/readiness")
		if err != nil {
			t.Fatalf("readiness during delay: %v (listener closed too early)", err)
		}
		if code == http.StatusServiceUnavailable && body == "shutting down" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("readiness never turned 503, last %d %q", code, body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, _, err := get("/health/liveness"); err != nil || code != http.StatusOK {
		t.Fatalf("liveness during delay = %d %v, want 200", code, err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run = %v, want nil", err)
		}
	case <-time.After(defaultShutdownDelay + shutdownTimeout):
		t.Fatal("run did not return")
	}
	if elapsed := time.Since(start); elapsed < defaultShutdownDelay {
		t.Errorf("run returned after %v, before shutdown_delay %v", elapsed, defaultShutdownDelay)
	}
	if _, _, err := get("/health/liveness"); err == nil {
		t.Error("listener still open after run returned")
	}
}

func TestRunListenError(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	err = run(t.Context(), []string{"asynqui", "-addr", ln.Addr().String()}, env(nil), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("run on a busy port = %v, want listen error", err)
	}
}
