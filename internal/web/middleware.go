package web

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// contentSecurityPolicy denies everything by default and allows only
// same-origin scripts, styles, images, fonts and fetches: no inline scripts,
// inline styles or eval, no frames in either direction, and forms post only
// to this origin. Flows that leave the origin (SSO, proxy sign out) are
// links, not forms, because form-action also applies to a form's redirects.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self'; font-src 'self'; connect-src 'self'; form-action 'self'; " +
	"frame-src 'none'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'"

// permissionsPolicy turns off powerful features the UI never uses.
// clipboard-write stays allowed for the copy buttons.
const permissionsPolicy = "accelerometer=(), autoplay=(), camera=(), display-capture=(), " +
	"geolocation=(), gyroscope=(), magnetometer=(), microphone=(), midi=(), payment=(), " +
	"publickey-credentials-get=(), serial=(), usb=(), xr-spatial-tracking=()"

// securityHeaders sets the headers every response carries.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", permissionsPolicy)
		next.ServeHTTP(w, r)
	})
}

// logRequests logs one structured line per request, except probes under
// <base>/health/.
func logRequests(logger *slog.Logger, base string, next http.Handler) http.Handler {
	probes := base + "/health/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, probes) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.statusCode()),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

// recoverPanics turns a handler panic into a 500 and logs it with the stack.
// If the handler already sent the header, the response is left as is.
// Otherwise internal writes the 500; nil writes plain text.
func recoverPanics(logger *slog.Logger, internal http.HandlerFunc, next http.Handler) http.Handler {
	if internal == nil {
		internal = func(w http.ResponseWriter, _ *http.Request) {
			plainError(w, http.StatusInternalServerError)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &responseRecorder{ResponseWriter: w}
		ctx := r.Context()
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, per net/http docs
				panic(v)
			}
			logger.ErrorContext(ctx, "panic",
				slog.Any("panic", v),
				slog.String("path", r.URL.Path),
				slog.String("stack", string(debug.Stack())),
			)
			if !rec.wroteHeader {
				internal(rec, r)
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

// responseRecorder remembers the status code and whether the header was sent.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rr *responseRecorder) WriteHeader(code int) {
	// 1xx informational responses (except 101) are not the final header.
	if !rr.wroteHeader && (code >= 200 || code == http.StatusSwitchingProtocols) {
		rr.status, rr.wroteHeader = code, true
	}
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if !rr.wroteHeader {
		rr.status, rr.wroteHeader = http.StatusOK, true
	}
	return rr.ResponseWriter.Write(b)
}

func (rr *responseRecorder) statusCode() int {
	if !rr.wroteHeader {
		return http.StatusOK
	}
	return rr.status
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (rr *responseRecorder) Unwrap() http.ResponseWriter { return rr.ResponseWriter }
