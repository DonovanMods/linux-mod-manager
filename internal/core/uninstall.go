// Package core provides business logic orchestration for lmm.
// uninstall.go holds the single-mod uninstall flow: UninstallOptions/
// UninstallResult and UninstallMod. Moved verbatim out of flows.go by v2
// Phase 2 Unit M (#293); runHook, which UninstallMod shares with
// DeployProfile, stays behind in flows.go.
package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// UninstallPlan is PlanUninstall's side-effect-free description of what
// `lmm uninstall <mod-id>` would do: which installed mod the arguments
// actually resolve to (the cross-source disambiguation the CLI used to do
// inline), which game-directory paths would disappear, whether the cache
// entry survives, and which hooks would run. ApplyUninstall refuses a plan
// whose installed-mod set has changed since (Ruling 5).
type UninstallPlan struct {
	// Mod is the resolved target - the whole installed record, not just a
	// reference, because it is also what ApplyUninstall reads the profile,
	// source and ID back off. With an explicit source it is that source's
	// copy; with a bare ID it is the FIRST installed mod carrying that ID,
	// preserving the pre-lift CLI's disambiguation exactly (see
	// PlanUninstall's doc comment).
	Mod domain.InstalledMod `json:"mod"`

	// Files lists the game-dir-relative paths the undeploy step would
	// remove: Installer.Uninstall's own removal set (the cache entry's
	// ListFiles union, falling back to the DB's tracked deployed paths for
	// an absent entry - #260), narrowed to the paths actually present under
	// game.ModPath right now. Empty for a mod that is installed but not
	// currently deployed.
	Files []string `json:"files"`

	// KeepCache echoes UninstallOptions.KeepCache: false means the mod's
	// cache entry is deleted too, so a later reinstall re-downloads.
	KeepCache bool `json:"keep_cache"`

	// Hooks names the uninstall.* hooks that would actually run, in run
	// order. Only configured hooks are listed, and none at all under
	// SkipHooks.
	Hooks []string `json:"hooks"`

	// MergedArtifact is what the post-uninstall merged-pak sync would do to
	// the profile's merged artifact on a DeployCompile game - an effect
	// Files cannot express, since the artifact belongs to the profile
	// rather than to any one mod. Nil when the game does not deploy by
	// compilation, and nil when the sync would leave the artifact exactly
	// as it is (Ruling 8). See mergedArtifactEffectForUninstall.
	MergedArtifact *MergedArtifactEffect `json:"merged_artifact"`

	// snapshot is Ruling 5's precondition: the installed-mod set this plan
	// was computed from, re-derived and compared by ApplyUninstall.
	snapshot installedSnapshot `json:"-"`
}

// PlanUninstall resolves sourceID/modID against profileName's installed mods
// and computes what UninstallMod would then do, without touching anything.
//
// Resolution mirrors the pre-lift cmd/lmm/uninstall.go exactly, including
// both of its error texts: with sourceID set, that source's copy is looked
// up directly ("mod X not found in profile P (source: S)"); with sourceID
// empty, every installed mod is scanned by ID and the FIRST hit wins ("mod X
// not found in profile P"), which is how `lmm uninstall <id>` has always
// disambiguated an ID installed from more than one source. The scan's own
// read failure keeps its pre-lift wording too ("listing installed mods: …").
//
// The returned plan is a snapshot: pass it to ApplyUninstall promptly, and
// be ready for ErrStalePlan if the installed set moved underneath it.
func (s *Service) PlanUninstall(ctx context.Context, game *domain.Game, profileName, sourceID, modID string, opts UninstallOptions) (*UninstallPlan, error) {
	// Resolution runs FIRST, in the pre-lift order, so a failing DB still
	// produces the historical message rather than the snapshot read's.
	var mod *domain.InstalledMod
	var installed []domain.InstalledMod
	if sourceID != "" {
		found, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
		if err != nil {
			return nil, fmt.Errorf("mod %s not found in profile %s (source: %s)", modID, profileName, sourceID)
		}
		mod = found
		if installed, err = s.GetInstalledMods(ctx, game.ID, profileName); err != nil {
			return nil, fmt.Errorf("listing installed mods: %w", err)
		}
	} else {
		all, err := s.GetInstalledMods(ctx, game.ID, profileName)
		if err != nil {
			return nil, fmt.Errorf("listing installed mods: %w", err)
		}
		installed = all
		for i := range all {
			if all[i].ID == modID {
				mod = &all[i]
				break
			}
		}
		if mod == nil {
			return nil, fmt.Errorf("mod %s not found in profile %s", modID, profileName)
		}
	}

	plan := &UninstallPlan{
		Mod:            *mod,
		KeepCache:      opts.KeepCache,
		Hooks:          uninstallHookNames(s.resolvedHooksForPlan(ctx, game, profileName), opts.SkipHooks),
		MergedArtifact: s.mergedArtifactEffectForUninstall(ctx, game, profileName, mod),
		snapshot:       snapshotOf(installed),
	}
	for _, f := range s.deployedPathsFor(ctx, game, profileName, mod) {
		if isDeployedNow(game, f) {
			plan.Files = append(plan.Files, f)
		}
	}
	return plan, nil
}

