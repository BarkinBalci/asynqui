package config

import (
	"fmt"
	"io"
	"log/slog"
	"strconv"

	"go.yaml.in/yaml/v3"
)

const redacted = "[redacted]"

// Secret is a string that is never printed, logged or marshaled. Use Reveal
// to get the value where it is actually needed.
type Secret string

// Reveal returns the secret value.
func (s Secret) Reveal() string { return string(s) }

// String returns "[redacted]", or "" when the secret is empty.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return redacted
}

// GoString keeps %#v from printing the value.
func (s Secret) GoString() string { return strconv.Quote(s.String()) }

// Format redacts the value for every fmt verb, including the ones (such as
// %d) for which fmt would otherwise print the underlying string.
func (s Secret) Format(f fmt.State, verb rune) {
	switch {
	case verb == 'v' && f.Flag('#'):
		_, _ = io.WriteString(f, s.GoString())
	case verb == 'q':
		_, _ = io.WriteString(f, strconv.Quote(s.String()))
	default:
		_, _ = io.WriteString(f, s.String())
	}
}

// LogValue keeps slog from logging the value.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalText keeps encoders (JSON, YAML) from writing the value.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// String returns the effective configuration as YAML, with secrets redacted.
func (c *Config) String() string {
	b, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Sprintf("config: %v", err)
	}
	return string(b)
}

// LogValue summarizes the configuration for slog, without secrets.
func (c *Config) LogValue() slog.Value {
	conns := make([]string, len(c.Connections))
	for i, cn := range c.Connections {
		conns[i] = cn.Name
	}
	return slog.GroupValue(
		slog.String("addr", c.Server.Addr),
		slog.String("base_path", c.Server.BasePath),
		slog.String("log_level", c.Log.Level),
		slog.String("refresh", c.Refresh.Default),
		slog.Bool("update_check", c.UpdateCheck),
		slog.Any("connections", conns),
		slog.String("auth_provider", string(c.Auth.Provider)),
		slog.String("authz_default", string(c.Authz.Default)),
		slog.Int("authz_bindings", len(c.Authz.Bindings)),
	)
}

// LogValue describes the connection for slog, without its password.
func (c Connection) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("name", c.Name),
		slog.String("addr", c.Addr),
		slog.Int("db", c.DB),
		slog.String("username", c.Username),
		slog.Bool("tls", c.TLS.Enabled),
	)
}
