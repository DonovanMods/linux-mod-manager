// Package core: this file holds core's typed errors - the ones a frontend
// branches on rather than merely prints. Spec §4 ("no callbacks into the
// frontend from Apply", v2 Phase 3 Ruling 1): anything an Apply can only
// discover mid-flight is reported as a typed error the caller inspects and
// answers by re-running Apply with the matching Options field, never by a
// callback core reaches back through.
package core

import (
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ConflictError is returned by ApplyInstall (STRICT path) and ImportArchive
// when deploying the mod would overwrite files another installed mod owns
// and the caller has not accepted that up front
// (InstallOptions/ImportArchiveOptions.AcceptConflicts, which Force implies).
//
// It is returned BEFORE any deploy, DB write or profile write, so a caller
// that declines is left in the same state a declined prompt always left:
// the mod is in the cache (a cache fill is not a mutation of managed state -
// Ruling 1) and nothing else has changed. A caller that accepts re-runs the
// same Apply with AcceptConflicts set.
//
// Conflicts is the freshly-computed, non-empty list core would have
// overwritten - the data a frontend needs to render its own prompt.
type ConflictError struct {
	Conflicts []Conflict
}

// Error reports domain.ErrFileConflict's text plus how many files the
// install would overwrite.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: %d file(s) would be overwritten", domain.ErrFileConflict, len(e.Conflicts))
}

// Unwrap makes errors.Is(err, domain.ErrFileConflict) true, so a caller that
// only cares that a conflict happened needs no type assertion.
func (e *ConflictError) Unwrap() error { return domain.ErrFileConflict }

// Details returns the payload the CLI's --json error envelope attaches under
// "details" (Ruling 3: {"error": "...", "details": {"conflicts": [...]}}).
// The returned type is unexported deliberately - it is a wire shape for the
// envelope, not a core contract type of its own.
func (e *ConflictError) Details() any {
	return conflictErrorDetails{Conflicts: e.Conflicts}
}

type conflictErrorDetails struct {
	Conflicts []Conflict `json:"conflicts"`
}

// ProfileWarningsError carries the diagnostics `ApplyProfileSwitch`/
// `ApplyProfileApply`/`ApplyProfileSync` accumulated before a fatal error,
// for a frontend's error envelope's "details" field
// ({"error": "...", "details": {"warnings": [...]}}). Plain text prints
// them directly, but an envelope (--json, or a future API response) only
// carries data for a typed error - so without this wrapper the #294
// warnings would reach neither the plain-text stream nor the envelope on
// this path, leaving the DB-vs-profile divergence #294 exists to expose
// silent again.
//
// Only construct this when there is at least one warning, so a fatal run
// with nothing to report still produces the bare {"error": ...} envelope.
// Follows the ConflictError / GameDetectPartialError convention: Unwrap
// exposes Err for errors.Is/As, Details() any is the unnamed interface a
// frontend's envelope writer picks up automatically.
type ProfileWarningsError struct {
	Err      error
	Warnings []string
}

// Error returns the wrapped fatal failure's own message, so plain text and
// the envelope's "error" field are unchanged by the wrapping.
func (e *ProfileWarningsError) Error() string { return e.Err.Error() }

// Unwrap exposes the wrapped fatal error for errors.Is/errors.As.
func (e *ProfileWarningsError) Unwrap() error { return e.Err }

// Details returns the accumulated warnings for a frontend's error
// envelope's "details" field.
func (e *ProfileWarningsError) Details() any {
	return profileWarningsDetails{Warnings: e.Warnings}
}

// profileWarningsDetails is ProfileWarningsError's wire shape: a named type
// rather than a map so the "warnings" key is part of the JSON contract and
// matches the same key on the ProfileApplyResult/ProfileSyncResult/
// SwitchResult documents a SUCCESSFUL run emits.
type profileWarningsDetails struct {
	Warnings []string `json:"warnings"`
}

// GameDetectPartialError reports a `game detect` run that failed partway
// through ApplyGameDetect: Result still names exactly the games that were
// fully persisted (games.yaml write + default profile) before Err stopped
// it, so a frontend's error envelope can say what was saved instead of only
// that the run failed - mirroring the plain-text path's own partial-success
// contract (the CLI's "Added:" loop prints every game Result.Profiles names,
// even on failure). Follows the ConflictError / ProfileWarningsError
// convention: Unwrap exposes Err for errors.Is/As, Details() any is the
// unnamed interface a frontend's envelope writer picks up automatically.
type GameDetectPartialError struct {
	Err    error
	Result *GameDetectResult
}

// Error returns the wrapped ApplyGameDetect failure's own message.
func (e *GameDetectPartialError) Error() string { return e.Err.Error() }

// Unwrap exposes the wrapped ApplyGameDetect error for errors.Is/errors.As.
func (e *GameDetectPartialError) Unwrap() error { return e.Err }

// Details returns the partial GameDetectResult - what was saved before the
// failure - for a frontend's error envelope's "details" field.
func (e *GameDetectPartialError) Details() any { return e.Result }

// ErrConfirmationRequired is returned by a frontend-facing entry point that
// would have to prompt but cannot - the CLI's --json mode, which never reads
// stdin (Ruling 2). The decision must come from a flag instead.
var ErrConfirmationRequired = errors.New("confirmation required: pass --yes (or --force where documented) in non-interactive mode")

// ErrInteractiveOnly marks a command that has no non-interactive form yet
// (Ruling 2: `game add`, `auth login`) and therefore rejects --json outright
// rather than half-running.
var ErrInteractiveOnly = errors.New("this command is interactive-only and does not support --json")
