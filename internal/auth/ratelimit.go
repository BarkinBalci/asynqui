package auth

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	loginAttempts = 5
	loginWindow   = time.Minute
	// sweepAt bounds memory: once this many clients are tracked, stale
	// entries are dropped.
	sweepAt = 1024
)

// limiter allows max attempts per client per sliding window. Clients are
// keyed by IP (IPv6 by /64) taken from RemoteAddr.
type limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	hits map[netip.Prefix][]time.Time
}

func newLimiter(maxAttempts int, window time.Duration, now func() time.Time) *limiter {
	return &limiter{max: maxAttempts, window: window, now: now, hits: map[netip.Prefix][]time.Time{}}
}

// allow records an attempt and reports whether it is within the limit. If
// not, retry is how long until the next attempt would be allowed.
func (l *limiter) allow(remoteAddr string) (ok bool, retry time.Duration) {
	key := clientKey(remoteAddr)
	now := l.now()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) >= sweepAt {
		for k, ts := range l.hits {
			if !ts[len(ts)-1].After(cutoff) {
				delete(l.hits, k)
			}
		}
	}
	ts := l.hits[key]
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	ts = ts[i:]
	if len(ts) >= l.max {
		l.hits[key] = ts
		return false, ts[0].Sub(cutoff)
	}
	l.hits[key] = append(ts, now)
	return true, 0
}

func clientKey(remoteAddr string) netip.Prefix {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Prefix{}
	}
	a = a.Unmap().WithZone("")
	bits := a.BitLen()
	if a.Is6() {
		bits = 64
	}
	p, _ := a.Prefix(bits)
	return p
}
