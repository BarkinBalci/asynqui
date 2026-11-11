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

// queueConcurrency bounds the per-queue Inspector calls in flight per
// connection (GetQueueInfo, History).
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
	// Size is asynq's QueueInfo.Size. In asynq v0.26.0 it counts every
	// stored task: Pending + Active + Scheduled + Retry + Archived +
	// Completed + Aggregating (its doc comment leaves Completed out).
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
// 1s cache when fresh. It is shared with Summary. Any failing queue fails
// the whole list; errors are not cached.
//
// The slice is shared between callers, who must not modify it.
func (c *Conn) Queues(ctx context.Context) ([]Queue, error) {
	return cached(ctx, c.cache, queuesKey, cacheTTL, c.fetchQueues)
}

// fetchQueues lists the queues and reads up to queueConcurrency of them at
// a time. After the first failure it starts no further reads.
//
// The Inspector takes no context: its calls are bounded only by the
// go-redis socket timeouts (see newConn), and ctx is unused.
//
// Cost: GetQueueInfo sends three commands per queue (SISMEMBER, then two
// Lua scripts). The second is asynq's memoryUsage script for
// QueueInfo.MemoryUsage, which the Connection page shows: it runs MEMORY
// USAGE on up to 20 task hashes from each of the pending, active,
// scheduled, retry, archived and completed sets and on each set itself (up
// to 126 calls), plus up to 20 tasks from each of the first 5 aggregation
// groups (100 more) and a ZCARD per group, all inside one script that
// blocks Redis while it runs. The Inspector has no cheaper per-queue read
// and its RDB is internal, so avoiding it would mean copying asynq's key
// layout. The 1s cache keeps the cost to one round per connection per
// second, whatever the viewer count.
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
	out := make([]Queue, 0, len(infos))
	for _, qi := range infos {
		if qi == nil {
			continue
		}
		out = append(out, Queue{
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
		})
	}
	return out, nil
}

// eachQueue calls fn for each name, at most queueConcurrency at a time,
// and returns the results in order. After the first failure it starts no
// further calls and returns that failure, naming its queue. A panic in fn
// is returned as an error.
func eachQueue[T any](names []string, fn func(string) (T, error)) ([]T, error) {
	out := make([]T, len(names))
	var (
		mu       sync.Mutex
		firstErr error
	)
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return firstErr != nil
	}
	sem := make(chan struct{}, queueConcurrency)
	var wg sync.WaitGroup
	for i, q := range names {
		sem <- struct{}{}
		if failed() {
			<-sem
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			v, err := recovered(func() (T, error) { return fn(q) })
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("queue %s: %w", q, err)
				}
				mu.Unlock()
				return
			}
			out[i] = v
		})
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
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
// ending today (UTC), from the 1s cache when fresh. An empty queue sums
// every queue of the connection; a connection without queues has days of
// zeros. A queue the connection does not have is ErrQueueNotFound, and
// days outside 1..MaxHistoryDays is ErrHistoryRange.
//
// The slice is shared between callers, who must not modify it.
func (c *Conn) History(ctx context.Context, queue string, days int) ([]DailyStat, error) {
	if days < 1 || days > MaxHistoryDays {
		return nil, ErrHistoryRange
	}
	return cached(ctx, c.cache, historyKey(queue, days), cacheTTL, func(context.Context) ([]DailyStat, error) {
		return c.fetchHistory(queue, days)
	})
}

// fetchHistory reads Inspector.History per queue (one SISMEMBER and one
// Lua script reading 2*days counters each), up to queueConcurrency at a
// time, and sums them by UTC day. The days are today and the days-1
// before it by our clock; a counter that asynq dated outside them (its
// clock on the other side of midnight) is dropped. The result is oldest
// first.
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
			if s == nil {
				continue
			}
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

// PauseQueue pauses the queue: asynq servers stop taking its tasks. It
// returns ErrQueueNotFound for a queue the connection does not have.
// Pausing a paused queue is an error from asynq. On success the
// connection's cached reads are dropped, so the next read shows the
// change.
func (c *Conn) PauseQueue(ctx context.Context, queue string) error {
	return c.queueAction(ctx, queue, c.queues.PauseQueue)
}

// UnpauseQueue resumes a paused queue; see PauseQueue.
func (c *Conn) UnpauseQueue(ctx context.Context, queue string) error {
	return c.queueAction(ctx, queue, c.queues.UnpauseQueue)
}

// DeleteQueue deletes an empty queue (asynq's DeleteQueue without force).
// It returns ErrQueueNotFound or ErrQueueNotEmpty, each also wrapping
// asynq's error. On success the cached reads are dropped.
func (c *Conn) DeleteQueue(ctx context.Context, queue string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := recovered(func() (struct{}, error) { return struct{}{}, c.queues.DeleteQueue(queue, false) })
	switch {
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
	if strings.TrimSpace(queue) == "" {
		return fmt.Errorf("%w: %q", ErrQueueNotFound, queue)
	}
	_, err := recovered(func() (struct{}, error) {
		names, err := c.queues.Queues()
		if err != nil {
			return struct{}{}, fmt.Errorf("list queues: %w", err)
		}
		if !slices.Contains(names, queue) {
			return struct{}{}, fmt.Errorf("%w: %q", ErrQueueNotFound, queue)
		}
		return struct{}{}, act(queue)
	})
	if err != nil {
		return err
	}
	c.cache.reset()
	return nil
}
