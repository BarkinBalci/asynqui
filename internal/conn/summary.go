package conn

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/hibiken/asynq"
)

// summaryConcurrency bounds the GetQueueInfo calls in flight per connection.
const summaryConcurrency = 8

// queueInspector is the part of *asynq.Inspector that Summary uses.
type queueInspector interface {
	Queues() ([]string, error)
	GetQueueInfo(queue string) (*asynq.QueueInfo, error)
}

// Summary is a connection's totals across its queues, for the Overview.
type Summary struct {
	Queues  int
	Pending int
	Active  int
	Retry   int
	// ProcessedToday and FailedToday count asynq's current UTC day.
	// Processed includes failed tasks.
	ProcessedToday int
	FailedToday    int
	// OldestPending is the largest QueueInfo.Latency: how long the oldest
	// pending task of any queue has waited. Zero when nothing is pending.
	OldestPending time.Duration
}

// Summary returns the connection's totals, from the 1s cache when fresh.
func (c *Conn) Summary(ctx context.Context) (Summary, error) {
	return cached(ctx, c.cache, "summary", cacheTTL, c.fetchSummary)
}

// fetchSummary lists the queues and reads them in parallel.
//
// Cost: GetQueueInfo sends three commands per queue (SISMEMBER, then two
// Lua scripts), and the second cannot be skipped: asynq's rdb.CurrentStats
// always ends with its memoryUsage script for QueueInfo.MemoryUsage, which
// Summary does not use. That script runs MEMORY USAGE on up to 20 task
// hashes from each of the pending, active, scheduled, retry, archived and
// completed sets and on each set itself (up to 126 calls), plus up to 20
// tasks from each of the first 5 aggregation groups (100 more) and a ZCARD
// per group, all inside one script that blocks Redis while it runs. The
// Inspector has no cheaper per-queue read and its RDB is internal, so
// avoiding it would mean copying asynq's key layout. The 1s cache keeps the
// cost to one round per connection per second, whatever the viewer count.
func (c *Conn) fetchSummary(ctx context.Context) (Summary, error) {
	names, err := call(ctx, c.timeout, c.queues.Queues)
	if err != nil {
		return Summary{}, fmt.Errorf("list queues: %w", err)
	}
	infos := make([]*asynq.QueueInfo, len(names))
	errs := make([]error, len(names))
	sem := make(chan struct{}, summaryConcurrency)
	var wg sync.WaitGroup
	for i, q := range names {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			infos[i], errs[i] = call(ctx, c.timeout, func() (*asynq.QueueInfo, error) {
				return c.queues.GetQueueInfo(q)
			})
		})
	}
	wg.Wait()

	if slices.ContainsFunc(errs, func(err error) bool { return err != nil }) {
		// A queue deleted after the listing fails GetQueueInfo, which does
		// not wrap asynq.ErrQueueNotFound. Ignore the failures of queues
		// that a second listing no longer has.
		now, err := call(ctx, c.timeout, c.queues.Queues)
		if err != nil {
			return Summary{}, fmt.Errorf("list queues: %w", err)
		}
		for i, err := range errs {
			if err != nil && slices.Contains(now, names[i]) {
				return Summary{}, fmt.Errorf("queue %s: %w", names[i], err)
			}
		}
	}

	var s Summary
	for _, qi := range infos {
		if qi == nil {
			continue
		}
		s.Queues++
		s.Pending += qi.Pending
		s.Active += qi.Active
		s.Retry += qi.Retry
		s.ProcessedToday += qi.Processed
		s.FailedToday += qi.Failed
		s.OldestPending = max(s.OldestPending, qi.Latency)
	}
	return s, nil
}

// call runs fn, which cannot be canceled, and returns its result, or ctx's
// error once ctx ends or timeout passes, whichever is first. The Inspector
// takes no context, so this is how its calls get a deadline. An abandoned
// fn keeps running in the background until the socket timeouts of its
// Redis client stop it; its result is dropped.
func call[T any](ctx context.Context, timeout time.Duration, fn func() (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1) // buffered, so an abandoned fn never blocks
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}
