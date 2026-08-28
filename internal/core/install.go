// Package core: this file holds the install flow - PlanInstall/ApplyInstall,
// their options/plan/result types and every private helper they own - moved
// verbatim out of flows.go by v2 Phase 2 Unit H (#288), per the phase plan's
// "flows.go shrinks every unit" constraint. The move commit changed nothing
// but the file the code lives in; the batch-install lift that follows it is
// its own commit.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
)

// --- PlanInstall (Phase 5b Task 1) ---

// InstallPlan is the pure, displayable result of PlanInstall: everything the
// pre-extraction CLI's pre-install prompts (dependency tree, conflict
// warnings, "already installed" notice) need to render before a caller
// decides whether to proceed (Phase 5b Task 2 adds ApplyInstall to actually
// execute one of these). Computed with zero side effects - see
// PlanInstall's doc comment.
type InstallPlan struct {
	SourceID string `json:"source_id"`
	GameID   string `json:"game_id"`
	Profile  string `json:"profile"`

	Mod domain.Mod `json:"mod"` // the mod that would be installed, freshly fetched via GetMod

	// Files is the file(s) that WOULD be downloaded: GetModFiles' result
	// after FilterAndSortFiles (also doInstall's default filter/sort - strips
	// ARCHIVED/OLD_VERSION/DELETED unless showArchived, sorts
	// MAIN>OPTIONAL>UPDATE>MISCELLANEOUS>other), then the same non-interactive
	// default cmd/lmm/install.go's selectInstallFiles falls back to (the
	// primary file, or the sole/first file) absent --file or an interactive
	// choice - reusing selectDeployFiles rather than porting
	// selectInstallFiles verbatim, since selectInstallFiles's --file flag and
	// interactive prompt both consume a plan rather than being part of one
	// (see the task report). Always exactly one file in practice: neither
	// selectDeployFiles nor this non-interactive default ever picks more
	// than one without a stored/explicit multi-file selection.
	Files []domain.DownloadableFile `json:"files"`

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
	// resolveInstallDependencies degrades to "no dependencies" rather than
	// failing the plan either way, but (#52 item 10) only records a
	// DependencyWarnings entry when the failure was something OTHER than
	// source.ErrNotSupported - see DependencyWarnings.
	Dependencies []domain.Mod `json:"dependencies"`

	// MissingDependencies records dependency references resolveDependencies
	// found but couldn't resolve (source fetch failure, or a SourceID
	// mismatch - see Dependencies) - the pre-extraction CLI's showInstallPlan
	// printed these as a warning, never a failure. Not part of the task
	// brief's directional API struct; added because the brief's own framing
	// ("output contains everything the CLI's pre-install prompts... need to
	// display") requires it to reproduce that warning - see the task report.
	MissingDependencies []domain.ModReference `json:"missing_dependencies,omitempty"`
	// CycleDetected mirrors resolveDependencies' cycleDetected: a circular
	// reference was found while resolving Dependencies (install order is
	// best-effort). Same rationale as MissingDependencies.
	CycleDetected bool `json:"cycle_detected"`

	// DependencyWarnings records one entry per GetDependencies call that
	// failed with something OTHER than source.ErrNotSupported while
	// resolving Dependencies (#52 item 10) - a real fetch failure (rate
	// limit, network blip, malformed response), as opposed to "this source
	// simply doesn't have the Dependencies capability" (ErrNotSupported),
	// which stays silent exactly as before. Either way resolution degrades
	// to "no dependencies found for that mod" and the plan still succeeds -
	// this field exists purely so a caller can tell the user dependency
	// resolution didn't run cleanly, the same way MissingDependencies/
	// CycleDetected surface their own non-fatal degradations. Each entry is
	// "<sourceID:modID>: <error>", already formatted for direct display
	// (see resolveInstallDependencies).
	DependencyWarnings []string `json:"dependency_warnings,omitempty"`

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
	Conflicts []Conflict `json:"conflicts,omitempty"`

	// Replaces is the currently-installed row for (SourceID, Mod.ID,
	// Profile), if any - non-nil means installing this plan would use
	// Installer.Replace (or its reinstall-cache-transaction variants, an
	// Apply-time concern) rather than Installer.Install. Mirrors doInstall's
	// existingMod exactly: populated regardless of whether the installed
	// version matches Mod.Version, so both a same-version reinstall and a
	// version upgrade set this.
	Replaces *domain.InstalledMod `json:"replaces,omitempty"`

	// TotalDownloadBytes is the sum of Files' declared sizes, or -1 if any
	// selected file's size is unreported (Size <= 0, matching the
	// DownloadEvent convention used elsewhere in this file: only a
	// positive TotalBytes/Size is treated as "known").
	TotalDownloadBytes int64 `json:"total_download_bytes"`

	// ShowArchived is the showArchived value PlanInstall was called with -
	// stored on the plan (Phase 5b Task 2) so ApplyInstall can resolve each
	// Dependencies entry's own downloadable files (at apply time - see
	// Dependencies' doc comment) using the identical filter the CLI showed
	// the user at plan time, without a second, possibly-inconsistent
	// parameter on InstallOptions. "The plan is the contract."
	ShowArchived bool `json:"show_archived"`
}

// PlanInstall computes what installing (sourceID, modID) into profileName
// would do - the pure, read-only half of the pre-extraction CLI's doInstall
// (cmd/lmm/install.go), extracted with zero mutations so a caller can
// render it and decide whether to proceed before Phase 5b Task 2's
// ApplyInstall executes it. See
// InstallPlan's doc comment for what each field means, and the task report
// for the exact mapping back to doInstall.
//
// Deliberately NOT reproduced here (both consume a plan rather than being
// part of one, matching PlanProfileSwitch's precedent):
//   - doInstall's interactive file picking / --file flag (selectInstallFiles)
//     and its "Install N mod(s)? [Y/n]" dependency confirm prompt - Files
//     always reflects the same non-interactive default cmd/lmm's own --yes
//     flag would pick; a caller that resolves a different selection
//     overrides plan.Files before calling ApplyInstall.
//   - --no-deps: a caller that wants to skip Dependencies can simply ignore
//     or clear them before calling ApplyInstall.
//
// showArchived mirrors doInstall's --show-archived flag exactly: it is
// threaded straight into FilterAndSortFiles (same ARCHIVED/OLD_VERSION/DELETED
// filter set, same MAIN>OPTIONAL>UPDATE>MISCELLANEOUS>other sort as
// doInstall's own default), which runs
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

	existing, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
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
		installedMods, _ := s.GetInstalledMods(ctx, game.ID, profileName)
		installedIDs := make(map[string]bool, len(installedMods))
		for _, im := range installedMods {
			installedIDs[domain.ModKey(im.SourceID, im.ID)] = true
		}
		plan.Dependencies, plan.MissingDependencies, plan.CycleDetected, plan.DependencyWarnings = s.resolveInstallDependencies(ctx, sourceID, game.ID, mod, installedIDs)
	}

	files, err := s.GetModFiles(ctx, sourceID, mod)
	if err != nil {
		return nil, fmt.Errorf("failed to get mod files: %w", err)
	}
	files = FilterAndSortFiles(files, showArchived)
	// This explicit check is intentionally NOT redundant with
	// selectDeployFiles's own len==0 guard below: that guard returns
	// ErrNoDownloadableFiles ("no downloadable files"), which is NOT
	// byte-identical to doInstall's message - so it stays here to reproduce
	// doInstall's exact wording on the FILTERED list.
	if len(files) == 0 {
		return nil, fmt.Errorf("no downloadable files available for this mod")
	}
	selected, _, err := selectDeployFiles(files, nil, false)
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
	// detected", never fails the plan. See Conflicts' doc comment. Extended
	// (#189) to GetInstallerForProfile's own error (e.g. an invalid
	// profile link_method): this is a read-only preview, not a deploy, so
	// the existing "never fails the plan" policy still applies - the
	// installer's resolution simply doesn't get to say whether this
	// specific plan conflicts with anything already on disk.
	if installer, err := s.GetInstallerForProfile(ctx, game, profileName); err == nil {
		if conflicts, err := installer.GetConflicts(ctx, game, mod, profileName); err == nil {
			plan.Conflicts = conflicts
		}
	}

	return plan, nil
}

