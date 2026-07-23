package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// EnableResult reports the outcome of EnableMod. Changed is true iff the
// mod was actually deployed and flipped to enabled — false (not an error)
// when it was already enabled, mirroring EnableMod's pre-Task-6 (bool,
// error) return. Notes carries operational diagnostics using the same
// display-contract convention as UninstallResult/DeployResult (Task 2's
// convention, extended here in Task 6 item a for result-struct
// convergence): always empty today — EnableMod has no diagnostic-producing
// step — kept for parity with DisableResult and so a future EnableMod
// diagnostic wouldn't need another signature change.
type EnableResult struct {
	Changed bool
	Notes   []string
}

// DisableResult reports the outcome of DisableMod. Changed mirrors
// EnableResult.Changed. Notes carries the sole diagnostic DisableMod can
// produce — a non-fatal undeploy failure (see DisableMod's doc comment) —
// using the same historical-prefix-baked-into-the-text convention
// UninstallResult's doc comment documents: a caller wanting byte-identical
// pre-5a output should print each entry to stdout ONLY under --verbose,
// verbatim, e.g. `fmt.Printf("  %s\n", n)`.
type DisableResult struct {
	Changed bool
	Notes   []string
}

// EnableMod deploys an installed-but-disabled mod's files from the cache to
// the game directory and marks it enabled in the database. Returns a result
// with Changed false — not an error — if the mod was already enabled.
func (s *Service) EnableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*EnableResult, error) {
	mod, err := s.GetInstalledMod(sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mod %s: %w", modID, err)
	}

	if mod.Enabled {
		return &EnableResult{}, nil
	}

	if !s.GetGameCache(game).Exists(game.ID, sourceID, modID, mod.Version) {
		return nil, fmt.Errorf("mod not found in cache - try reinstalling with 'lmm install --id %s'", modID)
	}

	installer := s.GetInstaller(game)
	if err := installer.Install(ctx, game, &mod.Mod, profileName); err != nil {
		return nil, fmt.Errorf("failed to deploy mod: %w", err)
	}

	if err := s.SetModEnabled(sourceID, modID, game.ID, profileName, true); err != nil {
		return nil, fmt.Errorf("failed to update mod status: %w", err)
	}

	return &EnableResult{Changed: true}, nil
}

// DisableMod undeploys the mod's files from the game directory — the cache
// entry is kept so the mod can be re-enabled later without downloading again
// — and marks it disabled in the database. Returns a result with Changed
// false — not an error — if the mod was already disabled.
//
// Undeploy failures are treated as non-fatal: the game files may already
// have been removed manually, and refusing to record the user's intent to
// disable the mod would leave it stuck. This mirrors the pre-extraction CLI,
// which warned (under --verbose) but always continued to flip the DB state
// — DisableResult.Notes (Task 6 item a) restores that diagnostic for
// callers that want it, rather than discarding it as the (bool, error)
// signature this replaces was forced to.
func (s *Service) DisableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*DisableResult, error) {
	mod, err := s.GetInstalledMod(sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mod %s: %w", modID, err)
	}

	if !mod.Enabled {
		return &DisableResult{}, nil
	}

	result := &DisableResult{}
	installer := s.GetInstaller(game)
	if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
		// Non-fatal — see doc comment. Historical "Warning: " prefix baked
		// into the text itself, matching UninstallResult's own convention.
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: failed to undeploy some files: %v", err))
	}

	if err := s.SetModEnabled(sourceID, modID, game.ID, profileName, false); err != nil {
		return result, fmt.Errorf("failed to update mod status: %w", err)
	}

	result.Changed = true
	return result, nil
}

// UninstallOptions configures UninstallMod.
type UninstallOptions struct {
	KeepCache bool // --keep-cache: skip deleting the mod's cache entry

	// Hook plumbing, mirroring BatchOptions. Hooks and/or HookRunner may be
	// nil to skip hook execution entirely (e.g. --no-hooks).
	Hooks       *ResolvedHooks
	HookRunner  *HookRunner
	HookContext HookContext
	Force       bool // continue past a failing uninstall.before_* hook (warn instead of fail)

	// No verbosity concept lives here: core never gates or prints
	// diagnostics. UninstallResult.Notes and .Warnings are always fully
	// populated; it is the caller's (CLI's/TUI's) job to decide what to
	// display and under what conditions. See UninstallResult's doc comment.
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
	Warnings []string // unconditional, stderr, audience: operator/always-visible
	Notes    []string // --verbose-gated, stdout, audience: diagnostic detail
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
	mod, err := s.GetInstalledMod(sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mod %s: %w", modID, err)
	}

	result := &UninstallResult{}
	hookCtx := opts.HookContext

	if err := runHook(ctx, opts.HookRunner, &hookCtx, "uninstall.before_all", opts.Hooks.GetUninstallBeforeAll()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("uninstall.before_all hook failed: %w", err)
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("uninstall.before_all hook failed (forced): %v", err))
	}

	hookCtx.ModID = mod.ID
	hookCtx.ModName = mod.Name
	hookCtx.ModVersion = mod.Version
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "uninstall.before_each", opts.Hooks.GetUninstallBeforeEach()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("uninstall.before_each hook failed: %w", err)
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("uninstall.before_each hook failed (forced): %v", err))
	}

	installer := s.GetInstaller(game)
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

	if err := s.DeleteInstalledMod(mod.SourceID, modID, game.ID, profileName); err != nil {
		return result, fmt.Errorf("failed to remove mod record: %w", err)
	}

	if err := s.NewProfileManager().RemoveMod(game.ID, profileName, mod.SourceID, modID); err != nil {
		// Don't fail if not in profile. Always recorded, historical "Note: "
		// prefix baked into the text (see UninstallResult's doc comment).
		result.Notes = append(result.Notes, fmt.Sprintf("Note: %v", err))
	}

	if err := runHook(ctx, opts.HookRunner, &hookCtx, "uninstall.after_each", opts.Hooks.GetUninstallAfterEach()); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("uninstall.after_each hook failed: %v", err))
	}

	hookCtx.ModID = ""
	hookCtx.ModName = ""
	hookCtx.ModVersion = ""
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "uninstall.after_all", opts.Hooks.GetUninstallAfterAll()); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("uninstall.after_all hook failed: %v", err))
	}

	return result, nil
}

// runHook runs command (a hook script path) via runner if both are set,
// updating hookCtx.HookName first. No-op if runner is nil or command is
// empty (hooks disabled, or that particular hook isn't configured). Shared
// by UninstallMod and DeployProfile - hookName ("install.before_all",
// "uninstall.after_each", ...) is just a label passed through to the script
// environment, so one helper covers both hook namespaces.
func runHook(ctx context.Context, runner *HookRunner, hookCtx *HookContext, hookName, command string) error {
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
	// nil (the zero value) means "use the game's effective link method" via
	// Service.GetGameLinkMethod. A pointer is used, rather than a bare
	// domain.LinkMethod with its zero value as the "unset" sentinel, because
	// domain.LinkMethod's zero value (LinkSymlink) is itself a valid,
	// explicit choice - it cannot double as "no override" without losing
	// the ability to explicitly request symlink. See the task report.
	LinkMethod *domain.LinkMethod

	// ModID/SourceID restrict the deploy to a single mod (`lmm deploy
	// <mod-id>`). Both empty (the default) deploys every mod in profile
	// order, subject to All. SourceID selects which source's copy of ModID
	// to deploy - the CLI's --source flag, default "nexusmods".
	ModID    string
	SourceID string

	All bool // --all: include disabled mods in a full-profile deploy, or allow deploying a disabled ModID.

	// Hook plumbing, mirroring UninstallOptions. Hooks and/or HookRunner may
	// be nil to skip hook execution entirely (e.g. --no-hooks). The deploy
	// pass runs install.* hooks; the purge pass (when Purge is set) runs
	// uninstall.* hooks, matching the pre-extraction CLI's doDeploy/
	// purgeDeployedMods split.
	Hooks       *ResolvedHooks
	HookRunner  *HookRunner
	HookContext HookContext
	Force       bool // continue past a failing before_* hook (warn instead of fail)
}

// DeployPhase identifies what DeployProfile is doing for the mod named in a
// DeployProgress event (or, for DeployPurging, for the purge pass as a
// whole), letting callers (CLI, TUI) render phase-appropriate UI without
// needing to know how a deploy is actually carried out.
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
	// DeployFallbackUsed: ModName's stored file IDs were not found on the
	// source; falling back to the primary file.
	DeployFallbackUsed
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
	// DeployPhase enum (per the task brief: "reuse DeployProgress and its
	// phase-constant pattern - extend, don't fork") rather than introducing
	// a parallel SwitchProgress/SwitchPhase pair. ApplyProfileSwitch is a
	// behavior-preserving extraction of cmd/lmm/profile.go's doProfileSwitch;
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
	// SwitchFallbackUsed fires when a to-be-installed mod's stored file IDs
	// were not found on the source and the primary file was used instead,
	// mirroring doProfileSwitch's unconditional (NOT --verbose-gated)
	// "    Warning: stored file IDs not found, using primary".
	SwitchFallbackUsed
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
	// `fmt.Println()` after the loop runs unconditionally either way. A
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
	// readout (see DeployProgress's doc comment on those fields). The
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
	// InstallExtracting mirrors doInstall's unconditional "Extracting to
	// cache..." status line, fired once after the STRICT-path primary's
	// download(s) finish, before Install/Replace. The BATCH path never
	// prints this (batchInstallMods had no equivalent status line).
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
	UpdateBeforeEachForced
	// UpdateWarning fires for either of the update's two after_each hook
	// failures - uninstall.after_each (old version) or install.after_each
	// (new version) - mirroring applyUpdate's own hookErrors/
	// printHookWarnings pair, fired right after both hooks have run
	// (Replace already succeeded), in hook-run order (uninstall.after_each,
	// then install.after_each) - unlike DeployWarning/InstallWarning's
	// end-of-whole-run deferral, since applyUpdate itself prints these
	// immediately, well before its own DB-update steps below.
	UpdateWarning
	// UpdateNote fires when SetModLinkMethod fails after a successful
	// update - the sole --verbose-gated diagnostic in applyUpdate,
	// mirroring "  Warning: could not update link method: %v" (2-space
	// indent, prefix baked into Detail, matching SwitchDisableNote/
	// SwitchEnableNote's own convention).
	UpdateNote

	// --- PurgeProfile progress events (#61, TUI Phase 6 prep): the
	// standalone `lmm purge` command's flow, extending this same enum.
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
)

