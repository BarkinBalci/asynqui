package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
)

// options are the command-line settings. Precedence is flag > env > config
// file > default; the file layer is applied by config loading.
type options struct {
	configPath string
	addr       string
	logLevel   slog.Level
	help       bool
}

// usageError reports a command-line error that the flag package has already
// printed together with the usage text.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// parseFlags reads args (args[0] is the program name) and the ASYNQUI_*
// environment variables. An explicitly set flag beats its env var; an env var
// that is set but empty counts as unset.
func parseFlags(args []string, lookupEnv func(string) (string, bool), stderr io.Writer) (options, error) {
	if len(args) == 0 {
		return options{}, errors.New("args must start with the program name")
	}
	fs := flag.NewFlagSet("asynqui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "asynqui.yaml", "path to the configuration file (env ASYNQUI_CONFIG)")
	addr := fs.String("addr", ":8080", "listen address (env ASYNQUI_ADDR)")
	logLevel := fs.String("log-level", "info", "debug, info, warn or error (env ASYNQUI_LOG_LEVEL)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{help: true}, nil
		}
		return options{}, usageError{err}
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return options{}, usageError{fmt.Errorf("unexpected argument %q", fs.Arg(0))}
	}

	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	fromEnv := func(name string, dst *string, env string) {
		if set[name] {
			return
		}
		if v, ok := lookupEnv(env); ok && v != "" {
			*dst = v
		}
	}
	fromEnv("config", configPath, "ASYNQUI_CONFIG")
	fromEnv("addr", addr, "ASYNQUI_ADDR")
	fromEnv("log-level", logLevel, "ASYNQUI_LOG_LEVEL")

	level, err := parseLevel(*logLevel)
	if err != nil {
		return options{}, err
	}
	return options{configPath: *configPath, addr: *addr, logLevel: level}, nil
}

func parseLevel(s string) (slog.Level, error) {
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
	return 0, fmt.Errorf("log level %q: want debug, info, warn or error", s)
}
