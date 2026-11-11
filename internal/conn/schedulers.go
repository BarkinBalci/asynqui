package conn

import (
	"cmp"
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

// defaultQueue is the queue asynq enqueues to without a Queue option.
const defaultQueue = "default"

// MaxEnqueuePageSize is the largest page of enqueue events.
const MaxEnqueuePageSize = 100

// ErrPage is returned for a page number below 1 or a page size outside
// 1..MaxEnqueuePageSize.
var ErrPage = errors.New("conn: page out of bounds")

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
	// Options are the entry's task options as asynq writes them
	// ("Queue(\"reports\")", "MaxRetry(3)"), in its order. asynq drops
	// the ones it cannot parse back.
	Options []string
	// Queue is the queue the entry enqueues to.
	Queue string
	Next  time.Time
	// Prev is zero when the entry has not enqueued yet.
	Prev time.Time
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
		se := SchedulerEntry{ID: e.ID, Spec: e.Spec, Queue: defaultQueue, Next: e.Next, Prev: e.Prev}
		if e.Task != nil {
			se.TaskType, se.Payload = e.Task.Type(), e.Task.Payload()
		}
		for _, o := range e.Opts {
			if o == nil {
				continue
			}
			se.Options = append(se.Options, o.String())
			if q, ok := o.Value().(string); ok && o.Type() == asynq.QueueOpt {
				se.Queue = q
			}
		}
		out = append(out, se)
	}
	slices.SortFunc(out, func(a, b SchedulerEntry) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// EnqueueEvent is one task a scheduler entry enqueued.
type EnqueueEvent struct {
	TaskID     string
	EnqueuedAt time.Time
	// TaskExists reports whether the task is still stored in the entry's
	// queue (in any state).
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

// SchedulerEnqueueEvents returns page page (from 1) of size events of the
// scheduler entry entryID, newest first, from the 1s cache when fresh.
// queue is the entry's queue: each event's task is looked up there to
// tell whether it still exists. An unknown entry has no events.
func (c *Conn) SchedulerEnqueueEvents(ctx context.Context, entryID, queue string, page, size int) (EnqueuePage, error) {
	if page < 1 || size < 1 || size > MaxEnqueuePageSize {
		return EnqueuePage{}, ErrPage
	}
	return cached(ctx, c.cache, enqueueKey(entryID, queue, page, size), cacheTTL, func(context.Context) (EnqueuePage, error) {
		return c.fetchEnqueueEvents(entryID, queue, page, size)
	})
}

// fetchEnqueueEvents reads one page with Inspector.ListSchedulerEnqueueEvents
// (a ZREVRANGE), whether an older page follows with a second one-event read
// at the first offset past the page, and then GetTaskInfo per event (a
// SISMEMBER and a Lua script each), up to queueConcurrency at a time.
// asynq keeps the newest 1000 events per entry.
func (c *Conn) fetchEnqueueEvents(entryID, queue string, page, size int) (EnqueuePage, error) {
	evs, err := recovered(func() ([]*asynq.SchedulerEnqueueEvent, error) {
		return c.schedulers.ListSchedulerEnqueueEvents(entryID, asynq.PageSize(size), asynq.Page(page))
	})
	if err != nil {
		return EnqueuePage{}, fmt.Errorf("list enqueue events: %w", err)
	}
	var out EnqueuePage
	if len(evs) == size {
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
	if err := c.markExisting(queue, out.Events); err != nil {
		return EnqueuePage{}, err
	}
	return out, nil
}

// markExisting sets TaskExists on the events whose task is still in queue,
// looking them up up to queueConcurrency at a time. A task or queue that
// is gone is not an error; any other failure is.
func (c *Conn) markExisting(queue string, events []EnqueueEvent) error {
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, queueConcurrency)
	for i := range events {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			_, err := recovered(func() (*asynq.TaskInfo, error) { return c.schedulers.GetTaskInfo(queue, events[i].TaskID) })
			switch {
			case err == nil:
				events[i].TaskExists = true
			case errors.Is(err, asynq.ErrTaskNotFound), errors.Is(err, asynq.ErrQueueNotFound):
			default:
				mu.Lock()
				firstErr = cmp.Or(firstErr, fmt.Errorf("task %s: %w", events[i].TaskID, err))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return firstErr
}
