package conn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hibiken/asynq"
)

// MaxPageSize is the largest page ListTasks reads.
const MaxPageSize = 100

// Errors of the task reads and actions. They wrap asynq's own where asynq
// has one, so errors.Is works with either.
var (
	// ErrTaskNotFound is returned for a task the queue does not have.
	ErrTaskNotFound = errors.New("conn: task not found")
	// ErrTaskState is returned for an action that does not apply to the
	// task's state, where conn checks it (CancelTask on a task that is not
	// active). asynq's own state errors are not typed and pass through.
	ErrTaskState = errors.New("conn: action does not apply to the task state")
	// ErrUnsupported is returned for a state that a list or "all" action
	// does not support.
	ErrUnsupported = errors.New("conn: unsupported for this task state")
)

// TaskState is the state a task is in, as asynq's TaskState.
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

// taskInspector is the part of *asynq.Inspector that the task reads and
// actions use.
type taskInspector interface {
	GetTaskInfo(queue, id string) (*asynq.TaskInfo, error)
	ListPendingTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	ListActiveTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	ListScheduledTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	ListRetryTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	ListArchivedTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	ListCompletedTasks(queue string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	ListAggregatingTasks(queue, group string, opts ...asynq.ListOption) ([]*asynq.TaskInfo, error)
	Groups(queue string) ([]*asynq.GroupInfo, error)

	RunTask(queue, id string) error
	ArchiveTask(queue, id string) error
	DeleteTask(queue, id string) error
	CancelProcessing(id string) error

	RunAllScheduledTasks(queue string) (int, error)
	RunAllRetryTasks(queue string) (int, error)
	RunAllArchivedTasks(queue string) (int, error)
	ArchiveAllPendingTasks(queue string) (int, error)
	ArchiveAllScheduledTasks(queue string) (int, error)
	ArchiveAllRetryTasks(queue string) (int, error)
	DeleteAllPendingTasks(queue string) (int, error)
	DeleteAllScheduledTasks(queue string) (int, error)
	DeleteAllRetryTasks(queue string) (int, error)
	DeleteAllArchivedTasks(queue string) (int, error)
	DeleteAllCompletedTasks(queue string) (int, error)
	RunAllAggregatingTasks(queue, group string) (int, error)
	ArchiveAllAggregatingTasks(queue, group string) (int, error)
	DeleteAllAggregatingTasks(queue, group string) (int, error)
}

// Task is one task, from asynq's TaskInfo.
type Task struct {
	ID      string
	Queue   string
	Type    string
	Payload []byte
	Headers map[string]string
	State   TaskState
	// MaxRetry and Retried count retries, not attempts.
	MaxRetry     int
	Retried      int
	LastErr      string
	LastFailedAt time.Time // zero without a failure
	Timeout      time.Duration
	Deadline     time.Time
	Group        string
	// NextProcessAt is when a scheduled, retry or pending task runs next;
	// zero otherwise.
	NextProcessAt time.Time
	// IsOrphaned marks an active task whose worker lost its lease.
	IsOrphaned  bool
	Retention   time.Duration
	CompletedAt time.Time
	Result      []byte
	// Started is when a worker started an active task, from the servers'
	// worker lists (ListTasks of active tasks only). Zero when no server
	// reports the task, as for an orphaned one.
	Started time.Time
	// ArchivedAt is when an archived task was archived: its score in the
	// archived set (ListTasks of archived tasks only; see keys.go).
	ArchivedAt time.Time
}

// Group is one aggregation group of a queue.
type Group struct {
	Name string
	Size int
}

// QueueInfo is one queue's counters, as Queue plus the counts the queue
// page's tabs show.
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

// queueKey, tasksKey, groupsKey and taskKey are the cache keys of one
// queue's reads (see the key list in cache.go). The queue name, which may
// hold any character, comes last or is length-prefixed, so two keys never
// collide.
func queueKey(queue string) cacheKey[QueueInfo] {
	return cacheKey[QueueInfo]("queue/" + queue)
}

func tasksKey(queue string, state TaskState, page, size int, desc bool) cacheKey[[]Task] {
	return cacheKey[[]Task]("tasks/" + strconv.Itoa(int(state)) + "/" + strconv.Itoa(page) + "/" + strconv.Itoa(size) + "/" + strconv.FormatBool(desc) + "/" + queue)
}

func groupTasksKey(queue, group string, page, size int) cacheKey[[]Task] {
	return cacheKey[[]Task]("group-tasks/" + strconv.Itoa(page) + "/" + strconv.Itoa(size) + "/" + strconv.Itoa(len(queue)) + "/" + queue + group)
}

func groupsKey(queue string) cacheKey[[]Group] {
	return cacheKey[[]Group]("groups/" + queue)
}

func taskKey(queue, id string) cacheKey[Task] {
	return cacheKey[Task]("task/" + strconv.Itoa(len(queue)) + "/" + queue + id)
}

// QueueInfo returns one queue's counters from the 1s cache when fresh. A
// queue the connection does not have is ErrQueueNotFound.
//
// Cost: one SMEMBERS for the existence check, then GetQueueInfo (see
// fetchQueues): about a second of the Connection page's cost for one
// queue.
func (c *Conn) QueueInfo(ctx context.Context, queue string) (QueueInfo, error) {
	return cached(ctx, c.cache, queueKey(queue), cacheTTL, func(context.Context) (QueueInfo, error) {
		if err := c.checkQueue(queue); err != nil {
			return QueueInfo{}, err
		}
		qi, err := c.queues.GetQueueInfo(queue)
		if err != nil {
			return QueueInfo{}, fmt.Errorf("queue %s: %w", queue, queueErr(err))
		}
		return QueueInfo{
			Queue: Queue{
				Name:           qi.Queue,
				Paused:         qi.Paused,
				Size:           qi.Size,
				Pending:        qi.Pending,
				Active:         qi.Active,
				Scheduled:      qi.Scheduled,
				Retry:          qi.Retry,
				Archived:       qi.Archived,
				ProcessedToday: qi.Processed,
				FailedToday:    qi.Failed,
				Latency:        qi.Latency,
				MemoryUsage:    qi.MemoryUsage,
			},
			Completed:   qi.Completed,
			Aggregating: qi.Aggregating,
		}, nil
	})
}

// checkQueue returns ErrQueueNotFound unless the connection has queue.
// asynq's "all" actions do not report a missing queue with its exported
// error, and its Pause marks any name paused.
func (c *Conn) checkQueue(queue string) error {
	if strings.TrimSpace(queue) == "" {
		return fmt.Errorf("%w: %q", ErrQueueNotFound, queue)
	}
	names, err := c.queues.Queues()
	if err != nil {
		return fmt.Errorf("list queues: %w", err)
	}
	if !slices.Contains(names, queue) {
		return fmt.Errorf("%w: %q", ErrQueueNotFound, queue)
	}
	return nil
}

// ListTasks returns one page (1-based, size 1..MaxPageSize) of the queue's
// tasks in state, from the 1s cache when fresh. A queue the connection does
// not have is ErrQueueNotFound.
//
// Scheduled, retry, archived and completed tasks come in the order of the
// state's time, the score of asynq's sorted set for the state (see
// keys.go): soonest or oldest first, or latest first with desc. The
// Inspector cannot list in reverse, so the page's IDs are read with one
// range read of the set, then each is read with GetTaskInfo,
// queueConcurrency at a time (see hydrate). Archived tasks carry
// ArchivedAt from their score.
//
// Pending and active tasks come in asynq's order (oldest first; desc is
// ignored), from ListPendingTasks and ListActiveTasks. Active tasks carry
// Started from the servers' worker lists: the Servers snapshot the
// Servers page shares. Aggregating tasks are listed per group
// (ListGroupTasks), so that state is ErrUnsupported.
//
// Cost: for the sorted states an SMEMBERS for the existence check and one
// range read, then per task GetTaskInfo's SISMEMBER and Lua script; for
// pending and active tasks one List*Tasks.
//
// The slice is shared between callers, who must not modify it.
func (c *Conn) ListTasks(ctx context.Context, queue string, state TaskState, page, size int, desc bool) ([]Task, error) {
	if state == TaskAggregating || state < TaskActive || state > TaskCompleted {
		return nil, ErrUnsupported
	}
	if _, sorted := stateKey(queue, state); !sorted {
		desc = false
	}
	page, size = max(page, 1), min(max(size, 1), MaxPageSize)
	return cached(ctx, c.cache, tasksKey(queue, state, page, size, desc), cacheTTL, func(ctx context.Context) ([]Task, error) {
		switch state {
		case TaskActive:
			return c.listActive(ctx, queue, page, size)
		case TaskPending:
			return c.listInspector(queue, page, size, c.tasks.ListPendingTasks)
		}
		if err := c.checkQueue(queue); err != nil {
			return nil, err
		}
		entries, err := readStateIDs(ctx, c.keys, queue, state, (page-1)*size, size, desc)
		if err != nil {
			return nil, fmt.Errorf("list tasks of %s: %w", queue, err)
		}
		return c.hydrate(ctx, queue, state, entries)
	})
}

// listInspector is one page of tasks from an Inspector list method.
func (c *Conn) listInspector(queue string, page, size int, list func(string, ...asynq.ListOption) ([]*asynq.TaskInfo, error)) ([]Task, error) {
	infos, err := recovered(func() ([]*asynq.TaskInfo, error) {
		return list(queue, asynq.PageSize(size), asynq.Page(page))
	})
	if err != nil {
		return nil, fmt.Errorf("list tasks of %s: %w", queue, taskErr(err))
	}
	out := make([]Task, len(infos))
	for i, ti := range infos {
		out[i] = newTask(ti)
	}
	return out, nil
}

// listActive is one page of the queue's active tasks, from the Inspector,
// with each task's start from the servers' worker lists.
func (c *Conn) listActive(ctx context.Context, queue string, page, size int) ([]Task, error) {
	out, err := c.listInspector(queue, page, size, c.tasks.ListActiveTasks)
	if err != nil || len(out) == 0 {
		return out, err
	}
	// A failing server read leaves Started out rather than failing the
	// list.
	starts, _ := c.workerStarts(ctx)
	for i := range out {
		out[i].Started = starts[out[i].ID]
	}
	return out, nil
}

// hydrate reads each entry's task with GetTaskInfo, at most
// queueConcurrency at a time, and returns them in the entries' order.
//
// A task that is gone, or no longer in state (run, deleted, moved on
// between the two reads), is left out, so a page may come up short. So is
// one whose read fails with an error reply (a WARN is logged): one bad
// task does not hide the page. A queue that is gone (ErrQueueNotFound) or
// a server that cannot be reached (Unreachable) fails the page, and no
// further reads start after either.
func (c *Conn) hydrate(ctx context.Context, queue string, state TaskState, entries []keyEntry) ([]Task, error) {
	tasks := make([]Task, len(entries))
	keep := make([]bool, len(entries))
	var (
		mu    sync.Mutex
		fatal error
	)
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return fatal != nil
	}
	sem := make(chan struct{}, queueConcurrency)
	var wg sync.WaitGroup
	for i, e := range entries {
		sem <- struct{}{}
		if failed() || ctx.Err() != nil {
			<-sem
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			ti, err := recovered(func() (*asynq.TaskInfo, error) { return c.tasks.GetTaskInfo(queue, e.ID) })
			switch {
			case errors.Is(err, asynq.ErrTaskNotFound):
				return
			case errors.Is(err, asynq.ErrQueueNotFound), Unreachable(err):
				mu.Lock()
				if fatal == nil {
					fatal = err
				}
				mu.Unlock()
				return
			case err != nil:
				c.logger.Warn("task read failed; left out of the page", slog.String("queue", queue),
					slog.String("task", e.ID), slog.String("error", err.Error()))
				return
			}
			t := newTask(ti)
			if t.State != state {
				return
			}
			if state == TaskArchived {
				t.ArchivedAt = e.Score
			}
			tasks[i], keep[i] = t, true
		})
	}
	wg.Wait()
	if fatal != nil {
		return nil, fmt.Errorf("list tasks of %s: %w", queue, taskErr(fatal))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(entries))
	for i, t := range tasks {
		if keep[i] {
			out = append(out, t)
		}
	}
	return out, nil
}

