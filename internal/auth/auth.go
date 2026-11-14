package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/config"
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

const (
	maxFormBytes = 64 << 10
	// maxNextBytes caps the return-to path; it travels in the flow cookie.
	maxNextBytes = 1024
)

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
	ipLimit  *limiter // basic
	usrLimit *limiter // basic
	oidc     *oidcProvider
	proxy    *proxyProvider
}

// New builds the Authenticator for cfg's auth.provider, from config that
// config.Load has validated. Cookies are scoped to server.base_path and
// redirects stay inside it. sessionGroups are the groups worth keeping in
// an OIDC session: those some authz binding references
// (authz.Policy.Groups); basic sessions store no groups, which come from
// config on every request. logger records sign-in failures; nil discards.
//
// For oidc it performs discovery using ctx, so it fails at startup if the
// IdP is unreachable. An *http.Client in ctx (oidc.ClientContext) talks
// to the IdP instead of the default one with a 10s timeout.
func New(ctx context.Context, cfg *config.Config, sessionGroups []string, logger *slog.Logger) (*Authenticator, error) {
	return newAuth(ctx, cfg, sessionGroups, logger, time.Now)
}

// newAuth is New with a clock.
func newAuth(ctx context.Context, cfg *config.Config, sessionGroups []string, logger *slog.Logger, now func() time.Time) (*Authenticator, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	c := cfg.Auth
	kind := Kind(c.Provider)
	base := cfg.Server.BasePath
	a := &Authenticator{kind: kind, basePath: base, logger: logger}

	var err error
	switch kind {
	case KindNone:
		a.ident = anonymous{}
		return a, nil
	case KindProxy:
		if a.proxy, err = newProxy(ctx, c.Proxy, logger, now); err != nil {
			return nil, fmt.Errorf("auth: %w", err)
		}
		a.ident = a.proxy
		return a, nil
	case KindBasic, KindOIDC:
	default:
		return nil, fmt.Errorf("auth: unknown provider %q", kind)
	}

	if a.jar, err = newCookieJar(kind, c.Session, base, sessionGroups, now); err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	if kind == KindBasic {
		a.basic, err = newBasic(c.Basic)
		a.ipLimit = newLimiter(loginAttempts, loginWindow, limiterCap, now)
		a.usrLimit = newLimiter(loginAttempts, loginWindow, limiterCap, now)
	} else {
		a.oidc, err = newOIDC(ctx, c.OIDC, logger, now)
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

// ProxyLogoutURL is auth.proxy.logout_url, or "" for other providers. The
// account menu links straight to it, so no form posts cross-origin.
func (a *Authenticator) ProxyLogoutURL() string {
	if a.kind != KindProxy {
		return ""
	}
	return a.proxy.logoutURL
}

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
// for htmx requests, sets HX-Redirect to the login page with the page the
// user is on (HX-Current-URL), so the whole page navigates instead of a
// fragment swap. For proxy and none it is a plain 401. Responses are never
// cached.
func (a *Authenticator) Challenge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch a.kind {
	case KindBasic, KindOIDC:
	default:
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	htmx := r.Header.Get("HX-Request") == "true"
	next := a.root()
	switch {
	case htmx:
		// The request URL is a fragment; return to the page shown.
		if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil && u.Path != "" {
			next = a.SafeNext(u.RequestURI())
		}
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		// RequestURI is the original request target, unaffected by
		// http.StripPrefix, so it still carries the base path.
		next = a.SafeNext(r.RequestURI)
	}
	target := a.basePath + "/login"
	if next != a.root() {
		target += "?" + url.Values{"next": {next}}.Encode()
	}
	if htmx {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// LoginHandler serves POST /login, the basic login form: it checks the
// "username" and "password" form fields (at most 5 attempts per minute
// per client IP and per username), sets the session and redirects to
// "next". Every other provider gets 404, oidc included: its flow starts
// only at SSOHandler (GET /login/sso). fail may be nil, which writes a
// plain-text error.
func (a *Authenticator) LoginHandler(fail FailFunc) http.Handler {
	fail = failFunc(fail)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.kind != KindBasic {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil {
			fail(w, r, http.StatusBadRequest, FailBadRequest)
			return
		}
		a.basicLogin(w, r, a.SafeNext(r.PostForm.Get("next")), fail)
	})
}

func (a *Authenticator) basicLogin(w http.ResponseWriter, r *http.Request, next string, fail FailFunc) {
	username := r.PostForm.Get("username")
	known := a.basic.exists(username)
	logUser := slog.Attr{}
	if known {
		// Unknown "usernames" are not logged: they are often passwords
		// typed into the wrong field.
		logUser = slog.String("username", username)
	}
	ok, retry := a.ipLimit.allow(clientKey(r.RemoteAddr))
	if ok {
		var r2 time.Duration
		ok, r2 = a.usrLimit.allow(authz.FoldUsername(username))
		retry = max(retry, r2)
	}
	if !ok {
		a.logger.WarnContext(r.Context(), "login rate limited", slog.String("remote_addr", r.RemoteAddr), logUser)
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Round(time.Second)/time.Second)+1))
		fail(w, r, http.StatusTooManyRequests, FailTooManyAttempts)
		return
	}
	id, version, ok := a.basic.verify(username, r.PostForm.Get("password"))
	if !ok {
		a.logger.WarnContext(r.Context(), "login failed", slog.String("remote_addr", r.RemoteAddr), logUser)
		fail(w, r, http.StatusUnauthorized, FailInvalidCredentials)
		return
	}
	if !a.startSession(w, r, id, version, fail) {
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther) //nolint:gosec // G710: next passed SafeNext.
}

