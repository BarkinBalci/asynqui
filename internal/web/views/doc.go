// Package views holds the page shell, with shared pieces in views/components
// and one file per page in views/pages.
package views

import (
	"github.com/a-h/templ"

	"github.com/BarkinBalci/asynqui/internal/web/views/components"
)

// Doc is what every HTML document needs: theme, refresh and asset URLs.
type Doc struct {
	Theme   components.Theme
	Refresh components.Refresh
	// CookiePath scopes the preference cookies app.js writes: base_path, or
	// "/" without one.
	CookiePath string
	Assets     Assets
}

// Assets are the content-hashed URLs of the static files.
type Assets struct {
	CSS  string
	JS   string
	HTMX string
	Icon string
}

// Layout is the app shell around a page: sidebar and top bar.
type Layout struct {
	Doc     Doc
	Sidebar components.Sidebar
	TopBar  components.TopBar
	// Toasts are shown when the page loads, such as the errors of an
	// action posted without JS. Nil shows none.
	Toasts templ.Component
}
