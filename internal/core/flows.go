package core

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// EnableResult reports the outcome of EnableMod. Changed is true iff the
// mod was actually deployed and flipped to enabled — false (not an error)
// when it was already enabled, mirroring EnableMod's pre-Task-6 (bool,
// error) return. Notes carries operational diagnostics using the same
// display-contract convention as UninstallResult/DeployResult (Task 2's
// convention, extended here in Task 6 item a for result-struct
// convergence, and by #183's SetModDeployed note below): a caller wanting
// byte-identical pre-5a output should print each entry to stdout ONLY
// under --verbose, verbatim, e.g. `fmt.Printf("  %s\n", n)`.
type EnableResult struct {
	Changed bool     `json:"changed"`
	Notes   []string `json:"notes,omitempty"`
	// Warnings holds diagnostics that must reach the user unconditionally
	// (#197 postsmoke fix), unlike Notes' --verbose-only display contract -
	// today, only a merged-pak sync failure. A silent sync failure here is
	// exactly the class of bug the postsmoke fix-wave exists to close: the
	// mod's Enabled bit flips, but the game directory may not actually
	// reflect it.
	Warnings []string `json:"warnings,omitempty"`
}

// DisableResult reports the outcome of DisableMod. Changed mirrors
// EnableResult.Changed. Notes carries the diagnostics DisableMod can
// produce — a non-fatal undeploy failure (see DisableMod's doc comment) and
// (#183) a non-fatal SetModDeployed failure — using the same
// historical-prefix-baked-into-the-text convention UninstallResult's doc
// comment documents: a caller wanting byte-identical pre-5a output should
// print each entry to stdout ONLY under --verbose, verbatim, e.g.
// `fmt.Printf("  %s\n", n)`.
type DisableResult struct {
	Changed bool     `json:"changed"`
	Notes   []string `json:"notes,omitempty"`
	// Warnings mirrors EnableResult.Warnings' identical rationale
	// (#197 postsmoke fix): unconditional display, unlike Notes.
	Warnings []string `json:"warnings,omitempty"`
}

// EnableMod deploys an installed-but-disabled mod's files from the cache to
// the game directory and marks it enabled (and deployed, #183) in the
// database. Returns a result with Changed false — not an error — if the
// mod was already enabled.
//
// A SetModDeployed failure is non-fatal (recorded in Notes) — mirroring
// both DisableMod's own treatment of the identical call and
// DeployProfile's/PurgeProfile's existing SetModDeployed call sites: the
// files are already live on disk at this point, and refusing to record the
// user's intent to enable the mod over a secondary bookkeeping-write
// failure would leave it stuck exactly like the undeploy-failure case
// DisableMod already accepts. SetModEnabled's own failure, in contrast,
// stays fatal (pre-existing behavior, unchanged) — it is the write that
// makes "the mod is enabled" true at all, unlike the deployed flag, which
// is a cache of already-true, already-observable state.
func (s *Service) EnableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*EnableResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.enableMod(ctx, game, profileName, sourceID, modID)
}

func (s *Service) enableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*EnableResult, error) {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mod %s: %w", modID, err)
	}

	if mod.Enabled {
		return &EnableResult{}, nil
	}

	if !s.GetGameCache(game).Exists(game.ID, sourceID, modID, mod.Version) {
		return nil, fmt.Errorf("mod not found in cache - try reinstalling with 'lmm install --id %s'", modID)
	}

	installer, err := s.getInstallerForProfile(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	if err := installer.Install(ctx, game, &mod.Mod, profileName); err != nil {
		return nil, fmt.Errorf("failed to deploy mod: %w", err)
	}

	result := &EnableResult{}
	if err := s.setModDeployed(ctx, sourceID, modID, game.ID, profileName, true); err != nil {
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: could not mark as deployed: %v", err))
	}

	if err := s.setModEnabled(ctx, sourceID, modID, game.ID, profileName, true); err != nil {
		return result, fmt.Errorf("failed to update mod status: %w", err)
	}

	// #197 postsmoke fix: Warnings, not Notes - Notes is --verbose-gated in
	// the CLI (printModNotes), so a sync failure here used to be silent by
	// default.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not sync merged pak: %v", syncErr))
	} else {
		result.Warnings = append(result.Warnings, syncWarnings...)
	}

	result.Changed = true
	return result, nil
}

// DisableMod undeploys the mod's files from the game directory — the cache
// entry is kept so the mod can be re-enabled later without downloading again
// — and marks it disabled (and not-deployed, #183) in the database. Returns
// a result with Changed false — not an error — if the mod was already
// disabled. That already-disabled path still self-heals a stale
// deployed=true left behind by a pre-#183 disable (or any other drift):
// it clears the flag, non-fatally, before returning, so calling disable
// again converges deployed state even when enabled was already false.
//
// Undeploy failures are treated as non-fatal: the game files may already
// have been removed manually, and refusing to record the user's intent to
// disable the mod would leave it stuck. This mirrors the pre-extraction CLI,
// which warned (under --verbose) but always continued to flip the DB state
// — DisableResult.Notes (Task 6 item a) restores that diagnostic for
// callers that want it, rather than discarding it as the (bool, error)
// signature this replaces was forced to.
//
// A SetModDeployed failure gets the identical non-fatal treatment (#183),
// for the identical reason and matching DeployProfile's/PurgeProfile's own
// SetModDeployed call sites: it is attempted unconditionally, even when the
// undeploy above already failed, because the deployed flag should reflect
// "disable was requested" regardless of whether the file-level undeploy
// itself succeeded — an undeploy failure already means the flag may not
// match reality either way, and the alternative (skipping the write
// because undeploy failed) would leave the mod stuck reporting DEPLOYED
// forever, which is #183 itself. SetModEnabled's own failure stays fatal,
// unchanged: it is the write that makes "the mod is disabled" true at all,
// not a cache of already-true state.
func (s *Service) DisableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*DisableResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.disableMod(ctx, game, profileName, sourceID, modID)
}