// ApplyUninstall carries out plan under the mutation lock. Ruling 5: the
// plan's recorded installed-mod set is re-derived first and a mismatch is
// refused with ErrStalePlan rather than applied.
//
// The plan supplies the target (profile, source and ID) and the freshness
// precondition; everything else comes from opts, so a frontend that showed
// the plan and then let the user toggle --keep-cache still applies what the
// user finally chose.
func (s *Service) ApplyUninstall(ctx context.Context, game *domain.Game, plan *UninstallPlan, opts UninstallOptions) (*UninstallResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if plan == nil {
		return nil, errors.New("uninstall plan is nil: call PlanUninstall first")
	}
	if err := s.checkPlanFresh(ctx, game.ID, plan.Mod.ProfileName, plan.snapshot); err != nil {
		return nil, err
	}
	return s.uninstallMod(ctx, game, plan.Mod.ProfileName, plan.Mod.SourceID, plan.Mod.ID, opts)
}

// UninstallOptions configures UninstallMod.
type UninstallOptions struct {
	KeepCache bool // --keep-cache: skip deleting the mod's cache entry

	// Hook plumbing: UninstallMod resolves the game/profile hooks and a
	// HookRunner itself (Service.resolvedHooks/hookRunner, hooks_resolve.go)
	// rather than taking them from the caller. SkipHooks (the CLI's
	// --no-hooks) still skips execution entirely even though the hooks were
	// resolved.
	Force     bool // continue past a failing uninstall.before_* hook (warn instead of fail)
	SkipHooks bool // run no hooks even when hooks are configured (the CLI's --no-hooks)

	// No verbosity concept lives here: core never gates or prints
	// diagnostics. UninstallResult.Notes and .Warnings are always fully
	// populated; it is the caller's job to decide what to display and
	// under what conditions. See UninstallResult's doc comment.
}

// UninstallResult reports the outcome of UninstallMod. Every entry in both
// slices below is always recorded — UninstallMod has no verbosity concept —
// but the two slices carry different display contracts for callers to honor
// (this is the convention Tasks 3-4 should follow too):
//
//   - Warnings holds diagnostics the pre-extraction CLI printed
//     unconditionally to stderr regardless of --verbose (hook failures:
//     uninstall.before_* when Force is set, and uninstall.after_*, which is
//     always non-fatal). Callers should print each entry to stderr,
//     unconditionally, e.g. `fmt.Fprintf(os.Stderr, "Warning: %v\n", w)`.
//   - Notes holds operational diagnostics the pre-extraction CLI only
//     printed under --verbose (undeploy failure, cache-delete failure, and
//     a failure to remove the mod from the profile). Each entry already
//     carries its historical prefix word baked into the text ("Warning: "
//     for undeploy/cache-delete, "Note: " for the profile-removal message,
//     matching the pre-extraction CLI's exact wording for each), so a
//     caller that wants byte-identical pre-extraction output should print
//     each entry to stdout ONLY under --verbose, verbatim, e.g.
//     `fmt.Printf("  %s\n", n)`.
//
// On error, the returned result carries any diagnostics accumulated before
// the failure; callers should surface them alongside the error.
type UninstallResult struct {
	Warnings []string `json:"warnings,omitempty"` // unconditional, stderr, audience: operator/always-visible
	Notes    []string `json:"notes,omitempty"`    // --verbose-gated, stdout, audience: diagnostic detail
}

