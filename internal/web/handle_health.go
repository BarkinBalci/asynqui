package web

import (
	"net/http"
	"sync"
)

// Readiness reports whether this instance should receive traffic. It starts
// not ready, becomes ready once startup completes and becomes not ready again
// when graceful shutdown begins. It never reflects Redis health: an outage is
// shown per connection in the UI and must not unroute or restart replicas.
type Readiness struct {
	mu     sync.RWMutex
	ready  bool
	reason string
}

// NewReadiness returns a Readiness that is not ready, with the given reason.
func NewReadiness(reason string) *Readiness {
	return &Readiness{reason: reason}
}

// SetReady marks the instance ready.
func (r *Readiness) SetReady() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ready, r.reason = true, ""
}

// SetNotReady marks the instance not ready with a human-readable reason.
func (r *Readiness) SetNotReady(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ready, r.reason = false, reason
}

// State returns whether the instance is ready and, if not, why.
func (r *Readiness) State() (bool, string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ready, r.reason
}

// handleLiveness answers 200 whenever the process can serve HTTP.
func handleLiveness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})
}

// handleReadiness answers 200 once startup is done and 503 otherwise.
func handleReadiness(ready *Readiness) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ok, reason := ready.State()
		if !ok {
			writePlain(w, http.StatusServiceUnavailable, reason)
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
