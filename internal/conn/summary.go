package conn

import (
	"context"
	"time"
)

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

// Summary returns the connection's totals. It is computed from Queues, so
// the Overview and the Connection page share one fetch per second.
//
// Any failing queue fails the whole Summary, including a queue deleted
// between the listing and its read: GetQueueInfo then fails with an error
// that does not wrap asynq.ErrQueueNotFound. Errors are not cached, so the
// next poll recovers.
func (c *Conn) Summary(ctx context.Context) (Summary, error) {
	qs, err := c.Queues(ctx)
	if err != nil {
		return Summary{}, err
	}
	return summarize(qs), nil
}

func summarize(qs []Queue) Summary {
	var s Summary
	for _, q := range qs {
		s.Queues++
		s.Pending += q.Pending
		s.Active += q.Active
		s.Retry += q.Retry
		s.ProcessedToday += q.ProcessedToday
		s.FailedToday += q.FailedToday
		if !q.Paused {
			s.OldestPending = max(s.OldestPending, q.Latency)
		}
	}
	return s
}
