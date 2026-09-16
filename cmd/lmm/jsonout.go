package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// emitJSON writes v to stdout as exactly one JSON document (2-space indent,
// deterministic map/key ordering) followed by exactly one trailing newline.
// Every --json document this process emits goes through this one function,
// so the framing a caller piping stdout to a parser relies on - one
// document, one newline - stays identical everywhere (Ruling 3). The
// framing itself is core.EncodeJSON's contract; this is stdout's wrapper
// around it.
func emitJSON(v any) error {
	return core.EncodeJSON(os.Stdout, v)
}

// jsonErrorEnvelope is the document reportError emits under --json. Details
// is declared after Error, not because of alphabetical sort (Deterministic
// governs map ordering, not struct field order), but so "error" is always
// the first key regardless of whether Details is present; omitempty drops
// the key entirely when errorDetails found nothing.
type jsonErrorEnvelope struct {
	Error   string `json:"error"`
	Details any    `json:"details,omitempty"`
}

// errorDetails returns the data err carries for the --json error envelope's
// "details" field, or nil for an error that carries none - e.g.
// core.ErrStalePlan, a plain sentinel with no data of its own.
//
// Extension point: a typed error that DOES carry data (today
// *core.ConflictError, with its own []core.Conflict) needs no change here -
// it just implements the unnamed `Details() any` interface below and
// errors.As picks it up.
func errorDetails(err error) any {
	switch {
	case errors.Is(err, core.ErrStalePlan):
		return nil
	default:
		var withDetails interface{ Details() any }
		if errors.As(err, &withDetails) {
			return withDetails.Details()
		}
		return nil
	}
}

// quietSink is what a mutating command passes core in place of its console
// event closure: sink for an ordinary run, nil under --json. Ruling 15
// suppresses events under --json - the run emits exactly one document on
// stdout and nothing else - and a nil sink is how core is told there is
// nothing to report to, so the closure is never even installed rather than
// installed and then ignored line by line.
//
// It also prints a download-time warning (core.DownloadWarning, #425)
// itself, on stderr, and does not hand it on. Any command can download - a
// deploy re-fetching a missing mod, a switch or an apply installing one -
// and each closure switches on its own flow's phases, so a warning that
// "lmm told you how to fix this" printed only for `lmm install`. Every
// mutating command already passes its closure through here, which makes
// this the one place that covers them all.
func quietSink(sink core.EventSink) core.EventSink {
	if jsonOutput {
		return nil
	}
	return func(e core.Event) {
		if w, ok := e.(core.WarningEvent); ok && w.Phase == core.DownloadWarning {
			printDownloadWarning(w)
			return
		}
		if sink != nil {
			sink(e)
		}
	}
}

// printDownloadWarning renders one download-time warning, naming the mod it
// is about when the flow's scope does - a batch or a deploy fetches several.
func printDownloadWarning(w core.WarningEvent) {
	if w.ModName != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s: %s\n", w.ModName, w.Message)
		return
	}
	fmt.Fprintf(os.Stderr, "Warning: %s\n", w.Message)
}
