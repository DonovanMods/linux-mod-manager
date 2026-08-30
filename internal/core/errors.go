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

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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

// ErrConfirmationRequired is returned by a frontend-facing entry point that
// would have to prompt but cannot - the CLI's --json mode, which never reads
// stdin (Ruling 2). The decision must come from a flag instead.
var ErrConfirmationRequired = errors.New("confirmation required: pass --yes (or --force where documented) in non-interactive mode")

// ErrInteractiveOnly marks a command that has no non-interactive form yet
// (Ruling 2: `game add`, `auth login`) and therefore rejects --json outright
// rather than half-running.
var ErrInteractiveOnly = errors.New("this command is interactive-only and does not support --json")
