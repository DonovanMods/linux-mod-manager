// Package core: this file holds the rollback flow - PlanRollback/
// ApplyRollback, their options/plan/result types and every private helper
// they own - moved verbatim out of flows.go by v2 Phase 2 Unit I (#289), per
// the phase plan's "flows.go shrinks every unit" constraint. The move commit
// changed nothing but the file the code lives in; PlanRollback and the
// lock-state additions that follow it are their own commit.
package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// --- PlanRollback (v2 Phase 2 Unit I, #289) ---

// RollbackPlan is the pure, displayable result of PlanRollback: everything
// the pre-extraction CLI's doUpdateRollback computed inline before deciding
// whether to print its "Rolling back..." header and call ApplyRollback, or
// refuse as a skip (locked) or an error (not installed, no previous
// version). See PlanRollback's doc comment for the exact mapping.
type RollbackPlan struct {
	// Mod is the installed mod PlanRollback was asked about, freshly re-read
	// via GetInstalledMod - mirrors UpdatePlan.Mod exactly.
	Mod domain.InstalledMod `json:"mod"`
	// FromVersion/ToVersion name the rollback's direction: FromVersion is
	// Mod.Version, ToVersion is Mod.PreviousVersion - split out (rather than
	// making a renderer read them off Mod directly) so a caller's "%s → %s"
	// header/footer never has to know which InstalledMod field means what,
	// mirroring RollbackResult's own ModName/FromVersion/ToVersion split.
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	// Locked/LockedVersion mirror the profile ref's lock state, read once -
	// same semantics as UpdatePlan.Locked/LockedVersion. LockedVersion is the
	// lock's OWN recorded version (domain.ModReference.Version), which can
	// differ from Mod.Version - 'lmm mod lock <id> <version>' allows locking
	// at a version other than the one currently installed - so a renderer
	// needing the exact locked-at version (doUpdateRollback's "locked at
	// v%s") cannot substitute FromVersion for it.
	Locked        bool   `json:"locked"`
	LockedVersion string `json:"locked_version,omitempty"`
	// Refusal is LockedRefUnlockOnlyRefusalError's full text, precomputed
	// whenever Locked - mirrors UpdatePlan.Refusal (there populated only
	// when Locked && Update != nil; here a rollback is always "available"
	// once PlanRollback returns successfully, so Locked alone gates it),
	// including that cmd/lmm's renderer prints it verbatim since #294
	// (Ruling 5) and that ApplyRollback's own gate ignores the version, so
	// the refusal offers unlocking only (unit Q review, I1).
	Refusal string `json:"refusal,omitempty"`
	// CacheMissing reports that ToVersion's cache entry is gone (pruned, or
	// manually deleted since the update that set PreviousVersion) - the
	// second of doUpdateRollback's two pre-ApplyRollback guards. Unlike the
	// "no previous version" guard (which PlanRollback itself refuses with an
	// error, since there is nothing left to plan), this is captured as plan
	// data: ApplyRollback re-derives it independently at apply time anyway
	// (a plan can go stale between planning and applying), so PlanRollback
	// only needs to report it for a renderer that wants to refuse before
	// ever calling ApplyRollback, matching doUpdateRollback's own ordering.
	CacheMissing bool `json:"cache_missing"`
	// snapshot is the installed-mod set this plan was computed against
	// (Ruling 5): ApplyRollback re-derives it under beginOp and returns
	// ErrStalePlan when it no longer matches. Unexported and outside the
	// wire contract, mirroring UpdatePlan.snapshot exactly.
	snapshot installedSnapshot `json:"-"`
}

