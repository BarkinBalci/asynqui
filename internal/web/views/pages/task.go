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
	// TaskArchiveDialogID confirms archiving the task.
	TaskArchiveDialogID = "task-archive"
)

// Task is the Task page.
type Task struct {
	Layout views.Layout
	// Live is the polling frame; NotFound marks a task (or queue) that
	// does not exist, which stops it.
	Live     Live
	NotFound bool

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

	FormURL       string
	Actions       []TaskActionButton
	DeleteDialog  components.Dialog
	ArchiveDialog components.Dialog
	// Hidden are the action form's fields.
	Hidden []Param

	// Changed makes a poll replace the badge and the cards (the task
	// differs from what the page shows), StateChanged the actions too.
	Changed      bool
	StateChanged bool
}
