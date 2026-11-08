package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbes(t *testing.T) {
	ready := NewReadiness("starting")
	srv := httptest.NewServer(NewServer(slog.New(slog.DiscardHandler), ready))
	t.Cleanup(srv.Close)

	get := func(path string) (int, string) {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, strings.TrimSpace(string(b))
	}

	tests := []struct {
		name       string
		setup      func()
		path       string
		wantStatus int
		wantBody   string
	}{
		{"liveness always ok", func() {}, "/health/liveness", 200, "ok"},
		{"readiness before startup", func() {}, "/health/readiness", 503, "starting"},
		{"readiness after startup", ready.SetReady, "/health/readiness", 200, "ok"},
		{"readiness during shutdown", func() { ready.SetNotReady("shutting down") }, "/health/readiness", 503, "shutting down"},
		{"liveness during shutdown", func() {}, "/health/liveness", 200, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			status, body := get(tt.path)
			if status != tt.wantStatus || body != tt.wantBody {
				t.Errorf("GET %s = %d %q, want %d %q", tt.path, status, body, tt.wantStatus, tt.wantBody)
			}
		})
	}
}
