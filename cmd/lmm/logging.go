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