// DeployProgress reports incremental status during DeployProfile. Index and
// Total describe ModName's position among the mods being deployed (both
// zero for phases with no mod/count in scope - see each DeployPhase
// constant's doc comment). ModID accompanies ModName wherever a specific
// mod is in scope; for phases whose historical text carries no mod name at
// all (DeployNote's link-method/mark-deployed cases), it is the only
// attribution available. Detail and Percent are populated only for the
// phases documented on DeployPhase's constants; both are zero otherwise.
type DeployProgress struct {
	Index, Total int
	ModName      string
	ModID        string
	// SourceID is populated by ApplyProfileSwitch's install-loop events
	// (SwitchInstallingMod onward), where a mod's SourceID:ModID pair is the
	// only identity known before it has even been fetched (no ModName yet).
	// DeployProfile's own events never set it (zero value, ignored).
	SourceID string
	Phase    DeployPhase
	Detail   string
	Percent  float64

	// --- Phase 5b Task 2: ApplyInstall-only fields ---

	// Downloaded and TotalBytes carry the raw byte counts behind Percent for
	// the primary mod's own download phases (InstallDownloading) - unlike
	// DeployDownloading (gated on TotalBytes > 0 and Percent-only),
	// doInstall's downloadSelectedFiles prints a byte-count readout even
	// when the total size is unknown ("Downloaded %s" vs "%.1f%% (%s /
	// %s)"), so the CLI needs the raw numbers, not just Percent, to
	// reproduce that byte-identically. Zero for every other phase.
	Downloaded int64
	TotalBytes int64
	// File identifies which of the primary mod's selected files an
	// InstallDownload* event concerns, so the CLI can call its own
	// displayFileLabel(*File) to reproduce doInstall's exact file-name
	// formatting without core duplicating that cosmetic, CLI-only helper.
	// Populated only for InstallDownloadStarted/InstallDownloading/
	// InstallDownloadDone/InstallDownloadFailed/InstallChecksumComputed.
	File *domain.DownloadableFile

	// --- Fix wave 1 (dep-path fidelity): BATCH-mode-only fields, used
	// exclusively by the Install-batch events (InstallDepInstalling onward)
	// that fire when plan.Dependencies is non-empty - see ApplyInstall's doc
	// comment and task-2-report.md's "Fix wave 1" entry for the full trace
	// back to cmd/lmm/install.go's pre-extraction batchInstallMods. ---

	// ModVersion carries the mod's version for InstallDepInstalling's
	// restored "Installing: %s v%s" header text (batchInstallMods printed
	// the version; the strict/no-deps path's own headers are printed
	// CLI-side from data already in doInstall's scope, so this is a
	// batch-only field). Zero for every other phase.
	ModVersion string
	// FilesExtracted carries the batch-mode mod's own extracted-file count
	// for InstallDepInstalled's restored "  ✓ Installed (%d files)" text
	// (batchInstallMods' downloadResult.FilesExtracted) - distinct from
	// InstallResult.FilesDeployed, which only ever tracks the STRICT path's
	// primary. Zero for every other phase.
	FilesExtracted int
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
// Every entry in both slices is ALSO reported via the progress callback at
// the exact point it is appended (DeployBeforeAllForced/DeployNote/
// DeployWarning/PurgeWarning/PurgeNote - see each DeployPhase constant's
// doc comment for which), with Detail equal to the slice entry verbatim and
// the phase itself indicating which display contract above applies. A
// caller driving its console output entirely from progress events (as
// cmd/lmm's doDeploy does) gets pre-extraction-accurate positioning; the
// slices remain here, unconditionally, for callers that only want the
// final, order-independent summary.
//
// Skipped carries one "<mod name>: <reason>" entry per mod that did not
// deploy, for any reason (hook failure, download failure, install
// failure); the pre-extraction CLI printed each of these unconditionally
// as it happened; DeployProgress's DeployBeforeEachSkipped/
// DeployDownloadFailed/DeploySkipped events carry the same reason text in
// real time for callers that want to print them as they occur instead of
// (or in addition to) at the end.
//
// On error, the returned result carries any diagnostics accumulated before
// the failure; callers should surface them alongside the error.
type DeployResult struct {
	Deployed int
	Skipped  []string
	Warnings []string
	Notes    []string
}

// errNoDeployFiles mirrors cmd/lmm's errNoDownloadableFiles for the
// redeploy-after-cache-miss path. DeployProfile duplicates a small slice of
// cmd/lmm/profile.go's selectFilesToDownload/selectPrimaryFile logic here
// (see selectDeployFiles below) instead of importing it, because
// internal/core cannot import cmd/lmm and this task's scope explicitly
// excludes touching profile.go to hoist it out; see the task report.
var errNoDeployFiles = fmt.Errorf("no downloadable files")

// filterAndSortInstallFiles is PlanInstall's faithful port of
// cmd/lmm/install.go's filterAndSortFiles (duplicated rather than shared for
// the same reason selectDeployFiles duplicates selectFilesToDownload:
// internal/core cannot import cmd/lmm - see errNoDeployFiles above): unless
// showArchived, drops any file whose Category (case-insensitive) is
// ARCHIVED, OLD_VERSION, or DELETED, then stable-sorts the remainder by
// category priority - MAIN, then OPTIONAL, then UPDATE, then MISCELLANEOUS,
// then anything else (archived categories sort last, but they're already
// gone unless showArchived kept them). Same category sets, same order, same
// stable sort as the CLI, so PlanInstall's file-selection step (the one
// ported here) picks the identical file the CLI's doInstall would.
func filterAndSortInstallFiles(files []domain.DownloadableFile, showArchived bool) []domain.DownloadableFile {
	var filtered []domain.DownloadableFile
	for _, f := range files {
		category := strings.ToUpper(f.Category)
		if !showArchived && (category == "ARCHIVED" || category == "OLD_VERSION" || category == "DELETED") {
			continue
		}
		filtered = append(filtered, f)
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		return installFileCategoryPriority(filtered[i].Category) < installFileCategoryPriority(filtered[j].Category)
	})

	return filtered
}

// installFileCategoryPriority is filterAndSortInstallFiles' sort key,
// ported from cmd/lmm/install.go's fileCategoryPriority (lower sorts first).
func installFileCategoryPriority(category string) int {
	switch strings.ToUpper(category) {
	case "MAIN":
		return 0
	case "OPTIONAL":
		return 1
	case "UPDATE":
		return 2
	case "MISCELLANEOUS":
		return 3
	case "ARCHIVED", "OLD_VERSION", "DELETED":
		return 99
	default:
		return 50
	}
}

