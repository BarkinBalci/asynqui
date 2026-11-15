package conn

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// healthInterval is the time between two background health checks of a
// connection.
const healthInterval = 10 * time.Second

// backendEvery is how many health checks pass between two backend detections
// of a connection that stays up.
const backendEvery = 6

// State is the reachability of a connection.
type State int

// States. The zero value is StateUnknown.
const (
	// StateUnknown means no health check has finished yet.
	StateUnknown State = iota
	// StateUp means the last PING succeeded.
	StateUp
	// StateDown means the last PING failed.
	StateDown
)

// String returns "unknown", "up" or "down".
func (s State) String() string {
	switch s {
	case StateUp:
		return "up"
	case StateDown:
		return "down"
	default:
		return "unknown"
	}
}

// Health is the result of the last finished health check.
type Health struct {
	State State
	// Err is why the check failed; nil unless State is StateDown.
	Err error
	// CheckedAt is when the check finished; zero while State is
	// StateUnknown.
	CheckedAt time.Time
	// Latency is how long the PING took, including any dial.
	Latency time.Duration
}

// pinger sends a PING.
type pinger interface {
	Ping(ctx context.Context) error
}

type redisPinger struct{ c redis.UniversalClient }

func (p redisPinger) Ping(ctx context.Context) error { return p.c.Ping(ctx).Err() }

// Health returns the result of the last finished health check. It never
// does any I/O.
func (c *Conn) Health() Health {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.health
}

// Recheck runs a health check now, stores its result and returns the health
// then stored: its own result, unless an overlapping check that started
// later stored first.
//
// If ctx ends first it returns ctx's error and stores nothing, so a
// canceled request never marks the connection down. After Registry.Close
// it returns ErrClosed without checking.
func (c *Conn) Recheck(ctx context.Context) (Health, error) {
	return c.check(ctx)
}

// healthLoop checks the connection now and then every interval until ctx is
// canceled; a check that outlasts the interval delays the next one. It also
// detects the backend each time the connection becomes reachable while the
// backend is unknown, and every backendEvery-th check, in case another server
// took over the address.
func (c *Conn) healthLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	prev := StateUnknown
	for tick := 0; ; tick++ {
		h, err := c.check(ctx) // only fails once ctx is done or Close ran
		if err == nil {
			_, known := c.Backend()
			if h.State == StateUp && (prev != StateUp || !known || tick%backendEvery == 0) {
				c.detectBackend(ctx)
			}
			prev = h.State
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// check sends one PING, bounded by c.timeout, and stores the result unless
// ctx ended first or the registry closed. Of two overlapping checks, the
// one that started last wins, whichever finishes first: it saw Redis more
// recently. It returns the health stored after it.
func (c *Conn) check(ctx context.Context) (Health, error) {
	if c.closed.Load() {
		return Health{}, ErrClosed
	}
	pctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	start := time.Now()
	err := c.pinger.Ping(pctx)
	end := time.Now()
	if ctx.Err() != nil {
		return Health{}, ctx.Err()
	}
	if c.closed.Load() {
		return Health{}, ErrClosed // Close ran meanwhile; the PING may have failed for it
	}
	h := Health{State: StateUp, CheckedAt: end, Latency: end.Sub(start)}
	if err != nil {
		h.State, h.Err = StateDown, err
	}

	c.mu.Lock()
	if start.Before(c.healthStarted) {
		// Stale: a check that started later already stored its result.
		h = c.health
		c.mu.Unlock()
		return h, nil
	}
	prev := c.health
	c.health, c.healthStarted = h, start
	c.mu.Unlock()

	if h.State != prev.State {
		if h.State == StateDown {
			c.logger.Warn("connection unreachable", slog.String("error", err.Error()))
		} else {
			c.logger.Info("connection reachable", slog.Duration("latency", h.Latency))
		}
	}
	return h, nil
}
