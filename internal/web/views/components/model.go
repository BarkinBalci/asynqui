// Package components holds the templ pieces shared by pages.
package components

import (
	"time"

	"github.com/a-h/templ"
)

// Health is a connection's last health check.
type Health int

// Health states. HealthUnknown means the first check has not finished.
const (
	HealthUnknown Health = iota
	HealthUp
	HealthDown
)

// Role is the viewer's role on a connection, shown as a chip and in the
// account menu.
type Role int

// Roles in ascending order.
const (
	RoleNone Role = iota
	RoleViewer
	RoleOperator
	RoleAdmin
)

// Theme is the color theme choice.
type Theme int

// Themes. ThemeSystem follows prefers-color-scheme.
const (
	ThemeSystem Theme = iota
	ThemeLight
	ThemeDark
)

// Value is the attribute and cookie spelling of t.
func (t Theme) Value() string {
	switch t {
	case ThemeLight:
		return "light"
	case ThemeDark:
		return "dark"
	}
	return "system"
}

// Themes lists the theme choices in control order.
func Themes() []Theme { return []Theme{ThemeSystem, ThemeLight, ThemeDark} }

// Refresh is the auto-refresh interval choice.
type Refresh int

// Refresh choices. RefreshOff sends no polling requests.
const (
	RefreshOff Refresh = iota
	Refresh2s
	Refresh5s
	Refresh10s
)

// Value is the attribute and cookie spelling of r ("off", "2s", …).
func (r Refresh) Value() string {
	switch r {
	case Refresh2s:
		return "2s"
	case Refresh5s:
		return "5s"
	case Refresh10s:
		return "10s"
	}
	return "off"
}

// Refreshes lists the refresh choices in control order.
func Refreshes() []Refresh { return []Refresh{RefreshOff, Refresh2s, Refresh5s, Refresh10s} }

// Section is a top-level area of the app, used for the sidebar's active
// item and for breadcrumb labels.
type Section int

// Sections.
const (
	SectionNone Section = iota
	SectionOverview
	SectionQueues
	SectionServers
	SectionSchedulers
	SectionInfo
)

// Sidebar is the left navigation, identical on every page.
type Sidebar struct {
	OverviewURL string
	// Section is the active area; with a current connection it selects
	// the sub-nav item.
	Section Section
	Conns   []SidebarConn
	Footer  Footer
}

// SidebarConn is one visible connection.
type SidebarConn struct {
	Name   string
	Addr   string // host:port/db
	URL    string
	Health Health
	// Current expands the item with its sub-nav.
	Current bool
	Sub     SubNav
}

// SubNav holds the links of the current connection.
type SubNav struct {
	QueuesURL     string
	ServersURL    string
	SchedulersURL string
	InfoURL       string
	// Valkey labels the info page "Valkey info" instead of "Redis info".
	Valkey bool
}

// Footer is the sidebar's pinned footer.
type Footer struct {
	Version string // already formatted, e.g. "v0.1.0", "dev" or "dev f9b5ecf"
	// FullVersion is the unshortened build version for the title when
	// Version is the short "dev <commit>" form; "" otherwise.
	FullVersion string
	ReleaseURL  string
	SourceURL   string
	// UpdateAvailable shows the accent dot after the version.
	UpdateAvailable bool
}

// TopBar is the bar above the content.
type TopBar struct {
	Crumbs    []Crumb
	UpdatedAt time.Time
	Theme     Theme
	Refresh   Refresh
	// PrefsURL receives the theme and refresh forms without JS; Next is
	// where it redirects back to.
	PrefsURL string
	Next     string
	Account  Account
}

// Crumb is one breadcrumb. Text is data (a connection name, a short ID);
// when Section is set, the section's label is shown instead. The last crumb
// is the current page and is not a link.
type Crumb struct {
	Text    string
	Section Section
	// Valkey labels SectionInfo "Valkey info".
	Valkey bool
	// Label, when set, renders the crumb's copy (e.g. an error page title)
	// instead of Text or Section.
	Label templ.Component
	URL   string
	// Mono sets the crumb in the monospace face (IDs, hosts); Title is
	// its native tooltip, such as the full ID behind a short one.
	Mono  bool
	Title string
}

// Account is the account menu. It is hidden when Visible is false
// (auth.provider none).
type Account struct {
	Visible bool
	User    string
	Role    Role
	// SignOutURL is where Sign out goes; empty hides it. For basic and oidc
	// it is POST <base>/logout. With SignOutExternal (proxy) it is
	// logout_url, reached by a plain link: the CSP form-action directive
	// would block a form whose response redirects cross-origin.
	SignOutURL      string
	SignOutExternal bool
}

// ConnCard is one Overview card.
type ConnCard struct {
	Name   string
	Addr   string
	Role   Role
	Health Health
	// Error is the failure text of an unreachable connection.
	Error         string
	Queues        int64
	Pending       int64
	Active        int64
	Retry         int64
	Processed     int64
	Failed        int64
	OldestPending time.Duration
	OpenURL       string
	RecheckURL    string
}

// Element ids. Each kind of element has its own prefix, so ids cannot collide
// whatever the connection names. htmx refocuses the element with the same id
// after a swap, so every focusable element in a polled region needs a stable
// id.
const (
	UpdatedID     = "updated-at"
	UnreachableID = "overview-unreachable"
	SidebarListID = "sidebar-conns"
	ContentID     = "content"
)

// CardID is the id of a connection's Overview card.
func CardID(name string) string { return "card-" + name }

// CardOpenID is the id of a card's "Open →" link.
func CardOpenID(name string) string { return "open-" + name }

// CardRetryID is the id of a card's "Retry now" button.
func CardRetryID(name string) string { return "retry-" + name }

// NavConnID is the id of a connection's sidebar link.
func NavConnID(name string) string { return "nav-" + name }

// SubNavID is the id of a sub-nav link of the current connection. There is
// one current connection, so the section alone makes it unique.
func SubNavID(s Section) string {
	switch s {
	case SectionQueues:
		return "subnav-queues"
	case SectionServers:
		return "subnav-servers"
	case SectionSchedulers:
		return "subnav-schedulers"
	case SectionInfo:
		return "subnav-info"
	}
	return "subnav"
}
