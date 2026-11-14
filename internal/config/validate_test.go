package config

import (
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

func TestValidConfigIsValid(t *testing.T) {
	for _, p := range []Provider{ProviderNone, ProviderBasic, ProviderProxy, ProviderOIDC} {
		c := validConfig()
		c.Auth.Provider = p
		if err := c.validate(); err != nil {
			t.Errorf("provider %s: %v", p, err)
		}
	}
}

func TestValidate(t *testing.T) {
	basic := func(c *Config) { c.Auth.Provider = ProviderBasic }
	proxy := func(c *Config) { c.Auth.Provider = ProviderProxy }
	oidc := func(c *Config) { c.Auth.Provider = ProviderOIDC }
	tests := []struct {
		name   string
		mutate func(*Config)
		want   []string
	}{
		{"listen addr", func(c *Config) { c.Server.Addr = "8080" }, []string{"server.addr: must be host:port"}},
		{"listen addr port 0 is fine", func(c *Config) { c.Server.Addr = "127.0.0.1:0" }, nil},
		{"base_path", func(c *Config) { c.Server.BasePath = "/asynq/" }, []string{"server.base_path: must be empty or a clean path such as /asynq, without a trailing slash"}},
		{"trusted origin", func(c *Config) { c.Server.TrustedOrigins = []string{"https://ok.example.com", "a.example.com"} }, []string{"server.trusted_origins[1]: must be an origin such as https://example.com"}},
		{"shutdown_delay", func(c *Config) { c.Server.ShutdownDelay = -time.Second }, []string{"server.shutdown_delay: must not be negative"}},
		{"log level", func(c *Config) { c.Log.Level = "verbose" }, []string{"log.level: must be one of debug, info, warn, error"}},
		{"refresh", func(c *Config) { c.Refresh.Default = "3s" }, []string{"refresh.default: must be one of off, 2s, 5s, 10s"}},

		{"no connections", func(c *Config) { c.Connections = nil }, []string{"connections: at least one connection is required"}},
		{"name required", func(c *Config) { c.Connections[1].Name = "" }, []string{"connections[1].name: is required"}},
		{"name pattern", func(c *Config) { c.Connections[1].Name = "Main" }, []string{"connections[1].name: must match ^[a-z0-9][a-z0-9._-]{0,62}$"}},
		{"name length", func(c *Config) { c.Connections[1].Name = strings.Repeat("a", 64) }, []string{"connections[1].name: must match ^[a-z0-9][a-z0-9._-]{0,62}$"}},
		{"duplicate name", func(c *Config) { c.Connections[2].Name = "main" }, []string{"connections[2].name: duplicate of connections[0]"}},
		{"redis addr", func(c *Config) { c.Connections[0].Addr = ":6379" }, []string{"connections[0].addr: must include a host"}},
		{"redis port", func(c *Config) { c.Connections[0].Addr = "redis:0" }, []string{"connections[0].addr: must have a port from 1 to 65535"}},
		{"db", func(c *Config) { c.Connections[0].DB = -1 }, []string{"connections[0].db: must not be negative"}},
		{"cert without key", func(c *Config) { c.Connections[0].TLS.CertFile = "c.pem" }, []string{"connections[0].tls: cert_file and key_file must be set together"}},
		{"timeout", func(c *Config) { c.Connections[0].ReadTimeout = 0 }, []string{"connections[0].read_timeout: must be a positive duration"}},

		{"provider", func(c *Config) { c.Auth.Provider = "ldap" }, []string{"auth.provider: must be one of none, basic, proxy, oidc"}},
		{"none ignores other blocks", func(c *Config) { c.Auth.Session, c.Auth.Basic, c.Auth.OIDC = Session{}, Basic{}, OIDC{} }, nil},
		{"session keys required", func(c *Config) { basic(c); c.Auth.Session.HashKey = "" }, []string{"auth.session.hash_key: is required (hex, 32 or 64 bytes; generate with openssl rand -hex 32)"}},
		{"session key hex", func(c *Config) { oidc(c); c.Auth.Session.BlockKey = "zz" }, []string{"auth.session.block_key: must be hex-encoded (32 bytes)"}},
		{"session key length", func(c *Config) { basic(c); c.Auth.Session.HashKey = Secret(strings.Repeat("ab", 16)) }, []string{"auth.session.hash_key: must decode to 32 or 64 bytes, got 16"}},
		{"session key whitespace", func(c *Config) { basic(c); c.Auth.Session.HashKey += "\n" }, []string{"auth.session.hash_key: contains whitespace, such as a trailing newline"}},
		{"session keys differ", func(c *Config) { basic(c); c.Auth.Session.BlockKey = c.Auth.Session.HashKey }, []string{"auth.session.block_key: must differ from hash_key"}},
		{"previous keys", func(c *Config) {
			basic(c)
			c.Auth.Session.PreviousKeys = []KeyPair{{HashKey: testHashKey, BlockKey: "00"}}
		}, []string{"auth.session.previous_keys[0].block_key: must decode to 32 bytes, got 1"}},
		{"session ttl", func(c *Config) { basic(c); c.Auth.Session.TTL = 0 }, []string{"auth.session.ttl: must be a positive duration"}},
		{"basic users", func(c *Config) { basic(c); c.Auth.Basic.Users = nil }, []string{"auth.basic.users: at least one user is required"}},
		{"basic username", func(c *Config) {
			basic(c)
			c.Auth.Basic.Users = append(c.Auth.Basic.Users, BasicUser{Username: "JDoe", PasswordHash: testBcrypt, Groups: []string{" "}})
		}, []string{"auth.basic.users[1].username: duplicate (case-insensitive) of auth.basic.users[0]", "auth.basic.users[1].groups[0]: must not be empty"}},
		{"basic usernames differ beyond ASCII case", func(c *Config) {
			basic(c)
			c.Auth.Basic.Users = []BasicUser{{Username: "Äda", PasswordHash: testBcrypt}, {Username: "äda", PasswordHash: testBcrypt}, {Username: "\u212Aim", PasswordHash: testBcrypt}, {Username: "kim", PasswordHash: testBcrypt}}
		}, nil},
		{"bcrypt costs differ", func(c *Config) {
			basic(c)
			c.Auth.Basic.Users = append(c.Auth.Basic.Users, BasicUser{Username: "b", PasswordHash: Secret("$2b$11" + testBcrypt[6:])})
		}, []string{"auth.basic.users[1].password_hash: has bcrypt cost 11, auth.basic.users[0] has 12; rehash all passwords at one cost"}},
		{"bcrypt", func(c *Config) { basic(c); c.Auth.Basic.Users[0].PasswordHash = Secret("$2b$99" + testBcrypt[6:]) }, []string{"auth.basic.users[0].password_hash: must be a bcrypt hash such as htpasswd -nbBC 12 prints"}},
		{"proxy headers", func(c *Config) { proxy(c); c.Auth.Proxy.UserHeader, c.Auth.Proxy.GroupsHeader = "X User", "X:G" }, []string{"auth.proxy.user_header: must be a valid header name", "auth.proxy.groups_header: must be a valid header name"}},
		{"proxy trusted required", func(c *Config) { proxy(c); c.Auth.Proxy.TrustedProxies = nil }, []string{"auth.proxy.trusted_proxies: at least one IP or CIDR is required"}},
		{"proxy trusted shape", func(c *Config) { proxy(c); c.Auth.Proxy.TrustedProxies = []string{"::1", "nope", "10.1.2.3/8"} }, []string{
			"auth.proxy.trusted_proxies[1]: must be an IP or a CIDR such as 10.0.0.0/8",
			"auth.proxy.trusted_proxies[2]: CIDR has host bits set; use the network address, such as 10.0.0.0/8",
		}},
		{"proxy logout url", func(c *Config) { proxy(c); c.Auth.Proxy.LogoutURL = "//evil.example" }, []string{"auth.proxy.logout_url: must be an absolute http(s) URL or a path starting with a single /"}},
		{"proxy needs no session", func(c *Config) { proxy(c); c.Auth.Session = Session{} }, nil},
		{"oidc required", func(c *Config) { oidc(c); c.Auth.OIDC = OIDC{} }, []string{
			"auth.oidc.issuer: is required", "auth.oidc.client_id: is required", "auth.oidc.redirect_url: is required",
			"auth.oidc.username_claim: is required", "auth.oidc.groups_claim: is required",
		}},
		{"oidc urls and scopes", func(c *Config) { oidc(c); c.Auth.OIDC.Issuer, c.Auth.OIDC.Scopes = "login.example.com", []string{""} }, []string{
			"auth.oidc.issuer: must be an absolute http(s) URL", "auth.oidc.scopes[0]: must not be empty",
		}},

		{"default role", func(c *Config) { c.Authz.Default = "superuser" }, []string{"authz.default: must be one of none, viewer, operator, admin"}},
		{"binding role", func(c *Config) { c.Authz.Bindings[0].Role = RoleNone }, []string{"authz.bindings[0].role: must be one of viewer, operator, admin"}},
		{"binding subjects", func(c *Config) { c.Authz.Bindings[0].Groups = nil }, []string{"authz.bindings[0]: must list users or groups"}},
		{"binding connections", func(c *Config) { c.Authz.Bindings[0].Connections = nil }, []string{"authz.bindings[0].connections: must list at least one connection glob"}},
		{"binding globs", func(c *Config) { c.Authz.Bindings[0].Connections = []string{"m?in", "", "["} }, []string{
			"authz.bindings[0].connections[1]: must be a valid, non-empty glob (path.Match syntax)",
			"authz.bindings[0].connections[2]: must be a valid, non-empty glob (path.Match syntax)",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(c)
			if diff := cmp.Diff(tt.want, errLines(c.validate())); diff != "" {
				t.Errorf("errors (-want +got):\n%s", diff)
			}
		})
	}
}

func TestWarnings(t *testing.T) {
	c := validConfig()
	c.Authz.Bindings = append(c.Authz.Bindings, Binding{Users: []string{"u"}, Role: RoleViewer, Connections: []string{"local", "lcoal"}})
	c.Connections[0].TLS = TLS{CAFile: "/ca.pem"}
	c.Auth.Provider = ProviderOIDC
	c.Server.BasePath = "/asynq"
	want := []string{
		`authz.bindings[1].connections[1]: "lcoal" matches no connection`,
		"connections[0].tls: options are set but tls.enabled is false, so they are ignored",
		`auth.oidc.redirect_url: path does not match the callback served at "/asynq/auth/callback"`,
	}
	if diff := cmp.Diff(want, c.Warnings()); diff != "" {
		t.Errorf("warnings (-want +got):\n%s", diff)
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
}

// TestTrustedOriginsMatchTheWebServer checks that validation accepts exactly
// what http.CrossOriginProtection.AddTrustedOrigin accepts.
func TestTrustedOriginsMatchTheWebServer(t *testing.T) {
	for _, o := range []string{"https://a.example.com", "http://localhost:3000", "https://a.example.com/x", "a.example.com", "null", ""} {
		c := validConfig()
		c.Server.TrustedOrigins = []string{o}
		gotValid := c.validate() == nil
		wantValid := http.NewCrossOriginProtection().AddTrustedOrigin(o) == nil
		if gotValid != wantValid {
			t.Errorf("origin %q: valid = %v, AddTrustedOrigin accepts = %v", o, gotValid, wantValid)
		}
	}
}

func TestTrustedPrefixes(t *testing.T) {
	got, err := Proxy{TrustedProxies: []string{"10.0.0.1", "10.0.0.0/8", "::1"}}.TrustedPrefixes()
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
}
