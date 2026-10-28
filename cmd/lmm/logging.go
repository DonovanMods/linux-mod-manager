package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

// logLevelFlagName is the flag name registered in root.go's init() and
// matched against in logLevelFlagErrorFunc.
const logLevelFlagName = "log-level"

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

// logLevelFlagErrorFunc is rootCmd's FlagErrorFunc. pflag's FlagSet.Set
// wraps every flag Value's Set error in *pflag.InvalidValueError, whose
// Error() always prepends `invalid argument %q for %q flag: ` ahead of the
// cause - for --log-level, that wrapper would make ParseFlags fail with a
// different string than every other path that surfaces the same
// validation (e.g. initServiceWith calling newCLILogger directly).
// Unwrapping back to the cause here makes --log-level's error text
// identical everywhere it's rejected. Every other flag's parse error is
// returned unchanged, so it keeps cobra's default wrapped text.
//
// pflag's FlagSet.Parse also stops at the first Set error, so a --json
// flag placed AFTER a bad --log-level on the command line never reaches
// ParseFlags and jsonOutput is left false, even though the user asked for
// JSON. That would make reportError's stdout-envelope-vs-stderr choice
// depend on argument order for this one flag, breaking the project's
// one-document-on-stdout invariant for scripts that pipe stdout to a
// parser. Falling back to a scan of rawArgs (the full argument list, not
// just what ParseFlags reached) restores order independence.
func logLevelFlagErrorFunc(_ *cobra.Command, err error) error {
	var invalidErr *pflag.InvalidValueError
	if errors.As(err, &invalidErr) && invalidErr.GetFlag() != nil && invalidErr.GetFlag().Name == logLevelFlagName {
		if !jsonOutput {
			for _, a := range rawArgs {
				if a == "--json" {
					jsonOutput = true
					break
				}
			}
		}
		return invalidErr.Unwrap()
	}
	return err
}
