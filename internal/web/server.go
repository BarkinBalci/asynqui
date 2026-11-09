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
)

// Source and release links shown in the sidebar footer.
const (
	sourceURL   = "https://github.com/BarkinBalci/asynqui"
	releasesURL = sourceURL + "/releases"
)

// Deps are the dependencies of NewServer. Every field is required except
// Now and Version.
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
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
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
	now     func() time.Time
	base    string // "" or "/asynq"
	refresh string // default refresh choice from config
	assets  *static.Assets
	urls    views.Assets
}

// NewServer returns the root handler with every route and the global
// middleware applied. It fails on an invalid trusted origin or a missing
// dependency.
func NewServer(d Deps) (http.Handler, error) {
	if d.Logger == nil || d.Config == nil || d.Auth == nil || d.Policy == nil || d.Conns == nil || d.Ready == nil {
		return nil, errors.New("web: missing dependency")
	}
	assets, err := static.Embedded()
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}
	cop := http.NewCrossOriginProtection()
	for _, o := range d.Config.Server.TrustedOrigins {
		if err := cop.AddTrustedOrigin(o); err != nil {
			return nil, fmt.Errorf("web: server.trusted_origins: %w", err)
		}
	}

	dd := &deps{
		logger:  d.Logger,
		auth:    d.Auth,
		policy:  d.Policy,
		conns:   d.Conns,
		ready:   d.Ready,
		version: d.Version,
		now:     d.Now,
		base:    strings.TrimRight(d.Config.Server.BasePath, "/"),
		refresh: d.Config.Refresh.Default,
		assets:  assets,
	}
	if dd.version == "" {
		dd.version = "dev"
	}
	if dd.now == nil {
		dd.now = time.Now
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
	h = recoverPanics(d.Logger, h)
	h = logRequests(d.Logger, dd.base, h)
	return h, nil
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
