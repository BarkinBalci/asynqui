package config

import "time"

// Config is the whole asynqui configuration file.
type Config struct {
	Server      Server       `yaml:"server"`
	Log         Log          `yaml:"log"`
	Refresh     Refresh      `yaml:"refresh"`
	UpdateCheck bool         `yaml:"update_check"`
	Connections []Connection `yaml:"connections"`
	Auth        Auth         `yaml:"auth"`
	Authz       Authz        `yaml:"authz"`
}

// Server configures the HTTP listener.
type Server struct {
	// Addr is the listen address. The -addr flag and ASYNQUI_ADDR override it.
	Addr string `yaml:"addr"`
	// BasePath serves the whole app under a sub-path, e.g. "/asynq". It is
	// empty or starts with "/" and has no trailing "/".
	BasePath string `yaml:"base_path"`
	// TrustedOrigins are extra origins (scheme://host[:port]) allowed to send
	// cross-origin unsafe requests, passed to http.CrossOriginProtection.
	TrustedOrigins []string `yaml:"trusted_origins"`
	// ShutdownDelay is how long readiness reports draining before the
	// listener closes. Zero disables it.
	ShutdownDelay time.Duration `yaml:"shutdown_delay"`
}

// Log configures structured logging.
type Log struct {
	// Level is one of debug, info, warn or error.
	Level string `yaml:"level"`
}

// Refresh configures UI auto-refresh.
type Refresh struct {
	// Default is the auto-refresh interval for new visitors: off, 2s, 5s or 10s.
	Default string `yaml:"default"`
}

