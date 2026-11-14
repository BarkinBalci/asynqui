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
	// TaskAction applies verb to one task, AllAction to every task of
	// the queue in state, and GroupAction to every task of one
	// aggregation group; the last two return how many they acted on.
	TaskAction(ctx context.Context, queue string, verb conn.TaskVerb, id string) error
	AllAction(ctx context.Context, queue string, verb conn.TaskVerb, state conn.TaskState) (int, error)
	GroupAction(ctx context.Context, queue, group string, verb conn.TaskVerb) (int, error)
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
	for _, v := range tabVerbs(s) {
		if v == verb {
			return true
		}
	}
	return false
}

// taskVerbs are the Task page's actions for a task in state s.
func taskVerbs(s conn.TaskState) []conn.TaskVerb { return tabVerbs(s) }

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

func pageVerb(v conn.TaskVerb) pages.TaskVerb {
	switch v {
	case conn.TaskRun:
		return pages.TaskRun
	case conn.TaskArchive:
		return pages.TaskArchive
	case conn.TaskCancel:
		return pages.TaskCancel
	}
	return pages.TaskDelete
}

func verbParam(v conn.TaskVerb) string {
	switch v {
	case conn.TaskRun:
		return "run"
	case conn.TaskArchive:
		return "archive"
	case conn.TaskCancel:
		return "cancel"
	}
	return "delete"
}

// taskVerbName names a verb in logs.
func taskVerbName(v conn.TaskVerb) string { return verbParam(v) }
