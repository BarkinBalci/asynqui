package web

import (
	"net/http"
	"net/url"

	"github.com/BarkinBalci/asynqui/internal/auth"
	"github.com/BarkinBalci/asynqui/internal/web/format"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// handleLogin serves GET /login: the username/password form for basic, the
// "Sign in with SSO" link for oidc. Other providers have no login page
// (404). A user who is already signed in goes straight to next.
func handleLogin(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.hasLogin() {
			d.renderError(w, r, http.StatusNotFound, pages.ErrorNotFound)
			return
		}
		next := r.URL.Query().Get("next")
		if _, ok := auth.IdentityFrom(r.Context()); ok {
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, d.auth.SafeNext(next), http.StatusSeeOther) //nolint:gosec // G710: SafeNext keeps it under base_path.
			return
		}
		// next is checked again by SafeNext before any redirect.
		d.renderLogin(w, r, http.StatusOK, next, pages.LoginOK)
	})
}

// loginFailed renders a failed sign-in on the login page; it is the
// auth.FailFunc of the login, SSO and callback handlers.
func (d *deps) loginFailed(w http.ResponseWriter, r *http.Request, status int, reason auth.Failure) {
	next := ""
	if r.PostForm != nil {
		next = r.PostForm.Get("next")
	} else if r.URL.Path == "/login/sso" {
		next = r.URL.Query().Get("next")
	}
	d.renderLogin(w, r, status, next, loginError(reason))
}

func (d *deps) renderLogin(w http.ResponseWriter, r *http.Request, status int, next string, e pages.LoginError) {
	d.render(w, r, status, pages.LoginPage(pages.Login{
		Doc:       d.doc(r),
		ActionURL: d.url("login"),
		SSOURL:    d.urlQuery(url.Values{"next": {next}}, "login", "sso"),
		Next:      next,
		Basic:     d.auth.Kind() == auth.KindBasic,
		SSO:       d.auth.Kind() == auth.KindOIDC,
		Error:     e,
		Version:   format.Version(d.version),
	}))
}

func (d *deps) hasLogin() bool {
	k := d.auth.Kind()
	return k == auth.KindBasic || k == auth.KindOIDC
}

func loginError(f auth.Failure) pages.LoginError {
	switch f {
	case auth.FailBadRequest:
		return pages.LoginBadRequest
	case auth.FailInvalidCredentials:
		return pages.LoginInvalidCredentials
	case auth.FailTooManyAttempts:
		return pages.LoginTooManyAttempts
	case auth.FailSSO:
		return pages.LoginSSOFailed
	case auth.FailSessionTooLarge:
		return pages.LoginSessionTooLarge
	}
	return pages.LoginInternal
}