// resolveInstallDependencies is PlanInstall's copy of cmd/lmm/install.go's
// resolveDependencies (duplicated rather than shared: internal/core cannot
// import cmd/lmm, and hoisting the CLI helper out is outside this task's
// scope - see the task report): a depth-first,
// cycle-detecting traversal of target's dependency graph that returns
// resolved dependencies in install order (deepest first). Every fetch uses
// sourceID - NOT each domain.ModReference's own SourceID - matching
// serviceDepFetcher's own fixed-source behavior exactly (see Dependencies'
// doc comment). Already-installed dependencies (per installedIDs) are
// skipped; a dependency this can't resolve (source fetch failure, or a
// SourceID mismatch) is recorded in missing rather than failing the whole
// resolution; a circular reference sets cycleDetected and is otherwise
// skipped.
//
// GetDependencies failures (#52 item 10) split in two: source.ErrNotSupported
// - "this source doesn't have the Dependencies capability at all" - degrades
// to "no dependencies for this mod" SILENTLY, unchanged from before. Any
// OTHER error degrades the same way (the plan still succeeds) but is also
// appended to warnings, since it represents an actual failure the caller
// couldn't otherwise tell apart from "this mod genuinely has none".
//
// gameID is the LMM game id (game.ID), NOT a mod's stamped GameID: sources
// stamp their own SOURCE-DOMAIN id onto the mods they return (e.g. NexusMods
// echoes "skyrimspecialedition"), and Service.GetMod translates an LMM id
// into that domain id via game.SourceIDs. Feeding a stamped id back into
// GetMod (#230) only worked while s.games[<source-domain-id>] happened to
// miss - LMM ids are user-chosen in games.yaml, so a collision with another
// game's LMM id would translate the already-translated id and silently fetch
// dependencies from the wrong game.
func (s *Service) resolveInstallDependencies(ctx context.Context, sourceID, gameID string, target *domain.Mod, installedIDs map[string]bool) (deps []domain.Mod, missing []domain.ModReference, cycleDetected bool, warnings []string) {
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
			// Degrade to "no dependencies for this mod" either way - but
			// only a REAL failure (not a plain capability gap) is worth
			// telling the caller about.
			if !errors.Is(err, source.ErrNotSupported) {
				warnings = append(warnings, fmt.Sprintf("%s: %v", key, err))
			}
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

			depMod, err := s.GetMod(ctx, sourceID, gameID, ref.ModID)
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
	return deps, missing, cycleDetected, warnings
}

// --- ApplyInstall (Phase 5b Task 2) ---

