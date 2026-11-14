package pages

import (
	"time"

	"github.com/a-h/templ"

	"github.com/BarkinBalci/asynqui/internal/web/views"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
)

// TaskState is a task state, and the Queue page tab that lists it.
type TaskState int

// Task states, in tab order. The zero value is no state.
const (
	StateActive TaskState = iota + 1
	StatePending
	StateAggregating
	StateScheduled
	StateRetry
	StateArchived
	StateCompleted
)

// TaskStates lists the states in tab order.
func TaskStates() []TaskState {
	return []TaskState{StateActive, StatePending, StateAggregating, StateScheduled, StateRetry, StateArchived, StateCompleted}
}

// TaskVerb is a task action.
type TaskVerb int

// Task actions.
const (
	TaskRun TaskVerb = iota
	TaskArchive
	TaskDelete
	TaskCancel
)

// Element ids of the Queue page. Polls replace the regions by id; the
// focusable elements in them have stable ids (tlink-, tsel-, tab-,
// page- prefixes).
const (
	// QueuePageID wraps the page: the hx-sync scope of its requests.
	QueuePageID    = "queue-page"
	QueuePollID    = "queue-poll"
	QueueBadgeID   = "queue-badge"
	QueueToggleID  = "queue-toggle"
	QueueStatsID   = "queue-stats"
	QueueBannerID  = "queue-banner"
	TaskCardID     = "task-card"
	TasksTableID   = "tasks-table"
	TasksRowsID    = "tasks-rows"
	TasksFormID    = "tasks-form"
	TasksSelectAll = "tsel-all"
	// GroupsSelectAll selects every group of the Aggregating tab.
	GroupsSelectAll = "gsel-all"
	// TasksAllID is the hidden all=1 field of "select all N".
	TasksAllID    = "tasks-all"
	TasksBannerID = "tasks-banner"
	TasksPagerID  = "tasks-pager"
	TasksNoticeID = "tasks-notice"
	TasksSizeID   = "tasks-size"
	FindDialogID  = "find-task"
	FindFormID    = "find-form"
	FindBodyID    = "find-body"
	FindInputID   = "find-id"
	FindOpenID    = "find-open"
	// TasksDeleteDialogID confirms deleting the selected tasks,
	// TasksArchiveDialogID archiving them.
	TasksDeleteDialogID  = "tasks-delete"
	TasksArchiveDialogID = "tasks-archive"
	// TasksSortID is the time column's sort link.
	TasksSortID = "sort-time"
)

// asynq trims the archive (asynq v0.26.0 internal/rdb: maxArchiveSize,
// archivedExpirationInDays): every Archive confirmation says so.
const (
	ArchiveCap  = 10000
	ArchiveDays = 90
)

// GroupDialogID is the id of the dialog confirming verb on every task of
// the selected aggregation groups.
func GroupDialogID(v TaskVerb) string { return "groups-" + VerbParam(v) }

// TabCountID is the id of a tab's count, which polls replace.
func TabCountID(s TaskState) string { return "tab-count-" + StateParam(s) }

// TabID is the id of a tab's link.
func TabID(s TaskState) string { return "tab-" + StateParam(s) }

// AllDialogID is the id of the dialog confirming verb on "all N".
func AllDialogID(v TaskVerb) string { return "tasks-all-" + VerbParam(v) }

// StateParam is the ?state= value and id part of s.
func StateParam(s TaskState) string {
	switch s {
	case StateActive:
		return "active"
	case StateAggregating:
		return "aggregating"
	case StateScheduled:
		return "scheduled"
	case StateRetry:
		return "retry"
	case StateArchived:
		return "archived"
	case StateCompleted:
		return "completed"
	}
	return "pending"
}

// ParseState is the state whose ?state= value is v.
func ParseState(v string) (TaskState, bool) {
	for _, s := range TaskStates() {
		if StateParam(s) == v {
			return s, true
		}
	}
	return 0, false
}

// VerbParam is a verb's path segment and id part ("run", "archive").
func VerbParam(v TaskVerb) string {
	switch v {
	case TaskRun:
		return "run"
	case TaskArchive:
		return "archive"
	case TaskCancel:
		return "cancel"
	}
	return "delete"
}