// ListGroupTasks returns one page (1-based, size 1..MaxPageSize) of the
// tasks of one aggregation group of the queue, in asynq's order (oldest
// first), from the 1s cache when fresh. A queue the connection does not
// have is ErrQueueNotFound; a group without tasks is an empty page.
//
// Cost: an SMEMBERS for the existence check, then ListAggregatingTasks.
//
// The slice is shared between callers, who must not modify it.
func (c *Conn) ListGroupTasks(ctx context.Context, queue, group string, page, size int) ([]Task, error) {
	page, size = max(page, 1), min(max(size, 1), MaxPageSize)
	return cached(ctx, c.cache, groupTasksKey(queue, group, page, size), cacheTTL, func(context.Context) ([]Task, error) {
		if err := c.checkQueue(queue); err != nil {
			return nil, err
		}
		infos, err := recovered(func() ([]*asynq.TaskInfo, error) {
			return c.tasks.ListAggregatingTasks(queue, group, asynq.PageSize(size), asynq.Page(page))
		})
		if err != nil {
			return nil, fmt.Errorf("list tasks of %s group %s: %w", queue, group, taskErr(err))
		}
		out := make([]Task, len(infos))
		for i, ti := range infos {
			out[i] = newTask(ti)
		}
		return out, nil
	})
}

