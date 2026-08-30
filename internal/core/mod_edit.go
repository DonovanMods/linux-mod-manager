// Package core: this file holds `mod edit`'s core flow - PlanRelinkMod/
// ApplyRelinkMod - a behavior-preserving extraction of cmd/lmm/mod_edit.go's
// pre-Task-10 doModEdit (v2 Phase 3 Task 10, #303). "Edit" covers two
// distinct shapes cmd's --name/--version/--author/--source/--source-id
// flags select between: a metadata-only edit (no --source/--source-id) that
// updates the DB row in place, and a re-link (either flag given) that moves
// the mod to a new source_id/mod_id identity - deleting the old DB row and
// profile ref, optionally refreshing metadata from the new source, and
// saving a fresh row/ref under the new identity. Both shapes flow through
// the same Plan/Apply pair; RelinkPlan.Relink tells a renderer (and
// ApplyRelinkMod itself) which one is in play.
package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// RelinkPlan is the pure, displayable result of PlanRelinkMod - what
// `lmm mod edit` would do to one installed mod.
type RelinkPlan struct {
	// Mod is the installed mod as it exists today (before any edit).
	Mod domain.InstalledMod `json:"mod"`
	// From is Mod's current identity (SourceID/ModID/Version).
	From domain.ModReference `json:"from"`
	// To is the identity ApplyRelinkMod would move Mod to: newSourceID/
	// newModID, each defaulted to Mod's own current value when the
	// corresponding argument is empty (mirrors doModEdit's own "whichever
	// of the two you omit keeps its current value" contract). To.Version is
	// always empty - the target version isn't resolved until
	// ApplyRelinkMod (a re-link to a non-local source may take it from that
	// source's own metadata).
	To domain.ModReference `json:"to"`
	// Relink reports whether the caller passed a non-empty newSourceID or
	// newModID at all (regardless of whether the resolved To identity
	// actually differs from From) - matching doModEdit's own `relink`
	// boolean exactly, including its edge case: re-specifying the mod's own
	// current source/id still counts as a re-link request.
	Relink bool `json:"relink"`
	// TargetInstalled reports that To's identity already names a DIFFERENT
	// installed mod in Profile. Informational only - ApplyRelinkMod does
	// not refuse on it (matching doModEdit's own pre-existing behavior,
	// unchanged by this extraction); a --json/dry-run caller can use it to
	// warn before applying.
	TargetInstalled bool `json:"target_installed"`
	// Locked/LockedVersion mirror the profile ref's lock state, read once.
	Locked        bool   `json:"locked"`
	LockedVersion string `json:"locked_version,omitempty"`
	// Refusal is populated whenever Relink && Locked: re-linking a locked
	// ref is always refused (#146), regardless of the target version. Since
	// #294 (Ruling 5) the text is the canonical one - one wording for every
	// lock refusal of this KIND in the product - specifically
	// LockedRefUnlockOnlyRefusalError's, since this gate ignores the
	// version and only unlocking can unblock it (unit Q review, I1); and
	// the SENTENCE half only (lockedRefUnlockOnlyMessage), without the
	// ErrModLocked sentinel prefix
	// UpdatePlan.Refusal/RollbackPlan.Refusal carry, because doModEdit
	// re-wraps it (`fmt.Errorf("%w: %s", core.ErrModLocked, plan.Refusal)`)
	// and would otherwise print the prefix twice. The wrapped result is
	// byte-identical to LockedRefUnlockOnlyRefusalError(...).Error(), which
	// is what ApplyRelinkMod itself returns. A version-only edit while locked is NOT
	// decidable here (PlanRelinkMod has no version argument); ApplyRelinkMod
	// re-derives that guard independently from RelinkOptions.Version,
	// mirroring ApplyRollback/ApplyUpdate's own independent lock re-checks.
	Refusal string `json:"refusal,omitempty"`
	// MergedPakAffected reports whether ApplyRelinkMod's merged-pak sync is
	// a real resync (the game is DeployCompile) rather than syncMergedPak's
	// own cheap non-compile no-op.
	MergedPakAffected bool `json:"merged_pak_affected"`
	// Profile is the profile this plan and ApplyRelinkMod both operate on.
	Profile string `json:"profile"`
	// snapshot is the installed-mod set this plan was computed against
	// (Ruling 5): ApplyRelinkMod re-derives it under beginOp and returns
	// ErrStalePlan when it no longer matches.
	snapshot installedSnapshot `json:"-"`
}

