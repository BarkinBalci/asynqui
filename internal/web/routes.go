package web

import "net/http"

// addRoutes registers every route. This file is the map of the application:
// see docs/architecture.md for the full route table.
//
// ServeMux notes: "GET /static/" is a prefix match while "GET /c/{conn}" is
// exact; "GET /{$}" matches only "/"; the most specific pattern wins regardless
// of registration order; conflicting patterns panic at startup.
func addRoutes(mux *http.ServeMux, ready *Readiness) {
	// Probes: no auth, excluded from request logging.
	mux.Handle("GET /health/liveness", handleLiveness())
	mux.Handle("GET /health/readiness", handleReadiness(ready))
}