// workerStarts maps each in-progress task ID to its start, from every
// server's worker list (the cached Servers snapshot).
func (c *Conn) workerStarts(ctx context.Context) (map[string]time.Time, error) {
	servers, err := c.Servers(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for _, s := range servers {
		for _, w := range s.Workers {
			out[w.TaskID] = w.Started
		}
	}
	return out, nil
}

// Groups returns the queue's aggregation groups, sorted by name, from the
// 1s cache when fresh.
func (c *Conn) Groups(ctx context.Context, queue string) ([]Group, error) {
	return cached(ctx, c.cache, groupsKey(queue), cacheTTL, func(context.Context) ([]Group, error) {
		if err := c.checkQueue(queue); err != nil {
			return nil, err
		}
		gs, err := recovered(func() ([]*asynq.GroupInfo, error) { return c.tasks.Groups(queue) })
		if err != nil {
			return nil, fmt.Errorf("groups of %s: %w", queue, err)
		}
		out := make([]Group, len(gs))
		for i, g := range gs {
			out[i] = Group{Name: g.Group, Size: g.Size}
		}
		slices.SortFunc(out, func(a, b Group) int { return strings.Compare(a.Name, b.Name) })
		return out, nil
	})
}

// Task returns one task of the queue (asynq's GetTaskInfo), from the 1s
// cache when fresh. It returns ErrTaskNotFound or ErrQueueNotFound, each
// also wrapping asynq's error.
func (c *Conn) Task(ctx context.Context, queue, id string) (Task, error) {
	if strings.TrimSpace(id) == "" {
		return Task{}, ErrTaskNotFound
	}
	return cached(ctx, c.cache, taskKey(queue, id), cacheTTL, func(context.Context) (Task, error) {
		ti, err := recovered(func() (*asynq.TaskInfo, error) { return c.tasks.GetTaskInfo(queue, id) })
		if err != nil {
			return Task{}, taskErr(err)
		}
		return newTask(ti), nil
	})
}

// RunTask moves a scheduled, retry, archived or aggregating task to
// pending. ArchiveTask archives a pending, scheduled, retry or aggregating
// task. DeleteTask deletes a task in any state but active. Each returns
// ErrTaskNotFound or ErrQueueNotFound (wrapping asynq's), or asynq's
// untyped error for a state the action does not apply to. On success the
// connection's cached reads are dropped.
func (c *Conn) RunTask(ctx context.Context, queue, id string) error {
	return c.taskAction(ctx, queue, id, c.tasks.RunTask)
}

// ArchiveTask archives a task; see RunTask.
func (c *Conn) ArchiveTask(ctx context.Context, queue, id string) error {
	return c.taskAction(ctx, queue, id, c.tasks.ArchiveTask)
}

// DeleteTask deletes a task; see RunTask.
func (c *Conn) DeleteTask(ctx context.Context, queue, id string) error {
	return c.taskAction(ctx, queue, id, c.tasks.DeleteTask)
}

func (c *Conn) taskAction(ctx context.Context, queue, id string, act func(string, string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return ErrTaskNotFound
	}
	_, err := recovered(func() (struct{}, error) { return struct{}{}, act(queue, id) })
	if err != nil {
		return taskErr(err)
	}
	c.cache.reset()
	return nil
}

// CancelTask asks the worker processing an active task of the queue to
// stop it (asynq's CancelProcessing, best effort). It first reads the task,
// so that it returns ErrTaskNotFound, ErrQueueNotFound, or ErrTaskState
// for a task that is not active, instead of publishing a signal no worker
// listens for.
func (c *Conn) CancelTask(ctx context.Context, queue, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return ErrTaskNotFound
	}
	_, err := recovered(func() (struct{}, error) {
		ti, err := c.tasks.GetTaskInfo(queue, id)
		if err != nil {
			return struct{}{}, taskErr(err)
		}
		if ti.State != asynq.TaskStateActive {
			return struct{}{}, ErrTaskState
		}
		if err := c.tasks.CancelProcessing(id); err != nil {
			return struct{}{}, fmt.Errorf("cancel %s: %w", id, err)
		}
		return struct{}{}, nil
	})
	if err != nil {
		return err
	}
	c.cache.reset()
	return nil
}

