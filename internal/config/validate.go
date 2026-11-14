package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// connectionNamePattern is the URL-safe shape of a connection name.
const connectionNamePattern = `^[a-z0-9][a-z0-9._-]{0,62}$`

// problems collects validation errors as "path: message". Messages never
// contain a configured value, which could be a secret.
type problems []error

func (p *problems) add(path, msg string) { *p = append(*p, errors.New(path+": "+msg)) }

func (p *problems) required(path, value string) bool {
	if value == "" {
		p.add(path, "is required")
	}
	return value != ""
}

func (p *problems) positive(path string, d time.Duration) {
	if d <= 0 {
		p.add(path, "must be a positive duration")
	}
}

func (p *problems) nonEmptyEntries(path string, list []string) {
	for i, s := range list {
		if strings.TrimSpace(s) == "" {
			p.add(fmt.Sprintf("%s[%d]", path, i), "must not be empty")
		}
	}
}

func (p *problems) oneOf(path, got string, allowed ...string) {
	if !slices.Contains(allowed, got) {
		p.add(path, "must be one of "+strings.Join(allowed, ", "))
	}
}

// validate checks a decoded, normalized config and joins every problem.
func (c *Config) validate() error {
	var p problems
	p.server(c.Server)
	p.oneOf("log.level", c.Log.Level, "debug", "info", "warn", "error")
	p.oneOf("refresh.default", c.Refresh.Default, "off", "2s", "5s", "10s")
	p.connections(c.Connections)
	switch c.Auth.Provider {
	case ProviderNone:
	case ProviderBasic:
		p.session(c.Auth.Session)
		p.basic(c.Auth.Basic)
	case ProviderProxy:
		p.proxy(c.Auth.Proxy)
	case ProviderOIDC:
		p.session(c.Auth.Session)
		p.oidc(c.Auth.OIDC)
	default:
		p.oneOf("auth.provider", string(c.Auth.Provider), "none", "basic", "proxy", "oidc")
	}
	p.authz(c.Authz)
	return errors.Join(p...)
}

func (p *problems) server(s Server) {
	if p.required("server.addr", s.Addr) {
		if msg := checkHostPort(s.Addr, false); msg != "" {
			p.add("server.addr", msg)
		}
	}
	if bp := s.BasePath; bp != "" && (!strings.HasPrefix(bp, "/") || strings.HasSuffix(bp, "/") ||
		path.Clean(bp) != bp || strings.ContainsAny(bp, "?#% \t")) {
		p.add("server.base_path", "must be empty or a clean path such as /asynq, without a trailing slash")
	}
	// Validate exactly as the web server will use them.
	cop := http.NewCrossOriginProtection()
	for i, o := range s.TrustedOrigins {
		if cop.AddTrustedOrigin(o) != nil {
			p.add(fmt.Sprintf("server.trusted_origins[%d]", i), "must be an origin such as https://example.com")
		}
	}
	if s.ShutdownDelay < 0 {
		p.add("server.shutdown_delay", "must not be negative")
	}
}

