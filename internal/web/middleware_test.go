package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hi"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'none'", "script-src 'self'", "style-src 'self'", "form-action 'self'",
		"frame-src 'none'", "frame-ancestors 'none'", "base-uri 'none'", "object-src 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	for _, bad := range []string{"unsafe-inline", "unsafe-eval"} {
		if strings.Contains(csp, bad) {
			t.Errorf("CSP %q contains %q", csp, bad)
		}
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("Referrer-Policy = %q, want same-origin", got)
	}
	if got := rec.Header().Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
		t.Errorf("Cross-Origin-Opener-Policy = %q, want same-origin", got)
	}
	pp := rec.Header().Get("Permissions-Policy")
	for _, want := range []string{"camera=()", "microphone=()", "geolocation=()", "payment=()"} {
		if !strings.Contains(pp, want) {
			t.Errorf("Permissions-Policy %q lacks %q", pp, want)
		}
	}
	if strings.Contains(pp, "clipboard-write") {
		t.Errorf("Permissions-Policy %q blocks the copy buttons", pp)
	}
}

func TestRecoverPanics(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
		wantBody   string
	}{
		{
			name:       "panic before writing",
			handler:    func(http.ResponseWriter, *http.Request) { panic("boom") },
			wantStatus: http.StatusInternalServerError,
			wantBody:   "Internal Server Error\n",
		},
		{
			name: "panic after writing",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte("partial"))
				panic("boom")
			},
			wantStatus: http.StatusAccepted,
			wantBody:   "partial",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			rec := httptest.NewRecorder()
			recoverPanics(logger, nil, tt.handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

			if rec.Code != tt.wantStatus || rec.Body.String() != tt.wantBody {
				t.Errorf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantBody)
			}
			if !strings.Contains(logs.String(), `"stack":"goroutine`) {
				t.Errorf("log has no stack: %s", logs.String())
			}
		})
	}
}

func TestRecoverPanicsRepanicsAbortHandler(t *testing.T) {
	h := recoverPanics(slog.New(slog.DiscardHandler), nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // identity check
			t.Errorf("recovered %v, want http.ErrAbortHandler", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}
