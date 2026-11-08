package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	pwValue     = "redis-pw-VALUE"
	hashValue   = "$2b$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW"
	clientValue = "client-secret-VALUE"
)

// secretConfig returns a valid config holding every kind of secret.
func secretConfig() *Config {
	c := validConfig()
	c.Auth.Provider = ProviderOIDC
	c.Connections[0].Password = pwValue
	c.Auth.Basic.Users[0].PasswordHash = hashValue
	c.Auth.OIDC.ClientSecret = clientValue
	c.Auth.Session.PreviousKeys = []KeyPair{{HashKey: testHashKey, BlockKey: testBlockKey}}
	return c
}

func assertNoSecrets(t *testing.T, what, out string) {
	t.Helper()
	for _, s := range []string{pwValue, hashValue, clientValue, testHashKey, testBlockKey} {
		if strings.Contains(out, s) {
			t.Errorf("%s leaks secret %q:\n%s", what, s, out)
		}
	}
}

func TestSecretFormatting(t *testing.T) {
	s := Secret("hunter2")
	for _, verb := range []string{"%v", "%s", "%q", "%x", "%X", "%d", "%+v", "%#v", "%10s"} {
		out := fmt.Sprintf(verb, s)
		if strings.Contains(out, "hunter2") || strings.Contains(out, fmt.Sprintf("%x", "hunter2")) {
			t.Errorf("Sprintf(%s) = %q leaks the secret", verb, out)
		}
	}
	if got := fmt.Sprint(s); got != "[redacted]" {
		t.Errorf("Sprint = %q, want [redacted]", got)
	}
	if got := fmt.Sprint(Secret("")); got != "" {
		t.Errorf("Sprint(empty) = %q, want empty", got)
	}
	if got := s.Reveal(); got != "hunter2" {
		t.Errorf("Reveal = %q", got)
	}
}

func TestConfigRedaction(t *testing.T) {
	c := secretConfig()

	t.Run("String", func(t *testing.T) {
		out := c.String()
		assertNoSecrets(t, "String", out)
		if !strings.Contains(out, "password: '[redacted]'") || !strings.Contains(out, "name: main") {
			t.Errorf("String does not look like the redacted YAML config:\n%s", out)
		}
	})
	t.Run("fmt", func(t *testing.T) {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			assertNoSecrets(t, verb+" of *Config", fmt.Sprintf(verb, c))
			assertNoSecrets(t, verb+" of Config", fmt.Sprintf(verb, *c))
			assertNoSecrets(t, verb+" of Connection", fmt.Sprintf(verb, c.Connections[0]))
		}
	})
	t.Run("json", func(t *testing.T) {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, "json", string(b))
	})
	t.Run("yaml", func(t *testing.T) {
		b, err := yaml.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, "yaml", string(b))
	})
	t.Run("slog", func(t *testing.T) {
		for _, newHandler := range []func(*bytes.Buffer) slog.Handler{
			func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
			func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		} {
			var buf bytes.Buffer
			logger := slog.New(newHandler(&buf))
			logger.Info("cfg",
				slog.Any("config", c),
				slog.Any("conn", c.Connections[0]),
				slog.Any("session", c.Auth.Session),
				slog.Any("oidc", c.Auth.OIDC),
				slog.Any("secret", c.Connections[0].Password),
			)
			out := buf.String()
			assertNoSecrets(t, "slog", out)
			if !strings.Contains(out, "main-jobs") || !strings.Contains(out, "redis:6379") {
				t.Errorf("slog output lacks the non-secret fields:\n%s", out)
			}
		}
	})
}

func TestErrorsNeverContainSecrets(t *testing.T) {
	// Secrets that fail validation, or land in fields of the wrong type,
	// are reported by location only.
	vars := env(map[string]string{"PW": pwValue, "SECRET": clientValue, "HASH": "not-a-bcrypt-" + pwValue})
	src := `connections:
  - name: a
    addr: h:1
    db: ${PW}
    tls: {enabled: "${SECRET}"}
auth:
  provider: basic
  session:
    hash_key: ${SECRET}
    block_key: ${PW}
    ttl: ${PW}
  basic:
    users:
      - {username: u, password_hash: "${HASH}"}
`
	_, err := parse([]byte(src), vars, Overrides{})
	if err == nil {
		t.Fatal("want errors")
	}
	assertNoSecrets(t, "error", err.Error())

	src = strings.Replace(src, "db: ${PW}", "db: 0", 1)
	src = strings.Replace(src, `"${SECRET}"}`, "true}", 1)
	src = strings.Replace(src, "ttl: ${PW}", "ttl: 1h", 1)
	_, err = parse([]byte(src), vars, Overrides{})
	want := []string{
		"line 9: auth.session.hash_key: must be hex-encoded (32 or 64 bytes)",
		"line 10: auth.session.block_key: must be hex-encoded (32 bytes)",
		"line 14: auth.basic.users[0].password_hash: must be a bcrypt hash such as htpasswd -nbBC 12 prints",
	}
	if got := errLines(err); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("errors = %q, want %q", got, want)
	}
	assertNoSecrets(t, "error", err.Error())
}

// TestSecretInContainers covers secrets reached through slices, maps (as
// keys and values), pointers and nested structs.
func TestSecretInContainers(t *testing.T) {
	s := Secret("hunter2")
	type wrap struct {
		S []Secret
		M map[string]Secret
		K map[Secret]int
		P *Secret
	}
	w := wrap{S: []Secret{s}, M: map[string]Secret{"k": s}, K: map[Secret]int{s: 1}, P: &s}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		if out := fmt.Sprintf(verb, w); strings.Contains(out, "hunter2") {
			t.Errorf("Sprintf(%s) leaks: %s", verb, out)
		}
		if out := fmt.Sprintf(verb, *w.P); strings.Contains(out, "hunter2") {
			t.Errorf("Sprintf(%s, *P) leaks: %s", verb, out)
		}
		if out := fmt.Sprintf(verb, secretConfig().Auth); strings.Contains(out, clientValue) || strings.Contains(out, testHashKey) {
			t.Errorf("Sprintf(%s, Auth) leaks: %s", verb, out)
		}
	}
	// encoding/json writes string-kind map keys verbatim, bypassing
	// MarshalText, so a Secret must never be a map key; no config type uses
	// one.
	w.K = nil
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "hunter2") {
		t.Errorf("json leaks: %s", b)
	}
}
