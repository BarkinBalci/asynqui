package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbes(t *testing.T) {
	tests := []struct {
		name       string
		draining   bool
		path       string
		wantStatus int
		wantBody   string
	}{
		{"liveness", false, "/health/liveness", http.StatusOK, "ok"},
		{"readiness", false, "/health/readiness", http.StatusOK, "ok"},
		{"readiness while draining", true, "/health/readiness", http.StatusServiceUnavailable, "shutting down"},
		{"liveness while draining", true, "/health/liveness", http.StatusOK, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var ready Readiness
			if tt.draining {
				ready.Drain()
			}
			h, _ := newTestServer(t, testOpts{ready: &ready})

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			body, err := io.ReadAll(rec.Result().Body)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(body)); rec.Code != tt.wantStatus || got != tt.wantBody {
				t.Errorf("GET %s = %d %q, want %d %q", tt.path, rec.Code, got, tt.wantStatus, tt.wantBody)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestReadinessDrainIsTerminal(t *testing.T) {
	var r Readiness
	if r.Draining() {
		t.Fatal("zero Readiness is draining")
	}
	r.Drain()
	r.Drain()
	if !r.Draining() {
		t.Fatal("Readiness not draining after Drain")
	}
}
