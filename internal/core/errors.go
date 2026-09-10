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
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
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

	// CleanupWarnings names anything the refusal could not undo. Today
	// that is ImportArchive's own promise: the refusal removes the cache
	// entry it created, and a removal that FAILS used to disappear into a
	// Debug log, leaving an orphaned entry with no user-visible signal
	// (#310). Empty on every ordinary refusal - including every install
	// refusal, which has nothing to undo - so an `omitempty` member on the
	// envelope, not a field a frontend has to render.
	CleanupWarnings []string
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
	return conflictErrorDetails{Conflicts: e.Conflicts, CleanupWarnings: e.CleanupWarnings}
}

type conflictErrorDetails struct {
	Conflicts []Conflict `json:"conflicts"`
	// Additive and omitted when empty (#310), so an ordinary refusal's
	// envelope is byte-identical to the one it emitted before.
	CleanupWarnings []string `json:"cleanup_warnings,omitempty"`
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

// ErrProfileExists is returned by ProfileManager.Create and ProfileManager.
// Rename when the target profile name is already taken - either a profile
// file already answers to it, or (Rename only) DB rows still name it after
// a Delete that only ever removed the file (ProfileManager.Delete's own doc
// comment; see refuseOccupiedName). It is detected INSIDE the gated
// CreateProfile/RenameProfile seams, before either writes anything, so a
// frontend's 409 never depends on an untyped error's wording (#332 M6).
var ErrProfileExists = errors.New("profile already exists")

// ErrConfirmationRequired is returned by a frontend-facing entry point that
// would have to prompt but cannot - the CLI's --json mode, which never reads
// stdin (Ruling 2). The decision must come from a flag instead.
var ErrConfirmationRequired = errors.New("confirmation required: pass --yes (or --force where documented) in non-interactive mode")

// ErrInteractiveOnly marks a value a frontend could only obtain by
// prompting, in a mode that forbids reading stdin (the CLI's --json,
// Ruling 2).
//
// Before #307 it meant something coarser - `game add` and `auth login` had
// no flag-driven form at all and rejected --json outright, before doing
// anything. Both now take flags for every prompt they had, so the refusal
// narrowed from "this command" to "this value": it is returned only when a
// specific value was supplied by neither a flag nor an interactive prompt,
// wrapped with the flag that would have answered it.
var ErrInteractiveOnly = errors.New("this value can only be supplied interactively; pass the matching flag instead")

// TokenKeyError reports that lmm could not use the key its stored
// credentials are encrypted under (#79) - the frontend-facing translation
// of *db.KeyError, so no frontend has to name the storage layer to branch
// on it.
//
// Reason is db.KeyErrorReason's wire name:
//
//   - "missing"       - the key file is gone but encrypted rows remain.
//     The credentials are unrecoverable; the remedy is to log in again.
//   - "permissions"   - the file is readable beyond its owner; chmod 600.
//   - "malformed"     - the file is not a 32-byte key.
//   - "unreadable"    - it could not be read or created at all.
//   - "undecryptable" - the KEY was fine but one row did not authenticate
//     under it. Sources names exactly that row, so a frontend reports one
//     broken credential rather than a global outage.
//
// It follows the same convention as this file's other typed errors: Unwrap
// exposes the cause for errors.Is/As, and Details() any puts the whole
// thing in the --json error envelope's "details" (Ruling 3).
type TokenKeyError struct {
	KeyPath string   `json:"key_path"`
	Reason  string   `json:"reason"`
	Sources []string `json:"sources,omitempty"`
	Err     error    `json:"-"`
}

// Error returns the storage layer's own message, which already names the
// file and the fix.
//
// Err is set by asTokenKeyError on every production path, but the type is
// exported and a hand-built value must not panic here (review, Minor 7);
// one without a cause falls back to what its own fields say.
func (e *TokenKeyError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("the token-encryption key %s could not be used (%s)", e.KeyPath, e.Reason)
	}
	return e.Err.Error()
}

// Unwrap exposes the underlying *db.KeyError.
func (e *TokenKeyError) Unwrap() error { return e.Err }

// Details implements the --json error envelope's extension point.
func (e *TokenKeyError) Details() any { return e }

// asTokenKeyError translates a *db.KeyError anywhere in err's chain into
// the frontend-facing TokenKeyError, and passes everything else (including
// nil) through untouched. Applied at every Service method that touches a
// credential, so the translation cannot be forgotten at one of them.
func asTokenKeyError(err error) error {
	var keyErr *db.KeyError
	if !errors.As(err, &keyErr) {
		return err
	}
	out := &TokenKeyError{KeyPath: keyErr.Path, Reason: string(keyErr.Reason), Err: err}
	if keyErr.SourceID != "" {
		out.Sources = []string{keyErr.SourceID}
	}
	return out
}

// AmbiguousModError reports that a bare mod ID matched more than one
// installed mod (#373). Mod IDs are unique only WITHIN a source, so the same
// ID can name a different mod in each source a game maps; a command that
// silently took the first match would rename - or, for uninstall, delete the
// files and cache entry of - a mod the user never named.
//
// Flag is the option that resolves it, worded as the command spells it
// (`-s/--source` for `uninstall`, `update` and `mod edit`), and Caveat is an
// optional trailing
// note a command adds about its own candidates (e.g. that a local mod cannot
// be update-checked). Sources is sorted, so the message is the same
// regardless of install order.
//
// It follows this file's convention: Details() any puts the whole thing in
// the --json error envelope's "details" (Ruling 3), so a scripting caller
// gets the candidate list as data rather than by parsing the sentence.
type AmbiguousModError struct {
	ModID   string   `json:"mod_id"`
	Profile string   `json:"profile"`
	Sources []string `json:"sources"`
	Flag    string   `json:"flag"`
	Caveat  string   `json:"caveat,omitempty"`
}

// Error names every candidate source and the flag that chooses between them.
func (e *AmbiguousModError) Error() string {
	caveat := ""
	if e.Caveat != "" {
		caveat = " " + e.Caveat
	}
	return fmt.Sprintf("mod %s is in profile %s under multiple sources (%s); retry with %s to choose%s",
		e.ModID, e.Profile, strings.Join(e.Sources, ", "), e.Flag, caveat)
}

// Details implements the --json error envelope's extension point.
func (e *AmbiguousModError) Details() any { return e }

// ResolveInstalledByID finds the single installed mod carrying modID among
// rows, refusing rather than guessing when more than one does (#373).
//
// flag names the option that disambiguates, since each command spells it
// differently. A caller that already knows the source must look the row up
// directly instead - this is the bare-ID path.
func ResolveInstalledByID(rows []domain.InstalledMod, modID, profileName, flag string) (*domain.InstalledMod, error) {
	var candidates []*domain.InstalledMod
	for i := range rows {
		if rows[i].ID == modID {
			candidates = append(candidates, &rows[i])
		}
	}
	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf("mod %s not found in profile %s", modID, profileName)
	case 1:
		return candidates[0], nil
	default:
		sources := make([]string, 0, len(candidates))
		for _, c := range candidates {
			sources = append(sources, c.SourceID)
		}
		sort.Strings(sources) // deterministic regardless of install order
		return nil, &AmbiguousModError{ModID: modID, Profile: profileName, Sources: sources, Flag: flag}
	}
}
