package config

import (
	"os"
	"path/filepath"
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

// env returns a lookupEnv backed by m.
func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// errLines splits a joined error into its messages.
func errLines(err error) []string {
	if err == nil {
		return nil
	}
	return strings.Split(err.Error(), "\n")
}

func mustParse(t *testing.T, src string, vars map[string]string) *Config {
	t.Helper()
	cfg, err := parse([]byte(src), env(vars), Overrides{})
	if err != nil {
		t.Fatalf("parse:\n%v", err)
	}
	return cfg
}

func TestLoadExample(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	cfg, err := Load(path, env(nil), Overrides{})
	if err != nil {
		t.Fatalf("config.example.yaml must load without any environment variable:\n%v", err)
	}
	conn := Connection{Name: "local", Addr: "localhost:6379", DialTimeout: 5 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second}
	jobs := conn
	jobs.Name, jobs.DB = "local-jobs", 3
	want := &Config{
		Server:      Server{Addr: ":8080", TrustedOrigins: []string{}, ShutdownDelay: 5 * time.Second},
		Log:         Log{Level: "info"},
		Refresh:     Refresh{Default: "5s"},
		UpdateCheck: true,
		Connections: []Connection{conn, jobs},
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

func TestParseValues(t *testing.T) {
	const conns = "connections: [{name: a, addr: 'h:1'}]\n"
	vars := map[string]string{"HOST": "redis", "DB": "3", "TLS": "true", "D": "7s", "NO": "no", "NULL": "null", "EMPTY": "", "YAML": "x\nbogus: [1]"}
	tests := []struct {
		name string
		yaml string
		get  func(*Config) any
		want any
	}{
		{"authz.default is viewer with provider none", conns,
			func(c *Config) any { return c.Authz.Default }, RoleViewer},
		{"authz.default is none with other providers", conns + "auth: {provider: proxy, proxy: {trusted_proxies: [10.0.0.1]}}\n",
			func(c *Config) any { return c.Authz.Default }, RoleNone},
		{"base_path / is empty", conns + "server: {base_path: /}\n",
			func(c *Config) any { return c.Server.BasePath }, ""},
		{"openid is added to scopes once", conns + "auth: {oidc: {scopes: [profile]}}\n",
			func(c *Config) any { return c.Auth.OIDC.Scopes }, []string{"openid", "profile"}},
		{"explicit zero and false are kept", conns + "server: {shutdown_delay: 0s}\nupdate_check: false\n",
			func(c *Config) any { return []any{c.Server.ShutdownDelay, c.UpdateCheck} }, []any{time.Duration(0), false}},
		{"null keeps the default", conns + "server: {addr: ~}\nlog:\n",
			func(c *Config) any { return []string{c.Server.Addr, c.Log.Level} }, []string{":8080", "info"}},
		{"connection timeouts default independently", "connections: [{name: a, addr: 'h:1', read_timeout: 1s}]\n",
			func(c *Config) any {
				cn := c.Connections[0]
				return []time.Duration{cn.DialTimeout, cn.ReadTimeout, cn.WriteTimeout}
			}, []time.Duration{5 * time.Second, time.Second, 3 * time.Second}},
		{"interpolated values fill typed fields, quoted or not",
			"connections: [{name: a, addr: '${HOST}:1', db: '${DB}', tls: {enabled: \"${TLS}\"}, dial_timeout: \"${D}\"}]\nupdate_check: ${NO}\n",
			func(c *Config) any {
				cn := c.Connections[0]
				return []any{cn.Addr, cn.DB, cn.TLS.Enabled, cn.DialTimeout, c.UpdateCheck}
			}, []any{"redis:1", 3, true, 7 * time.Second, false}},
		{"strings are verbatim", "connections:\n  - name: a\n    addr: h:1\n    username: '${DB}'\n    password: ${NULL}\n",
			func(c *Config) any { return []string{c.Connections[0].Username, c.Connections[0].Password.Reveal()} }, []string{"3", "null"}},
		{"empty value keeps the default", "connections: [{name: a, addr: 'h:1', db: '${EMPTY}', dial_timeout: '${UNSET:-}'}]\n",
			func(c *Config) any { return []any{c.Connections[0].DB, c.Connections[0].DialTimeout} }, []any{0, 5 * time.Second}},
		{"values cannot inject YAML", "connections: [{name: a, addr: 'h:1', username: '${YAML}'}]\n",
			func(c *Config) any { return c.Connections[0].Username }, "x\nbogus: [1]"},
		{"anchors are interpolated once and aliased", "connections:\n  - {name: a, addr: &h '${HOST}:1'}\n  - {name: b, addr: *h}\n",
			func(c *Config) any { return []string{c.Connections[0].Addr, c.Connections[1].Addr} }, []string{"redis:1", "redis:1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mustParse(t, tt.yaml, vars)
			if diff := cmp.Diff(tt.want, tt.get(cfg)); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseOverrides(t *testing.T) {
	src := "server: {addr: ':1'}\nlog: {level: debug}\nconnections: [{name: a, addr: 'h:1'}]\n"
	for _, tt := range []struct {
		name string
		o    Overrides
		want []string
	}{
		{"file beats defaults", Overrides{}, []string{":1", "debug"}},
		{"overrides beat the file", Overrides{Addr: ":2", LogLevel: "warn"}, []string{":2", "warn"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parse([]byte(src), env(nil), tt.o)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, []string{cfg.Server.Addr, cfg.Log.Level}); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
	if _, err := parse([]byte(src), env(nil), Overrides{Addr: "nope"}); err == nil || err.Error() != "server.addr: must be host:port" {
		t.Errorf("invalid override: %v", err)
	}
}

func TestParseErrors(t *testing.T) {
	const conns = "connections: [{name: a, addr: 'h:1'}]\n"
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"unknown field", conns + "server:\n  adr: ':1'\n", "line 3: field adr not found in type config.Server"},
		{"unknown connection field", "connections:\n  - name: a\n    addr: h:1\n    passwd: x\n", "line 4: field passwd not found in type config.connection"},
		{"unknown field in a merged mapping", "connections:\n  - name: a\n    addr: h:1\n    tls:\n      <<: {bogus: 1}\n", "line 5: field bogus not found in type config.TLS"},
		{"keys are not interpolated", conns + "server:\n  ${X}: ':1'\n", "line 3: field ${X} not found in type config.Server"},
		{"wrong type", conns + "refresh: [5s]\n", "line 2: cannot unmarshal !!seq into config.Refresh"},
		{"missing variable", "connections:\n  - name: a\n    addr: ${REDIS_ADDR}\n", "line 3: environment variable REDIS_ADDR is not set"},
		{"invalid reference", conns + "log: {level: '${lo-g}'}\n", `line 2: invalid variable name "lo-g" in ${...}`},
		{"unterminated reference", conns + "log: {level: '${A'}\n", "line 2: unterminated ${"},
		{"null list entry", conns + "server: {trusted_origins: [null]}\n", "line 2: list entry must not be empty"},
		{"empty interpolated list entry", conns + "auth: {oidc: {scopes: ['${E:-}']}}\n", "line 2: list entry must not be empty"},
		{"syntax error", "connections: [\n", "yaml: line 1: did not find expected node content"},
		{"duplicate key", conns + "log: {level: info}\nlog: {level: debug}\n", `mapping key "log" already defined at line 2`},
		{"multiple documents", conns + "---\nlog: {level: debug}\n", "only one YAML document is allowed"},
		{"validation after decoding", conns + "log: {level: loud}\n", "log.level: must be one of debug, info, warn, error"},
		{"empty file", "", "connections: at least one connection is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse([]byte(tt.yaml), env(nil), Overrides{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	if _, err := Load(missing, env(nil), Overrides{}); err == nil || !strings.Contains(err.Error(), "read config: open "+missing) {
		t.Errorf("Load(missing) = %v, want a read error naming the file", err)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("connections: []\nlog: {level: loud}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(bad, env(nil), Overrides{})
	want := []string{"config " + bad + ": log.level: must be one of debug, info, warn, error", "connections: at least one connection is required"}
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
	for _, in := range []string{"", "INFO", "warning"} {
		if _, err := ParseLogLevel(in); err != ErrLogLevel { //nolint:errorlint // sentinel returned directly
			t.Errorf("ParseLogLevel(%q) = %v, want ErrLogLevel", in, err)
		}
	}
}

func TestRefreshInterval(t *testing.T) {
	for in, want := range map[string]time.Duration{"off": 0, "2s": 2 * time.Second, "10s": 10 * time.Second} {
		if got := (Refresh{Default: in}).Interval(); got != want {
			t.Errorf("Interval(%q) = %v, want %v", in, got, want)
		}
	}
}
