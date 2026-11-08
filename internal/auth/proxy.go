package auth

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const defaultUserHeader = "X-Forwarded-User"

// ProxyOptions configures the reverse-proxy provider.
type ProxyOptions struct {
	// UserHeader carries the username. Empty means "X-Forwarded-User".
	UserHeader string
	// GroupsHeader carries comma-separated groups. Empty disables groups.
	GroupsHeader string
	// TrustedProxies are the CIDRs (or bare IPs) whose requests may carry
	// identity headers. Requests from any other address are
	// unauthenticated. Empty trusts nobody, so every request is
	// unauthenticated until this is configured.
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
}

func newProxy(o ProxyOptions, logger *slog.Logger) (*proxyProvider, error) {
	p := &proxyProvider{
		userHeader:   o.UserHeader,
		groupsHeader: o.GroupsHeader,
		logoutURL:    o.LogoutURL,
		logger:       logger,
	}
	if p.userHeader == "" {
		p.userHeader = defaultUserHeader
	}
	for _, s := range o.TrustedProxies {
		pfx, err := parsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("proxy: trusted proxy %q: %w", s, err)
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
// read on every request; there is no session.
func (p *proxyProvider) identify(_ http.ResponseWriter, r *http.Request) (Identity, bool) {
	if !p.trustedPeer(r.RemoteAddr) {
		if r.Header.Get(p.userHeader) != "" {
			p.logger.WarnContext(r.Context(), "auth: identity header from untrusted peer ignored",
				slog.String("remote_addr", r.RemoteAddr), slog.String("header", p.userHeader))
		}
		return Identity{}, false
	}
	user := strings.TrimSpace(r.Header.Get(p.userHeader))
	if user == "" {
		return Identity{}, false
	}
	id := Identity{Subject: user, Name: user}
	if p.groupsHeader != "" {
		id.Groups = splitGroups(r.Header.Values(p.groupsHeader))
	}
	return id, true
}

func (p *proxyProvider) trustedPeer(remoteAddr string) bool {
	if len(p.trusted) == 0 {
		return false
	}
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

func splitGroups(values []string) []string {
	var out []string
	for _, v := range values {
		for g := range strings.SplitSeq(v, ",") {
			if g = strings.TrimSpace(g); g != "" {
				out = append(out, g)
			}
		}
	}
	return out
}
