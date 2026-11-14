package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BarkinBalci/asynqui/internal/auth"
	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/web/static"
	"github.com/BarkinBalci/asynqui/internal/web/views"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// Source and release links shown in the sidebar footer.
const (
	sourceURL   = "https://github.com/BarkinBalci/asynqui"
	releasesURL = sourceURL + "/releases"
)

// defaultSummaryBudget bounds how long the Overview waits for connection
// summaries. Cards that miss it render as "Checking"; their fetches go on
// in conn's cache, so the next poll usually has them.
const defaultSummaryBudget = 1500 * time.Millisecond

// UpdateSource reports a release newer than the running version: its tag,
// with ok true when one is known. The footer links to that tag's release
// notes under releasesURL.
type UpdateSource interface {
	Latest() (version string, ok bool)
}

// Deps are the dependencies of NewServer. Every field is required except
// Version, Updates, Now, SummaryBudget and PageBudget.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	Auth   *auth.Authenticator
	Policy *authz.Policy
	Conns  ConnectionSource
	Ready  *Readiness
	// Version is the build version shown in the sidebar and on the login
	// page. Empty means "dev".
	Version string
	// Updates drives the sidebar's update dot. Nil means no update check.
	Updates UpdateSource
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// SummaryBudget bounds the Overview's wait for summaries. Zero means
	// 1.5s.
	SummaryBudget time.Duration
	// PageBudget bounds a connection page's wait for its data. Zero means
	// 5s.
	PageBudget time.Duration
}

// deps is what handlers share: the validated Deps plus everything derived
// from them once at startup.
type deps struct {
	logger  *slog.Logger
	auth    *auth.Authenticator
	policy  *authz.Policy
	conns   ConnectionSource
	ready   *Readiness
	version string
	updates UpdateSource // nil without an update check
	now     func() time.Time
	budget  time.Duration
	// pageBudget bounds a connection page's wait for its data.
	pageBudget time.Duration
	base       string // "" or "/asynq"
	refresh    string // default refresh choice from config
	// secureCookies follows auth.session.cookie_secure for the preference
	// cookies too.
	secureCookies bool
	assets        *static.Assets
	urls          views.Assets
}

// NewServer returns the root handler with every route and the global
// middleware applied. It fails on an invalid trusted origin or a missing
// dependency.
func NewServer(d Deps) (http.Handler, error) {
	_, h, err := newServer(d)
	return h, err
}

// newServer is NewServer that also returns the handlers' shared deps, for
// tests.
func newServer(d Deps) (*deps, http.Handler, error) {
	if d.Logger == nil || d.Config == nil || d.Auth == nil || d.Policy == nil || d.Conns == nil || d.Ready == nil {
		return nil, nil, errors.New("web: missing dependency")
	}
	assets, err := static.Embedded()
	if err != nil {
		return nil, nil, fmt.Errorf("web: %w", err)
	}
	cop := http.NewCrossOriginProtection()
	for _, o := range d.Config.Server.TrustedOrigins {
		if err := cop.AddTrustedOrigin(o); err != nil {
			return nil, nil, fmt.Errorf("web: server.trusted_origins: %w", err)
		}
	}

	dd := &deps{
		logger:        d.Logger,
		auth:          d.Auth,
		policy:        d.Policy,
		conns:         d.Conns,
		ready:         d.Ready,
		version:       d.Version,
		updates:       d.Updates,
		now:           d.Now,
		budget:        d.SummaryBudget,
		pageBudget:    d.PageBudget,
		base:          strings.TrimRight(d.Config.Server.BasePath, "/"),
		refresh:       d.Config.Refresh.Default,
		secureCookies: d.Config.Auth.Session.CookieSecure,
		assets:        assets,
	}
	if dd.version == "" {
		dd.version = "dev"
	}
	if dd.now == nil {
		dd.now = time.Now
	}
	if dd.budget <= 0 {
		dd.budget = defaultSummaryBudget
	}
	if dd.pageBudget <= 0 {
		dd.pageBudget = defaultPageBudget
	}
	dd.urls = views.Assets{
		CSS:  dd.static("app.css"),
		JS:   dd.static("app.js"),
		HTMX: dd.static("vendor/htmx.min.js"),
		Icon: dd.static("logo.svg"),
	}

	app := http.NewServeMux()
	addRoutes(app, dd)

	// The app lives under base_path; anything else is 404.
	h := d.Auth.Middleware(app)
	if dd.base != "" {
		outer := http.NewServeMux()
		outer.Handle(dd.base+"/", http.StripPrefix(dd.base, h))
		h = outer
	}
	h = cop.Handler(h)
	h = securityHeaders(h)
	h = recoverPanics(d.Logger, dd.internalError, h)
	h = logRequests(d.Logger, dd.base, h)
	return dd, h, nil
}

// url builds every link, redirect and form action: base_path plus each
// segment escaped with url.PathEscape. url() is the app root.
func (d *deps) url(segments ...string) string {
	var b strings.Builder
	b.WriteString(d.base)
	for _, s := range segments {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(s))
	}
	if len(segments) == 0 {
		b.WriteByte('/')
	}
	return b.String()
}

// urlQuery is url(segments...) with a query string; empty values are
// dropped, and no query is added when nothing is left.
func (d *deps) urlQuery(q url.Values, segments ...string) string {
	for k, vs := range q {
		if len(vs) == 0 || (len(vs) == 1 && vs[0] == "") {
			delete(q, k)
		}
	}
	if len(q) == 0 {
		return d.url(segments...)
	}
	return d.url(segments...) + "?" + q.Encode()
}

// static returns the hashed URL of a static file such as "vendor/htmx.min.js".
func (d *deps) static(name string) string {
	return d.url(append([]string{"static"}, strings.Split(d.assets.Path(name), "/")...)...)
}

// cookiePath scopes cookies to base_path, or "/" without one.
func (d *deps) cookiePath() string {
	if d.base == "" {
		return "/"
	}
	return d.base
}

// internalError answers a panic or a failed render with the standalone
// error page (the shell itself may be what failed).
func (d *deps) internalError(w http.ResponseWriter, r *http.Request) {
	d.errorDocument(w, r, http.StatusInternalServerError, pages.ErrorInternal)
}
