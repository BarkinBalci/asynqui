package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	defaultUserHeader = "X-Forwarded-User"
	// untrustedLogEvery throttles the WARN for identity headers from
	// untrusted peers, which an attacker can trigger at will.
	untrustedLogEvery = time.Minute
)

// ProxyOptions configures the reverse-proxy provider.
type ProxyOptions struct {
	// UserHeader carries the username. Empty means "X-Forwarded-User".
	UserHeader string
	// GroupsHeader carries comma-separated groups. Empty disables groups.
	GroupsHeader string
	// TrustedProxies are the CIDRs (or bare IPs) whose requests may carry
	// identity headers; required. Requests from any other address are
	// unauthenticated.
	TrustedProxies []string
	// LogoutURL is where POST /logout sends the user, typically the proxy's
	// own sign-out endpoint. Empty hides Sign out.
	LogoutURL string
}

type proxyProvider struct {
	userHeader   string
	groupsHeader string
	trusted      []netip.Prefix
	logoutURL    string
	logger       *slog.Logger
	now          func() time.Time

	mu         sync.Mutex
	lastLogged time.Time
}

func newProxy(ctx context.Context, o ProxyOptions, logger *slog.Logger, now func() time.Time) (*proxyProvider, error) {
	p := &proxyProvider{
		userHeader:   o.UserHeader,
		groupsHeader: o.GroupsHeader,
		logoutURL:    o.LogoutURL,
		logger:       logger,
		now:          now,
	}
	if p.userHeader == "" {
		p.userHeader = defaultUserHeader
	}
	if len(o.TrustedProxies) == 0 {
		return nil, errors.New("proxy: trusted_proxies is required")
	}
	for _, s := range o.TrustedProxies {
		pfx, err := parsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("proxy: trusted proxy %q: %w", s, err)
		}
		if pfx.Bits() == 0 {
			logger.WarnContext(ctx, "auth: trusted proxy covers every address; anyone can set identity headers",
				slog.String("trusted_proxy", s))
		}
		p.trusted = append(p.trusted, pfx)
	}
	return p, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		pfx, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		return unmapPrefix(pfx).Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap().WithZone("")
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// unmapPrefix turns an IPv4-mapped IPv6 prefix (::ffff:10.0.0.0/104) into
// its IPv4 form so it matches unmapped remote addresses.
func unmapPrefix(p netip.Prefix) netip.Prefix {
	a := p.Addr()
	if !a.Is4In6() || p.Bits() < 96 {
		return p
	}
	return netip.PrefixFrom(a.Unmap(), p.Bits()-96)
}

// identify trusts the identity headers only when the direct peer
// (RemoteAddr, never X-Forwarded-For) is a trusted proxy. The identity is
// read on every request; there is no session. A header sent more than once
// fails closed: a proxy that appends instead of replacing would otherwise
// let the client's copy through.
func (p *proxyProvider) identify(_ http.ResponseWriter, r *http.Request) (Identity, bool) {
	users := r.Header.Values(p.userHeader)
	if !p.trustedPeer(r.RemoteAddr) {
		if len(users) > 0 || (p.groupsHeader != "" && len(r.Header.Values(p.groupsHeader)) > 0) {
			p.warnUntrusted(r)
		}
		return Identity{}, false
	}
	var groups []string
	if p.groupsHeader != "" {
		groups = r.Header.Values(p.groupsHeader)
	}
	if len(users) > 1 || len(groups) > 1 {
		p.logger.WarnContext(r.Context(), "auth: identity header repeated; request rejected",
			slog.String("remote_addr", r.RemoteAddr), slog.Int("user_values", len(users)), slog.Int("groups_values", len(groups)))
		return Identity{}, false
	}
	if len(users) == 0 {
		return Identity{}, false
	}
	user := strings.TrimSpace(users[0])
	if user == "" {
		return Identity{}, false
	}
	id := Identity{Subject: user, Name: user}
	if len(groups) == 1 {
		id.Groups = splitGroups(groups[0])
	}
	return id, true
}

func (p *proxyProvider) warnUntrusted(r *http.Request) {
	now := p.now()
	p.mu.Lock()
	skip := now.Sub(p.lastLogged) < untrustedLogEvery
	if !skip {
		p.lastLogged = now
	}
	p.mu.Unlock()
	if skip {
		return
	}
	p.logger.WarnContext(r.Context(), "auth: identity header from untrusted peer ignored (logged at most once a minute)",
		slog.String("remote_addr", r.RemoteAddr), slog.String("header", p.userHeader))
}

func (p *proxyProvider) trustedPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap().WithZone("")
	for _, pfx := range p.trusted {
		if pfx.Contains(a) {
			return true
		}
	}
	return false
}

func splitGroups(v string) []string {
	var out []string
	for g := range strings.SplitSeq(v, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}
