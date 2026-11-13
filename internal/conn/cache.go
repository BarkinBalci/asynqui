package conn

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// cacheTTL is how long a fetched snapshot is served to every viewer.
const cacheTTL = time.Second

// sweepInterval is the most often a cache removes its expired entries.
const sweepInterval = 10 * time.Second

// cacheKey names one snapshot of a connection. Its type parameter is the
// type of the snapshot, so two uses of a key can never disagree on it: keys
// with the same name but different T are different keys.
type cacheKey[T any] string

// Cache keys. History keys are built per queue and range (historyKey),
// enqueue event keys per entry and page (enqueueKey), and the Queue and
// Task pages' keys per queue, state, page and task (queueKey, tasksKey,
// groupsKey, taskKey in tasks.go).
const (
	queuesKey           cacheKey[[]Queue]          = "queues"
	infoKey             cacheKey[*Info]            = "info"
	serversKey          cacheKey[[]Server]         = "servers"
	schedulerEntriesKey cacheKey[[]SchedulerEntry] = "scheduler-entries"
)

// cache holds one connection's recent snapshots, and coalesces concurrent
// fetches of the same key into one.
type cache struct {
	mu      sync.Mutex
	entries map[any]cacheEntry // by cacheKey[T]
	flights map[any]*flight    // fetches in progress, by cacheKey[T]
	// nextSweep is when expired entries are next removed.
	nextSweep time.Time
	// gen counts resets. A fetch stores its result only if no reset
	// happened while it ran, so a value read before an action is never
	// served after it.
	gen uint64
}

type cacheEntry struct {
	val     any
	expires time.Time
}

// flight is one fetch shared by every caller that asked for its key while
// it ran. val and err are written before done is closed.
type flight struct {
	done chan struct{}
	gen  uint64 // cache.gen when the fetch started
	val  any
	err  error
}

func newCache() *cache {
	return &cache{entries: map[any]cacheEntry{}, flights: map[any]*flight{}}
}

// cached returns the value stored under key if it is younger than ttl.
// Otherwise it starts fetch, or joins the one already running for key, and
// stores a successful result for ttl. Errors are not stored, so the next
// call fetches again. A panic in fetch is returned as an error.
//
// A served value is at most ttl plus its fetch time old: ttl counts from
// when the fetch finished.
//
// fetch runs in its own goroutine under ctx without its cancelation, so a
// viewer that goes away does not fail the fetch for the others; fetch must
// be bounded by its own timeouts. A caller whose ctx ends stops waiting and
// gets ctx's error, while the fetch goes on and is stored for later callers.
//
// The value is shared between callers, who must not modify it.
func cached[T any](ctx context.Context, c *cache, key cacheKey[T], ttl time.Duration, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && time.Now().Before(e.expires) {
		c.mu.Unlock()
		return e.val.(T), nil
	}
	f, ok := c.flights[key]
	if !ok {
		f = &flight{done: make(chan struct{}), gen: c.gen}
		c.flights[key] = f
		fctx := context.WithoutCancel(ctx)
		go c.fly(key, f, ttl, func() (any, error) { return fetch(fctx) })
	}
	c.mu.Unlock()

	select {
	case <-f.done:
		if f.err != nil {
			return zero, f.err
		}
		return f.val.(T), nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// fly runs the fetch of flight f, stores a successful result under key and
// releases the waiters.
func (c *cache) fly(key any, f *flight, ttl time.Duration, fetch func() (any, error)) {
	defer close(f.done)
	f.val, f.err = recovered(fetch)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.flights[key] == f {
		delete(c.flights, key)
	}
	if f.err != nil || f.gen != c.gen {
		return
	}
	now := time.Now()
	// Sweep expired entries at most once per sweepInterval, so keys that
	// are not asked for again (a deleted queue's, say) do not pile up.
	if !now.Before(c.nextSweep) {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		c.nextSweep = now.Add(sweepInterval)
	}
	c.entries[key] = cacheEntry{val: f.val, expires: now.Add(ttl)}
}

// reset drops every stored value and detaches the fetches in flight: they
// still answer the callers waiting on them, but store nothing, and the
// next caller starts a fresh fetch. Actions that change a connection call
// it so the next read sees their effect.
func (c *cache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	clear(c.entries)
	clear(c.flights)
}

// recovered calls fn and returns a panic in it as an error, since nothing
// up the stack of a background goroutine could recover it.
func recovered[T any](fn func() (T, error)) (v T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("conn: panic: %v", r)
		}
	}()
	return fn()
}
