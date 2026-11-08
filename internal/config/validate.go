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
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// connectionNamePattern is the URL-safe shape of a connection name, checked
// by validConnectionName.
const connectionNamePattern = `^[a-z0-9][a-z0-9._-]{0,62}$`

// fieldError is a problem at one location of the config. Its message never
// contains a configured value, which could be a secret.
type fieldError struct {
	path string // e.g. connections[2].name; "" for the whole file
	line int    // 0 when unknown
	msg  string
}

func (e *fieldError) Error() string {
	var b strings.Builder
	if e.line > 0 {
		fmt.Fprintf(&b, "line %d: ", e.line)
	}
	if e.path != "" {
		b.WriteString(e.path + ": ")
	}
	b.WriteString(e.msg)
	return b.String()
}

// validator collects every problem instead of stopping at the first.
type validator struct {
	errs []*fieldError
}

func (v *validator) add(path, format string, args ...any) {
	v.errs = append(v.errs, &fieldError{path: path, msg: fmt.Sprintf(format, args...)})
}

// validate checks a decoded, normalized config.
func (c *Config) validate() []*fieldError {
	v := &validator{}
	v.server(c.Server)
	oneOf(v, "log.level", c.Log.Level, "debug", "info", "warn", "error")
	oneOf(v, "refresh.default", c.Refresh.Default, "off", "2s", "5s", "10s")
	v.connections(c.Connections)
	v.auth(c.Auth)
	v.authz(c.Authz)
	return v.errs
}

func oneOf[T ~string](v *validator, path string, got T, allowed ...T) {
	if slices.Contains(allowed, got) {
		return
	}
	names := make([]string, len(allowed))
	for i, a := range allowed {
		names[i] = string(a)
	}
	v.add(path, "must be one of %s", strings.Join(names, ", "))
}

func (v *validator) required(path, value string) bool {
	if value == "" {
		v.add(path, "is required")
		return false
	}
	return true
}

func (v *validator) positive(path string, d time.Duration) {
	if d <= 0 {
		v.add(path, "must be a positive duration")
	}
}

func (v *validator) nonEmptyEntries(path string, list []string) {
	for i, s := range list {
		if strings.TrimSpace(s) == "" {
			v.add(fmt.Sprintf("%s[%d]", path, i), "must not be empty")
		}
	}
}