// Connection is one Redis address plus logical database.
type Connection struct {
	Name         string        `yaml:"name"`
	Addr         string        `yaml:"addr"`
	DB           int           `yaml:"db"`
	Username     string        `yaml:"username"`
	Password     Secret        `yaml:"password"`
	TLS          TLS           `yaml:"tls"`
	DialTimeout  time.Duration `yaml:"dial_timeout"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

// TLS configures TLS for a connection.
type TLS struct {
	Enabled            bool   `yaml:"enabled"`
	CAFile             string `yaml:"ca_file"`
	CertFile           string `yaml:"cert_file"`
	KeyFile            string `yaml:"key_file"`
	ServerName         string `yaml:"server_name"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

// Provider names an authentication provider.
type Provider string

// Authentication providers.
const (
	ProviderNone  Provider = "none"
	ProviderBasic Provider = "basic"
	ProviderProxy Provider = "proxy"
	ProviderOIDC  Provider = "oidc"
)

// Auth configures authentication. Only the block for the selected Provider is
// validated; the others are ignored.
type Auth struct {
	Provider Provider `yaml:"provider"`
	Session  Session  `yaml:"session"`
	Basic    Basic    `yaml:"basic"`
	Proxy    Proxy    `yaml:"proxy"`
	OIDC     OIDC     `yaml:"oidc"`
}

// Session configures the session cookie used by the basic and oidc providers.
type Session struct {
	// HashKey and BlockKey are hex-encoded. HashKey decodes to 32 or 64
	// bytes, BlockKey to 32 bytes.
	HashKey  Secret `yaml:"hash_key"`
	BlockKey Secret `yaml:"block_key"`
	// PreviousKeys still decode existing cookies during key rotation.
	PreviousKeys []KeyPair `yaml:"previous_keys"`
	// TTL is the absolute session lifetime.
	TTL time.Duration `yaml:"ttl"`
	// CookieSecure sets the cookie's Secure attribute. Disable it only for
	// local plain-HTTP use.
	CookieSecure bool `yaml:"cookie_secure"`
}

// KeyPair is a hex-encoded hash key and block key.
type KeyPair struct {
	HashKey  Secret `yaml:"hash_key"`
	BlockKey Secret `yaml:"block_key"`
}

// Basic configures username and password login.
type Basic struct {
	Users []BasicUser `yaml:"users"`
}

// BasicUser is one user of the basic provider.
type BasicUser struct {
	Username string `yaml:"username"`
	// PasswordHash is a bcrypt hash.
	PasswordHash Secret   `yaml:"password_hash"`
	Groups       []string `yaml:"groups"`
}

// Proxy configures identity headers set by a trusted reverse proxy.
type Proxy struct {
	UserHeader string `yaml:"user_header"`
	// GroupsHeader is optional and holds comma-separated groups.
	GroupsHeader string `yaml:"groups_header"`
	// TrustedProxies are IPs or CIDRs; headers from any other peer are
	// rejected.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// LogoutURL is optional. Sign out is shown only when it is set.
	LogoutURL string `yaml:"logout_url"`
}

// OIDC configures the OpenID Connect authorization-code flow with PKCE.
type OIDC struct {
	Issuer        string   `yaml:"issuer"`
	ClientID      string   `yaml:"client_id"`
	ClientSecret  Secret   `yaml:"client_secret"`
	RedirectURL   string   `yaml:"redirect_url"`
	Scopes        []string `yaml:"scopes"`
	UsernameClaim string   `yaml:"username_claim"`
	GroupsClaim   string   `yaml:"groups_claim"`
}

// Role is an authorization role. Roles are ordered viewer < operator < admin;
// RoleNone means no access.
type Role string

// Roles.
const (
	RoleNone     Role = "none"
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

// Authz configures per-connection authorization.
type Authz struct {
	// Default is the role of a user on a connection no binding grants.
	Default  Role      `yaml:"default"`
	Bindings []Binding `yaml:"bindings"`
}

// Binding grants Role on the connections matching any of Connections (path.Match
// globs) to the listed users and to members of the listed groups.
type Binding struct {
	Users       []string `yaml:"users"`
	Groups      []string `yaml:"groups"`
	Role        Role     `yaml:"role"`
	Connections []string `yaml:"connections"`
}

// Default values.
const (
	DefaultAddr          = ":8080"
	DefaultShutdownDelay = 5 * time.Second
	DefaultLogLevel      = "info"
	DefaultRefresh       = "5s"
	DefaultDialTimeout   = 5 * time.Second
	DefaultReadTimeout   = 3 * time.Second
	DefaultWriteTimeout  = 3 * time.Second
	DefaultSessionTTL    = 12 * time.Hour
	DefaultUserHeader    = "X-Forwarded-User"
	DefaultUsernameClaim = "email"
	DefaultGroupsClaim   = "groups"
)

// DefaultScopes returns the OIDC scopes used when none are configured.
func DefaultScopes() []string { return []string{"openid", "profile", "email"} }

// defaults returns a Config holding every default that does not depend on
// another field. Decoding YAML on top of it keeps the defaults of absent or
// null keys, so an explicit zero (e.g. shutdown_delay: 0s) is kept as written.
func defaults() *Config {
	return &Config{
		Server:      Server{Addr: DefaultAddr, ShutdownDelay: DefaultShutdownDelay},
		Log:         Log{Level: DefaultLogLevel},
		Refresh:     Refresh{Default: DefaultRefresh},
		UpdateCheck: true,
		Auth: Auth{
			Provider: ProviderNone,
			Session:  Session{TTL: DefaultSessionTTL, CookieSecure: true},
			Proxy:    Proxy{UserHeader: DefaultUserHeader},
			OIDC: OIDC{
				Scopes:        DefaultScopes(),
				UsernameClaim: DefaultUsernameClaim,
				GroupsClaim:   DefaultGroupsClaim,
			},
		},
	}
}

// UnmarshalYAML decodes a connection on top of its default timeouts. It
// uses the old-style callback so the Decoder's KnownFields still applies.
func (c *Connection) UnmarshalYAML(unmarshal func(any) error) error {
	type connection Connection // no methods, so no recursion
	p := connection{
		DialTimeout:  DefaultDialTimeout,
		ReadTimeout:  DefaultReadTimeout,
		WriteTimeout: DefaultWriteTimeout,
	}
	if err := unmarshal(&p); err != nil {
		return err
	}
	*c = Connection(p)
	return nil
}
