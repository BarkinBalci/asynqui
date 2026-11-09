package web

import (
	"context"
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
