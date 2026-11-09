package web

import (
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/BarkinBalci/asynqui/internal/web/views/components"
)

// Preference cookies. app.js writes the same cookies when JS is on; the
// server only reads them, except for the no-JS form fallback below.
const (
	themeCookie   = "asynqui_theme"
	refreshCookie = "asynqui_refresh"
	prefsMaxAge   = 365 * 24 * time.Hour
)

func themeFrom(r *http.Request) components.Theme {
	c, err := r.Cookie(themeCookie)
	if err != nil {
		return components.ThemeSystem
	}
	if t, ok := parseTheme(c.Value); ok {
		return t
	}
	return components.ThemeSystem
}

func parseTheme(s string) (components.Theme, bool) {
	for _, t := range components.Themes() {
		if t.Value() == s {
			return t, true
		}
	}
	return components.ThemeSystem, false
}

// refreshFrom reads the refresh cookie, falling back to refresh.default.
func (d *deps) refreshFrom(r *http.Request) components.Refresh {
	if c, err := r.Cookie(refreshCookie); err == nil {
		if v, ok := parseRefresh(c.Value); ok {
			return v
		}
	}
	v, _ := parseRefresh(d.refresh)
	return v
}

func parseRefresh(s string) (components.Refresh, bool) {
	for _, v := range components.Refreshes() {
		if v.Value() == s {
			return v, true
		}
	}
	return components.RefreshOff, false
}

// handlePrefs is the no-JS fallback of the theme and refresh controls: a
// form post sets the cookie and redirects back. Preferences are not
// sensitive, so no identity is needed. Unknown values are ignored.
func handlePrefs(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		if t, ok := parseTheme(r.PostForm.Get("theme")); ok {
			d.setPref(w, themeCookie, t.Value())
		}
		if v, ok := parseRefresh(r.PostForm.Get("refresh")); ok {
			d.setPref(w, refreshCookie, v.Value())
		}
		http.Redirect(w, r, d.localPath(r.PostForm.Get("next")), http.StatusSeeOther) //nolint:gosec // G710: localPath keeps it under base_path.
	})
}

func (d *deps) setPref(w http.ResponseWriter, name, value string) {
	// Readable by app.js on purpose; it holds no secret.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: preference cookie, not a credential.
		Name:     name,
		Value:    value,
		Path:     d.cookiePath(),
		MaxAge:   int(prefsMaxAge / time.Second),
		SameSite: http.SameSiteLaxMode,
	})
}

// localPath returns next if it is a local path under base_path, else the
// app root, so the redirect can never leave the app.
func (d *deps) localPath(next string) string {
	root := d.url()
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.ContainsAny(next, "\\\x00\r\n\t") {
		return root
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return root
	}
	if clean := path.Clean(u.Path); clean != u.Path && clean+"/" != u.Path {
		return root // dot segments
	}
	if d.base != "" && u.Path != d.base && !strings.HasPrefix(u.Path, d.base+"/") {
		return root
	}
	return (&url.URL{Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery}).String()
}