// PlanRollback computes what "lmm update rollback <mod-id>" would do for
// (sourceID, modID) in profileName - the pure, read-only half of the pre-
// extraction CLI's doUpdateRollback. No DB write, filesystem write, or hook
// execution ever happens here; the only reads are GetInstalledMod, the
// profile's lock state, and a cache-existence check.
//
// Errors mirror doUpdateRollback's own first two guards exactly: a missing
// mod returns GetInstalledMod's raw error (domain.ErrModNotFound - the CLI
// wraps it as "mod not found: %s", matching its pre-extraction text), and an
// empty PreviousVersion returns "no previous version available for
// rollback" verbatim. Both guards are true error returns, not plan data,
// because there is nothing left to plan - a caller cannot render an
// unlocked/locked or cache-missing/cache-present rollback for a mod that
// either doesn't exist or has nothing to roll back to. The remaining two
// pre-extraction checks (locked, cache missing) DO return a valid plan -
// see RollbackPlan's doc comment for why.
func (s *Service) PlanRollback(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*RollbackPlan, error) {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, err
	}

	if mod.PreviousVersion == "" {
		return nil, fmt.Errorf("no previous version available for rollback")
	}

	// Ruling 5: record the installed set this plan is being computed
	// against, so ApplyRollback can refuse it once that set has moved on.
	snapshot, err := s.currentInstalledSnapshot(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}

	plan := &RollbackPlan{
		Mod:         *mod,
		FromVersion: mod.Version,
		ToVersion:   mod.PreviousVersion,
		snapshot:    snapshot,
	}

	locked, lockedVersion, err := s.lockState(ctx, game.ID, profileName, mod.SourceID, mod.ID)
	if err != nil {
		return nil, err
	}
	if locked {
		plan.Locked = true
		plan.LockedVersion = lockedVersion
		ref := &domain.ModReference{Version: lockedVersion}
		plan.Refusal = LockedRefUnlockOnlyRefusalError(mod.Mod, profileName, ref).Error()
	}

	plan.CacheMissing = !s.GetGameCache(game).Exists(game.ID, mod.SourceID, mod.ID, mod.PreviousVersion)

	return plan, nil
}

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
//   - ModName, FromVersion, ToVersion identify the rollback - separate
//     fields, exactly like UpdateApplyResult's own Name/FromVersion/
//     ToVersion, because the CLI needs them independently for its own
//     "Rolling back %s %s → %s..." header, printed BEFORE ApplyRollback is
//     even called (the CLI renders that header from RollbackPlan.Mod/
//     FromVersion/ToVersion instead of its own GetInstalledMod call - see
//     PlanRollback's doc comment). All three are populated as soon as
//     ApplyRollback's guard checks pass - before any hook runs - so a
//     caller can rely on them for its footer, but that also means they are
//     NOT gated on the whole rollback having succeeded the way
//     UpdateApplyResult.Name is. Status, not ModName, is this result's
//     "did it happen" signal.
//   - Status is UpdateRolledBack once the whole sequence has succeeded and
//     UpdateSkipped otherwise - the two outcomes cmd/lmm's rollback --json
//     document has always reported ("rolled_back", and "skipped" with
//     Reason "locked" for a locked ref), reusing UpdateStatus rather than
//     a second near-identical enum (v2 Phase 3 Task 3, #301) so Unit O can
//     emit this result directly. It is UpdateSkipped from construction, so
//     an error return never claims a rollback that did not happen. Reason
//     carries the raw refusal word ("locked") and is empty otherwise -
//     never a pre-formatted sentence, matching InstalledRef.Reason.
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
	// Mod identifies what was rolled back, with Version = the version rolled
	// back TO - the convention core's own results follow (v2 Phase 3 Task 6,
	// #302: rollback's own document had no mod reference at all, so
	// `lmm update rollback --json` could not say which mod, or which source,
	// it was reporting on). cmd/lmm/update.go's planUpdateResult sets
	// UpdateApplyResult.Mod.Version differently for its own not-applied
	// branches (pinned/up-to-date/locked/dry-run) - see that function's own
	// doc comment.
	Mod         domain.ModReference `json:"mod"`
	ModName     string              `json:"mod_name"`
	FromVersion string              `json:"from_version"`
	ToVersion   string              `json:"to_version"`
	Status      UpdateStatus        `json:"status"`
	Reason      string              `json:"reason,omitempty"`
	Warnings    []string            `json:"warnings,omitempty"`
	Notes       []string            `json:"notes,omitempty"`
}