// PlanRelinkMod computes what "lmm mod edit" would do to sourceID/modID in
// profileName, re-linking it to newSourceID/newModID - pass empty strings
// for both to plan a metadata-only edit (--name/--version/--author with no
// --source/--source-id). Pure and read-only: the only reads are
// GetInstalledMod, the installed set (TargetInstalled and Ruling 5's
// staleness snapshot), and the profile's lock state - no source is ever
// contacted (a re-link's metadata refresh happens in ApplyRelinkMod,
// exactly where doModEdit always did it, since Ruling 1 keeps Plans
// side-effect- and network-call-free).
func (s *Service) PlanRelinkMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID, newSourceID, newModID string) (*RelinkPlan, error) {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, err
	}

	relink := newSourceID != "" || newModID != ""
	resolvedSourceID := newSourceID
	if resolvedSourceID == "" {
		resolvedSourceID = mod.SourceID
	}
	resolvedModID := newModID
	if resolvedModID == "" {
		resolvedModID = mod.ID
	}

	snapshot, err := s.currentInstalledSnapshot(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}

	plan := &RelinkPlan{
		Mod:               *mod,
		From:              domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version},
		To:                domain.ModReference{SourceID: resolvedSourceID, ModID: resolvedModID},
		Relink:            relink,
		MergedPakAffected: game.DeployMode == domain.DeployCompile,
		Profile:           profileName,
		snapshot:          snapshot,
	}

	if relink && (resolvedSourceID != mod.SourceID || resolvedModID != mod.ID) {
		if _, err := s.GetInstalledMod(ctx, resolvedSourceID, resolvedModID, game.ID, profileName); err == nil {
			plan.TargetInstalled = true
		}
	}

	locked, lockedVersion, err := s.lockState(ctx, game.ID, profileName, mod.SourceID, mod.ID)
	if err != nil {
		return nil, err
	}
	if locked {
		plan.Locked = true
		plan.LockedVersion = lockedVersion
		if relink {
			// #294 (Ruling 5): lockedRefUnlockOnlyMessage's sentence half -
			// see RelinkPlan.Refusal for why the sentinel prefix is left off.
			plan.Refusal = lockedRefUnlockOnlyMessage(mod.Mod, profileName, &domain.ModReference{Version: lockedVersion})
		}
	}

	return plan, nil
}

// RelinkOptions carries ApplyRelinkMod's metadata overrides - doModEdit's
// --name/--version/--author flags. Each is applied verbatim when non-empty;
// a re-link's metadata refresh from the target source fills in whichever of
// Name/Author/Version was left empty (see ApplyRelinkMod's doc comment).
type RelinkOptions struct {
	Name    string
	Version string
	Author  string
}

// RelinkResult reports the outcome of ApplyRelinkMod.
type RelinkResult struct {
	// Mod is the mod's final state after the edit.
	Mod domain.InstalledMod `json:"mod"`
	// Changes lists what changed, in the exact order doModEdit printed them
	// - e.g. "name -> New Name", "source -> curseforge (was nexusmods)".
	// Empty (with NoChanges true) when nothing was requested.
	Changes []string `json:"changes,omitempty"`
	// NoChanges is true when no edit was requested at all (no --name/
	// --version/--author and no re-link) - ApplyRelinkMod returns
	// successfully without writing anything, mirroring doModEdit's own
	// "No changes specified..." early return.
	NoChanges bool `json:"no_changes,omitempty"`
	// Notes holds ApplyRelinkMod's --verbose-gated profile-write
	// diagnostics, prefix baked in ("Warning: ..."), also reported via
	// RelinkProfileNote events at the point each happens: a caller wanting
	// byte-identical output prints `if verbose { fmt.Printf("%s\n", n) }`
	// for each entry ONLY if it is not already consuming the event stream
	// (printing both double-prints).
	Notes []string `json:"notes,omitempty"`
	// Warnings holds diagnostics that must reach the user unconditionally
	// (a failed metadata fetch, a merged-pak sync failure/warning) - raw
	// text, no prefix - also reported via RelinkWarning events at the point
	// each happens: a caller wanting byte-identical output prints
	// `fmt.Fprintf(os.Stderr, "Warning: %s\n", w)` for each entry ONLY if it
	// is not already consuming the event stream.
	Warnings []string `json:"warnings,omitempty"`
}

