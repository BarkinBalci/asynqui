package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Kind names an authentication provider.
type Kind string

// Providers, as spelled in auth.provider.
const (
	KindNone  Kind = "none"
	KindBasic Kind = "basic"
	KindProxy Kind = "proxy"
	KindOIDC  Kind = "oidc"
)

// Failure says why a sign-in failed. The login page maps it to text; the
// detail is logged here and never shown.
type Failure int

// Failure codes passed to a FailFunc.
const (
	FailBadRequest         Failure = iota + 1 // malformed form
	FailInvalidCredentials                    // "Invalid username or password."
	FailTooManyAttempts                       // basic rate limit hit
	FailSSO                                   // any OIDC failure
	FailSessionTooLarge                       // identity does not fit in the cookie
	FailInternal                              // anything else
)

const maxFormBytes = 64 << 10

// Options configures New. Only the section matching Provider is used.
type Options struct {
	Provider Kind
	// Session is required for basic and oidc. none and proxy keep no
	// session: proxy re-reads the headers on every request.
	Session SessionOptions
	// SessionGroups are the groups worth keeping in an OIDC session: those
	// referenced by some authz binding. Other groups are dropped to keep the
	// cookie small. Basic sessions take groups from config instead.
	SessionGroups []string
	Basic         BasicOptions
	Proxy         ProxyOptions
	OIDC          OIDCOptions
	// BasePath is the sub-path the app is served under ("" or "/asynq").
	// Cookies are scoped to it and redirects stay inside it.
	BasePath string
	// Logger records sign-in failures. Nil discards.
	Logger *slog.Logger
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// FailFunc renders a failed sign-in with the suggested HTTP status. Nil
// means a bare status-text response.
type FailFunc func(w http.ResponseWriter, r *http.Request, status int, reason Failure)

// identifier is what each provider contributes to the middleware: who the
// request belongs to, if anyone. It may write headers (e.g. clear a stale
// cookie) but never a body.
type identifier interface {
	identify(w http.ResponseWriter, r *http.Request) (Identity, bool)
}

// Authenticator authenticates requests with the configured provider. Build
// it with New; it is safe for concurrent use.
type Authenticator struct {
	kind     Kind
	basePath string
	logger   *slog.Logger
	ident    identifier
	jar      *cookieJar // basic, oidc
	basic    *basicProvider
	limiter  *limiter // basic
	oidc     *oidcProvider
	proxy    *proxyProvider
}

// New builds the Authenticator for o.Provider. For oidc it performs
// discovery using ctx, so it fails at startup if the IdP is unreachable.
func New(ctx context.Context, o Options) (*Authenticator, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	base := strings.TrimRight(o.BasePath, "/")
	if base != "" && !strings.HasPrefix(base, "/") {
		return nil, fmt.Errorf("auth: base path %q must start with /", o.BasePath)
	}
	a := &Authenticator{kind: o.Provider, basePath: base, logger: logger}

	var err error
	switch o.Provider {
	case KindNone:
		a.ident = anonymous{}
		return a, nil
	case KindProxy:
		if a.proxy, err = newProxy(o.Proxy, logger); err != nil {
			return nil, fmt.Errorf("auth: %w", err)
		}
		if len(a.proxy.trusted) == 0 {
			logger.WarnContext(ctx, "auth: proxy provider has no trusted proxies; every request is unauthenticated")
		}
		a.ident = a.proxy
		return a, nil
	case KindBasic, KindOIDC:
	default:
		return nil, fmt.Errorf("auth: unknown provider %q (want none, basic, proxy or oidc)", o.Provider)
	}

	if a.jar, err = newCookieJar(o.Session, base, o.SessionGroups, now); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	if o.Provider == KindBasic {
		a.basic, err = newBasic(o.Basic)
		a.limiter = newLimiter(loginAttempts, loginWindow, now)
	} else {
		a.oidc, err = newOIDC(ctx, o.OIDC, logger, now)
	}
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	a.ident = sessionIdentifier{jar: a.jar, basic: a.basic}
	return a, nil
}

// Kind returns the configured provider, e.g. so the login page can choose
// between the form and the SSO button.
func (a *Authenticator) Kind() Kind { return a.kind }

// SignOutVisible reports whether the account menu offers Sign out: always
// for basic and oidc, for proxy only with a logout URL, never for none.
func (a *Authenticator) SignOutVisible() bool {
	switch a.kind {
	case KindBasic, KindOIDC:
		return true
	case KindProxy:
		return a.proxy.logoutURL != ""
	}
	return false
}

type anonymous struct{}

func (anonymous) identify(http.ResponseWriter, *http.Request) (Identity, bool) {
	return Identity{}, true
}

// sessionIdentifier reads the session cookie for basic and oidc, clearing
// it when it is invalid or expired. Basic sessions are re-checked against
// config on every request.
type sessionIdentifier struct {
	jar   *cookieJar
	basic *basicProvider // nil for oidc
}

func (s sessionIdentifier) identify(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	d, ok, present := s.jar.readSession(r)
	var id Identity
	if ok {
		if s.basic != nil {
			id, ok = s.basic.lookup(d.Name, d.Version)
		} else {
			id = Identity{Subject: d.Subject, Name: d.Name, Email: d.Email, Groups: d.Groups}
		}
	}
	if !ok && present {
		s.jar.clearSession(w)
	}
	return id, ok
}

// Middleware stores the request's Identity in its context when the request
// is authenticated; read it with IdentityFrom. It never rejects a request:
// route wrappers decide what needs a session and call Challenge. An invalid
// or expired session cookie is cleared. For none every request carries the
// zero Identity.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := a.ident.identify(w, r); ok {
			r = r.WithContext(WithIdentity(r.Context(), id))
		}
		next.ServeHTTP(w, r)
	})
}

