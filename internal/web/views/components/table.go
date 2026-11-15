package components

import (
	"encoding/hex"
	"strconv"
	"time"
)

// Tone is a badge's color family; the copy comes from the caller.
type Tone int

// Tones, as in the style guide's status badges.
const (
	ToneNeutral Tone = iota
	ToneOK
	ToneWarn
	ToneDanger
	ToneInfo
	ToneTeal
	ToneViolet
	ToneOrange
)

// SortDir is a sortable column's state.
type SortDir int

// Sort states. SortNone is a column the table is not sorted by.
const (
	SortNone SortDir = iota
	SortAsc
	SortDesc
)

// SortHead is one sortable column header. Its link applies the column's sort,
// toggling the direction when it is already the sort column, and keeps the
// page's other query parameters.
type SortHead struct {
	// ID is the link's id: it is focusable inside a polled region.
	ID  string
	URL string
	Dir SortDir
	// Num right-aligns a numeric column.
	Num bool
}

// RowSelect is a row's selection checkbox. It belongs to the form with id Form
// by the form attribute and submits Name=Value when checked. It carries
// hx-preserve, so its checked state survives a swap as long as the new table
// has a checkbox with the same ID.
type RowSelect struct {
	ID    string
	Form  string
	Name  string
	Value string
	// Fresh leaves out hx-preserve, so the swap replaces the checkbox
	// with an unchecked one: for the answer to an action, after which the
	// selection clears.
	Fresh bool
}

// Enable is when a selection toolbar button is enabled, given the
// selected rows' data-state values or flags. app.js applies it.
type Enable int

// Enable rules.
const (
	// EnableAny enables the button when any row is selected.
	EnableAny Enable = iota
	// EnableState enables it when any selected row has data-state equal
	// to Action.State.
	EnableState
	// EnableEvery enables it when every selected row carries the
	// data-{Action.Flag} attribute. While a selected row lacks it the
	// button is disabled with the native title Action.Blocked.
	EnableEvery
)

// Action is a selection toolbar button that posts the selected rows.
type Action struct {
	ID string
	// URL is the POST target (formaction, and hx-post unless Confirm).
	URL string
	// Form is the id of the form holding the selection.
	Form string
	// Allowed is false when the viewer's role is too low: the button is
	// rendered disabled with a "Requires {Need}" title and never enabled.
	Allowed bool
	Need    Role
	Enable  Enable
	// State is the data-state a selected row needs for EnableState.
	State string
	// Flag is the attribute (data-{Flag}) every selected row needs for
	// EnableEvery, and Blocked the title while one lacks it. Blocked is
	// copy: set it in templ.
	Flag    string
	Blocked string
	Danger  bool
	// Primary is the accent button; Large the header-action size.
	Primary bool
	Large   bool
	// Confirm is the id of the ConfirmDialog that asks first. Without JS
	// the button posts directly and the server answers with a
	// confirmation page.
	Confirm string
	// ConfirmAll is the id of the ConfirmDialog app.js opens instead while the
	// selection is "all N" of a state.
	ConfirmAll string
	// Conn, when set, names the connection in the disabled button's title
	// ("Requires admin on main").
	Conn string
}

// Dialog is a confirmation dialog, opened by app.js from an Action with
// Confirm set to its ID. Its confirm button submits the same form with
// confirm=1.
type Dialog struct {
	ID string
	// ConfirmURL and Form are what the confirm button posts.
	ConfirmURL string
	Form       string
	Danger     bool
}

// TitleID is the id of the dialog's heading.
func (d Dialog) TitleID() string { return d.ID + "-title" }

// ConfirmID is the id of the dialog's confirm button.
func (d Dialog) ConfirmID() string { return d.ID + "-confirm" }

// Chart is a daily two-series line chart (processed on the left axis,
// failed on the right), with geometry precomputed by package chart in a
// ViewW × ViewH box stretched to the plot's width.
type Chart struct {
	// ID prefixes the ids of the chart's title, description and table.
	ID           string
	ViewW, ViewH string
	Days         []ChartDay
	// ProcessedTicks and FailedTicks are the axis values, bottom to top,
	// at fixed fractions of the height.
	ProcessedTicks []int64
	FailedTicks    []int64
	// GridY are the y of the grid lines above the baseline.
	GridY         []string
	ProcessedLine string
	ProcessedArea string
	FailedLine    string
	// Peak is the index of the day with the most failures, -1 when none
	// failed.
	Peak int
}

// ChartDay is one point of a Chart.
type ChartDay struct {
	Date      time.Time
	Processed int64
	Failed    int64
	Today     bool
	// Label shows the date under the x axis.
	Label bool
	// BandX and BandW are the day's hover band.
	BandX, BandW string
}

// Element ids of the Connection page and its shared regions.
const (
	ToastsID      = "toasts"
	ToastFailedID = "toast-request-failed"
)

// HexID is prefix plus the hex encoding of s: a valid, collision-free id
// (and CSS id selector) for any string, such as a queue name with spaces
// or slashes.
func HexID(prefix, s string) string { return prefix + hex.EncodeToString([]byte(s)) }

// colspan is an attribute value, not display text.
func colspan(n int) string { return strconv.Itoa(n) }
