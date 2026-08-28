package main

import (
	"fmt"
	"io"
	"log/slog"
)

// newCLILogger builds the diagnostics logger for --log-level. "off" discards.
// This is deliberately separate from --verbose, which controls user-facing
// detail on stdout and must not change output.
func newCLILogger(level string, w io.Writer) (*slog.Logger, error) {
	var lvl slog.Level
	switch level {
	case "off":
		return slog.New(slog.DiscardHandler), nil
	case "error":
		lvl = slog.LevelError
	case "warn":
		lvl = slog.LevelWarn
	case "info":
		lvl = slog.LevelInfo
	case "debug":
		lvl = slog.LevelDebug
	default:
		return nil, fmt.Errorf("invalid --log-level %q: expected off, error, warn, info, or debug", level)
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})), nil
}

// logLevelFlag is a pflag.Value wrapping --log-level's destination string.
// Cobra checks --help and (when Version is set) --version at the top of a
// command's execute(), immediately after ParseFlags but before ever walking
// the parent chain for PersistentPreRunE - so `lmm --log-level loud <sub>
// --help` (or a bare `--log-level loud --version`) would reach neither
// rootCmd's PersistentPreRunE nor any RunE, silently letting the bad level
// through. Validating in Set makes ParseFlags itself fail on the bad value,
// which cobra checks first, before either short-circuit.
type logLevelFlag struct{ dest *string }

func (f logLevelFlag) String() string {
	if f.dest == nil {
		return ""
	}
	return *f.dest
}

func (f logLevelFlag) Set(v string) error {
	if _, err := newCLILogger(v, io.Discard); err != nil {
		return err
	}
	*f.dest = v
	return nil
}

func (f logLevelFlag) Type() string { return "string" }
