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
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BarkinBalci/asynqui/internal/config"
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
			want: options{configPath: "asynqui.yaml"},
		},
		{
			name: "env beats default",
			args: []string{"asynqui"},
			env:  map[string]string{"ASYNQUI_CONFIG": "/etc/a.yaml", "ASYNQUI_ADDR": ":9090", "ASYNQUI_LOG_LEVEL": "debug"},
			want: options{configPath: "/etc/a.yaml", overrides: config.Overrides{Addr: ":9090", LogLevel: "debug"}},
		},
		{
			name: "flag beats env",
			args: []string{"asynqui", "-addr", ":7070", "-config", "b.yaml", "-log-level", "warn"},
			env:  map[string]string{"ASYNQUI_CONFIG": "/etc/a.yaml", "ASYNQUI_ADDR": ":9090", "ASYNQUI_LOG_LEVEL": "debug"},
			want: options{configPath: "b.yaml", overrides: config.Overrides{Addr: ":7070", LogLevel: "warn"}},
		},
		{
			name: "flag set to its default still beats env",
			args: []string{"asynqui", "-addr", ":8080"},
			env:  map[string]string{"ASYNQUI_ADDR": ":9090"},
			want: options{configPath: "asynqui.yaml", overrides: config.Overrides{Addr: ":8080"}},
		},
		{
			name: "empty env counts as unset",
			args: []string{"asynqui"},
			env:  map[string]string{"ASYNQUI_ADDR": "", "ASYNQUI_CONFIG": "", "ASYNQUI_LOG_LEVEL": ""},
			want: options{configPath: "asynqui.yaml"},
		},
		{
			name: "help",
			args: []string{"asynqui", "-h"},
			want: options{help: true},
		},
		{
			name: "version ignores the rest",
			args: []string{"asynqui", "-version", "-addr", ":7070"},
			env:  map[string]string{"ASYNQUI_LOG_LEVEL": "loud"},
			want: options{version: true},
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

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer
	if err := run(t.Context(), []string{"asynqui", "-version", "-config", "/does/not/exist"}, env(nil), &stdout, io.Discard); err != nil {
		t.Fatalf("run -version = %v, want nil", err)
	}
	if got := stdout.String(); got != "asynqui "+buildVersion(version, debug.ReadBuildInfo)+"\n" {
		t.Errorf("stdout = %q", got)
	}
}

func TestBuildVersion(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Path: "github.com/BarkinBalci/asynqui", Version: v}}, true
		}
	}
	none := func() (*debug.BuildInfo, bool) { return nil, false }
	tests := []struct {
		name    string
		stamped string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"ldflags win", "0.2.0", info("v0.1.0"), "0.2.0"},
		{"go install @tag", "dev", info("v0.1.0"), "v0.1.0"},
		{"build in a checkout", "dev", info("v0.1.1-0.20261114015959-c1a81a5e0d4f+dirty"), "v0.1.1-0.20261114015959-c1a81a5e0d4f+dirty"},
		{"devel", "dev", info("(devel)"), "dev"},
		{"empty", "dev", info(""), "dev"},
		{"no build info", "dev", none, "dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildVersion(tt.stamped, tt.read); got != tt.want {
				t.Errorf("buildVersion = %q, want %q", got, tt.want)
			}
		})
	}
	// A pseudo-version is a prerelease, so such builds never check.
	if c := newUpdateChecker(true, "v0.1.1-0.20261114015959-c1a81a5e0d4f+dirty", slog.New(slog.DiscardHandler)); c != nil {
		t.Error("checker for a pseudo-version build")
	}
}

func TestNewUpdateChecker(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	if c := newUpdateChecker(false, "0.1.0", logger); c != nil {
		t.Error("checker with update_check: false")
	}
	if c := newUpdateChecker(true, "dev", logger); c != nil {
		t.Error("checker for a dev build")
	}
	if c := newUpdateChecker(true, "v0.1.0-3-gabc1234-dirty", logger); c != nil {
		t.Error("checker for a git describe build")
	}
	if c := newUpdateChecker(true, "0.1.0", logger); c == nil {
		t.Error("no checker for a release build")
	}
}