func (s *Service) disableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*DisableResult, error) {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mod %s: %w", modID, err)
	}

	if !mod.Enabled {
		// Self-heal (#183): a mod disabled before this fix shipped can be
		// stuck with enabled=false but deployed=true forever, since nothing
		// else clears the flag once the mod is already disabled. Clear it
		// here too, under the same non-fatal Note convention as the
		// already-enabled path below, so disable converges the flag even
		// when called on an already-disabled mod.
		result := &DisableResult{}
		if mod.Deployed {
			if err := s.setModDeployed(ctx, sourceID, modID, game.ID, profileName, false); err != nil {
				result.Notes = append(result.Notes, fmt.Sprintf("Warning: could not mark as not deployed: %v", err))
			}
		}
		return result, nil
	}

	result := &DisableResult{}
	installer, err := s.getInstallerForProfile(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
		// Non-fatal — see doc comment. Historical "Warning: " prefix baked
		// into the text itself, matching UninstallResult's own convention.
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: failed to undeploy some files: %v", err))
	}

	if err := s.setModDeployed(ctx, sourceID, modID, game.ID, profileName, false); err != nil {
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: could not mark as not deployed: %v", err))
	}

	if err := s.setModEnabled(ctx, sourceID, modID, game.ID, profileName, false); err != nil {
		return result, fmt.Errorf("failed to update mod status: %w", err)
	}

	// #197 postsmoke fix: Warnings, not Notes (see EnableMod's identical fix).
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not sync merged pak: %v", syncErr))
	} else {
		result.Warnings = append(result.Warnings, syncWarnings...)
	}

	result.Changed = true
	return result, nil
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

	if err := s.NewProfileManager().RemoveMod(game.ID, profileName, mod.SourceID, modID); err != nil {
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

// runHook runs command (a hook script path) via runner if both are set,
// updating hookCtx.HookName first. No-op if skip is true, or runner is nil,
// or command is empty (nil Hooks/HookRunner or SkipHooks, or that particular
// hook isn't configured). Shared by UninstallMod and DeployProfile -
// hookName ("install.before_all", "uninstall.after_each", ...) is just a
// label passed through to the script environment, so one helper covers both
// hook namespaces.
func runHook(ctx context.Context, skip bool, runner *HookRunner, hookCtx *HookContext, hookName, command string) error {
	if skip {
		return nil
	}
	if runner == nil || command == "" {
		return nil
	}
	hookCtx.HookName = hookName
	_, err := runner.Run(ctx, command, *hookCtx)
	return err
}

// DeployOptions configures DeployProfile.
type DeployOptions struct {
	Purge bool // --purge: undeploy every installed mod (regardless of ModID/All) before deploying, remembering which were enabled beforehand for the profile-wide selection below.

	// LinkMethod overrides the link method used for this deploy (--method).
	// nil (the zero value) means "use the profile's effective link method" via
	// Service.GetEffectiveLinkMethod. A pointer is used, rather than a bare
	// domain.LinkMethod with its zero value as the "unset" sentinel, because
	// domain.LinkMethod's zero value (LinkSymlink) is itself a valid,
	// explicit choice - it cannot double as "no override" without losing
	// the ability to explicitly request symlink. See the task report.
	LinkMethod *domain.LinkMethod

	// ModID/SourceID restrict the deploy to a single mod (`lmm deploy
	// <mod-id>`). Both empty (the default) deploys every mod in profile
	// order, subject to All. SourceID selects which source's copy of ModID
	// to deploy - the CLI's --source flag, resolved dynamically since
	// v1.22.0 (sole configured source, or an interactive prompt).
	ModID    string
	SourceID string

	All bool // --all: include disabled mods in a full-profile deploy, or allow deploying a disabled ModID.

	// Hook plumbing, mirroring UninstallOptions: DeployProfile resolves the
	// game/profile hooks and a HookRunner itself. The deploy pass runs
	// install.* hooks; the purge pass (when Purge is set) runs uninstall.*
	// hooks, matching the pre-extraction CLI's doDeploy/purgeDeployedMods
	// split.
	Force     bool // continue past a failing before_* hook (warn instead of fail)
	SkipHooks bool // run no hooks even when hooks are configured (the CLI's --no-hooks)
}

// DeployPhase identifies what DeployProfile is doing for the mod named in
// a flow event (or, for DeployPurging, for the purge pass as a whole),
// letting callers render phase-appropriate UI without needing to know how
// a deploy is actually carried out.
type DeployPhase int

const (
	// DeployPurging fires once, before any purge-phase mod is touched -
	// from a deploy --purge pass or from PurgeProfile (#61) - when there
	// is at least one installed mod to purge. Total is the number of mods
	// being purged; Index and ModName are zero/empty.
	DeployPurging DeployPhase = iota
	// DeployBeforeEachSkipped: install.before_each failed for ModName: the
	// mod is skipped (added to DeployResult.Skipped). Detail is the reason.
	DeployBeforeEachSkipped
	// DeployRedownloading: ModName's cache entry is missing; DeployProfile
	// is re-fetching it from source.
	DeployRedownloading
	// DeployDownloading: a file for ModName is downloading. Percent is the
	// 0-100 completion (only reported once the source declares a total
	// size, matching the pre-extraction CLI's progress callback gating).
	DeployDownloading
	// DeployDownloadFailed: a file for ModName failed to download; the mod
	// is skipped. Detail is the reason.
	DeployDownloadFailed
	// DeployDownloadDone fires once, after a cache-miss mod's redownload
	// loop finishes without error, mirroring the pre-extraction CLI's
	// unconditional `fmt.Println() // Clear progress line` immediately
	// after the download loop (git show b2ad559:cmd/lmm/deploy.go) - it
	// terminates DeployDownloading's carriage-returned progress line with a
	// real newline before the mod's own DeployDeployed line prints. Unlike
	// its ApplyProfileSwitch analog (SwitchDownloadDone), which fires on
	// both success and failure since doProfileSwitch's equivalent Println
	// sat unconditionally after its own loop, redeployFromSource's failure
	// path returns immediately via a DeployDownloadFailed event instead (see
	// below) without reaching this point - so this phase covers the
	// success path only.
	DeployDownloadDone
	// DeploySkipped: ModName was skipped for a reason other than a hook or
	// download failure (fetch failure, no files available, file-selection
	// failure, or an outright deploy/install failure). Detail is the reason.
	DeploySkipped
	// DeployDeployed: ModName was (re)deployed successfully.
	DeployDeployed

	// --- Fix wave 1: every remaining Warnings/Notes diagnostic gets an
	// event at its exact point of occurrence, restoring the pre-extraction
	// CLI's console positioning (see DeployResult's doc comment for the
	// full Warnings/Notes -> event mapping and task-3-report.md's "Fix
	// wave 1" entry for the review findings these fix). ---

	// DeployBeforeAllForced fires once, immediately, when install.before_all
	// (a deploy) or uninstall.before_all (a --purge pass) fails and Force is
	// set: the pre-extraction CLI printed this warning as the very first
	// line of output, before anything else (the "Purging..."/"Deploying..."
	// header included) - so this event always precedes DeployPurging and
	// any other event. No mod is in scope (Index/Total/ModName/ModID are
	// zero); Detail matches the DeployResult.Warnings entry verbatim.
	DeployBeforeAllForced
	// DeployNote fires wherever DeployProfile appends an entry to
	// DeployResult.Notes for a specific mod during the main deploy loop
	// (a failed undeploy-before-redeploy, a failed SetModLinkMethod, or a
	// failed SetModDeployed), at the exact point it happens - always
	// before that same mod's own DeployDeployed event, matching the
	// pre-extraction CLI's inline ordering. ModName/ModID identify the
	// mod; for the latter two diagnostics, whose historical text carries
	// no mod identity at all, the event's ModName/ModID are the ONLY way
	// to attribute the diagnostic to a mod.
	DeployNote
	// DeployWarning fires wherever DeployProfile appends an entry to
	// DeployResult.Warnings other than a DeployBeforeAllForced one: a
	// failed install.after_each hook (ModName/ModID set), a failed
	// install.after_all hook, or a failed ApplyProfileOverrides (neither
	// has a mod in scope). The pre-extraction CLI printed the overrides
	// warning immediately once computed, then its batched hook warnings
	// (after_each in mod order, then after_all) right after - so
	// DeployProfile emits the overrides DeployWarning (if any) first, then
	// the after_each/after_all ones, reproducing that print order without
	// changing when each check actually runs (see DeployProfile's body).
	DeployWarning
	// PurgeWarning fires wherever a purge appends an entry to its
	// result's Warnings (DeployResult for deploy --purge, PurgeResult for
	// PurgeProfile): a skipped uninstall.before_each mod (deploy mode
	// only - PurgeProfile reports that skip as PurgeModSkipped instead;
	// fires inline, per mod, as it happens), or a failed
	// uninstall.after_each/after_all hook (fires after the whole purge
	// loop has finished, in mod order then after_all - mirroring the
	// pre-extraction CLIs, which accumulated these and printed them
	// together, after every per-mod line, via printHookWarnings).
	PurgeWarning
	// PurgeNote fires wherever a purge appends a per-mod entry to its
	// result's Notes (a failed undeploy, a failed SetModDeployed(false),
	// or PurgeProfile --uninstall's record-delete/profile-remove
	// failures), inline, immediately after that operation - mirroring the
	// pre-extraction CLIs' --verbose-gated "⚠ "/"Note: " lines.
	PurgeNote
	// PurgeComplete fires once, after a non-empty purge has finished
	// everything (including its own hook warnings) - before DeployProfile
	// moves on to gathering mods to deploy, or as PurgeProfile's terminal
	// event. It carries no data; a deploy --purge caller wanting
	// byte-identical pre-extraction output prints exactly one blank line
	// here - purgeDeployedMods's own final `fmt.Println()`, which the
	// initial extraction had misplaced immediately after the purge header
	// instead of at the end of the purge phase (`lmm purge` prints
	// nothing for it).
	PurgeComplete

	// --- Task 4: ApplyProfileSwitch progress events, extending this same
	// DeployPhase enum (per the task brief: "reuse the progress carrier and
	// its phase-constant pattern - extend, don't fork") rather than
	// introducing a parallel SwitchProgress/SwitchPhase pair.
	//
	// v2 Phase 2 Unit J (#290): ApplyProfileApply emits this same family.
	// Every line doProfileApply printed is worded identically to its
	// doProfileSwitch counterpart, so a duplicate ProfileApply* family
	// would be twelve constants of copied text; Scope.Op (OpProfileApply
	// vs OpSwitch) is what tells the two flows apart on the wire. Changing
	// any wording below therefore changes BOTH flows - which is correct:
	// they are the same lines.
	// ApplyProfileSwitch is a behavior-preserving extraction of
	// cmd/lmm/profile.go's doProfileSwitch;
	// every phase below corresponds to exactly one of doProfileSwitch's
	// fmt.Print* call sites - see the task report for the full mapping.
	// Unlike DeployProfile, doProfileSwitch never printed to stderr at all,
	// so none of these have a Warnings-bucket counterpart: every
	// SwitchResult diagnostic below is a Note (--verbose-gated stdout). ---

	// SwitchDisableNote fires for each of the disable loop's two possible
	// per-mod diagnostics (a failed Uninstall, then a failed SetModEnabled),
	// mirroring doProfileSwitch's "  Warning: failed to undeploy %s: %v" /
	// "  Warning: failed to update %s: %v" - both --verbose-gated stdout
	// prints. Detail carries the historical "Warning: " prefix baked in; a
	// caller wanting byte-identical output prints
	// `if verbose { fmt.Printf("  %s\n", p.Detail) }`.
	SwitchDisableNote
	// SwitchDisabled fires once a mod's disable step has finished
	// (regardless of whether SwitchDisableNote fired for it) -
	// doProfileSwitch always disables the DB row and always prints
	// "  ✓ Disabled: %s" even when the undeploy/DB update above it failed.
	// ModName is set.
	SwitchDisabled
	// SwitchEnableNote mirrors SwitchDisableNote for the enable loop's two
	// diagnostics (a failed Install, then a failed SetModEnabled). Unlike
	// the disable loop, a failed Install is fatal FOR THAT MOD ONLY: the mod
	// is skipped (no SwitchEnabled event follows) - see doProfileSwitch's
	// `continue` after the Install failure branch.
	SwitchEnableNote
	// SwitchEnabled fires once a mod has been successfully deployed (and
	// enabled, or deployed but its SetModEnabled bookkeeping failed - see
	// SwitchEnableNote), mirroring "  ✓ Enabled: %s".
	SwitchEnabled
	// SwitchInstalling fires once, before the install loop, only when there
	// is at least one mod to install (Total = len(SwitchPlan.ToInstall)),
	// mirroring doProfileSwitch's "\nInstalling missing mods...".
	SwitchInstalling
	// SwitchInstallingMod fires once per mod to install, before it is even
	// fetched - SourceID/ModID are the only identity available at this
	// point, mirroring "  Installing %s:%s...".
	SwitchInstallingMod
	// SwitchInstallError fires for any of the install loop's mod-fatal-only
	// failure reasons (fetch, get-files, no-files, file-selection, deploy,
	// or save), each already worded to match its historical text exactly
	// (Detail is printed verbatim as "    Error: %s"). Unlike
	// DeployProfile's DeploySkipped, these are NOT accumulated into any
	// SwitchResult slice - doProfileSwitch never printed a final
	// skipped-count summary for profile switch, so there is nothing to
	// accumulate beyond the live event.
	SwitchInstallError
	// SwitchDownloading mirrors DeployDownloading for the install loop's
	// download progress (Percent set, gated the same way: only once the
	// source declares a total size).
	SwitchDownloading
	// SwitchDownloadFailed fires when a file download fails; Detail is
	// "download failed: %v". A caller wanting byte-identical output prints a
	// blank line then "    Error: %s" with Detail - see SwitchDownloadDone's
	// doc comment for why the blank line isn't included here.
	SwitchDownloadFailed
	// SwitchDownloadDone fires once per install-loop mod after its download
	// loop finishes, on both success and failure - doProfileSwitch's
	// `fmt.Println()` after the loop runs unconditionally either way. When
	// the #96 cache-first guard skips the download entirely, this phase is
	// skipped with it (there is no download readout to terminate). A
	// caller wanting byte-identical output prints a bare blank line here;
	// combined with SwitchDownloadFailed's own leading blank line, a failed
	// download reproduces the original's blank/error/blank sequence, and a
	// successful one reproduces its single trailing blank line.
	SwitchDownloadDone
	// SwitchInstalled fires once a to-be-installed mod has been fetched,
	// downloaded, deployed, and saved to the DB, mirroring "    ✓ Installed:
	// %s". ModName is set (mod.Name, now known).
	SwitchInstalled
	// SwitchInstallNote fires when UpsertMod (recording the profile's
	// FileIDs) fails after a successful install - the sole --verbose-gated
	// diagnostic in the install loop, mirroring "    Warning: could not
	// update profile: %v" (4-space indent, one level deeper than
	// SwitchDisableNote/SwitchEnableNote's 2-space Notes).
	SwitchInstallNote

	// --- Phase 5b Task 2: ApplyInstall progress events, restored to
	// byte-for-byte per-path fidelity in Fix wave 1 (see
	// task-2-report.md's "Fix wave 1 (dep-path fidelity)" entry for the full
	// review trace). ApplyInstall reproduces the pre-extraction CLI's own
	// TWO divergent execution engines EXACTLY, gated on
	// len(plan.Dependencies):
	//
	//   - Empty (the STRICT/no-deps path): the primary uses doInstall's own
	//     single-mod code unchanged from Task 2 - Force-gated
	//     before_all/before_each, Install-or-Replace (incl. the
	//     reinstall-cache-transaction for a same-version reinstall),
	//     interactive/--file file selection and the blocking
	//     conflict-confirm prompt are the CALLER's job (plan.Files/
	//     plan.Conflicts), SaveFileChecksum, --skip-verify. See
	//     InstallDownload*/InstallChecksumComputed/InstallExtracting/
	//     InstallDeploying/InstallDone below.
	//   - Non-empty (the BATCH path): EVERY mod in [Dependencies...,
	//     primary] uses batchInstallMods' lenient mechanics IDENTICALLY -
	//     the primary is NOT special-cased at all here, matching the
	//     pre-extraction CLI's own behavior of delegating the WHOLE list,
	//     target included, to batchInstallMods whenever there were
	//     dependencies to install (doInstall's "if len(modsToInstall) > 1"
	//     early return, before any single-mod code - including file
	//     selection and the conflict prompt - ever ran). before_each is
	//     NEVER Force-gated (a failure always just skips that one mod and
	//     continues, primary included), no Replace path (always a fresh
	//     Install; a same-key existing mod is uninstalled+cache-deleted
	//     first), no interactive file selection (always the
	//     primary-or-first file, re-resolved per mod - plan.Files is never
	//     consulted), conflicts are a non-blocking inline warning (never a
	//     prompt). See InstallDepInstalling below onward.
	InstallBeforeAllForced

	// InstallBeforeEachForced fires when the PRIMARY mod's install.before_each
	// hook fails and Force is set (a forced warning, not a fatal error) -
	// mirrors doInstall's own before_each Force-gate exactly. ModName/ModID
	// identify the primary. ONLY fires in the STRICT (no-deps) path - in the
	// BATCH path the primary's before_each is never Force-gated at all (see
	// InstallDepSkipped), matching batchInstallMods exactly.
	InstallBeforeEachForced

	// InstallDepInstalling fires once per mod in the BATCH path's combined
	// [Dependencies..., primary] list - dependency OR primary alike -
	// before before_each even runs, mirroring batchInstallMods' own
	// "\n[%d/%d] Installing: %s v%s\n" byte-for-byte (Fix wave 1 restored
	// the exact text and the primary's participation; Task 2's original
	// design fired this for dependencies only, with different wording -
	// see task-2-report.md). Index/Total count across the WHOLE combined
	// list (len(plan.Dependencies)+1), matching batchInstallMods' shared
	// counter; ModVersion carries the version for the restored "v%s" text.
	InstallDepInstalling
	// InstallDepReinstalling fires, unconditionally (not verbose-gated),
	// when a BATCH-path mod (dependency or primary) already has an existing
	// installed row for (SourceID, ID, Profile) - mirroring
	// batchInstallMods' unconditional "  Removing previous installation...".
	// The existing install is then uninstalled and its cache entry deleted
	// - never a Replace/reinstall-cache-transaction (that mechanism is
	// STRICT-path only).
	InstallDepReinstalling
	// InstallDepFileSelected fires once a BATCH-path mod's downloadable
	// files have been fetched, filtered/sorted, and reduced to the
	// primary-or-first file (never interactive, never --file) - mirroring
	// batchInstallMods' "  File: %s\n". File identifies which, for the
	// CLI's own displayFileLabel call.
	InstallDepFileSelected
	// InstallDepDownloading mirrors batchInstallMods' per-mod download
	// progress readout (Percent only, gated on a known total size - no
	// byte-count fallback line, unlike the STRICT path's
	// InstallDownloading). Fires for a dependency OR the primary alike.
	InstallDepDownloading
	// InstallDepSkipped fires whenever ANY BATCH-path mod (dependency or
	// primary alike) is skipped for any reason (hook failure, fetch/files/
	// download/deploy/save failure) - unconditional, never Force-gated,
	// matching batchInstallMods exactly. Detail already carries the
	// restored, failure-type-specific, fully-prefixed line text verbatim
	// ("Skipped: install.before_each hook failed: %v" for a hook failure;
	// "Error: <reason>" for every other failure type - batchInstallMods
	// used different wording per failure type, never a uniform "Skipped:
	// <name>: <reason>" - see task-2-report.md's Fix wave 1 for the
	// before/after); a caller wanting byte-identical output prints
	// `fmt.Printf("  %s\n", p.Detail)`. Index/Total count across the whole
	// combined list, matching InstallDepInstalling.
	InstallDepSkipped
	// InstallDepDownloadDone fires, unconditionally (success OR failure
	// alike), immediately after a BATCH-path mod's DownloadMod call
	// returns - mirroring batchInstallMods' unconditional `fmt.Println()`
	// right after the download call, which precedes InstallDepSkipped's
	// own restored "\n  Error: download failed: %v\n" leading blank line
	// on failure. A caller wanting byte-identical output prints a bare
	// `fmt.Println()` here.
	InstallDepDownloadDone
	// InstallDepConflictWarning fires when a BATCH-path mod's files
	// (already downloaded/cached at this point) would overwrite files from
	// another installed mod and Force is NOT set - a non-blocking,
	// informational warning only (batchInstallMods never prompts in the
	// BATCH path, primary included - the blocking plan.Conflicts prompt is
	// STRICT-path only). Detail is "%d file conflict(s) - will overwrite".
	InstallDepConflictWarning
	// InstallDepInstalled fires once a BATCH-path mod (dependency or
	// primary) has been fully installed (downloaded, deployed, saved,
	// profile-upserted) - mirroring batchInstallMods' restored
	// "  ✓ Installed (%d files)\n" (Fix wave 1: Task 2's original design
	// used the mod's name instead of its file count - see
	// task-2-report.md). FilesExtracted carries the count.
	InstallDepInstalled

	// InstallDownloadStarted fires once per one of the PRIMARY's selected
	// files (plan.Files) in the STRICT (no-deps) path only, before it
	// begins downloading - mirrors downloadSelectedFiles'
	// "\n[%d/%d] Downloading %s...\n" (or, for a single file,
	// "\nDownloading %s...\n"). File identifies which (for the CLI's own
	// displayFileLabel call); Index/Total count among plan.Files. The BATCH
	// path has no equivalent "starting" event - its download progress
	// begins directly at InstallDepDownloading.
	InstallDownloadStarted
	// InstallDownloading mirrors the STRICT path's primary per-tick
	// download progress - Downloaded/TotalBytes/Percent carry the raw
	// numbers so the CLI can reproduce its exact byte-count/percent
	// readout (see DownloadEvent's doc comment on those fields). The
	// BATCH path's per-mod download progress fires InstallDepDownloading
	// instead (Percent only, no byte-count fallback).
	InstallDownloading
	// InstallDownloadDone fires once a STRICT-path file's download attempt
	// finishes - success OR failure alike, mirroring downloadSelectedFiles'
	// `fmt.Println()` that runs unconditionally right after the download
	// call returns, before branching on its error. The BATCH path's
	// equivalent is InstallDepDownloadDone.
	InstallDownloadDone
	// InstallDownloadFailed fires when a STRICT-path (primary) file
	// download fails; Detail carries "download failed: %v" (the CLI checks
	// Detail for the "third-party downloads" substring itself, mirroring
	// doInstall's own check, to print the manual-install notice using the
	// plan's own Mod.SourceURL/ID - already in the CLI's enclosing scope,
	// so it isn't duplicated onto the event). Always fatal - the BATCH
	// path's equivalent (InstallDepSkipped) never is.
	InstallDownloadFailed
	// InstallChecksumComputed fires once a checksum has been computed and
	// !SkipVerify, for BOTH paths: the STRICT path's primary file(s)
	// (Index/Total/File populated, matching InstallDownloadStarted) and
	// the BATCH path's per-mod checksum (Index/Total/ModName populated
	// instead, File unset - mirroring batchInstallMods' own
	// "  Checksum: %s\n", fired once per mod right after its download
	// succeeds). Detail carries the full (untruncated) checksum either
	// way; the CLI applies its own truncateChecksum.
	InstallChecksumComputed
	// InstallCompiling fires instead of InstallExtracting, once per file,
	// when a DeployCompile game's ".exmodz" file was validated and retained
	// for a later merge (#190 item 1; #197: ingest no longer compiles a
	// per-mod pak - the real merge happens once, batched across the whole
	// profile, via Service.syncMergedPak) - the generic "Extracting to
	// cache..." wording is misleading here either way, since nothing is
	// extracted. File identifies the source file (for displayFileLabel);
	// Detail is unset (there is no per-file compiled output filename left
	// to announce under the merged-only model). The BATCH path never
	// prints this (it has no DeployCompile support and no equivalent
	// status line at all).
	InstallCompiling
	// InstallExtracting mirrors doInstall's unconditional "Extracting to
	// cache..." status line, fired once after the STRICT-path primary's
	// download(s) finish, before Install/Replace - unless every downloaded
	// file was compiled instead (InstallCompiling fires in that case, one
	// event per compiled file, and this is skipped entirely). The BATCH
	// path never prints this (batchInstallMods had no equivalent status
	// line).
	InstallExtracting
	// InstallDeploying mirrors "Deploying to game directory...", fired once
	// right before the STRICT-path primary's Install/Replace. The BATCH
	// path never prints this.
	InstallDeploying
	// InstallDone fires once the STRICT-path primary has been fully
	// installed (deployed, saved, checksum stored, profile upserted). The
	// BATCH path's equivalent (for every mod, primary included) is
	// InstallDepInstalled.
	InstallDone

	// InstallNote fires wherever ApplyInstall appends an entry to
	// InstallResult.Notes (a failed profile-create, UpsertMod,
	// reinstall-cache-transaction commit, old-cache cleanup, or - BATCH
	// path only - a failed Uninstall/cache-Delete while removing a
	// mod's previous installation, see InstallDepReinstalling) - the
	// --verbose-gated stdout bucket, mirroring DeployNote/SwitchInstallNote.
	// Detail equals the Notes entry verbatim; ModName/ModID identify the
	// mod when relevant.
	InstallNote
	// InstallWarning fires wherever ApplyInstall appends an entry to
	// InstallResult.Warnings other than an InstallBeforeAllForced/
	// InstallBeforeEachForced one: a failed SaveFileChecksum (unconditional
	// stderr, matching doInstall exactly - NOT verbose-gated), or an
	// install.after_each/after_all hook failure (deferred - see
	// ApplyInstall's doc comment - emitted after the whole run, mirroring
	// DeployWarning/printHookWarnings' batched timing).
	InstallWarning

	// --- Phase 5b Task 3: ApplyUpdate progress events, extending this same
	// DeployPhase enum (matching Task 2's own "extend, don't fork"
	// precedent). ApplyUpdate is a behavior-preserving extraction of
	// cmd/lmm/update.go's applyUpdate; every phase below corresponds to one
	// of applyUpdate's own console print sites - see the task report for the
	// full mapping. Unlike ApplyInstall, applyUpdate never ran an
	// install.before_all/install.after_all pair at all - each CLI-side
	// update-loop iteration calls applyUpdate once, per mod, with no
	// enclosing before_all/after_all of its own - so there is no
	// UpdateBeforeAllForced counterpart here.

	// UpdateDownloading mirrors applyUpdate's own download-progress readout
	// ("\r  Downloading: %.1f%%", verbose-gated in the pre-extraction CLI) -
	// Percent only, gated on a known total size, matching
	// DeployDownloading/InstallDepDownloading's own gating (no raw
	// byte-count fallback - applyUpdate never printed one).
	UpdateDownloading
	// UpdateDownloadDone fires once, only after EVERY file in the update's
	// download step has downloaded successfully - mirroring applyUpdate's
	// own `if verbose { fmt.Println() }`, which terminates the
	// carriage-returned UpdateDownloading progress line. A download failure
	// returns immediately instead (see ApplyUpdate's doc comment), so -
	// like DeployDownloadDone, and unlike InstallDownloadDone - this covers
	// the success path only. A caller wanting byte-identical pre-extraction
	// output prints this ONLY under --verbose (the historical gate lived on
	// the print itself, not just the progress ticks).
	UpdateDownloadDone
	// UpdateBeforeEachForced fires when EITHER of the update's two
	// Force-gated hooks - uninstall.before_each (old version) or
	// install.before_each (new version) - fails with Force set, mirroring
	// applyUpdate's own two, textually-near-identical (only the hook name
	// differs) "Warning: %s hook failed (forced): %v" unconditional stderr
	// prints. Detail already carries the full, hook-specific message
	// verbatim.
	//
	// Reused, extend-don't-fork (Phase 6b Task 5): ApplyRollback fires this
	// SAME phase for its own two Force-gated before_each hooks -
	// uninstall.before_each (the version being rolled back FROM) and
	// install.before_each (the version being rolled back TO) - mirroring
	// doUpdateRollback's own two near-identical Force checks exactly. The
	// two flows are never in progress at once, so the shared phase carries
	// no ambiguity; Detail alone (plus ModName/ModID) tells a caller which
	// hook and which mod failed.
	UpdateBeforeEachForced
	// UpdateWarning fires for either of the update's two after_each hook
	// failures - uninstall.after_each (old version) or install.after_each
	// (new version) - mirroring applyUpdate's own hookErrors/
	// printHookWarnings pair, fired right after both hooks have run
	// (Replace already succeeded), in hook-run order (uninstall.after_each,
	// then install.after_each) - unlike DeployWarning/InstallWarning's
	// end-of-whole-run deferral, since applyUpdate itself prints these
	// immediately, well before its own DB-update steps below.
	//
	// #143 additionally fires this phase for file-SELECTION warnings (a
	// stored file whose version label left it unresolvable - see
	// updateAmbiguousFileWarning). Those come from a pure decision made
	// BEFORE any download, so unlike the hook failures above they can
	// precede every side effect - the phase no longer implies that Replace
	// or the hooks have run. That early emission is deliberate: the fact is
	// already known, and surfacing it up front means the user sees it even
	// if a later download fails (ApplyUpdate's partial-result convention
	// returns accumulated diagnostics alongside the error either way).
	//
	// Reused (Phase 6b Task 5): ApplyRollback fires this SAME phase for its
	// own two always-non-fatal after_each hooks, in the same
	// uninstall-then-install order, mirroring doUpdateRollback's own
	// hookErrors/printHookWarnings pair exactly.
	UpdateWarning
	// UpdateNote fires when SetModLinkMethod fails after a successful
	// update - the sole --verbose-gated diagnostic in applyUpdate,
	// mirroring "  Warning: could not update link method: %v" (2-space
	// indent, prefix baked into Detail, matching SwitchDisableNote/
	// SwitchEnableNote's own convention).
	//
	// Reused (Phase 6b Task 5): ApplyRollback fires this SAME phase for its
	// own SetModLinkMethod failure, mirroring doUpdateRollback's
	// textually-identical verbose-gated print exactly.
	UpdateNote

	// --- PurgeProfile progress events (#61): the standalone `lmm purge`
	// command's flow, extending this same enum.
	// PurgeProfile also reuses DeployBeforeAllForced, DeployPurging,
	// PurgeNote, PurgeWarning, and PurgeComplete; the two phases below are
	// purge-command-only and NEVER fire during a deploy --purge pass, whose
	// event stream is unchanged. ---

	// PurgeModSkipped fires when a mod's uninstall.before_each hook fails
	// during `lmm purge`: the mod is skipped entirely (stays deployed) and
	// counts toward PurgeResult.Skipped. Index/Total/ModName/ModID are set;
	// Detail carries "uninstall.before_each hook failed: <err>" - the text
	// doPurge printed after "  Skipped <name>: " (the matching Skipped
	// entry is the same Detail behind a "<name>: " prefix). Contrast with
	// deploy --purge, which reports the equivalent skip as a PurgeWarning.
	PurgeModSkipped
	// PurgeModPurged fires when a mod finishes purging - at doPurge's
	// "  ✓ <name>"/succeeded++ point, after that mod's uninstall.after_each
	// attempt. Index/Total/ModName/ModID are set. Note a best-effort
	// undeploy or SetModDeployed failure (PurgeNote) does NOT suppress
	// this; only a before_each skip or an --uninstall record-delete
	// failure does.
	PurgeModPurged

	// --- Phase 6b Task 8: ApplyImport progress events, extending this same
	// DeployPhase enum (matching every prior flow's own "extend, don't
	// fork" precedent). ApplyImport is a behavior-preserving extraction of
	// cmd/lmm/profile.go's doProfileImport; every phase below corresponds to
	// one of its own console print sites - see the task report for the full
	// mapping. Unlike ApplyProfileSwitch's install loop, which gives each
	// failure reason (fetch/get-files/no-files/file-selection/deploy/save) a
	// DISTINCT phase, doProfileImport printed every one of those with the
	// SAME "    Error: %s\n" shape - so ImportModFailed below covers all of
	// them, Detail carrying whichever reason text applies (fidelity forbids
	// sharing ApplyProfileSwitch's own install loop verbatim for exactly
	// this reason - see the task report's sharing-decision entry). ---

	// ImportSaved fires once, immediately after the profile is saved
	// (ProfileManager.ImportWithOptions succeeds), mirroring doProfileImport's
	// "\n✓ Imported profile: %s\n". ModName carries the saved profile's name.
	ImportSaved
	// ImportInstalling fires once, only when the install loop is actually
	// about to run (downloads pending, NoInstall unset, and ConfirmInstall -
	// if any - accepted), mirroring "\nDownloading and installing mods...\n".
	// Total is the number of mods about to be attempted (len(toDownload)).
	ImportInstalling
	// ImportModInstalling fires once per mod in the combined
	// [NeedsRedownload..., Missing...] download list, before it is even
	// fetched - mirroring "  Installing %s:%s...\n". SourceID/ModID are the
	// only identity available at this point (ModName is set once the mod is
	// fetched, for every LATER event concerning this same ref); Index/Total
	// count across the whole combined list, matching ApplyProfileSwitch's
	// SwitchInstallingMod.
	ImportModInstalling
	// ImportDownloading mirrors the per-mod download-progress readout ("\r
	// Downloading: %.1f%%") - Percent only, gated on a known total size,
	// matching every other flow's own gating.
	ImportDownloading
	// ImportDownloadDone fires once per mod whose download loop actually
	// ran (success OR failure alike), immediately after that loop finishes
	// - mirroring doProfileImport's own unconditional `fmt.Println()` right
	// after the download loop, which precedes ImportModFailed's own leading
	// blank line on failure (see ImportModFailed). When #138's cache-first
	// guard skips the download entirely (target version already fully
	// marked in cache), this phase is skipped with it - the same shape as
	// SwitchDownloadDone under ApplyProfileSwitch's #96 guard - so there is
	// no download readout to terminate. A caller wanting byte-identical
	// output prints a bare `fmt.Println()` here.
	ImportDownloadDone
	// ImportModFailed fires for ANY of the download loop's mod-skipping
	// failure reasons - a failed GetMod, GetModFiles, an empty file list, a
	// file-selection error, a failed DownloadMod, a failed installer.Install,
	// or a failed SaveInstalledMod - mirroring doProfileImport's uniform "
	// Error: %s\n" (Detail already carries the reason text verbatim: "failed
	// to fetch mod: %v", "failed to get files: %v", "no downloadable files",
	// the file-selection error's own message, "download failed: %v",
	// "deploy failed: %v", or "save failed: %v"). The download-failure
	// variant is preceded by its own extra blank line in the pre-extraction
	// CLI (printed inside the download loop, before the unconditional
	// ImportDownloadDone one after it) - a caller wanting byte-identical
	// output detects this the same way InstallDownloadFailed's own doc
	// comment describes (checking Detail's text, here for a
	// "download failed:" prefix) and prints a bare blank line first. Always
	// non-fatal - the loop always continues to the next ref, matching
	// failedCount++; continue.
	ImportModFailed
	// ImportModInstalled fires once a to-be-installed mod has been fully
	// installed (downloaded, deployed, saved, profile-upserted) - mirroring
	// "    ✓ Installed: %s\n". ModName is set (mod.Name, now known).
	ImportModInstalled
	// ImportNote fires when UpsertMod (recording the profile's FileIDs after
	// a successful install) fails - the sole --verbose-gated diagnostic in
	// the install loop, mirroring "    Warning: could not update profile: %v"
	// (4-space indent, matching ApplyProfileSwitch's own SwitchInstallNote
	// convention).
	ImportNote

	// --- #255: compile-mode deploy readout, extending this same enum
	// (the established "extend, don't fork" convention above). ---

	// DeployMergeSynced fires once per DeployProfile on a DeployCompile
	// game, after the post-loop merged-artifact sync succeeds with a
	// merged artifact in place. It does not fire when the profile has no
	// merge participants (nothing merged, nothing to report) or when the
	// sync itself fails (that path emits a DeployWarning instead). Total
	// carries the number of mods whose content the merged artifact
	// carries; Detail names the artifact file
	// (source.MergeCompiler.MergedArtifactName - the format is the
	// source's business, never core's, #256); RawFallbacks counts
	// participant mods that fell back to an individual raw deploy (failed
	// conversion). No single mod is in scope (Index/ModName/ModID are
	// zero). The same readout is recorded on DeployResult
	// (MergedArtifact/MergedMods/RawFallbacks) for callers with no
	// progress stream.
	DeployMergeSynced

	// --- v2 Phase 2 Unit H (#288): three phases that exist so the BATCH
	// install engine can emit DATA where its two frontends' frozen wordings
	// differ. `lmm install <query>`'s multi-select path (batchInstallMods
	// before the lift) and `lmm install`'s dependency path (doInstallBatch)
	// print the same three facts with different text; core cannot pick one
	// without breaking the other's byte-identity, so each renders its own
	// sentence from the same event. Appended at the end of the enum so no
	// existing phase's numeric value moves. ---

	// InstallLockRefusal fires when a BATCH-path mod is skipped because its
	// profile ref is LOCKED at another version. Detail is the refusal
	// SENTENCE ONLY - LockedRefRefusalError's text minus its ErrModLocked
	// prefix (see lockedRefRefusalMessage) - because the multi-select path
	// prints exactly that ("  Skipped: <sentence>") while the dependency
	// path prints the wrapped error ("  Skipped: mod is locked:
	// <sentence>"). InstallResult.Skipped keeps the FULL wrapped error text
	// either way, so a caller reading the result (rather than the stream)
	// still gets the sentinel-prefixed message every other lock gate
	// produces.
	InstallLockRefusal
	// InstallChecksumSaveFailed fires when a BATCH-path mod's
	// SaveFileChecksum fails - non-fatal, the mod stays installed. Message
	// carries the reason with no prefix at all ("failed to save checksum:
	// ..."), matching InstallResult.Warnings' own no-baked-in-prefix
	// convention: the multi-select path prints it INDENTED ("  Warning:
	// %s") and the dependency path flush ("Warning: %s"). Distinct from
	// InstallWarning purely for that indent - the STRICT path's own
	// checksum failure still uses InstallWarning.
	InstallChecksumSaveFailed
	// InstallMergedPakSyncFailed fires when ApplyInstall's unconditional
	// end-of-install merged-pak sync returns a hard error. Message is the
	// RAW error text, with no leading phrase, because the two frontends
	// word it differently ("Warning: syncing merged pak: %s" on the
	// single-mod/dependency paths, "Warning: could not sync merged pak: %s"
	// on the multi-select one). InstallResult.Warnings keeps its own
	// "syncing merged pak: %v" entry unchanged, and
	// InstallResult.MergedPakSyncFailed records the same fact for callers
	// with no progress stream. The sync's non-fatal WARNINGS (as opposed to
	// a hard failure) still arrive as ordinary InstallWarning events - both
	// frontends print those identically.
	InstallMergedPakSyncFailed
)

// deployPhaseNames maps each DeployPhase to its wire name (snake_case of
// the constant without the type prefix rules — the constant's own name,
// lower-snake). Keep in declaration order.
var deployPhaseNames = [...]string{
	DeployPurging: "deploy_purging", DeployBeforeEachSkipped: "deploy_before_each_skipped", DeployRedownloading: "deploy_redownloading",
	DeployDownloading: "deploy_downloading", DeployDownloadFailed: "deploy_download_failed", DeployDownloadDone: "deploy_download_done",
	DeploySkipped: "deploy_skipped", DeployDeployed: "deploy_deployed", DeployBeforeAllForced: "deploy_before_all_forced",
	DeployNote: "deploy_note", DeployWarning: "deploy_warning", PurgeWarning: "purge_warning", PurgeNote: "purge_note",
	PurgeComplete: "purge_complete", SwitchDisableNote: "switch_disable_note", SwitchDisabled: "switch_disabled",
	SwitchEnableNote: "switch_enable_note", SwitchEnabled: "switch_enabled", SwitchInstalling: "switch_installing",
	SwitchInstallingMod: "switch_installing_mod", SwitchInstallError: "switch_install_error", SwitchDownloading: "switch_downloading",
	SwitchDownloadFailed: "switch_download_failed", SwitchDownloadDone: "switch_download_done", SwitchInstalled: "switch_installed",
	SwitchInstallNote: "switch_install_note", InstallBeforeAllForced: "install_before_all_forced", InstallBeforeEachForced: "install_before_each_forced",
	InstallDepInstalling: "install_dep_installing", InstallDepReinstalling: "install_dep_reinstalling", InstallDepFileSelected: "install_dep_file_selected",
	InstallDepDownloading: "install_dep_downloading", InstallDepSkipped: "install_dep_skipped", InstallDepDownloadDone: "install_dep_download_done",
	InstallDepConflictWarning: "install_dep_conflict_warning", InstallDepInstalled: "install_dep_installed", InstallDownloadStarted: "install_download_started",
	InstallDownloading: "install_downloading", InstallDownloadDone: "install_download_done", InstallDownloadFailed: "install_download_failed",
	InstallChecksumComputed: "install_checksum_computed", InstallCompiling: "install_compiling", InstallExtracting: "install_extracting",
	InstallDeploying: "install_deploying", InstallDone: "install_done", InstallNote: "install_note", InstallWarning: "install_warning",
	UpdateDownloading: "update_downloading", UpdateDownloadDone: "update_download_done", UpdateBeforeEachForced: "update_before_each_forced",
	UpdateWarning: "update_warning", UpdateNote: "update_note", PurgeModSkipped: "purge_mod_skipped", PurgeModPurged: "purge_mod_purged",
	ImportSaved: "import_saved", ImportInstalling: "import_installing", ImportModInstalling: "import_mod_installing",
	ImportDownloading: "import_downloading", ImportDownloadDone: "import_download_done", ImportModFailed: "import_mod_failed",
	ImportModInstalled: "import_mod_installed", ImportNote: "import_note", DeployMergeSynced: "deploy_merge_synced",
	InstallLockRefusal: "install_lock_refusal", InstallChecksumSaveFailed: "install_checksum_save_failed",
	InstallMergedPakSyncFailed: "install_merged_pak_sync_failed",
}

// String returns the phase's wire name.
func (p DeployPhase) String() string {
	if p >= 0 && int(p) < len(deployPhaseNames) && deployPhaseNames[p] != "" {
		return deployPhaseNames[p]
	}
	return fmt.Sprintf("deploy_phase(%d)", int(p))
}

// MarshalText implements encoding.TextMarshaler.
func (p DeployPhase) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (p *DeployPhase) UnmarshalText(b []byte) error {
	for i, n := range deployPhaseNames {
		if n == string(b) {
			*p = DeployPhase(i)
			return nil
		}
	}
	return fmt.Errorf("unknown deploy phase %q", b)
}

// DeployModClass classifies how a DeployDeployed mod's content reaches the
// game directory on a DeployCompile game (#255), so callers stop rendering
// merge participants - which individually deploy zero files by design
// (#197) - as ordinary per-mod deployments. Classification happens BEFORE
// the deploy loop (from enabledMergeSources), so it cannot know this sync's
// conversion outcomes yet: an opted-in pak that goes on to fail conversion
// is optimistically DeployModMerged here, and the correction is carried by
// the existing conversion-failure warning plus DeployMergeSynced's
// RawFallbacks count (issue #255's decided option (b); the stored
// fingerprint is deliberately NOT consulted pre-loop - it describes the
// previous sync, which is wrong on first deploy and after input changes).
type DeployModClass int

const (
	// DeployModIndividual (the zero value): an ordinary mod deploying its
	// own files - every mod on a non-compile game, and a loose-file mod
	// in a compile profile.
	DeployModIndividual DeployModClass = iota
	// DeployModMerged: a merge participant - its content reaches the game
	// via the profile-level merged artifact built after the deploy loop;
	// its own install step deploys nothing (native merge sources) or its
	// raw copy is claimed by the merge (converted artifacts).
	DeployModMerged
	// DeployModRaw: a convertible artifact excluded from the merge by the
	// game- or mod-level ConvertPaks opt-out (#221) - it deploys raw,
	// individually.
	DeployModRaw
)

// deployModClassNames maps each DeployModClass to its wire name. Keep in
// declaration order.
var deployModClassNames = [...]string{
	DeployModIndividual: "individual",
	DeployModMerged:     "merged",
	DeployModRaw:        "raw",
}

// String returns the class's wire name.
func (c DeployModClass) String() string {
	if c >= 0 && int(c) < len(deployModClassNames) && deployModClassNames[c] != "" {
		return deployModClassNames[c]
	}
	return fmt.Sprintf("deploy_mod_class(%d)", int(c))
}

// MarshalText implements encoding.TextMarshaler.
func (c DeployModClass) MarshalText() ([]byte, error) { return []byte(c.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (c *DeployModClass) UnmarshalText(b []byte) error {
	for i, n := range deployModClassNames {
		if n == string(b) {
			*c = DeployModClass(i)
			return nil
		}
	}
	return fmt.Errorf("unknown deploy mod class %q", b)
}

// DeployResult reports the outcome of DeployProfile. As with UninstallResult
// (see its doc comment), every entry below is always recorded - there is no
// verbosity concept in core - but Warnings and Notes carry the same two
// display contracts Task 2 established:
//
//   - Warnings holds diagnostics the pre-extraction CLI printed
//     unconditionally to stderr: install.before_all/uninstall.before_all
//     (when forced), a skipped uninstall.before_each during purge,
//     install/uninstall after_each/after_all hook failures, and a
//     profile-overrides application failure. Callers should print each
//     entry to stderr, unconditionally, e.g.
//     `fmt.Fprintf(os.Stderr, "Warning: %v\n", w)`.
//   - Notes holds operational diagnostics the pre-extraction CLI only
//     printed under --verbose: a failed undeploy-before-redeploy, a failed
//     SetModLinkMethod, and a failed SetModDeployed, all per mod, plus (for
//     a --purge pass) the equivalent per-mod undeploy/SetModDeployed
//     failures from purging. Each entry already carries its historical
//     prefix ("Warning: " for the deploy-loop trio, "⚠ " for the purge
//     trio) baked into the text, matching each one's pre-extraction
//     wording; a caller wanting byte-identical pre-extraction output should
//     print each entry to stdout ONLY under --verbose, verbatim, e.g.
//     `fmt.Printf("  %s\n", n)`.
//
// Every entry in both slices is ALSO reported via the event stream at
// the exact point it is appended (DeployBeforeAllForced/DeployNote/
// DeployWarning/PurgeWarning/PurgeNote - see each DeployPhase constant's
// doc comment for which), with Detail equal to the slice entry verbatim and
// the phase itself indicating which display contract above applies. A
// caller driving its console output entirely from the event stream (as
// cmd/lmm's doDeploy does) gets pre-extraction-accurate positioning; the
// slices remain here, unconditionally, for callers that only want the
// final, order-independent summary.
//
// Skipped carries one "<mod name>: <reason>" entry per mod that did not
// deploy, for any reason (hook failure, download failure, install
// failure); the pre-extraction CLI printed each of these unconditionally
// as it happened; the DeployBeforeEachSkipped/DeployDownloadFailed/
// DeploySkipped events carry the same reason text in real time for callers
// that want to print them as they occur instead of (or in addition to) at
// the end.
//
// On error, the returned result carries any diagnostics accumulated before
// the failure; callers should surface them alongside the error.
type DeployResult struct {
	Deployed int      `json:"deployed"`
	Skipped  []string `json:"skipped,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Notes    []string `json:"notes,omitempty"`

	// MergedArtifact/MergedMods/RawFallbacks mirror the DeployMergeSynced
	// event for callers with no event sink (#255 - a caller may pass
	// nil): the merged artifact's file name
	// (source.MergeCompiler.MergedArtifactName), how many mods' content it
	// carries, and how many participants fell back to an individual raw
	// deploy (failed conversion). All zero when the deploy produced/kept
	// no merged artifact: non-compile games, or a compile profile with no
	// merge participants.
	MergedArtifact string `json:"merged_artifact,omitempty"`
	MergedMods     int    `json:"merged_mods"`
	RawFallbacks   int    `json:"raw_fallbacks"`
}

// sameFileIDSet reports whether selected is exactly the set of currentFileIDs
// - the only provable "this update changes nothing" condition (see
// guardNoOpUpdateSelection).
func sameFileIDSet(selected []*domain.DownloadableFile, currentFileIDs []string) bool {
	if len(currentFileIDs) == 0 {
		return false
	}
	current := make(map[string]bool, len(currentFileIDs))
	for _, id := range currentFileIDs {
		current[id] = true
	}
	seen := make(map[string]bool, len(selected))
	for _, f := range selected {
		if !current[f.ID] {
			return false
		}
		seen[f.ID] = true
	}
	return len(seen) == len(current)
}

// OrderByProfile returns mods in a stable, deterministic order for
// multi-mod operations (deploy, plan/apply): mods absent from profile.Mods
// first - sorted by "SourceID:ID" key (domain.ModKey) for a reproducible
// tie-break - followed by mods present in profile.Mods, in profile.Mods
// order. domain.Profile.Mods documents "first = lowest priority" (see its
// doc comment), so later entries in that order deploy later and win file
// conflicts; this function preserves that meaning end to end.
//
// profile may be nil - treated as an empty profile, so every mod is
// "absent" and the whole result is simply sorted by key. Callers with a
// profile that failed to load (e.g. an unreadable/missing YAML file) use
// this to stay deterministic without aborting the caller's own operation.
//
// Keys are deduplicated: a mod repeated in profile.Mods (which shouldn't
// normally happen - ReorderMods already dedupes on save) or in mods
// contributes only its single occurrence to the result, at its first
// resolved position.
func OrderByProfile(profile *domain.Profile, mods []domain.InstalledMod) []domain.InstalledMod {
	byKey := make(map[string]domain.InstalledMod, len(mods))
	for _, m := range mods {
		byKey[domain.ModKey(m.SourceID, m.ID)] = m
	}

	var profileMods []domain.ModReference
	if profile != nil {
		profileMods = profile.Mods
	}

	inProfile := make(map[string]bool, len(profileMods))
	for _, ref := range profileMods {
		inProfile[domain.ModKey(ref.SourceID, ref.ModID)] = true
	}

	var unlisted []domain.InstalledMod
	for key, m := range byKey {
		if !inProfile[key] {
			unlisted = append(unlisted, m)
		}
	}
	sort.Slice(unlisted, func(i, j int) bool {
		return domain.ModKey(unlisted[i].SourceID, unlisted[i].ID) < domain.ModKey(unlisted[j].SourceID, unlisted[j].ID)
	})

	ordered := make([]domain.InstalledMod, 0, len(mods))
	ordered = append(ordered, unlisted...)

	seen := make(map[string]bool, len(profileMods))
	for _, ref := range profileMods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if seen[key] {
			continue
		}
		seen[key] = true
		if m, ok := byKey[key]; ok {
			ordered = append(ordered, m)
		}
	}

	return ordered
}

// DeployProfile redeploys the mods of a profile in profile order: an
// optional --purge pass first (undeploying every installed mod), then for
// each mod to deploy - re-downloading from source if its cache entry is
// missing - an undeploy-then-install cycle recording the effective link
// method and deployed state, and finally applying any profile overrides.
// This is a behavior-preserving extraction of the pre-extraction CLI's
// doDeploy (cmd/lmm/deploy.go) and purgeDeployedMods (cmd/lmm/purge.go's
// --purge-before-deploy variant; the standalone `lmm purge` command was
// later extracted too, as PurgeProfile, and since #61 both purges share
// purgeMods); see the task report for the exact mapping.
//
// sink may be nil. When non-nil, it is called synchronously from this
// function for every notable event - see DeployPhase's constants for what
// each one means, and each event type's own doc comment for the payload it
// carries.
func (s *Service) DeployProfile(ctx context.Context, game *domain.Game, profileName string, opts DeployOptions, sink EventSink) (*DeployResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &DeployResult{}, err
	}
	defer release()
	return s.deployProfile(ctx, game, profileName, opts, sink)
}

func (s *Service) deployProfile(ctx context.Context, game *domain.Game, profileName string, opts DeployOptions, sink EventSink) (*DeployResult, error) {
	result := &DeployResult{}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}

	hooks, err := s.resolvedHooks(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	runner, err := s.hookRunner(ctx)
	if err != nil {
		return result, err
	}

	var enabledBeforePurge map[string]bool
	if opts.Purge {
		mods, err := s.GetInstalledMods(ctx, game.ID, profileName)
		if err != nil {
			return result, fmt.Errorf("getting installed mods: %w", err)
		}
		// Deterministic purge order: profile.Mods order (nil-safe - an
		// unreadable profile falls back to OrderByProfile's nil handling
		// rather than aborting the purge).
		profile, _ := config.LoadProfile(s.configDir, game.ID, profileName)
		mods = OrderByProfile(profile, mods)
		enabledBeforePurge = make(map[string]bool)
		for _, m := range mods {
			if m.Enabled {
				enabledBeforePurge[domain.ModKey(m.SourceID, m.ID)] = true
			}
		}
		if err := s.purgeForDeploy(ctx, game, profileName, mods, opts, hooks, runner, result, emit); err != nil {
			return result, fmt.Errorf("purging mods: %w", err)
		}
	}

	var linkMethod domain.LinkMethod
	if opts.LinkMethod != nil {
		linkMethod = *opts.LinkMethod
	} else {
		method, err := s.GetEffectiveLinkMethod(ctx, game, profileName)
		if err != nil {
			return result, err
		}
		linkMethod = method
	}
	installer := s.NewInstallerWithLinker(game, s.GetLinker(linkMethod))

	var modsToDeploy []*domain.InstalledMod
	if opts.ModID != "" {
		mod, err := s.GetInstalledMod(ctx, opts.SourceID, opts.ModID, game.ID, profileName)
		if err != nil {
			return result, fmt.Errorf("mod not found: %s", opts.ModID)
		}
		if !mod.Enabled && !opts.All {
			return result, fmt.Errorf("mod %s is disabled - use --all to deploy disabled mods, or enable it with 'lmm mod enable %s'", mod.Name, opts.ModID)
		}
		modsToDeploy = append(modsToDeploy, mod)
	} else {
		mods, err := s.GetInstalledModsInProfileOrder(ctx, game.ID, profileName)
		if err != nil {
			return result, fmt.Errorf("getting installed mods: %w", err)
		}
		for i := range mods {
			var shouldDeploy bool
			switch {
			case opts.All:
				shouldDeploy = true
			case enabledBeforePurge != nil:
				shouldDeploy = enabledBeforePurge[domain.ModKey(mods[i].SourceID, mods[i].ID)]
			default:
				shouldDeploy = mods[i].Enabled
			}
			if shouldDeploy {
				modsToDeploy = append(modsToDeploy, &mods[i])
			}
		}
	}

	if len(modsToDeploy) == 0 {
		return result, nil
	}

	hookCtx := hookContextFor(game)
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.before_all", hooks.GetInstallBeforeAll()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("install.before_all hook failed: %w", err)
		}
		msg := fmt.Sprintf("install.before_all hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(HookEvent{Scope: Scope{Op: OpDeploy}, Phase: DeployBeforeAllForced, Stage: "install.before_all", Detail: msg})
	}

	// deferredWarnings holds install.after_each (per mod, in loop order)
	// and install.after_all DeployWarning events: the pre-extraction CLI
	// printed these together, AFTER the profile-overrides warning below,
	// even though both hooks run earlier in the function - see
	// DeployWarning's doc comment. Emission (and therefore printing) is
	// deferred to preserve that order; the Warnings slice itself is still
	// appended to at the natural point, unchanged.
	var deferredWarnings []Event

	// #255: on a compile game, classify each mod up front so its
	// DeployDeployed event can say whether it deploys files individually or
	// rides the merged artifact built after the loop (nil map - and
	// therefore the zero class - everywhere else).
	modClasses := s.classifyCompileDeployMods(ctx, game, profileName, modsToDeploy)

	total := len(modsToDeploy)
	for idx, mod := range modsToDeploy {
		// Task 6 item d (cancel-then-drain): checked between mods, never
		// mid-file-operation - a cancelled ctx aborts here with whatever
		// result has accumulated so far (the partial-result convention -
		// see this function's doc comment and DeployResult's).
		if err := ctx.Err(); err != nil {
			return result, err
		}

		scope := Scope{Op: OpDeploy, Index: idx + 1, Total: total, ModName: mod.Name, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}}

		hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
		if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.before_each", hooks.GetInstallBeforeEach()); err != nil {
			reason := fmt.Sprintf("install.before_each hook failed: %v", err)
			emit(ModEvent{Scope: scope, Phase: DeployBeforeEachSkipped, Detail: reason})
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %s", mod.Name, reason))
			continue
		}

		if !s.GetGameCache(game).Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
			if skipped := s.redeployFromSource(ctx, game, mod, scope, emit, result); skipped {
				continue
			}
		}

		if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
			msg := fmt.Sprintf("Warning: undeploy %s: %v", mod.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: DeployNote, Detail: msg})
		}

		if err := installer.Install(ctx, game, &mod.Mod, profileName); err != nil {
			reason := err.Error()
			emit(ModEvent{Scope: scope, Phase: DeploySkipped, Detail: reason})
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %s", mod.Name, reason))
			continue
		}

		if err := s.setModLinkMethod(ctx, mod.SourceID, mod.ID, game.ID, profileName, linkMethod); err != nil {
			msg := fmt.Sprintf("Warning: could not update link method: %v", err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: DeployNote, Detail: msg})
		}
		if err := s.setModDeployed(ctx, mod.SourceID, mod.ID, game.ID, profileName, true); err != nil {
			msg := fmt.Sprintf("Warning: could not mark as deployed: %v", err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: DeployNote, Detail: msg})
		}

		result.Deployed++
		emit(ModEvent{Scope: scope, Phase: DeployDeployed, Class: modClasses[domain.ModKey(mod.SourceID, mod.ID)]})

		if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.after_each", hooks.GetInstallAfterEach()); err != nil {
			msg := fmt.Sprintf("install.after_each hook failed for %s: %v", mod.ID, err)
			result.Warnings = append(result.Warnings, msg)
			deferredWarnings = append(deferredWarnings, WarningEvent{Scope: scope, Phase: DeployWarning, Message: msg})
		}
	}

	// The head-of-loop check above cannot see a cancellation that lands
	// during the LAST mod's iteration, which would otherwise fall through to
	// after_all/merged-pak sync and return (result, nil) - review finding I1.
	if err := ctx.Err(); err != nil {
		return result, err
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = "", "", ""
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.after_all", hooks.GetInstallAfterAll()); err != nil {
		msg := fmt.Sprintf("install.after_all hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		deferredWarnings = append(deferredWarnings, WarningEvent{Scope: Scope{Op: OpDeploy}, Phase: DeployWarning, Message: msg})
	}

	if profile, err := config.LoadProfile(s.configDir, game.ID, profileName); err == nil && len(profile.Overrides) > 0 {
		if err := ApplyProfileOverrides(game, profile); err != nil {
			msg := fmt.Sprintf("applying profile overrides: %v", err)
			result.Warnings = append(result.Warnings, msg)
			emit(WarningEvent{Scope: Scope{Op: OpDeploy}, Phase: DeployWarning, Message: msg})
		}
	}

	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		msg := fmt.Sprintf("syncing merged pak: %v", syncErr)
		result.Warnings = append(result.Warnings, msg)
		emit(WarningEvent{Scope: Scope{Op: OpDeploy}, Phase: DeployWarning, Message: msg})
	} else {
		for _, w := range syncWarnings {
			result.Warnings = append(result.Warnings, w)
			emit(WarningEvent{Scope: Scope{Op: OpDeploy}, Phase: DeployWarning, Message: w})
		}
		// #255: the sync succeeded, so the just-written fingerprint is the
		// authoritative record of what the merged artifact carries - report
		// it (result fields + one DeployMergeSynced event).
		s.recordMergeOutcome(ctx, game, profileName, OpDeploy, result, emit)
	}

	for _, w := range deferredWarnings {
		emit(w)
	}

	return result, nil
}

// redeployFromSource re-fetches mod from source and downloads its file(s)
// into the cache when DeployProfile finds the cache entry missing,
// mirroring doDeploy's cache-miss branch exactly, including its one
// preserved quirk: the freshly-fetched *domain.Mod (not the InstalledMod's
// own, possibly-stale, Mod) is what gets downloaded, while the InstalledMod
// row's own Mod is what DeployProfile installs from afterward - see the
// task report. Returns true if the mod was skipped (added to
// result.Skipped and reported via emit) and the caller must not proceed to
// undeploy/install it.
func (s *Service) redeployFromSource(ctx context.Context, game *domain.Game, mod *domain.InstalledMod, scope Scope, emit func(Event), result *DeployResult) bool {
	skip := func(reason string) bool {
		emit(ModEvent{Scope: scope, Phase: DeploySkipped, Detail: reason})
		result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %s", mod.Name, reason))
		return true
	}

	emit(StepEvent{Scope: scope, Phase: DeployRedownloading})

	fetchedMod, err := s.GetMod(ctx, mod.SourceID, game.ID, mod.ID)
	if err != nil {
		return skip(fmt.Sprintf("failed to fetch: %v", err))
	}

	files, err := s.GetModFiles(ctx, mod.SourceID, fetchedMod)
	if err != nil || len(files) == 0 {
		return skip("no files available")
	}

	filesToDownload, err := selectFilesForVersion(files, mod.FileIDs, mod.Version)
	if err != nil {
		return skip(err.Error())
	}

	// fetchedMod (not the InstalledMod's own mod.Mod - see this function's
	// doc comment) is what actually gets downloaded and cached below, so its
	// Version must be stamped to match the resolved file's effective version
	// (#94's convention, applied here for #96): otherwise a healed/pinned
	// version (mod.Version) that differs from the source's mod-level
	// Version - exactly the drift case this function now resolves - caches
	// under the WRONG version, and the installer.Install call that follows
	// (using the InstalledMod's own, unmodified mod.Version) fails with "mod
	// not in cache" even though the download just succeeded.
	fetchedMod.Version = domain.EffectiveInstalledVersion(mod.Version, filesToDownload)

	for _, file := range filesToDownload {
		if err := ctx.Err(); err != nil {
			// Record the skip like every other early exit here: the caller
			// only sees `continue`, so a bare `true` on the LAST mod of the
			// profile made a cancelled deploy look successful (review
			// finding I1).
			return skip(fmt.Sprintf("cancelled: %v", err))
		}
		progressFn := func(e Event) {
			d, ok := e.(DownloadEvent)
			if !ok || d.TotalBytes <= 0 {
				return
			}
			emit(DownloadEvent{Scope: scope, Phase: DeployDownloading, Percent: d.Percent})
		}
		if _, err := s.downloadMod(ctx, mod.SourceID, game, fetchedMod, file, progressFn); err != nil {
			reason := fmt.Sprintf("download failed: %v", err)
			emit(ModEvent{Scope: scope, Phase: DeployDownloadFailed, Detail: reason})
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %s", mod.Name, reason))
			return true
		}
	}

	emit(StepEvent{Scope: scope, Phase: DeployDownloadDone})

	// #139: a successful heal must outlive this deploy - persist the resolved
	// FileIDs onto the DB row (via the targeted SetModFileIDs setter, never a
	// full-row save, which would wipe fields this flow didn't load) so
	// `profile export` emits the live IDs and the next cache miss resolves
	// them directly instead of re-healing. Failure is a note, not a skip: the
	// download itself succeeded and the deploy should proceed (the same
	// non-fatal idiom as DeployProfile's SetModLinkMethod/SetModDeployed).
	// The write is skipped when the resolved set equals the stored one (no
	// heal happened - the stored IDs were simply redownloaded): SetModFileIDs
	// rewrites the installed_mod_files rows, and rewriting an unchanged set
	// would silently drop their recorded checksums.
	if sameFileIDSet(filesToDownload, mod.FileIDs) {
		return false
	}
	healedIDs := make([]string, 0, len(filesToDownload))
	for _, f := range filesToDownload {
		if err := ctx.Err(); err != nil {
			return skip(fmt.Sprintf("cancelled: %v", err))
		}
		healedIDs = append(healedIDs, f.ID)
	}
	if err := s.setModFileIDs(ctx, mod.SourceID, mod.ID, game.ID, mod.ProfileName, healedIDs); err != nil {
		msg := fmt.Sprintf("Warning: could not persist healed file IDs for %s: %v", mod.Name, err)
		result.Notes = append(result.Notes, msg)
		emit(StepEvent{Scope: scope, Phase: DeployNote, Detail: msg})
	} else {
		mod.FileIDs = healedIDs
	}

	return false
}

// purgeForDeploy undeploys every currently-installed mod in profileName
// before DeployProfile redeploys them, mirroring the pre-extraction CLI's
// purgeDeployedMods (used only by `lmm deploy --purge`). Since #61 it is a
// thin adapter over purgeMods - the single purge loop it shares with
// PurgeProfile (the standalone `lmm purge` command's flow). See
// DeployResult's doc comment for where each diagnostic ends up.
func (s *Service) purgeForDeploy(ctx context.Context, game *domain.Game, profileName string, mods []domain.InstalledMod, opts DeployOptions, hooks *ResolvedHooks, runner *HookRunner, result *DeployResult, emit func(Event)) error {
	return s.purgeMods(ctx, game, profileName, mods, purgeSpec{
		op:        OpDeploy,
		forDeploy: true,
		hooks:     hooks,
		runner:    runner,
		hookCtx:   hookContextFor(game),
		force:     opts.Force,
		skip:      opts.SkipHooks,
		emit:      emit,
		warnings:  &result.Warnings,
		notes:     &result.Notes,
	})
}

// purgeSpec parameterizes purgeMods' two consumers: purgeForDeploy
// (deploy --purge) and PurgeProfile (lmm purge). Every historical
// divergence between the pre-#61 copies (purgeDeployedMods vs doPurge) is
// an explicit forDeploy branch at its point of occurrence inside
// purgeMods, each pinned by a named test - see the branch comments.
// warnings/notes point into the consumer's result slices; skipped/purged
// are purge-command-only (nil in deploy mode, which neither counts
// successes nor tracks per-mod failures).
type purgeSpec struct {
	uninstall bool // PurgeProfile --uninstall; always false for deploy
	forDeploy bool

	// op is the calling flow's operation, stamped onto every event this
	// loop emits: OpDeploy for purgeForDeploy, OpPurge for PurgeProfile.
	op Op

	hooks   *ResolvedHooks
	runner  *HookRunner
	hookCtx HookContext
	force   bool
	skip    bool // SkipHooks: run no hooks even when hooks/runner are set

	emit     func(Event)
	warnings *[]string
	notes    *[]string
	skipped  *[]string
	purged   *int
}

// purgeMods is THE purge loop (#61): the one shared implementation of
// "undeploy every mod in mods", consumed via purgeForDeploy and
// PurgeProfile. An empty mods slice returns immediately - no hooks, no
// events. Cancellation is honored between mods; the caller's accumulated
// result travels back through the spec's pointers (partial-result
// convention).
func (s *Service) purgeMods(ctx context.Context, game *domain.Game, profileName string, mods []domain.InstalledMod, spec purgeSpec) error {
	if len(mods) == 0 {
		return nil
	}

	hookCtx := spec.hookCtx
	if err := runHook(ctx, spec.skip, spec.runner, &hookCtx, "uninstall.before_all", spec.hooks.GetUninstallBeforeAll()); err != nil {
		if !spec.force {
			return fmt.Errorf("uninstall.before_all hook failed: %w", err)
		}
		msg := fmt.Sprintf("uninstall.before_all hook failed (forced): %v", err)
		*spec.warnings = append(*spec.warnings, msg)
		spec.emit(HookEvent{Scope: Scope{Op: spec.op}, Phase: DeployBeforeAllForced, Stage: "uninstall.before_all", Detail: msg})
	}

	installer, err := s.getInstallerForProfile(ctx, game, profileName)
	if err != nil {
		return err
	}
	spec.emit(StepEvent{Scope: Scope{Op: spec.op, Total: len(mods)}, Phase: DeployPurging})

	// deferredWarnings holds uninstall.after_each (per mod, in loop order)
	// and uninstall.after_all PurgeWarning events: both pre-#61 copies
	// accumulated these during/after the loop and only printed them
	// together, via printHookWarnings, once the whole loop had finished -
	// so emission is deferred to right after the loop, mirroring that.
	var deferredWarnings []Event

	total := len(mods)
	for idx, mod := range mods {
		if err := ctx.Err(); err != nil {
			return err
		}

		// scope is this mod's event scope: purge-command mode carries
		// Index/Total (a progress denominator for callers); deploy mode
		// keeps its historical event shape (mod name/ID only).
		scope := Scope{Op: spec.op, ModName: mod.Name, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}}
		if !spec.forDeploy {
			scope.Index, scope.Total = idx+1, total
		}

		hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
		if err := runHook(ctx, spec.skip, spec.runner, &hookCtx, "uninstall.before_each", spec.hooks.GetUninstallBeforeEach()); err != nil {
			// Divergence 1 of 4 (#61) - both sides skip the mod (it stays
			// deployed) but report differently. Deploy: a Warning with the
			// "during purge (not purged)" wording, pinned by
			// TestService_DeployProfile_PurgeBeforeEachSkip_WarningTextExact.
			// Purge: a Skipped entry (doPurge's failed++) + PurgeModSkipped,
			// pinned by TestService_PurgeProfile_BeforeEachSkip_*.
			if spec.forDeploy {
				msg := fmt.Sprintf("uninstall.before_each hook failed for %s during purge (not purged): %v", mod.Name, err)
				*spec.warnings = append(*spec.warnings, msg)
				spec.emit(WarningEvent{Scope: scope, Phase: PurgeWarning, Message: msg})
			} else {
				detail := fmt.Sprintf("uninstall.before_each hook failed: %v", err)
				*spec.skipped = append(*spec.skipped, fmt.Sprintf("%s: %s", mod.Name, detail))
				spec.emit(ModEvent{Scope: scope, Phase: PurgeModSkipped, Detail: detail})
			}
			continue
		}

		if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
			// Best-effort: files may have been manually removed.
			msg := fmt.Sprintf("⚠ %s - %v", mod.Name, err)
			*spec.notes = append(*spec.notes, msg)
			spec.emit(StepEvent{Scope: scope, Phase: PurgeNote, Detail: msg})
		}

		// Divergence 4 of 4 (#61): --uninstall (purge-command-only)
		// deletes the DB record and profile-YAML entry; everything else
		// marks the record not-deployed. A record-delete failure skips the
		// rest of the mod (doPurge's failed++ + continue), including its
		// after_each hook and PurgeModPurged.
		if spec.uninstall {
			if err := s.deleteInstalledMod(ctx, mod.SourceID, mod.ID, game.ID, profileName); err != nil {
				msg := fmt.Sprintf("⚠ %s - failed to remove record: %v", mod.Name, err)
				*spec.notes = append(*spec.notes, msg)
				spec.emit(StepEvent{Scope: scope, Phase: PurgeNote, Detail: msg})
				*spec.skipped = append(*spec.skipped, fmt.Sprintf("%s: failed to remove record: %v", mod.Name, err))
				continue
			}
			if err := s.NewProfileManager().RemoveMod(game.ID, profileName, mod.SourceID, mod.ID); err != nil {
				msg := fmt.Sprintf("Note: %s - %v", mod.Name, err)
				*spec.notes = append(*spec.notes, msg)
				spec.emit(StepEvent{Scope: scope, Phase: PurgeNote, Detail: msg})
			}
		} else {
			if err := s.setModDeployed(ctx, mod.SourceID, mod.ID, game.ID, profileName, false); err != nil {
				msg := fmt.Sprintf("⚠ %s - failed to mark as not deployed: %v", mod.Name, err)
				*spec.notes = append(*spec.notes, msg)
				spec.emit(StepEvent{Scope: scope, Phase: PurgeNote, Detail: msg})
			}
		}

		if err := runHook(ctx, spec.skip, spec.runner, &hookCtx, "uninstall.after_each", spec.hooks.GetUninstallAfterEach()); err != nil {
			// Divergence 2 of 4 (#61): deploy attributes by mod ID
			// (pinned by TestService_DeployProfile_PurgeAfterEachWarning_
			// UsesModID), purge by mod NAME (doPurge purge.go's historical
			// wording, pinned by TestService_PurgeProfile_AfterHookFailures_*).
			attr := mod.Name
			if spec.forDeploy {
				attr = mod.ID
			}
			msg := fmt.Sprintf("uninstall.after_each hook failed for %s: %v", attr, err)
			*spec.warnings = append(*spec.warnings, msg)
			deferredWarnings = append(deferredWarnings, WarningEvent{Scope: scope, Phase: PurgeWarning, Message: msg})
		}

		// Divergence 3 of 4 (#61): only the purge command counts and
		// announces per-mod completion (doPurge's "✓"/succeeded++); the
		// deploy pass's event stream stays byte-identical to pre-#61.
		if !spec.forDeploy {
			*spec.purged++
			spec.emit(ModEvent{Scope: scope, Phase: PurgeModPurged})
		}
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = "", "", ""
	if err := runHook(ctx, spec.skip, spec.runner, &hookCtx, "uninstall.after_all", spec.hooks.GetUninstallAfterAll()); err != nil {
		msg := fmt.Sprintf("uninstall.after_all hook failed: %v", err)
		*spec.warnings = append(*spec.warnings, msg)
		deferredWarnings = append(deferredWarnings, WarningEvent{Scope: Scope{Op: spec.op}, Phase: PurgeWarning, Message: msg})
	}

	for _, w := range deferredWarnings {
		spec.emit(w)
	}

	linker.CleanupEmptyDirs(game.ModPath)
	spec.emit(StepEvent{Scope: Scope{Op: spec.op}, Phase: PurgeComplete})
	return nil
}

// PurgeOptions configures PurgeProfile.
type PurgeOptions struct {
	// Uninstall additionally deletes each purged mod's DB record and
	// profile-YAML entry (like uninstalling it), instead of just marking
	// it not deployed - `lmm purge --uninstall`.
	Uninstall bool

	// Hook plumbing, mirroring DeployOptions/InstallOptions: PurgeProfile
	// resolves the game/profile hooks and a HookRunner itself; all four
	// uninstall.* hooks fire (purge is an uninstall-family operation).
	// Force continues past a failing uninstall.before_all hook (recorded
	// as a Warning) instead of aborting the purge.
	Force     bool
	SkipHooks bool
}

// PurgeResult reports the outcome of PurgeProfile. Warnings and Notes
// follow DeployResult's display contract (Warnings: unconditional stderr;
// Notes: --verbose-gated stdout, historical text baked in). Skipped holds
// one "<name>: <reason>" entry per mod that was NOT fully purged (a
// before_each-skipped mod, or an --uninstall record-delete failure);
// len(Skipped) is doPurge's historical `failed` counter, so the CLI's
// "Purged: N, Failed: M" summary comes from Purged and len(Skipped).
type PurgeResult struct {
	Purged   int      `json:"purged"`
	Skipped  []string `json:"skipped,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// PurgeProfile undeploys every mod in mods from game's directory - the
// `lmm purge` command's flow, a behavior-preserving extraction of
// cmd/lmm/purge.go's doPurge (#61). The caller fetches mods (via
// GetInstalledMods) and confirms with the user first, so the set shown in
// the confirmation prompt is exactly the set purged; an empty mods slice
// returns immediately - no hooks, no events (the "No mods installed"
// message stays caller-side). Without opts.Uninstall each mod's record is
// kept and marked not-deployed; with it, records and profile entries are
// removed. Undeploy and DB-mark failures are best-effort (Notes); a
// before_each hook failure or --uninstall record-delete failure skips
// that mod (Skipped).
//
// sink may be nil. Cancellation is honored between mods (the
// partial-result convention: the accumulated result comes back alongside
// ctx.Err()); one cancellation-behavior delta from the pre-extraction
// doPurge, which never checked ctx mid-loop.
func (s *Service) PurgeProfile(ctx context.Context, game *domain.Game, profileName string, mods []domain.InstalledMod, opts PurgeOptions, sink EventSink) (*PurgeResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &PurgeResult{}, err
	}
	defer release()
	return s.purgeProfile(ctx, game, profileName, mods, opts, sink)
}

func (s *Service) purgeProfile(ctx context.Context, game *domain.Game, profileName string, mods []domain.InstalledMod, opts PurgeOptions, sink EventSink) (*PurgeResult, error) {
	result := &PurgeResult{}

	hooks, err := s.resolvedHooks(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	runner, err := s.hookRunner(ctx)
	if err != nil {
		return result, err
	}

	err = s.purgeMods(ctx, game, profileName, mods, purgeSpec{
		op:        OpPurge,
		uninstall: opts.Uninstall,
		hooks:     hooks,
		runner:    runner,
		hookCtx:   hookContextFor(game),
		force:     opts.Force,
		skip:      opts.SkipHooks,
		emit: func(e Event) {
			if sink != nil {
				sink(e)
			}
		},
		warnings: &result.Warnings,
		notes:    &result.Notes,
		skipped:  &result.Skipped,
		purged:   &result.Purged,
	})
	if err != nil {
		return result, err
	}

	// #197 I2 fix: see PurgeMergedPak's own doc comment - exmodz mods have
	// no per-mod deployment for the loop above to have already undeployed.
	// #197 postsmoke fix: Warnings, not Notes, AND emit PurgeWarning -
	// cmd/lmm/purge.go's own doc comment claims every Notes/Warnings entry
	// has a corresponding live event; this one didn't, so it was
	// completely invisible (not even --verbose-gated).
	if perr := s.purgeMergedPak(ctx, game, profileName, opts.Uninstall); perr != nil {
		msg := fmt.Sprintf("could not remove merged pak: %v", perr)
		result.Warnings = append(result.Warnings, msg)
		if sink != nil {
			sink(WarningEvent{Scope: Scope{Op: OpPurge}, Phase: PurgeWarning, Message: msg})
		}
	}

	return result, nil
}

// SwitchPlan is the pure, displayable diff between the currently-active
// default profile and a target profile - computed by PlanProfileSwitch with
// zero side effects, so a caller can render it (in a print block or a
// confirmation prompt) before deciding whether to call
// ApplyProfileSwitch. This is a behavior-preserving extraction of
// cmd/lmm/profile.go's doProfileSwitch's diff computation (through its
// "Show changes" print block) - see the task report for the exact mapping.
//
// CRITICAL: this mirrors the CLI's OWN diff algorithm. (An older, unused
// ProfileManager.Switch implementation coexisted with it until #60 retired
// it - this flow is the only switch implementation now.)
type SwitchPlan struct {
	GameID string `json:"game_id"`
	From   string `json:"from"`
	To     string `json:"to"`

	ToEnable  []domain.InstalledMod `json:"to_enable"`  // installed+disabled (or installed under a different profile) -> enable, deployed under To
	ToDisable []domain.InstalledMod `json:"to_disable"` // enabled under From but absent from To -> disable, undeployed under From
	ToInstall []domain.ModReference `json:"to_install"` // in To but not installed anywhere -> download+install (FileIDs preserved from the installed mod's own record when this is really a cache-miss redeploy - see PlanProfileSwitch)

	// PriorVersions carries, for each ToInstall entry that is really a #96
	// version-drift convergence (keyed by domain.ModKey(SourceID, ModID)),
	// the installed row being converged AWAY from - review round 1 finding
	// 1: ToInstall's own element type (domain.ModReference) has no room for
	// this, but ApplyProfileSwitch's install loop needs it to know whether
	// a LIVE older deployment exists that must be replaced (removing files
	// the new version doesn't serve) rather than merely installed over -
	// mirroring ApplyUpdate's Installer.Replace semantics. Absent for every
	// other ToInstall entry (brand-new installs, cache-miss redeploys).
	PriorVersions map[string]domain.InstalledMod `json:"prior_versions,omitempty"`

	NoChanges     bool `json:"no_changes"`     // To's mod set matches From's content-wise; only SetDefault is needed
	AlreadyActive bool `json:"already_active"` // To is already the active default profile; nothing to plan
}

// PlanProfileSwitch computes the diff between game's currently-active
// default profile and target, without mutating anything (no DB writes, no
// filesystem changes, no deploys) - callers may call this speculatively (to
// render a confirmation prompt) and discard the result without consequence.
// See SwitchPlan's doc comment; ctx is accepted for API consistency with the
// rest of Service's methods and future-proofing, even though today's
// algorithm performs no I/O that needs it.
func (s *Service) PlanProfileSwitch(ctx context.Context, game *domain.Game, target string) (*SwitchPlan, error) {
	pm := s.NewProfileManager()

	targetProfile, err := pm.Get(game.ID, target)
	if err != nil {
		return nil, fmt.Errorf("profile not found: %s", target)
	}

	currentProfile, err := pm.GetDefault(game.ID)
	var currentName string
	if err != nil {
		currentName = "default"
	} else {
		currentName = currentProfile.Name
	}

	if currentName == target {
		return &SwitchPlan{GameID: game.ID, From: currentName, To: target, AlreadyActive: true}, nil
	}

	// currentMods/allMods errors are ignored, matching doProfileSwitch
	// exactly (a missing/unreadable profile's mods are simply treated as
	// empty rather than aborting the plan).
	currentMods, _ := s.GetInstalledMods(ctx, game.ID, currentName)

	currentEnabled := make(map[string]*domain.InstalledMod)
	for i := range currentMods {
		if currentMods[i].Enabled {
			currentEnabled[domain.ModKey(currentMods[i].SourceID, currentMods[i].ID)] = &currentMods[i]
		}
	}

	targetKeys := make(map[string]domain.ModReference)
	for _, mr := range targetProfile.Mods {
		targetKeys[domain.ModKey(mr.SourceID, mr.ModID)] = mr
	}

	// allInstalled merges what's installed under the target profile with
	// what's installed under the current one (current wins on key
	// collision) - doProfileSwitch's "Get all installed mods (any profile)
	// to check what's available", which despite the comment only actually
	// considers these two profiles.
	allInstalled := make(map[string]*domain.InstalledMod)
	allMods, _ := s.GetInstalledMods(ctx, game.ID, target)
	for i := range allMods {
		allInstalled[domain.ModKey(allMods[i].SourceID, allMods[i].ID)] = &allMods[i]
	}
	for i := range currentMods {
		allInstalled[domain.ModKey(currentMods[i].SourceID, currentMods[i].ID)] = &currentMods[i]
	}

	var toDisable, toEnable []domain.InstalledMod
	var toInstall []domain.ModReference
	var priorVersions map[string]domain.InstalledMod // #96 - see SwitchPlan.PriorVersions

	// Deterministic order: iterate currentMods in fromProfile's load order
	// (mods enabled but absent from fromProfile.Mods sort first by key - see
	// OrderByProfile), filtered down to currentEnabled's members - not `for
	// key, im := range currentEnabled`, which iterates map order.
	for _, im := range OrderByProfile(currentProfile, currentMods) {
		key := domain.ModKey(im.SourceID, im.ID)
		if _, enabled := currentEnabled[key]; !enabled {
			continue
		}
		if _, inTarget := targetKeys[key]; !inTarget {
			toDisable = append(toDisable, im)
		}
	}

	// Deterministic order: iterate targetProfile.Mods in its own load
	// order - not `for key, ref := range targetKeys`, which iterates map
	// order - keeping the exact per-key classification logic. seenTarget
	// guards the same dedup targetKeys gave for free (a mod repeated in
	// targetProfile.Mods, which shouldn't normally happen, is only
	// classified once).
	seenTarget := make(map[string]bool, len(targetProfile.Mods))
	for _, ref := range targetProfile.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if seenTarget[key] {
			continue
		}
		seenTarget[key] = true

		im, installed := allInstalled[key]
		switch {
		case !installed:
			toInstall = append(toInstall, ref)
		case ref.Version != "" && im.Version != ref.Version:
			// #96 convergence: the profile names a different version than
			// the installed row - reinstall at the profile's version
			// (downgrades included). ref is passed as-is: its own FileIDs
			// (if any) describe the TARGET version; the installed row's
			// describe the wrong one. The installed row itself is recorded
			// in priorVersions (review finding 1) so ApplyProfileSwitch's
			// install loop can Replace a live older deployment instead of
			// installing over it.
			toInstall = append(toInstall, ref)
			if priorVersions == nil {
				priorVersions = make(map[string]domain.InstalledMod)
			}
			priorVersions[key] = *im
		case !s.GetGameCache(game).Exists(game.ID, im.SourceID, im.ID, im.Version):
			// Cache missing - needs a redownload; preserve the installed
			// mod's own FileIDs (not the profile YAML's, which may be
			// empty or stale).
			refWithFileIDs := ref
			refWithFileIDs.FileIDs = im.FileIDs
			toInstall = append(toInstall, refWithFileIDs)
		case !im.Enabled:
			toEnable = append(toEnable, *im)
		default:
			// Installed, cached, and enabled - but was it enabled under the
			// CURRENT profile? If not (e.g. it was only ever enabled under
			// some other profile), it still needs an explicit enable pass
			// for the target.
			if _, wasCurrent := currentEnabled[key]; !wasCurrent {
				toEnable = append(toEnable, *im)
			}
		}
	}

	return &SwitchPlan{
		GameID: game.ID, From: currentName, To: target,
		ToDisable: toDisable, ToEnable: toEnable, ToInstall: toInstall,
		PriorVersions: priorVersions,
		NoChanges:     len(toDisable) == 0 && len(toEnable) == 0 && len(toInstall) == 0,
	}, nil
}

// SwitchResult reports the outcome of ApplyProfileSwitch. As with
// DeployResult/UninstallResult, every Notes entry is always recorded - there
// is no verbosity concept in core.
//
//   - Notes holds every diagnostic doProfileSwitch only printed under
//     --verbose: failed Uninstall/SetModEnabled during the disable loop,
//     failed Install/SetModEnabled during the enable loop, and a failed
//     UpsertMod during the install loop. Each entry already carries its
//     historical "Warning: " prefix, matching doProfileSwitch's exact
//     wording; a caller wanting byte-identical output should print each
//     entry to stdout ONLY under --verbose, e.g. `fmt.Printf("  %s\n", n)`
//     (disable/enable loop notes) or `fmt.Printf("    %s\n", n)` (the
//     install loop's profile-update note, one indent level deeper).
//
// Every Notes entry is ALSO reported via the event stream at the exact
// point it is appended (SwitchDisableNote/SwitchEnableNote/SwitchInstallNote
// - see each DeployPhase constant's doc comment), with Detail equal to the
// slice entry verbatim.
//
// On error, the returned result carries any diagnostics/counts accumulated
// before the failure; callers should surface them alongside the error.
type SwitchResult struct {
	Disabled  int      `json:"disabled"`
	Enabled   int      `json:"enabled"`
	Installed int      `json:"installed"`
	Notes     []string `json:"notes,omitempty"`
	// Warnings holds diagnostics that must reach the user unconditionally
	// (#197 postsmoke fix), unlike Notes' --verbose-only display contract -
	// today, only a merged-pak sync failure for plan.To.
	Warnings []string `json:"warnings,omitempty"`
}

// ApplyProfileSwitch executes a plan produced by PlanProfileSwitch: disables
// every ToDisable mod, then enables every ToEnable mod, then downloads and
// installs every ToInstall mod, and finally calls ProfileManager.SetDefault
// to make plan.To the active profile - in that order, matching
// doProfileSwitch exactly. sink may be nil.
//
// doProfileSwitch runs no install/uninstall hooks at all (unlike
// DeployProfile/UninstallMod), so ApplyProfileSwitch doesn't either - there
// is deliberately no hook plumbing in its signature or DeployOptions-style
// options struct, since profile switch takes no CLI flags beyond the target
// profile name.
//
// plan is executed EXACTLY as given - this method never re-plans or
// re-validates it against current state. A caller that computed plan some
// time ago (e.g. to show a user a preview) and only calls this later, after
// showing that preview, accepts whatever has changed in the interim as
// already baked into plan; PlanProfileSwitch's own doc comment documents
// why speculative plans are cheap enough to discard and recompute instead.
func (s *Service) ApplyProfileSwitch(ctx context.Context, game *domain.Game, plan *SwitchPlan, sink EventSink) (*SwitchResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &SwitchResult{}, err
	}
	defer release()
	return s.applyProfileSwitch(ctx, game, plan, sink)
}

func (s *Service) applyProfileSwitch(ctx context.Context, game *domain.Game, plan *SwitchPlan, sink EventSink) (*SwitchResult, error) {
	result := &SwitchResult{}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}

	// #81: a switch spans two profiles that may carry different explicit
	// link methods - the disable loop undeploys the FROM profile's
	// deployments (which were made with plan.From's method), while the
	// enable and install loops deploy into plan.To.
	fromInstaller, err := s.getInstallerForProfile(ctx, game, plan.From)
	if err != nil {
		return result, err
	}
	toInstaller, err := s.getInstallerForProfile(ctx, game, plan.To)
	if err != nil {
		return result, err
	}
	pm := s.NewProfileManager()

	totalDisable := len(plan.ToDisable)
	for idx := range plan.ToDisable {
		// Task 6 item d (cancel-then-drain): checked between mods, never
		// mid-file-operation - see DeployProfile's identical check.
		if err := ctx.Err(); err != nil {
			return result, err
		}

		im := plan.ToDisable[idx]
		scope := Scope{Op: OpSwitch, Index: idx + 1, Total: totalDisable, ModName: im.Name, Mod: &domain.ModReference{SourceID: im.SourceID, ModID: im.ID}}

		if err := fromInstaller.Uninstall(ctx, game, &im.Mod, plan.From); err != nil {
			msg := fmt.Sprintf("Warning: failed to undeploy %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: SwitchDisableNote, Detail: msg})
		}
		if err := s.setModEnabled(ctx, im.SourceID, im.ID, game.ID, plan.From, false); err != nil {
			msg := fmt.Sprintf("Warning: failed to update %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: SwitchDisableNote, Detail: msg})
		}

		result.Disabled++
		emit(ModEvent{Scope: scope, Phase: SwitchDisabled})
	}

	totalEnable := len(plan.ToEnable)
	for idx := range plan.ToEnable {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		im := plan.ToEnable[idx]
		scope := Scope{Op: OpSwitch, Index: idx + 1, Total: totalEnable, ModName: im.Name, Mod: &domain.ModReference{SourceID: im.SourceID, ModID: im.ID}}

		if err := toInstaller.Install(ctx, game, &im.Mod, plan.To); err != nil {
			msg := fmt.Sprintf("Warning: failed to deploy %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: SwitchEnableNote, Detail: msg})
			continue
		}
		if err := s.setModEnabled(ctx, im.SourceID, im.ID, game.ID, plan.To, true); err != nil {
			if errors.Is(err, domain.ErrModNotFound) {
				// im's row lives under a different profile (PlanProfileSwitch
				// admits such mods into ToEnable), so the UPDATE-only
				// SetModEnabled matched nothing; create the target-profile row
				// so the deployment we just made isn't orphaned (#60).
				row := im
				row.ProfileName = plan.To
				row.Enabled = true
				row.Deployed = true
				err = s.saveInstalledMod(ctx, &row)
			}
			if err != nil {
				msg := fmt.Sprintf("Warning: failed to update %s: %v", im.Name, err)
				result.Notes = append(result.Notes, msg)
				emit(StepEvent{Scope: scope, Phase: SwitchEnableNote, Detail: msg})
			}
		}

		result.Enabled++
		emit(ModEvent{Scope: scope, Phase: SwitchEnabled})
	}

	if totalInstall := len(plan.ToInstall); totalInstall > 0 {
		emit(StepEvent{Scope: Scope{Op: OpSwitch, Total: totalInstall}, Phase: SwitchInstalling})

		for idx, ref := range plan.ToInstall {
			if err := ctx.Err(); err != nil {
				return result, err
			}

			scope := Scope{Op: OpSwitch, Index: idx + 1, Total: totalInstall, Mod: &domain.ModReference{SourceID: ref.SourceID, ModID: ref.ModID}}
			emit(ModEvent{Scope: scope, Phase: SwitchInstallingMod})

			fail := func(reason string) {
				emit(ModEvent{Scope: scope, Phase: SwitchInstallError, Detail: reason})
			}

			mod, err := s.GetMod(ctx, ref.SourceID, game.ID, ref.ModID)
			if err != nil {
				fail(fmt.Sprintf("failed to fetch mod: %v", err))
				continue
			}
			scope.ModName = mod.Name

			files, err := s.GetModFiles(ctx, ref.SourceID, mod)
			if err != nil {
				fail(fmt.Sprintf("failed to get files: %v", err))
				continue
			}
			if len(files) == 0 {
				fail("no downloadable files")
				continue
			}

			filesToDownload, err := selectFilesForVersion(files, ref.FileIDs, ref.Version)
			if err != nil {
				fail(err.Error())
				continue
			}

			mod.Version = domain.EffectiveInstalledVersion(mod.Version, filesToDownload) // #94

			downloadedFileIDs := make([]string, 0, len(filesToDownload))
			for _, f := range filesToDownload {
				downloadedFileIDs = append(downloadedFileIDs, f.ID)
			}
			// #96 review finding 2: HasFileIDs (not bare Exists) - a version
			// directory can exist yet be only PARTIALLY populated by a
			// previous download run that broke off partway through a
			// multi-file mod; skipping the download on directory presence
			// alone would silently leave it that way forever. Round 2: the
			// check is by FILE ID (the per-file completion markers
			// commitStagedCacheWithMarker stamps), never by FileName - a
			// cache entry for an extracted archive holds member names that
			// match no DownloadableFile, so a name-based check would miss
			// every archive-based mod and redownload a complete cache.
			if !s.GetGameCache(game).HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, downloadedFileIDs) {
				downloadFailed := false
				for _, file := range filesToDownload {
					progressFn := func(e Event) {
						d, ok := e.(DownloadEvent)
						if !ok || d.TotalBytes <= 0 {
							return
						}
						emit(DownloadEvent{Scope: scope, Phase: SwitchDownloading, Percent: d.Percent})
					}
					if _, err := s.downloadMod(ctx, ref.SourceID, game, mod, file, progressFn); err != nil {
						emit(ModEvent{Scope: scope, Phase: SwitchDownloadFailed, Detail: fmt.Sprintf("download failed: %v", err)})
						downloadFailed = true
						break
					}
				}
				emit(StepEvent{Scope: scope, Phase: SwitchDownloadDone})

				if downloadFailed {
					continue
				}
			}

			// #96 convergence (review finding 1): a version-drift entry
			// whose prior installed row is actually live on disk must be
			// replaced (removing files the new version doesn't serve), not
			// just installed over - mirrors ApplyUpdate's Installer.Replace
			// semantics. prior.Deployed alone isn't enough: only Replace
			// when the OLD version's cache entry is still there for it to
			// read from (a corrupted/missing old cache falls back to a
			// bare Install, same as any other toInstall entry - Replace
			// would otherwise hard-fail with "old mod not in cache" and
			// abort convergence). The caveat, shared with the cmd twin
			// (cmd/lmm/profile.go's doProfileApply): without the old file
			// list, files the new version no longer serves stay behind as
			// stale deployments (`lmm verify` surfaces them) - strictly
			// better than failing to converge at all.
			key := domain.ModKey(ref.SourceID, ref.ModID)
			if prior, ok := plan.PriorVersions[key]; ok && prior.Deployed &&
				s.GetGameCache(game).Exists(game.ID, prior.SourceID, prior.ID, prior.Version) {
				if err := toInstaller.Replace(ctx, game, &prior.Mod, mod, plan.To); err != nil {
					fail(fmt.Sprintf("deploy failed: %v", err))
					continue
				}
			} else if err := toInstaller.Install(ctx, game, mod, plan.To); err != nil {
				fail(fmt.Sprintf("deploy failed: %v", err))
				continue
			}

			// Save to DB. Normalize GameID to the lmm game (not the
			// source-mapped value Service.GetMod may have stamped onto
			// mod.GameID for querying the source) so every DB read, which
			// queries by the lmm game ID, can find this row again.
			installedMod := &domain.InstalledMod{
				Mod:          *mod,
				ProfileName:  plan.To,
				UpdatePolicy: domain.UpdateNotify,
				Enabled:      true,
				Deployed:     true, // review finding 3: Install/Replace above just succeeded
				FileIDs:      downloadedFileIDs,
			}
			installedMod.Mod.GameID = game.ID
			if err := s.saveInstalledMod(ctx, installedMod); err != nil {
				fail(fmt.Sprintf("save failed: %v", err))
				continue
			}

			modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: downloadedFileIDs}
			if err := pm.UpsertMod(game.ID, plan.To, modRef); err != nil {
				msg := fmt.Sprintf("Warning: could not update profile: %v", err)
				result.Notes = append(result.Notes, msg)
				emit(StepEvent{Scope: scope, Phase: SwitchInstallNote, Detail: msg})
			}

			result.Installed++
			emit(ModEvent{Scope: scope, Phase: SwitchInstalled})
		}
	}

	if err := pm.SetDefault(game.ID, plan.To); err != nil {
		return result, fmt.Errorf("setting default profile: %w", err)
	}

	// #197 postsmoke fix: Warnings, not Notes - SwitchResult.Notes is
	// --verbose-gated in the CLI, so a sync failure here used to be
	// silent by default.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, plan.To); syncErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not sync merged pak: %v", syncErr))
	} else {
		result.Warnings = append(result.Warnings, syncWarnings...)
	}

	return result, nil
}

// --- ImportPlan/ApplyImport (Phase 6b Task 8) ---

// ImportPlan is the pure, displayable result of PlanImport: everything the
// pre-extraction CLI's pre-import preview (doProfileImport :416-478) needs to
// render before a caller decides whether/how to proceed (ApplyImport then
// actually executes one of these). Computed with zero side effects - it
// parses the given data and inspects existing DB/cache/profile state, but
// never writes anything.
type ImportPlan struct {
	// Profile is the parsed-but-not-yet-saved profile (ProfileManager.
	// ParseProfile's result) - its Name/Mods drive both the CLI's preview
	// print and ApplyImport's own save step.
	Profile *domain.Profile `json:"profile,omitempty"`

	// Installed holds every profile mod already installed (a DB row exists)
	// at the profile's own version (or with no version recorded in the
	// profile at all) AND cached at that exact version - nothing to do for
	// these. NeedsRedownload holds mods that must be re-fetched: a DB row
	// with no matching cache entry (installed somewhere, cache gone), or -
	// #138's convergence case, mirroring PlanProfileSwitch's #96 drift case -
	// a row installed at a DIFFERENT version than the imported profile
	// records, scheduled for reinstall at the profile's version (downgrades
	// included; each such ref also records the row being converged away from
	// in priorVersions). Missing holds mods with no DB row anywhere (checked
	// across EVERY saved profile for the game, not just the one being
	// imported into - doProfileImport's cross-profile scan, :428-438). All
	// three preserve profile.Mods' own order.
	Installed       []domain.ModReference `json:"installed"`
	NeedsRedownload []domain.ModReference `json:"needs_redownload"`
	Missing         []domain.ModReference `json:"missing"`

	// Exists reports whether a profile with this name is already saved for
	// the game - purely informational (e.g. so a caller can warn before even
	// attempting the save); ApplyImport does not consult it, instead letting
	// ProfileManager.ImportWithOptions' own existence check (driven by
	// ProfileImportOptions.Force) produce the authoritative error.
	Exists bool `json:"exists"`

	// data is the raw import bytes, preserved so ApplyImport can hand them
	// to ProfileManager.ImportWithOptions unchanged - PlanImport parses via
	// ParseProfile purely for preview, without persisting anything.
	data []byte

	// storedFileIDs maps domain.ModKey keys (for NeedsRedownload's cache-miss
	// entries only) to that mod's DB-recorded FileIDs - preserving
	// doProfileImport's :541-552 rule: a redownload uses the INSTALLED row's
	// own FileIDs, never the imported profile YAML's ref.FileIDs (which may
	// be empty or stale, since the row may have been updated - or reinstalled
	// under a different FileIDs set - after that profile was last exported).
	// A #138 version-drift entry is deliberately absent here: the stored
	// FileIDs describe the WRONG (installed) version, while the imported
	// ref's own FileIDs (if any) describe the target one - the same rule
	// PlanProfileSwitch's drift case applies.
	storedFileIDs map[string][]string

	// priorVersions maps domain.ModKey keys (for NeedsRedownload's #138
	// version-drift entries only) to the installed row being converged AWAY
	// from - the import twin of SwitchPlan.PriorVersions (see its doc
	// comment): ApplyImport's install loop needs it to know whether a LIVE
	// older deployment exists that must be replaced (removing files the new
	// version doesn't serve) rather than merely installed over. Private,
	// like storedFileIDs: pure plan-to-apply plumbing no preview renders.
	priorVersions map[string]domain.InstalledMod
}

// PlanImport parses data (an exported profile) and categorizes its mods
// against game's current installed/cache state, without saving anything or
// touching the network - mirrors doProfileImport's preview step
// (:411-459) exactly. ctx is accepted for API consistency with the rest of
// Service's methods (see PlanProfileSwitch's own doc comment for why a
// speculative, side-effect-free plan doesn't need it today).
func (s *Service) PlanImport(ctx context.Context, game *domain.Game, data []byte) (*ImportPlan, error) {
	pm := s.NewProfileManager()

	profile, err := pm.ParseProfile(data)
	if err != nil {
		return nil, fmt.Errorf("parsing profile: %w", err)
	}

	_, existErr := pm.Get(game.ID, profile.Name)
	exists := existErr == nil

	// installedData keeps each mod key's full installed row (doProfileImport
	// tracked only Version/FileIDs), needed to (a) check the cache at the
	// RIGHT version, (b) preserve the redownload FileIDs rule above, and
	// (c) record the prior row a #138 version-drift entry converges away
	// from (priorVersions needs the whole Mod for Installer.Replace).
	installedMods, _ := s.GetInstalledMods(ctx, game.ID, profile.Name)
	installedData := make(map[string]domain.InstalledMod)
	for _, im := range installedMods {
		key := domain.ModKey(im.SourceID, im.ID)
		installedData[key] = im
	}

	// Cross-profile scan (:428-438): a mod installed under some OTHER saved
	// profile still counts as "installed", not "missing". Errors from List/
	// GetInstalledMods are ignored, matching doProfileImport exactly (a
	// missing/unreadable profile simply contributes nothing).
	allProfiles, _ := pm.List(game.ID)
	for _, p := range allProfiles {
		mods, _ := s.GetInstalledMods(ctx, game.ID, p.Name)
		for _, im := range mods {
			key := domain.ModKey(im.SourceID, im.ID)
			if _, exists := installedData[key]; !exists {
				installedData[key] = im
			}
		}
	}

	var installed, needsRedownload, missing []domain.ModReference
	storedFileIDs := make(map[string][]string)
	var priorVersions map[string]domain.InstalledMod // #138 - see ImportPlan.priorVersions
	gameCache := s.GetGameCache(game)
	for _, ref := range profile.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		im, inDB := installedData[key]
		switch {
		case !inDB:
			missing = append(missing, ref)
		case ref.Version != "" && im.Version != ref.Version:
			// #138 convergence, mirroring PlanProfileSwitch's #96 drift
			// case: the imported profile names a different version than the
			// installed row - reinstall at the profile's version (downgrades
			// included). ref is passed as-is: its own FileIDs (if any)
			// describe the TARGET version; the installed row's describe the
			// wrong one (so no storedFileIDs entry). The installed row
			// itself is recorded in priorVersions so ApplyImport's install
			// loop can Replace a live older deployment instead of
			// installing over it.
			needsRedownload = append(needsRedownload, ref)
			if priorVersions == nil {
				priorVersions = make(map[string]domain.InstalledMod)
			}
			priorVersions[key] = im
		case gameCache.Exists(game.ID, ref.SourceID, ref.ModID, im.Version):
			installed = append(installed, ref)
		default:
			needsRedownload = append(needsRedownload, ref)
			storedFileIDs[key] = im.FileIDs
		}
	}

	return &ImportPlan{
		Profile:         profile,
		Installed:       installed,
		NeedsRedownload: needsRedownload,
		Missing:         missing,
		Exists:          exists,
		data:            data,
		storedFileIDs:   storedFileIDs,
		priorVersions:   priorVersions,
	}, nil
}