// ApplyRelinkMod applies plan - a metadata-only edit, or a re-link to a
// different source_id/mod_id - exactly reproducing doModEdit's
// pre-extraction sequence: guard checks, metadata overrides, a re-link's
// metadata refresh from the target source (skipped for domain.SourceLocal),
// the DB row replace (DeleteInstalledMod + SaveInstalledMod) and profile ref
// upsert/remove a re-link requires, and a final SyncMergedPak so a version
// or identity change that affects the merge is picked up immediately (#197
// postsmoke fix). sink may be nil.
//
// Guards, checked fresh here (after Ruling 5's checkPlanFresh) rather than
// trusted from plan - a plan is a snapshot, mirroring ApplyRollback/
// ApplyUpdate's own independent lock re-checks: a re-link refuses
// unconditionally when the ref is locked (plan.Refusal's wording, wrapped in
// ErrModLocked so callers can errors.Is it - LockedRefUnlockOnlyRefusalError,
// since moving the lock cannot unblock a re-link); a metadata-only edit
// refuses only when opts.Version is set and differs from the lock's own
// target (LockedRefRefusalError, whose "move the lock" remedy DOES work
// here) - a --version equal to the locked version is a
// realign, not a move, and stays allowed, matching UpsertMod's own
// same-version allowance.
func (s *Service) ApplyRelinkMod(ctx context.Context, game *domain.Game, plan *RelinkPlan, opts RelinkOptions, sink EventSink) (*RelinkResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.applyRelinkMod(ctx, game, plan, opts, sink)
}

