package config

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// validConfig returns a config that passes validation with any provider.
func validConfig() *Config {
	c := defaults()
	c.Connections = []Connection{
		{Name: "main", Addr: "redis:6379", DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
		{Name: "main-jobs", Addr: "redis:6379", DB: 3, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
		{Name: "local", Addr: "[::1]:6379", DB: 1, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
	}
	c.Auth.Session.HashKey = testHashKey
	c.Auth.Session.BlockKey = testBlockKey
	c.Auth.Basic.Users = []BasicUser{{Username: "jdoe", PasswordHash: testBcrypt, Groups: []string{"team"}}}
	c.Auth.Proxy = Proxy{UserHeader: "X-Forwarded-User", TrustedProxies: []string{"10.0.0.1/32"}}
	c.Auth.OIDC.Issuer = "https://login.example.com"
	c.Auth.OIDC.ClientID = "asynqui"
	c.Auth.OIDC.RedirectURL = "https://asynqui.example.com/auth/callback"
	c.Authz.Default = RoleViewer
	c.Authz.Bindings = []Binding{{Groups: []string{"team"}, Role: RoleAdmin, Connections: []string{"main*"}}}
	return c
}

func validationErrors(c *Config) []string {
	var out []string
	for _, e := range c.validate() {
		out = append(out, e.Error())
	}
	return out
}

func TestValidConfigIsValid(t *testing.T) {
	for _, p := range []Provider{ProviderNone, ProviderBasic, ProviderProxy, ProviderOIDC} {
		c := validConfig()
		c.Auth.Provider = p
		if errs := validationErrors(c); errs != nil {
			t.Errorf("provider %s: unexpected errors %q", p, errs)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   []string
	}{
		// server, log, refresh
		{"listen addr", func(c *Config) { c.Server.Addr = "8080" }, []string{"server.addr: must be host:port"}},
		{"listen addr required", func(c *Config) { c.Server.Addr = "" }, []string{"server.addr: is required"}},
		{"listen addr any port", func(c *Config) { c.Server.Addr = "127.0.0.1:0" }, nil},
		{"listen addr bad port", func(c *Config) { c.Server.Addr = ":http" }, []string{"server.addr: must have a port from 1 to 65535"}},
		{"base_path ok", func(c *Config) { c.Server.BasePath = "/asynq/ui" }, nil},
		{"base_path relative", func(c *Config) { c.Server.BasePath = "asynq" }, []string{"server.base_path: must be empty or a clean path such as /asynq, without a trailing slash"}},
		{"base_path trailing slash", func(c *Config) { c.Server.BasePath = "/asynq/" }, []string{"server.base_path: must be empty or a clean path such as /asynq, without a trailing slash"}},
		{"base_path root", func(c *Config) { c.Server.BasePath = "/" }, []string{"server.base_path: must be empty or a clean path such as /asynq, without a trailing slash"}},
		{"base_path unclean", func(c *Config) { c.Server.BasePath = "/a/../b" }, []string{"server.base_path: must be empty or a clean path such as /asynq, without a trailing slash"}},
		{"trusted origins ok", func(c *Config) { c.Server.TrustedOrigins = []string{"https://a.example.com", "http://localhost:3000"} }, nil},
		{"trusted origins bad", func(c *Config) {
			c.Server.TrustedOrigins = []string{"a.example.com", "https://a.example.com/x", "ftp://a"}
		}, []string{
			"server.trusted_origins[0]: must be an origin such as https://example.com",
			"server.trusted_origins[1]: must be an origin such as https://example.com",
		}},
		{"shutdown_delay zero", func(c *Config) { c.Server.ShutdownDelay = 0 }, nil},
		{"shutdown_delay negative", func(c *Config) { c.Server.ShutdownDelay = -time.Second }, []string{"server.shutdown_delay: must not be negative"}},
		{"log level", func(c *Config) { c.Log.Level = "verbose" }, []string{"log.level: must be one of debug, info, warn, error"}},
		{"refresh", func(c *Config) { c.Refresh.Default = "3s" }, []string{"refresh.default: must be one of off, 2s, 5s, 10s"}},

		// connections
		{"no connections", func(c *Config) { c.Connections = nil }, []string{"connections: at least one connection is required"}},
		{"name required", func(c *Config) { c.Connections[1].Name = "" }, []string{"connections[1].name: is required"}},
		{"name uppercase", func(c *Config) { c.Connections[1].Name = "Main" }, []string{`connections[1].name: must match ^[a-z0-9][a-z0-9._-]{0,62}$`}},
		{"name leading dash", func(c *Config) { c.Connections[1].Name = "-x" }, []string{`connections[1].name: must match ^[a-z0-9][a-z0-9._-]{0,62}$`}},
		{"name slash", func(c *Config) { c.Connections[1].Name = "a/b" }, []string{`connections[1].name: must match ^[a-z0-9][a-z0-9._-]{0,62}$`}},
		{"name 63 chars", func(c *Config) { c.Connections[1].Name = strings.Repeat("a", 63) }, nil},
		{"name 64 chars", func(c *Config) { c.Connections[1].Name = strings.Repeat("a", 64) }, []string{`connections[1].name: must match ^[a-z0-9][a-z0-9._-]{0,62}$`}},
		{"name punctuation", func(c *Config) { c.Connections[1].Name = "0.a_b-c" }, nil},
		{"duplicate names name both entries", func(c *Config) {
			c.Connections[2].Name = "main"
			c.Connections = append(c.Connections, c.Connections[2])
		}, []string{
			"connections[2].name: duplicate name, already used by connections[0]",
			"connections[3].name: duplicate name, already used by connections[0]",
		}},
		{"addr required", func(c *Config) { c.Connections[0].Addr = "" }, []string{"connections[0].addr: is required"}},
		{"addr without port", func(c *Config) { c.Connections[0].Addr = "redis" }, []string{"connections[0].addr: must be host:port"}},
		{"addr without host", func(c *Config) { c.Connections[0].Addr = ":6379" }, []string{"connections[0].addr: must include a host"}},
		{"addr port zero", func(c *Config) { c.Connections[0].Addr = "redis:0" }, []string{"connections[0].addr: must have a port from 1 to 65535"}},
		{"addr port range", func(c *Config) { c.Connections[0].Addr = "redis:65536" }, []string{"connections[0].addr: must have a port from 1 to 65535"}},
		{"addr url", func(c *Config) { c.Connections[0].Addr = "redis://redis:6379" }, []string{"connections[0].addr: must be host:port"}},
		{"db above 15 allowed", func(c *Config) { c.Connections[0].DB = 99 }, nil},
		{"db negative", func(c *Config) { c.Connections[0].DB = -1 }, []string{"connections[0].db: must not be negative"}},
		{"cert without key", func(c *Config) { c.Connections[0].TLS.CertFile = "c.pem" }, []string{"connections[0].tls: cert_file and key_file must be set together"}},
		{"cert and key", func(c *Config) { c.Connections[0].TLS.CertFile, c.Connections[0].TLS.KeyFile = "c.pem", "k.pem" }, nil},
		{"timeouts", func(c *Config) {
			c.Connections[0].DialTimeout, c.Connections[0].ReadTimeout, c.Connections[0].WriteTimeout = 0, -1, -time.Second
		}, []string{
			"connections[0].dial_timeout: must be a positive duration",
			"connections[0].read_timeout: must be a positive duration",
			"connections[0].write_timeout: must be a positive duration",
		}},

		// auth
		{"unknown provider", func(c *Config) { c.Auth.Provider = "ldap" }, []string{"auth.provider: must be one of none, basic, proxy, oidc"}},
		{"none ignores other blocks", func(c *Config) { c.Auth.Session = Session{}; c.Auth.Basic = Basic{}; c.Auth.OIDC = OIDC{} }, nil},
		{"session keys missing", func(c *Config) {
			c.Auth.Provider = ProviderBasic
			c.Auth.Session.HashKey, c.Auth.Session.BlockKey = "", ""
		}, []string{
			"auth.session.hash_key: is required (hex, 32 or 64 bytes; generate with openssl rand -hex 32)",
			"auth.session.block_key: is required (hex, 32 bytes; generate with openssl rand -hex 32)",
		}},
		{"session keys not hex", func(c *Config) {
			c.Auth.Provider = ProviderOIDC
			c.Auth.Session.HashKey, c.Auth.Session.BlockKey = "not-hex-secret", "zz"
		}, []string{
			"auth.session.hash_key: must be hex-encoded (32 or 64 bytes)",
			"auth.session.block_key: must be hex-encoded (32 bytes)",
		}},
		{"session key lengths", func(c *Config) {
			c.Auth.Provider = ProviderBasic
			c.Auth.Session.HashKey = Secret(strings.Repeat("ab", 16))
			c.Auth.Session.BlockKey = Secret(strings.Repeat("ab", 24))
		}, []string{
			"auth.session.hash_key: must decode to 32 or 64 bytes, got 16",
			"auth.session.block_key: must decode to 32 bytes, got 24",
		}},
		{"identical keys", func(c *Config) { c.Auth.Provider = ProviderBasic; c.Auth.Session.BlockKey = c.Auth.Session.HashKey }, []string{
			"auth.session.block_key: must differ from hash_key",
		}},
		{"key with a trailing newline", func(c *Config) { c.Auth.Provider = ProviderBasic; c.Auth.Session.HashKey += "\n" }, []string{
			"auth.session.hash_key: contains whitespace, such as a trailing newline",
		}},
		{"key with inner space", func(c *Config) {
			c.Auth.Provider = ProviderBasic
			c.Auth.Session.BlockKey = Secret(testBlockKey[:10] + " " + testBlockKey[11:])
		}, []string{
			"auth.session.block_key: contains whitespace, such as a trailing newline",
		}},
		{"bcrypt hashes", func(c *Config) {
			c.Auth.Provider = ProviderBasic
			for _, h := range []string{
				"$2b$99$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW", // cost out of range
				"$2b$12$!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!", // not bcrypt base64
				"$2x$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW", // unknown version
				"$2y$04$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW", // ok
				"$2a$31$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMU",  // too short
			} {
				c.Auth.Basic.Users = append(c.Auth.Basic.Users, BasicUser{Username: fmt.Sprint(len(c.Auth.Basic.Users)), PasswordHash: Secret(h)})
			}
		}, []string{
			"auth.basic.users[1].password_hash: must be a bcrypt hash such as htpasswd -nbBC 12 prints",
			"auth.basic.users[2].password_hash: must be a bcrypt hash such as htpasswd -nbBC 12 prints",
			"auth.basic.users[3].password_hash: must be a bcrypt hash such as htpasswd -nbBC 12 prints",
			"auth.basic.users[5].password_hash: must be a bcrypt hash such as htpasswd -nbBC 12 prints",
		}},
		{"64-byte hash key", func(c *Config) {
			c.Auth.Provider = ProviderBasic
			c.Auth.Session.HashKey = Secret(strings.Repeat("ab", 64))
		}, nil},
		{"previous keys", func(c *Config) {
			c.Auth.Provider = ProviderBasic
			c.Auth.Session.PreviousKeys = []KeyPair{{HashKey: testHashKey, BlockKey: testBlockKey}, {HashKey: "00", BlockKey: ""}}
		}, []string{
			"auth.session.previous_keys[1].hash_key: must decode to 32 or 64 bytes, got 1",
			"auth.session.previous_keys[1].block_key: is required (hex, 32 bytes; generate with openssl rand -hex 32)",
		}},
		{"session ttl", func(c *Config) { c.Auth.Provider = ProviderBasic; c.Auth.Session.TTL = 0 }, []string{"auth.session.ttl: must be a positive duration"}},
		{"proxy needs no session", func(c *Config) { c.Auth.Provider = ProviderProxy; c.Auth.Session = Session{} }, nil},
		{"basic without users", func(c *Config) { c.Auth.Provider = ProviderBasic; c.Auth.Basic.Users = nil }, []string{"auth.basic.users: at least one user is required"}},
		{"basic user fields", func(c *Config) {
			c.Auth.Provider = ProviderBasic
			c.Auth.Basic.Users = append(c.Auth.Basic.Users,
				BasicUser{Username: "JDoe", PasswordHash: "plaintext-password", Groups: []string{""}},
				BasicUser{},
			)
		}, []string{
			"auth.basic.users[1].username: duplicate username (case-insensitive), already used by auth.basic.users[0]",
			"auth.basic.users[1].password_hash: must be a bcrypt hash such as htpasswd -nbBC 12 prints",
			"auth.basic.users[1].groups[0]: must not be empty",
			"auth.basic.users[2].username: is required",
			"auth.basic.users[2].password_hash: is required",
		}},
		{"proxy fields", func(c *Config) {
			c.Auth.Provider = ProviderProxy
			c.Auth.Proxy = Proxy{
				UserHeader: "X User", GroupsHeader: "X:Groups", LogoutURL: "ftp://x",
				TrustedProxies: []string{"10.0.0.1", "10.0.0.0/8", "::1/128", "::1", "nope", "10.1.2.3/8", "10.0.0.1/33"},
			}
		}, []string{
			"auth.proxy.user_header: must be a valid header name",
			"auth.proxy.groups_header: must be a valid header name",
			"auth.proxy.trusted_proxies[4]: must be an IP or a CIDR such as 10.0.0.0/8",
			"auth.proxy.trusted_proxies[5]: CIDR has host bits set; use the network address, such as 10.0.0.0/8",
			"auth.proxy.trusted_proxies[6]: must be an IP or a CIDR such as 10.0.0.0/8",
			"auth.proxy.logout_url: must be an absolute http(s) URL or a path starting with a single /",
		}},
		{"proxy required", func(c *Config) { c.Auth.Provider = ProviderProxy; c.Auth.Proxy = Proxy{} }, []string{
			"auth.proxy.user_header: is required",
			"auth.proxy.trusted_proxies: at least one IP or CIDR is required",
		}},
		{"proxy logout protocol-relative", func(c *Config) { c.Auth.Provider = ProviderProxy; c.Auth.Proxy.LogoutURL = "//evil.example" }, []string{
			"auth.proxy.logout_url: must be an absolute http(s) URL or a path starting with a single /",
		}},
		{"proxy logout absolute", func(c *Config) {
			c.Auth.Provider = ProviderProxy
			c.Auth.Proxy.LogoutURL = "https://sso.example.com/logout"
		}, nil},
		{"proxy logout path", func(c *Config) { c.Auth.Provider = ProviderProxy; c.Auth.Proxy.LogoutURL = "/oauth2/sign_out" }, nil},
		{"oidc required", func(c *Config) {
			c.Auth.Provider = ProviderOIDC
			c.Auth.OIDC = OIDC{}
		}, []string{
			"auth.oidc.issuer: is required",
			"auth.oidc.client_id: is required",
			"auth.oidc.redirect_url: is required",
			"auth.oidc.username_claim: is required",
			"auth.oidc.groups_claim: is required",
		}},
		{"oidc shapes", func(c *Config) {
			c.Auth.Provider = ProviderOIDC
			c.Auth.OIDC.Issuer = "login.example.com"
			c.Auth.OIDC.RedirectURL = "/auth/callback"
			c.Auth.OIDC.Scopes = []string{"profile", ""}
		}, []string{
			"auth.oidc.issuer: must be an absolute http(s) URL",
			"auth.oidc.redirect_url: must be an absolute http(s) URL",
			"auth.oidc.scopes[1]: must not be empty",
		}},
		{"oidc client secret optional", func(c *Config) { c.Auth.Provider = ProviderOIDC; c.Auth.OIDC.ClientSecret = "" }, nil},

		// authz
		{"default role", func(c *Config) { c.Authz.Default = "superuser" }, []string{"authz.default: must be one of none, viewer, operator, admin"}},
		{"default none", func(c *Config) { c.Authz.Default = RoleNone }, nil},
		{"binding role", func(c *Config) { c.Authz.Bindings[0].Role = "root" }, []string{"authz.bindings[0].role: must be one of viewer, operator, admin"}},
		{"binding role none", func(c *Config) { c.Authz.Bindings[0].Role = RoleNone }, []string{"authz.bindings[0].role: must be one of viewer, operator, admin"}},
		{"binding role missing", func(c *Config) { c.Authz.Bindings[0].Role = "" }, []string{"authz.bindings[0].role: must be one of viewer, operator, admin"}},
		{"binding subjects", func(c *Config) { c.Authz.Bindings[0].Groups = nil }, []string{"authz.bindings[0]: must list users or groups"}},
		{"binding empty subjects", func(c *Config) {
			c.Authz.Bindings[0].Users = []string{"a", " "}
			c.Authz.Bindings[0].Groups = []string{""}
		}, []string{
			"authz.bindings[0].users[1]: must not be empty",
			"authz.bindings[0].groups[0]: must not be empty",
		}},
		{"binding connections", func(c *Config) { c.Authz.Bindings[0].Connections = nil }, []string{"authz.bindings[0].connections: must list at least one connection glob"}},
		{"binding globs", func(c *Config) { c.Authz.Bindings[0].Connections = []string{"", "[", "main\\", "[a-", "m?in", "[!x]*"} }, []string{
			"authz.bindings[0].connections[0]: must not be empty",
			"authz.bindings[0].connections[1]: must be a valid glob (path.Match syntax)",
			"authz.bindings[0].connections[2]: must be a valid glob (path.Match syntax)",
			"authz.bindings[0].connections[3]: must be a valid glob (path.Match syntax)",
		}},
		{"glob matching nothing is not an error", func(c *Config) { c.Authz.Bindings[0].Connections = []string{"staging-*"} }, nil},

		// collection
		{"every error is reported", func(c *Config) {
			c.Log.Level = ""
			c.Connections[0].Name = ""
			c.Connections[1].Addr = "x"
			c.Authz.Bindings[0].Role = "boss"
		}, []string{
			"log.level: must be one of debug, info, warn, error",
			"connections[0].name: is required",
			"connections[1].addr: must be host:port",
			"authz.bindings[0].role: must be one of viewer, operator, admin",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(c)
			if diff := cmp.Diff(tt.want, validationErrors(c)); diff != "" {
				t.Errorf("errors (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWarnings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   []string
	}{
		{"none", func(*Config) {}, nil},
		{"globs matching no connection", func(c *Config) {
			c.Authz.Bindings = append(c.Authz.Bindings, Binding{Users: []string{"u"}, Role: RoleViewer, Connections: []string{"local", "staging-*", "lcoal"}})
		}, []string{
			`authz.bindings[1].connections[1]: "staging-*" matches no connection`,
			`authz.bindings[1].connections[2]: "lcoal" matches no connection`,
		}},
		{"tls options while disabled", func(c *Config) {
			c.Connections[0].TLS = TLS{CAFile: "/ca.pem"}
			c.Connections[1].TLS = TLS{InsecureSkipVerify: true}
			c.Connections[2].TLS = TLS{Enabled: true, CAFile: "/ca.pem"}
		}, []string{
			"connections[0].tls: options are set but tls.enabled is false, so they are ignored",
			"connections[1].tls: options are set but tls.enabled is false, so they are ignored",
		}},
		{"oidc redirect elsewhere", func(c *Config) {
			c.Auth.Provider = ProviderOIDC
			c.Server.BasePath = "/asynq"
		}, []string{`auth.oidc.redirect_url: path does not match the callback served at "/asynq/auth/callback"`}},
		{"oidc redirect under base path", func(c *Config) {
			c.Auth.Provider = ProviderOIDC
			c.Server.BasePath = "/asynq"
			c.Auth.OIDC.RedirectURL = "https://x.example.com/asynq/auth/callback"
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(c)
			if diff := cmp.Diff(tt.want, c.Warnings()); diff != "" {
				t.Errorf("warnings (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSessionKeyPairs(t *testing.T) {
	s := Session{
		HashKey: testHashKey, BlockKey: testBlockKey,
		PreviousKeys: []KeyPair{{HashKey: Secret(strings.Repeat("00", 64)), BlockKey: Secret(strings.Repeat("11", 32))}},
	}
	pairs, err := s.KeyPairs()
	if err != nil {
		t.Fatal(err)
	}
	var lens []int
	for _, p := range pairs {
		lens = append(lens, len(p))
	}
	if diff := cmp.Diff([]int{32, 32, 64, 32}, lens); diff != "" {
		t.Errorf("key lengths (-want +got):\n%s", diff)
	}
	if string(pairs[0]) != "abcdefghijklmnopqrstuvwxyz012345" {
		t.Errorf("hash key = %q", pairs[0])
	}

	s.PreviousKeys[0].BlockKey = "secret-not-hex"
	_, err = s.KeyPairs()
	if err == nil || err.Error() != "session key pair 1: block_key must be hex-encoded (32 bytes)" {
		t.Errorf("KeyPairs error = %v", err)
	}
}

// TestTrustedOriginsMatchTheWebServer checks that validation accepts exactly
// what http.CrossOriginProtection.AddTrustedOrigin accepts.
func TestTrustedOriginsMatchTheWebServer(t *testing.T) {
	for _, o := range []string{
		"https://a.example.com", "http://localhost:3000", "https://a.example.com:8443", "HTTPS://A.EXAMPLE.COM",
		"https://a.example.com/", "https://a.example.com/x", "a.example.com", "ftp://a", "https://u@a",
		"https://a?x=1", "null", "https://a.example.com:99999", "https://a.example.com#", "https://*.example.com",
		"http://[::1", "https://a.example.com:", "",
	} {
		c := validConfig()
		c.Server.TrustedOrigins = []string{o}
		gotValid := len(validationErrors(c)) == 0
		wantValid := http.NewCrossOriginProtection().AddTrustedOrigin(o) == nil
		if gotValid != wantValid {
			t.Errorf("origin %q: valid = %v, AddTrustedOrigin accepts = %v", o, gotValid, wantValid)
		}
	}
}

func TestValidGlob(t *testing.T) {
	tests := map[string]bool{
		"*": true, "main*": true, "m?in": true, "[a-c]*": true, "[!x]y": true, "[^x]y": true, `a\*`: true,
		"[": false, "a[": false, "*[": false, "[]": false, "[^]": false, `a\`: false, "ab[c": false, "[a-": false,
	}
	for g, want := range tests {
		if got := validGlob(g); got != want {
			t.Errorf("validGlob(%q) = %v, want %v", g, got, want)
		}
	}
}

func TestTrustedPrefixes(t *testing.T) {
	p := Proxy{TrustedProxies: []string{"10.0.0.1", "10.0.0.0/8", "::1"}}
	got, err := p.TrustedPrefixes()
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, pfx := range got {
		s = append(s, pfx.String())
	}
	if diff := cmp.Diff([]string{"10.0.0.1/32", "10.0.0.0/8", "::1/128"}, s); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if _, err := (Proxy{TrustedProxies: []string{"10.1.2.3/8"}}).TrustedPrefixes(); err == nil {
		t.Error("unmasked CIDR accepted")
	}
}
