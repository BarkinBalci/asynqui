package web

import (
	"log/slog"
	"net/http"
)

// NewServer returns the root handler with every route and global middleware
// applied.
func NewServer(logger *slog.Logger, ready *Readiness) http.Handler {
	mux := http.NewServeMux()
	addRoutes(mux, ready)

	var h http.Handler = mux
	h = securityHeaders(h)
	h = recoverPanics(logger, h)
	h = logRequests(logger, h)
	return h
}
