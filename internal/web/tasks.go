package web

import (
	"context"
	"slices"

	"github.com/BarkinBalci/asynqui/internal/conn"
)

// TaskSource is what the Queue and Task pages need from a connection. pages
// has conn's TaskState and TaskVerb enums in the same order, since views do
// not import conn.
type TaskSource interface {
	// QueueInfo reads one queue's counters. An unknown queue is
	// conn.ErrQueueNotFound.
	QueueInfo(ctx context.Context, queue string) (conn.QueueInfo, error)
	// ListTasks lists one page (1-based) of the queue's tasks in state; see
	// conn.Conn.ListTasks.
	ListTasks(ctx context.Context, queue string, state conn.TaskState, page, size int, desc bool) ([]conn.Task, error)
	// Groups lists the queue's aggregation groups.
	Groups(ctx context.Context, queue string) ([]conn.Group, error)
	// ListGroupTasks lists one page (1-based) of one aggregation group's
	// tasks, in asynq's order.
	ListGroupTasks(ctx context.Context, queue, group string, page, size int) ([]conn.Task, error)
	// Task reads one task: conn.ErrTaskNotFound or conn.ErrQueueNotFound
	// when there is none.
	Task(ctx context.Context, queue, id string) (conn.Task, error)
	// TaskAction applies verb to one task, AllAction to every task of
	// the queue in state, and GroupAction to every task of one
	// aggregation group; the last two return how many they acted on.
	TaskAction(ctx context.Context, queue string, verb conn.TaskVerb, id string) error
	AllAction(ctx context.Context, queue string, verb conn.TaskVerb, state conn.TaskState) (int, error)
	GroupAction(ctx context.Context, queue, group string, verb conn.TaskVerb) (int, error)
}

// tabVerbs are the actions for a state, in button order: the Queue page
// toolbar's and the Task page's. Every one of them also has an "all"
// variant, except on Active.
func tabVerbs(s conn.TaskState) []conn.TaskVerb {
	switch s {
	case conn.TaskActive:
		return []conn.TaskVerb{conn.TaskCancel}
	case conn.TaskPending:
		return []conn.TaskVerb{conn.TaskArchive, conn.TaskDelete}
	case conn.TaskScheduled, conn.TaskRetry, conn.TaskAggregating:
		return []conn.TaskVerb{conn.TaskRun, conn.TaskArchive, conn.TaskDelete}
	case conn.TaskArchived:
		return []conn.TaskVerb{conn.TaskRun, conn.TaskDelete}
	case conn.TaskCompleted:
		return []conn.TaskVerb{conn.TaskDelete}
	}
	return nil
}

// allowsAll reports whether verb has an "all" variant in state s.
func allowsAll(s conn.TaskState, verb conn.TaskVerb) bool {
	if s == conn.TaskActive {
		return false
	}
	return slices.Contains(tabVerbs(s), verb)
}

// sortable reports whether the state's table sorts by its time column (see
// conn.Conn.ListTasks).
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
