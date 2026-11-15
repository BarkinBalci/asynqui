package web

import (
	"net/http"
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

// handlePrefs is the no-JS fallback of the theme and refresh controls: a form
// post sets the cookie and redirects back to next (through SafeNext).
// Preferences are not sensitive, so no identity is needed. Unknown values are
// ignored.
func handlePrefs(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		if err := r.ParseForm(); err != nil {
			plainError(w, http.StatusBadRequest)
			return
		}
		if t, ok := parseTheme(r.PostForm.Get("theme")); ok {
			d.setPref(w, themeCookie, t.Value())
		}
		if v, ok := parseRefresh(r.PostForm.Get("refresh")); ok {
			d.setPref(w, refreshCookie, v.Value())
		}
		http.Redirect(w, r, d.auth.SafeNext(r.PostForm.Get("next")), http.StatusSeeOther) //nolint:gosec // G710: SafeNext keeps it under base_path.
	})
}

// setPref writes a preference cookie, readable by app.js, which writes the
// same cookies. Secure follows auth.session.cookie_secure.
func (d *deps) setPref(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: preference cookie, not a credential; HttpOnly would hide it from app.js.
		Name:     name,
		Value:    value,
		Path:     d.cookiePath(),
		MaxAge:   int(prefsMaxAge / time.Second),
		Secure:   d.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}
