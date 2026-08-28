// Package core: this file holds the rollback flow - ApplyRollback, its
// options/result types and every private helper they own - moved verbatim
// out of flows.go by v2 Phase 2 Unit I (#289), per the phase plan's "flows.go
// shrinks every unit" constraint. The move commit changed nothing but the
// file the code lives in; PlanRollback and ApplyRollback's plan-taking
// signature follow in their own commit.
package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// --- ApplyRollback (Phase 6b Task 5) ---

// RollbackOptions configures ApplyRollback, mirroring UpdateOptions' own
// hook plumbing exactly: ApplyRollback resolves the game/profile hooks and
// a HookRunner itself (Service.resolvedHooks/hookRunner, hooks_resolve.go).
//
// Force gates ONLY the rollback's two before_each hooks - uninstall.before_each
// (the version being rolled back FROM) and install.before_each (the version
// being rolled back TO) - matching doUpdateRollback's own --force check
// exactly. As with UpdateOptions, there is no before_all/after_all pair:
// doUpdateRollback never ran one.
type RollbackOptions struct {
	Force     bool
	SkipHooks bool // run no hooks even when hooks are configured (the CLI's --no-hooks)
}

// RollbackResult reports the outcome of ApplyRollback.
//
//   - ModName, FromVersion, ToVersion identify the rollback - split into
//     separate fields (unlike UpdateApplyResult.Applied's single formatted
//     string) because the CLI needs FromVersion/ToVersion independently for
//     its own "Rolling back %s %s → %s..." header, printed BEFORE
//     ApplyRollback is even called (the CLI keeps its own GetInstalledMod
//     call for that header - see ApplyRollback's doc comment). All three are
//     populated as soon as ApplyRollback's guard checks pass - before any
//     hook runs - so a caller can rely on them for its footer even though
//     they are not gated on the whole rollback having succeeded the way
//     UpdateApplyResult.Applied is (ApplyRollback has no equivalent
//     "succeeded end to end" list; callers infer success from a nil error).
//   - Warnings holds diagnostics doUpdateRollback printed unconditionally:
//     uninstall.before_each/install.before_each (when forced), and
//     uninstall.after_each/install.after_each hook failures (always
//     non-fatal) - same display contract as UpdateApplyResult.Warnings:
//     callers should print each entry to stderr, unconditionally, e.g.
//     `fmt.Fprintf(os.Stderr, "Warning: %v\n", w)`.
//   - Notes holds the sole diagnostic doUpdateRollback only printed under
//     --verbose: a failed SetModLinkMethod, with the historical "Warning: "
//     prefix baked into the text already, matching UpdateApplyResult.Notes'
//     own convention exactly (doUpdateRollback's verbose print was
//     textually identical to applyUpdate's own); a caller wanting
//     byte-identical output should print it to stdout ONLY under --verbose,
//     e.g. `fmt.Printf("  %s\n", n)`.
//
// Every entry in both slices is ALSO reported via the event stream at
// the exact point it is appended (UpdateBeforeEachForced/UpdateWarning/
// UpdateNote - reused verbatim from ApplyUpdate, see each DeployPhase
// constant's doc comment), Detail equal to the slice entry verbatim.
//
// On error, the returned result carries any diagnostics/identity fields
// accumulated before the failure; callers should surface them alongside the
// error.
type RollbackResult struct {
	ModName     string   `json:"mod_name"`
	FromVersion string   `json:"from_version"`
	ToVersion   string   `json:"to_version"`
	Warnings    []string `json:"warnings,omitempty"`
	Notes       []string `json:"notes,omitempty"`
}

