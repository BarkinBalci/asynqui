package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/BarkinBalci/asynqui/internal/auth"
	"github.com/BarkinBalci/asynqui/internal/authz"
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
// template error becomes a clean 500 instead of a truncated page.
func (d *deps) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		d.logger.ErrorContext(r.Context(), "render", slog.String("path", r.URL.Path), slog.Any("error", err))
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", "HX-Request")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// renderError answers with an error page inside the app shell, or with
// plain text for htmx and non-GET requests.
func (d *deps) renderError(w http.ResponseWriter, r *http.Request, status int, kind pages.ErrorKind, a access) {
	if isFragment(r) || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.Error(w, http.StatusText(status), status)
		return
	}
	d.render(w, r, status, pages.ErrorPage(pages.Error{
		Layout: d.layout(r, a, components.SectionNone, nil),
		Kind:   kind,
	}))
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
	visible := d.visible(a.id)
	side := components.Sidebar{
		OverviewURL: d.url(),
		Section:     section,
		Conns:       make([]components.SidebarConn, 0, len(visible)),
		Footer:      d.footer(),
	}
	names := make([]string, 0, len(visible))
	for _, c := range visible {
		names = append(names, c.Name)
		sc := components.SidebarConn{
			Name:   c.Name,
			Addr:   c.Addr,
			URL:    d.url("c", c.Name),
			Health: healthView(c.Health.State),
		}
		if c.Name == a.conn.Name {
			sc.Current = true
			sc.Sub = components.SubNav{
				QueuesURL:     d.url("c", c.Name),
				ServersURL:    d.url("c", c.Name, "servers"),
				SchedulersURL: d.url("c", c.Name, "schedulers"),
				InfoURL:       d.url("c", c.Name, "info"),
			}
		}
		side.Conns = append(side.Conns, sc)
	}

	// The account menu shows the role on the current connection, or the
	// highest role on the Overview and other pages without one.
	role := a.role
	if a.conn.Name == "" {
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
	if d.auth.Kind() == auth.KindNone {
		return components.Account{}
	}
	return components.Account{
		Visible:   true,
		User:      id.Name,
		Role:      roleView(role),
		SignOut:   d.auth.SignOutVisible(),
		LogoutURL: d.url("logout"),
	}
}

func (d *deps) footer() components.Footer {
	v := format.Version(d.version)
	release := releasesURL
	if strings.HasPrefix(v, "v") && !strings.ContainsAny(v, "-+") {
		release = releasesURL + "/tag/" + v
	}
	return components.Footer{Version: v, ReleaseURL: release, SourceURL: sourceURL}
}

func healthView(s HealthState) components.Health {
	switch s {
	case HealthUp:
		return components.HealthUp
	case HealthDown:
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