// selectDeployFiles picks the file(s) to (re)download for a cache-miss mod:
// the files matching storedFileIDs if any are found, else the primary file
// (first file with IsPrimary, or simply the first file), reporting whether
// it had to fall back. Mirrors cmd/lmm/profile.go's selectFilesToDownload.
func selectDeployFiles(files []domain.DownloadableFile, storedFileIDs []string) ([]*domain.DownloadableFile, bool, error) {
	if len(files) == 0 {
		return nil, false, errNoDeployFiles
	}
	primary := func() *domain.DownloadableFile {
		for i := range files {
			if files[i].IsPrimary {
				return &files[i]
			}
		}
		return &files[0]
	}
	if len(storedFileIDs) > 0 {
		idSet := make(map[string]bool, len(storedFileIDs))
		for _, id := range storedFileIDs {
			idSet[id] = true
		}
		var found []*domain.DownloadableFile
		for i := range files {
			if idSet[files[i].ID] {
				found = append(found, &files[i])
			}
		}
		if len(found) > 0 {
			return found, false, nil
		}
		return []*domain.DownloadableFile{primary()}, true, nil
	}
	return []*domain.DownloadableFile{primary()}, false, nil
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
// progress may be nil. When non-nil, it is called synchronously from this
// function for every notable event - see DeployPhase's constants for what
// each one means and what Detail/Percent carry.
func (s *Service) DeployProfile(ctx context.Context, game *domain.Game, profileName string, opts DeployOptions, progress func(DeployProgress)) (*DeployResult, error) {
	result := &DeployResult{}
	emit := func(p DeployProgress) {
		if progress != nil {
			progress(p)
		}
	}

	var enabledBeforePurge map[string]bool
	if opts.Purge {
		mods, err := s.GetInstalledMods(game.ID, profileName)
		if err != nil {
			return result, fmt.Errorf("getting installed mods: %w", err)
		}
		enabledBeforePurge = make(map[string]bool)
		for _, m := range mods {
			if m.Enabled {
				enabledBeforePurge[domain.ModKey(m.SourceID, m.ID)] = true
			}
		}
		if err := s.purgeForDeploy(ctx, game, profileName, mods, opts, result, emit); err != nil {
			return result, fmt.Errorf("purging mods: %w", err)
		}
	}

	linkMethod := s.GetGameLinkMethod(game)
	if opts.LinkMethod != nil {
		linkMethod = *opts.LinkMethod
	}
	installer := s.NewInstallerWithLinker(game, s.GetLinker(linkMethod))

	var modsToDeploy []*domain.InstalledMod
	if opts.ModID != "" {
		mod, err := s.GetInstalledMod(opts.SourceID, opts.ModID, game.ID, profileName)
		if err != nil {
			return result, fmt.Errorf("mod not found: %s", opts.ModID)
		}
		if !mod.Enabled && !opts.All {
			return result, fmt.Errorf("mod %s is disabled - use --all to deploy disabled mods, or enable it with 'lmm mod enable %s'", mod.Name, opts.ModID)
		}
		modsToDeploy = append(modsToDeploy, mod)
	} else {
		mods, err := s.GetInstalledModsInProfileOrder(game.ID, profileName)
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

	hookCtx := opts.HookContext
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.before_all", opts.Hooks.GetInstallBeforeAll()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("install.before_all hook failed: %w", err)
		}
		msg := fmt.Sprintf("install.before_all hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(DeployProgress{Phase: DeployBeforeAllForced, Detail: msg})
	}

	// deferredWarnings holds install.after_each (per mod, in loop order)
	// and install.after_all DeployWarning events: the pre-extraction CLI
	// printed these together, AFTER the profile-overrides warning below,
	// even though both hooks run earlier in the function - see
	// DeployWarning's doc comment. Emission (and therefore printing) is
	// deferred to preserve that order; the Warnings slice itself is still
	// appended to at the natural point, unchanged.
	var deferredWarnings []DeployProgress

	total := len(modsToDeploy)
	for idx, mod := range modsToDeploy {
		// Task 6 item d (cancel-then-drain): checked between mods, never
		// mid-file-operation - a cancelled ctx aborts here with whatever
		// result has accumulated so far (the partial-result convention -
		// see this function's doc comment and DeployResult's).
		if err := ctx.Err(); err != nil {
			return result, err
		}

		base := DeployProgress{Index: idx + 1, Total: total, ModName: mod.Name, ModID: mod.ID}

		hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
		if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.before_each", opts.Hooks.GetInstallBeforeEach()); err != nil {
			reason := fmt.Sprintf("install.before_each hook failed: %v", err)
			evt := base
			evt.Phase, evt.Detail = DeployBeforeEachSkipped, reason
			emit(evt)
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %s", mod.Name, reason))
			continue
		}

		if !s.GetGameCache(game).Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
			if skipped := s.redeployFromSource(ctx, game, mod, base, emit, result); skipped {
				continue
			}
		}

		if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
			msg := fmt.Sprintf("Warning: undeploy %s: %v", mod.Name, err)
			result.Notes = append(result.Notes, msg)
			evt := base
			evt.Phase, evt.Detail = DeployNote, msg
			emit(evt)
		}

		if err := installer.Install(ctx, game, &mod.Mod, profileName); err != nil {
			reason := err.Error()
			evt := base
			evt.Phase, evt.Detail = DeploySkipped, reason
			emit(evt)
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %s", mod.Name, reason))
			continue
		}

		if err := s.SetModLinkMethod(mod.SourceID, mod.ID, game.ID, profileName, linkMethod); err != nil {
			msg := fmt.Sprintf("Warning: could not update link method: %v", err)
			result.Notes = append(result.Notes, msg)
			evt := base
			evt.Phase, evt.Detail = DeployNote, msg
			emit(evt)
		}
		if err := s.SetModDeployed(mod.SourceID, mod.ID, game.ID, profileName, true); err != nil {
			msg := fmt.Sprintf("Warning: could not mark as deployed: %v", err)
			result.Notes = append(result.Notes, msg)
			evt := base
			evt.Phase, evt.Detail = DeployNote, msg
			emit(evt)
		}

		result.Deployed++
		evt := base
		evt.Phase = DeployDeployed
		emit(evt)

		if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.after_each", opts.Hooks.GetInstallAfterEach()); err != nil {
			msg := fmt.Sprintf("install.after_each hook failed for %s: %v", mod.ID, err)
			result.Warnings = append(result.Warnings, msg)
			evt := base
			evt.Phase, evt.Detail = DeployWarning, msg
			deferredWarnings = append(deferredWarnings, evt)
		}
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = "", "", ""
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.after_all", opts.Hooks.GetInstallAfterAll()); err != nil {
		msg := fmt.Sprintf("install.after_all hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		deferredWarnings = append(deferredWarnings, DeployProgress{Phase: DeployWarning, Detail: msg})
	}

	if profile, err := config.LoadProfile(s.configDir, game.ID, profileName); err == nil && len(profile.Overrides) > 0 {
		if err := ApplyProfileOverrides(game, profile); err != nil {
			msg := fmt.Sprintf("applying profile overrides: %v", err)
			result.Warnings = append(result.Warnings, msg)
			emit(DeployProgress{Phase: DeployWarning, Detail: msg})
		}
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
func (s *Service) redeployFromSource(ctx context.Context, game *domain.Game, mod *domain.InstalledMod, base DeployProgress, emit func(DeployProgress), result *DeployResult) bool {
	skip := func(reason string) bool {
		evt := base
		evt.Phase, evt.Detail = DeploySkipped, reason
		emit(evt)
		result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %s", mod.Name, reason))
		return true
	}

	redl := base
	redl.Phase = DeployRedownloading
	emit(redl)

	fetchedMod, err := s.GetMod(ctx, mod.SourceID, game.ID, mod.ID)
	if err != nil {
		return skip(fmt.Sprintf("failed to fetch: %v", err))
	}

	files, err := s.GetModFiles(ctx, mod.SourceID, fetchedMod)
	if err != nil || len(files) == 0 {
		return skip("no files available")
	}

	filesToDownload, usedFallback, err := selectDeployFiles(files, mod.FileIDs)
	if err != nil {
		return skip(err.Error())
	}
	if usedFallback {
		fb := base
		fb.Phase = DeployFallbackUsed
		emit(fb)
	}

	for _, file := range filesToDownload {
		progressFn := func(p DownloadProgress) {
			if p.TotalBytes > 0 {
				dl := base
				dl.Phase, dl.Percent = DeployDownloading, p.Percentage
				emit(dl)
			}
		}
		if _, err := s.DownloadMod(ctx, mod.SourceID, game, fetchedMod, file, progressFn); err != nil {
			reason := fmt.Sprintf("download failed: %v", err)
			evt := base
			evt.Phase, evt.Detail = DeployDownloadFailed, reason
			emit(evt)
			result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %s", mod.Name, reason))
			return true
		}
	}

	done := base
	done.Phase = DeployDownloadDone
	emit(done)

	return false
}

// purgeForDeploy undeploys every currently-installed mod in profileName
// before DeployProfile redeploys them, mirroring the pre-extraction CLI's
// purgeDeployedMods (used only by `lmm deploy --purge`). Since #61 it is a
// thin adapter over purgeMods - the single purge loop it shares with
// PurgeProfile (the standalone `lmm purge` command's flow). See
// DeployResult's doc comment for where each diagnostic ends up.
func (s *Service) purgeForDeploy(ctx context.Context, game *domain.Game, profileName string, mods []domain.InstalledMod, opts DeployOptions, result *DeployResult, emit func(DeployProgress)) error {
	return s.purgeMods(ctx, game, profileName, mods, purgeSpec{
		forDeploy: true,
		hooks:     opts.Hooks,
		runner:    opts.HookRunner,
		hookCtx:   opts.HookContext,
		force:     opts.Force,
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

	hooks   *ResolvedHooks
	runner  *HookRunner
	hookCtx HookContext
	force   bool

	emit     func(DeployProgress)
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
	if err := runHook(ctx, spec.runner, &hookCtx, "uninstall.before_all", spec.hooks.GetUninstallBeforeAll()); err != nil {
		if !spec.force {
			return fmt.Errorf("uninstall.before_all hook failed: %w", err)
		}
		msg := fmt.Sprintf("uninstall.before_all hook failed (forced): %v", err)
		*spec.warnings = append(*spec.warnings, msg)
		spec.emit(DeployProgress{Phase: DeployBeforeAllForced, Detail: msg})
	}

	installer := s.GetInstaller(game)
	spec.emit(DeployProgress{Phase: DeployPurging, Total: len(mods)})

	// deferredWarnings holds uninstall.after_each (per mod, in loop order)
	// and uninstall.after_all PurgeWarning events: both pre-#61 copies
	// accumulated these during/after the loop and only printed them
	// together, via printHookWarnings, once the whole loop had finished -
	// so emission is deferred to right after the loop, mirroring that.
	var deferredWarnings []DeployProgress

	total := len(mods)
	for idx, mod := range mods {
		if err := ctx.Err(); err != nil {
			return err
		}

		// modEvent builds a per-mod event: purge-command mode carries
		// Index/Total (a progress denominator for the TUI); deploy mode
		// keeps its historical event shape (ModName/ModID/Detail only).
		modEvent := func(phase DeployPhase, detail string) DeployProgress {
			if spec.forDeploy {
				return DeployProgress{Phase: phase, ModName: mod.Name, ModID: mod.ID, Detail: detail}
			}
			return DeployProgress{Phase: phase, Index: idx + 1, Total: total, ModName: mod.Name, ModID: mod.ID, Detail: detail}
		}

		hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
		if err := runHook(ctx, spec.runner, &hookCtx, "uninstall.before_each", spec.hooks.GetUninstallBeforeEach()); err != nil {
			// Divergence 1 of 4 (#61) - both sides skip the mod (it stays
			// deployed) but report differently. Deploy: a Warning with the
			// "during purge (not purged)" wording, pinned by
			// TestService_DeployProfile_PurgeBeforeEachSkip_WarningTextExact.
			// Purge: a Skipped entry (doPurge's failed++) + PurgeModSkipped,
			// pinned by TestService_PurgeProfile_BeforeEachSkip_*.
			if spec.forDeploy {
				msg := fmt.Sprintf("uninstall.before_each hook failed for %s during purge (not purged): %v", mod.Name, err)
				*spec.warnings = append(*spec.warnings, msg)
				spec.emit(modEvent(PurgeWarning, msg))
			} else {
				detail := fmt.Sprintf("uninstall.before_each hook failed: %v", err)
				*spec.skipped = append(*spec.skipped, fmt.Sprintf("%s: %s", mod.Name, detail))
				spec.emit(modEvent(PurgeModSkipped, detail))
			}
			continue
		}

		if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
			// Best-effort: files may have been manually removed.
			msg := fmt.Sprintf("⚠ %s - %v", mod.Name, err)
			*spec.notes = append(*spec.notes, msg)
			spec.emit(modEvent(PurgeNote, msg))
		}

		// Divergence 4 of 4 (#61): --uninstall (purge-command-only)
		// deletes the DB record and profile-YAML entry; everything else
		// marks the record not-deployed. A record-delete failure skips the
		// rest of the mod (doPurge's failed++ + continue), including its
		// after_each hook and PurgeModPurged.
		if spec.uninstall {
			if err := s.DeleteInstalledMod(mod.SourceID, mod.ID, game.ID, profileName); err != nil {
				msg := fmt.Sprintf("⚠ %s - failed to remove record: %v", mod.Name, err)
				*spec.notes = append(*spec.notes, msg)
				spec.emit(modEvent(PurgeNote, msg))
				*spec.skipped = append(*spec.skipped, fmt.Sprintf("%s: failed to remove record: %v", mod.Name, err))
				continue
			}
			if err := s.NewProfileManager().RemoveMod(game.ID, profileName, mod.SourceID, mod.ID); err != nil {
				msg := fmt.Sprintf("Note: %s - %v", mod.Name, err)
				*spec.notes = append(*spec.notes, msg)
				spec.emit(modEvent(PurgeNote, msg))
			}
		} else {
			if err := s.SetModDeployed(mod.SourceID, mod.ID, game.ID, profileName, false); err != nil {
				msg := fmt.Sprintf("⚠ %s - failed to mark as not deployed: %v", mod.Name, err)
				*spec.notes = append(*spec.notes, msg)
				spec.emit(modEvent(PurgeNote, msg))
			}
		}

		if err := runHook(ctx, spec.runner, &hookCtx, "uninstall.after_each", spec.hooks.GetUninstallAfterEach()); err != nil {
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
			deferredWarnings = append(deferredWarnings, modEvent(PurgeWarning, msg))
		}

		// Divergence 3 of 4 (#61): only the purge command counts and
		// announces per-mod completion (doPurge's "✓"/succeeded++); the
		// deploy pass's event stream stays byte-identical to pre-#61.
		if !spec.forDeploy {
			*spec.purged++
			spec.emit(modEvent(PurgeModPurged, ""))
		}
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = "", "", ""
	if err := runHook(ctx, spec.runner, &hookCtx, "uninstall.after_all", spec.hooks.GetUninstallAfterAll()); err != nil {
		msg := fmt.Sprintf("uninstall.after_all hook failed: %v", err)
		*spec.warnings = append(*spec.warnings, msg)
		deferredWarnings = append(deferredWarnings, DeployProgress{Phase: PurgeWarning, Detail: msg})
	}

	for _, w := range deferredWarnings {
		spec.emit(w)
	}

	linker.CleanupEmptyDirs(game.ModPath)
	spec.emit(DeployProgress{Phase: PurgeComplete})
	return nil
}

// PurgeOptions configures PurgeProfile.
type PurgeOptions struct {
	// Uninstall additionally deletes each purged mod's DB record and
	// profile-YAML entry (like uninstalling it), instead of just marking
	// it not deployed - `lmm purge --uninstall`.
	Uninstall bool

	// Hook plumbing, mirroring DeployOptions/InstallOptions: all four
	// uninstall.* hooks fire (purge is an uninstall-family operation).
	// Force continues past a failing uninstall.before_all hook (recorded
	// as a Warning) instead of aborting the purge.
	Hooks       *ResolvedHooks
	HookRunner  *HookRunner
	HookContext HookContext
	Force       bool
}

// PurgeResult reports the outcome of PurgeProfile. Warnings and Notes
// follow DeployResult's display contract (Warnings: unconditional stderr;
// Notes: --verbose-gated stdout, historical text baked in). Skipped holds
// one "<name>: <reason>" entry per mod that was NOT fully purged (a
// before_each-skipped mod, or an --uninstall record-delete failure);
// len(Skipped) is doPurge's historical `failed` counter, so the CLI's
// "Purged: N, Failed: M" summary comes from Purged and len(Skipped).
type PurgeResult struct {
	Purged   int
	Skipped  []string
	Warnings []string
	Notes    []string
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
// progress may be nil. Cancellation is honored between mods (the
// partial-result convention: the accumulated result comes back alongside
// ctx.Err()); one cancellation-behavior delta from the pre-extraction
// doPurge, which never checked ctx mid-loop.
func (s *Service) PurgeProfile(ctx context.Context, game *domain.Game, profileName string, mods []domain.InstalledMod, opts PurgeOptions, progress func(DeployProgress)) (*PurgeResult, error) {
	result := &PurgeResult{}
	err := s.purgeMods(ctx, game, profileName, mods, purgeSpec{
		uninstall: opts.Uninstall,
		hooks:     opts.Hooks,
		runner:    opts.HookRunner,
		hookCtx:   opts.HookContext,
		force:     opts.Force,
		emit: func(p DeployProgress) {
			if progress != nil {
				progress(p)
			}
		},
		warnings: &result.Warnings,
		notes:    &result.Notes,
		skipped:  &result.Skipped,
		purged:   &result.Purged,
	})
	return result, err
}

// SwitchPlan is the pure, displayable diff between the currently-active
// default profile and a target profile - computed by PlanProfileSwitch with
// zero side effects, so a caller (the CLI, or eventually the TUI) can render
// it (in a print block or a confirmation modal) before deciding whether to
// call ApplyProfileSwitch. This is a behavior-preserving extraction of
// cmd/lmm/profile.go's doProfileSwitch's diff computation (through its
// "Show changes" print block) - see the task report for the exact mapping.
//
// CRITICAL: this mirrors the CLI's OWN diff algorithm. (An older, unused
// ProfileManager.Switch implementation coexisted with it until #60 retired
// it - this flow is the only switch implementation now.)
type SwitchPlan struct {
	GameID, From, To string

	ToEnable  []domain.InstalledMod // installed+disabled (or installed under a different profile) -> enable, deployed under To
	ToDisable []domain.InstalledMod // enabled under From but absent from To -> disable, undeployed under From
	ToInstall []domain.ModReference // in To but not installed anywhere -> download+install (FileIDs preserved from the installed mod's own record when this is really a cache-miss redeploy - see PlanProfileSwitch)

	NoChanges     bool // To's mod set matches From's content-wise; only SetDefault is needed
	AlreadyActive bool // To is already the active default profile; nothing to plan
}

// PlanProfileSwitch computes the diff between game's currently-active
// default profile and target, without mutating anything (no DB writes, no
// filesystem changes, no deploys) - callers may call this speculatively (to
// render a confirmation modal) and discard the result without consequence.
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
	currentMods, _ := s.GetInstalledMods(game.ID, currentName)

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
	allMods, _ := s.GetInstalledMods(game.ID, target)
	for i := range allMods {
		allInstalled[domain.ModKey(allMods[i].SourceID, allMods[i].ID)] = &allMods[i]
	}
	for i := range currentMods {
		allInstalled[domain.ModKey(currentMods[i].SourceID, currentMods[i].ID)] = &currentMods[i]
	}

	var toDisable, toEnable []domain.InstalledMod
	var toInstall []domain.ModReference

	for key, im := range currentEnabled {
		if _, inTarget := targetKeys[key]; !inTarget {
			toDisable = append(toDisable, *im)
		}
	}

	for key, ref := range targetKeys {
		im, installed := allInstalled[key]
		switch {
		case !installed:
			toInstall = append(toInstall, ref)
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
		NoChanges: len(toDisable) == 0 && len(toEnable) == 0 && len(toInstall) == 0,
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
// Every Notes entry is ALSO reported via the progress callback at the exact
// point it is appended (SwitchDisableNote/SwitchEnableNote/SwitchInstallNote
// - see each DeployPhase constant's doc comment), with Detail equal to the
// slice entry verbatim.
//
// On error, the returned result carries any diagnostics/counts accumulated
// before the failure; callers should surface them alongside the error.
type SwitchResult struct {
	Disabled, Enabled, Installed int
	Notes                        []string
}

// ApplyProfileSwitch executes a plan produced by PlanProfileSwitch: disables
// every ToDisable mod, then enables every ToEnable mod, then downloads and
// installs every ToInstall mod, and finally calls ProfileManager.SetDefault
// to make plan.To the active profile - in that order, matching
// doProfileSwitch exactly. progress may be nil.
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
// The TUI's coreProvider.ApplyProfileSwitch (Task 6 item e) is exactly this
// caller: it re-plans immediately before calling this method, which is a
// SEPARATE PlanProfileSwitch call from whichever one built the confirmation
// modal the user actually saw - see that method's own doc comment for the
// resulting preview/apply drift this can introduce.
func (s *Service) ApplyProfileSwitch(ctx context.Context, game *domain.Game, plan *SwitchPlan, progress func(DeployProgress)) (*SwitchResult, error) {
	result := &SwitchResult{}
	emit := func(p DeployProgress) {
		if progress != nil {
			progress(p)
		}
	}

	installer := s.GetInstaller(game)
	pm := s.NewProfileManager()

	totalDisable := len(plan.ToDisable)
	for idx := range plan.ToDisable {
		// Task 6 item d (cancel-then-drain): checked between mods, never
		// mid-file-operation - see DeployProfile's identical check.
		if err := ctx.Err(); err != nil {
			return result, err
		}

		im := plan.ToDisable[idx]
		base := DeployProgress{Index: idx + 1, Total: totalDisable, ModName: im.Name, ModID: im.ID}

		if err := installer.Uninstall(ctx, game, &im.Mod, plan.From); err != nil {
			msg := fmt.Sprintf("Warning: failed to undeploy %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			evt := base
			evt.Phase, evt.Detail = SwitchDisableNote, msg
			emit(evt)
		}
		if err := s.SetModEnabled(im.SourceID, im.ID, game.ID, plan.From, false); err != nil {
			msg := fmt.Sprintf("Warning: failed to update %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			evt := base
			evt.Phase, evt.Detail = SwitchDisableNote, msg
			emit(evt)
		}

		result.Disabled++
		evt := base
		evt.Phase = SwitchDisabled
		emit(evt)
	}

	totalEnable := len(plan.ToEnable)
	for idx := range plan.ToEnable {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		im := plan.ToEnable[idx]
		base := DeployProgress{Index: idx + 1, Total: totalEnable, ModName: im.Name, ModID: im.ID}

		if err := installer.Install(ctx, game, &im.Mod, plan.To); err != nil {
			msg := fmt.Sprintf("Warning: failed to deploy %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			evt := base
			evt.Phase, evt.Detail = SwitchEnableNote, msg
			emit(evt)
			continue
		}
		if err := s.SetModEnabled(im.SourceID, im.ID, game.ID, plan.To, true); err != nil {
			if errors.Is(err, domain.ErrModNotFound) {
				// im's row lives under a different profile (PlanProfileSwitch
				// admits such mods into ToEnable), so the UPDATE-only
				// SetModEnabled matched nothing; create the target-profile row
				// so the deployment we just made isn't orphaned (#60).
				row := im
				row.ProfileName = plan.To
				row.Enabled = true
				row.Deployed = true
				err = s.SaveInstalledMod(&row)
			}
			if err != nil {
				msg := fmt.Sprintf("Warning: failed to update %s: %v", im.Name, err)
				result.Notes = append(result.Notes, msg)
				evt := base
				evt.Phase, evt.Detail = SwitchEnableNote, msg
				emit(evt)
			}
		}

		result.Enabled++
		evt := base
		evt.Phase = SwitchEnabled
		emit(evt)
	}

	if totalInstall := len(plan.ToInstall); totalInstall > 0 {
		emit(DeployProgress{Phase: SwitchInstalling, Total: totalInstall})

		for idx, ref := range plan.ToInstall {
			if err := ctx.Err(); err != nil {
				return result, err
			}

			base := DeployProgress{Index: idx + 1, Total: totalInstall, SourceID: ref.SourceID, ModID: ref.ModID}
			installingEvt := base
			installingEvt.Phase = SwitchInstallingMod
			emit(installingEvt)

			fail := func(reason string) {
				evt := base
				evt.Phase, evt.Detail = SwitchInstallError, reason
				emit(evt)
			}

			mod, err := s.GetMod(ctx, ref.SourceID, game.ID, ref.ModID)
			if err != nil {
				fail(fmt.Sprintf("failed to fetch mod: %v", err))
				continue
			}
			base.ModName = mod.Name

			files, err := s.GetModFiles(ctx, ref.SourceID, mod)
			if err != nil {
				fail(fmt.Sprintf("failed to get files: %v", err))
				continue
			}
			if len(files) == 0 {
				fail("no downloadable files")
				continue
			}

			filesToDownload, usedFallback, err := selectDeployFiles(files, ref.FileIDs)
			if err != nil {
				fail(err.Error())
				continue
			}
			if usedFallback && len(ref.FileIDs) > 0 {
				fallbackEvt := base
				fallbackEvt.Phase = SwitchFallbackUsed
				emit(fallbackEvt)
			}

			var downloadedFileIDs []string
			downloadFailed := false
			for _, file := range filesToDownload {
				progressFn := func(p DownloadProgress) {
					if p.TotalBytes > 0 {
						dl := base
						dl.Phase, dl.Percent = SwitchDownloading, p.Percentage
						emit(dl)
					}
				}
				if _, err := s.DownloadMod(ctx, ref.SourceID, game, mod, file, progressFn); err != nil {
					evt := base
					evt.Phase, evt.Detail = SwitchDownloadFailed, fmt.Sprintf("download failed: %v", err)
					emit(evt)
					downloadFailed = true
					break
				}
				downloadedFileIDs = append(downloadedFileIDs, file.ID)
			}
			doneEvt := base
			doneEvt.Phase = SwitchDownloadDone
			emit(doneEvt)

			if downloadFailed {
				continue
			}

			if err := installer.Install(ctx, game, mod, plan.To); err != nil {
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
				FileIDs:      downloadedFileIDs,
			}
			installedMod.Mod.GameID = game.ID
			if err := s.SaveInstalledMod(installedMod); err != nil {
				fail(fmt.Sprintf("save failed: %v", err))
				continue
			}

			modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: downloadedFileIDs}
			if err := pm.UpsertMod(game.ID, plan.To, modRef); err != nil {
				msg := fmt.Sprintf("Warning: could not update profile: %v", err)
				result.Notes = append(result.Notes, msg)
				evt := base
				evt.Phase, evt.Detail = SwitchInstallNote, msg
				emit(evt)
			}

			result.Installed++
			installedEvt := base
			installedEvt.Phase = SwitchInstalled
			emit(installedEvt)
		}
	}

	if err := pm.SetDefault(game.ID, plan.To); err != nil {
		return result, fmt.Errorf("setting default profile: %w", err)
	}

	return result, nil
}

// --- PlanInstall (Phase 5b Task 1) ---

// InstallPlan is the pure, displayable result of PlanInstall: everything the
// pre-extraction CLI's pre-install prompts (dependency tree, conflict
// warnings, "already installed" notice) and the TUI's future install modal
// need to render before a caller decides whether to proceed (Phase 5b Task 2
// adds ApplyInstall to actually execute one of these). Computed with zero
// side effects - see PlanInstall's doc comment.
type InstallPlan struct {
	SourceID, GameID, Profile string

	Mod domain.Mod // the mod that would be installed, freshly fetched via GetMod

	// Files is the file(s) that WOULD be downloaded: GetModFiles' result
	// after filterAndSortInstallFiles (doInstall's filterAndSortFiles,
	// ported - strips ARCHIVED/OLD_VERSION/DELETED unless showArchived, sorts
	// MAIN>OPTIONAL>UPDATE>MISCELLANEOUS>other), then the same non-interactive
	// default cmd/lmm/install.go's selectInstallFiles falls back to (the
	// primary file, or the sole/first file) absent --file or an interactive
	// choice - reusing selectDeployFiles rather than porting
	// selectInstallFiles verbatim, since selectInstallFiles's --file flag and
	// interactive prompt both consume a plan rather than being part of one
	// (see the task report). Always exactly one file in practice: neither
	// selectDeployFiles nor this non-interactive default ever picks more
	// than one without a stored/explicit multi-file selection.
	Files []domain.DownloadableFile

	// Dependencies is target's resolved, not-yet-installed dependency chain,
	// deepest dependency first (install order) - target itself is excluded
	// (it's Mod, above). Mirrors cmd/lmm/install.go's resolveDependencies
	// exactly, including one quirk worth calling out: every dependency is
	// fetched using the TOP-LEVEL SourceID field above, not each
	// ModReference's own SourceID - a dependency listed for a different
	// source therefore always ends up in MissingDependencies unless that
	// source happens to stamp the same SourceID onto the Mod it returns (see
	// resolveInstallDependencies). Empty (with a nil error) whenever the
	// source lacks the Dependencies capability, returns
	// source.ErrNotSupported, or Mod is a local (domain.SourceLocal) mod -
	// resolveDependencies swallows ANY GetDependencies error the same way,
	// degrading to "no dependencies" rather than failing the plan.
	Dependencies []domain.Mod

	// MissingDependencies records dependency references resolveDependencies
	// found but couldn't resolve (source fetch failure, or a SourceID
	// mismatch - see Dependencies) - the pre-extraction CLI's showInstallPlan
	// printed these as a warning, never a failure. Not part of the task
	// brief's directional API struct; added because the brief's own framing
	// ("output contains everything the CLI's pre-install prompts... need to
	// display") requires it to reproduce that warning - see the task report.
	MissingDependencies []domain.ModReference
	// CycleDetected mirrors resolveDependencies' cycleDetected: a circular
	// reference was found while resolving Dependencies (install order is
	// best-effort). Same rationale as MissingDependencies.
	CycleDetected bool

	// Conflicts lists files installing Mod would overwrite from OTHER
	// installed mods, exactly as installer.GetConflicts reports them - but
	// ONLY when Mod's exact (SourceID, ID, Version) is already cached:
	// GetConflicts inspects the cache's extracted file list, and PlanInstall
	// must never download to populate it (see the function doc comment). A
	// mod that has never been downloaded before therefore always reports
	// empty Conflicts here; this mirrors the pre-extraction CLI's own
	// confirmInstallConflicts, which likewise treats ANY GetConflicts error
	// (a cache-miss included) as "no conflicts, continue" rather than an
	// install-blocking failure - see the task report.
	Conflicts []Conflict

	// Replaces is the currently-installed row for (SourceID, Mod.ID,
	// Profile), if any - non-nil means installing this plan would use
	// Installer.Replace (or its reinstall-cache-transaction variants, an
	// Apply-time concern) rather than Installer.Install. Mirrors doInstall's
	// existingMod exactly: populated regardless of whether the installed
	// version matches Mod.Version, so both a same-version reinstall and a
	// version upgrade set this.
	Replaces *domain.InstalledMod

	// TotalDownloadBytes is the sum of Files' declared sizes, or -1 if any
	// selected file's size is unreported (Size <= 0, matching the
	// DownloadProgress convention used elsewhere in this file: only a
	// positive TotalBytes/Size is treated as "known").
	TotalDownloadBytes int64

	// ShowArchived is the showArchived value PlanInstall was called with -
	// stored on the plan (Phase 5b Task 2) so ApplyInstall can resolve each
	// Dependencies entry's own downloadable files (at apply time - see
	// Dependencies' doc comment) using the identical filter the CLI showed
	// the user at plan time, without a second, possibly-inconsistent
	// parameter on InstallOptions. "The plan is the contract."
	ShowArchived bool
}

// PlanInstall computes what installing (sourceID, modID) into profileName
// would do - the pure, read-only half of the pre-extraction CLI's doInstall
// (cmd/lmm/install.go), extracted with zero mutations so a caller (the CLI,
// or the TUI's future install modal) can render it and decide whether to
// proceed before Phase 5b Task 2's ApplyInstall executes it. See
// InstallPlan's doc comment for what each field means, and the task report
// for the exact mapping back to doInstall.
//
// Deliberately NOT reproduced here (both consume a plan rather than being
// part of one, matching PlanProfileSwitch's precedent):
//   - doInstall's interactive file picking / --file flag (selectInstallFiles)
//     and its "Install N mod(s)? [Y/n]" dependency confirm prompt - Files
//     always reflects the same non-interactive default cmd/lmm's own --yes
//     flag would pick; a CLI/TUI caller that resolves a different selection
//     overrides plan.Files before calling ApplyInstall.
//   - --no-deps: a caller that wants to skip Dependencies can simply ignore
//     or clear them before calling ApplyInstall.
//
// showArchived mirrors doInstall's --show-archived flag exactly: it is
// threaded straight into filterAndSortInstallFiles (the faithful port of
// cmd/lmm/install.go's filterAndSortFiles - same ARCHIVED/OLD_VERSION/DELETED
// filter set, same MAIN>OPTIONAL>UPDATE>MISCELLANEOUS>other sort), which runs
// BEFORE the "no downloadable files" check and BEFORE selectDeployFiles - so
// a mod whose files are all archived reports the CLI's exact error instead
// of a plan, and the no-IsPrimary fallback picks the CLI's post-sort file,
// not GetModFiles' raw-order first. This parameter exists so Task 2's CLI
// refit can pass installShowArchived straight through without re-porting
// filterAndSortFiles into cmd/lmm a second time - see the task report's Fix
// wave 1 for why a parameter (rather than a hardcoded false, or a separate
// options type/overload) is the shape picked here.
//
// Network reads (GetMod, GetDependencies, GetModFiles) are expected; no DB
// write, filesystem write, cache write, hook execution, or download ever
// happens here - see TestService_PlanInstall_PerformsZeroMutations.
func (s *Service) PlanInstall(ctx context.Context, game *domain.Game, profileName, sourceID, modID string, showArchived bool) (*InstallPlan, error) {
	mod, err := s.GetMod(ctx, sourceID, game.ID, modID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch mod: %w", err)
	}

	plan := &InstallPlan{
		SourceID:     sourceID,
		GameID:       game.ID,
		Profile:      profileName,
		Mod:          *mod,
		ShowArchived: showArchived,
	}

	existing, err := s.GetInstalledMod(sourceID, modID, game.ID, profileName)
	switch {
	case err == nil:
		plan.Replaces = existing
	case errors.Is(err, domain.ErrModNotFound):
		// Not installed anywhere for this profile - Replaces stays nil,
		// matching doInstall's own errors.Is(err, domain.ErrModNotFound)
		// branch exactly.
	default:
		return nil, fmt.Errorf("checking existing installed mod: %w", err)
	}

	// NOTE: kept for doInstall fidelity (cmd/lmm/install.go:478 gates
	// dependency resolution the same way); unreachable via any registered
	// source today - domain.SourceLocal is never a source.ModSource's own
	// ID (see internal/source/registry.go), only a marker other commands
	// (list/verify/import/uninstall) stamp onto locally-imported mods, so
	// GetMod's returned mod.SourceID here can never equal it in practice.
	if mod.SourceID != domain.SourceLocal {
		// installedMods error ignored, matching doInstall/PlanProfileSwitch's
		// own "a missing/unreadable profile is simply empty" convention.
		installedMods, _ := s.GetInstalledMods(game.ID, profileName)
		installedIDs := make(map[string]bool, len(installedMods))
		for _, im := range installedMods {
			installedIDs[domain.ModKey(im.SourceID, im.ID)] = true
		}
		plan.Dependencies, plan.MissingDependencies, plan.CycleDetected = s.resolveInstallDependencies(ctx, sourceID, mod, installedIDs)
	}

	files, err := s.GetModFiles(ctx, sourceID, mod)
	if err != nil {
		return nil, fmt.Errorf("failed to get mod files: %w", err)
	}
	files = filterAndSortInstallFiles(files, showArchived)
	// This explicit check is intentionally NOT redundant with
	// selectDeployFiles's own len==0 guard below: that guard returns
	// errNoDeployFiles ("no downloadable files"), which is NOT byte-identical
	// to doInstall's message - so it stays here to reproduce doInstall's
	// exact wording on the FILTERED list.
	if len(files) == 0 {
		return nil, fmt.Errorf("no downloadable files available for this mod")
	}
	selected, _, err := selectDeployFiles(files, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to select files: %w", err)
	}
	plan.Files = make([]domain.DownloadableFile, len(selected))
	var totalBytes int64
	unknownSize := false
	for i, f := range selected {
		plan.Files[i] = *f
		if f.Size <= 0 {
			unknownSize = true
			continue
		}
		totalBytes += f.Size
	}
	if unknownSize {
		plan.TotalDownloadBytes = -1
	} else {
		plan.TotalDownloadBytes = totalBytes
	}

	// Conflict detection mirrors confirmInstallConflicts exactly: ANY
	// GetConflicts error - including "mod not in cache" for a mod PlanInstall
	// has (by construction) never downloaded - degrades to "no conflicts
	// detected", never fails the plan. See Conflicts' doc comment.
	if conflicts, err := s.GetInstaller(game).GetConflicts(ctx, game, mod, profileName); err == nil {
		plan.Conflicts = conflicts
	}

	return plan, nil
}

// resolveInstallDependencies is PlanInstall's copy of cmd/lmm/install.go's
// resolveDependencies (duplicated rather than shared for the same reason
// selectDeployFiles duplicates cmd/lmm/profile.go's selectFilesToDownload:
// internal/core cannot import cmd/lmm, and hoisting the CLI helper out is
// outside this task's scope - see the task report): a depth-first,
// cycle-detecting traversal of target's dependency graph that returns
// resolved dependencies in install order (deepest first). Every fetch uses
// sourceID - NOT each domain.ModReference's own SourceID - matching
// serviceDepFetcher's own fixed-source behavior exactly (see Dependencies'
// doc comment). Already-installed dependencies (per installedIDs) are
// skipped; a dependency this can't resolve (source fetch failure, or a
// SourceID mismatch) is recorded in missing rather than failing the whole
// resolution; a circular reference sets cycleDetected and is otherwise
// skipped.
func (s *Service) resolveInstallDependencies(ctx context.Context, sourceID string, target *domain.Mod, installedIDs map[string]bool) (deps []domain.Mod, missing []domain.ModReference, cycleDetected bool) {
	visited := make(map[string]bool)
	stack := make(map[string]bool) // keys currently being visited (cycle detection)

	var collect func(mod *domain.Mod)
	collect = func(mod *domain.Mod) {
		key := domain.ModKey(mod.SourceID, mod.ID)
		if visited[key] {
			return
		}
		visited[key] = true
		stack[key] = true
		defer delete(stack, key)

		modDeps, err := s.GetDependencies(ctx, sourceID, mod)
		if err != nil {
			// Degrade to "no dependencies for this mod" - matches
			// resolveDependencies, which swallows ANY error here (a
			// capability gap, source.ErrNotSupported, or anything else).
			return
		}

		for _, ref := range modDeps {
			depKey := domain.ModKey(ref.SourceID, ref.ModID)

			switch {
			case installedIDs[depKey]:
				continue
			case stack[depKey]:
				cycleDetected = true
				continue
			case visited[depKey]:
				continue
			}

			gameIDForFetch := target.GameID
			if gameIDForFetch == "" {
				gameIDForFetch = mod.GameID
			}
			depMod, err := s.GetMod(ctx, sourceID, gameIDForFetch, ref.ModID)
			if err != nil {
				// Dependency not available on this source (e.g. an external
				// requirement like SKSE).
				missing = append(missing, ref)
				continue
			}
			if depMod.SourceID != "" && depMod.SourceID != ref.SourceID {
				// Listed for a different source than the one that actually
				// served it.
				missing = append(missing, ref)
				continue
			}
			if depMod.SourceID == "" {
				depMod.SourceID = ref.SourceID
			}

			// Recurse into transitive dependencies before recording this
			// one, so Dependencies ends up deepest-first (install order).
			collect(depMod)
			deps = append(deps, *depMod)
		}
	}

	collect(target)
	return deps, missing, cycleDetected
}

// --- ApplyInstall (Phase 5b Task 2) ---

// InstallOptions configures ApplyInstall.
type InstallOptions struct {
	// SkipVerify mirrors doInstall's --skip-verify: when true, a downloaded
	// file's checksum is neither saved (SaveFileChecksum) nor reported via
	// an InstallChecksumComputed event, matching downloadSelectedFiles' "if
	// !skipVerify && checksum != ..." gate exactly for every mod (primary
	// and dependencies alike - batchInstallMods honors the same flag).
	SkipVerify bool

	// Hook plumbing, mirroring UninstallOptions/DeployOptions. Hooks and/or
	// HookRunner may be nil to skip hook execution entirely (e.g.
	// --no-hooks).
	//
	// Force gates install.before_all (once, always) and, in the STRICT
	// (no-deps) path ONLY, the primary's own install.before_each - matching
	// doInstall's own single-mod code exactly (a failure aborts with an
	// error unless Force is set, in which case it is recorded as a Warning
	// and the install proceeds). In the BATCH (Dependencies-present) path,
	// NO mod's before_each - dependency or primary alike - is EVER
	// Force-gated: it unconditionally skips that one mod and continues,
	// matching batchInstallMods exactly (Fix wave 1 - see
	// task-2-report.md's "Fix wave 1" entry - restored this for the primary
	// too; pre-extraction doInstall delegated the WHOLE list, target
	// included, to batchInstallMods whenever Dependencies was non-empty).
	Hooks       *ResolvedHooks
	HookRunner  *HookRunner
	HookContext HookContext
	Force       bool

	// ConfirmConflicts gates the STRICT (no-deps) path's deploy step
	// (applyInstallPrimary), restoring the pre-extraction CLI's blocking
	// conflict prompt at its ORIGINAL position: AFTER the primary is
	// downloaded and extracted to cache and BEFORE it is deployed - the
	// exact point confirmInstallConflicts occupied in doInstall
	// (cmd/lmm/install.go), since installer.GetConflicts can only inspect a
	// mod's cache once something has actually been downloaded into it (see
	// InstallPlan.Conflicts' doc comment for why a pre-download PlanInstall
	// call can't do this for a mod that has never been cached before - the
	// C1 review finding this field fixes: conflicts had regressed into
	// PlanInstall alone, which silently missed every uncached mod's
	// conflicts and, for an already-cached one, prompted at the wrong
	// position).
	//
	// Called with the freshly-computed, non-empty conflict list ONLY when
	// !Force and ConfirmConflicts is non-nil - Force skips the check
	// entirely without ever calling it (matching doInstall's own "if
	// !installForce" gate), and a nil ConfirmConflicts likewise skips it
	// (proceeds silently), for a caller that doesn't want the STRICT path's
	// blocking behavior at all (the BATCH path - applyInstallBatchMod - has
	// its own separate, always-non-blocking inline warning and never
	// consults this field).
	//
	// Returning false aborts the install with the exact error
	// confirmInstallConflicts' decline produced ("installation cancelled"),
	// leaving the same state a decline left in doInstall: before_all/
	// before_each hooks already ran, the download is already cached (a
	// fresh/upgrade install's cache entry is left in place; a same-version
	// reinstall's staged reinstall-cache-transaction is rolled back via its
	// existing deferred Rollback, restoring the live cache/deployed files
	// exactly as they were), and nothing is deployed or saved to the DB/
	// profile.
	ConfirmConflicts func(conflicts []Conflict) bool
}

// InstallResult reports the outcome of ApplyInstall. As with DeployResult/
// UninstallResult/SwitchResult, every entry below is always recorded - there
// is no verbosity concept in core.
//
//   - Warnings holds diagnostics doInstall/batchInstallMods printed
//     unconditionally: install.before_all/before_each (STRICT-path primary
//     only, when forced), a failed SaveFileChecksum (note: unconditional,
//     NOT --verbose-gated - doInstall/batchInstallMods print this one to
//     stderr regardless), and install.after_each/after_all hook failures.
//     Callers should print each entry to stderr, unconditionally, e.g.
//     `fmt.Fprintf(os.Stderr, "Warning: %v\n", w)`.
//   - Notes holds diagnostics doInstall/batchInstallMods only printed under
//     --verbose: a failed profile-create, a failed UpsertMod, a failed
//     reinstall-cache-transaction commit, a failed old-cache cleanup after
//     a version upgrade (all STRICT-path), or - BATCH path only - a failed
//     Uninstall/cache-Delete while removing a mod's previous installation
//   - each already carrying its historical "Warning: " prefix baked into
//     the text, matching the pre-extraction CLI's exact wording; a caller
//     wanting byte-identical output should print each entry to stdout ONLY
//     under --verbose, e.g. `fmt.Printf("  %s\n", n)`.
//
// Every entry in both slices is ALSO reported via the progress callback at
// the exact point it is appended (InstallBeforeAllForced/
// InstallBeforeEachForced/InstallWarning/InstallNote - see each DeployPhase
// constant's doc comment), with Detail equal to the slice entry verbatim.
//
// On error, the returned result carries any diagnostics/counts accumulated
// before the failure; callers should surface them alongside the error.
type InstallResult struct {
	// Installed holds display names in install order: dependencies first,
	// then the primary. In the STRICT (no-deps) path, a primary failure is
	// FATAL - it returns an error instead of appending here. In the BATCH
	// (Dependencies-present) path, the primary follows the exact same
	// skip-and-continue semantics as every dependency (Fix wave 1 - see
	// task-2-report.md's "Fix wave 1" entry) - a primary failure there
	// populates Failed/Skipped below instead of returning an error.
	Installed []string
	// Skipped holds "<name>: <reason>" entries for any mod that failed in
	// the BATCH (Dependencies-present) path - dependency OR primary alike
	// (Fix wave 1 restored the primary's participation; see InstallOptions'
	// Force doc comment). Always empty in the STRICT (no-deps) path, since
	// a primary failure there returns an error instead.
	Skipped []string
	// Failed holds JUST the display names (no reason - see Skipped for
	// that) of every BATCH-path mod that failed, dependency or primary
	// alike, in the SAME order Skipped uses - mirrors batchInstallMods' own
	// `failed []string` accumulator, which the pre-extraction CLI's
	// restored terminal "--- Summary ---\nInstalled: %d\nFailed: %d (%s)\n"
	// block joins verbatim (task-2-report.md's Fix wave 1). Always empty
	// in the STRICT (no-deps) path.
	Failed []string

	// FilesDeployed is the number of files extracted for the STRICT path's
	// PRIMARY mod across all of plan.Files - mirrors doInstall's
	// totalFileCount / the pre-extraction CLI's final "Files deployed: %d"
	// line. Always 0 in the BATCH path (batchInstallMods' terminal summary
	// never printed a file count, only Installed/Failed - see Failed).
	FilesDeployed int

	Warnings []string
	Notes    []string
}

// ensureProfileExists creates profileName if it doesn't exist yet, matching
// doInstall/batchInstallMods' lazy profile-creation convention ("Ensure
// profile exists, create if needed") - failures are non-fatal (mirroring
// doInstall's own "Log but don't fail - mod is installed" comment) and
// reported by the caller via the returned error (nil on success or
// already-exists).
func ensureProfileExists(pm *ProfileManager, gameID, profileName string) error {
	if _, err := pm.Get(gameID, profileName); err != nil {
		if errors.Is(err, domain.ErrProfileNotFound) {
			if _, err := pm.Create(gameID, profileName); err != nil {
				return err
			}
		}
	}
	return nil
}

// reinstallCacheTransaction stages a same-version reinstall's freshly
// downloaded files in a temporary cache, separate from the live game cache,
// so a failure partway through (download, deploy, or DB save) can restore
// the ORIGINAL cached files exactly as they were - ported verbatim from
// cmd/lmm/install.go's identically-named type (Phase 5b Task 2 moves this
// into core since ApplyInstall, not the CLI, now owns the whole
// download-then-deploy-then-save sequence it coordinates; see the task
// report). Only ever used for the PRIMARY mod, and only when plan.Replaces
// is set AND its Version matches the mod being installed (a same-version
// reinstall) - a version upgrade downloads into a distinct cache path
// already (version is part of the cache key) and needs no staging.
type reinstallCacheTransaction struct {
	live      *cache.Cache
	snapshot  *cache.Cache
	staged    *cache.Cache
	tempDir   string
	gameID    string
	sourceID  string
	modID     string
	version   string
	activated bool
}

func prepareReinstallCacheTransaction(live *cache.Cache, gameID, sourceID, modID, version string) (*reinstallCacheTransaction, error) {
	tempDir, err := os.MkdirTemp("", "lmm-reinstall-cache-*")
	if err != nil {
		return nil, fmt.Errorf("creating cache snapshot: %w", err)
	}
	snapshot := cache.New(filepath.Join(tempDir, "snapshot"))
	staged := cache.New(filepath.Join(tempDir, "staged"))
	if err := live.CloneMod(snapshot, gameID, sourceID, modID, version); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("snapshotting existing cache: %w", err)
	}
	return &reinstallCacheTransaction{
		live:     live,
		snapshot: snapshot,
		staged:   staged,
		tempDir:  tempDir,
		gameID:   gameID,
		sourceID: sourceID,
		modID:    modID,
		version:  version,
	}, nil
}

func (s *reinstallCacheTransaction) Activate() error {
	if s == nil {
		return nil
	}
	if s.activated {
		return nil
	}
	if err := s.live.Delete(s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	if err := s.staged.CloneMod(s.live, s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	s.activated = true
	return nil
}

func (s *reinstallCacheTransaction) RestoreLive() error {
	if s == nil || !s.activated {
		return nil
	}
	if err := s.live.Delete(s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	if err := s.snapshot.CloneMod(s.live, s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	s.activated = false
	return nil
}

func (s *reinstallCacheTransaction) Rollback() error {
	if s == nil {
		return nil
	}
	if err := s.RestoreLive(); err != nil {
		return err
	}
	err := os.RemoveAll(s.tempDir)
	*s = reinstallCacheTransaction{}
	return err
}

func (s *reinstallCacheTransaction) Commit() error {
	if s == nil {
		return nil
	}
	err := os.RemoveAll(s.tempDir)
	*s = reinstallCacheTransaction{}
	return err
}

// ApplyInstall executes a plan produced by PlanInstall, gated on
// len(plan.Dependencies) - see the DeployPhase Install* constants' doc
// comments (starting at InstallBeforeAllForced) for the full restored-
// fidelity design this reproduces, and task-2-report.md's "Fix wave 1
// (dep-path fidelity)" entry for the review trace that drove it:
//
//   - Empty: the STRICT (no-deps) path - only plan.Mod installs, via
//     applyInstallPrimary's doInstall-derived single-mod mechanics
//     (Force-gated hooks, Install-or-Replace, SaveFileChecksum;
//     interactive/--file selection is the CALLER's job, applied to
//     plan.Files before this is ever called - but the blocking conflict
//     prompt is NOT: it fires INSIDE applyInstallPrimary itself, post-
//     download/pre-deploy, via opts.ConfirmConflicts - see that field's doc
//     comment for why a caller-side, plan.Conflicts-driven prompt can never
//     detect an uncached mod's conflicts, the C1 review finding this
//     restores fidelity for).
//   - Non-empty: the BATCH path - plan.Dependencies (in plan order) THEN
//     plan.Mod all install via applyInstallBatchMod, IDENTICALLY, matching
//     batchInstallMods' own "every mod in the list is treated the same"
//     design byte-for-byte - the primary is NOT special-cased here at all
//     (no Replace, no interactive selection, no blocking conflict prompt -
//     see applyInstallBatchMod's own doc comment).
//
// install.before_all runs once, before any mod is touched, in EITHER path
// (matching both doInstall's own single-mod code and batchInstallMods,
// which each had their own, functionally-identical, Force-gated
// install.before_all call). install.after_all runs once, at the very end:
// in the STRICT path, only if the primary's own install fully succeeded (an
// early return skips it entirely, matching doInstall's single-mod code); in
// the BATCH path, unconditionally once the loop finishes, since no per-mod
// failure there is ever fatal (matching batchInstallMods, which always
// reaches its own install.after_all call regardless of how many mods in
// its list failed). progress may be nil.
//
// On error, the returned result carries any diagnostics/Installed entries
// accumulated before the failure - callers should surface them alongside the
// error (see InstallResult's doc comment).
func (s *Service) ApplyInstall(ctx context.Context, game *domain.Game, plan *InstallPlan, opts InstallOptions, progress func(DeployProgress)) (*InstallResult, error) {
	result := &InstallResult{}
	emit := func(p DeployProgress) {
		if progress != nil {
			progress(p)
		}
	}

	if err := ctx.Err(); err != nil {
		return result, err
	}

	hookCtx := opts.HookContext
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.before_all", opts.Hooks.GetInstallBeforeAll()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("install.before_all hook failed: %w", err)
		}
		msg := fmt.Sprintf("install.before_all hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(DeployProgress{Phase: InstallBeforeAllForced, Detail: msg})
	}

	linkMethod := s.GetGameLinkMethod(game)
	pm := s.NewProfileManager()

	// deferredWarnings holds every install.after_each (BATCH path: every mod
	// in loop order, primary included; STRICT path: the primary's own) and
	// the final install.after_all warning, flushed together at the very end
	// - mirroring DeployProfile/purgeForDeploy's deferredWarnings pattern
	// (itself modeled on batchInstallMods' own printHookWarnings, which
	// accumulated hook errors across the WHOLE loop - deps and primary
	// alike - and printed them together only after everything else had
	// already happened).
	var deferredWarnings []DeployProgress

	if len(plan.Dependencies) > 0 {
		// --- BATCH path: every mod, primary included, treated identically. ---
		mods := make([]*domain.Mod, 0, len(plan.Dependencies)+1)
		for i := range plan.Dependencies {
			mods = append(mods, &plan.Dependencies[i])
		}
		primary := plan.Mod // local, addressable copy - distinct from plan.Mod
		mods = append(mods, &primary)

		total := len(mods)
		for idx, mod := range mods {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if warn := s.applyInstallBatchMod(ctx, game, plan, mod, idx, total, linkMethod, pm, opts, result, emit); warn != nil {
				deferredWarnings = append(deferredWarnings, *warn)
			}
		}
	} else {
		// --- STRICT path: only the primary, doInstall's own mechanics. ---
		afterEachWarning, err := s.applyInstallPrimary(ctx, game, plan, linkMethod, pm, opts, result, emit)
		if err != nil {
			return result, err
		}
		if afterEachWarning != nil {
			deferredWarnings = append(deferredWarnings, *afterEachWarning)
		}
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = "", "", ""
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.after_all", opts.Hooks.GetInstallAfterAll()); err != nil {
		msg := fmt.Sprintf("install.after_all hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		deferredWarnings = append(deferredWarnings, DeployProgress{Phase: InstallWarning, Detail: msg})
	}

	for _, w := range deferredWarnings {
		emit(w)
	}

	return result, nil
}

// applyInstallBatchMod installs one mod from the BATCH path's combined
// [Dependencies..., primary] list - a dependency OR the primary, treated
// COMPLETELY identically - matching cmd/lmm/install.go's pre-extraction
// batchInstallMods per-mod loop byte-for-byte (Fix wave 1 restored the
// primary's participation in this exact mechanism; Task 2's original design
// special-cased the primary onto applyInstallPrimary's strict mechanics even
// when Dependencies was non-empty - see task-2-report.md's "Fix wave 1"
// entry for the review trace this fixes). Any failure (hook, fetch, files,
// download, conflict aside, deploy, or save) skips this mod and continues -
// never Force-gated, never fatal to the overall ApplyInstall call, primary
// included. No Replace/reinstall-cache-transaction (an existing same-key
// install is uninstalled+cache-deleted first, then a fresh Install always),
// no interactive/--file file selection (always the filtered list's
// primary-or-first file, re-resolved here - plan.Files is never consulted),
// a non-blocking inline conflict warning (never a blocking prompt). Returns
// the install.after_each warning event to defer (nil if none), matching
// ApplyInstall's deferredWarnings convention.
func (s *Service) applyInstallBatchMod(ctx context.Context, game *domain.Game, plan *InstallPlan, mod *domain.Mod, idx, total int, linkMethod domain.LinkMethod, pm *ProfileManager, opts InstallOptions, result *InstallResult, emit func(DeployProgress)) *DeployProgress {
	base := DeployProgress{Index: idx + 1, Total: total, ModName: mod.Name, ModVersion: mod.Version, ModID: mod.ID, SourceID: mod.SourceID}
	skip := func(label, reason string) {
		evt := base
		evt.Phase, evt.Detail = InstallDepSkipped, fmt.Sprintf("%s: %s", label, reason)
		emit(evt)
		result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %s", mod.Name, reason))
		result.Failed = append(result.Failed, mod.Name)
	}

	installing := base
	installing.Phase = InstallDepInstalling
	emit(installing)

	hookCtx := opts.HookContext
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.before_each", opts.Hooks.GetInstallBeforeEach()); err != nil {
		skip("Skipped", fmt.Sprintf("install.before_each hook failed: %v", err))
		return nil
	}

	installer := s.GetInstaller(game)

	// mod.SourceID (NOT plan.SourceID) is used for every source call below,
	// matching batchInstallMods' own `sourceID := mod.SourceID` exactly -
	// this only ever differs from plan.SourceID in the SourceLocal edge
	// case InstallPlan.Dependencies' doc comment already documents as
	// unreachable via any registered source in practice.
	if existing, err := s.GetInstalledMod(mod.SourceID, mod.ID, game.ID, plan.Profile); err == nil && existing != nil {
		reinstalling := base
		reinstalling.Phase = InstallDepReinstalling
		emit(reinstalling)
		if err := installer.Uninstall(ctx, game, &existing.Mod, plan.Profile); err != nil {
			msg := fmt.Sprintf("Warning: could not remove old files: %v", err)
			result.Notes = append(result.Notes, msg)
			evt := base
			evt.Phase, evt.Detail = InstallNote, msg
			emit(evt)
		}
		if err := s.GetGameCache(game).Delete(game.ID, existing.SourceID, existing.ID, existing.Version); err != nil {
			msg := fmt.Sprintf("Warning: could not clear old cache: %v", err)
			result.Notes = append(result.Notes, msg)
			evt := base
			evt.Phase, evt.Detail = InstallNote, msg
			emit(evt)
		}
	}

	files, err := s.GetModFiles(ctx, mod.SourceID, mod)
	if err != nil {
		skip("Error", fmt.Sprintf("failed to get mod files: %v", err))
		return nil
	}
	files = filterAndSortInstallFiles(files, plan.ShowArchived)
	if len(files) == 0 {
		skip("Error", "no downloadable files available")
		return nil
	}
	selected, _, err := selectDeployFiles(files, nil)
	if err != nil {
		skip("Error", err.Error())
		return nil
	}
	file := selected[0]

	fileEvt := base
	fileEvt.Phase, fileEvt.File = InstallDepFileSelected, file
	emit(fileEvt)

	progressFn := func(p DownloadProgress) {
		if p.TotalBytes > 0 {
			dl := base
			dl.Phase, dl.Percent = InstallDepDownloading, p.Percentage
			emit(dl)
		}
	}
	downloadResult, err := s.DownloadMod(ctx, mod.SourceID, game, mod, file, progressFn)

	// Unconditional (success OR failure alike), mirroring batchInstallMods'
	// own `fmt.Println()` immediately after the download call returns -
	// see InstallDepDownloadDone's doc comment for why this precedes the
	// failure branch's own InstallDepSkipped event.
	done := base
	done.Phase = InstallDepDownloadDone
	emit(done)

	if err != nil {
		skip("Error", fmt.Sprintf("download failed: %v", err))
		return nil
	}

	if !opts.SkipVerify && downloadResult.Checksum != "" {
		evt := base
		evt.Phase, evt.Detail = InstallChecksumComputed, downloadResult.Checksum
		emit(evt)
	}

	if !opts.Force {
		if conflicts, err := installer.GetConflicts(ctx, game, mod, plan.Profile); err == nil && len(conflicts) > 0 {
			evt := base
			evt.Phase, evt.Detail = InstallDepConflictWarning, fmt.Sprintf("%d file conflict(s) - will overwrite", len(conflicts))
			emit(evt)
		}
	}

	if err := installer.Install(ctx, game, mod, plan.Profile); err != nil {
		skip("Error", fmt.Sprintf("deployment failed: %v", err))
		return nil
	}

	installedMod := &domain.InstalledMod{
		Mod:          *mod,
		ProfileName:  plan.Profile,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   linkMethod,
		FileIDs:      []string{file.ID},
	}
	installedMod.Mod.GameID = game.ID
	if err := s.SaveInstalledMod(installedMod); err != nil {
		skip("Error", fmt.Sprintf("failed to save mod: %v", err))
		return nil
	}

	if !opts.SkipVerify && downloadResult.Checksum != "" {
		if err := s.SaveFileChecksum(mod.SourceID, mod.ID, game.ID, plan.Profile, file.ID, downloadResult.Checksum); err != nil {
			msg := fmt.Sprintf("failed to save checksum: %v", err)
			result.Warnings = append(result.Warnings, msg)
			evt := base
			evt.Phase, evt.Detail = InstallWarning, msg
			emit(evt)
		}
	}

	if err := ensureProfileExists(pm, game.ID, plan.Profile); err != nil {
		msg := fmt.Sprintf("Warning: could not create profile: %v", err)
		result.Notes = append(result.Notes, msg)
		evt := base
		evt.Phase, evt.Detail = InstallNote, msg
		emit(evt)
	}
	modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: []string{file.ID}}
	if err := pm.UpsertMod(game.ID, plan.Profile, modRef); err != nil {
		msg := fmt.Sprintf("Warning: could not update profile: %v", err)
		result.Notes = append(result.Notes, msg)
		evt := base
		evt.Phase, evt.Detail = InstallNote, msg
		emit(evt)
	}

	result.Installed = append(result.Installed, mod.Name)
	installedEvt := base
	installedEvt.Phase, installedEvt.FilesExtracted = InstallDepInstalled, downloadResult.FilesExtracted
	emit(installedEvt)

	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.after_each", opts.Hooks.GetInstallAfterEach()); err != nil {
		msg := fmt.Sprintf("install.after_each hook failed for %s: %v", mod.ID, err)
		result.Warnings = append(result.Warnings, msg)
		evt := base
		evt.Phase, evt.Detail = InstallWarning, msg
		return &evt
	}
	return nil
}

// fileChecksum pairs a downloaded file's ID with its computed checksum, in
// download order - an ordered alternative to a map so
// applyInstallPrimary's later SaveFileChecksum loop is deterministic (the
// pre-extraction CLI's own map-based fileChecksums had no ordering
// guarantee across multiple files, so this is a harmless, if anything more
// correct, deviation - see the task report).
type fileChecksum struct {
	fileID, checksum string
}

// applyInstallPrimary installs plan.Mod - doInstall's OWN single-mod
// mechanics (Force-gated before_each, Install-or-Replace incl. the
// reinstall-cache-transaction for a same-version reinstall,
// SaveFileChecksum, --skip-verify). ONLY ever called from ApplyInstall's
// STRICT (no-deps) path - see ApplyInstall's doc comment - matching
// doInstall's own early return: whenever Dependencies was non-empty,
// pre-extraction doInstall delegated the WHOLE list, target included, to
// batchInstallMods instead (applyInstallBatchMod, in the BATCH path), and
// this function never ran at all for that mod (Fix wave 1 - see
// task-2-report.md's "Fix wave 1" entry - restored this; Task 2's original
// design incorrectly ran this unconditionally, primary included, even when
// Dependencies was non-empty). Returns the install.after_each warning event
// to defer (nil if none). A non-nil error is always fatal to ApplyInstall
// as a whole, matching doInstall's own early returns.
func (s *Service) applyInstallPrimary(ctx context.Context, game *domain.Game, plan *InstallPlan, linkMethod domain.LinkMethod, pm *ProfileManager, opts InstallOptions, result *InstallResult, emit func(DeployProgress)) (*DeployProgress, error) {
	mod := plan.Mod // local, addressable copy - distinct from plan.Mod
	base := DeployProgress{ModName: mod.Name, ModID: mod.ID, SourceID: mod.SourceID}

	hookCtx := opts.HookContext
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.before_each", opts.Hooks.GetInstallBeforeEach()); err != nil {
		if !opts.Force {
			return nil, fmt.Errorf("install.before_each hook failed: %w", err)
		}
		msg := fmt.Sprintf("install.before_each hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		evt := base
		evt.Phase, evt.Detail = InstallBeforeEachForced, msg
		emit(evt)
	}

	installer := s.GetInstaller(game)
	downloadCache := s.GetGameCache(game)

	var reinstallTxn *reinstallCacheTransaction
	if plan.Replaces != nil && plan.Replaces.Version == mod.Version {
		var txnErr error
		reinstallTxn, txnErr = prepareReinstallCacheTransaction(s.GetGameCache(game), game.ID, plan.Replaces.SourceID, plan.Replaces.ID, plan.Replaces.Version)
		if txnErr != nil {
			return nil, fmt.Errorf("preparing reinstall cache: %w", txnErr)
		}
		downloadCache = reinstallTxn.staged
		defer func() {
			if reinstallTxn != nil {
				_ = reinstallTxn.Rollback() //nolint:errcheck // best-effort cleanup on an already-erroring path
			}
		}()
	}

	var downloadedFileIDs []string
	var checksums []fileChecksum
	filesTotal := len(plan.Files)
	for i := range plan.Files {
		file := &plan.Files[i]

		started := base
		started.Phase, started.Index, started.Total, started.File = InstallDownloadStarted, i+1, filesTotal, file
		emit(started)

		progressFn := func(p DownloadProgress) {
			dl := base
			dl.Phase, dl.Index, dl.Total, dl.File = InstallDownloading, i+1, filesTotal, file
			dl.Percent, dl.Downloaded, dl.TotalBytes = p.Percentage, p.Downloaded, p.TotalBytes
			emit(dl)
		}

		downloadResult, dlErr := s.DownloadModToCache(ctx, downloadCache, plan.SourceID, game, &mod, file, progressFn)

		done := base
		done.Phase, done.Index, done.Total, done.File = InstallDownloadDone, i+1, filesTotal, file
		emit(done)

		if dlErr != nil {
			reason := fmt.Sprintf("download failed: %v", dlErr)
			evt := base
			evt.Phase, evt.Index, evt.Total, evt.File, evt.Detail = InstallDownloadFailed, i+1, filesTotal, file, reason
			emit(evt)
			if strings.Contains(dlErr.Error(), "third-party downloads") && mod.SourceURL != "" {
				return nil, fmt.Errorf("download unavailable via API")
			}
			return nil, fmt.Errorf("download failed: %w", dlErr)
		}

		if !opts.SkipVerify && downloadResult.Checksum != "" {
			evt := base
			evt.Phase, evt.Index, evt.Total, evt.File, evt.Detail = InstallChecksumComputed, i+1, filesTotal, file, downloadResult.Checksum
			emit(evt)
			checksums = append(checksums, fileChecksum{fileID: file.ID, checksum: downloadResult.Checksum})
		}

		result.FilesDeployed += downloadResult.FilesExtracted
		downloadedFileIDs = append(downloadedFileIDs, file.ID)
	}

	emit(DeployProgress{Phase: InstallExtracting, ModName: mod.Name, ModID: mod.ID})

	// Conflict confirmation restored to doInstall's ORIGINAL position (C1
	// review finding): AFTER the primary is downloaded/extracted to cache,
	// BEFORE it is deployed - installer.GetConflicts can only see what's
	// actually in the cache at this point, so this is the earliest point a
	// fresh (never-before-cached) mod's conflicts can be detected at all.
	// See InstallOptions.ConfirmConflicts' doc comment for the exact
	// Force/nil-callback gating and decline-state fidelity this reproduces.
	if !opts.Force && opts.ConfirmConflicts != nil {
		if conflicts, err := installer.GetConflicts(ctx, game, &mod, plan.Profile); err == nil && len(conflicts) > 0 {
			if !opts.ConfirmConflicts(conflicts) {
				return nil, fmt.Errorf("installation cancelled")
			}
		}
	}

	emit(DeployProgress{Phase: InstallDeploying, ModName: mod.Name, ModID: mod.ID})

	if plan.Replaces != nil {
		if reinstallTxn != nil {
			if err := reinstallTxn.Activate(); err != nil {
				return nil, fmt.Errorf("activating reinstall cache: %w", err)
			}
		}
		var replaceErr error
		if reinstallTxn != nil {
			replaceErr = installer.ReplaceWithOldCache(ctx, game, reinstallTxn.snapshot, &plan.Replaces.Mod, &mod, plan.Profile)
		} else {
			replaceErr = installer.Replace(ctx, game, &plan.Replaces.Mod, &mod, plan.Profile)
		}
		if replaceErr != nil {
			if reinstallTxn != nil {
				_ = reinstallTxn.RestoreLive()                                                                                                                //nolint:errcheck // best-effort recovery on an already-erroring path
				_ = installer.ReplaceWithCaches(ctx, game, reinstallTxn.snapshot, s.GetGameCache(game), &plan.Replaces.Mod, &plan.Replaces.Mod, plan.Profile) //nolint:errcheck // best-effort recovery
			}
			return nil, fmt.Errorf("deployment failed: %w", replaceErr)
		}
	} else if err := installer.Install(ctx, game, &mod, plan.Profile); err != nil {
		return nil, fmt.Errorf("deployment failed: %w", err)
	}

	installedMod := &domain.InstalledMod{
		Mod:          mod,
		ProfileName:  plan.Profile,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   linkMethod,
		FileIDs:      downloadedFileIDs,
	}
	installedMod.Mod.GameID = game.ID

	if err := s.SaveInstalledMod(installedMod); err != nil {
		if plan.Replaces != nil {
			if reinstallTxn != nil {
				_ = reinstallTxn.RestoreLive()                                                                                                //nolint:errcheck // best-effort recovery on an already-erroring path
				_ = installer.ReplaceWithCaches(ctx, game, reinstallTxn.staged, s.GetGameCache(game), &mod, &plan.Replaces.Mod, plan.Profile) //nolint:errcheck // best-effort recovery
			} else {
				_ = installer.Replace(ctx, game, &mod, &plan.Replaces.Mod, plan.Profile) //nolint:errcheck // best-effort recovery
			}
		} else {
			_ = installer.Uninstall(ctx, game, &mod, plan.Profile) //nolint:errcheck // best-effort recovery
		}
		return nil, fmt.Errorf("failed to save mod: %w", err)
	}
	if reinstallTxn != nil {
		if err := reinstallTxn.Commit(); err != nil {
			msg := fmt.Sprintf("Warning: could not finalize reinstall cache transaction: %v", err)
			result.Notes = append(result.Notes, msg)
			emit(DeployProgress{Phase: InstallNote, Detail: msg, ModName: mod.Name, ModID: mod.ID})
		}
		reinstallTxn = nil
	}

	for _, fc := range checksums {
		if err := s.SaveFileChecksum(plan.SourceID, mod.ID, game.ID, plan.Profile, fc.fileID, fc.checksum); err != nil {
			msg := fmt.Sprintf("failed to save checksum for file %s: %v", fc.fileID, err)
			result.Warnings = append(result.Warnings, msg)
			emit(DeployProgress{Phase: InstallWarning, Detail: msg, ModName: mod.Name, ModID: mod.ID})
		}
	}

	if err := ensureProfileExists(pm, game.ID, plan.Profile); err != nil {
		msg := fmt.Sprintf("Warning: could not create profile: %v", err)
		result.Notes = append(result.Notes, msg)
		emit(DeployProgress{Phase: InstallNote, Detail: msg, ModName: mod.Name, ModID: mod.ID})
	}
	modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: downloadedFileIDs}
	if err := pm.UpsertMod(game.ID, plan.Profile, modRef); err != nil {
		msg := fmt.Sprintf("Warning: could not update profile: %v", err)
		result.Notes = append(result.Notes, msg)
		emit(DeployProgress{Phase: InstallNote, Detail: msg, ModName: mod.Name, ModID: mod.ID})
	}

	if plan.Replaces != nil && plan.Replaces.Version != mod.Version {
		if err := s.GetGameCache(game).Delete(game.ID, plan.Replaces.SourceID, plan.Replaces.ID, plan.Replaces.Version); err != nil {
			msg := fmt.Sprintf("Warning: could not clear old cache: %v", err)
			result.Notes = append(result.Notes, msg)
			emit(DeployProgress{Phase: InstallNote, Detail: msg, ModName: mod.Name, ModID: mod.ID})
		}
	}

	result.Installed = append(result.Installed, mod.Name)
	emit(DeployProgress{Phase: InstallDone, ModName: mod.Name, ModID: mod.ID})

	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.after_each", opts.Hooks.GetInstallAfterEach()); err != nil {
		msg := fmt.Sprintf("install.after_each hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		return &DeployProgress{Phase: InstallWarning, Detail: msg, ModName: mod.Name, ModID: mod.ID}, nil
	}
	return nil, nil
}

// --- ApplyUpdate (Phase 5b Task 3) ---

// UpdateOptions configures ApplyUpdate. Unlike InstallOptions/DeployOptions,
// there is no before_all/after_all hook plumbing at all - applyUpdate never
// ran that pair (see ApplyUpdate's doc comment) - so Force here gates ONLY
// the two before_each hooks (uninstall.before_each for the old version,
// install.before_each for the new one), matching applyUpdate's own
// near-identical Force checks exactly.
type UpdateOptions struct {
	// Hook plumbing, mirroring UninstallOptions/DeployOptions/InstallOptions.
	// Hooks and/or HookRunner may be nil to skip hook execution entirely
	// (e.g. --no-hooks).
	Hooks       *ResolvedHooks
	HookRunner  *HookRunner
	HookContext HookContext
	// Force: continue past a failing uninstall.before_each/install.before_each
	// hook (warn instead of fail), matching applyUpdate's own --force gate.
	Force bool
}

// UpdateApplyResult reports the outcome of ApplyUpdate. As with
// DeployResult/UninstallResult/SwitchResult/InstallResult, every entry below
// is always recorded - there is no verbosity concept in core.
//
//   - Applied holds a single "<name> <old version> → <new version>" entry
//     (matching what the CLI prints, e.g. "SkyUI 5.1 → 5.2") once the WHOLE
//     sequence - download, hooks, Replace, and all three DB/profile writes -
//     has succeeded. Empty on any failure; ApplyUpdate applies exactly one
//     domain.Update per call (the CLI's own update loop calls it once per
//     mod), so this is never more than a single entry.
//   - Warnings holds diagnostics applyUpdate printed unconditionally:
//     uninstall.before_each/install.before_each (when forced), and
//     uninstall.after_each/install.after_each hook failures (always
//     non-fatal). Callers should print each entry to stderr,
//     unconditionally, e.g. `fmt.Fprintf(os.Stderr, "Warning: %v\n", w)`.
//   - Notes holds the sole diagnostic applyUpdate only printed under
//     --verbose: a failed SetModLinkMethod, with the historical "Warning: "
//     prefix baked into the text already (matching applyUpdate's exact
//     wording); a caller wanting byte-identical output should print it to
//     stdout ONLY under --verbose, e.g. `fmt.Printf("  %s\n", n)`.
//
// Every entry in both slices is ALSO reported via the progress callback at
// the exact point it is appended (UpdateBeforeEachForced/UpdateWarning/
// UpdateNote - see each DeployPhase constant's doc comment), with Detail
// equal to the slice entry verbatim.
//
// On error, the returned result carries any diagnostics accumulated before
// the failure; callers should surface them alongside the error.
type UpdateApplyResult struct {
	Applied  []string
	Warnings []string
	Notes    []string
}

// ApplyUpdate applies upd to the installed mod it references
// (upd.InstalledMod), following cmd/lmm/update.go's pre-extraction
// applyUpdate ordering exactly: GetMod (the new version) -> GetModFiles ->
// resolve FileIDReplacements -> download -> hooks -> installer.Replace ->
// ApplyModUpdate -> SetModLinkMethod -> UpsertMod. This is a
// behavior-preserving extraction - see the task report for the full mapping.
//
// FileIDReplacements resolution mirrors applyUpdate exactly: each of the
// installed mod's own FileIDs is looked up in upd.FileIDReplacements; a hit
// substitutes the new (superseding) file ID, a miss retains the ORIGINAL id
// verbatim (never silently dropped) - selectDeployFiles' own primary-file
// fallback only kicks in afterward, if NONE of the resulting IDs are found
// among the new version's available files at all.
//
// A download failure returns immediately - before any hook runs, before
// Replace, before any DB/profile write - so the old version is left
// deployed and every row untouched, matching applyUpdate's own bare early
// return. Installer.Replace never touches the cache (only the game
// directory and deployed-file tracking - see installer.go), so the OLD
// version's cache entry always survives an update; ApplyModUpdate records
// PreviousVersion/PreviousFileIDs before overwriting version/FileIDs - both
// preconditions `lmm update rollback` (doUpdateRollback, NOT extracted by
// this task - see the task report) depends on.
//
// Hook failure semantics mirror applyUpdate's own two, independently
// Force-gated before_each hooks (uninstall.before_each for the OLD mod,
// install.before_each for the NEW mod: fatal unless Force is set, in which
// case a Warning is recorded and the update proceeds) and its two always-
// non-fatal after_each hooks (uninstall.after_each, install.after_each -
// both recorded as Warnings regardless of Force, printed immediately after
// Replace, well before the DB/profile writes below - see UpdateWarning's
// doc comment).
//
// A failure to write ApplyModUpdate or UpsertMod triggers the same
// best-effort compensating actions applyUpdate itself performed (a reverse
// Installer.Replace to restore the old deployment, plus - for UpsertMod - a
// RollbackModVersion to undo the DB version swap first); a failure to write
// SetModLinkMethod is NOT rolled back, matching applyUpdate exactly (it only
// ever produced a --verbose-gated Note).
//
// progress may be nil. On error, the returned result carries any
// diagnostics accumulated before the failure - callers should surface them
// alongside the error (see UpdateApplyResult's doc comment).
func (s *Service) ApplyUpdate(ctx context.Context, game *domain.Game, profileName string, upd domain.Update, opts UpdateOptions, progress func(DeployProgress)) (*UpdateApplyResult, error) {
	result := &UpdateApplyResult{}
	emit := func(p DeployProgress) {
		if progress != nil {
			progress(p)
		}
	}

	mod := upd.InstalledMod // local, addressable copy - distinct from upd.InstalledMod
	newVersion := upd.NewVersion
	base := DeployProgress{ModName: mod.Name, ModID: mod.ID, SourceID: mod.SourceID}

	newMod, err := s.GetMod(ctx, mod.SourceID, game.ID, mod.ID)
	if err != nil {
		return result, fmt.Errorf("fetching new version: %w", err)
	}
	if newMod.Version != newVersion {
		newMod.Version = newVersion
	}

	files, err := s.GetModFiles(ctx, mod.SourceID, newMod)
	if err != nil {
		return result, fmt.Errorf("getting mod files: %w", err)
	}
	if len(files) == 0 {
		return result, fmt.Errorf("no downloadable files available")
	}

	effectiveFileIDs := mod.FileIDs
	if len(upd.FileIDReplacements) > 0 {
		effectiveFileIDs = make([]string, len(mod.FileIDs))
		for i, fid := range mod.FileIDs {
			if newID, ok := upd.FileIDReplacements[fid]; ok {
				effectiveFileIDs[i] = newID
			} else {
				effectiveFileIDs[i] = fid
			}
		}
	}
	filesToDownload, _, err := selectDeployFiles(files, effectiveFileIDs)
	if err != nil {
		return result, fmt.Errorf("selecting files to download: %w", err)
	}

	var downloadedFileIDs []string
	for _, file := range filesToDownload {
		progressFn := func(p DownloadProgress) {
			if p.TotalBytes > 0 {
				dl := base
				dl.Phase, dl.Percent = UpdateDownloading, p.Percentage
				emit(dl)
			}
		}
		if _, err := s.DownloadMod(ctx, mod.SourceID, game, newMod, file, progressFn); err != nil {
			return result, fmt.Errorf("downloading update: %w", err)
		}
		downloadedFileIDs = append(downloadedFileIDs, file.ID)
	}
	emit(DeployProgress{Phase: UpdateDownloadDone, ModName: mod.Name, ModID: mod.ID, SourceID: mod.SourceID})

	// Task 6 item d (cancel-then-drain): checked between the download step
	// above and the hook/deploy (Replace) steps below, at minimum - a
	// cancelled ctx aborts here, before running any before_each hook or
	// touching the deployed files, leaving the OLD version fully deployed
	// and untouched (the partial-result convention - see this function's
	// doc comment).
	if err := ctx.Err(); err != nil {
		return result, err
	}

	hookCtx := opts.HookContext
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "uninstall.before_each", opts.Hooks.GetUninstallBeforeEach()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("uninstall.before_each hook failed: %w", err)
		}
		msg := fmt.Sprintf("uninstall.before_each hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		evt := base
		evt.Phase, evt.Detail = UpdateBeforeEachForced, msg
		emit(evt)
	}

	linkMethod := s.GetGameLinkMethod(game)
	installer := s.GetInstaller(game)

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = newMod.ID, newMod.Name, newMod.Version
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.before_each", opts.Hooks.GetInstallBeforeEach()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("install.before_each hook failed: %w", err)
		}
		msg := fmt.Sprintf("install.before_each hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		evt := base
		evt.Phase, evt.Detail = UpdateBeforeEachForced, msg
		emit(evt)
	}

	if err := installer.Replace(ctx, game, &mod.Mod, newMod, profileName); err != nil {
		return result, fmt.Errorf("deploying update: %w", err)
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "uninstall.after_each", opts.Hooks.GetUninstallAfterEach()); err != nil {
		msg := fmt.Sprintf("uninstall.after_each hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		evt := base
		evt.Phase, evt.Detail = UpdateWarning, msg
		emit(evt)
	}
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = newMod.ID, newMod.Name, newMod.Version
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.after_each", opts.Hooks.GetInstallAfterEach()); err != nil {
		msg := fmt.Sprintf("install.after_each hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		evt := base
		evt.Phase, evt.Detail = UpdateWarning, msg
		emit(evt)
	}

	if err := s.ApplyModUpdate(mod.SourceID, mod.ID, game.ID, profileName, newVersion, downloadedFileIDs); err != nil {
		_ = installer.Replace(ctx, game, newMod, &mod.Mod, profileName) //nolint:errcheck // best-effort recovery on an already-erroring path
		return result, fmt.Errorf("updating database: %w", err)
	}

	if err := s.SetModLinkMethod(mod.SourceID, mod.ID, game.ID, profileName, linkMethod); err != nil {
		msg := fmt.Sprintf("Warning: could not update link method: %v", err)
		result.Notes = append(result.Notes, msg)
		evt := base
		evt.Phase, evt.Detail = UpdateNote, msg
		emit(evt)
	}

	pm := s.NewProfileManager()
	modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: newVersion, FileIDs: downloadedFileIDs}
	if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
		_ = s.RollbackModVersion(mod.SourceID, mod.ID, game.ID, profileName) //nolint:errcheck // best-effort recovery on an already-erroring path
		_ = installer.Replace(ctx, game, newMod, &mod.Mod, profileName)      //nolint:errcheck // best-effort recovery on an already-erroring path
		return result, fmt.Errorf("updating profile: %w", err)
	}

	result.Applied = append(result.Applied, fmt.Sprintf("%s %s → %s", mod.Name, mod.Version, newVersion))
	return result, nil
}
