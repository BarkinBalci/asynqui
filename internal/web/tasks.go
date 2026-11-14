package web

import (
	"context"

	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// TaskSource is what the Queue and Task pages need from a connection.
type TaskSource interface {
	// QueueInfo reads one queue's counters. An unknown queue is
	// conn.ErrQueueNotFound.
	QueueInfo(ctx context.Context, queue string) (conn.QueueInfo, error)
	// ListTasks lists one page (1-based) of the queue's tasks in state.
	// The sortable states come by their time: soonest or oldest first,
	// latest first with desc. Active and pending tasks come in asynq's
	// order. Aggregating tasks are listed per group (ListGroupTasks).
	ListTasks(ctx context.Context, queue string, state conn.TaskState, page, size int, desc bool) ([]conn.Task, error)
	// Groups lists the queue's aggregation groups.
	Groups(ctx context.Context, queue string) ([]conn.Group, error)
	// ListGroupTasks lists one page (1-based) of one aggregation group's
	// tasks, in asynq's order.
	ListGroupTasks(ctx context.Context, queue, group string, page, size int) ([]conn.Task, error)
	// Task reads one task: conn.ErrTaskNotFound or conn.ErrQueueNotFound
	// when there is none.
	Task(ctx context.Context, queue, id string) (conn.Task, error)
	// The actions on one task, on every task of a state, and on every
	// task of an aggregation group; the "all" ones return how many they
	// acted on.
	RunTask(ctx context.Context, queue, id string) error
	ArchiveTask(ctx context.Context, queue, id string) error
	DeleteTask(ctx context.Context, queue, id string) error
	CancelTask(ctx context.Context, queue, id string) error
	RunAllTasks(ctx context.Context, queue string, state conn.TaskState) (int, error)
	ArchiveAllTasks(ctx context.Context, queue string, state conn.TaskState) (int, error)
	DeleteAllTasks(ctx context.Context, queue string, state conn.TaskState) (int, error)
	RunAllGroupTasks(ctx context.Context, queue, group string) (int, error)
	ArchiveAllGroupTasks(ctx context.Context, queue, group string) (int, error)
	DeleteAllGroupTasks(ctx context.Context, queue, group string) (int, error)
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

// taskAction applies verb to one task.
func taskAction(ctx context.Context, c Conn, queue string, verb TaskVerb, id string) error {
	switch verb {
	case TaskRun:
		return c.RunTask(ctx, queue, id)
	case TaskArchive:
		return c.ArchiveTask(ctx, queue, id)
	case TaskCancel:
		return c.CancelTask(ctx, queue, id)
	}
	return c.DeleteTask(ctx, queue, id)
}

// allAction applies verb (run, archive or delete) to every task of the
// queue in state.
func allAction(ctx context.Context, c Conn, queue string, verb TaskVerb, state conn.TaskState) (int, error) {
	switch verb {
	case TaskRun:
		return c.RunAllTasks(ctx, queue, state)
	case TaskArchive:
		return c.ArchiveAllTasks(ctx, queue, state)
	case TaskDelete:
		return c.DeleteAllTasks(ctx, queue, state)
	}
	return 0, conn.ErrUnsupported
}

// groupAction applies verb (run, archive or delete) to every task of one
// aggregation group.
func groupAction(ctx context.Context, c Conn, queue string, verb TaskVerb, group string) (int, error) {
	switch verb {
	case TaskRun:
		return c.RunAllGroupTasks(ctx, queue, group)
	case TaskArchive:
		return c.ArchiveAllGroupTasks(ctx, queue, group)
	case TaskDelete:
		return c.DeleteAllGroupTasks(ctx, queue, group)
	}
	return 0, conn.ErrUnsupported
}

// The actions each state offers, as asynq supports them. Run is valid for
// scheduled, retry, archived and aggregating tasks, Archive for pending,
// scheduled, retry and aggregating ones, Delete for any but active ones,
// and Cancel (CancelProcessing) only for active ones. asynq has no "all"
// variant of Cancel, and per the user's decision the Active tab has no
// "select all N" either. The "all" variants of aggregating tasks act on
// one group (asynq's *AllAggregatingTasks).

// tabVerbs are the Queue page toolbar's actions for a state, in button
// order. Every one of them also has an "all" variant, except on Active.
func tabVerbs(s conn.TaskState) []TaskVerb {
	switch s {
	case conn.TaskActive:
		return []TaskVerb{TaskCancel}
	case conn.TaskPending:
		return []TaskVerb{TaskArchive, TaskDelete}
	case conn.TaskScheduled, conn.TaskRetry, conn.TaskAggregating:
		return []TaskVerb{TaskRun, TaskArchive, TaskDelete}
	case conn.TaskArchived:
		return []TaskVerb{TaskRun, TaskDelete}
	case conn.TaskCompleted:
		return []TaskVerb{TaskDelete}
	}
	return nil
}

// allowsAll reports whether verb has an "all" variant in state s.
func allowsAll(s conn.TaskState, verb TaskVerb) bool {
	if s == conn.TaskActive {
		return false
	}
	for _, v := range tabVerbs(s) {
		if v == verb {
			return true
		}
	}
	return false
}

// taskVerbs are the Task page's actions for a task in state s.
func taskVerbs(s conn.TaskState) []TaskVerb { return tabVerbs(s) }

// sortable reports whether the state's table sorts by its time column:
// scheduled and retry by the next process time, archived by when it was
// archived, completed by when its result expires. Tasks applies desc for
// these states only.
func sortable(s conn.TaskState) bool {
	switch s {
	case conn.TaskScheduled, conn.TaskRetry, conn.TaskArchived, conn.TaskCompleted:
		return true
	}
	return false
}

// taskDefaultDesc is the state's default direction: what is next first for
// scheduled and retry, the most recent first for archived and completed.
func taskDefaultDesc(s conn.TaskState) bool { return s == conn.TaskArchived || s == conn.TaskCompleted }

func stateParam(s conn.TaskState) string { return pages.StateParam(pageState(s)) }

// parseState is the state whose ?state= value is v.
func parseState(v string) (conn.TaskState, bool) {
	for _, s := range []conn.TaskState{conn.TaskActive, conn.TaskPending, conn.TaskAggregating, conn.TaskScheduled, conn.TaskRetry, conn.TaskArchived, conn.TaskCompleted} {
		if stateParam(s) == v {
			return s, true
		}
	}
	return 0, false
}

func pageState(s conn.TaskState) pages.TaskState {
	switch s {
	case conn.TaskActive:
		return pages.StateActive
	case conn.TaskAggregating:
		return pages.StateAggregating
	case conn.TaskScheduled:
		return pages.StateScheduled
	case conn.TaskRetry:
		return pages.StateRetry
	case conn.TaskArchived:
		return pages.StateArchived
	case conn.TaskCompleted:
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
