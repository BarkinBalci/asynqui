package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const (
	testHashKey  = "6162636465666768696a6b6c6d6e6f707172737475767778797a303132333435" // 32 bytes
	testBlockKey = "3132333435363738393031323334353637383930313233343536373839303132" // 32 bytes
	testBcrypt   = "$2b$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW"
)

// errLines splits a joined error into its messages.
func errLines(err error) []string {
	if err == nil {
		return nil
	}
	return strings.Split(err.Error(), "\n")
}

// mustParse parses src with vars and fails the test on error.
func mustParse(t *testing.T, src string, vars map[string]string) *Config {
	t.Helper()
	cfg, err := parse([]byte(src), env(vars), Overrides{})
	if err != nil {
		t.Fatalf("parse:\n%v", err)
	}
	return cfg
}

// parseErrors parses src with vars and returns the error messages.
func parseErrors(src string, vars map[string]string) []string {
	_, err := parse([]byte(src), env(vars), Overrides{})
	return errLines(err)
}

func defaultTimeouts(c Connection) Connection {
	c.DialTimeout, c.ReadTimeout, c.WriteTimeout = 5*time.Second, 3*time.Second, 3*time.Second
	return c
}

func TestLoadExample(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	cfg, err := Load(path, env(nil), Overrides{})
	if err != nil {
		t.Fatalf("config.example.yaml must load without any environment variable:\n%v", err)
	}
	want := &Config{
		Server:      Server{Addr: ":8080", TrustedOrigins: []string{}, ShutdownDelay: 5 * time.Second},
		Log:         Log{Level: "info"},
		Refresh:     Refresh{Default: "5s"},
		UpdateCheck: true,
		Connections: []Connection{
			defaultTimeouts(Connection{Name: "local", Addr: "localhost:6379"}),
			defaultTimeouts(Connection{Name: "local-jobs", Addr: "localhost:6379", DB: 3}),
		},
		Auth: Auth{
			Provider: ProviderNone,
			Session:  Session{TTL: 12 * time.Hour, CookieSecure: true},
			Proxy:    Proxy{UserHeader: "X-Forwarded-User"},
			OIDC:     OIDC{Scopes: []string{"openid", "profile", "email"}, UsernameClaim: "email", GroupsClaim: "groups"},
		},
		Authz: Authz{Default: RoleViewer, Bindings: []Binding{}},
	}
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Errorf("config.example.yaml mismatch (-want +got):\n%s", diff)
	}
	if w := cfg.Warnings(); len(w) != 0 {
		t.Errorf("config.example.yaml has warnings: %q", w)
	}

	cfg, err = Load(path, env(map[string]string{"LOCAL_REDIS_PASSWORD": "s3cret"}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Connections[0].Password.Reveal(); got != "s3cret" {
		t.Errorf("password = %q, want s3cret", got)
	}
}

// TestLoadDevConfig checks the config `task run` uses.
func TestLoadDevConfig(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.dev.yaml"), env(nil), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{string(cfg.Auth.Provider), string(cfg.Authz.Default), cfg.Connections[0].Addr}
	if diff := cmp.Diff([]string{"none", "admin", "localhost:6379"}, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

// TestLoadExampleProviders uncomments the commented-out blocks of
// config.example.yaml and checks that each provider loads with them.
func TestLoadExampleProviders(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	keyLine := regexp.MustCompile(`^  # (\s*(?:- )?[a-z_]+:.*)$`)
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if keyLine.MatchString(l) && !strings.Contains(l, "provider:") {
			lines[i] = keyLine.ReplaceAllString(l, "  $1")
		}
	}
	src := strings.Join(lines, "\n")
	src = strings.Replace(src, "bindings: []", "bindings:", 1)
	src = strings.Replace(src, "$2y$12$<53 chars from htpasswd>", testBcrypt, 1)
	vars := map[string]string{
		"ASYNQUI_SESSION_HASH_KEY":   testHashKey,
		"ASYNQUI_SESSION_BLOCK_KEY":  testBlockKey,
		"ASYNQUI_OIDC_CLIENT_SECRET": "client-secret",
	}
	for _, provider := range []Provider{ProviderBasic, ProviderProxy, ProviderOIDC} {
		t.Run(string(provider), func(t *testing.T) {
			src := strings.Replace(src, "\n  provider: none\n", "\n  provider: "+string(provider)+"\n", 1)
			cfg := mustParse(t, src, vars)
			if cfg.Auth.Provider != provider {
				t.Errorf("provider = %q, want %q", cfg.Auth.Provider, provider)
			}
			if len(cfg.Auth.Basic.Users) != 1 || len(cfg.Authz.Bindings) != 3 || cfg.Auth.OIDC.ClientID == "" {
				t.Errorf("commented-out blocks were not all decoded: %+v", cfg)
			}
			if w := cfg.Warnings(); len(w) != 0 {
				t.Errorf("warnings: %q", w)
			}
		})
	}
}

func TestParseDefaults(t *testing.T) {
	cfg := mustParse(t, "connections:\n  - {name: local, addr: 'localhost:6379'}\n", nil)
	want := &Config{
		Server:      Server{Addr: ":8080", ShutdownDelay: 5 * time.Second},
		Log:         Log{Level: "info"},
		Refresh:     Refresh{Default: "5s"},
		UpdateCheck: true,
		Connections: []Connection{defaultTimeouts(Connection{Name: "local", Addr: "localhost:6379"})},
		Auth: Auth{
			Provider: ProviderNone,
			Session:  Session{TTL: 12 * time.Hour, CookieSecure: true},
			Proxy:    Proxy{UserHeader: "X-Forwarded-User"},
			OIDC:     OIDC{Scopes: []string{"openid", "profile", "email"}, UsernameClaim: "email", GroupsClaim: "groups"},
		},
		Authz: Authz{Default: RoleViewer},
	}
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Errorf("defaults mismatch (-want +got):\n%s", diff)
	}
}

func TestParseNormalizedValues(t *testing.T) {
	const conns = "connections: [{name: a, addr: 'h:1'}]\n"
	tests := []struct {
		name string
		yaml string
		vars map[string]string
		get  func(*Config) any
		want any
	}{
		{"authz.default is viewer with provider none", conns, nil,
			func(c *Config) any { return c.Authz.Default }, RoleViewer},
		{"authz.default is none with other providers",
			conns + "auth: {provider: proxy, proxy: {trusted_proxies: [10.0.0.1]}}\n", nil,
			func(c *Config) any { return c.Authz.Default }, RoleNone},
		{"explicit authz.default wins", conns + "authz: {default: admin}\n", nil,
			func(c *Config) any { return c.Authz.Default }, RoleAdmin},
		{"base_path / is empty", conns + "server: {base_path: /}\n", nil,
			func(c *Config) any { return c.Server.BasePath }, ""},
		{"proxy user_header default", conns + "auth: {provider: proxy, proxy: {trusted_proxies: [10.0.0.0/8]}}\n", nil,
			func(c *Config) any { return c.Auth.Proxy.UserHeader }, "X-Forwarded-User"},
		{"openid is added to scopes", conns + "auth: {oidc: {scopes: [profile, groups]}}\n", nil,
			func(c *Config) any { return c.Auth.OIDC.Scopes }, []string{"openid", "profile", "groups"}},
		{"openid is not duplicated", conns + "auth: {oidc: {scopes: [email, openid]}}\n", nil,
			func(c *Config) any { return c.Auth.OIDC.Scopes }, []string{"email", "openid"}},
		{"empty scopes become openid", conns + "auth: {oidc: {scopes: []}}\n", nil,
			func(c *Config) any { return c.Auth.OIDC.Scopes }, []string{"openid"}},
		{"explicit zero shutdown_delay disables it", conns + "server: {shutdown_delay: 0s}\n", nil,
			func(c *Config) any { return c.Server.ShutdownDelay }, time.Duration(0)},
		{"explicit false booleans are kept", conns + "update_check: false\nauth: {session: {cookie_secure: false}}\n", nil,
			func(c *Config) any { return []bool{c.UpdateCheck, c.Auth.Session.CookieSecure} }, []bool{false, false}},
		{"per-connection timeouts override defaults independently", "connections: [{name: a, addr: 'h:1', read_timeout: 1s}]\n", nil,
			func(c *Config) any {
				return []time.Duration{c.Connections[0].DialTimeout, c.Connections[0].ReadTimeout, c.Connections[0].WriteTimeout}
			}, []time.Duration{5 * time.Second, time.Second, 3 * time.Second}},
		{"null values keep defaults", "connections: [{name: a, addr: 'h:1', dial_timeout: ~}]\nserver: {addr: ~}\nlog:\nauth:\n", nil,
			func(c *Config) any {
				return []string{c.Server.Addr, c.Log.Level, string(c.Auth.Provider), c.Connections[0].DialTimeout.String()}
			}, []string{":8080", "info", "none", "5s"}},
		{"empty interpolated value keeps a non-string default", "connections: [{name: a, addr: 'h:1', dial_timeout: '${D:-}', db: '${N:-}'}]\n", nil,
			func(c *Config) any { return []any{c.Connections[0].DialTimeout, c.Connections[0].DB} }, []any{5 * time.Second, 0}},
		{"literal yes is a boolean", conns + "update_check: no\n", nil,
			func(c *Config) any { return c.UpdateCheck }, false},
		{"interpolated yes is a boolean too", conns + "update_check: ${B}\n", map[string]string{"B": "no"},
			func(c *Config) any { return c.UpdateCheck }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mustParse(t, tt.yaml, tt.vars)
			if diff := cmp.Diff(tt.want, tt.get(cfg)); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseOverrides(t *testing.T) {
	src := "server: {addr: ':1'}\nlog: {level: debug}\nconnections: [{name: a, addr: 'h:1'}]\n"
	tests := []struct {
		name      string
		overrides Overrides
		want      []string
	}{
		{"none: file wins over defaults", Overrides{}, []string{":1", "debug"}},
		{"addr", Overrides{Addr: ":2"}, []string{":2", "debug"}},
		{"both", Overrides{Addr: "127.0.0.1:3", LogLevel: "warn"}, []string{"127.0.0.1:3", "warn"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parse([]byte(src), env(nil), tt.overrides)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, []string{cfg.Server.Addr, cfg.Log.Level}); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}

	// An invalid override is reported without the file's line.
	_, err := parse([]byte(src), env(nil), Overrides{Addr: "nope"})
	if diff := cmp.Diff([]string{"server.addr: must be host:port"}, errLines(err)); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestParseInterpolation(t *testing.T) {
	vars := map[string]string{
		"DB":      "3",
		"TLS":     "true",
		"TIMEOUT": "7s",
		"NULLPW":  "null",
		"NUMPW":   "123",
		"EMPTY":   "",
		"REF":     "${DB}",
		"HOST":    "redis",
	}
	src := `# a comment with ${UNSET} is not interpolated
connections:
  - name: a
    addr: ${HOST}:6379
    db: "${DB}"             # quoted, still an integer field
    username: '${HOST}'     # quoted string
    password: ${NULLPW}     # plain, but verbatim for a string field
    tls:
      enabled: ${TLS}
      server_name: ${EMPTY:-default}  # set but empty: used as is
    dial_timeout: ${TIMEOUT}
  - name: b
    addr: ${MISSING_HOST:-localhost}:${MISSING_PORT:-6380}
    password: ${NUMPW}
    username: $${HOST}
  - name: c
    addr: localhost:1
    password: ${REF}
    username: $2b$12$literal
`
	cfg := mustParse(t, src, vars)
	type view struct {
		Addr, Username, Password, ServerName string
		DB                                   int
		TLS                                  bool
		Dial                                 time.Duration
	}
	var got []view
	for _, c := range cfg.Connections {
		got = append(got, view{c.Addr, c.Username, c.Password.Reveal(), c.TLS.ServerName, c.DB, c.TLS.Enabled, c.DialTimeout})
	}
	want := []view{
		{Addr: "redis:6379", Username: "redis", Password: "null", DB: 3, TLS: true, Dial: 7 * time.Second},
		{Addr: "localhost:6380", Username: "${HOST}", Password: "123", Dial: 5 * time.Second},
		{Addr: "localhost:1", Username: "$2b$12$literal", Password: "${DB}", Dial: 5 * time.Second},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("interpolated connections (-want +got):\n%s", diff)
	}
}

func TestParseInterpolatedIntegersAreDecimal(t *testing.T) {
	tests := []struct {
		value   string
		want    int
		wantErr bool
	}{
		{"010", 10, false}, {"011", 11, false}, {"08", 8, false}, {"+3", 3, false}, {"0", 0, false},
		{"0x10", 0, true}, {"0o10", 0, true}, {"1_000", 0, true}, {"3.0", 0, true}, {" 3", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			src := "connections:\n  - name: a\n    addr: h:1\n    db: \"${DB}\"\n"
			cfg, err := parse([]byte(src), env(map[string]string{"DB": tt.value}), Overrides{})
			if tt.wantErr {
				if diff := cmp.Diff([]string{"line 4: connections[0].db: must be a decimal integer"}, errLines(err)); diff != "" {
					t.Errorf("(-want +got):\n%s", diff)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Connections[0].DB; got != tt.want {
				t.Errorf("DB=%q gives db %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseAliases(t *testing.T) {
	vars := map[string]string{"PW": "${OTHER}", "CA": "/ca.pem"}
	src := `connections:
  - name: a
    addr: h:1
    password: &pw ${PW}
    tls: &tls {enabled: true, ca_file: "${CA}"}
  - name: b
    addr: h:2
    password: *pw
    tls:
      <<: *tls
      server_name: b.example.com
`
	cfg := mustParse(t, src, vars)
	got := []string{
		cfg.Connections[0].Password.Reveal(), cfg.Connections[1].Password.Reveal(),
		cfg.Connections[0].TLS.CAFile, cfg.Connections[1].TLS.CAFile, cfg.Connections[1].TLS.ServerName,
	}
	want := []string{"${OTHER}", "${OTHER}", "/ca.pem", "/ca.pem", "b.example.com"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("aliases are expanded exactly once (-want +got):\n%s", diff)
	}
}

func TestParseErrors(t *testing.T) {
	vars := map[string]string{"X": "addr", "WORD": "hunter2", "NUM": "5", "SECRETISH": "TOPSECRETPW"}
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{
			name: "unknown fields at every level, all reported",
			yaml: "adr: ':1'\nconnections:\n  - name: a\n    addr: h:1\n    tls: {enable: true}\n    passwd: x\nauth:\n  oidc: {scope: [openid]}\n",
			want: []string{
				"line 1: adr: unknown field",
				"line 5: connections[0].tls.enable: unknown field",
				"line 6: connections[0].passwd: unknown field",
				"line 8: auth.oidc.scope: unknown field",
			},
		},
		{
			name: "keys are not interpolated",
			yaml: "connections: [{name: a, addr: 'h:1'}]\nserver:\n  ${X}: ':1'\n",
			want: []string{"line 3: server.${X}: unknown field"},
		},
		{
			name: "missing variables, all reported with location, not validated again",
			yaml: "connections:\n  - name: a\n    addr: ${REDIS_ADDR}\n    password: ${REDIS_PASSWORD}\n    db: ${REDIS_DB}\n",
			want: []string{
				"line 3: connections[0].addr: environment variable REDIS_ADDR is not set",
				"line 4: connections[0].password: environment variable REDIS_PASSWORD is not set",
				"line 5: connections[0].db: environment variable REDIS_DB is not set",
			},
		},
		{
			name: "missing variable inside a list",
			yaml: "connections: [{name: a, addr: 'h:1'}]\nauthz:\n  bindings:\n    - users: [a, '${ADMIN_USER}']\n      role: admin\n      connections: ['*']\n",
			want: []string{"line 4: authz.bindings[0].users[1]: environment variable ADMIN_USER is not set"},
		},
		{
			name: "values of the wrong type, literal or interpolated, are never echoed",
			yaml: "connections:\n  - db: ${WORD}\n    tls: {enabled: '${WORD}'}\n    dial_timeout: ${NUM}\n    read_timeout: hunter2\n    write_timeout: [1s]\n    name: {a: b}\nauth:\n  basic:\n    users: ${WORD}\n",
			want: []string{
				"line 2: connections[0].db: must be a decimal integer",
				"line 3: connections[0].tls.enabled: must be true or false",
				"line 4: connections[0].dial_timeout: must be a duration such as 5s",
				"line 5: connections[0].read_timeout: must be a duration such as 5s",
				"line 6: connections[0].write_timeout: must be a duration such as 5s, not a list",
				"line 7: connections[0].name: must be a string, not a mapping",
				"line 10: auth.basic.users: must be a list",
			},
		},
		{
			name: "explicit tags are checked too",
			yaml: "connections:\n  - {name: a, addr: 'h:1', db: !!str 3}\n",
			want: []string{"line 2: connections[0].db: must be a decimal integer"},
		},
		{
			name: "validation errors never echo values",
			yaml: "connections:\n  - name: Bad_Name\n    addr: ${SECRETISH}\nserver:\n  trusted_origins: ['https://${SECRETISH}.example.com/path']\nlog: {level: TOPSECRETPW}\n",
			want: []string{
				"line 2: connections[0].name: must match ^[a-z0-9][a-z0-9._-]{0,62}$",
				"line 3: connections[0].addr: must be host:port",
				"line 5: server.trusted_origins[0]: must be an origin such as https://example.com",
				"line 6: log.level: must be one of debug, info, warn, error",
			},
		},
		{
			name: "unknown fields and validation errors are reported together",
			yaml: "bogus: 1\nlog: {level: loud}\nconnections: [{name: a, addr: 'h:1'}]\n",
			want: []string{
				"line 1: bogus: unknown field",
				"line 2: log.level: must be one of debug, info, warn, error",
			},
		},
		{
			name: "type and validation errors are reported together",
			yaml: "log: {level: [a]}\nrefresh: {default: 3s}\nconnections: [{name: a, addr: 'h:1'}]\n",
			want: []string{
				"line 1: log.level: must be a string, not a list",
				"line 2: refresh.default: must be one of off, 2s, 5s, 10s",
			},
		},
		{
			name: "null list entries",
			yaml: "connections:\n  -\nauthz: {bindings: [~]}\nserver: {trusted_origins: [null]}\n",
			want: []string{
				"line 2: connections[0]: must not be empty",
				"line 3: authz.bindings[0]: must not be empty",
				"line 4: server.trusted_origins[0]: must not be empty",
			},
		},
		{
			name: "invalid reference",
			yaml: "connections: [{name: a, addr: 'h:1'}]\nlog: {level: '${lo-g}'}\n",
			want: []string{`line 2: log.level: invalid variable name "lo-g" in ${...}`},
		},
		{
			name: "unbalanced brace in a default",
			yaml: "connections: [{name: a, addr: 'h:1', username: '${U:-a{b}'}]\n",
			want: []string{"line 1: connections[0].username: unterminated ${ in value"},
		},
		{
			name: "syntax error",
			yaml: "connections: [\n",
			want: []string{"parse: yaml: line 1: did not find expected node content"},
		},
		{
			name: "multiple documents",
			yaml: "log: {level: info}\n---\nlog: {level: debug}\n",
			want: []string{"line 2: only one YAML document is allowed"},
		},
		{
			name: "duplicate key",
			yaml: "log: {level: info}\nlog: {level: debug}\n",
			want: []string{`decode: yaml: unmarshal errors:`, `  line 2: mapping key "log" already defined at line 1`},
		},
		{
			name: "validation errors carry lines, missing fields use the closest parent",
			yaml: "connections:\n  - name: main\n    addr: h:1\n  - addr: h:2\n  - name: main\n    addr: h:3\n",
			want: []string{
				"line 4: connections[1].name: is required",
				"line 5: connections[2].name: duplicate name, already used by connections[0]",
			},
		},
		{
			name: "an explicit key after a merge carries the line",
			yaml: "connections:\n  - &c\n    name: ok\n    addr: h:1\n  - <<: *c\n    name: Bad_Name\n",
			want: []string{"line 6: connections[1].name: must match ^[a-z0-9][a-z0-9._-]{0,62}$"},
		},
		{
			name: "merged values are validated at the merging mapping",
			yaml: "connections:\n  - name: a\n    addr: h:1\n    tls: &t {enabled: true}\n  - name: b\n    addr: h:2\n    tls:\n      <<: *t\n      cert_file: b.pem\n",
			want: []string{"line 8: connections[1].tls: cert_file and key_file must be set together"},
		},
		{
			name: "unknown field in a merged mapping",
			yaml: "connections:\n  - name: a\n    addr: h:1\n    tls: &t {enabled: true}\n  - name: b\n    addr: h:2\n    tls:\n      <<: [*t, {bogus: 1}]\n",
			want: []string{"line 8: connections[1].tls.bogus: unknown field"},
		},
		{
			name: "empty file",
			yaml: "",
			want: []string{"connections: at least one connection is required"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseErrors(tt.yaml, vars)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("errors (-want +got):\n%s", diff)
			}
			for _, leak := range []string{"hunter2", "TOPSECRETPW", "Bad_Name"} {
				if strings.Contains(strings.Join(got, "\n"), leak) {
					t.Errorf("errors echo the value %q", leak)
				}
			}
		})
	}
}

func TestParseAliasAcrossTypes(t *testing.T) {
	const head = "connections:\n  - name: a\n    addr: h:1\n"
	tests := []struct {
		name string
		yaml string
		vars map[string]string
		want []string
	}{
		{"string to int", head + "    username: &u ${PW}\n    db: *u\n", map[string]string{"PW": "hunter2"},
			[]string{"line 5: connections[0].db: an interpolated value aliased here must be a string"}},
		{"secret to bool", head + "    password: &u ${PW}\n    tls: {enabled: *u}\n", map[string]string{"PW": "true"},
			[]string{"line 5: connections[0].tls.enabled: an interpolated value aliased here must be a string"}},
		{"secret to duration", head + "    password: &u ${PW}\n    dial_timeout: *u\n", map[string]string{"PW": "hunter2"},
			[]string{"line 5: connections[0].dial_timeout: an interpolated value aliased here must be a string"}},
		{"int to string", head + "    db: &u ${N}\n    username: *u\n", map[string]string{"N": "010"},
			[]string{"line 5: connections[0].username: an interpolated value aliased here must be a decimal integer"}},
		{"bool to secret", head + "    tls: {enabled: &u '${N}'}\n    password: *u\n", map[string]string{"N": "true"},
			[]string{"line 5: connections[0].password: an interpolated value aliased here must be true or false"}},
		{"literal string to int", head + "    username: &u hunter2\n    db: *u\n", nil,
			[]string{"line 5: connections[0].db: must be a decimal integer"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseErrors(tt.yaml, tt.vars)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("errors (-want +got):\n%s", diff)
			}
		})
	}

	// The same type, and string-like types, may share an interpolated value.
	cfg := mustParse(t, head+"    username: &u ${U}\n  - name: b\n    addr: h:2\n    username: *u\n    password: *u\n", map[string]string{"U": "x"})
	if cfg.Connections[1].Username != "x" || cfg.Connections[1].Password.Reveal() != "x" {
		t.Errorf("shared alias: %+v", cfg.Connections[1])
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	if _, err := Load(missing, env(nil), Overrides{}); err == nil || !strings.Contains(err.Error(), "read config: open "+missing) {
		t.Errorf("Load(missing) = %v, want a read error naming the file", err)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("connections: []\nbogus: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(bad, env(nil), Overrides{})
	want := []string{"config " + bad + ": line 1: connections: at least one connection is required", "line 2: bogus: unknown field"}
	if diff := cmp.Diff(want, errLines(err)); diff != "" {
		t.Errorf("Load(bad) (-want +got):\n%s", diff)
	}
}

func TestLogLevels(t *testing.T) {
	for in, want := range map[string]string{"debug": "DEBUG", "info": "INFO", "warn": "WARN", "error": "ERROR"} {
		if got := (Log{Level: in}).SlogLevel().String(); got != want {
			t.Errorf("SlogLevel(%q) = %s, want %s", in, got, want)
		}
	}
	for _, in := range []string{"", "INFO", "warning", "loud"} {
		if _, err := ParseLogLevel(in); err != ErrLogLevel { //nolint:errorlint // sentinel returned directly
			t.Errorf("ParseLogLevel(%q) = %v, want ErrLogLevel", in, err)
		}
	}
}

func TestRefreshInterval(t *testing.T) {
	for in, want := range map[string]time.Duration{"off": 0, "2s": 2 * time.Second, "5s": 5 * time.Second, "10s": 10 * time.Second} {
		if got := (Refresh{Default: in}).Interval(); got != want {
			t.Errorf("Interval(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestNoInlineFields guards the resolver's assumptions: every config struct
// field is exported, named, not embedded and not inline, so its YAML key is
// exactly its tag.
func TestNoInlineFields(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var check func(reflect.Type)
	check = func(t2 reflect.Type) {
		switch t2.Kind() {
		case reflect.Slice, reflect.Pointer, reflect.Map:
			check(t2.Elem())
			return
		case reflect.Struct:
		default:
			return
		}
		if seen[t2] {
			return
		}
		seen[t2] = true
		for i := range t2.NumField() {
			f := t2.Field(i)
			tag := f.Tag.Get("yaml")
			name, opts, _ := strings.Cut(tag, ",")
			if f.Anonymous || !f.IsExported() || name == "" || name == "-" || opts != "" {
				t.Errorf("%s.%s: tag %q; config fields must be exported with a plain yaml name", t2, f.Name, tag)
			}
			if f.Type.Kind() == reflect.Pointer || f.Type.Kind() == reflect.Map || f.Type.Kind() == reflect.Interface {
				t.Errorf("%s.%s: %s fields are not supported by the resolver", t2, f.Name, f.Type.Kind())
			}
			check(f.Type)
		}
	}
	check(reflect.TypeFor[Config]())
	if len(seen) < 10 {
		t.Errorf("walked only %d struct types", len(seen))
	}
}