// TestRunDrain checks that readiness turns 503 while the listener still serves
// for shutdown_delay, then the listener closes and run returns nil.
func TestRunDrain(t *testing.T) {
	const shutdownDelay = time.Second
	cfgPath := writeConfig(t, "server: {addr: '127.0.0.1:0', shutdown_delay: 1s}\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	logs, logw := io.Pipe()
	defer func() { _ = logs.Close() }()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"asynqui", "-config", cfgPath}, env(nil), logw, io.Discard)
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
	case <-time.After(shutdownDelay + shutdownTimeout):
		t.Fatal("run did not return")
	}
	if elapsed := time.Since(start); elapsed < shutdownDelay {
		t.Errorf("run returned after %v, before shutdown_delay %v", elapsed, shutdownDelay)
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

	busy := ln.Addr().String()

	// Each case puts the busy address in a different layer above a free one,
	// so a listen error proves that layer won (flag > env > file).
	free := writeConfig(t, "server: {addr: '127.0.0.1:0'}\n")
	busyFile := writeConfig(t, "server: {addr: '"+busy+"'}\n")
	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"file", []string{"asynqui", "-config", busyFile}, nil},
		{"env over file", []string{"asynqui", "-config", free}, map[string]string{"ASYNQUI_ADDR": busy}},
		{"flag over env", []string{"asynqui", "-config", free, "-addr", busy}, map[string]string{"ASYNQUI_ADDR": "127.0.0.1:0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(t.Context(), tt.args, env(tt.env), io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "listen") {
				t.Fatalf("run on a busy port = %v, want listen error", err)
			}
		})
	}
}

// writeConfig writes a minimal valid config plus extra YAML.
func writeConfig(t *testing.T, extra string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "asynqui.yaml")
	src := "connections: [{name: local, addr: 'localhost:6379'}]\n" + extra
	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunConfig(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope.yaml")
		err := run(t.Context(), []string{"asynqui", "-config", missing}, env(nil), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("run = %v, want an error naming the file", err)
		}
	})
	t.Run("invalid config fails before listening", func(t *testing.T) {
		p := writeConfig(t, "server: {addr: '127.0.0.1:0'}\nlog: {level: loud}\n")
		var out bytes.Buffer
		err := run(t.Context(), []string{"asynqui", "-config", p}, env(nil), &out, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "log.level: must be one of") {
			t.Errorf("run = %v, want the validation error", err)
		}
		if strings.Contains(out.String(), "listening") {
			t.Error("listened despite an invalid config")
		}
	})
	t.Run("missing variable", func(t *testing.T) {
		p := writeConfig(t, "server: {addr: '127.0.0.1:0'}\nrefresh: {default: '${REFRESH}'}\n")
		err := run(t.Context(), []string{"asynqui", "-config", p}, env(nil), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "environment variable REFRESH is not set") {
			t.Errorf("run = %v, want the interpolation error", err)
		}
	})
}

// TestRunLogs checks the startup log lines: the redacted config, warnings,
// the auth-none warning on a non-loopback address, and the log level.
func TestRunLogs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		yaml     string
		want     []string
		dontWant []string
	}{
		{
			name: "loopback, warnings, no secrets",
			yaml: "server: {addr: '127.0.0.1:0', shutdown_delay: 0s}\n" +
				"connections: [{name: local, addr: 'localhost:6379', password: '${PW}'}]\n" +
				"authz: {bindings: [{users: [u], role: admin, connections: [nothing-*]}]}\n",
			env:      map[string]string{"PW": "hunter2"},
			want:     []string{`"msg":"config loaded"`, `\"nothing-*\" matches no connection`, `"msg":"listening"`},
			dontWant: []string{"hunter2", "authentication is disabled"},
		},
		{
			name:     "non-loopback with auth none warns",
			args:     []string{"-addr", ":0"},
			yaml:     "server: {shutdown_delay: 0s}\nconnections: [{name: local, addr: 'localhost:6379'}]\n",
			want:     []string{"authentication is disabled"},
			dontWant: nil,
		},
		{
			name:     "env log level beats the file",
			env:      map[string]string{"ASYNQUI_LOG_LEVEL": "error"},
			yaml:     "server: {addr: '127.0.0.1:0', shutdown_delay: 0s}\nlog: {level: debug}\nconnections: [{name: local, addr: 'localhost:6379'}]\n",
			dontWant: []string{`"msg":"listening"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "c.yaml")
			if err := os.WriteFile(p, []byte(tt.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			var out syncBuffer
			args := append([]string{"asynqui", "-config", p}, tt.args...)
			if err := run(ctx, args, env(tt.env), &out, io.Discard); err != nil {
				t.Fatalf("run: %v", err)
			}
			logs := out.String()
			for _, w := range tt.want {
				if !strings.Contains(logs, w) {
					t.Errorf("logs lack %s:\n%s", w, logs)
				}
			}
			for _, w := range tt.dontWant {
				if strings.Contains(logs, w) {
					t.Errorf("logs contain %s:\n%s", w, logs)
				}
			}
		})
	}
}

func TestIsLoopback(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:8080": true, "[::1]:8080": true, "localhost:8080": true, "127.1.2.3:1": true,
		":8080": false, "0.0.0.0:8080": false, "[::]:8080": false, "10.0.0.1:80": false, "bad": false,
	}
	for addr, want := range tests {
		if got := isLoopback(addr); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// syncBuffer is a bytes.Buffer safe for the server goroutine to write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