func (v *validator) server(s Server) {
	if v.required("server.addr", s.Addr) {
		if msg := checkHostPort(s.Addr, false); msg != "" {
			v.add("server.addr", "%s", msg)
		}
	}
	if bp := s.BasePath; bp != "" && (!strings.HasPrefix(bp, "/") || strings.HasSuffix(bp, "/") ||
		path.Clean(bp) != bp || strings.ContainsAny(bp, "?#% \t")) {
		v.add("server.base_path", "must be empty or a clean path such as /asynq, without a trailing slash")
	}
	// Validate exactly as the web server will use them.
	cop := http.NewCrossOriginProtection()
	for i, o := range s.TrustedOrigins {
		if err := cop.AddTrustedOrigin(o); err != nil {
			v.add(fmt.Sprintf("server.trusted_origins[%d]", i), "must be an origin such as https://example.com")
		}
	}
	if s.ShutdownDelay < 0 {
		v.add("server.shutdown_delay", "must not be negative")
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

func (v *validator) connections(conns []Connection) {
	if len(conns) == 0 {
		v.add("connections", "at least one connection is required")
	}
	first := make(map[string]int, len(conns))
	for i, c := range conns {
		p := fmt.Sprintf("connections[%d]", i)
		v.connection(p, c)
		if c.Name == "" {
			continue
		}
		if j, dup := first[c.Name]; dup {
			v.add(p+".name", "duplicate name, already used by connections[%d]", j)
			continue
		}
		first[c.Name] = i
	}
}

func (v *validator) connection(p string, c Connection) {
	if v.required(p+".name", c.Name) && !validConnectionName(c.Name) {
		v.add(p+".name", "must match %s", connectionNamePattern)
	}
	if v.required(p+".addr", c.Addr) {
		if msg := checkHostPort(c.Addr, true); msg != "" {
			v.add(p+".addr", "%s", msg)
		}
	}
	if c.DB < 0 {
		v.add(p+".db", "must not be negative")
	}
	if (c.TLS.CertFile == "") != (c.TLS.KeyFile == "") {
		v.add(p+".tls", "cert_file and key_file must be set together")
	}
	v.positive(p+".dial_timeout", c.DialTimeout)
	v.positive(p+".read_timeout", c.ReadTimeout)
	v.positive(p+".write_timeout", c.WriteTimeout)
}

// validConnectionName reports whether name matches connectionNamePattern.
func validConnectionName(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for i, c := range []byte(name) {
		alnum := ('a' <= c && c <= 'z') || ('0' <= c && c <= '9')
		if !alnum && (i == 0 || (c != '.' && c != '_' && c != '-')) {
			return false
		}
	}
	return true
}

func (v *validator) auth(a Auth) {
	switch a.Provider {
	case ProviderNone:
	case ProviderBasic:
		v.session(a.Session)
		v.basic(a.Basic)
	case ProviderProxy:
		v.proxy(a.Proxy)
	case ProviderOIDC:
		v.session(a.Session)
		v.oidc(a.OIDC)
	default:
		oneOf(v, "auth.provider", a.Provider, ProviderNone, ProviderBasic, ProviderProxy, ProviderOIDC)
	}
}

func (v *validator) session(s Session) {
	v.keyPair("auth.session", KeyPair{HashKey: s.HashKey, BlockKey: s.BlockKey})
	for i, kp := range s.PreviousKeys {
		v.keyPair(fmt.Sprintf("auth.session.previous_keys[%d]", i), kp)
	}
	v.positive("auth.session.ttl", s.TTL)
}

func (v *validator) keyPair(p string, kp KeyPair) {
	_, herr := decodeHashKey(kp.HashKey)
	if herr != nil {
		v.add(p+".hash_key", "%v", herr)
	}
	_, berr := decodeBlockKey(kp.BlockKey)
	if berr != nil {
		v.add(p+".block_key", "%v", berr)
	}
	if herr == nil && berr == nil && kp.HashKey == kp.BlockKey {
		v.add(p+".block_key", "must differ from hash_key")
	}
}

// decodeHashKey decodes a hex hash key of 32 or 64 bytes.
func decodeHashKey(s Secret) ([]byte, error) {
	return decodeKey(s, "32 or 64", 32, 64)
}

// decodeBlockKey decodes a hex block key of 32 bytes (AES-256).
func decodeBlockKey(s Secret) ([]byte, error) {
	return decodeKey(s, "32", 32)
}

// decodeKey decodes a hex key whose length is one of sizes. Its errors never
// contain the key.
func decodeKey(s Secret, want string, sizes ...int) ([]byte, error) {
	k := s.Reveal()
	switch {
	case k == "":
		return nil, fmt.Errorf("is required (hex, %s bytes; generate with openssl rand -hex 32)", want)
	case strings.TrimSpace(k) != k || strings.ContainsAny(k, " \t\r\n"):
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

func (v *validator) basic(b Basic) {
	if len(b.Users) == 0 {
		v.add("auth.basic.users", "at least one user is required")
	}
	first := map[string]int{}
	for i, u := range b.Users {
		p := fmt.Sprintf("auth.basic.users[%d]", i)
		if v.required(p+".username", u.Username) {
			key := strings.ToLower(u.Username)
			if j, dup := first[key]; dup {
				v.add(p+".username", "duplicate username (case-insensitive), already used by auth.basic.users[%d]", j)
			} else {
				first[key] = i
			}
		}
		if v.required(p+".password_hash", u.PasswordHash.Reveal()) && !isBcryptHash(u.PasswordHash.Reveal()) {
			v.add(p+".password_hash", "must be a bcrypt hash such as htpasswd -nbBC 12 prints")
		}
		v.nonEmptyEntries(p+".groups", u.Groups)
	}
}

// isBcryptHash reports whether h is a well-formed bcrypt hash: a known
// version, a valid cost, and 53 characters of bcrypt's base64 alphabet.
func isBcryptHash(h string) bool {
	if len(h) != 60 || !slices.Contains([]string{"$2a$", "$2b$", "$2y$"}, h[:4]) {
		return false
	}
	if _, err := bcrypt.Cost([]byte(h)); err != nil {
		return false
	}
	for _, c := range []byte(h[7:]) {
		ok := c == '.' || c == '/' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func (v *validator) proxy(p Proxy) {
	if v.required("auth.proxy.user_header", p.UserHeader) && !isToken(p.UserHeader) {
		v.add("auth.proxy.user_header", "must be a valid header name")
	}
	if p.GroupsHeader != "" && !isToken(p.GroupsHeader) {
		v.add("auth.proxy.groups_header", "must be a valid header name")
	}
	if len(p.TrustedProxies) == 0 {
		v.add("auth.proxy.trusted_proxies", "at least one IP or CIDR is required")
	}
	for i, s := range p.TrustedProxies {
		if _, err := parseTrustedProxy(s); err != nil {
			v.add(fmt.Sprintf("auth.proxy.trusted_proxies[%d]", i), "%v", err)
		}
	}
	if u := p.LogoutURL; u != "" && !isHTTPURL(u) && (!strings.HasPrefix(u, "/") || strings.HasPrefix(u, "//")) {
		v.add("auth.proxy.logout_url", "must be an absolute http(s) URL or a path starting with a single /")
	}
}

// parseTrustedProxy parses an IP (a single-address prefix) or a CIDR whose
// host bits are zero. Its errors never contain the value.
func parseTrustedProxy(s string) (netip.Prefix, error) {
	if ip, err := netip.ParseAddr(s); err == nil {
		return netip.PrefixFrom(ip, ip.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, errors.New("must be an IP or a CIDR such as 10.0.0.0/8")
	}
	if p != p.Masked() {
		return netip.Prefix{}, errors.New("CIDR has host bits set; use the network address, such as 10.0.0.0/8")
	}
	return p, nil
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
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		ok := ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') ||
			strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
		if !ok {
			return false
		}
	}
	return true
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (v *validator) oidc(o OIDC) {
	if v.required("auth.oidc.issuer", o.Issuer) && !isHTTPURL(o.Issuer) {
		v.add("auth.oidc.issuer", "must be an absolute http(s) URL")
	}
	v.required("auth.oidc.client_id", o.ClientID)
	if v.required("auth.oidc.redirect_url", o.RedirectURL) && !isHTTPURL(o.RedirectURL) {
		v.add("auth.oidc.redirect_url", "must be an absolute http(s) URL")
	}
	v.nonEmptyEntries("auth.oidc.scopes", o.Scopes)
	v.required("auth.oidc.username_claim", o.UsernameClaim)
	v.required("auth.oidc.groups_claim", o.GroupsClaim)
}

func (v *validator) authz(a Authz) {
	oneOf(v, "authz.default", a.Default, RoleNone, RoleViewer, RoleOperator, RoleAdmin)
	for i, b := range a.Bindings {
		p := fmt.Sprintf("authz.bindings[%d]", i)
		if len(b.Users) == 0 && len(b.Groups) == 0 {
			v.add(p, "must list users or groups")
		}
		v.nonEmptyEntries(p+".users", b.Users)
		v.nonEmptyEntries(p+".groups", b.Groups)
		oneOf(v, p+".role", b.Role, RoleViewer, RoleOperator, RoleAdmin)
		if len(b.Connections) == 0 {
			v.add(p+".connections", "must list at least one connection glob")
		}
		for j, g := range b.Connections {
			gp := fmt.Sprintf("%s.connections[%d]", p, j)
			switch {
			case g == "":
				v.add(gp, "must not be empty")
			case !validGlob(g):
				v.add(gp, "must be a valid glob (path.Match syntax)")
			}
		}
	}
}

func validGlob(g string) bool {
	_, err := path.Match(g, "")
	return err == nil
}

// Warnings returns problems that do not stop startup:
//   - connection globs that match no connection,
//   - TLS options on a connection whose TLS is disabled,
//   - an OIDC redirect_url that does not point at {base_path}/auth/callback.
//
// Like errors, warnings never contain configured values except globs.
func (c *Config) Warnings() []string {
	var w []string
	for i, b := range c.Authz.Bindings {
		for j, g := range b.Connections {
			if validGlob(g) && !c.anyConnectionMatches(g) {
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

func (c *Config) anyConnectionMatches(glob string) bool {
	for _, cn := range c.Connections {
		if ok, _ := path.Match(glob, cn.Name); ok {
			return true
		}
	}
	return false
}
