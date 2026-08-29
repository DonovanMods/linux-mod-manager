// Package core provides business logic orchestration for lmm.
// deploy.go holds the deploy flow: DeployOptions/DeployResult, the
// DeployModClass readout enum, and DeployProfile with its private helpers
// (redeployFromSource, purgeForDeploy). Moved verbatim out of flows.go by
// v2 Phase 2 Unit M (#293); the purge loop it shares with PurgeProfile
// (purgeSpec/purgeMods) stays behind until Unit M's purge task moves it.
package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

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
	installer := s.newInstallerWithLinker(game, s.getLinker(linkMethod))

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