// ProfileImportOptions configures ApplyImport.
type ProfileImportOptions struct {
	// Force mirrors doProfileImport's --force: passed straight through to
	// ProfileManager.ImportWithOptions, allowing the save to overwrite an
	// already-saved profile of the same name instead of failing.
	Force bool
	// NoInstall mirrors --no-install: the install loop never runs at all
	// (ConfirmInstall is never even consulted - see its own doc comment),
	// and every pending mod is counted in ProfileImportResult.Skipped instead.
	NoInstall bool

	// ConfirmInstall, when non-nil and downloads are pending (and NoInstall
	// is unset), is called AFTER the profile is saved - mirroring the CLI's
	// own prompt position (doProfileImport's "\nDownload and install mods?
	// [Y/n]: " sits right after "\n✓ Imported profile: %s\n") - with the
	// full combined [NeedsRedownload..., Missing...] list. Returning false
	// skips the install loop entirely (every pending mod is counted in
	// Skipped, matching a declined prompt's zero-mutations outcome); nil
	// means proceed unconditionally, matching InstallOptions.ConfirmConflicts'
	// own "nil = proceed" convention.
	ConfirmInstall func(toDownload []domain.ModReference) bool
}

// ProfileImportResult reports the outcome of ApplyImport. As with every other
// flow's result type, every field is always recorded - there is no
// verbosity concept in core.
//
//   - Notes holds the install loop's sole --verbose-gated diagnostic (a
//     failed UpsertMod), matching ApplyProfileSwitch's SwitchInstallNote
//     convention; a caller wanting byte-identical pre-extraction output
//     should print each entry to stdout ONLY under --verbose, e.g.
//     `fmt.Printf("    %s\n", n)` (4-space indent).
//   - Warnings holds one "source:mod: reason" entry per failed mod (#131),
//     appended at the same point Failed is bumped - so an outcome-driven
//     caller keeps the reason and any remediation hint it carries
//     (e.g. #95's stored-files-gone message) after the live progress
//     line is gone.
//
// Every Notes entry is ALSO reported via the event stream at the exact
// point it is appended (ImportNote - see its DeployPhase doc comment), with
// Detail equal to the slice entry verbatim; likewise every Warnings entry
// has a corresponding ImportModFailed event with Detail equal to the bare
// reason - a live-printing caller (the CLI) must NOT batch-print Warnings
// afterward or it would double-report every failure.
//
// On error (a failed save), the returned result carries any diagnostics
// accumulated before the failure (none, today, since the save is the very
// first step) - callers should surface it alongside the error.
type ProfileImportResult struct {
	ProfileName string   `json:"profile_name"`
	Installed   int      `json:"installed"`
	Failed      int      `json:"failed"`
	Skipped     int      `json:"skipped"`
	Warnings    []string `json:"warnings,omitempty"`
	Notes       []string `json:"notes,omitempty"`
}

