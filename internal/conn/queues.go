package conn

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hibiken/asynq"
	"golang.org/x/sync/errgroup"
)

// queueConcurrency bounds the per-queue Inspector calls in flight per
// connection.
const queueConcurrency = 8

// MaxHistoryDays is the longest History range.
const MaxHistoryDays = 90

// Errors of the queue reads and actions. They wrap asynq's own where asynq
// has one, so errors.Is works with either.
var (
	// ErrQueueNotFound is returned for a queue the connection does not
	// have.
	ErrQueueNotFound = errors.New("conn: queue not found")
	// ErrQueueNotEmpty is returned by DeleteQueue for a queue that still
	// holds tasks.
	ErrQueueNotEmpty = errors.New("conn: queue not empty")
	// ErrHistoryRange is returned by History for a day count outside
	// 1..MaxHistoryDays.
	ErrHistoryRange = errors.New("conn: history range out of bounds")
)

// queueInspector is the part of *asynq.Inspector that the queue reads and
// actions use.
type queueInspector interface {
	Queues() ([]string, error)
	GetQueueInfo(queue string) (*asynq.QueueInfo, error)
	History(queue string, n int) ([]*asynq.DailyStats, error)
	PauseQueue(queue string) error
	UnpauseQueue(queue string) error
	DeleteQueue(queue string, force bool) error
}

// Queue is one queue of a connection, from asynq's QueueInfo.
type Queue struct {
	Name   string
	Paused bool
	// Size is asynq's QueueInfo.Size, which in asynq v0.26.0 counts every
	// stored task, completed and aggregating included (its doc comment leaves
	// Completed out).
	Size      int
	Pending   int
	Active    int
	Scheduled int
	Retry     int
	Archived  int
	// ProcessedToday and FailedToday count asynq's current UTC day.
	// Processed includes failed tasks.
	ProcessedToday int
	FailedToday    int
	// Latency is how long the oldest pending task has waited.
	Latency time.Duration
	// MemoryUsage is asynq's sampled estimate of the bytes the queue
	// takes in Redis.
	MemoryUsage int64
}

// Queues returns every queue of the connection, sorted by name, from the
// cache. Any failing queue fails the whole list. The slice is shared between
// callers, who must not modify it.
func (c *Conn) Queues(ctx context.Context) ([]Queue, error) {
	return cached(ctx, c.cache, queuesKey, cacheTTL, c.fetchQueues)
}

// fetchQueues lists the queues and reads up to queueConcurrency of them at a
// time. After the first failure it starts no further reads. The Inspector
// takes no context, so ctx is unused and only the go-redis socket timeouts
// bound the calls (see newConn). GetQueueInfo runs asynq's memoryUsage script,
// which blocks Redis while it samples the queue's keys; the cache keeps that
// to one round per connection per cacheTTL.
func (c *Conn) fetchQueues(context.Context) ([]Queue, error) {
	names, err := c.queues.Queues()
	if err != nil {
		return nil, fmt.Errorf("list queues: %w", err)
	}
	slices.Sort(names)
	infos, err := eachQueue(names, c.queues.GetQueueInfo)
	if err != nil {
		return nil, err
	}
	out := make([]Queue, len(infos))
	for i, qi := range infos {
		out[i] = newQueue(qi)
	}
	return out, nil
}

func newQueue(qi *asynq.QueueInfo) Queue {
	return Queue{
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
	}
}