// RunAllTasks moves every scheduled, retry or archived task of the queue
// to pending and returns how many it moved. Other states are
// ErrUnsupported, a queue the connection does not have ErrQueueNotFound.
// On success the cached reads are dropped.
func (c *Conn) RunAllTasks(ctx context.Context, queue string, state TaskState) (int, error) {
	var fn func(string) (int, error)
	switch state {
	case TaskScheduled:
		fn = c.tasks.RunAllScheduledTasks
	case TaskRetry:
		fn = c.tasks.RunAllRetryTasks
	case TaskArchived:
		fn = c.tasks.RunAllArchivedTasks
	}
	return c.allAction(ctx, queue, fn)
}

// ArchiveAllTasks archives every pending, scheduled or retry task of the
// queue; see RunAllTasks.
func (c *Conn) ArchiveAllTasks(ctx context.Context, queue string, state TaskState) (int, error) {
	var fn func(string) (int, error)
	switch state {
	case TaskPending:
		fn = c.tasks.ArchiveAllPendingTasks
	case TaskScheduled:
		fn = c.tasks.ArchiveAllScheduledTasks
	case TaskRetry:
		fn = c.tasks.ArchiveAllRetryTasks
	}
	return c.allAction(ctx, queue, fn)
}

// DeleteAllTasks deletes every pending, scheduled, retry, archived or
// completed task of the queue; see RunAllTasks.
func (c *Conn) DeleteAllTasks(ctx context.Context, queue string, state TaskState) (int, error) {
	var fn func(string) (int, error)
	switch state {
	case TaskPending:
		fn = c.tasks.DeleteAllPendingTasks
	case TaskScheduled:
		fn = c.tasks.DeleteAllScheduledTasks
	case TaskRetry:
		fn = c.tasks.DeleteAllRetryTasks
	case TaskArchived:
		fn = c.tasks.DeleteAllArchivedTasks
	case TaskCompleted:
		fn = c.tasks.DeleteAllCompletedTasks
	}
	return c.allAction(ctx, queue, fn)
}

