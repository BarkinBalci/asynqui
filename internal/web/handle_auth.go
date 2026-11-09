package web

import (
	"net/http"

	"github.com/BarkinBalci/asynqui/internal/auth"
	"github.com/BarkinBalci/asynqui/internal/web/format"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// handleLogin serves GET /login: the username/password form for basic, the
// SSO button for oidc. Other providers have no login page (404).
func handleLogin(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.hasLogin() {
			http.NotFound(w, r)
			return
		}
		// next is checked again by the login handler before any redirect.
		d.renderLogin(w, r, http.StatusOK, r.URL.Query().Get("next"), pages.LoginOK)
	})
}

// loginFailed renders a failed sign-in on the login page; it is the
// auth.FailFunc of the login and callback handlers.
func (d *deps) loginFailed(w http.ResponseWriter, r *http.Request, status int, reason auth.Failure) {
	next := ""
	if r.PostForm != nil {
		next = r.PostForm.Get("next")
	}
	d.renderLogin(w, r, status, next, loginError(reason))
}

func (d *deps) renderLogin(w http.ResponseWriter, r *http.Request, status int, next string, e pages.LoginError) {
	d.render(w, r, status, pages.LoginPage(pages.Login{
		Doc:       d.doc(r),
		ActionURL: d.url("login"),
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
