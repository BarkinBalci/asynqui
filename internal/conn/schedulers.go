package conn

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hibiken/asynq"
)

// MaxEnqueuePageSize is the largest page of enqueue events.
const MaxEnqueuePageSize = 100

// maxEnqueueEvents is how many enqueue events asynq keeps per scheduler
// entry, newest first (its scheduler trims the history to 1000).
const maxEnqueueEvents = 1000

// taskExistsTTL is how long a task-existence lookup of the enqueue history
// is reused. Task IDs link to the Task page only while the task exists, a
// slow-changing fact compared with the 1s snapshots.
const taskExistsTTL = 10 * time.Second

// ErrPage is returned for a page number outside 1..MaxEnqueuePage(size)
// or a page size outside 1..MaxEnqueuePageSize.
var ErrPage = errors.New("conn: page out of bounds")

// MaxEnqueuePage is the last page of enqueue events of the given size that
// asynq can hold: ceil(1000/size).
func MaxEnqueuePage(size int) int {
	if size < 1 {
		return 0
	}
	return (maxEnqueueEvents + size - 1) / size
}

// schedulerInspector is the part of *asynq.Inspector that the Schedulers
// pages use.
type schedulerInspector interface {
	SchedulerEntries() ([]*asynq.SchedulerEntry, error)
	ListSchedulerEnqueueEvents(entryID string, opts ...asynq.ListOption) ([]*asynq.SchedulerEnqueueEvent, error)
	GetTaskInfo(queue, id string) (*asynq.TaskInfo, error)
}

// SchedulerEntry is a periodic task registered by a running asynq
// scheduler. Its ID is regenerated when the scheduler restarts.
type SchedulerEntry struct {
	ID       string
	Spec     string
	TaskType string
	Payload  []byte
	// Options are the options the entry was registered with, in asynq's
	// order. asynq drops the ones it cannot parse back (TaskID, Group),
	// and records none of the options set on the task itself.
	Options []EntryOption
	// Queue is the entry's Queue option, empty without one. Empty does
	// not mean "default": a Queue option set on the task (asynq.NewTask)
	// is not recorded, so the queue is unknown.
	Queue string
	Next  time.Time
	// Prev is zero when the entry has not enqueued yet.
	Prev time.Time
}

// EntryOption is one option of a scheduler entry: a snake_case name
// ("queue", "max_retry") and its value ("critical", "3", "2m0s").
type EntryOption struct {
	Name  string
	Value string
}

// SchedulerEntries returns the entries of the connection's running
// schedulers, sorted by ID, from the 1s cache when fresh.
//
// The slice is shared between callers, who must not modify it.
func (c *Conn) SchedulerEntries(ctx context.Context) ([]SchedulerEntry, error) {
	return cached(ctx, c.cache, schedulerEntriesKey, cacheTTL, c.fetchSchedulerEntries)
}