func (s *Service) applyRelinkMod(ctx context.Context, game *domain.Game, plan *RelinkPlan, opts RelinkOptions, sink EventSink) (*RelinkResult, error) {
	if err := s.checkPlanFresh(ctx, game.ID, plan.Profile, plan.snapshot); err != nil {
		return nil, err
	}

	mod := plan.Mod
	profileName := plan.Profile

	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	scope := func() Scope {
		return Scope{Op: OpModEdit, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}, ModName: mod.Name}
	}

	result := &RelinkResult{}
	step := func(phase DeployPhase, msg string) {
		emit(StepEvent{Scope: scope(), Phase: phase, Detail: msg})
	}
	warn := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		result.Warnings = append(result.Warnings, msg)
		step(RelinkWarning, msg)
	}
	note := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		result.Notes = append(result.Notes, msg)
		step(RelinkProfileNote, msg)
	}

	locked, lockedVersion, err := s.lockState(ctx, game.ID, profileName, mod.SourceID, mod.ID)
	if err != nil {
		return nil, err
	}
	if locked {
		if plan.Relink {
			// #294 (Ruling 5), as refined by the unit Q review (I1): the
			// canonical refusal, in its UNLOCK-ONLY variant - this gate
			// refuses on the lock alone, so moving the lock cannot unblock
			// it and must not be offered as a remedy.
			return nil, LockedRefUnlockOnlyRefusalError(mod.Mod, profileName, &domain.ModReference{Version: lockedVersion})
		}
		if opts.Version != "" && opts.Version != lockedVersion {
			ref := &domain.ModReference{Version: lockedVersion}
			return nil, LockedRefRefusalError(mod.Mod, profileName, ref)
		}
	}

	var changes []string

	if opts.Name != "" {
		mod.Name = opts.Name
		changes = append(changes, fmt.Sprintf("name -> %s", opts.Name))
	}
	if opts.Version != "" {
		mod.Version = opts.Version
		changes = append(changes, fmt.Sprintf("version -> %s", opts.Version))
	}
	if opts.Author != "" {
		mod.Author = opts.Author
		changes = append(changes, fmt.Sprintf("author -> %s", opts.Author))
	}

	if plan.Relink {
		newSourceID := plan.To.SourceID
		newModID := plan.To.ModID

		if newSourceID != domain.SourceLocal {
			cfGameID, ok := game.SourceIDs[newSourceID]
			if !ok {
				return nil, fmt.Errorf("source %q is not configured for %s", newSourceID, game.Name)
			}

			step(RelinkFetching, "Fetching metadata from "+newSourceID+"...")
			fetched, ferr := s.GetMod(ctx, newSourceID, cfGameID, newModID)
			if ferr != nil {
				warn("could not fetch metadata: %v", ferr)
			} else {
				if opts.Name == "" {
					mod.Name = fetched.Name
					changes = append(changes, fmt.Sprintf("name -> %s (from %s)", fetched.Name, newSourceID))
				}
				if opts.Author == "" && fetched.Author != "" {
					mod.Author = fetched.Author
					changes = append(changes, fmt.Sprintf("author -> %s (from %s)", fetched.Author, newSourceID))
				}
				if opts.Version == "" && fetched.Version != "" {
					mod.Version = fetched.Version
					changes = append(changes, fmt.Sprintf("version -> %s (from %s)", fetched.Version, newSourceID))
				}
				mod.Summary = fetched.Summary
				mod.SourceURL = fetched.SourceURL
				mod.PictureURL = fetched.PictureURL
				mod.ManualDownload = false // now linked, updates may work
			}
		}

		oldSourceID, oldModID := mod.SourceID, mod.ID
		mod.SourceID = newSourceID
		mod.ID = newModID

		changes = append(changes, fmt.Sprintf("source -> %s (was %s)", newSourceID, oldSourceID))
		if newModID != oldModID {
			changes = append(changes, fmt.Sprintf("id -> %s (was %s)", newModID, oldModID))
		}

		if err := s.deleteInstalledMod(ctx, oldSourceID, oldModID, game.ID, profileName); err != nil {
			return nil, fmt.Errorf("removing old record: %w", err)
		}

		// Ruling 16 (A): the old DB record is already deleted, so ALL THREE
		// steps that complete it - dropping the old profile ref, writing
		// the new one, and saving the new DB row under the new identity -
		// run to the end even under a cancelled ctx. They are one
		// completion, so the cancellation is re-checked once, after all
		// three, and never in between (fix wave round 1's residual: this
		// used to stop after the profile pair, leaving the profile agreeing
		// with the NEW identity while the DB had NEITHER identity's row).
		pm := s.NewProfileManager()
		if err := completeProfileWrite(ctx, func(ctx context.Context) error {
			return pm.RemoveMod(ctx, game.ID, profileName, oldSourceID, oldModID)
		}); err != nil && ctx.Err() == nil {
			note("Warning: could not remove old profile entry: %v", err)
		}
		modRef := domain.ModReference{SourceID: newSourceID, ModID: newModID, Version: mod.Version}
		if err := completeProfileWrite(ctx, func(ctx context.Context) error {
			return pm.UpsertMod(ctx, game.ID, profileName, modRef)
		}); err != nil && ctx.Err() == nil {
			note("Warning: could not update profile: %v", err)
		}
		if err := completeDBWrite(ctx, func(ctx context.Context) error {
			return s.saveInstalledMod(ctx, &mod)
		}); err != nil && ctx.Err() == nil {
			return nil, fmt.Errorf("saving changes: %w", err)
		}
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
	}

	if len(changes) == 0 {
		result.Mod = mod
		result.NoChanges = true
		return result, nil
	}

	if !plan.Relink {
		// A re-link already saved mod under its new identity above, as the
		// last step of that one completion chain - saving it again here
		// would be a harmless but redundant duplicate write.
		if err := s.saveInstalledMod(ctx, &mod); err != nil {
			return nil, fmt.Errorf("saving changes: %w", err)
		}
	}

	if opts.Version != "" && !plan.Relink {
		pm := s.NewProfileManager()
		modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version}
		// Ruling 16 (A): saveInstalledMod above already committed the new
		// version, so the profile ref that completes it is written even
		// under a cancelled ctx; the cancellation is then fatal.
		if err := completeProfileWrite(ctx, func(ctx context.Context) error {
			return pm.UpsertMod(ctx, game.ID, profileName, modRef)
		}); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			note("Warning: could not update profile version: %v", err)
		}
	}

	// #197 postsmoke seam-audit fix: a --version edit is a direct
	// regeneration trigger; a --source/--source-id relink changes the
	// identity enabledMergeSources keys off (mod.SourceID + ":" + mod.ID).
	// Sync unconditionally now that changes is non-empty - cheap no-op if
	// nothing merge-relevant actually moved (MergedPakAffected reports
	// which case this is).
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		warn("could not sync merged pak: %v", syncErr)
	} else {
		for _, w := range syncWarnings {
			warn("%s", w)
		}
	}

	result.Mod = mod
	result.Changes = changes
	return result, nil
}