// UninstallMod removes a mod from the profile: runs uninstall hooks,
// undeploys files, deletes the cache entry (unless KeepCache), removes the
// DB row, and removes the mod from the profile YAML.
//
// Hook failure semantics (matching the pre-extraction CLI's doUninstall):
//   - uninstall.before_all / uninstall.before_each: a failure aborts the
//     operation with an error, unless Force is set, in which case it is
//     recorded in Warnings and the uninstall proceeds.
//   - uninstall.after_each / uninstall.after_all: always non-fatal; a
//     failure is recorded in Warnings after every other step has already
//     committed.
//
// Undeploy failures, cache-delete failures, and a failure to remove the mod
// from the profile (e.g. the DB and profile have drifted out of sync) are
// all non-fatal and always recorded in Notes; the operation still
// completes. See UninstallResult's doc comment for the Warnings/Notes
// display contract.
//
// It is PlanUninstall + ApplyUninstall in one call, under a single mutation
// slot, for callers with nothing to show between the two (core's own tests);
// it takes the resolved sourceID directly rather than re-running the plan's
// bare-ID disambiguation. Frontends plan first, render, then apply.
//
// Convenience = PlanUninstall + ApplyUninstall; kept exported for core tests
// and for frontends that want one call, even though it has no non-test
// caller today (Task 25 review Important #1; ruling recorded in the
// 2026-08-29 decisions-log row of docs/plans/2026-08-27-v2-core-refactor-design.md).
// No freshness check: with no plan, there is nothing for ApplyUninstall's
// checkPlanFresh to compare against, so a profile mutated between resolution
// and this call is not caught the way the Plan+Apply pair catches it
// (Task 25 review Minor #5).
func (s *Service) UninstallMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string, opts UninstallOptions) (*UninstallResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.uninstallMod(ctx, game, profileName, sourceID, modID, opts)
}

func (s *Service) uninstallMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string, opts UninstallOptions) (*UninstallResult, error) {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mod %s: %w", modID, err)
	}

	result := &UninstallResult{}

	hooks, err := s.resolvedHooks(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	runner, err := s.hookRunner(ctx)
	if err != nil {
		return result, err
	}
	hookCtx := hookContextFor(game)

	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "uninstall.before_all", hooks.GetUninstallBeforeAll()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("uninstall.before_all hook failed: %w", err)
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("uninstall.before_all hook failed (forced): %v", err))
	}

	hookCtx.ModID = mod.ID
	hookCtx.ModName = mod.Name
	hookCtx.ModVersion = mod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "uninstall.before_each", hooks.GetUninstallBeforeEach()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("uninstall.before_each hook failed: %w", err)
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("uninstall.before_each hook failed (forced): %v", err))
	}

	installer, err := s.getInstallerForProfile(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
		// Non-fatal - files may have been manually removed. Always
		// recorded; the historical "Warning: " prefix is baked into the
		// text itself (see UninstallResult's doc comment).
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: failed to undeploy some files: %v", err))
	}

	if !opts.KeepCache {
		if err := s.GetGameCache(game).Delete(game.ID, mod.SourceID, modID, mod.Version); err != nil {
			result.Notes = append(result.Notes, fmt.Sprintf("Warning: failed to clean cache: %v", err))
		}
	}

	if err := s.deleteInstalledMod(ctx, mod.SourceID, modID, game.ID, profileName); err != nil {
		return result, fmt.Errorf("failed to remove mod record: %w", err)
	}

	if err := s.NewProfileManager().RemoveMod(ctx, game.ID, profileName, mod.SourceID, modID); err != nil {
		// Don't fail if not in profile. Always recorded, historical "Note: "
		// prefix baked into the text (see UninstallResult's doc comment).
		result.Notes = append(result.Notes, fmt.Sprintf("Note: %v", err))
	}

	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "uninstall.after_each", hooks.GetUninstallAfterEach()); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("uninstall.after_each hook failed: %v", err))
	}

	hookCtx.ModID = ""
	hookCtx.ModName = ""
	hookCtx.ModVersion = ""
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "uninstall.after_all", hooks.GetUninstallAfterAll()); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("uninstall.after_all hook failed: %v", err))
	}

	// #197 postsmoke fix: UninstallResult.Warnings (unconditional stderr)
	// already exists for exactly this - Notes is --verbose-gated
	// (printUninstallDiagnostics), so a sync failure here used to be
	// silent by default.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not sync merged pak: %v", syncErr))
	} else {
		result.Warnings = append(result.Warnings, syncWarnings...)
	}

	return result, nil
}