// checkHostPort checks addr has the host:port shape and returns a problem
// description or "". The host may be empty, and the port 0, only when
// requireHost is false (listen addresses).
func checkHostPort(addr string, requireHost bool) string {
	host, port, err := net.SplitHostPort(addr)
	switch {
	case err != nil:
		return "must be host:port"
	case requireHost && host == "":
		return "must include a host"
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || (requireHost && n == 0) {
		return "must have a port from 1 to 65535"
	}
	return ""
}

func (p *problems) connections(conns []Connection) {
	if len(conns) == 0 {
		p.add("connections", "at least one connection is required")
	}
	validName := regexp.MustCompile(connectionNamePattern)
	first := make(map[string]int, len(conns))
	for i, c := range conns {
		at := fmt.Sprintf("connections[%d]", i)
		if p.required(at+".name", c.Name) {
			if j, dup := first[c.Name]; dup {
				p.add(at+".name", fmt.Sprintf("duplicate of connections[%d]", j))
			} else if !validName.MatchString(c.Name) {
				p.add(at+".name", "must match "+connectionNamePattern)
			} else {
				first[c.Name] = i
			}
		}
		if p.required(at+".addr", c.Addr) {
			if msg := checkHostPort(c.Addr, true); msg != "" {
				p.add(at+".addr", msg)
			}
		}
		if c.DB < 0 {
			p.add(at+".db", "must not be negative")
		}
		if (c.TLS.CertFile == "") != (c.TLS.KeyFile == "") {
			p.add(at+".tls", "cert_file and key_file must be set together")
		}
		p.positive(at+".dial_timeout", c.DialTimeout)
		p.positive(at+".read_timeout", c.ReadTimeout)
		p.positive(at+".write_timeout", c.WriteTimeout)
	}
}

func (p *problems) session(s Session) {
	pairs := append([]KeyPair{{HashKey: s.HashKey, BlockKey: s.BlockKey}}, s.PreviousKeys...)
	for i, kp := range pairs {
		at := "auth.session"
		if i > 0 {
			at = fmt.Sprintf("auth.session.previous_keys[%d]", i-1)
		}
		_, herr := decodeHashKey(kp.HashKey)
		if herr != nil {
			p.add(at+".hash_key", herr.Error())
		}
		_, berr := decodeBlockKey(kp.BlockKey)
		if berr != nil {
			p.add(at+".block_key", berr.Error())
		}
		if herr == nil && berr == nil && kp.HashKey == kp.BlockKey {
			p.add(at+".block_key", "must differ from hash_key")
		}
	}
	p.positive("auth.session.ttl", s.TTL)
}

// decodeHashKey decodes a hex hash key of 32 or 64 bytes.
func decodeHashKey(s Secret) ([]byte, error) { return decodeKey(s, "32 or 64", 32, 64) }

// decodeBlockKey decodes a hex block key of 32 bytes (AES-256).
func decodeBlockKey(s Secret) ([]byte, error) { return decodeKey(s, "32", 32) }

// decodeKey decodes a hex key whose length is one of sizes. Its errors never
// contain the key.
func decodeKey(s Secret, want string, sizes ...int) ([]byte, error) {
	k := s.Reveal()
	switch {
	case k == "":
		return nil, fmt.Errorf("is required (hex, %s bytes; generate with openssl rand -hex 32)", want)
	case strings.ContainsAny(k, " \t\r\n"):
		return nil, errors.New("contains whitespace, such as a trailing newline")
	}
	b, err := hex.DecodeString(k)
	if err != nil {
		return nil, fmt.Errorf("must be hex-encoded (%s bytes)", want)
	}
	if !slices.Contains(sizes, len(b)) {
		return nil, fmt.Errorf("must decode to %s bytes, got %d", want, len(b))
	}
	return b, nil
}

// KeyPairs returns the decoded current and previous session keys as
// hashKey, blockKey, hashKey, blockKey, ..., the order
// securecookie.CodecsFromPairs takes. Load has already validated them.
func (s Session) KeyPairs() ([][]byte, error) {
	pairs := append([]KeyPair{{HashKey: s.HashKey, BlockKey: s.BlockKey}}, s.PreviousKeys...)
	out := make([][]byte, 0, 2*len(pairs))
	for i, kp := range pairs {
		h, err := decodeHashKey(kp.HashKey)
		if err != nil {
			return nil, fmt.Errorf("session key pair %d: hash_key %w", i, err)
		}
		b, err := decodeBlockKey(kp.BlockKey)
		if err != nil {
			return nil, fmt.Errorf("session key pair %d: block_key %w", i, err)
		}
		out = append(out, h, b)
	}
	return out, nil
}

func (p *problems) basic(b Basic) {
	if len(b.Users) == 0 {
		p.add("auth.basic.users", "at least one user is required")
	}
	first := map[string]int{}
	cost, costAt := -1, 0
	for i, u := range b.Users {
		at := fmt.Sprintf("auth.basic.users[%d]", i)
		if p.required(at+".username", u.Username) {
			key := FoldUsername(u.Username)
			if j, dup := first[key]; dup {
				p.add(at+".username", fmt.Sprintf("duplicate (case-insensitive) of auth.basic.users[%d]", j))
			} else {
				first[key] = i
			}
		}
		if h := u.PasswordHash.Reveal(); p.required(at+".password_hash", h) && !isBcryptHash(h) {
			p.add(at+".password_hash", "must be a bcrypt hash such as htpasswd -nbBC 12 prints")
		} else if c, err := bcrypt.Cost([]byte(h)); err == nil {
			// One cost for everyone, so the timing of a failed login does
			// not tell which user (or that no user) was tried.
			if cost >= 0 && c != cost {
				p.add(at+".password_hash", fmt.Sprintf("has bcrypt cost %d, auth.basic.users[%d] has %d; rehash all passwords at one cost", c, costAt, cost))
			} else if cost < 0 {
				cost, costAt = c, i
			}
		}
		p.nonEmptyEntries(at+".groups", u.Groups)
	}
}

// FoldUsername lowercases ASCII A–Z and leaves every other byte alone, so
// usernames compare case-insensitively without Unicode folding: the
// duplicate check here, authz's binding match and auth's per-user rate
// limit all use it. Unicode folding would let look-alikes such as the
// Kelvin sign (U+212A) or the Angstrom sign (U+212B) match "k" or "å", and
// İ (U+0130) match "i"; invalid UTF-8 stays as it is.
func FoldUsername(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// isBcryptHash reports whether h is a well-formed bcrypt hash: a known
// version, a valid cost, and 53 characters of bcrypt's base64 alphabet.
func isBcryptHash(h string) bool {
	_, err := bcrypt.Cost([]byte(h))
	return err == nil && regexp.MustCompile(`^\$2[aby]\$\d\d\$[./A-Za-z0-9]{53}$`).MatchString(h)
}

func (p *problems) proxy(px Proxy) {
	if p.required("auth.proxy.user_header", px.UserHeader) && !isToken(px.UserHeader) {
		p.add("auth.proxy.user_header", "must be a valid header name")
	}
	if px.GroupsHeader != "" && !isToken(px.GroupsHeader) {
		p.add("auth.proxy.groups_header", "must be a valid header name")
	}
	if len(px.TrustedProxies) == 0 {
		p.add("auth.proxy.trusted_proxies", "at least one IP or CIDR is required")
	}
	for i, s := range px.TrustedProxies {
		if _, err := parseTrustedProxy(s); err != nil {
			p.add(fmt.Sprintf("auth.proxy.trusted_proxies[%d]", i), err.Error())
		}
	}
	if u := px.LogoutURL; u != "" && !isHTTPURL(u) && (!strings.HasPrefix(u, "/") || strings.HasPrefix(u, "//")) {
		p.add("auth.proxy.logout_url", "must be an absolute http(s) URL or a path starting with a single /")
	}
}

// parseTrustedProxy parses an IP (a single-address prefix) or a CIDR whose
// host bits are zero. Its errors never contain the value.
func parseTrustedProxy(s string) (netip.Prefix, error) {
	if ip, err := netip.ParseAddr(s); err == nil {
		return netip.PrefixFrom(ip, ip.BitLen()), nil
	}
	pfx, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, errors.New("must be an IP or a CIDR such as 10.0.0.0/8")
	}
	if pfx != pfx.Masked() {
		return netip.Prefix{}, errors.New("CIDR has host bits set; use the network address, such as 10.0.0.0/8")
	}
	return pfx, nil
}

// TrustedPrefixes returns the trusted proxies as prefixes; a bare IP is a
// single-address prefix. Load has already validated them.
func (p Proxy) TrustedPrefixes() ([]netip.Prefix, error) {
	out := make([]netip.Prefix, len(p.TrustedProxies))
	for i, s := range p.TrustedProxies {
		pfx, err := parseTrustedProxy(s)
		if err != nil {
			return nil, fmt.Errorf("trusted_proxies[%d]: %w", i, err)
		}
		out[i] = pfx
	}
	return out, nil
}

// isToken reports whether s is an HTTP header field name (RFC 9110 token).
func isToken(s string) bool {
	const tchar = "!#$%&'*+-.^_`|~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	return s != "" && strings.Trim(s, tchar) == ""
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (p *problems) oidc(o OIDC) {
	if p.required("auth.oidc.issuer", o.Issuer) && !isHTTPURL(o.Issuer) {
		p.add("auth.oidc.issuer", "must be an absolute http(s) URL")
	}
	p.required("auth.oidc.client_id", o.ClientID)
	if p.required("auth.oidc.redirect_url", o.RedirectURL) && !isHTTPURL(o.RedirectURL) {
		p.add("auth.oidc.redirect_url", "must be an absolute http(s) URL")
	}
	p.nonEmptyEntries("auth.oidc.scopes", o.Scopes)
	p.required("auth.oidc.username_claim", o.UsernameClaim)
	p.required("auth.oidc.groups_claim", o.GroupsClaim)
}

func (p *problems) authz(a Authz) {
	p.oneOf("authz.default", string(a.Default), "none", "viewer", "operator", "admin")
	for i, b := range a.Bindings {
		at := fmt.Sprintf("authz.bindings[%d]", i)
		if len(b.Users) == 0 && len(b.Groups) == 0 {
			p.add(at, "must list users or groups")
		}
		p.nonEmptyEntries(at+".users", b.Users)
		p.nonEmptyEntries(at+".groups", b.Groups)
		p.oneOf(at+".role", string(b.Role), "viewer", "operator", "admin")
		if len(b.Connections) == 0 {
			p.add(at+".connections", "must list at least one connection glob")
		}
		for j, g := range b.Connections {
			if !validGlob(g) {
				p.add(fmt.Sprintf("%s.connections[%d]", at, j), "must be a valid, non-empty glob (path.Match syntax)")
			}
		}
	}
}

func validGlob(g string) bool {
	_, err := path.Match(g, "")
	return g != "" && err == nil
}

// Warnings returns problems that do not stop startup:
//   - connection globs that match no connection,
//   - TLS options on a connection whose TLS is disabled,
//   - an OIDC redirect_url that does not point at {base_path}/auth/callback.
func (c *Config) Warnings() []string {
	var w []string
	for i, b := range c.Authz.Bindings {
		for j, g := range b.Connections {
			matches := slices.ContainsFunc(c.Connections, func(cn Connection) bool {
				ok, _ := path.Match(g, cn.Name)
				return ok
			})
			if validGlob(g) && !matches {
				w = append(w, fmt.Sprintf("authz.bindings[%d].connections[%d]: %q matches no connection", i, j, g))
			}
		}
	}
	for i, cn := range c.Connections {
		if t := cn.TLS; !t.Enabled && (t != TLS{}) {
			w = append(w, fmt.Sprintf("connections[%d].tls: options are set but tls.enabled is false, so they are ignored", i))
		}
	}
	if c.Auth.Provider == ProviderOIDC {
		want := c.Server.BasePath + "/auth/callback"
		if u, err := url.Parse(c.Auth.OIDC.RedirectURL); err == nil && u.Path != want {
			w = append(w, fmt.Sprintf("auth.oidc.redirect_url: path does not match the callback served at %q", want))
		}
	}
	return w
}
