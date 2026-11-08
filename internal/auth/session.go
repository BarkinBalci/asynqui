package auth

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/securecookie"
)

const (
	sessionCookie = "asynqui_session"
	flowCookie    = "asynqui_oidc"
	defaultTTL    = 12 * time.Hour
	flowTTL       = 10 * time.Minute
	// maxCookieBytes bounds the whole Set-Cookie value, leaving headroom
	// under the 4096-byte browser limit.
	maxCookieBytes = 3800
	// maxDecodeBytes bounds a cookie value before any HMAC or decryption.
	maxDecodeBytes = 4096
)

// errSessionTooLarge means the identity does not fit in the session cookie,
// usually because of too many groups.
var errSessionTooLarge = errors.New("session cookie too large")

// KeyPair is one hash/block key pair, already hex-decoded.
type KeyPair struct {
	// HashKey signs the cookie (HMAC-SHA256): 32 or 64 bytes.
	HashKey []byte
	// BlockKey encrypts the cookie (AES-256): 32 bytes.
	BlockKey []byte
}

// SessionOptions configures the session cookie.
type SessionOptions struct {
	KeyPair
	// PreviousKeys still decode existing cookies during key rotation; new
	// cookies always use the current pair.
	PreviousKeys []KeyPair
	// TTL is the absolute session lifetime from sign-in. Zero means 12h.
	TTL time.Duration
	// Insecure drops the Secure cookie attribute so sessions work over plain
	// HTTP (config cookie_secure: false). Leave false behind HTTPS.
	Insecure bool
}

func (k KeyPair) validate() error {
	if n := len(k.HashKey); n != 32 && n != 64 {
		return fmt.Errorf("hash key must be 32 or 64 bytes, got %d", n)
	}
	if n := len(k.BlockKey); n != 32 {
		return fmt.Errorf("block key must be 32 bytes, got %d", n)
	}
	return nil
}

// cookieJar encodes signed, encrypted, expiring cookies. securecookie binds
// the cookie name into the MAC, so a flow cookie cannot be replayed as a
// session or vice versa; the payload names the provider, so a session from
// a basic deployment is not accepted by an oidc one sharing the keys.
type cookieJar struct {
	kind Kind
	// sessionEnc has no length limit so oversized sessions reach the
	// explicit check in setSession; sessionDec and flow cap input size.
	sessionEnc []securecookie.Codec
	sessionDec []securecookie.Codec
	flow       []securecookie.Codec
	ttl        time.Duration
	secure     bool
	path       string // session cookie path: base path or "/"
	flowPath   string // <base>/auth/callback
	keepGroups map[string]struct{}
	now        func() time.Time
}

func newCookieJar(kind Kind, o SessionOptions, basePath string, keepGroups []string, now func() time.Time) (*cookieJar, error) {
	pairs := append([]KeyPair{o.KeyPair}, o.PreviousKeys...)
	raw := make([][]byte, 0, 2*len(pairs))
	for i, p := range pairs {
		if err := p.validate(); err != nil {
			if i == 0 {
				return nil, fmt.Errorf("session: %w", err)
			}
			return nil, fmt.Errorf("session: previous key %d: %w", i-1, err)
		}
		raw = append(raw, p.HashKey, p.BlockKey)
	}
	if o.TTL < 0 {
		return nil, errors.New("session: ttl must not be negative")
	}
	ttl := o.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	codecs := func(maxAge time.Duration, maxLen int) ([]securecookie.Codec, error) {
		cs := securecookie.CodecsFromPairs(raw...)
		for _, c := range cs {
			sc, ok := c.(*securecookie.SecureCookie)
			if !ok {
				return nil, fmt.Errorf("session: unexpected codec %T", c)
			}
			sc.SetSerializer(securecookie.JSONEncoder{}).MaxAge(int(maxAge / time.Second)).MaxLength(maxLen)
		}
		return cs, nil
	}
	j := &cookieJar{
		kind:       kind,
		ttl:        ttl,
		secure:     !o.Insecure,
		path:       basePath,
		flowPath:   basePath + "/auth/callback",
		keepGroups: make(map[string]struct{}, len(keepGroups)),
		now:        now,
	}
	if j.path == "" {
		j.path = "/"
	}
	var err error
	if j.sessionEnc, err = codecs(ttl, 0); err != nil {
		return nil, err
	}
	if j.sessionDec, err = codecs(ttl, maxDecodeBytes); err != nil {
		return nil, err
	}
	if j.flow, err = codecs(flowTTL, maxDecodeBytes); err != nil {
		return nil, err
	}
	for _, g := range keepGroups {
		j.keepGroups[g] = struct{}{}
	}
	return j, nil
}