// fetchSchedulerEntries reads Inspector.SchedulerEntries.
//
// Cost: one Lua script (listing the live scheduler keys and dropping
// expired ones) and an LRANGE per scheduler.
func (c *Conn) fetchSchedulerEntries(context.Context) ([]SchedulerEntry, error) {
	entries, err := recovered(c.schedulers.SchedulerEntries)
	if err != nil {
		return nil, fmt.Errorf("list scheduler entries: %w", err)
	}
	out := make([]SchedulerEntry, 0, len(entries))
	for _, e := range entries {
		if e == nil {
			continue
		}
		se := SchedulerEntry{ID: e.ID, Spec: e.Spec, Next: e.Next, Prev: e.Prev}
		if e.Task != nil {
			se.TaskType, se.Payload = e.Task.Type(), e.Task.Payload()
		}
		for _, o := range e.Opts {
			if o == nil {
				continue
			}
			eo := entryOption(o)
			se.Options = append(se.Options, eo)
			if o.Type() == asynq.QueueOpt {
				se.Queue = eo.Value
			}
		}
		out = append(out, se)
	}
	slices.SortFunc(out, func(a, b SchedulerEntry) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// entryOption names an option by its asynq type and renders its value.
func entryOption(o asynq.Option) EntryOption {
	var name string
	switch o.Type() {
	case asynq.QueueOpt:
		name = "queue"
	case asynq.MaxRetryOpt:
		name = "max_retry"
	case asynq.TimeoutOpt:
		name = "timeout"
	case asynq.DeadlineOpt:
		name = "deadline"
	case asynq.UniqueOpt:
		name = "unique"
	case asynq.ProcessAtOpt:
		name = "process_at"
	case asynq.ProcessInOpt:
		name = "process_in"
	case asynq.TaskIDOpt:
		name = "task_id"
	case asynq.RetentionOpt:
		name = "retention"
	case asynq.GroupOpt:
		name = "group"
	default:
		name = "option"
	}
	var v string
	switch x := o.Value().(type) {
	case string:
		v = x
	case int:
		v = strconv.Itoa(x)
	case time.Duration:
		v = x.String()
	case time.Time:
		v = x.UTC().Format(time.RFC3339)
	default:
		v = fmt.Sprint(x)
	}
	return EntryOption{Name: name, Value: v}
}

// EnqueueEvent is one task a scheduler entry enqueued.
type EnqueueEvent struct {
	TaskID     string
	EnqueuedAt time.Time
	// TaskExists reports whether the task is still stored in the entry's
	// queue (in any state). It is false when the queue is unknown or the
	// lookup failed.
	TaskExists bool
}

// EnqueuePage is one page of an entry's enqueue events, newest first.
type EnqueuePage struct {
	Events []EnqueueEvent
	// HasNext reports whether an older page follows.
	HasNext bool
}

// enqueueKey is the cache key of one page of enqueue events. The entry ID
// is length-prefixed so no ID and queue pair can collide with another.
func enqueueKey(entryID, queue string, page, size int) cacheKey[EnqueuePage] {
	return cacheKey[EnqueuePage]("enqueue/" + strconv.Itoa(page) + "/" + strconv.Itoa(size) + "/" +
		strconv.Itoa(len(entryID)) + ":" + entryID + "/" + queue)
}

// taskExistsKey is the cache key of one task-existence lookup.
func taskExistsKey(queue, id string) cacheKey[bool] {
	return cacheKey[bool]("task-exists/" + strconv.Itoa(len(queue)) + ":" + queue + "/" + id)
}

// SchedulerEnqueueEvents returns page page (from 1) of size events of the
// scheduler entry entryID, newest first, from the 1s cache when fresh.
// queue is the entry's queue: each event's task is looked up there to
// tell whether it still exists; with queue empty (unknown) none is looked
// up. An unknown entry has no events. A page past MaxEnqueuePage(size) is
// ErrPage.
func (c *Conn) SchedulerEnqueueEvents(ctx context.Context, entryID, queue string, page, size int) (EnqueuePage, error) {
	if size < 1 || size > MaxEnqueuePageSize || page < 1 || page > MaxEnqueuePage(size) {
		return EnqueuePage{}, ErrPage
	}
	return cached(ctx, c.cache, enqueueKey(entryID, queue, page, size), cacheTTL, func(ctx context.Context) (EnqueuePage, error) {
		return c.fetchEnqueueEvents(ctx, entryID, queue, page, size)
	})
}

// fetchEnqueueEvents reads one page with Inspector.ListSchedulerEnqueueEvents
// (a ZREVRANGE), whether an older page follows with a second one-event read
// at the first offset past the page (asynq gives no count), and then
// GetTaskInfo per event (a SISMEMBER and a Lua script each, cached for
// taskExistsTTL), up to queueConcurrency at a time.
func (c *Conn) fetchEnqueueEvents(ctx context.Context, entryID, queue string, page, size int) (EnqueuePage, error) {
	evs, err := recovered(func() ([]*asynq.SchedulerEnqueueEvent, error) {
		return c.schedulers.ListSchedulerEnqueueEvents(entryID, asynq.PageSize(size), asynq.Page(page))
	})
	if err != nil {
		return EnqueuePage{}, fmt.Errorf("list enqueue events: %w", err)
	}
	var out EnqueuePage
	if len(evs) == size && page < MaxEnqueuePage(size) {
		// With a page size of 1, page n is the event at offset n-1.
		next, err := recovered(func() ([]*asynq.SchedulerEnqueueEvent, error) {
			return c.schedulers.ListSchedulerEnqueueEvents(entryID, asynq.PageSize(1), asynq.Page(page*size+1))
		})
		if err != nil {
			return EnqueuePage{}, fmt.Errorf("list enqueue events: %w", err)
		}
		out.HasNext = len(next) > 0
	}
	for _, e := range evs {
		if e != nil {
			out.Events = append(out.Events, EnqueueEvent{TaskID: e.TaskID, EnqueuedAt: e.EnqueuedAt})
		}
	}
	if queue != "" {
		c.markExisting(ctx, queue, out.Events)
	}
	return out, nil
}

// markExisting sets TaskExists on the events whose task is still in queue,
// looking them up up to queueConcurrency at a time. A task or queue that
// is gone is cached as missing; a failed lookup is not cached and counts
// as missing, so the ID is shown without a link.
func (c *Conn) markExisting(ctx context.Context, queue string, events []EnqueueEvent) {
	sem := make(chan struct{}, queueConcurrency)
	var wg sync.WaitGroup
	for i := range events {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			id := events[i].TaskID
			ok, err := cached(ctx, c.cache, taskExistsKey(queue, id), taskExistsTTL, func(context.Context) (bool, error) {
				_, err := recovered(func() (*asynq.TaskInfo, error) { return c.schedulers.GetTaskInfo(queue, id) })
				switch {
				case err == nil:
					return true, nil
				case errors.Is(err, asynq.ErrTaskNotFound), errors.Is(err, asynq.ErrQueueNotFound):
					return false, nil
				}
				return false, err
			})
			if err != nil {
				c.logger.Debug("task lookup failed", "queue", queue, "task", id, "error", err)
			}
			events[i].TaskExists = ok && err == nil
		})
	}
	wg.Wait()
}