// ApplyRollback rolls the installed mod identified by sourceID/modID back to
// its PreviousVersion, following cmd/lmm/update.go's pre-extraction
// doUpdateRollback ordering exactly: GetInstalledMod -> guard checks ->
// hooks -> installer.ReplaceForUpdate(current -> previous) - the extracted
// CLI's plain Replace step, now carrying the reversed file-ID transition
// (current FileIDs -> PreviousFileIDs) that narrows a same-version rollback
// to the restored file's own members (#150) -> RollbackModVersion (DB
// swap, with a compensating reverse-replace on failure) -> SetModLinkMethod
// -> reload -> ProfileManager.UpsertMod (compensating BOTH the DB swap and
// the Replace on failure). This is a behavior-preserving extraction - see the
// task report for the full mapping. Unlike ApplyUpdate, there is no
// download step at all - the previous version's files already live in the
// cache (ApplyUpdate itself guarantees this: it never deletes a mod's OLD
// cache entry - see ApplyUpdate's own doc comment) - so the FIRST thing this
// function's caller-visible behavior depends on is that cache entry still
// existing.
//
// Guards, checked before anything else, in order: mod.PreviousVersion must
// be non-empty ("no previous version available for rollback" - a mod that
// has never been updated, or has already been rolled back once, has no
// second previous version to roll back to), and the previous version must
// still exist in the game's cache ("previous version %s not found in
// cache" - defends against a cache entry pruned or manually deleted between
// the update and the rollback). Both mirror doUpdateRollback's own two
// precondition checks verbatim, including their exact error text (the CLI's
// own "mod not found: %s" wrapping of a failed GetInstalledMod is preserved
// here too, for the same reason).
//
// Hook failure semantics mirror doUpdateRollback's own two, independently
// Force-gated before_each hooks (uninstall.before_each for the CURRENT
// version, install.before_each for the PREVIOUS version being redeployed:
// fatal unless Force is set, in which case a Warning is recorded and the
// rollback proceeds) and its two always-non-fatal after_each hooks
// (uninstall.after_each, install.after_each - both recorded as Warnings
// regardless of Force, run in that order immediately after Replace, well
// before the DB/profile writes below - see UpdateWarning's doc comment).
//
// A failure to write RollbackModVersion triggers a best-effort compensating
// reverse Installer.ReplaceForUpdate (redeploying the CURRENT version with
// the file-ID transition swapped back, undoing the replace this function
// just performed) before returning the error; a
// failure to write ProfileManager.UpsertMod afterward compensates BOTH -
// another RollbackModVersion (undoing the DB swap) AND another reverse
// replace - matching doUpdateRollback's own two, textually-near-identical
// compensation blocks exactly. A failure reloading the rolled-back mod
// (the GetInstalledMod call between those two steps) is, however, NOT
// compensated - matching doUpdateRollback's own verbatim behavior, a
// pre-existing gap this extraction preserves rather than fixes (see the
// task report). A failure to write SetModLinkMethod is NOT rolled back
// either, matching doUpdateRollback exactly (it only ever produced a
// --verbose-gated Note).
//
// sink may be nil. On error, the returned result carries any
// diagnostics/identity fields accumulated before the failure - callers
// should surface them alongside the error (see RollbackResult's doc
// comment).
func (s *Service) ApplyRollback(ctx context.Context, game *domain.Game, profileName, sourceID, modID string, opts RollbackOptions, sink EventSink) (*RollbackResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &RollbackResult{}, err
	}
	defer release()
	return s.applyRollback(ctx, game, profileName, sourceID, modID, opts, sink)
}