// Queue is the Queue page: the queue's header and stats, the state tabs
// and one state's tasks.
type Queue struct {
	Layout views.Layout
	Name   string
	// Live is the polling frame: a poll leaves the card's controls alone.
	Live Live
	// NotFound marks a queue the connection does not have.
	NotFound bool

	Paused bool
	Stats  QueueStats
	Toggle QueueToggle
	// FindURL is the Find task page: the dialog's form target, and the
	// button's link without JS.
	FindURL string
	Find    Find

	Tabs []Tab
	Tab  TaskState
	// Total is the number of tasks in the tab's state.
	Total int64
	Rows  []TaskRow
	// Groups are the Aggregating tab's rows, when it lists the groups.
	Groups []GroupRow
	// Group is the aggregation group whose tasks the Aggregating tab
	// lists; empty when it lists the groups. GroupsURL lists them.
	Group     string
	GroupsURL string
	// Sort is the time column's sort link; its ID is empty for a table
	// that does not sort.
	Sort components.SortHead
	// Actions are the toolbar's buttons, in order.
	Actions []TaskActionButton
	// AllowAll offers "select all N" for this state.
	AllowAll bool
	// FormURL is the selection form's default action.
	FormURL       string
	DeleteDialog  components.Dialog
	ArchiveDialog components.Dialog
	AllDialogs    []AllDialog
	Pager         Pager
	Sizes         SizePicker
	// Next is the page's own URL, posted with actions.
	Next string
	// Notice is the result of the action this answers, if any.
	Notice *TaskNotice
}

// QueueStats are the stat cards.
type QueueStats struct {
	Size, Processed, Failed, Memory int64
	Latency                         time.Duration
	LatencyHigh                     bool
}

// QueueToggle is the Pause queue / Resume queue button.
type QueueToggle struct {
	URL     string
	Allowed bool
	// Queue and Next are posted with it.
	Queue string
	Next  string
}

// Tab is one state tab.
type Tab struct {
	State TaskState
	Count int64
	URL   string
	On    bool
}

// TaskRow is one task in the table. Which fields show depends on the tab.
type TaskRow struct {
	ID     string
	URL    string
	LinkID string
	Select components.RowSelect
	Type   string
	// Payload is the raw payload; the template previews one line of it.
	Payload       []byte
	Retried       int
	LastErr       string
	LastFailedAt  time.Time
	NextProcessAt time.Time
	CompletedAt   time.Time
	KeptUntil     time.Time
	ArchivedAt    time.Time
	// Started is when the worker started an active task; zero when no
	// server reports it. RunningFor is the time since.
	Started    time.Time
	RunningFor time.Duration
}

// GroupRow is one aggregation group: a link to its tasks and a
// selection checkbox for the "all" actions.
type GroupRow struct {
	Name   string
	Size   int64
	URL    string
	LinkID string
	Select components.RowSelect
}

// TaskActionButton is a toolbar button for verb; All labels it as the
// "all" variant (the aggregation groups' toolbar).
type TaskActionButton struct {
	Verb   TaskVerb
	Action components.Action
	All    bool
}

// AllDialog confirms verb on every task of the state.
type AllDialog struct {
	Verb   TaskVerb
	Dialog components.Dialog
}

// Pager is the table's footer: the range shown and the page links.
type Pager struct {
	From, To, Total int64
	// PrevURL and NextURL are empty on the first and last page.
	PrevURL, NextURL string
}

// SizePicker is the rows-per-page select. It submits to FormURL with the
// Hidden state (GET).
type SizePicker struct {
	FormURL string
	Hidden  []Param
	Size    int
	Choices []int
}

// Find is the Find task form: the dialog's body and the no-JS page.
type Find struct {
	// Action is the form's GET target.
	Action string
	// Queue names the queue in the title.
	Queue string
	// ID is the looked-up ID; Missed shows "Task not found", Failed the
	// error of a lookup that could not be made.
	ID     string
	Missed bool
	Failed string
	Down   bool
}

// FindPage is the Find task page, for browsers without JS.
type FindPage struct {
	Layout   views.Layout
	Find     Find
	QueueURL string
}

// TaskNotice is the result of a task action: how many tasks it acted on.
type TaskNotice struct {
	Verb  TaskVerb
	Count int64
}

// TaskFailure is a task action that failed on one task, or on every task
// of a state (ID empty).
type TaskFailure struct {
	Verb TaskVerb
	ID   string
	// Group is the aggregation group of a failed "all" action, if any.
	Group string
	// NotFound marks a task (or queue) that does not exist; otherwise
	// Detail is the error text.
	NotFound bool
	Detail   string
}

// Flash is what an action posted without JS leaves for the next page.
type Flash struct {
	Failures     []ActionFailure
	TaskFailures []TaskFailure
	Notice       *TaskNotice
}

// Confirm is the confirmation page of an action posted without JS (with
// JS, a dialog confirms): a queue delete, or a task action that needs
// one.
type Confirm struct {
	Layout views.Layout
	// Title is the heading, the title and the last breadcrumb.
	Title templ.Component
	// Names are the selected queues, tasks or aggregation groups.
	Names []string
	// Verb and All label the confirm button; a queue delete is Delete.
	Verb TaskVerb
	All  bool
	// Action and Hidden are what the confirm button posts, with
	// confirm=1.
	Action    string
	Hidden    []Param
	CancelURL string
}

// groupList reports whether the page lists the Aggregating tab's groups
// rather than tasks.
func (m Queue) groupList() bool { return m.Tab == StateAggregating && m.Group == "" }