// ApplyRollback rolls plan.Mod back to its PreviousVersion, following
// cmd/lmm/update.go's pre-extraction doUpdateRollback ordering exactly:
// guard checks -> hooks -> installer.ReplaceForUpdate(current -> previous) -
// the extracted CLI's plain Replace step, now carrying the reversed file-ID
// transition (current FileIDs -> PreviousFileIDs) that narrows a
// same-version rollback to the restored file's own members (#150) ->
// RollbackModVersion (DB swap, with a compensating reverse-replace on
// failure) -> SetModLinkMethod -> reload -> ProfileManager.UpsertMod
// (compensating BOTH the DB swap and the Replace on failure). This is a
// behavior-preserving extraction - see the task report for the full
// mapping. Unlike ApplyUpdate, there is no download step at all - the
// previous version's files already live in the cache (ApplyUpdate itself
// guarantees this: it never deletes a mod's OLD cache entry - see
// ApplyUpdate's own doc comment) - so the FIRST thing this function's
// caller-visible behavior depends on is that cache entry still existing.
//
// Guards, checked before anything else (after Ruling 5's checkPlanFresh),
// in order: mod.PreviousVersion must be non-empty ("no previous version
// available for rollback" - a mod that has never been updated, or has
// already been rolled back once, has no second previous version to roll
// back to), and the previous version must still exist in the game's cache
// ("previous version %s not found in cache" - defends against a cache entry
// pruned or manually deleted between the update and the rollback). Both
// mirror doUpdateRollback's own two precondition checks verbatim, including
// their exact error text - and both are re-derived here from plan.Mod
// rather than trusted from RollbackPlan.CacheMissing/the plan's own
// implicit "PreviousVersion != empty" guarantee, mirroring ApplyUpdate's own
// independent lock re-check: a plan is a snapshot, and either condition can
// have changed in the window between PlanRollback and this call.
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
func (s *Service) ApplyRollback(ctx context.Context, game *domain.Game, plan *RollbackPlan, opts RollbackOptions, sink EventSink) (*RollbackResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &RollbackResult{Status: UpdateSkipped}, err
	}
	defer release()
	return s.applyRollback(ctx, game, plan, opts, sink)
}

func (s *Service) applyRollback(ctx context.Context, game *domain.Game, plan *RollbackPlan, opts RollbackOptions, sink EventSink) (*RollbackResult, error) {
	// Status starts at UpdateSkipped: every early return below is a
	// rollback that did not happen, and the zero UpdateStatus
	// (UpdateUpdated) would misreport that (#301).
	result := &RollbackResult{Status: UpdateSkipped}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}

	// Ruling 5: the plan is a contract about a world that may have moved.
	// First statement inside the op (ApplyRollback took beginOp just above),
	// before any lock check, hook, or side effect - mirroring applyUpdate's
	// own placement.
	if err := s.checkPlanFresh(ctx, plan.Mod.GameID, plan.Mod.ProfileName, plan.snapshot); err != nil {
		return result, err
	}

	mod := plan.Mod
	profileName := mod.ProfileName

	// #97: a locked ref refuses rollback entirely, mirroring ApplyUpdate's
	// own gate - rollback moves a locked ref's Version just as surely as an
	// update would, and the lock's whole contract is that only an explicit
	// re-lock or unlock may do that. Checked before any side effect (hooks,
	// Replace, DB/profile writes).
	if prof, err := s.NewProfileManager().Get(game.ID, profileName); err == nil {
		if ref := prof.FindRef(mod.SourceID, mod.ID); ref != nil && ref.Locked {
			result.Reason = "locked"
			return result, LockedRefUnlockOnlyRefusalError(mod.Mod, profileName, ref)
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

	result.Mod = domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.PreviousVersion}
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
	installer := s.newInstallerWithLinker(game, s.getLinker(linkMethod))

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
	restoredRef := domain.ModReference{
		SourceID: rolledBackMod.SourceID,
		ModID:    rolledBackMod.ID,
		Version:  rolledBackMod.Version,
		FileIDs:  rolledBackMod.FileIDs,
	}
	if err := pm.UpsertMod(game.ID, profileName, restoredRef); err != nil {
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

	// The whole sequence - hooks, Replace, DB swap, link method, profile
	// upsert - has succeeded: everything after this point is non-fatal
	// merged-pak housekeeping, so the rollback is reported as done (#301).
	result.Status = UpdateRolledBack
	// Upgraded from the early stamp above to the ref actually written: same
	// source/mod/version, now also carrying the restored FileIDs.
	result.Mod = restoredRef

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
