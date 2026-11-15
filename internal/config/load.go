package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"
)

// Overrides are settings from flags or ASYNQUI_* variables. Non-empty fields
// replace the file's value.
type Overrides struct {
	Addr     string
	LogLevel string
}

// Load reads, interpolates, decodes and validates the config file at path. A
// YAML or interpolation error is returned on its own; otherwise every
// validation problem is joined with errors.Join.
func Load(path string, lookupEnv func(string) (string, bool), overrides Overrides) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is chosen by the operator
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := parse(data, lookupEnv, overrides)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// parse turns YAML into a validated Config.
func parse(data []byte, lookupEnv func(string) (string, bool), o Overrides) (*Config, error) {
	cfg := defaults()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&document{cfg: cfg, lookupEnv: lookupEnv}); err != nil && !errors.Is(err, io.EOF) {
		return nil, err // io.EOF: an empty file
	}
	switch err := dec.Decode(new(yaml.Node)); {
	case err == nil:
		return nil, errors.New("only one YAML document is allowed")
	case !errors.Is(err, io.EOF):
		return nil, err
	}
	if o.Addr != "" {
		cfg.Server.Addr = o.Addr
	}
	if o.LogLevel != "" {
		cfg.Log.Level = o.LogLevel
	}
	cfg.normalize()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// document decodes a whole config file into cfg. Its old-style UnmarshalYAML
// keeps the Decoder's KnownFields, which Node.Decode would drop: the first
// call interpolates the decoder's own tree, the second decodes the result.
type document struct {
	cfg       *Config
	lookupEnv func(string) (string, bool)
}

func (d *document) UnmarshalYAML(unmarshal func(any) error) error {
	if err := unmarshal(&interpolator{d.lookupEnv}); err != nil {
		return err
	}
	return unmarshal(d.cfg)
}

// interpolator interpolates the tree it is decoded from, in place.
type interpolator struct{ lookupEnv func(string) (string, bool) }

func (i *interpolator) UnmarshalYAML(n *yaml.Node) error { return interpolate(n, i.lookupEnv) }

// normalize fills defaults that depend on other fields and canonicalises
// equivalent spellings.
func (c *Config) normalize() {
	if c.Authz.Default == "" {
		c.Authz.Default = RoleNone
		if c.Auth.Provider == ProviderNone {
			c.Authz.Default = RoleViewer
		}
	}
	if c.Server.BasePath == "/" {
		c.Server.BasePath = ""
	}
	if !slices.Contains(c.Auth.OIDC.Scopes, "openid") {
		c.Auth.OIDC.Scopes = append([]string{"openid"}, c.Auth.OIDC.Scopes...)
	}
}

// ErrLogLevel reports a log level other than debug, info, warn or error.
var ErrLogLevel = errors.New("log level must be debug, info, warn or error")

// ParseLogLevel parses debug, info, warn or error.
func ParseLogLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, ErrLogLevel
}

// SlogLevel returns the configured level; Load has validated it.
func (l Log) SlogLevel() slog.Level {
	level, _ := ParseLogLevel(l.Level)
	return level
}

// Interval returns the default refresh interval, or 0 for "off".
func (r Refresh) Interval() time.Duration {
	d, err := time.ParseDuration(r.Default)
	if err != nil {
		return 0
	}
	return d
}