// Challenge answers a request that needs an identity but has none. For
// basic and oidc it redirects to <base>/login?next=<this page> (303), or,
// for htmx requests, sets HX-Redirect so the whole page navigates instead
// of a fragment swap. For proxy and none it is a plain 401.
func (a *Authenticator) Challenge(w http.ResponseWriter, r *http.Request) {
	if a.jar == nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	target := a.basePath + "/login"
	next := a.basePath + "/"
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		// RequestURI is the original request target, unaffected by
		// http.StripPrefix, so it still carries the base path.
		next = a.safeNext(r.RequestURI)
	}
	if next != a.basePath+"/" {
		target += "?next=" + url.QueryEscape(next)
	}
	if r.Header.Get("HX-Request") == "true" {
		if cur := r.Header.Get("HX-Current-URL"); cur != "" {
			if u, err := url.Parse(cur); err == nil {
				if n := a.safeNext(u.RequestURI()); n != a.basePath+"/" {
					target = a.basePath + "/login?next=" + url.QueryEscape(n)
				}
			}
		}
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// LoginHandler serves POST /login. For basic it checks the "username" and
// "password" form fields (at most 5 attempts per client IP per minute),
// sets the session and redirects to "next". For oidc it starts the
// authorization code flow (the "Sign in with SSO" button posts here). Other
// providers get 404. fail may be nil, which writes a plain-text error.
func (a *Authenticator) LoginHandler(fail FailFunc) http.Handler {
	fail = failFunc(fail)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.basic == nil && a.oidc == nil {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil {
			fail(w, r, http.StatusBadRequest, FailBadRequest)
			return
		}
		next := a.safeNext(r.PostForm.Get("next"))

		if a.oidc != nil {
			if err := a.oidc.start(w, r, a.jar, next); err != nil {
				a.logger.ErrorContext(r.Context(), "oidc start failed", slog.Any("error", err))
				fail(w, r, http.StatusInternalServerError, FailSSO)
			}
			return
		}

		if ok, retry := a.limiter.allow(r.RemoteAddr); !ok {
			a.logger.WarnContext(r.Context(), "login rate limited", slog.String("remote_addr", r.RemoteAddr))
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Round(time.Second)/time.Second)+1))
			fail(w, r, http.StatusTooManyRequests, FailTooManyAttempts)
			return
		}
		username := r.PostForm.Get("username")
		id, version, ok := a.basic.verify(username, r.PostForm.Get("password"))
		if !ok {
			a.logger.WarnContext(r.Context(), "login failed", slog.String("username", username))
			fail(w, r, http.StatusUnauthorized, FailInvalidCredentials)
			return
		}
		if !a.startSession(w, r, id, version, fail) {
			return
		}
		http.Redirect(w, r, next, http.StatusSeeOther) //nolint:gosec // G710: next passed safeNext above.
	})
}

// CallbackHandler serves GET /auth/callback for oidc; other providers get
// 404. On success it sets the session and redirects to the path saved when
// the flow started. fail may be nil.
func (a *Authenticator) CallbackHandler(fail FailFunc) http.Handler {
	fail = failFunc(fail)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.oidc == nil {
			http.NotFound(w, r)
			return
		}
		id, next, err := a.oidc.callback(w, r, a.jar)
		if err != nil {
			status := http.StatusUnauthorized
			if ce, ok := errors.AsType[*callbackError](err); ok {
				status = ce.status
			}
			a.logger.WarnContext(r.Context(), "oidc callback failed", slog.Any("error", err))
			fail(w, r, status, FailSSO)
			return
		}
		if !a.startSession(w, r, id, "", fail) {
			return
		}
		http.Redirect(w, r, a.safeNext(next), http.StatusSeeOther) //nolint:gosec // G710: safeNext keeps it a local path.
	})
}

// LogoutHandler serves POST /logout. basic and oidc clear the session (a
// local logout only, also for oidc) and redirect to the login page. proxy
// redirects to its logout URL, or 404s when none is set; none 404s.
func (a *Authenticator) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case a.jar != nil:
			a.jar.clearSession(w)
			http.Redirect(w, r, a.basePath+"/login", http.StatusSeeOther)
		case a.proxy != nil && a.proxy.logoutURL != "":
			http.Redirect(w, r, a.proxy.logoutURL, http.StatusSeeOther)
		default:
			http.NotFound(w, r)
		}
	})
}

// startSession writes the session cookie, or reports the failure and
// returns false.
func (a *Authenticator) startSession(w http.ResponseWriter, r *http.Request, id Identity, version string, fail FailFunc) bool {
	err := a.jar.setSession(w, id, version)
	if err == nil {
		return true
	}
	a.logger.ErrorContext(r.Context(), "set session failed", slog.String("user", id.Name), slog.Any("error", err))
	reason := FailInternal
	if errors.Is(err, errSessionTooLarge) {
		reason = FailSessionTooLarge
	}
	fail(w, r, http.StatusInternalServerError, reason)
	return false
}

func failFunc(f FailFunc) FailFunc {
	if f != nil {
		return f
	}
	return func(w http.ResponseWriter, _ *http.Request, status int, _ Failure) {
		http.Error(w, http.StatusText(status), status)
	}
}

// safeNext returns next if it is a same-origin relative path under the
// base path, and the app root otherwise, so a login link cannot redirect
// off-site.
func (a *Authenticator) safeNext(next string) string {
	root := a.basePath + "/"
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.ContainsAny(next, "\\\x00\r\n\t") {
		return root
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil {
		return root
	}
	if a.basePath != "" && u.Path != a.basePath && !strings.HasPrefix(u.Path, root) {
		return root
	}
	return next
}