// RunAllGroupTasks moves every task of one aggregation group of the queue
// to pending (asynq's RunAllAggregatingTasks) and returns how many it
// moved. ArchiveAllGroupTasks and DeleteAllGroupTasks archive or delete
// them. A queue the connection does not have is ErrQueueNotFound. On
// success the cached reads are dropped.
func (c *Conn) RunAllGroupTasks(ctx context.Context, queue, group string) (int, error) {
	return c.groupAction(ctx, queue, group, c.tasks.RunAllAggregatingTasks)
}

// ArchiveAllGroupTasks archives every task of a group; see
// RunAllGroupTasks.
func (c *Conn) ArchiveAllGroupTasks(ctx context.Context, queue, group string) (int, error) {
	return c.groupAction(ctx, queue, group, c.tasks.ArchiveAllAggregatingTasks)
}

// DeleteAllGroupTasks deletes every task of a group; see RunAllGroupTasks.
func (c *Conn) DeleteAllGroupTasks(ctx context.Context, queue, group string) (int, error) {
	return c.groupAction(ctx, queue, group, c.tasks.DeleteAllAggregatingTasks)
}

func (c *Conn) groupAction(ctx context.Context, queue, group string, fn func(string, string) (int, error)) (int, error) {
	return c.allAction(ctx, queue, func(q string) (int, error) { return fn(q, group) })
}

