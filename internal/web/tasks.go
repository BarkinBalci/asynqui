package web

import (
	"context"
	"errors"
	"time"

	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// TaskSource is what the Queue and Task pages need from the connection
// registry.
type TaskSource interface {
	// QueueInfo reads one queue's counters. An unknown queue is
	// ErrQueueNotFound.
	QueueInfo(ctx context.Context, name, queue string) (QueueInfo, error)
	// Tasks lists one page (1-based) of the queue's tasks in state, in
	// asynq's order. Aggregating tasks are listed per group (Groups).
	Tasks(ctx context.Context, name, queue string, state TaskState, page, size int) ([]Task, error)
	// Groups lists the queue's aggregation groups.
	Groups(ctx context.Context, name, queue string) ([]Group, error)
	// Task reads one task: ErrTaskNotFound or ErrQueueNotFound when there
	// is none.
	Task(ctx context.Context, name, queue, id string) (Task, error)
	// TaskAction applies verb to one task.
	TaskAction(ctx context.Context, name, queue string, verb TaskVerb, id string) error
	// TaskActionAll applies verb to every task of the queue in state and
	// returns how many it acted on.
	TaskActionAll(ctx context.Context, name, queue string, verb TaskVerb, state TaskState) (int, error)
}

// ErrTaskNotFound is wrapped by a TaskSource for a task that does not
// exist.
var ErrTaskNotFound = errors.New("web: task not found")

// TaskState is a task's state.
type TaskState int

// Task states. The zero value is no state.
const (
	TaskActive TaskState = iota + 1
	TaskPending
	TaskAggregating
	TaskScheduled
	TaskRetry
	TaskArchived
	TaskCompleted
)

// TaskVerb is a task action.
type TaskVerb int

// Task actions.
const (
	TaskRun TaskVerb = iota
	TaskArchive
	TaskDelete
	TaskCancel
)

// QueueInfo is one queue with the counts of every state.
type QueueInfo struct {
	Queue
	Completed   int
	Aggregating int
}

// Count is the number of the queue's tasks in state s.
func (q QueueInfo) Count(s TaskState) int {
	switch s {
	case TaskActive:
		return q.Active
	case TaskPending:
		return q.Pending
	case TaskAggregating:
		return q.Aggregating
	case TaskScheduled:
		return q.Scheduled
	case TaskRetry:
		return q.Retry
	case TaskArchived:
		return q.Archived
	case TaskCompleted:
		return q.Completed
	}
	return 0
}

// Task is one task, from asynq's TaskInfo.
type Task struct {
	ID            string
	Queue         string
	Type          string
	Payload       []byte
	Headers       map[string]string
	State         TaskState
	MaxRetry      int
	Retried       int
	LastErr       string
	LastFailedAt  time.Time
	Timeout       time.Duration
	Deadline      time.Time
	Group         string
	NextProcessAt time.Time
	IsOrphaned    bool
	Retention     time.Duration
	CompletedAt   time.Time
	Result        []byte
	// Started is when a worker started an active task, when a server
	// reports it (Tasks only).
	Started time.Time
}

// Group is one aggregation group.
type Group struct {
	Name string
	Size int
}

// The actions each state offers, as asynq supports them. Run is valid for
// scheduled, retry, archived and aggregating tasks, Archive for pending,
// scheduled, retry and aggregating ones, Delete for any but active ones,
// and Cancel (CancelProcessing) only for active ones. asynq has no "all"
// variant of Cancel, and per the user's decision the Active tab has no
// "select all N" either.

// tabVerbs are the Queue page toolbar's actions for a state, in button
// order. Every one of them also has an "all" variant, except on Active.
func tabVerbs(s TaskState) []TaskVerb {
	switch s {
	case TaskActive:
		return []TaskVerb{TaskCancel}
	case TaskPending:
		return []TaskVerb{TaskArchive, TaskDelete}
	case TaskScheduled, TaskRetry:
		return []TaskVerb{TaskRun, TaskArchive, TaskDelete}
	case TaskArchived:
		return []TaskVerb{TaskRun, TaskDelete}
	case TaskCompleted:
		return []TaskVerb{TaskDelete}
	}
	return nil
}

// allowsAll reports whether verb has an "all" variant in state s.
func allowsAll(s TaskState, verb TaskVerb) bool {
	if s == TaskActive {
		return false
	}
	for _, v := range tabVerbs(s) {
		if v == verb {
			return true
		}
	}
	return false
}

// taskVerbs are the Task page's actions for a task in state s: the tab's,
// plus those asynq allows on a single aggregating task.
func taskVerbs(s TaskState) []TaskVerb {
	if s == TaskAggregating {
		return []TaskVerb{TaskRun, TaskArchive, TaskDelete}
	}
	return tabVerbs(s)
}

func stateParam(s TaskState) string { return pages.StateParam(pageState(s)) }

// parseState is the state whose ?state= value is v.
func parseState(v string) (TaskState, bool) {
	for _, s := range []TaskState{TaskActive, TaskPending, TaskAggregating, TaskScheduled, TaskRetry, TaskArchived, TaskCompleted} {
		if stateParam(s) == v {
			return s, true
		}
	}
	return 0, false
}

func pageState(s TaskState) pages.TaskState {
	switch s {
	case TaskActive:
		return pages.StateActive
	case TaskAggregating:
		return pages.StateAggregating
	case TaskScheduled:
		return pages.StateScheduled
	case TaskRetry:
		return pages.StateRetry
	case TaskArchived:
		return pages.StateArchived
	case TaskCompleted:
		return pages.StateCompleted
	}
	return pages.StatePending
}

func pageVerb(v TaskVerb) pages.TaskVerb {
	switch v {
	case TaskRun:
		return pages.TaskRun
	case TaskArchive:
		return pages.TaskArchive
	case TaskCancel:
		return pages.TaskCancel
	}
	return pages.TaskDelete
}

func verbParam(v TaskVerb) string {
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

// taskVerbName names a verb in logs.
func taskVerbName(v TaskVerb) string { return verbParam(v) }
