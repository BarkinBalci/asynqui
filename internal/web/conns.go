package web

import (
	"context"
	"errors"
	"time"
)

// ConnectionSource is what the web layer needs from the connection
// registry (package conn). It is declared here, where it is used, so tests
// can pass a fake.
type ConnectionSource interface {
	// Connections lists every configured connection in config order, each
	// with its cached health. It does no Redis I/O.
	Connections() []Connection
	// Summary returns the Overview numbers of one connection. It may be
	// served from a short-lived cache.
	Summary(ctx context.Context, name string) (Summary, error)
	// Recheck runs a health check now, stores it and returns it.
	Recheck(ctx context.Context, name string) Health

	// Queues lists the connection's queues, sorted by name. It may be
	// served from a short-lived cache shared with Summary.
	Queues(ctx context.Context, name string) ([]Queue, error)
	// History returns the daily counters of the last days days, oldest
	// first, for one queue or, with queue empty, summed over all of them.
	// An unknown queue is ErrQueueNotFound.
	History(ctx context.Context, name, queue string, days int) ([]DailyStat, error)
	// PauseQueue, ResumeQueue and DeleteQueue (empty queues only) act on
	// one queue. They return errors wrapping ErrQueueNotFound or
	// ErrQueueNotEmpty where those apply.
	PauseQueue(ctx context.Context, name, queue string) error
	ResumeQueue(ctx context.Context, name, queue string) error
	DeleteQueue(ctx context.Context, name, queue string) error
}

// Errors a ConnectionSource wraps so handlers can tell causes apart; the
// templates map them to copy.
var (
	ErrQueueNotFound = errors.New("web: queue not found")
	ErrQueueNotEmpty = errors.New("web: queue not empty")
)

// Queue is one queue of a connection.
type Queue struct {
	Name   string
	Paused bool
	// Size is what asynq counts: every task but the completed ones.
	Size           int
	Pending        int
	Active         int
	Scheduled      int
	Retry          int
	Archived       int
	ProcessedToday int
	FailedToday    int
	Latency        time.Duration
	// MemoryUsage is asynq's sampled estimate, in bytes.
	MemoryUsage int64
}

// DailyStat is one UTC day of counters.
type DailyStat struct {
	Date      time.Time
	Processed int
	Failed    int
}

// Connection is one configured connection.
type Connection struct {
	Name string
	// Addr is the display address, "host:port/db".
	Addr   string
	Health Health
}

// HealthState is the result of the last health check.
type HealthState int

// Health states. HealthUnknown means the first check has not finished.
const (
	HealthUnknown HealthState = iota
	HealthUp
	HealthDown
)

// Health is a connection's cached health.
type Health struct {
	State HealthState
	// Err is why the last check failed (HealthDown only). Its text is shown
	// on the Overview card.
	Err error
	// CheckedAt is when the last check finished; zero while unknown.
	CheckedAt time.Time
}

// Summary holds the Overview numbers of one connection.
type Summary struct {
	Queues         int
	Pending        int
	Active         int
	Retry          int
	ProcessedToday int
	FailedToday    int
	// OldestPending is the maximum QueueInfo.Latency across queues.
	OldestPending time.Duration
}

// lookup finds a connection by name.
func lookup(src ConnectionSource, name string) (Connection, bool) {
	for _, c := range src.Connections() {
		if c.Name == name {
			return c, true
		}
	}
	return Connection{}, false
}
