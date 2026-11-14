package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/a-h/templ"

	"github.com/BarkinBalci/asynqui/internal/auth"
	"github.com/BarkinBalci/asynqui/internal/authz"
	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web/format"
	"github.com/BarkinBalci/asynqui/internal/web/views"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// isFragment reports whether the request wants only the page's data region:
// an htmx request (polling, sorting, paging), but not an htmx history
// restore, which needs the full page.
func isFragment(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"
}

// render writes c as an HTML response. It renders into a buffer first, so a
// template error becomes a clean error page instead of a truncated one.
func (d *deps) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	body, err := d.renderBytes(r, c)
	if err != nil {
		d.internalError(w, r)
		return
	}
	writeHTML(w, status, body)
}

func (d *deps) renderBytes(r *http.Request, c templ.Component) ([]byte, error) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		d.logger.ErrorContext(r.Context(), "render", slog.String("path", r.URL.Path), slog.Any("error", err))
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeHTML(w http.ResponseWriter, status int, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", "HX-Request")
	w.WriteHeader(status)
	_, _ = w.Write(body) //nolint:gosec // G705: body is templ output, escaped by templ.
}

// plainError is a plain-text error that is never cached.
func plainError(w http.ResponseWriter, status int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(status), status)
}

// renderError answers with an error page: inside the app shell for a
// signed-in GET, as plain text for htmx and non-GET requests (a fragment
// swap must not receive a whole page), and as the standalone error page
// without an identity, so the sidebar never lists connections to strangers.
func (d *deps) renderError(w http.ResponseWriter, r *http.Request, status int, kind pages.ErrorKind) {
	if isFragment(r) || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		plainError(w, status)
		return
	}
	id, ok := auth.IdentityFrom(r.Context())
	if !ok {
		d.errorDocument(w, r, status, kind)
		return
	}
	a := accessFrom(r.Context())
	a.id = id
	crumb := components.Crumb{Label: pages.ErrorTitle(kind)}
	d.render(w, r, status, pages.ErrorPage(pages.Error{
		Layout: d.layout(r, a, components.SectionNone, []components.Crumb{crumb}),
		Kind:   kind,
	}))
}

// errorDocument writes the standalone error page. If even that fails to
// render, the answer is plain text.
func (d *deps) errorDocument(w http.ResponseWriter, r *http.Request, status int, kind pages.ErrorKind) {
	if isFragment(r) || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		plainError(w, status)
		return
	}
	body, err := d.renderBytes(r, pages.ErrorDocument(d.doc(r), kind, d.url()))
	if err != nil {
		plainError(w, status)
		return
	}
	writeHTML(w, status, body)
}

// handleNotFound is the catch-all route.
func handleNotFound(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.renderError(w, r, http.StatusNotFound, pages.ErrorNotFound)
	})
}

// doc builds the document-level view model: theme, refresh, assets.
func (d *deps) doc(r *http.Request) views.Doc {
	return views.Doc{
		Theme:      themeFrom(r),
		Refresh:    d.refreshFrom(r),
		CookiePath: d.cookiePath(),
		Assets:     d.urls,
	}
}

// layout builds the app shell for a page in section. crumbs after
// "Overview" are passed in; the Overview crumb is always first.
func (d *deps) layout(r *http.Request, a access, section components.Section, crumbs []components.Crumb) views.Layout {
	return d.layoutWith(r, a, section, crumbs, d.visible(a.id), nil)
}

// layoutWith is layout with the visible connections given, and health
// overrides for the sidebar (the Overview passes what its cards found, so
// a connection whose Summary failed shows down in both places).
func (d *deps) layoutWith(r *http.Request, a access, section components.Section, crumbs []components.Crumb,
	visible []visibleConn, health map[string]components.Health) views.Layout {
	side := components.Sidebar{
		OverviewURL: d.url(),
		Section:     section,
		Conns:       make([]components.SidebarConn, 0, len(visible)),
		Footer:      d.footer(),
	}
	names := make([]string, 0, len(visible))
	for _, c := range visible {
		name := c.Name()
		names = append(names, name)
		h, ok := health[name]
		if !ok {
			h = healthView(c.Health().State)
		}
		sc := components.SidebarConn{
			Name:   name,
			Addr:   c.Addr(),
			URL:    d.url("c", name),
			Health: h,
		}
		if a.conn != nil && name == a.conn.Name() {
			sc.Current = true
			sc.Sub = components.SubNav{
				QueuesURL:     d.url("c", name),
				ServersURL:    d.url("c", name, "servers"),
				SchedulersURL: d.url("c", name, "schedulers"),
				InfoURL:       d.url("c", name, "info"),
				Valkey:        isValkey(c),
			}
		}
		side.Conns = append(side.Conns, sc)
	}

	// The account menu shows the role on the current connection, or the
	// highest role on the Overview and other pages without one.
	role := a.role
	if a.conn == nil {
		role = d.policy.MaxRole(subject(a.id), names)
	}
	all := append([]components.Crumb{{Section: components.SectionOverview, URL: d.url()}}, crumbs...)
	return views.Layout{
		Doc:     d.doc(r),
		Sidebar: side,
		TopBar: components.TopBar{
			Crumbs:    all,
			UpdatedAt: d.now(),
			Theme:     themeFrom(r),
			Refresh:   d.refreshFrom(r),
			PrefsURL:  d.url("prefs"),
			Next:      d.base + r.URL.RequestURI(),
			Account:   d.account(a.id, role),
		},
	}
}

func (d *deps) account(id auth.Identity, role authz.Role) components.Account {
	a := components.Account{Visible: true, User: id.Name, Role: roleView(role)}
	switch d.auth.Kind() {
	case auth.KindNone:
		return components.Account{}
	case auth.KindProxy:
		a.SignOutURL, a.SignOutExternal = d.auth.ProxyLogoutURL(), true
	case auth.KindBasic, auth.KindOIDC:
		a.SignOutURL = d.url("logout")
	}
	return a
}

func (d *deps) footer() components.Footer {
	v := format.Version(d.version)
	release := releasesURL
	if strings.HasPrefix(v, "v") && !strings.ContainsAny(v, "-+") {
		release = releasesURL + "/tag/" + v
	}
	f := components.Footer{Version: v, ReleaseURL: release, SourceURL: sourceURL}
	if format.IsShortVersion(d.version) {
		f.FullVersion = d.version
	}
	if d.updates != nil {
		if tag, ok := d.updates.Latest(); ok {
			f.UpdateAvailable, f.ReleaseURL = true, releasesURL+"/tag/"+url.PathEscape(tag)
		}
	}
	return f
}

func healthView(s conn.State) components.Health {
	switch s {
	case conn.StateUp:
		return components.HealthUp
	case conn.StateDown:
		return components.HealthDown
	}
	return components.HealthUnknown
}

func roleView(r authz.Role) components.Role {
	switch r {
	case authz.RoleViewer:
		return components.RoleViewer
	case authz.RoleOperator:
		return components.RoleOperator
	case authz.RoleAdmin:
		return components.RoleAdmin
	}
	return components.RoleNone
}
