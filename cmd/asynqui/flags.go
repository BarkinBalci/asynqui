package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/BarkinBalci/asynqui/internal/config"
)

// options are the command-line settings. Precedence is flag > env > config
// file > default; overrides holds only what a flag or env var set, and
// config.Load applies it over the file.
type options struct {
	configPath string
	overrides  config.Overrides
	help       bool
	version    bool // print the version and exit
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
	addr := fs.String("addr", config.DefaultAddr, "listen address (env ASYNQUI_ADDR; overrides server.addr)")
	logLevel := fs.String("log-level", config.DefaultLogLevel, "debug, info, warn or error (env ASYNQUI_LOG_LEVEL; overrides log.level)")
	printVersion := fs.Bool("version", false, "print the version and exit")
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
	if *printVersion {
		return options{version: true}, nil
	}

	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	// explicit returns the flag's value if it was set, else its env var if
	// set and non-empty, else "".
	explicit := func(name, value, env string) string {
		if set[name] {
			return value
		}
		if v, ok := lookupEnv(env); ok && v != "" {
			return v
		}
		return ""
	}

	opts := options{
		configPath: explicit("config", *configPath, "ASYNQUI_CONFIG"),
		overrides: config.Overrides{
			Addr:     explicit("addr", *addr, "ASYNQUI_ADDR"),
			LogLevel: explicit("log-level", *logLevel, "ASYNQUI_LOG_LEVEL"),
		},
	}
	if opts.configPath == "" {
		opts.configPath = *configPath
	}
	if l := opts.overrides.LogLevel; l != "" {
		if _, err := config.ParseLogLevel(l); err != nil {
			return options{}, fmt.Errorf("-log-level %q: %w", l, err)
		}
	}
	return opts, nil
}
