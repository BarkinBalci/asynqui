package pages

import (
	"time"

	"github.com/BarkinBalci/asynqui/internal/web/views"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
)

// Element ids of the Task page. Polls replace the badge, the last error,
// result and details cards, and the actions only when the state changed.
const (
	TaskPageID         = "task-page"
	TaskPollID         = "task-poll"
	TaskMainID         = "task-main"
	TaskBannerID       = "task-banner"
	TaskBadgeID        = "task-badge"
	TaskActionsID      = "task-actions"
	TaskErrorID        = "task-error"
	TaskResultID       = "task-result"
	TaskDetailsID      = "task-details"
	TaskFormID         = "task-form"
	TaskDeleteDialogID = "task-delete"
)

// Task is the Task page.
type Task struct {
	Layout views.Layout
	// State is whether the task was read; NotFound marks a task (or
	// queue) that does not exist.
	State    DataState
	Error    string
	NotFound bool
	// Down marks a failure to reach the server; see Queue.Down.
	Down bool

	Queue    string
	QueueURL string
	// TabURL is the queue's tab of the task's state: the back link.
	TabURL string
	ID     string
	Type   string
	Status TaskState

	// Payload is the shown payload text: indented JSON, or the raw text.
	// PayloadJSON offers the JSON / Raw toggle (valid JSON only); Raw is
	// the raw view.
	Payload     string
	PayloadJSON bool
	Raw         bool
	JSONURL     string
	RawURL      string
	// Result is the written result (indented when JSON), if HasResult.
	Result    string
	HasResult bool

	LastErr      string
	LastFailedAt time.Time
	// Attempt is the attempt the last error came from.
	Attempt       int
	Retried       int
	MaxRetry      int
	NextProcessAt time.Time
	Timeout       time.Duration
	Deadline      time.Time
	Group         string
	CompletedAt   time.Time
	KeptUntil     time.Time
	Headers       []Param
	Orphaned      bool

	FormURL      string
	Actions      []TaskActionButton
	DeleteDialog components.Dialog
	// Hidden are the action form's fields.
	Hidden  []Param
	PollURL string

	// Poll marks the answer to a poll; Init a poll from a page without
	// data; KeepPage a poll that found no data while the page shows the
	// task. Changed makes a poll replace the badge and the cards (the task
	// differs from what the page shows), StateChanged the actions too.
	Poll         bool
	Init         bool
	KeepPage     bool
	Changed      bool
	StateChanged bool
}