// ApplyImport executes a plan produced by PlanImport: saves the profile
// (ProfileManager.ImportWithOptions), then - unless there is nothing to
// download, NoInstall is set, or ConfirmInstall declines - downloads and
// installs every NeedsRedownload/Missing mod, in that order, matching
// doProfileImport exactly (:481-633). Since #138 the install loop also
// carries ApplyProfileSwitch's convergence machinery: a fully-cached target
// version (by per-file completion marker) deploys from cache without
// redownloading, and a version-drift entry with a live prior deployment is
// Replaced rather than installed over. sink may be nil.
//
// plan is executed EXACTLY as given - like PlanProfileSwitch/ApplyProfileSwitch,
// this method never re-plans or re-validates it against current state (see
// that pair's own doc comments for why a speculative plan is cheap enough to
// simply discard and recompute instead, for a caller that wants to guard
// against drift).
func (s *Service) ApplyImport(ctx context.Context, game *domain.Game, plan *ImportPlan, opts ProfileImportOptions, sink EventSink) (*ProfileImportResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &ProfileImportResult{}, err
	}
	defer release()
	return s.applyImport(ctx, game, plan, opts, sink)
}

func (s *Service) applyImport(ctx context.Context, game *domain.Game, plan *ImportPlan, opts ProfileImportOptions, sink EventSink) (*ProfileImportResult, error) {
	result := &ProfileImportResult{}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}

	pm := s.NewProfileManager()
	profile, err := pm.ImportWithOptions(plan.data, opts.Force)
	if err != nil {
		return result, fmt.Errorf("importing profile: %w", err)
	}
	result.ProfileName = profile.Name
	emit(StepEvent{Scope: Scope{Op: OpImport, ModName: profile.Name}, Phase: ImportSaved})

	toDownload := make([]domain.ModReference, 0, len(plan.NeedsRedownload)+len(plan.Missing))
	toDownload = append(toDownload, plan.NeedsRedownload...)
	toDownload = append(toDownload, plan.Missing...)

	if len(toDownload) == 0 {
		return result, nil
	}
	if opts.NoInstall {
		result.Skipped = len(toDownload)
		return result, nil
	}
	if opts.ConfirmInstall != nil && !opts.ConfirmInstall(toDownload) {
		result.Skipped = len(toDownload)
		return result, nil
	}

	installer, err := s.getInstallerForProfile(ctx, game, profile.Name)
	if err != nil {
		return result, err
	}
	total := len(toDownload)
	emit(StepEvent{Scope: Scope{Op: OpImport, Total: total}, Phase: ImportInstalling})

	for idx, ref := range toDownload {
		// Task 6 item d (cancel-then-drain): checked between mods, never
		// mid-file-operation - see DeployProfile/ApplyProfileSwitch's
		// identical check.
		if err := ctx.Err(); err != nil {
			return result, err
		}

		scope := Scope{Op: OpImport, Index: idx + 1, Total: total, Mod: &domain.ModReference{SourceID: ref.SourceID, ModID: ref.ModID}}
		emit(ModEvent{Scope: scope, Phase: ImportModInstalling})

		fail := func(reason string) {
			result.Failed++
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s:%s: %s", ref.SourceID, ref.ModID, reason))
			emit(ModEvent{Scope: scope, Phase: ImportModFailed, Detail: reason})
		}

		mod, err := s.GetMod(ctx, ref.SourceID, game.ID, ref.ModID)
		if err != nil {
			fail(fmt.Sprintf("failed to fetch mod: %v", err))
			continue
		}
		scope.ModName = mod.Name

		files, err := s.GetModFiles(ctx, ref.SourceID, mod)
		if err != nil {
			fail(fmt.Sprintf("failed to get files: %v", err))
			continue
		}
		if len(files) == 0 {
			fail("no downloadable files")
			continue
		}

		// Select files to download - use the DB-stored FileIDs for a
		// redownload, or the imported profile's own FileIDs for a fresh
		// install (:541-552's rule; see ImportPlan.storedFileIDs' doc
		// comment for why this can't just be ref.FileIDs uniformly).
		key := domain.ModKey(ref.SourceID, ref.ModID)
		var fileIDsToUse []string
		if stored, ok := plan.storedFileIDs[key]; ok {
			fileIDsToUse = stored
		} else if len(ref.FileIDs) > 0 {
			fileIDsToUse = ref.FileIDs
		}
		filesToDownload, err := selectFilesForVersion(files, fileIDsToUse, ref.Version)
		if err != nil {
			fail(err.Error())
			continue
		}

		mod.Version = domain.EffectiveInstalledVersion(mod.Version, filesToDownload) // #94

		downloadedFileIDs := make([]string, 0, len(filesToDownload))
		for _, f := range filesToDownload {
			downloadedFileIDs = append(downloadedFileIDs, f.ID)
		}
		// #138: cache-first, by per-file completion marker - the same guard
		// (and the same two review findings) as ApplyProfileSwitch's install
		// loop: HasFileIDs, not bare Exists (a version directory can exist
		// yet be only PARTIALLY populated by a broken-off download run), and
		// by FILE ID, never FileName (an extracted archive's cache entry
		// holds member names that match no DownloadableFile). Deploying from
		// cache matters most for exactly this flow's drift convergence: a
		// downgrade's archived file may have vanished upstream.
		if !s.GetGameCache(game).HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, downloadedFileIDs) {
			downloadFailed := false
			for _, file := range filesToDownload {
				if err := ctx.Err(); err != nil {
					return result, err
				}
				progressFn := func(e Event) {
					d, ok := e.(DownloadEvent)
					if !ok || d.TotalBytes <= 0 {
						return
					}
					emit(DownloadEvent{Scope: scope, Phase: ImportDownloading, Percent: d.Percent})
				}
				if _, err := s.downloadMod(ctx, ref.SourceID, game, mod, file, progressFn); err != nil {
					fail(fmt.Sprintf("download failed: %v", err))
					downloadFailed = true
					break
				}
			}
			emit(StepEvent{Scope: scope, Phase: ImportDownloadDone})

			if downloadFailed {
				continue
			}
		}

		// #138 convergence: a version-drift entry whose prior installed row
		// is actually live on disk must be replaced (removing files the new
		// version doesn't serve), not just installed over - the same gate,
		// with the same caveats, as ApplyProfileSwitch's install loop (see
		// its comment): only Replace when the OLD version's cache entry is
		// still there for it to read from; a corrupted/missing old cache
		// falls back to a bare Install rather than hard-failing convergence.
		if prior, ok := plan.priorVersions[key]; ok && prior.Deployed &&
			s.GetGameCache(game).Exists(game.ID, prior.SourceID, prior.ID, prior.Version) {
			if err := installer.Replace(ctx, game, &prior.Mod, mod, profile.Name); err != nil {
				fail(fmt.Sprintf("deploy failed: %v", err))
				continue
			}
		} else if err := installer.Install(ctx, game, mod, profile.Name); err != nil {
			fail(fmt.Sprintf("deploy failed: %v", err))
			continue
		}

		// Save to DB. Normalize GameID to the lmm game (see the comment on
		// ApplyProfileSwitch's own identical save site for why).
		installedMod := &domain.InstalledMod{
			Mod:          *mod,
			ProfileName:  profile.Name,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			FileIDs:      downloadedFileIDs,
			Deployed:     true, // installer.Install above just succeeded
		}
		installedMod.Mod.GameID = game.ID
		if err := s.saveInstalledMod(ctx, installedMod); err != nil {
			fail(fmt.Sprintf("save failed: %v", err))
			continue
		}

		modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: downloadedFileIDs}
		if err := pm.UpsertMod(game.ID, profile.Name, modRef); err != nil {
			msg := fmt.Sprintf("Warning: could not update profile: %v", err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: ImportNote, Detail: msg})
		}

		result.Installed++
		emit(ModEvent{Scope: scope, Phase: ImportModInstalled})
	}

	// #197 I3 fix: profile import deploys mods (installer.Install above) the
	// same way ApplyInstall/DeployProfile do - without this, an imported
	// profile's exmodz mods (zero per-mod deployment members of their own,
	// Task 2/3) put NO content in the game directory at all until some
	// OTHER flow happens to sync the merged pak.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profile.Name); syncErr != nil {
		msg := fmt.Sprintf("syncing merged pak: %v", syncErr)
		result.Warnings = append(result.Warnings, msg)
		emit(StepEvent{Scope: Scope{Op: OpImport}, Phase: ImportNote, Detail: msg})
	} else {
		for _, w := range syncWarnings {
			result.Warnings = append(result.Warnings, w)
			emit(StepEvent{Scope: Scope{Op: OpImport}, Phase: ImportNote, Detail: w})
		}
	}

	return result, nil
}