// eachQueue calls fn for each name, at most queueConcurrency at a time,
// and returns the results in order. After the first failure it starts no
// further calls and returns that failure, naming its queue. A panic in fn
// is returned as an error.
func eachQueue[T any](names []string, fn func(string) (T, error)) ([]T, error) {
	out := make([]T, len(names))
	var (
		g      errgroup.Group
		failed atomic.Bool
	)
	g.SetLimit(queueConcurrency)
	for i, q := range names {
		g.Go(func() error {
			if failed.Load() {
				return nil // another queue failed: start no more calls
			}
			v, err := recovered(func() (T, error) { return fn(q) })
			if err != nil {
				failed.Store(true)
				return fmt.Errorf("queue %s: %w", q, err)
			}
			out[i] = v
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// DailyStat is one UTC day of a queue's (or a connection's) counters.
type DailyStat struct {
	// Date is the day, at 00:00 UTC.
	Date time.Time
	// Processed includes failed tasks.
	Processed int
	Failed    int
}

// historyKey is the cache key of one History read.
func historyKey(queue string, days int) cacheKey[[]DailyStat] {
	return cacheKey[[]DailyStat]("history/" + strconv.Itoa(days) + "/" + queue)
}

// History returns the daily counters of the last days days, oldest first,
// ending today (UTC), from the cache. An empty queue sums every queue of the
// connection; a connection without queues has days of zeros. A queue the
// connection does not have is ErrQueueNotFound, and days outside
// 1..MaxHistoryDays is ErrHistoryRange. The slice is shared between callers,
// who must not modify it.
func (c *Conn) History(ctx context.Context, queue string, days int) ([]DailyStat, error) {
	if days < 1 || days > MaxHistoryDays {
		return nil, ErrHistoryRange
	}
	return cached(ctx, c.cache, historyKey(queue, days), cacheTTL, func(context.Context) ([]DailyStat, error) {
		return c.fetchHistory(queue, days)
	})
}

// fetchHistory reads Inspector.History per queue, up to queueConcurrency at a
// time, and sums them by UTC day. The days are today and the days-1 before it
// by our clock; a counter that asynq dated outside them (its clock on the
// other side of midnight) is dropped. The result is oldest first.
func (c *Conn) fetchHistory(queue string, days int) ([]DailyStat, error) {
	names, err := c.queues.Queues()
	if err != nil {
		return nil, fmt.Errorf("list queues: %w", err)
	}
	if queue != "" {
		if !slices.Contains(names, queue) {
			return nil, fmt.Errorf("%w: %q", ErrQueueNotFound, queue)
		}
		names = []string{queue}
	}

	out := make([]DailyStat, days)
	index := make(map[time.Time]int, days)
	today := utcDay(c.now())
	for i := range out {
		out[i].Date = today.AddDate(0, 0, i-days+1)
		index[out[i].Date] = i
	}
	per, err := eachQueue(names, func(q string) ([]*asynq.DailyStats, error) { return c.queues.History(q, days) })
	if err != nil {
		return nil, err
	}
	for _, stats := range per {
		for _, s := range stats {
			if i, ok := index[utcDay(s.Date)]; ok {
				out[i].Processed += s.Processed
				out[i].Failed += s.Failed
			}
		}
	}
	return out, nil
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// PauseQueue pauses the queue. It returns ErrQueueNotFound for a queue the
// connection does not have; pausing a paused queue is an error from asynq. On
// success the connection's cached reads are dropped.
func (c *Conn) PauseQueue(ctx context.Context, queue string) error {
	return c.queueAction(ctx, queue, c.queues.PauseQueue)
}

// UnpauseQueue resumes a paused queue; see PauseQueue.
func (c *Conn) UnpauseQueue(ctx context.Context, queue string) error {
	return c.queueAction(ctx, queue, c.queues.UnpauseQueue)
}

// DeleteQueue deletes an empty queue (asynq's DeleteQueue without force). It
// returns ErrQueueNotFound or ErrQueueNotEmpty, each also wrapping asynq's
// error when asynq reported it. On success the cached reads are dropped. Empty
// means a QueueInfo.Size of 0, read fresh just before the delete: asynq's own
// check leaves out completed and aggregating tasks, whose keys a delete would
// orphan.
func (c *Conn) DeleteQueue(ctx context.Context, queue string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := recovered(func() (struct{}, error) {
		// GetQueueInfo does not report a missing queue with asynq's
		// exported error.
		if err := c.checkQueue(queue); err != nil {
			return struct{}{}, err
		}
		qi, err := c.queues.GetQueueInfo(queue)
		if err != nil {
			return struct{}{}, err
		}
		if qi.Size > 0 {
			return struct{}{}, fmt.Errorf("%w: %q has %d tasks", ErrQueueNotEmpty, queue, qi.Size)
		}
		return struct{}{}, c.queues.DeleteQueue(queue, false)
	})
	switch {
	case errors.Is(err, ErrQueueNotEmpty), errors.Is(err, ErrQueueNotFound):
		return err
	case errors.Is(err, asynq.ErrQueueNotFound):
		return fmt.Errorf("%w: %w", ErrQueueNotFound, err)
	case errors.Is(err, asynq.ErrQueueNotEmpty):
		return fmt.Errorf("%w: %w", ErrQueueNotEmpty, err)
	case err != nil:
		return fmt.Errorf("delete queue %s: %w", queue, err)
	}
	c.cache.reset()
	return nil
}

// queueAction runs a pause or resume after checking that the queue exists:
// asynq's PauseQueue does not, and would mark a queue that does not exist
// as paused.
func (c *Conn) queueAction(ctx context.Context, queue string, act func(string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := recovered(func() (struct{}, error) {
		if err := c.checkQueue(queue); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, act(queue)
	})
	if err != nil {
		return err
	}
	c.cache.reset()
	return nil
}
