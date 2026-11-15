package web

import (
	"net/http"
	"sync/atomic"
)

// Readiness reports whether this instance should receive traffic. The zero
// value is ready and Drain is terminal. It never reflects Redis health.
type Readiness struct {
	draining atomic.Bool
}

// Drain marks the instance as shutting down. It cannot be undone.
func (r *Readiness) Drain() { r.draining.Store(true) }

// Draining reports whether Drain has been called.
func (r *Readiness) Draining() bool { return r.draining.Load() }

// handleLiveness answers 200 whenever the process can serve HTTP.
func handleLiveness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})
}

// handleReadiness answers 200 until shutdown begins and 503 afterwards.
func handleReadiness(ready *Readiness) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if ready.Draining() {
			writePlain(w, http.StatusServiceUnavailable, "shutting down")
			return
		}
		writePlain(w, http.StatusOK, "ok")
	})
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}
