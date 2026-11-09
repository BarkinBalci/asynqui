package conn

import (
	"context"
	"fmt"
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
	// OldestPending is the largest QueueInfo.Latency of the queues that are
	// not paused: how long the oldest pending task of a running queue has
	// waited. Zero when none is pending, or every queue is paused.
	OldestPending time.Duration
}

// Summary returns the connection's totals, from the 1s cache when fresh.
//
// Any failing queue fails the whole Summary, including a queue deleted
// between the listing and its read: GetQueueInfo then fails with an error
// that does not wrap asynq.ErrQueueNotFound. Errors are not cached, so the
// next poll recovers.
func (c *Conn) Summary(ctx context.Context) (Summary, error) {
	return cached(ctx, c.cache, summaryKey, cacheTTL, c.fetchSummary)
}

// fetchSummary lists the queues and reads up to summaryConcurrency of them
// at a time. After the first failure it starts no further reads.
//
// The Inspector takes no context: its calls are bounded only by the
// go-redis socket timeouts (see newConn), and ctx is unused.
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
func (c *Conn) fetchSummary(context.Context) (Summary, error) {
	names, err := c.queues.Queues()
	if err != nil {
		return Summary{}, fmt.Errorf("list queues: %w", err)
	}

	infos := make([]*asynq.QueueInfo, len(names))
	var (
		mu       sync.Mutex
		firstErr error
	)
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return firstErr != nil
	}
	sem := make(chan struct{}, summaryConcurrency)
	var wg sync.WaitGroup
	for i, q := range names {
		sem <- struct{}{}
		if failed() {
			<-sem
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			qi, err := recovered(func() (*asynq.QueueInfo, error) { return c.queues.GetQueueInfo(q) })
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("queue %s: %w", q, err)
				}
				mu.Unlock()
				return
			}
			infos[i] = qi
		})
	}
	wg.Wait()
	if firstErr != nil {
		return Summary{}, firstErr
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
		if !qi.Paused {
			s.OldestPending = max(s.OldestPending, qi.Latency)
		}
	}
	return s, nil
}