// allAction runs an "all" action after checking that the queue exists.
// The cached reads are dropped even when it fails, since asynq may have
// moved some tasks before failing.
func (c *Conn) allAction(ctx context.Context, queue string, fn func(string) (int, error)) (int, error) {
	if fn == nil {
		return 0, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n, err := recovered(func() (int, error) {
		if err := c.checkQueue(queue); err != nil {
			return 0, err
		}
		return fn(queue)
	})
	if errors.Is(err, ErrQueueNotFound) {
		return 0, err
	}
	c.cache.reset()
	if err != nil {
		return n, fmt.Errorf("queue %s: %w", queue, err)
	}
	return n, nil
}

// taskErr adds conn's typed errors to asynq's exported ones.
func taskErr(err error) error {
	switch {
	case errors.Is(err, asynq.ErrTaskNotFound):
		return fmt.Errorf("%w: %w", ErrTaskNotFound, err)
	case errors.Is(err, asynq.ErrQueueNotFound):
		return fmt.Errorf("%w: %w", ErrQueueNotFound, err)
	}
	return err
}

// queueErr adds ErrQueueNotFound to asynq's.
func queueErr(err error) error {
	if errors.Is(err, asynq.ErrQueueNotFound) {
		return fmt.Errorf("%w: %w", ErrQueueNotFound, err)
	}
	return err
}

func newTask(ti *asynq.TaskInfo) Task {
	return Task{
		ID:            ti.ID,
		Queue:         ti.Queue,
		Type:          ti.Type,
		Payload:       ti.Payload,
		Headers:       ti.Headers,
		State:         taskState(ti.State),
		MaxRetry:      ti.MaxRetry,
		Retried:       ti.Retried,
		LastErr:       ti.LastErr,
		LastFailedAt:  ti.LastFailedAt,
		Timeout:       ti.Timeout,
		Deadline:      ti.Deadline,
		Group:         ti.Group,
		NextProcessAt: ti.NextProcessAt,
		IsOrphaned:    ti.IsOrphaned,
		Retention:     ti.Retention,
		CompletedAt:   ti.CompletedAt,
		Result:        ti.Result,
	}
}

func taskState(s asynq.TaskState) TaskState {
	switch s {
	case asynq.TaskStateActive:
		return TaskActive
	case asynq.TaskStatePending:
		return TaskPending
	case asynq.TaskStateAggregating:
		return TaskAggregating
	case asynq.TaskStateScheduled:
		return TaskScheduled
	case asynq.TaskStateRetry:
		return TaskRetry
	case asynq.TaskStateArchived:
		return TaskArchived
	case asynq.TaskStateCompleted:
		return TaskCompleted
	}
	return 0
}