// InstallOptions configures ApplyInstall.
type InstallOptions struct {
	// TargetVersion, when non-empty, pins the exact version to install for
	// plan.Mod ONLY (#96 decision 6: batch dependencies always install at
	// latest, untouched by this field). Honored on BOTH paths (#140 item 2
	// closed the STRICT gap - previously it was documented-inert there and
	// the CLI compensated by overriding plan.Files, a "flag lies" trap for
	// any other core caller):
	//
	//   - STRICT (no-deps): resolved by resolveStrictInstallFiles at the
	//     very top of ApplyInstall - before the #143 lock gate, any hook,
	//     or any side effect - overwriting plan.Files with the version's
	//     matches (TargetFileIDs' picks within them, else the primary-or-
	//     first heuristic). A plan.Files selection that already sits
	//     entirely inside TargetVersion is kept VERBATIM (no refetch): that
	//     is how the CLI's interactive/--file sub-selection, applied to
	//     plan.Files before this is called, survives unclobbered.
	//   - BATCH (Dependencies-present): the per-mod selection
	//     (applyInstallBatchMod) never consults plan.Files, so the
	//     primary's selection is resolved from this field up front - see
	//     below (#93's silent---version class, found again in #96 review).
	//
	// Resolved ONCE, up front, before any mod (dependency or primary) is
	// touched - not lazily when the loop reaches the primary's turn. A
	// version that doesn't resolve is fatal to the WHOLE install and
	// returned immediately, with zero dependencies installed: the user
	// explicitly asked for this version, so a quiet per-mod "Failed: 1 (X)"
	// summary line is not loud enough, and installing dependencies for a
	// primary that is about to fail to install at all would leave a
	// confusing half-applied state.
	TargetVersion string

	// TargetFileIDs, when non-empty, pins the exact file selection for
	// plan.Mod ONLY - the core-side counterpart of the CLI's --file flag
	// (#140, same silent-flag family as #93/#96: previously --file was
	// silently ignored whenever the named mod had resolvable dependencies).
	// Each ID must resolve within the primary's candidate pool - the
	// TargetVersion matches when TargetVersion is also set, else the
	// plan.ShowArchived-filtered list - and ANY miss is fatal to the WHOLE
	// install, up front (BATCH path: zero dependencies installed, the #96
	// TargetVersion loudness precedent). Dependencies are never affected:
	// they always auto-select their own primary file at latest. Empty means
	// no pin - the BATCH path auto-selects from the pool, the STRICT path
	// installs plan.Files.
	TargetFileIDs []string

	// SkipVerify mirrors doInstall's --skip-verify: when true, a downloaded
	// file's checksum is neither saved (SaveFileChecksum) nor reported via
	// an InstallChecksumComputed event, matching downloadSelectedFiles' "if
	// !skipVerify && checksum != ..." gate exactly for every mod (primary
	// and dependencies alike - batchInstallMods honors the same flag).
	SkipVerify bool

	// Hook plumbing, mirroring UninstallOptions/DeployOptions: ApplyInstall
	// resolves the game/profile hooks and a HookRunner itself.
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
	Force     bool
	SkipHooks bool // run no hooks even when hooks are configured (the CLI's --no-hooks)

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
// Every entry in both slices is ALSO reported via the event stream at
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
	Installed []string `json:"installed"`
	// Skipped holds "<name>: <reason>" entries for any mod that failed in
	// the BATCH (Dependencies-present) path - dependency OR primary alike
	// (Fix wave 1 restored the primary's participation; see InstallOptions'
	// Force doc comment). Always empty in the STRICT (no-deps) path, since
	// a primary failure there returns an error instead.
	Skipped []string `json:"skipped,omitempty"`
	// Failed holds JUST the display names (no reason - see Skipped for
	// that) of every BATCH-path mod that failed, dependency or primary
	// alike, in the SAME order Skipped uses - mirrors batchInstallMods' own
	// `failed []string` accumulator, which the pre-extraction CLI's
	// restored terminal "--- Summary ---\nInstalled: %d\nFailed: %d (%s)\n"
	// block joins verbatim (task-2-report.md's Fix wave 1). Always empty
	// in the STRICT (no-deps) path.
	Failed []string `json:"failed,omitempty"`

	// FilesDeployed is the number of files extracted for the STRICT path's
	// PRIMARY mod across all of plan.Files - mirrors doInstall's
	// totalFileCount / the pre-extraction CLI's final "Files deployed: %d"
	// line. Always 0 in the BATCH path (batchInstallMods' terminal summary
	// never printed a file count, only Installed/Failed - see Failed).
	FilesDeployed int `json:"files_deployed"`

	// MergedPakSyncFailed is true when this call's own end-of-install
	// syncMergedPak attempt returned a hard error (#197 postsmoke review
	// fix - Copilot flagged that a DeployCompile zero-file mod's success
	// line unconditionally claimed "merged pak updated" even when the
	// non-fatal sync failed, contradicting the loud Warning already on
	// stderr). False when the sync succeeded, including when it returned
	// its own non-fatal merge warnings - those still leave the pak
	// deployed. Always false for a non-DeployCompile game.
	MergedPakSyncFailed bool `json:"merged_pak_sync_failed"`

	Warnings []string `json:"warnings,omitempty"`
	Notes    []string `json:"notes,omitempty"`
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

func prepareReinstallCacheTransaction(ctx context.Context, live *cache.Cache, gameID, sourceID, modID, version string, logger *slog.Logger) (*reinstallCacheTransaction, error) {
	tempDir, err := os.MkdirTemp("", "lmm-reinstall-cache-*")
	if err != nil {
		return nil, fmt.Errorf("creating cache snapshot: %w", err)
	}
	snapshot := cache.New(filepath.Join(tempDir, "snapshot"))
	snapshot.SetLogger(logger)
	staged := cache.New(filepath.Join(tempDir, "staged"))
	staged.SetLogger(logger)
	if err := live.CloneMod(ctx, snapshot, gameID, sourceID, modID, version); err != nil {
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

// Activate publishes the staged download over the live cache entry. It is
// the FORWARD path, so it takes the caller's own ctx and a cancellation
// legitimately aborts the install - but the Delete below has already
// destroyed the live entry by the time the clone can fail, so activated is
// set the moment the delete succeeds: it is what tells RestoreLive there is
// something to put back. (Before that, a cancelled clone left the entry
// deleted with the recovery path early-returning nil - review finding C1.)
func (s *reinstallCacheTransaction) Activate(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.activated {
		return nil
	}
	if err := s.live.Delete(s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	s.activated = true
	if err := s.staged.CloneMod(ctx, s.live, s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	return nil
}

// RestoreLive puts the original cache entry back. It is a RECOVERY path:
// its Delete-then-CloneMod sequence destroys the live entry before it can
// rewrite it, so every call site passes context.WithoutCancel(ctx) - a
// cancelled clone here would leave the mod's cache entry gone for good
// (review finding C1). ctx is still threaded rather than dropped so the
// copy stays interruptible by a future non-cancellation signal.
func (s *reinstallCacheTransaction) RestoreLive(ctx context.Context) error {
	if s == nil || !s.activated {
		return nil
	}
	if err := s.live.Delete(s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	if err := s.snapshot.CloneMod(ctx, s.live, s.gameID, s.sourceID, s.modID, s.version); err != nil {
		return err
	}
	s.activated = false
	return nil
}

// Rollback restores the live entry and ALWAYS removes the temp dir, even
// when the restore failed: the old early return leaked the snapshot into
// $TMPDIR on exactly the paths that most need cleaning up (review finding
// C1). Both errors are reported together.
func (s *reinstallCacheTransaction) Rollback(ctx context.Context) error {
	if s == nil {
		return nil
	}
	restoreErr := s.RestoreLive(ctx)
	rmErr := os.RemoveAll(s.tempDir)
	*s = reinstallCacheTransaction{}
	return errors.Join(restoreErr, rmErr)
}

func (s *reinstallCacheTransaction) Commit() error {
	if s == nil {
		return nil
	}
	err := os.RemoveAll(s.tempDir)
	*s = reinstallCacheTransaction{}
	return err
}

// lockedInstallRefusal implements ApplyInstall's #143 up-front gate: it
// returns LockedRefRefusalError when plan.Profile holds a LOCKED ref for
// plan.Mod AND the version this install would record differs from the
// locked version, and nil otherwise (no profile, no ref, no lock, same
// version, or a would-be version that cannot be determined here - the flow
// itself then produces its own authoritative error/skip, with UpsertMod's
// ErrModLocked guard as the final backstop). A missing/unreadable profile
// cannot hold a lock - ApplyUpdate's tolerant precedent.
func (s *Service) lockedInstallRefusal(ctx context.Context, plan *InstallPlan, opts InstallOptions) error {
	prof, err := s.NewProfileManager().Get(plan.GameID, plan.Profile)
	if err != nil {
		if errors.Is(err, domain.ErrProfileNotFound) {
			// A profile that hasn't been materialized as a YAML file yet is
			// the everyday case on a first-ever install, not a fault -
			// Warn here would fire on essentially every fresh install.
			s.logger().Debug("profile not found while checking lock", "game_id", plan.GameID, "profile", plan.Profile, "err", err)
		} else {
			s.logger().Warn("profile load failed while checking lock", "game_id", plan.GameID, "profile", plan.Profile, "err", err)
		}
		return nil
	}
	ref := prof.FindRef(plan.Mod.SourceID, plan.Mod.ID)
	if ref == nil || !ref.Locked {
		return nil
	}
	target, ok := s.resolveInstallTargetVersion(ctx, plan, opts)
	if !ok || target == ref.Version {
		return nil
	}
	return LockedRefRefusalError(plan.Mod, plan.Profile, ref)
}

// resolveInstallTargetVersion computes the version ApplyInstall would record
// for plan.Mod - mirroring each path's own later derivation exactly, without
// side effects - solely so lockedInstallRefusal can compare it against a
// locked ref. ok is false when the derivation fails (no files, unresolvable
// TargetVersion/TargetFileIDs, source error); the caller must then let the
// flow proceed to its own authoritative handling of that failure rather than
// refusing on a version this couldn't determine.
//
//   - STRICT (no dependencies): applyInstallPrimary installs exactly
//     plan.Files - by the time lockedInstallRefusal runs, ApplyInstall's own
//     resolveStrictInstallFiles call has already folded opts.TargetVersion/
//     TargetFileIDs into plan.Files (#140; the CLI's interactive/--file
//     sub-selection was applied to the plan even earlier) - so the would-be
//     version is plan.Files' effective version, the same
//     domain.EffectiveInstalledVersion stamp (#94) applyInstallPrimary
//     itself performs.
//   - BATCH: applyInstallBatchMod never consults plan.Files - the primary's
//     selection is re-derived here exactly as ApplyInstall's own #96/#140
//     pre-resolution derives it (resolveInstallCandidatePool +
//     selectInstallTargetFiles). The GetModFiles network read here happens
//     ONLY when the ref is locked (lockedInstallRefusal checks the lock
//     first).
func (s *Service) resolveInstallTargetVersion(ctx context.Context, plan *InstallPlan, opts InstallOptions) (version string, ok bool) {
	if len(plan.Dependencies) == 0 {
		if len(plan.Files) == 0 {
			return "", false
		}
		selected := make([]*domain.DownloadableFile, len(plan.Files))
		for i := range plan.Files {
			selected[i] = &plan.Files[i]
		}
		return domain.EffectiveInstalledVersion(plan.Mod.Version, selected), true
	}

	primary := plan.Mod // local, addressable copy - distinct from plan.Mod
	pool, err := s.resolveInstallCandidatePool(ctx, primary.SourceID, &primary, plan.ShowArchived, opts.TargetVersion)
	if err != nil {
		return "", false
	}
	selected, err := selectInstallTargetFiles(pool, opts.TargetFileIDs)
	if err != nil {
		return "", false
	}
	refs := make([]*domain.DownloadableFile, len(selected))
	for i := range selected {
		refs[i] = &selected[i]
	}
	return domain.EffectiveInstalledVersion(primary.Version, refs), true
}

// resolveInstallCandidatePool fetches mod's downloadable files and narrows
// them to the pool an explicit file pin (or the auto-pick heuristic) may
// select from: targetVersion's exact matches when targetVersion is non-empty
// (resolved against the RAW list - a version pin usually names an archived
// file, #96), else the showArchived-filtered, category-sorted list. Shared
// by ApplyInstall's up-front primary resolution on both paths (#140) and the
// #143 lock gate's dry-run derivation, so the gate can never judge a
// different selection than the install performs.
func (s *Service) resolveInstallCandidatePool(ctx context.Context, sourceID string, mod *domain.Mod, showArchived bool, targetVersion string) ([]domain.DownloadableFile, error) {
	files, err := s.GetModFiles(ctx, sourceID, mod)
	if err != nil {
		return nil, fmt.Errorf("failed to get mod files: %w", err)
	}
	if targetVersion != "" {
		return ResolveVersionFiles(sourceID, files, targetVersion)
	}
	return FilterAndSortFiles(files, showArchived), nil
}

// selectInstallTargetFiles applies targetFileIDs to pool (every ID must
// resolve - see resolveTargetFiles), or falls back to the primary-or-first
// heuristic (selectDeployFiles) when no IDs are pinned - the single
// selection rule behind ApplyInstall's TargetVersion/TargetFileIDs handling
// on both paths (#140).
func selectInstallTargetFiles(pool []domain.DownloadableFile, targetFileIDs []string) ([]domain.DownloadableFile, error) {
	if len(targetFileIDs) > 0 {
		return resolveTargetFiles(pool, targetFileIDs)
	}
	selected, _, err := selectDeployFiles(pool, nil, false)
	if err != nil {
		return nil, err
	}
	out := make([]domain.DownloadableFile, len(selected))
	for i := range selected {
		out[i] = *selected[i]
	}
	return out, nil
}

// resolveTargetFiles returns the pool entries matching ids, in ids order,
// with duplicate ids collapsed to their first occurrence (a repeated pin is
// the same request twice, and letting it through would fail
// SaveInstalledMod's PK-constrained installed_mod_files INSERT only after
// download and deploy). EVERY id must match - any miss fails with the CLI
// selectInstallFiles' historical wording ("file ID %s not found"), never a
// silent partial selection (#95's silent-fallback class). Unlike
// selectDeployFiles' storedFileIDs matching, which tolerates misses by
// design (stored IDs go stale when sources prune files), these ids are an
// explicit, present-tense user request.
func resolveTargetFiles(pool []domain.DownloadableFile, ids []string) ([]domain.DownloadableFile, error) {
	seen := make(map[string]bool, len(ids))
	out := make([]domain.DownloadableFile, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		found := false
		for i := range pool {
			if pool[i].ID == id {
				out = append(out, pool[i])
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("file ID %s not found", id)
		}
	}
	return out, nil
}

// installSelectionSatisfiesTargets reports whether files (a caller-supplied
// plan.Files selection) already honors opts' TargetVersion/TargetFileIDs
// pins: non-empty, every file at exactly TargetVersion (when set), and the
// file-ID set exactly TargetFileIDs' (when set). resolveStrictInstallFiles
// keeps a satisfying selection VERBATIM - and skips the GetModFiles refetch
// entirely - so the CLI's interactive/--file sub-selection (applied to
// plan.Files from the same version pool before ApplyInstall runs) survives,
// while a stale default (e.g. PlanInstall's latest-primary pick under a
// TargetVersion naming an older version) is re-resolved in core (#140).
func installSelectionSatisfiesTargets(files []domain.DownloadableFile, opts InstallOptions) bool {
	if len(files) == 0 {
		return false
	}
	if opts.TargetVersion != "" {
		for _, f := range files {
			if f.Version != opts.TargetVersion {
				return false
			}
		}
	}
	if len(opts.TargetFileIDs) > 0 {
		want := make(map[string]bool, len(opts.TargetFileIDs))
		for _, id := range opts.TargetFileIDs {
			want[id] = true
		}
		got := make(map[string]bool, len(files))
		for _, f := range files {
			if !want[f.ID] {
				return false
			}
			got[f.ID] = true
		}
		if len(got) != len(want) {
			return false
		}
	}
	return true
}

// resolveStrictInstallFiles computes the STRICT (no-deps) path's final
// plan.Files selection under opts' TargetVersion/TargetFileIDs pins (#140
// item 2: previously TargetVersion was inert here and only a CLI-side
// plan.Files override made --version real, a trap for any other core
// caller). Returns (nil, nil) when there is nothing to do - no pins set, or
// plan.Files already satisfies them (kept verbatim, no refetch - see
// installSelectionSatisfiesTargets); otherwise fetches the candidate pool
// and returns the pinned (or auto-picked) selection. Any resolution failure
// is fatal to the install: the user explicitly asked for this version/file,
// so silently installing something else is never acceptable (#93).
func (s *Service) resolveStrictInstallFiles(ctx context.Context, plan *InstallPlan, opts InstallOptions) ([]domain.DownloadableFile, error) {
	if opts.TargetVersion == "" && len(opts.TargetFileIDs) == 0 {
		return nil, nil
	}
	if installSelectionSatisfiesTargets(plan.Files, opts) {
		return nil, nil
	}
	primary := plan.Mod // local, addressable copy - distinct from plan.Mod
	pool, err := s.resolveInstallCandidatePool(ctx, plan.SourceID, &primary, plan.ShowArchived, opts.TargetVersion)
	if err != nil {
		return nil, err
	}
	return selectInstallTargetFiles(pool, opts.TargetFileIDs)
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
//     INTERACTIVE selection is the CALLER's job, applied to plan.Files
//     before this is ever called, while opts.TargetVersion/TargetFileIDs
//     pins are folded into plan.Files HERE, up front, when the caller's
//     selection doesn't already satisfy them (#140 - see
//     resolveStrictInstallFiles) - but the blocking conflict prompt is NOT:
//     it fires INSIDE applyInstallPrimary itself, post-download/pre-deploy,
//     via opts.ConfirmConflicts - see that field's doc comment for why a
//     caller-side, plan.Conflicts-driven prompt can never detect an
//     uncached mod's conflicts, the C1 review finding this restores
//     fidelity for).
//   - Non-empty: the BATCH path - plan.Dependencies (in plan order) THEN
//     plan.Mod all install via applyInstallBatchMod, IDENTICALLY, matching
//     batchInstallMods' own "every mod in the list is treated the same"
//     design byte-for-byte - the primary is NOT special-cased here at all
//     (no Replace, no interactive selection, no blocking conflict prompt -
//     see applyInstallBatchMod's own doc comment; its ONE divergence is the
//     opts.TargetVersion/TargetFileIDs pre-resolution, #96/#140, which pins
//     the primary's file selection only). Like the STRICT path's own fold
//     above, this now happens HERE, up front - before the #143 lock gate
//     and before install.before_all - rather than inside the BATCH branch
//     itself (#214: an unhonorable pin must fail before any hook or side
//     effect, on BOTH paths alike).
//
// install.before_all runs once, before any mod is touched - and, on either
// path, only after that path's own up-front pre-resolution has already
// succeeded (see above) - matching both doInstall's own single-mod code and
// batchInstallMods, which each had their own, functionally-identical,
// Force-gated install.before_all call. install.after_all runs once, at the
// very end: in the STRICT path, only if the primary's own install fully
// succeeded (an early return skips it entirely, matching doInstall's
// single-mod code); in the BATCH path, unconditionally once the loop
// finishes, since no per-mod failure there is ever fatal (matching
// batchInstallMods, which always reaches its own install.after_all call
// regardless of how many mods in its list failed). sink may be nil.
//
// On error, the returned result carries any diagnostics/Installed entries
// accumulated before the failure - callers should surface them alongside the
// error (see InstallResult's doc comment).
func (s *Service) ApplyInstall(ctx context.Context, game *domain.Game, plan *InstallPlan, opts InstallOptions, sink EventSink) (*InstallResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &InstallResult{}, err
	}
	defer release()
	return s.applyInstall(ctx, game, plan, opts, sink)
}

func (s *Service) applyInstall(ctx context.Context, game *domain.Game, plan *InstallPlan, opts InstallOptions, sink EventSink) (*InstallResult, error) {
	result := &InstallResult{}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}

	if err := ctx.Err(); err != nil {
		return result, err
	}

	// #140: fold opts.TargetVersion/TargetFileIDs into the STRICT path's
	// plan.Files up front, BEFORE the #143 lock gate below judges the
	// would-be version and before any hook or side effect - a caller-
	// supplied selection that already satisfies the pins is kept verbatim
	// (see resolveStrictInstallFiles). An unresolvable pin is fatal here,
	// with zero side effects, mirroring the BATCH path's own #96 up-front
	// abort. The BATCH path resolves the same pins inside its own branch
	// below (applyInstallBatchMod never consults plan.Files).
	if len(plan.Dependencies) == 0 {
		strictFiles, err := s.resolveStrictInstallFiles(ctx, plan, opts)
		if err != nil {
			return result, err
		}
		if strictFiles != nil {
			plan.Files = strictFiles
		}
		// #211: validate the STRICT path's final selection - covers BOTH an
		// explicit --file pin resolved just above (strictFiles) AND a
		// caller-supplied plan.Files left untouched (resolveStrictInstallFiles
		// returned nil, nil: no pins set, or the caller's selection already
		// satisfied them - the CLI's interactive-override shape). One call
		// here is correct rather than a second one inside
		// resolveStrictInstallFiles: that helper has no other caller (see its
		// doc comment), so validating its result immediately after this fold
		// is equivalent to validating inside it, and plan.Files is the only
		// value that matters to applyInstallPrimary either way.
		if err := s.ValidateInstallFileSelection(plan.SourceID, plan.Files); err != nil {
			return result, err
		}
	}

	// #214: the BATCH path's primary pre-resolution (#96/#140 - pins the
	// primary's file selection only) runs HERE, before the lock gate and
	// install.before_all, for the same reason the STRICT path resolves
	// pins up front: a selection the user asked for that cannot be honored
	// must fail before any hook or side effect. The block is read-only
	// (candidate-pool fetch + pure selection), so a successful install is
	// byte-identical to the previous ordering.
	//
	// #96/#140: opts.TargetVersion/TargetFileIDs pin the PRIMARY only
	// (see their doc comments) - resolved here, once, to the FINAL
	// file selection, before any mod in mods is touched, so an
	// unresolvable version or file ID aborts the whole install with
	// zero side effects rather than surfacing as a per-mod "Failed"
	// line after dependencies already installed. primaryOverrideFiles
	// is passed to applyInstallBatchMod ONLY for the primary's own
	// iteration (the last entry in mods, by construction above); every
	// dependency iteration gets nil and re-derives its own selection
	// exactly as before.
	var primaryOverrideFiles []domain.DownloadableFile
	if len(plan.Dependencies) > 0 && (opts.TargetVersion != "" || len(opts.TargetFileIDs) > 0) {
		primary := plan.Mod // local, addressable copy - distinct from plan.Mod
		pool, err := s.resolveInstallCandidatePool(ctx, primary.SourceID, &primary, plan.ShowArchived, opts.TargetVersion)
		if err != nil {
			return result, err
		}
		primaryOverrideFiles, err = selectInstallTargetFiles(pool, opts.TargetFileIDs)
		if err != nil {
			return result, err
		}
		// #211: validate the primary's up-front resolved selection
		// before any dependency (or the primary itself) is touched -
		// mirrors the STRICT path's fold-site validation above for the
		// BATCH path's one place a caller can pin more than one file.
		if err := s.ValidateInstallFileSelection(primary.SourceID, primaryOverrideFiles); err != nil {
			return result, err
		}
	}

	// #143: refuse up front - before any hook, download, deploy, or DB/
	// profile write - when the target profile holds a LOCKED ref for
	// plan.Mod and this install would record a DIFFERENT version. Only
	// explicit lock/unlock may move a locked version; installing at exactly
	// the locked version (converge/repair) stays allowed. In the BATCH path
	// this deliberately aborts the WHOLE install with zero dependencies
	// installed, mirroring the #96 TargetVersion precedent (see
	// InstallOptions.TargetVersion). UpsertMod's own ErrModLocked guard
	// backstops this check; refusing here is what keeps a refused install
	// from deploying first and leaving drift behind a mere Note.
	if err := s.lockedInstallRefusal(ctx, plan, opts); err != nil {
		return result, err
	}

	hooks, err := s.resolvedHooks(ctx, game, plan.Profile)
	if err != nil {
		return result, err
	}
	runner, err := s.hookRunner(ctx)
	if err != nil {
		return result, err
	}
	hookCtx := hookContextFor(game)
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.before_all", hooks.GetInstallBeforeAll()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("install.before_all hook failed: %w", err)
		}
		msg := fmt.Sprintf("install.before_all hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(HookEvent{Scope: Scope{Op: OpInstall}, Phase: InstallBeforeAllForced, Stage: "install.before_all", Detail: msg})
	}

	linkMethod, err := s.GetEffectiveLinkMethod(ctx, game, plan.Profile)
	if err != nil {
		return result, err
	}
	pm := s.NewProfileManager()

	// deferredWarnings holds every install.after_each (BATCH path: every mod
	// in loop order, primary included; STRICT path: the primary's own) and
	// the final install.after_all warning, flushed together at the very end
	// - mirroring DeployProfile/purgeForDeploy's deferredWarnings pattern
	// (itself modeled on batchInstallMods' own printHookWarnings, which
	// accumulated hook errors across the WHOLE loop - deps and primary
	// alike - and printed them together only after everything else had
	// already happened).
	var deferredWarnings []Event

	if len(plan.Dependencies) > 0 {
		// --- BATCH path: every mod, primary included, treated identically. ---
		mods := make([]*domain.Mod, 0, len(plan.Dependencies)+1)
		for i := range plan.Dependencies {
			mods = append(mods, &plan.Dependencies[i])
		}
		primary := plan.Mod // local, addressable copy - distinct from plan.Mod
		mods = append(mods, &primary)

		// primaryOverrideFiles was resolved (and validated) up front, above
		// - see #214's comment there - before the lock gate and
		// install.before_all. It is passed to applyInstallBatchMod ONLY for
		// the primary's own iteration (the last entry in mods, by
		// construction above); every dependency iteration gets nil and
		// re-derives its own selection exactly as before.
		total := len(mods)
		for idx, mod := range mods {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			var overrideFiles []domain.DownloadableFile
			if idx == total-1 {
				overrideFiles = primaryOverrideFiles
			}
			if warn := s.applyInstallBatchMod(ctx, game, plan, mod, idx, total, linkMethod, pm, opts, hooks, runner, result, emit, overrideFiles); warn != nil {
				deferredWarnings = append(deferredWarnings, *warn)
			}
		}
		// The primary is mods' LAST entry, so a cancellation inside its own
		// iteration never reaches the head-of-loop check above - without
		// this, install.after_all would run and ApplyInstall would return
		// (result, nil) (review finding I2).
		if err := ctx.Err(); err != nil {
			return result, err
		}
	} else {
		// --- STRICT path: only the primary, doInstall's own mechanics. ---
		afterEachWarning, err := s.applyInstallPrimary(ctx, game, plan, linkMethod, pm, opts, hooks, runner, result, emit)
		if err != nil {
			return result, err
		}
		if afterEachWarning != nil {
			deferredWarnings = append(deferredWarnings, *afterEachWarning)
		}
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = "", "", ""
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.after_all", hooks.GetInstallAfterAll()); err != nil {
		msg := fmt.Sprintf("install.after_all hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		deferredWarnings = append(deferredWarnings, WarningEvent{Scope: Scope{Op: OpInstall}, Phase: InstallWarning, Message: msg})
	}

	for _, w := range deferredWarnings {
		emit(w)
	}

	// #197 postsmoke fix: appending to result.Warnings alone is not loud -
	// doInstall (cmd/lmm) never reads result.Warnings back, only the
	// progress events emitted live above (InstallWarning is what actually
	// reaches stderr). A sync failure here used to be completely silent,
	// the exact plumbing gap that let the postsmoke bug through even on
	// the already-fixed single-mod install path.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, plan.Profile); syncErr != nil {
		msg := fmt.Sprintf("syncing merged pak: %v", syncErr)
		result.Warnings = append(result.Warnings, msg)
		result.MergedPakSyncFailed = true
		emit(WarningEvent{Scope: Scope{Op: OpInstall}, Phase: InstallWarning, Message: msg})
	} else {
		for _, w := range syncWarnings {
			result.Warnings = append(result.Warnings, w)
			emit(WarningEvent{Scope: Scope{Op: OpInstall}, Phase: InstallWarning, Message: w})
		}
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
// no interactive selection (the filtered list's primary-or-first file,
// re-resolved here - plan.Files is never consulted; the primary's
// --version/--file pins arrive pre-resolved via overrideFiles, #96/#140),
// a non-blocking inline conflict warning (never a blocking prompt). Returns
// the install.after_each warning event to defer (nil if none), matching
// ApplyInstall's deferredWarnings convention.
//
// overrideFiles, when non-nil, is the FINAL file selection - every entry
// downloads and is recorded, in order, with no further sub-selection -
// exclusively how ApplyInstall's #96/#140 opts.TargetVersion/TargetFileIDs
// pins reach the PRIMARY's iteration (already resolved to exact files
// before the loop started; see ApplyInstall's own comment). This is the one
// place the BATCH path installs more than one file per mod (--file can name
// several); the no-override derivation below always selects exactly one.
// Every dependency iteration passes nil here and re-derives its own
// selection exactly as before - decision 6, dependencies install at latest
// regardless of the primary's pins.
func (s *Service) applyInstallBatchMod(ctx context.Context, game *domain.Game, plan *InstallPlan, mod *domain.Mod, idx, total int, linkMethod domain.LinkMethod, pm *ProfileManager, opts InstallOptions, hooks *ResolvedHooks, runner *HookRunner, result *InstallResult, emit func(Event), overrideFiles []domain.DownloadableFile) *WarningEvent {
	scope := Scope{Op: OpInstall, Index: idx + 1, Total: total, ModName: mod.Name, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}}
	skip := func(label, reason string) {
		emit(ModEvent{Scope: scope, Phase: InstallDepSkipped, Detail: fmt.Sprintf("%s: %s", label, reason)})
		result.Skipped = append(result.Skipped, fmt.Sprintf("%s: %s", mod.Name, reason))
		result.Failed = append(result.Failed, mod.Name)
	}

	emit(ModEvent{Scope: scope, Phase: InstallDepInstalling, Version: mod.Version})

	hookCtx := hookContextFor(game)
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.before_each", hooks.GetInstallBeforeEach()); err != nil {
		skip("Skipped", fmt.Sprintf("install.before_each hook failed: %v", err))
		return nil
	}

	installer := s.NewInstallerWithLinker(game, s.GetLinker(linkMethod))

	// mod.SourceID (NOT plan.SourceID) is used for every source call below,
	// matching batchInstallMods' own `sourceID := mod.SourceID` exactly -
	// this only ever differs from plan.SourceID in the SourceLocal edge
	// case InstallPlan.Dependencies' doc comment already documents as
	// unreachable via any registered source in practice.
	//
	// File selection is derived BEFORE the uninstall-existing block below
	// (#143 review finding F2, mirroring cmd/lmm's batchInstallMods after
	// its own F1 reorder): the #143 lock check must judge the selected
	// version before anything is removed, and a fetch/selection failure now
	// skips this mod while its previous installation is still intact.
	var selected []*domain.DownloadableFile
	if overrideFiles != nil {
		// #96/#140: the primary's --version/--file-resolved FINAL selection,
		// already fetched and matched by ApplyInstall before the loop
		// started - see this function's own doc comment.
		selected = make([]*domain.DownloadableFile, len(overrideFiles))
		for i := range overrideFiles {
			selected[i] = &overrideFiles[i]
		}
	} else {
		files, err := s.GetModFiles(ctx, mod.SourceID, mod)
		if err != nil {
			skip("Error", fmt.Sprintf("failed to get mod files: %v", err))
			return nil
		}
		files = FilterAndSortFiles(files, plan.ShowArchived)
		if len(files) == 0 {
			skip("Error", "no downloadable files available")
			return nil
		}
		selected, _, err = selectDeployFiles(files, nil, false)
		if err != nil {
			skip("Error", err.Error())
			return nil
		}
	}
	mod.Version = domain.EffectiveInstalledVersion(mod.Version, selected) // #94

	// #143: a LOCKED profile ref converges only via explicit lock/unlock -
	// skip (batch per-mod semantics) when the selected version differs from
	// the lock, BEFORE the uninstall-existing block and this mod's
	// download/deploy. This is NOT redundant with ApplyInstall's up-front
	// lockedInstallRefusal, for the PRIMARY included: that gate deliberately
	// passes when its own derivation fails transiently (ok=false), and a
	// locked, already-installed primary with a missing dependency then
	// reaches this loop (review finding F2 - the earlier post-uninstall
	// placement let that fallthrough uninstall the deployed lock target and
	// delete its cache before skipping). It equally covers a DEPENDENCY
	// whose ref is locked in the profile but absent from the DB (drift) -
	// only a ref without a DB row still resolves as a dependency - which
	// would otherwise deploy and leave drift behind UpsertMod's refusal
	// Note below.
	if prof, err := pm.Get(game.ID, plan.Profile); err == nil {
		if ref := prof.FindRef(mod.SourceID, mod.ID); ref != nil && ref.Locked && ref.Version != mod.Version {
			skip("Skipped", LockedRefRefusalError(*mod, plan.Profile, ref).Error())
			return nil
		}
	}

	if existing, err := s.GetInstalledMod(ctx, mod.SourceID, mod.ID, game.ID, plan.Profile); err == nil && existing != nil {
		emit(ModEvent{Scope: scope, Phase: InstallDepReinstalling})
		if err := installer.Uninstall(ctx, game, &existing.Mod, plan.Profile); err != nil {
			msg := fmt.Sprintf("Warning: could not remove old files: %v", err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: InstallNote, Detail: msg})
		}
		if err := s.GetGameCache(game).Delete(game.ID, existing.SourceID, existing.ID, existing.Version); err != nil {
			msg := fmt.Sprintf("Warning: could not clear old cache: %v", err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: InstallNote, Detail: msg})
		}
	}

	// #140: overrideFiles may pin several files (--file A,B) - download and
	// record EVERY selected file, in order, matching the STRICT path's own
	// plan.Files loop. The nil-override derivation above always selects
	// exactly one file, so dependencies and unpinned primaries keep
	// batchInstallMods' historical single-file mechanics (and event stream)
	// unchanged.
	fileIDs := make([]string, 0, len(selected))
	var checksums []fileChecksum
	filesExtracted := 0
	for _, file := range selected {
		if err := ctx.Err(); err != nil {
			// skip() (not a bare nil) so the mod lands in Skipped AND
			// Failed: the primary is the last entry in the batch loop by
			// construction, so a silent return made ApplyInstall exit 0
			// having installed nothing (review finding I2).
			skip("Error", fmt.Sprintf("cancelled: %v", err))
			return nil
		}
		emit(StepEvent{Scope: scope, Phase: InstallDepFileSelected, File: file})

		progressFn := func(e Event) {
			d, ok := e.(DownloadEvent)
			if !ok || d.TotalBytes <= 0 {
				return
			}
			emit(DownloadEvent{Scope: scope, Phase: InstallDepDownloading, Percent: d.Percent})
		}
		downloadResult, err := s.downloadMod(ctx, mod.SourceID, game, mod, file, progressFn)

		// Unconditional (success OR failure alike), mirroring batchInstallMods'
		// own `fmt.Println()` immediately after the download call returns -
		// see InstallDepDownloadDone's doc comment for why this precedes the
		// failure branch's own InstallDepSkipped event.
		emit(StepEvent{Scope: scope, Phase: InstallDepDownloadDone})

		if err != nil {
			skip("Error", fmt.Sprintf("download failed: %v", err))
			return nil
		}

		if !opts.SkipVerify && downloadResult.Checksum != "" {
			emit(StepEvent{Scope: scope, Phase: InstallChecksumComputed, Detail: downloadResult.Checksum})
			checksums = append(checksums, fileChecksum{fileID: file.ID, checksum: downloadResult.Checksum})
		}

		filesExtracted += downloadResult.FilesExtracted
		fileIDs = append(fileIDs, file.ID)
	}

	if !opts.Force {
		if conflicts, err := installer.GetConflicts(ctx, game, mod, plan.Profile); err == nil && len(conflicts) > 0 {
			emit(WarningEvent{Scope: scope, Phase: InstallDepConflictWarning, Message: fmt.Sprintf("%d file conflict(s) - will overwrite", len(conflicts))})
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
		FileIDs:      fileIDs,
	}
	installedMod.Mod.GameID = game.ID
	if err := s.saveInstalledMod(ctx, installedMod); err != nil {
		skip("Error", fmt.Sprintf("failed to save mod: %v", err))
		return nil
	}

	for _, cs := range checksums {
		if err := s.saveFileChecksum(ctx, mod.SourceID, mod.ID, game.ID, plan.Profile, cs.fileID, cs.checksum); err != nil {
			msg := fmt.Sprintf("failed to save checksum: %v", err)
			result.Warnings = append(result.Warnings, msg)
			emit(WarningEvent{Scope: scope, Phase: InstallWarning, Message: msg})
		}
	}

	if err := ensureProfileExists(pm, game.ID, plan.Profile); err != nil {
		msg := fmt.Sprintf("Warning: could not create profile: %v", err)
		result.Notes = append(result.Notes, msg)
		emit(StepEvent{Scope: scope, Phase: InstallNote, Detail: msg})
	}
	modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: fileIDs}
	if err := pm.UpsertMod(game.ID, plan.Profile, modRef); err != nil {
		msg := fmt.Sprintf("Warning: could not update profile: %v", err)
		result.Notes = append(result.Notes, msg)
		emit(StepEvent{Scope: scope, Phase: InstallNote, Detail: msg})
	}

	result.Installed = append(result.Installed, mod.Name)
	emit(ModEvent{Scope: scope, Phase: InstallDepInstalled, FilesExtracted: filesExtracted})

	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.after_each", hooks.GetInstallAfterEach()); err != nil {
		msg := fmt.Sprintf("install.after_each hook failed for %s: %v", mod.ID, err)
		result.Warnings = append(result.Warnings, msg)
		return &WarningEvent{Scope: scope, Phase: InstallWarning, Message: msg}
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
func (s *Service) applyInstallPrimary(ctx context.Context, game *domain.Game, plan *InstallPlan, linkMethod domain.LinkMethod, pm *ProfileManager, opts InstallOptions, hooks *ResolvedHooks, runner *HookRunner, result *InstallResult, emit func(Event)) (*WarningEvent, error) {
	mod := plan.Mod // local, addressable copy - distinct from plan.Mod

	// #94: record what is actually being installed. plan.Files is the final
	// selection (the CLI --file/picker path overwrites it after PlanInstall),
	// and mod.Version keys the cache, the DB row, and the profile ref below.
	selectedFiles := make([]*domain.DownloadableFile, len(plan.Files))
	for i := range plan.Files {
		selectedFiles[i] = &plan.Files[i]
	}
	mod.Version = domain.EffectiveInstalledVersion(mod.Version, selectedFiles)

	// scope carries the download/checksum events; modScope carries the
	// mod-lifecycle events below. Both now carry the same full identity
	// (source + mod ID) - kept as separate values because they're read at
	// different points as selectedFiles/mod.Version settle.
	scope := Scope{Op: OpInstall, ModName: mod.Name, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}}
	modScope := Scope{Op: OpInstall, ModName: mod.Name, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}}

	hookCtx := hookContextFor(game)
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = mod.ID, mod.Name, mod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.before_each", hooks.GetInstallBeforeEach()); err != nil {
		if !opts.Force {
			return nil, fmt.Errorf("install.before_each hook failed: %w", err)
		}
		msg := fmt.Sprintf("install.before_each hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(HookEvent{Scope: scope, Phase: InstallBeforeEachForced, Stage: "install.before_each", Detail: msg})
	}

	installer := s.NewInstallerWithLinker(game, s.GetLinker(linkMethod))
	downloadCache := s.GetGameCache(game)

	var reinstallTxn *reinstallCacheTransaction
	if plan.Replaces != nil && plan.Replaces.Version == mod.Version {
		var txnErr error
		reinstallTxn, txnErr = prepareReinstallCacheTransaction(ctx, s.GetGameCache(game), game.ID, plan.Replaces.SourceID, plan.Replaces.ID, plan.Replaces.Version, s.logger())
		if txnErr != nil {
			return nil, fmt.Errorf("preparing reinstall cache: %w", txnErr)
		}
		downloadCache = reinstallTxn.staged
		defer func() {
			if reinstallTxn != nil {
				// WithoutCancel: this deferred cleanup fires precisely when the
				// caller's ctx is already dead, and its restore must not be
				// interrupted halfway (review finding C1).
				_ = reinstallTxn.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // best-effort cleanup on an already-erroring path
			}
		}()
	}

	var downloadedFileIDs []string
	var checksums []fileChecksum

	// Resolve the source to check MergeCompiler capability for .pak gating (#221)
	src, err := s.GetSource(plan.SourceID)
	if err != nil {
		return nil, fmt.Errorf("resolving source %q: %w", plan.SourceID, err)
	}
	mc, isMergeCompiler := src.(source.MergeCompiler)

	// compiledFiles accumulates every file this loop actually compiled (game
	// DeployCompile + a ".exmodz" file - the same condition
	// DownloadModToCache itself gates on), re-derived here rather than read
	// back from DownloadModToCache's result since flows.go already has
	// everything the condition needs. Drives the InstallCompiling
	// announcement below in place of the generic InstallExtracting one.
	// #221: convert-eligible .pak files are only included if the source
	// implements MergeCompiler, matching the ingest path's predicate.
	var compiledFiles []*domain.DownloadableFile
	filesTotal := len(plan.Files)
	for i := range plan.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file := &plan.Files[i]

		fileScope := scope
		fileScope.Index, fileScope.Total = i+1, filesTotal

		emit(StepEvent{Scope: fileScope, Phase: InstallDownloadStarted, File: file})

		progressFn := func(e Event) {
			d, ok := e.(DownloadEvent)
			if !ok {
				return
			}
			emit(DownloadEvent{
				Scope: fileScope, Phase: InstallDownloading, File: file,
				Percent: d.Percent, Downloaded: d.Downloaded, TotalBytes: d.TotalBytes,
			})
		}

		downloadResult, dlErr := s.downloadModToCache(ctx, downloadCache, plan.SourceID, game, &mod, file, progressFn)

		emit(StepEvent{Scope: fileScope, Phase: InstallDownloadDone, File: file})

		if dlErr != nil {
			reason := fmt.Sprintf("download failed: %v", dlErr)
			emit(ModEvent{Scope: fileScope, Phase: InstallDownloadFailed, Detail: reason})
			if strings.Contains(dlErr.Error(), "third-party downloads") && mod.SourceURL != "" {
				return nil, fmt.Errorf("download unavailable via API")
			}
			return nil, fmt.Errorf("download failed: %w", dlErr)
		}

		if !opts.SkipVerify && downloadResult.Checksum != "" {
			emit(StepEvent{Scope: fileScope, Phase: InstallChecksumComputed, File: file, Detail: downloadResult.Checksum})
			checksums = append(checksums, fileChecksum{fileID: file.ID, checksum: downloadResult.Checksum})
		}

		result.FilesDeployed += downloadResult.FilesExtracted
		downloadedFileIDs = append(downloadedFileIDs, file.ID)

		// Copilot round 1 (PR #222): compiledFiles collects BOTH kinds -
		// .exmodz files and convert-eligible raw files alike - so the
		// InstallCompiling/"Retaining ... for merge" messaging below (and
		// in cmd/lmm/install.go's InstallCompiling case) fires for a
		// convert-eligible raw pak exactly as it does for a native
		// .exmodz; len(compiledFiles) > 0 doesn't care which kind matched.
		// #221: gate raw files on MergeCompiler capability, matching the
		// ingest path's predicate. Native merge archives are still included
		// when only the GAME's compile source (not this file's own source)
		// recognizes them - isNativeMergeFile's fallback - because ingest
		// hard-errors on exactly that mismatch later (#256).
		if game.DeployMode == domain.DeployCompile && (s.isNativeMergeFile(game, mc, file.FileName) || (isMergeCompiler && isConvertEligibleArtifact(game, mc, file.FileName))) {
			compiledFiles = append(compiledFiles, file)
		}
	}

	// A retained-for-merge file was never "extracted" - announce the step
	// by name instead of the generic message, which is actively misleading
	// here (#190 item 1). Only fires for files that actually go through
	// ingest's validate+retain branch, so every non-DeployCompile (or
	// non-exmodz) install keeps today's exact "Extracting to cache..." text
	// unchanged. #197: this no longer compiles a per-mod pak - the merge
	// happens later, batched across the whole profile, via
	// Service.syncMergedPak - so there is no compiled output filename left
	// to announce (Detail is unset).
	if len(compiledFiles) > 0 {
		for _, cf := range compiledFiles {
			emit(StepEvent{Scope: scope, Phase: InstallCompiling, File: cf})
		}
	} else {
		emit(StepEvent{Scope: modScope, Phase: InstallExtracting})
	}

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

	emit(StepEvent{Scope: modScope, Phase: InstallDeploying})

	if plan.Replaces != nil {
		if reinstallTxn != nil {
			if err := reinstallTxn.Activate(ctx); err != nil {
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
				// recovery must not inherit the caller's cancellation (v2 Phase 1 Task 3 C1 class)
				rctx := context.WithoutCancel(ctx)
				if err := reinstallTxn.RestoreLive(rctx); err != nil {
					s.logger().Warn("rollback after failed install also failed", "step", "restore_live", "err", err)
				}
				if err := installer.ReplaceWithCaches(rctx, game, reinstallTxn.snapshot, s.GetGameCache(game), &plan.Replaces.Mod, &plan.Replaces.Mod, plan.Profile); err != nil {
					s.logger().Warn("rollback after failed install also failed", "step", "replace_with_caches", "err", err)
				}
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

	if s.beforeSaveInstalled != nil {
		s.beforeSaveInstalled()
	}
	if err := s.saveInstalledMod(ctx, installedMod); err != nil {
		// recovery must not inherit the caller's cancellation (v2 Phase 1 Task 3 C1 class)
		rctx := context.WithoutCancel(ctx)
		if plan.Replaces != nil {
			if reinstallTxn != nil {
				if err := reinstallTxn.RestoreLive(rctx); err != nil {
					s.logger().Warn("rollback after failed install also failed", "step", "restore_live", "err", err)
				}
				if err := installer.ReplaceWithCaches(rctx, game, reinstallTxn.staged, s.GetGameCache(game), &mod, &plan.Replaces.Mod, plan.Profile); err != nil {
					s.logger().Warn("rollback after failed install also failed", "step", "replace_with_caches", "err", err)
				}
			} else {
				if err := installer.Replace(rctx, game, &mod, &plan.Replaces.Mod, plan.Profile); err != nil {
					s.logger().Warn("rollback after failed install also failed", "step", "replace", "err", err)
				}
			}
		} else {
			if err := installer.Uninstall(rctx, game, &mod, plan.Profile); err != nil {
				s.logger().Warn("rollback after failed install also failed", "step", "uninstall", "err", err)
			}
		}
		return nil, fmt.Errorf("failed to save mod: %w", err)
	}
	if reinstallTxn != nil {
		if err := reinstallTxn.Commit(); err != nil {
			msg := fmt.Sprintf("Warning: could not finalize reinstall cache transaction: %v", err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: modScope, Phase: InstallNote, Detail: msg})
		}
		reinstallTxn = nil
	}

	for _, fc := range checksums {
		if err := s.saveFileChecksum(ctx, plan.SourceID, mod.ID, game.ID, plan.Profile, fc.fileID, fc.checksum); err != nil {
			msg := fmt.Sprintf("failed to save checksum for file %s: %v", fc.fileID, err)
			result.Warnings = append(result.Warnings, msg)
			emit(WarningEvent{Scope: modScope, Phase: InstallWarning, Message: msg})
		}
	}

	if err := ensureProfileExists(pm, game.ID, plan.Profile); err != nil {
		msg := fmt.Sprintf("Warning: could not create profile: %v", err)
		result.Notes = append(result.Notes, msg)
		emit(StepEvent{Scope: modScope, Phase: InstallNote, Detail: msg})
	}
	modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: downloadedFileIDs}
	if err := pm.UpsertMod(game.ID, plan.Profile, modRef); err != nil {
		msg := fmt.Sprintf("Warning: could not update profile: %v", err)
		result.Notes = append(result.Notes, msg)
		emit(StepEvent{Scope: modScope, Phase: InstallNote, Detail: msg})
	}

	if plan.Replaces != nil && plan.Replaces.Version != mod.Version {
		if err := s.GetGameCache(game).Delete(game.ID, plan.Replaces.SourceID, plan.Replaces.ID, plan.Replaces.Version); err != nil {
			msg := fmt.Sprintf("Warning: could not clear old cache: %v", err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: modScope, Phase: InstallNote, Detail: msg})
		}
	}

	result.Installed = append(result.Installed, mod.Name)
	emit(ModEvent{Scope: modScope, Phase: InstallDone})

	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.after_each", hooks.GetInstallAfterEach()); err != nil {
		msg := fmt.Sprintf("install.after_each hook failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		return &WarningEvent{Scope: modScope, Phase: InstallWarning, Message: msg}, nil
	}
	return nil, nil
}
