// Package components holds the templ pieces shared by pages: sidebar, top
// bar, badges, icons, times and the connection card. They render plain
// view-model structs and map enums to English copy; Go code never builds
// UI text.
package components

import "time"

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
	Version    string // already formatted, e.g. "v0.1.0" or "dev"
	ReleaseURL string
	SourceURL  string
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
	URL    string
}

// Account is the account menu. It is hidden when Visible is false
// (auth.provider none).
type Account struct {
	Visible bool
	User    string
	Role    Role
	// SignOut shows the Sign out item, posting to LogoutURL.
	SignOut   bool
	LogoutURL string
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

// ElementID is the card's HTML id, the target of "Retry now". Connection
// names are restricted to [a-z0-9._-], so they are valid in an id.
func (c ConnCard) ElementID() string { return "conn-" + c.Name }
