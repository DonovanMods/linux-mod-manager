package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
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
			return skip(fmt.Sprintf("download failed: %v", err))
		}
	}

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
// is no verbosity concept in core. Unlike those two flows, doProfileSwitch
// never printed anything to stderr, so SwitchResult has no diagnostic that
// belongs in a Warnings bucket; the field exists only for API consistency
// with DeployResult/UninstallResult and is always empty for this flow (see
// the task report).
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
	Warnings                     []string // always empty for this flow; see doc comment
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
