package auth

import (
	"container/list"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// loginBurst attempts are allowed at once; then one more every
	// loginRefill.
	loginBurst  = 5
	loginRefill = 12 * time.Second
	// limiterCap bounds memory per limiter. When full, the least recently
	// seen key is evicted.
	limiterCap = 10000
)

// limiter is a token bucket per key. Keys are client IPs (IPv6 by /64) or
// folded usernames. Behind a reverse proxy every user shares the proxy's
// IP, so one user's failures can lock out the others: that is the
// documented trade-off of keying on RemoteAddr only.
type limiter struct {
	cap     int
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]*list.Element // of *limitEntry
	order   *list.List               // front = most recently seen
}

type limitEntry struct {
	key    string
	bucket *rate.Limiter
}

func newLimiter(capacity int, now func() time.Time) *limiter {
	return &limiter{cap: capacity, now: now, entries: map[string]*list.Element{}, order: list.New()}
}

// allow takes a token for key and reports whether there was one. If not,
// retry is how long until the next one.
func (l *limiter) allow(key string) (ok bool, retry time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	el, found := l.entries[key]
	if !found {
		if l.order.Len() >= l.cap {
			oldest := l.order.Back()
			l.order.Remove(oldest)
			delete(l.entries, oldest.Value.(*limitEntry).key)
		}
		el = l.order.PushFront(&limitEntry{key: key, bucket: rate.NewLimiter(rate.Every(loginRefill), loginBurst)})
		l.entries[key] = el
	} else {
		l.order.MoveToFront(el)
	}
	b := el.Value.(*limitEntry).bucket
	if b.AllowN(now, 1) {
		return true, 0
	}
	return false, time.Duration((1 - b.TokensAt(now)) * float64(loginRefill))
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
