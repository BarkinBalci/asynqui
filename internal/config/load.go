package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Overrides are settings from flags or ASYNQUI_* variables. Non-empty fields
// replace the file's value: flag > env > file > default.
type Overrides struct {
	Addr     string // server.addr
	LogLevel string // log.level
}

// apply sets the overridden values and returns their paths.
func (o Overrides) apply(c *Config) []string {
	var paths []string
	if o.Addr != "" {
		c.Server.Addr = o.Addr
		paths = append(paths, "server.addr")
	}
	if o.LogLevel != "" {
		c.Log.Level = o.LogLevel
		paths = append(paths, "log.level")
	}
	return paths
}

// Load reads, interpolates, decodes and validates the config file at path.
// lookupEnv resolves ${NAME} references; pass os.LookupEnv in production.
// overrides carries the flags and ASYNQUI_* variables that were set.
//
// Every interpolation, schema and validation problem is reported at once,
// joined with errors.Join and ordered by line, each as
// "line N: location: problem". Error messages never contain a configured
// value, so secrets cannot leak through them.
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
func parse(data []byte, lookupEnv func(string) (string, bool), overrides Overrides) (*Config, error) {
	root, err := parseYAML(data)
	if err != nil {
		return nil, err
	}
	r := newResolver(lookupEnv)
	r.walk(root, reflect.TypeFor[Config](), "")

	cfg := defaults()
	if err := root.Decode(cfg); err != nil {
		var terr *yaml.TypeError
		if len(r.errs) == 0 || !errors.As(err, &terr) {
			return nil, joinErrors(r.errs, fmt.Errorf("decode: %w", err))
		}
		// The resolver already reported these values, without quoting them.
	}
	overridden := overrides.apply(cfg)
	cfg.normalize()

	errs := r.errs
	for _, fe := range cfg.validate() {
		if coveredBy(fe.path, r.errs) {
			continue
		}
		if !slices.Contains(overridden, fe.path) { // else the value is not from the file
			fe.line = r.lineFor(fe.path)
		}
		errs = append(errs, fe)
	}
	if len(errs) > 0 {
		return nil, joinErrors(errs)
	}
	return cfg, nil
}

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

// coveredBy reports whether a problem at, above or below path was already
// reported. Validating around a value that failed to resolve only repeats
// it (a list entry that failed to decode is missing from the list, say).
func coveredBy(path string, errs []*fieldError) bool {
	for _, e := range errs {
		if within(path, e.path) || within(e.path, path) {
			return true
		}
	}
	return false
}

// within reports whether path is base or a location inside it.
func within(path, base string) bool {
	return path == base || base == "" ||
		strings.HasPrefix(path, base+".") || strings.HasPrefix(path, base+"[")
}

// joinErrors orders field errors by line (those without a line last) and
// joins them, followed by any extra errors.
func joinErrors(ferrs []*fieldError, extra ...error) error {
	sorted := slices.Clone(ferrs)
	slices.SortStableFunc(sorted, func(a, b *fieldError) int {
		switch {
		case a.line == b.line:
			return 0
		case a.line == 0:
			return 1
		case b.line == 0:
			return -1
		}
		return a.line - b.line
	})
	errs := make([]error, 0, len(sorted)+len(extra))
	for _, e := range sorted {
		errs = append(errs, e)
	}
	return errors.Join(append(errs, extra...)...)
}

// parseYAML parses exactly one YAML document. An empty file is an empty
// mapping.
func parseYAML(data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
		}
		return nil, fmt.Errorf("parse: %w", err)
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, fmt.Errorf("line %d: only one YAML document is allowed", extra.Line)
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &root, nil
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