// sessionData is the cookie payload: identity claims and an expiry, never
// tokens.
type sessionData struct {
	Provider Kind     `json:"p"`
	Subject  string   `json:"sub"`
	Name     string   `json:"name"`
	Email    string   `json:"email,omitempty"`
	Groups   []string `json:"groups,omitempty"`
	// Version ties a basic session to the user's password hash, so changing
	// the hash in config ends existing sessions.
	Version string `json:"v,omitempty"`
	Expires int64  `json:"exp"`
}

// setSession writes the session cookie. Only groups in keepGroups are
// stored, and none for basic, whose groups come from config. It fails with
// errSessionTooLarge rather than write a cookie the browser would drop.
func (j *cookieJar) setSession(w http.ResponseWriter, id Identity, version string) error {
	var groups []string
	if j.kind != KindBasic {
		for _, g := range id.Groups {
			if _, ok := j.keepGroups[g]; ok {
				groups = append(groups, g)
			}
		}
	}
	v, err := securecookie.EncodeMulti(sessionCookie, sessionData{
		Provider: j.kind,
		Subject:  id.Subject,
		Name:     id.Name,
		Email:    id.Email,
		Groups:   groups,
		Version:  version,
		Expires:  j.now().Add(j.ttl).Unix(),
	}, j.sessionEnc...)
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	c := j.cookie(sessionCookie, v, j.path, j.ttl)
	if n := len(c.String()); n > maxCookieBytes {
		return fmt.Errorf("%w: %d bytes with %d groups (limit %d)", errSessionTooLarge, n, len(groups), maxCookieBytes)
	}
	http.SetCookie(w, c)
	return nil
}

// readSession decodes the session cookie. Browsers may send several
// cookies of the same name (e.g. from different paths); the first valid
// one wins. present reports whether any was sent, so callers can clear a
// bad one.
func (j *cookieJar) readSession(r *http.Request) (d sessionData, ok, present bool) {
	for _, c := range r.CookiesNamed(sessionCookie) {
		present = true
		if d, ok := j.decodeSession(c.Value); ok {
			return d, true, true
		}
	}
	return sessionData{}, false, present
}

func (j *cookieJar) decodeSession(value string) (sessionData, bool) {
	var d sessionData
	if err := securecookie.DecodeMulti(sessionCookie, value, &d, j.sessionDec...); err != nil {
		return sessionData{}, false
	}
	if d.Provider != j.kind || d.Name == "" || !j.now().Before(time.Unix(d.Expires, 0)) {
		return sessionData{}, false
	}
	return d, true
}

func (j *cookieJar) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, j.expired(sessionCookie, j.path))
}

// flowData is the short-lived OIDC login state.
type flowData struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
	Expires  int64  `json:"exp"`
}

func (j *cookieJar) setFlow(w http.ResponseWriter, f flowData) error {
	f.Expires = j.now().Add(flowTTL).Unix()
	v, err := securecookie.EncodeMulti(flowCookie, f, j.flow...)
	if err != nil {
		return fmt.Errorf("encode login state: %w", err)
	}
	http.SetCookie(w, j.cookie(flowCookie, v, j.flowPath, flowTTL))
	return nil
}

// readFlow decodes the flow cookie without clearing it, so a forged
// callback cannot cancel a login in progress; call clearFlow once the
// state has matched.
func (j *cookieJar) readFlow(r *http.Request) (flowData, error) {
	c, err := r.Cookie(flowCookie)
	if err != nil {
		return flowData{}, errors.New("missing login state cookie")
	}
	var f flowData
	if err := securecookie.DecodeMulti(flowCookie, c.Value, &f, j.flow...); err != nil {
		return flowData{}, fmt.Errorf("decode login state: %w", err)
	}
	if !j.now().Before(time.Unix(f.Expires, 0)) {
		return flowData{}, errors.New("login state expired")
	}
	return f, nil
}

func (j *cookieJar) clearFlow(w http.ResponseWriter) {
	http.SetCookie(w, j.expired(flowCookie, j.flowPath))
}

// cookie builds every cookie this package writes: HttpOnly, SameSite=Lax,
// and Secure unless cookie_secure is off for local plain HTTP.
func (j *cookieJar) cookie(name, value, path string, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // G124: Secure is deliberately configurable (cookie_secure, default true).
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   int(maxAge / time.Second),
		Secure:   j.secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func (j *cookieJar) expired(name, path string) *http.Cookie {
	c := j.cookie(name, "", path, 0) //nolint:gosec // G124: attributes set in cookie.
	c.MaxAge = -1
	return c
}