// SSOHandler serves GET /login/sso for oidc: the "Sign in with SSO" link.
// It starts the authorization code flow and redirects to the IdP, returning
// afterwards to the ?next= path if it passes SafeNext. A GET is safe here:
// it only sets a fresh flow cookie, and the state, nonce and PKCE verifier
// in it bind the callback to this browser. Being a navigation rather than a
// form post, it is not subject to the CSP form-action directive, which would
// block a form that redirects to the IdP. Other providers get 404.
func (a *Authenticator) SSOHandler(fail FailFunc) http.Handler {
	fail = failFunc(fail)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.kind != KindOIDC {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if err := a.oidc.start(w, r, a.jar, a.SafeNext(r.URL.Query().Get("next"))); err != nil {
			a.logger.ErrorContext(r.Context(), "oidc start failed", slog.Any("error", err))
			fail(w, r, http.StatusInternalServerError, FailSSO)
		}
	})
}

// CallbackHandler serves GET /auth/callback for oidc; other providers get
// 404. On success it sets the session and redirects to the path saved when
// the flow started. fail may be nil.
func (a *Authenticator) CallbackHandler(fail FailFunc) http.Handler {
	fail = failFunc(fail)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.kind != KindOIDC {
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
		http.Redirect(w, r, a.SafeNext(next), http.StatusSeeOther) //nolint:gosec // G710: SafeNext keeps it a local path.
	})
}

// LogoutHandler serves POST /logout. basic and oidc clear the session (a
// local logout only, also for oidc) and redirect to the login page. proxy
// redirects to its logout URL, or 404s when none is set; none 404s.
func (a *Authenticator) LogoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case a.kind == KindBasic || a.kind == KindOIDC:
			a.jar.clearSession(w)
			http.Redirect(w, r, a.basePath+"/login", http.StatusSeeOther)
		case a.kind == KindProxy && a.proxy.logoutURL != "":
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

func (a *Authenticator) root() string { return a.basePath + "/" }

// SafeNext returns next, cleaned and re-serialized, if it is a same-origin
// relative path under the base path, and the app root otherwise, so a
// login link (or any other return-to parameter) cannot redirect off-site
// or escape the base path.
func (a *Authenticator) SafeNext(next string) string {
	root := a.root()
	if next == "" || len(next) > maxNextBytes || !strings.HasPrefix(next, "/") ||
		strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\x00\r\n\t") {
		return root
	}
	// Encoded dots, slashes and backslashes would let the path mean one
	// thing to this check and another to a browser or proxy.
	lower := strings.ToLower(next)
	for _, enc := range []string{"%2e", "%2f", "%5c"} {
		if strings.Contains(lower, enc) {
			return root
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.User != nil {
		return root
	}
	if strings.ContainsFunc(u.Path, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return root
	}
	clean := path.Clean(u.Path)
	if strings.HasSuffix(u.Path, "/") && clean != "/" {
		clean += "/"
	}
	if a.basePath != "" && clean != a.basePath && !strings.HasPrefix(clean, root) {
		return root
	}
	out := url.URL{Path: clean, RawQuery: u.RawQuery}
	s := out.String()
	if !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") {
		return root
	}
	return s
}
