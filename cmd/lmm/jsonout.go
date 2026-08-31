package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// emitJSON writes v to stdout as exactly one JSON document (2-space indent,
// deterministic map/key ordering) followed by exactly one trailing newline.
// Every --json document this process emits goes through this one function,
// so the framing a caller piping stdout to a parser relies on - one
// document, one newline - stays identical everywhere (Ruling 3).
func emitJSON(v any) error {
	b, err := json.Marshal(v, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = os.Stdout.Write(b)
	return err
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
func quietSink(sink core.EventSink) core.EventSink {
	if jsonOutput {
		return nil
	}
	return sink
}
