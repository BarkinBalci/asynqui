package pages

import (
	"time"

	"github.com/BarkinBalci/asynqui/internal/web/views"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
)

// QueueColumn is a column of the queues table, in display order after the
// checkbox.
type QueueColumn int

// Queue columns. Every one sorts.
const (
	ColName QueueColumn = iota
	ColState
	ColSize
	ColPending
	ColActive
	ColScheduled
	ColRetry
	ColArchived
	ColLatency
	ColMemory
	ColProcessed
	ColFailed
	NumQueueColumns
)

// DataState is whether a connection page has its data.
type DataState int

// Data states.
const (
	// DataReady: the data was read.
	DataReady DataState = iota
	// DataFailed: the connection is down, or the read failed; Error says
	// why.
	DataFailed
	// DataChecking: the read did not finish within the page budget; the
	// next poll will likely have it.
	DataChecking
)

// Element ids of the Connection page. Polls replace the regions by id;
// focusable elements inside them have stable ids too (sort-, qsel-,
// qlink-, range- prefixes).
const (
	// QueuesPageID wraps the page: it is the hx-sync scope of every
	// request the page makes.
	QueuesPageID    = "queues-page"
	QueuesPollID    = "queues-poll"
	ConnHealthID    = "conn-health"
	ConnStateID     = "conn-state"
	ChartBodyID     = "tp-chart"
	RangeFormID     = "tp-range"
	QueuesTableID   = "queues-table"
	QueuesRowsID    = "queues-rows"
	QueuesFormID    = "queues-form"
	QueuesFilterID  = "queues-filter"
	QueuesSelectAll = "qsel-all"
	DeleteDialogID  = "queues-delete"
	ThroughputID    = "throughput"
	ChartID         = "tp"
	PickerID        = "tp-queue"
	PickerFormID    = "tp-form"
	PickerApplyID   = "tp-apply"
)

// Queues is the Connection page: the queues table and the throughput
// chart.
type Queues struct {
	Layout views.Layout
	Addr   string
	Health components.Health
	// Live is the polling frame. A poll leaves the chart's controls alone
	// (an open select stays open) and swaps only the chart body.
	Live  Live
	Rows  []QueueRow
	Total QueueRow
	Heads [NumQueueColumns]components.SortHead
	// Next is the page's own URL (path and query), posted with actions so
	// the answer keeps the sort and chart state.
	Next string
	// FormURL is the selection form's default action.
	FormURL      string
	Pause        components.Action
	Resume       components.Action
	Delete       components.Action
	DeleteDialog components.Dialog
	Throughput   Throughput
}

// QueueRow is one queue in the table, or the Total row.
type QueueRow struct {
	Name   string
	URL    string
	LinkID string
	Select components.RowSelect
	Paused bool

	Size, Pending, Active, Scheduled, Retry, Archived int64
	Processed, Failed                                 int64
	Memory                                            int64
	Latency                                           time.Duration
	// LatencyHigh highlights a latency over the warning threshold.
	LatencyHigh bool
}

// Throughput is the chart card.
type Throughput struct {
	// Show is false when the page has no data: the card is left out.
	Show bool
	// State is the history's: DataChecking when it missed the page
	// budget, DataFailed (with Error) when it could not be read.
	State  DataState
	Error  string
	Ranges []RangeButton
	// Queue is the picked queue; empty is all queues.
	Queue  string
	Queues []string
	// FormURL is the page URL both controls submit to (GET). RangeHidden
	// and PickerHidden carry the rest of the state for each.
	FormURL      string
	RangeHidden  []Param
	PickerHidden []Param
	Chart        components.Chart
}

// RangeButton is one of the 7d/14d/30d/90d buttons.
type RangeButton struct {
	ID    string
	Days  int
	Value string // the ?range= value
	On    bool
}

// Param is a hidden form field.
type Param struct {
	Name, Value string
}

// QueueVerb is a queue action.
type QueueVerb int

// Queue actions.
const (
	VerbPause QueueVerb = iota
	VerbResume
	VerbDelete
)

// FailureKind is why an action failed on a queue.
type FailureKind int

// Failure kinds. FailureOther shows the error text.
const (
	FailureOther FailureKind = iota
	FailureNotFound
	FailureNotEmpty
)

// ActionFailure is an action that failed on one queue.
type ActionFailure struct {
	Verb  QueueVerb
	Queue string
	Kind  FailureKind
	// Detail is the error text for FailureOther.
	Detail string
}
