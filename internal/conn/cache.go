package conn

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// cacheTTL is how long a fetched snapshot is served to every viewer.
const cacheTTL = time.Second

// cache holds one connection's recent snapshots by query key, and coalesces
// concurrent fetches of the same key into one.
type cache struct {
	group singleflight.Group

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	val     any
	expires time.Time
}

func newCache() *cache {
	return &cache{entries: map[string]cacheEntry{}}
}

func (c *cache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !time.Now().Before(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return e.val, true
}

func (c *cache) set(key string, val any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{val: val, expires: time.Now().Add(ttl)}
}

// cached returns the value stored under key if it is younger than ttl.
// Otherwise it calls fetch, shared with every concurrent caller of the same
// key, and stores a successful result for ttl. Errors are not stored.
//
// fetch runs under ctx without its cancelation, so a viewer that goes away
// does not fail the fetch for the others; fetch must bound its own calls.
// A caller whose ctx ends stops waiting and gets ctx's error.
//
// Every use of a key must have the same T. The value is shared between
// callers, so they must not modify it.
func cached[T any](ctx context.Context, c *cache, key string, ttl time.Duration, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	if v, ok := c.get(key); ok {
		return v.(T), nil
	}
	fctx := context.WithoutCancel(ctx)
	ch := c.group.DoChan(key, func() (any, error) {
		// A flight that finished between our get and DoChan stored a value.
		if v, ok := c.get(key); ok {
			return v, nil
		}
		v, err := fetch(fctx)
		if err != nil {
			return nil, err
		}
		c.set(key, v, ttl)
		return v, nil
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return zero, r.Err
		}
		return r.Val.(T), nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
