package auth

import (
	"container/list"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	loginAttempts = 5
	loginWindow   = time.Minute
	// limiterCap bounds memory per limiter. When full, the least recently
	// seen key is evicted.
	limiterCap = 10000
)

// limiter allows max attempts per key per sliding window. Keys are client
// IPs (IPv6 by /64) or folded usernames. Behind a reverse proxy every user
// shares the proxy's IP, so one user's failures can lock out the others:
// that is the documented trade-off of keying on RemoteAddr only.
type limiter struct {
	max       int
	window    time.Duration
	cap       int
	now       func() time.Time
	mu        sync.Mutex
	entries   map[string]*list.Element // of *limitEntry
	order     *list.List               // front = most recently seen
	lastSweep time.Time
}

type limitEntry struct {
	key  string
	hits []time.Time // ascending
}

func newLimiter(maxAttempts int, window time.Duration, capacity int, now func() time.Time) *limiter {
	return &limiter{
		max: maxAttempts, window: window, cap: capacity, now: now,
		entries: map[string]*list.Element{}, order: list.New(),
	}
}

// allow records an attempt for key and reports whether it is within the
// limit. If not, retry is how long until the next attempt would be allowed.
func (l *limiter) allow(key string) (ok bool, retry time.Duration) {
	now := l.now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= l.window {
		l.lastSweep = now
		for el := l.order.Back(); el != nil; {
			prev := el.Prev()
			if e := el.Value.(*limitEntry); !e.hits[len(e.hits)-1].After(cutoff) {
				l.order.Remove(el)
				delete(l.entries, e.key)
			}
			el = prev
		}
	}

	el, found := l.entries[key]
	if !found {
		if l.order.Len() >= l.cap {
			oldest := l.order.Back()
			l.order.Remove(oldest)
			delete(l.entries, oldest.Value.(*limitEntry).key)
		}
		el = l.order.PushFront(&limitEntry{key: key})
		l.entries[key] = el
	} else {
		l.order.MoveToFront(el)
	}
	e := el.Value.(*limitEntry)
	i := 0
	for i < len(e.hits) && !e.hits[i].After(cutoff) {
		i++
	}
	e.hits = e.hits[i:]
	if len(e.hits) >= l.max {
		return false, e.hits[0].Sub(cutoff)
	}
	e.hits = append(e.hits, now)
	return true, 0
}

func (l *limiter) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}

// clientKey is the rate-limit key for a peer: its IP, or its /64 for IPv6.
func clientKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return "invalid"
	}
	a = a.Unmap().WithZone("")
	bits := a.BitLen()
	if a.Is6() {
		bits = 64
	}
	p, _ := a.Prefix(bits)
	return p.String()
}
