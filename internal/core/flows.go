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

// EnableMod deploys an installed-but-disabled mod's files from the cache to
// the game directory and marks it enabled in the database. Returns
// (false, nil) — not an error — if the mod was already enabled.
func (s *Service) EnableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (bool, error) {
	mod, err := s.GetInstalledMod(sourceID, modID, game.ID, profileName)
	if err != nil {
		return false, fmt.Errorf("getting installed mod %s: %w", modID, err)
	}

	if mod.Enabled {
		return false, nil
	}

	if !s.GetGameCache(game).Exists(game.ID, sourceID, modID, mod.Version) {
		return false, fmt.Errorf("mod not found in cache - try reinstalling with 'lmm install --id %s'", modID)
	}

	installer := s.GetInstaller(game)
	if err := installer.Install(ctx, game, &mod.Mod, profileName); err != nil {
		return false, fmt.Errorf("failed to deploy mod: %w", err)
	}

	if err := s.SetModEnabled(sourceID, modID, game.ID, profileName, true); err != nil {
		return false, fmt.Errorf("failed to update mod status: %w", err)
	}

	return true, nil
}

// DisableMod undeploys the mod's files from the game directory — the cache
// entry is kept so the mod can be re-enabled later without downloading again
// — and marks it disabled in the database. Returns (false, nil) — not an
// error — if the mod was already disabled.
//
// Undeploy failures are treated as non-fatal: the game files may already
// have been removed manually, and refusing to record the user's intent to
// disable the mod would leave it stuck. This mirrors the pre-extraction CLI,
// which warned (under --verbose) but always continued to flip the DB state.
func (s *Service) DisableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (bool, error) {
	mod, err := s.GetInstalledMod(sourceID, modID, game.ID, profileName)
	if err != nil {
		return false, fmt.Errorf("getting installed mod %s: %w", modID, err)
	}

	if !mod.Enabled {
		return false, nil
	}

	installer := s.GetInstaller(game)
	_ = installer.Uninstall(ctx, game, &mod.Mod, profileName) //nolint:errcheck // best-effort undeploy; see doc comment

	if err := s.SetModEnabled(sourceID, modID, game.ID, profileName, false); err != nil {
		return false, fmt.Errorf("failed to update mod status: %w", err)
	}

	return true, nil
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
	// DeployPurging fires once, before any purge-phase mod is touched, when
	// Purge is set and there is at least one installed mod to purge. Total
	// is the number of mods being purged; Index and ModName are zero/empty.
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
	// PurgeWarning fires wherever a --purge pass appends an entry to
	// DeployResult.Warnings: a skipped uninstall.before_each mod (fires
	// inline, per mod, as it happens), or a failed uninstall.after_each/
	// after_all hook (fires after the whole purge loop has finished, in
	// mod order then after_all - mirroring the pre-extraction
	// purgeDeployedMods, which accumulated these and printed them
	// together, after every per-mod line, via printHookWarnings).
	PurgeWarning
	// PurgeNote fires wherever a --purge pass appends an entry to
	// DeployResult.Notes for a specific mod (a failed undeploy, or a
	// failed SetModDeployed(false)), inline, immediately after that
	// operation - mirroring the pre-extraction purgeDeployedMods's
	// --verbose-gated "⚠ " lines.
	PurgeNote
	// PurgeComplete fires once, after a non-empty --purge pass has
	// finished everything (including its own hook warnings) but before
	// DeployProfile moves on to gathering mods to deploy. It carries no
	// data; a caller wanting byte-identical pre-extraction output prints
	// exactly one blank line here - purgeDeployedMods's own final
	// `fmt.Println()`, which the initial extraction had misplaced
	// immediately after the purge header instead of at the end of the
	// purge phase.
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

	// --- Phase 5b Task 2: ApplyInstall progress events. ApplyInstall
	// unifies two historically-DIVERGENT pre-extraction execution engines
	// (see the task report for the full trace):
	//
	//   - doInstall's OWN single-mod code (cmd/lmm/install.go, reached only
	//     when there are no dependencies to install) - Force-gated
	//     before_all/before_each, Install-or-Replace (incl. the
	//     reinstall-cache-transaction for a same-version reinstall),
	//     interactive/--file file selection, a blocking conflict-confirm
	//     prompt, SaveFileChecksum, --skip-verify.
	//   - batchInstallMods (cmd/lmm/install.go), which is what doInstall
	//     ACTUALLY delegated dependency installation to whenever
	//     Dependencies was non-empty - before_each is NEVER Force-gated
	//     (a failure always just skips that one mod and continues), no
	//     Replace path (always a fresh Install; a same-key existing mod is
	//     uninstalled+cache-deleted first), no interactive file selection
	//     (always the primary-or-first file), conflicts are a non-blocking
	//     inline warning (never a prompt).
	//
	// ApplyInstall's per-mod loop applies the FIRST set of semantics to the
	// primary (plan.Mod) ALWAYS - even when Dependencies is non-empty - and
	// the SECOND set to every entry in Dependencies. This is a deliberate
	// unification/simplification (the pre-extraction asymmetry - the
	// primary silently downgrading to batchInstallMods' simpler mechanics
	// merely because it happened to have dependencies - was an accident of
	// code structure, not a deliberate design choice), not a literal port of
	// batchInstallMods' own "treat every mod in the list identically"
	// behavior. See the task report for the full justification; none of the
	// brief's mandatory byte-identical CLI tests exercise the
	// dependencies-present EXECUTION phase's exact text (only the
	// pre-Apply plan/prompt, which IS byte-identical).

	// InstallBeforeAllForced fires once, immediately, when install.before_all
	// fails and Force is set - mirrors DeployBeforeAllForced/
	// PurgeWarning's "forced" role. No mod in scope.
	InstallBeforeAllForced
	// InstallBeforeEachForced fires when the PRIMARY mod's install.before_each
	// hook fails and Force is set (a forced warning, not a fatal error) -
	// mirrors doInstall's own before_each Force-gate exactly. ModName/ModID
	// identify the primary. Dependencies never fire this phase - see
	// InstallDepSkipped.
	InstallBeforeEachForced

	// InstallDepInstalling fires once per dependency, before before_each
	// even runs - the only point at which a caller can render a per-
	// dependency "starting" header (mirrors, in spirit rather than exact
	// text, batchInstallMods' "[%d/%d] Installing: %s v%s" - see the task
	// report for why the exact text is a deliberate deviation here).
	// Index/Total count among Dependencies only, matching InstallDepSkipped.
	InstallDepInstalling
	// InstallDepSkipped fires whenever a DEPENDENCY is skipped for any
	// reason (hook failure, fetch/files/download/deploy/save failure) -
	// unconditional, never Force-gated, matching batchInstallMods exactly.
	// Index/Total count among Dependencies only (the primary is never
	// included in this count - a deliberate deviation from
	// batchInstallMods' shared counter across every mod in its list, part
	// of the unification above). ModName/ModID identify the dependency;
	// Detail is the reason (unprefixed - the CLI's handler adds "  Skipped:
	// ").
	InstallDepSkipped
	// InstallDepConflictWarning fires when a dependency's files (already
	// downloaded/cached at this point) would overwrite files from another
	// installed mod and Force is NOT set - a non-blocking, informational
	// warning only (batchInstallMods never prompts for dependencies,
	// unlike the primary's plan.Conflicts-driven CLI confirm prompt).
	// Detail is "%d file conflict(s) - will overwrite".
	InstallDepConflictWarning
	// InstallDepDownloading mirrors batchInstallMods' dependency download
	// progress readout (Percent only, gated on a known total size - no
	// byte-count fallback line, unlike the primary's InstallDownloading).
	InstallDepDownloading
	// InstallDepInstalled fires once a dependency has been fully installed
	// (downloaded, deployed, saved, profile-upserted).
	InstallDepInstalled

	// InstallDownloadStarted fires once per one of the PRIMARY's selected
	// files (plan.Files), before it begins downloading - mirrors
	// downloadSelectedFiles' "\n[%d/%d] Downloading %s...\n" (or, for a
	// single file, "\nDownloading %s...\n"). File identifies which (for the
	// CLI's own displayFileLabel call); Index/Total count among plan.Files.
	InstallDownloadStarted
	// InstallDownloading mirrors the primary's per-tick download progress -
	// Downloaded/TotalBytes/Percent carry the raw numbers so the CLI can
	// reproduce its exact byte-count/percent readout (see DeployProgress's
	// doc comment on those fields).
	InstallDownloading
	// InstallDownloadDone fires once a file's download attempt finishes -
	// success OR failure alike, mirroring downloadSelectedFiles' `
	// fmt.Println()` that runs unconditionally right after the download
	// call returns, before branching on its error.
	InstallDownloadDone
	// InstallDownloadFailed fires when a file download fails; Detail
	// carries "download failed: %v" (the CLI checks Detail for the
	// "third-party downloads" substring itself, mirroring doInstall's own
	// check, to print the manual-install notice using the plan's own
	// Mod.SourceURL/ID - already in the CLI's enclosing scope, so it isn't
	// duplicated onto the event).
	InstallDownloadFailed
	// InstallChecksumComputed fires once per successfully-downloaded
	// primary file, only when a checksum was computed and !SkipVerify -
	// Detail carries the full (untruncated) checksum; the CLI applies its
	// own truncateChecksum.
	InstallChecksumComputed
	// InstallExtracting mirrors doInstall's unconditional "Extracting to
	// cache..." status line, fired once after the primary's download(s)
	// finish, before Install/Replace.
	InstallExtracting
	// InstallDeploying mirrors "Deploying to game directory...", fired once
	// right before Install/Replace.
	InstallDeploying
	// InstallDone fires once the primary mod has been fully installed
	// (deployed, saved, checksum stored, profile upserted).
	InstallDone

	// InstallNote fires wherever ApplyInstall appends an entry to
	// InstallResult.Notes (a failed profile-create, UpsertMod,
	// reinstall-cache-transaction commit, or old-cache cleanup) - the
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
// doDeploy (cmd/lmm/deploy.go) and purgeDeployedMods (cmd/lmm/purge.go, the
// --purge-before-deploy call only - the standalone `lmm purge` command is
// untouched by this extraction); see the task report for the exact mapping.
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
// purgeDeployedMods (used only by `lmm deploy --purge`; the standalone `lmm
// purge` command, and its own purgeDeployedMods call site, are untouched by
// this task). See DeployResult's doc comment for where each diagnostic
// below ends up.
func (s *Service) purgeForDeploy(ctx context.Context, game *domain.Game, profileName string, mods []domain.InstalledMod, opts DeployOptions, result *DeployResult, emit func(DeployProgress)) error {
	if len(mods) == 0 {
		return nil
	}

	hookCtx := opts.HookContext
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "uninstall.before_all", opts.Hooks.GetUninstallBeforeAll()); err != nil {
		if !opts.Force {
			return fmt.Errorf("uninstall.before_all hook failed: %w", err)
		}
		msg := fmt.Sprintf("uninstall.before_all hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(DeployProgress{Phase: DeployBeforeAllForced, Detail: msg})
	}

	installer := s.GetInstaller(game)
	emit(DeployProgress{Phase: DeployPurging, Total: len(mods)})

	// deferredWarnings holds uninstall.after_each (per mod, in loop order)
	// and uninstall.after_all PurgeWarning events: the pre-extraction
	// purgeDeployedMods accumulated these during/after the loop and only
	// printed them together, via printHookWarnings, once the whole loop
	// had finished - so emission is deferred to right after the loop,
	// mirroring that. Unlike DeployProfile's deploy-loop equivalent,
	// nothing else needs to print between the loop ending and these -
	// purgeDeployedMods went straight from printHookWarnings to
	// CleanupEmptyDirs/the closing blank line.
	var deferredWarnings []DeployProgress

	for _, mod := range mods {
		hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
		if err := runHook(ctx, opts.HookRunner, &hookCtx, "uninstall.before_each", opts.Hooks.GetUninstallBeforeEach()); err != nil {
			// Matches purgeDeployedMods: skip this mod (leave it deployed),
			// keep going. The pre-extraction CLI printed this unconditionally
			// to stdout ("  Skipped: ..."); recorded here as a Warning
			// (unconditional, stderr) instead - see the task report.
			msg := fmt.Sprintf("uninstall.before_each hook failed for %s during purge (not purged): %v", mod.Name, err)
			result.Warnings = append(result.Warnings, msg)
			emit(DeployProgress{Phase: PurgeWarning, ModName: mod.Name, ModID: mod.ID, Detail: msg})
			continue
		}

		if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
			msg := fmt.Sprintf("⚠ %s - %v", mod.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(DeployProgress{Phase: PurgeNote, ModName: mod.Name, ModID: mod.ID, Detail: msg})
		}

		if err := s.SetModDeployed(mod.SourceID, mod.ID, game.ID, profileName, false); err != nil {
			msg := fmt.Sprintf("⚠ %s - failed to mark as not deployed: %v", mod.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(DeployProgress{Phase: PurgeNote, ModName: mod.Name, ModID: mod.ID, Detail: msg})
		}

		if err := runHook(ctx, opts.HookRunner, &hookCtx, "uninstall.after_each", opts.Hooks.GetUninstallAfterEach()); err != nil {
			msg := fmt.Sprintf("uninstall.after_each hook failed for %s: %v", mod.ID, err)
			result.Warnings = append(result.Warnings, msg)
			deferredWarnings = append(deferredWarnings, DeployProgress{Phase: PurgeWarning, ModName: mod.Name, ModID: mod.ID, Detail: msg})
		}
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = "", "", ""
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "uninstall.after_all", opts.Hooks.GetUninstallAfterAll()); err != nil {
		msg := fmt.Sprintf("uninstall.after_all hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		deferredWarnings = append(deferredWarnings, DeployProgress{Phase: PurgeWarning, Detail: msg})
	}

	for _, w := range deferredWarnings {
		emit(w)
	}

	linker.CleanupEmptyDirs(game.ModPath)
	emit(DeployProgress{Phase: PurgeComplete})
	return nil
}

// SwitchPlan is the pure, displayable diff between the currently-active
// default profile and a target profile - computed by PlanProfileSwitch with
// zero side effects, so a caller (the CLI, or eventually the TUI) can render
// it (in a print block or a confirmation modal) before deciding whether to
// call ApplyProfileSwitch. This is a behavior-preserving extraction of
// cmd/lmm/profile.go's doProfileSwitch's diff computation (through its
// "Show changes" print block) - see the task report for the exact mapping.
//
// CRITICAL: this mirrors the CLI's OWN diff algorithm, which is distinct
// from (and does not call) ProfileManager.Switch - see the task report for
// why both exist.
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
			msg := fmt.Sprintf("Warning: failed to update %s: %v", im.Name, err)
			result.Notes = append(result.Notes, msg)
			evt := base
			evt.Phase, evt.Detail = SwitchEnableNote, msg
			emit(evt)
		}

		result.Enabled++
		evt := base
		evt.Phase = SwitchEnabled
		emit(evt)
	}

	if totalInstall := len(plan.ToInstall); totalInstall > 0 {
		emit(DeployProgress{Phase: SwitchInstalling, Total: totalInstall})

		for idx, ref := range plan.ToInstall {
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
	// Force gates ONLY install.before_all (once) and the PRIMARY mod's own
	// install.before_each, matching doInstall's own single-mod code exactly
	// (a failure aborts with an error unless Force is set, in which case it
	// is recorded as a Warning and the install proceeds). A DEPENDENCY's
	// install.before_each failure is NEVER Force-gated - it unconditionally
	// skips that one dependency and continues, matching batchInstallMods,
	// which is what pre-extraction doInstall actually delegated dependency
	// installation to. See the task report for the full trace and the
	// unification this represents.
	Hooks       *ResolvedHooks
	HookRunner  *HookRunner
	HookContext HookContext
	Force       bool
}

// InstallResult reports the outcome of ApplyInstall. As with DeployResult/
// UninstallResult/SwitchResult, every entry below is always recorded - there
// is no verbosity concept in core.
//
//   - Warnings holds diagnostics doInstall/batchInstallMods printed
//     unconditionally: install.before_all/before_each (primary only, when
//     forced), a failed SaveFileChecksum (note: unconditional, NOT
//     --verbose-gated - doInstall prints this one to stderr regardless),
//     and install.after_each/after_all hook failures. Callers should print
//     each entry to stderr, unconditionally, e.g.
//     `fmt.Fprintf(os.Stderr, "Warning: %v\n", w)`.
//   - Notes holds diagnostics doInstall only printed under --verbose: a
//     failed profile-create, a failed UpsertMod, a failed
//     reinstall-cache-transaction commit, and a failed old-cache cleanup
//     after a version upgrade - each already carrying its historical
//     "Warning: " prefix baked into the text, matching doInstall's exact
//     wording; a caller wanting byte-identical output should print each
//     entry to stdout ONLY under --verbose, e.g. `fmt.Printf("  %s\n", n)`.
//
// Every entry in both slices is ALSO reported via the progress callback at
// the exact point it is appended (InstallBeforeAllForced/
// InstallBeforeEachForced/InstallWarning/InstallNote - see each DeployPhase
// constant's doc comment), with Detail equal to the slice entry verbatim.
//
// On error, the returned result carries any diagnostics/counts accumulated
// before the failure; callers should surface them alongside the error.
type InstallResult struct {
	Installed []string // display names in install order, dependencies first (skip-and-continue on failure), then the primary (fatal on failure)
	Skipped   []string // "<name>: <reason>" - DEPENDENCIES only; the primary is never in this slice - a primary failure returns an error instead (see InstallOptions' Force doc comment)

	// FilesDeployed is the number of files extracted for the PRIMARY mod
	// across all of plan.Files - mirrors doInstall's totalFileCount / the
	// pre-extraction CLI's final "Files deployed: %d" line. Dependencies'
	// own extracted-file counts are not tracked here (batchInstallMods
	// never summed them either).
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

// ApplyInstall executes a plan produced by PlanInstall: dependencies (in
// plan order) install first, then the primary (plan.Mod) - see InstallPlan's
// doc comment for how ApplyInstall unifies the pre-extraction CLI's two
// divergent execution engines (doInstall's own single-mod code vs.
// batchInstallMods, which is what doInstall actually delegated dependency
// installation to) behind this one entry point; DeployPhase's Install*
// constants document the exact per-mod mechanics and failure semantics.
//
// install.before_all runs once, before any mod is touched; install.after_all
// runs once, only if every step through the primary's own install succeeded
// (matching doInstall - an early return, e.g. the primary's own fatal
// before_each/download/deploy/save failure, skips after_all entirely, same
// as the pre-extraction single-mod code path). progress may be nil.
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

	// deferredWarnings holds every install.after_each (dependencies in loop
	// order, then the primary) and the final install.after_all warning,
	// flushed together at the very end - mirroring DeployProfile/
	// purgeForDeploy's deferredWarnings pattern (itself modeled on
	// batchInstallMods' own printHookWarnings, which accumulated hook
	// errors across the WHOLE loop - deps and primary alike - and printed
	// them together only after everything else had already happened).
	var deferredWarnings []DeployProgress

	total := len(plan.Dependencies)
	for idx := range plan.Dependencies {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		dep := plan.Dependencies[idx]
		if warn := s.applyInstallDependency(ctx, game, plan, &dep, idx, total, linkMethod, pm, opts, result, emit); warn != nil {
			deferredWarnings = append(deferredWarnings, *warn)
		}
	}

	if err := ctx.Err(); err != nil {
		return result, err
	}

	afterEachWarning, err := s.applyInstallPrimary(ctx, game, plan, linkMethod, pm, opts, result, emit)
	if err != nil {
		return result, err
	}
	if afterEachWarning != nil {
		deferredWarnings = append(deferredWarnings, *afterEachWarning)
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

// applyInstallDependency installs one entry of plan.Dependencies, matching
// cmd/lmm/install.go's batchInstallMods per-mod loop exactly (the
// pre-extraction CLI's actual dependency-installation mechanism - see
// ApplyInstall's doc comment): any failure (hook, fetch, files, download,
// conflict aside, deploy, or save) skips this dependency and continues -
// never Force-gated, never fatal to the overall ApplyInstall call. Returns
// the install.after_each warning event to defer (nil if none), matching
// ApplyInstall's deferredWarnings convention.
func (s *Service) applyInstallDependency(ctx context.Context, game *domain.Game, plan *InstallPlan, dep *domain.Mod, idx, total int, linkMethod domain.LinkMethod, pm *ProfileManager, opts InstallOptions, result *InstallResult, emit func(DeployProgress)) *DeployProgress {
	base := DeployProgress{Index: idx + 1, Total: total, ModName: dep.Name, ModID: dep.ID, SourceID: dep.SourceID}
	skip := func(reason string) {
		evt := base
		evt.Phase, evt.Detail = InstallDepSkipped, reason
		emit(evt)
		result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %s", dep.Name, reason))
	}

	installing := base
	installing.Phase = InstallDepInstalling
	emit(installing)

	hookCtx := opts.HookContext
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = dep.ID, dep.Name, dep.Version
	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.before_each", opts.Hooks.GetInstallBeforeEach()); err != nil {
		skip(fmt.Sprintf("install.before_each hook failed: %v", err))
		return nil
	}

	installer := s.GetInstaller(game)

	// Defensive parity with batchInstallMods, which always re-checks
	// (rather than trusting its caller) - in practice unreachable via
	// PlanInstall's own contract, which never lists an already-installed
	// mod in Dependencies.
	if existing, err := s.GetInstalledMod(dep.SourceID, dep.ID, game.ID, plan.Profile); err == nil {
		_ = installer.Uninstall(ctx, game, &existing.Mod, plan.Profile)                            //nolint:errcheck // best-effort, matching batchInstallMods
		_ = s.GetGameCache(game).Delete(game.ID, existing.SourceID, existing.ID, existing.Version) //nolint:errcheck // best-effort, matching batchInstallMods
	}

	files, err := s.GetModFiles(ctx, plan.SourceID, dep)
	if err != nil {
		skip(fmt.Sprintf("failed to get mod files: %v", err))
		return nil
	}
	files = filterAndSortInstallFiles(files, plan.ShowArchived)
	if len(files) == 0 {
		skip("no downloadable files available")
		return nil
	}
	selected, _, err := selectDeployFiles(files, nil)
	if err != nil {
		skip(err.Error())
		return nil
	}
	file := selected[0]

	progressFn := func(p DownloadProgress) {
		if p.TotalBytes > 0 {
			dl := base
			dl.Phase, dl.Percent = InstallDepDownloading, p.Percentage
			emit(dl)
		}
	}
	downloadResult, err := s.DownloadMod(ctx, plan.SourceID, game, dep, file, progressFn)
	if err != nil {
		skip(fmt.Sprintf("download failed: %v", err))
		return nil
	}

	if !opts.Force {
		if conflicts, err := installer.GetConflicts(ctx, game, dep, plan.Profile); err == nil && len(conflicts) > 0 {
			evt := base
			evt.Phase, evt.Detail = InstallDepConflictWarning, fmt.Sprintf("%d file conflict(s) - will overwrite", len(conflicts))
			emit(evt)
		}
	}

	if err := installer.Install(ctx, game, dep, plan.Profile); err != nil {
		skip(fmt.Sprintf("deployment failed: %v", err))
		return nil
	}

	installedMod := &domain.InstalledMod{
		Mod:          *dep,
		ProfileName:  plan.Profile,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   linkMethod,
		FileIDs:      []string{file.ID},
	}
	installedMod.Mod.GameID = game.ID
	if err := s.SaveInstalledMod(installedMod); err != nil {
		skip(fmt.Sprintf("failed to save mod: %v", err))
		return nil
	}

	if !opts.SkipVerify && downloadResult.Checksum != "" {
		if err := s.SaveFileChecksum(dep.SourceID, dep.ID, game.ID, plan.Profile, file.ID, downloadResult.Checksum); err != nil {
			msg := fmt.Sprintf("failed to save checksum for file %s: %v", file.ID, err)
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
	modRef := domain.ModReference{SourceID: dep.SourceID, ModID: dep.ID, Version: dep.Version, FileIDs: []string{file.ID}}
	if err := pm.UpsertMod(game.ID, plan.Profile, modRef); err != nil {
		msg := fmt.Sprintf("Warning: could not update profile: %v", err)
		result.Notes = append(result.Notes, msg)
		evt := base
		evt.Phase, evt.Detail = InstallNote, msg
		emit(evt)
	}

	result.Installed = append(result.Installed, dep.Name)
	installedEvt := base
	installedEvt.Phase = InstallDepInstalled
	emit(installedEvt)

	if err := runHook(ctx, opts.HookRunner, &hookCtx, "install.after_each", opts.Hooks.GetInstallAfterEach()); err != nil {
		msg := fmt.Sprintf("install.after_each hook failed for %s: %v", dep.ID, err)
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
// SaveFileChecksum, --skip-verify), applied here regardless of whether
// Dependencies was non-empty (see ApplyInstall's doc comment for why this
// deliberately diverges from batchInstallMods' "treat the primary just like
// any other mod in the list" behavior). Returns the install.after_each
// warning event to defer (nil if none). A non-nil error is always fatal to
// ApplyInstall as a whole, matching doInstall's own early returns.
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