func (s *Service) applyRollback(ctx context.Context, game *domain.Game, profileName, sourceID, modID string, opts RollbackOptions, sink EventSink) (*RollbackResult, error) {
	result := &RollbackResult{}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}

	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return result, fmt.Errorf("mod not found: %s", modID)
	}

	// #97: a locked ref refuses rollback entirely, mirroring ApplyUpdate's
	// own gate - rollback moves a locked ref's Version just as surely as an
	// update would, and the lock's whole contract is that only an explicit
	// re-lock or unlock may do that. Checked before any side effect (hooks,
	// Replace, DB/profile writes).
	if prof, err := s.NewProfileManager().Get(game.ID, profileName); err == nil {
		if ref := prof.FindRef(mod.SourceID, mod.ID); ref != nil && ref.Locked {
			return result, LockedRefRefusalError(mod.Mod, profileName, ref)
		}
	}
	// (A missing/unreadable profile falls through - matches ApplyUpdate's
	// own precedent: a lock cannot exist in an unloadable profile.)

	if mod.PreviousVersion == "" {
		return result, fmt.Errorf("no previous version available for rollback")
	}

	if !s.GetGameCache(game).Exists(game.ID, mod.SourceID, mod.ID, mod.PreviousVersion) {
		return result, fmt.Errorf("previous version %s not found in cache", mod.PreviousVersion)
	}

	result.ModName = mod.Name
	result.FromVersion = mod.Version
	result.ToVersion = mod.PreviousVersion

	scope := Scope{Op: OpRollback, ModName: mod.Name, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}}

	hooks, err := s.resolvedHooks(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	runner, err := s.hookRunner(ctx)
	if err != nil {
		return result, err
	}
	hookCtx := hookContextFor(game)
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "uninstall.before_each", hooks.GetUninstallBeforeEach()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("uninstall.before_each hook failed: %w", err)
		}
		msg := fmt.Sprintf("uninstall.before_each hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(HookEvent{Scope: scope, Phase: UpdateBeforeEachForced, Stage: "uninstall.before_each", Detail: msg})
	}

	linkMethod, err := s.GetEffectiveLinkMethod(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	installer := s.NewInstallerWithLinker(game, s.GetLinker(linkMethod))

	prevMod := mod.Mod
	prevMod.Version = mod.PreviousVersion

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = prevMod.ID, prevMod.Name, prevMod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.before_each", hooks.GetInstallBeforeEach()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("install.before_each hook failed: %w", err)
		}
		msg := fmt.Sprintf("install.before_each hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(HookEvent{Scope: scope, Phase: UpdateBeforeEachForced, Stage: "install.before_each", Detail: msg})
	}

	// #150: the update path's file-ID transition, reversed - current FileIDs
	// back to PreviousFileIDs - so a same-version file-only rollback (ONE
	// shared cache dir) narrows to the restored file's members instead of
	// deploying the union; see ReplaceForUpdate/resolveSharedDirUpdate. On a
	// normal different-version rollback this behaves exactly like Replace.
	if err := installer.ReplaceForUpdate(ctx, game, &mod.Mod, &prevMod, profileName, mod.FileIDs, mod.PreviousFileIDs); err != nil {
		return result, fmt.Errorf("deploying previous version: %w", err)
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "uninstall.after_each", hooks.GetUninstallAfterEach()); err != nil {
		msg := fmt.Sprintf("uninstall.after_each hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(WarningEvent{Scope: scope, Phase: UpdateWarning, Message: msg})
	}
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = prevMod.ID, prevMod.Name, prevMod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.after_each", hooks.GetInstallAfterEach()); err != nil {
		msg := fmt.Sprintf("install.after_each hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(WarningEvent{Scope: scope, Phase: UpdateWarning, Message: msg})
	}

	if err := s.rollbackModVersion(ctx, mod.SourceID, mod.ID, game.ID, profileName); err != nil {
		// recovery must not inherit the caller's cancellation (v2 Phase 1 Task 3 C1 class)
		if rerr := installer.ReplaceForUpdate(context.WithoutCancel(ctx), game, &prevMod, &mod.Mod, profileName, mod.PreviousFileIDs, mod.FileIDs); rerr != nil {
			s.logger().Warn("rollback after failed install also failed", "step", "replace_for_update", "err", rerr)
		}
		return result, fmt.Errorf("updating database: %w", err)
	}

	if err := s.setModLinkMethod(ctx, mod.SourceID, mod.ID, game.ID, profileName, linkMethod); err != nil {
		msg := fmt.Sprintf("Warning: could not update link method: %v", err)
		result.Notes = append(result.Notes, msg)
		emit(StepEvent{Scope: scope, Phase: UpdateNote, Detail: msg})
	}

	rolledBackMod, err := s.GetInstalledMod(ctx, mod.SourceID, mod.ID, game.ID, profileName)
	if err != nil {
		return result, fmt.Errorf("reloading rolled back mod: %w", err)
	}

	pm := s.NewProfileManager()
	if err := pm.UpsertMod(game.ID, profileName, domain.ModReference{
		SourceID: rolledBackMod.SourceID,
		ModID:    rolledBackMod.ID,
		Version:  rolledBackMod.Version,
		FileIDs:  rolledBackMod.FileIDs,
	}); err != nil {
		// recovery must not inherit the caller's cancellation (v2 Phase 1 Task 3 C1 class)
		rctx := context.WithoutCancel(ctx)
		if rerr := s.rollbackModVersion(rctx, mod.SourceID, mod.ID, game.ID, profileName); rerr != nil {
			s.logger().Warn("rollback after failed install also failed", "step", "rollback_mod_version", "err", rerr)
		}
		if rerr := installer.ReplaceForUpdate(rctx, game, &prevMod, &mod.Mod, profileName, mod.PreviousFileIDs, mod.FileIDs); rerr != nil {
			s.logger().Warn("rollback after failed install also failed", "step", "replace_for_update", "err", rerr)
		}
		return result, fmt.Errorf("updating profile: %w", err)
	}

	// #197 I1 fix: a rollback changes the mod's Version (and possibly its
	// FileIDs), both regeneration triggers - without this, the merged pak
	// keeps the rolled-away-from version's diff until some OTHER flow
	// happens to sync it. #197 postsmoke fix: Warnings, not Notes (Notes is
	// --verbose-gated in the CLI) - AND emit UpdateWarning: doUpdateRollback
	// (cmd/lmm/update.go) drives its console output from live progress
	// events, never reads RollbackResult.Warnings back directly.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		msg := fmt.Sprintf("could not sync merged pak: %v", syncErr)
		result.Warnings = append(result.Warnings, msg)
		emit(WarningEvent{Scope: Scope{Op: OpRollback}, Phase: UpdateWarning, Message: msg})
	} else {
		for _, w := range syncWarnings {
			result.Warnings = append(result.Warnings, w)
			emit(WarningEvent{Scope: Scope{Op: OpRollback}, Phase: UpdateWarning, Message: w})
		}
	}

	return result, nil
}
