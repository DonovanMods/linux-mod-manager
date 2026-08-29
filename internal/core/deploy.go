// Package core provides business logic orchestration for lmm.
// deploy.go holds the deploy flow: DeployOptions/DeployResult, the
// DeployModClass readout enum, and DeployProfile with its private helpers
// (redeployFromSource, purgeForDeploy). Moved verbatim out of flows.go by
// v2 Phase 2 Unit M (#293); the purge loop it shares with PurgeProfile
// (purgeSpec/purgeMods) now lives in purge.go.
package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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

// DeployPlan is PlanDeploy's side-effect-free description of what a deploy
// would do: which mods it would touch (in load order), which game-dir paths
// each one would link or leave removed, what a --purge pass would undeploy
// first, which hooks would run, and - on a DeployCompile game - what the
// merged artifact would carry. ApplyDeploy re-derives the same selection
// under the mutation lock and refuses a plan whose installed-mod set has
// changed since (Ruling 5).
type DeployPlan struct {
	Profile string          `json:"profile"`
	Mods    []DeployPlanMod `json:"mods"`

	// Purge lists the game-dir-relative paths a --purge pass would
	// undeploy before the deploy loop runs: each installed mod's
	// removal-direction union (deployedPathsFor), narrowed to the paths
	// actually present under game.ModPath right now (an os.Lstat check -
	// still a pure read), deduplicated and in purge order. Empty when
	// opts.Purge is false, or when opts.Purge is true but nothing
	// installed is currently deployed. A path here may well be re-linked
	// moments later by the deploy loop - purge undeploys everything
	// currently there, then the loop redeploys the selection.
	Purge []string `json:"purge"`

	// Hooks names the hooks that would actually run, in run order: the
	// uninstall.* family (the purge pass) ahead of the install.* family
	// (the deploy loop). Only configured hooks are listed, only for a pass
	// that has at least one mod to work on, and none at all under
	// SkipHooks.
	Hooks []string `json:"hooks"`

	// Merged is the DeployCompile merge readout (#255) - nil on any other
	// game, and on a compile game whose selection contributes neither a
	// merge source nor a raw fallback.
	Merged *MergePlan `json:"merged,omitempty"`

	// NoChanges reports that this deploy has nothing to act on at all: no
	// mod selected and no purge pass. It is NOT a claim that the deploy
	// would write no bytes - a selected mod always redeploys its files,
	// whether or not they are already on disk.
	NoChanges bool `json:"no_changes"`

	// snapshot is Ruling 5's precondition: the installed-mod set this plan
	// was computed from, re-derived and compared by ApplyDeploy.
	snapshot installedSnapshot `json:"-"`
}

// DeployPlanMod is one mod's row in a DeployPlan, in the load order the
// deploy loop walks.
type DeployPlanMod struct {
	Ref  domain.ModReference `json:"ref"`
	Name string              `json:"name"`

	// Class is the #255 readout: how this mod's content reaches the game
	// directory on a DeployCompile game. Always DeployModIndividual
	// elsewhere. Classification is optimistic in exactly the way
	// classifyCompileDeployMods documents.
	Class DeployModClass `json:"class"`

	// Link lists the game-dir-relative paths the deploy would link/copy
	// for this mod, in cache listing order - deployableFiles' result, which
	// is precisely what Installer.Install deploys. On a DeployCompile game
	// a DeployModMerged entry's own Link is empty (#197 - it deploys zero
	// files of its own): its content reaches the game dir only via the
	// profile-level MergePlan.Artifact, written AFTER every mod's Link is
	// applied. A merge participant's contribution is therefore represented
	// EXCLUSIVELY by MergePlan, never inside any mod's Link (Task 24
	// review, Minor #2).
	Link []string `json:"link"`

	// Remove lists the game-dir-relative paths the undeploy-before-redeploy
	// step would remove and NOT put back: the removal direction's full
	// union (or, for an absent cache entry, the DB's tracked deployed
	// paths) minus Link. This is normally empty; it is non-empty exactly
	// where #210's narrowing self-heals a stale, unclaimed deployment.
	// Always empty when Redownload is set - a plan cannot enumerate files
	// it has not fetched yet.
	Remove []string `json:"remove"`

	// Redownload reports that this mod's cache entry is missing: the live
	// deploy heals this by re-downloading from source before installing,
	// so it WOULD deploy this mod (optimistically - Link just cannot be
	// enumerated without doing the fetch), not skip it. Task 24 review,
	// Important #2: this is what tells a renderer apart from a genuine
	// Skipped mod, which never deploys.
	Redownload bool `json:"redownload,omitzero"`

	// Skipped, when set, is why this mod would not deploy its files: "mod
	// not found", or a disabled single-mod selection. A selection problem
	// the pre-lift flow reported only AFTER its --purge pass is plan DATA
	// here, never a PlanDeploy error - see PlanDeploy's doc comment.
	Skipped string `json:"skipped,omitempty"`
}

// MergePlan is the DeployCompile merge readout in plan form (#255): the
// artifact the deploy would produce, the mods whose content it would carry,
// and the mods that deploy raw instead because conversion is unavailable to
// them (the game- or mod-level ConvertPaks opt-out, #221). Names, not refs,
// because this is the same rendering vocabulary DeployMergeSynced uses.
type MergePlan struct {
	Artifact     string   `json:"artifact"`
	Sources      []string `json:"sources"`
	RawFallbacks []string `json:"raw_fallbacks"`
}

// PlanDeploy computes what DeployProfile would do for game/profileName under
// opts, without touching anything. It reads the same installed set, applies
// the same selection rules, and resolves each selected mod's deploy-direction
// files (deployableFiles) and removal-direction files (the cache union, or
// the DB's tracked paths for an absent entry) - so a frontend can show the
// exact file-level consequences of a deploy before running one.
//
// PlanDeploy deliberately does NOT fail on a selection problem. `lmm deploy
// --purge <unknown-id>` historically ran its whole purge pass - hooks,
// warnings and all - and only then failed with "mod not found"; a Plan that
// errored first would silently skip that purge. Such problems are therefore
// recorded as DeployPlanMod.Skipped entries and left for ApplyDeploy to
// raise at the historical point.
//
// The returned plan is a snapshot: pass it to ApplyDeploy promptly, and be
// ready for ErrStalePlan if the installed set moved underneath it.
func (s *Service) PlanDeploy(ctx context.Context, game *domain.Game, profileName string, opts DeployOptions) (*DeployPlan, error) {
	return s.planDeploy(ctx, game, profileName, opts)
}

func (s *Service) planDeploy(ctx context.Context, game *domain.Game, profileName string, opts DeployOptions) (*DeployPlan, error) {
	// Read directly (not via currentInstalledSnapshot) to keep the
	// pre-lift error text ("getting installed mods: …") on this
	// reachable failure path - see planDeploy's doc comment and
	// TestPlanDeploy_InstalledModsReadFailure_PreservesHistoricalErrorText.
	installedMods, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}
	plan := &DeployPlan{Profile: profileName, snapshot: snapshotOf(installedMods)}
	gameCache := s.GetGameCache(game)

	// --purge pass, mirroring deployProfile's own ordering (and its nil-safe
	// profile fallback) so the listed paths come out in purge order. Reuses
	// the read above instead of re-querying the same rows.
	purgeMods := 0
	if opts.Purge {
		profile, _ := config.LoadProfile(s.configDir, game.ID, profileName)
		mods := OrderByProfile(profile, installedMods)
		purgeMods = len(mods)
		seen := make(map[string]bool)
		for i := range mods {
			for _, f := range s.deployedPathsFor(ctx, game, profileName, &mods[i]) {
				if seen[f] {
					continue
				}
				seen[f] = true
				// A mod's removal-direction union names everything that
				// COULD be undeployed; only paths actually present under
				// game.ModPath right now are what a purge would actually
				// touch (Task 24 review, Minor #1). Lstat, not Stat - a
				// dangling symlink is still a deployed path to remove.
				if _, err := os.Lstat(filepath.Join(game.ModPath, f)); err != nil {
					continue
				}
				plan.Purge = append(plan.Purge, f)
			}
		}
	}

	// Selection, mirroring deployProfile's three branches. The --purge
	// variant's enabledBeforePurge map is exactly "Enabled as it stands
	// now" (a purge marks mods not-deployed, never not-enabled), so all
	// three collapse to All-or-Enabled here.
	var modsToDeploy []*domain.InstalledMod
	switch {
	case opts.ModID != "":
		mod, err := s.GetInstalledMod(ctx, opts.SourceID, opts.ModID, game.ID, profileName)
		switch {
		case err != nil:
			plan.Mods = append(plan.Mods, DeployPlanMod{
				Ref:     domain.ModReference{SourceID: opts.SourceID, ModID: opts.ModID},
				Name:    opts.ModID,
				Skipped: "mod not found",
			})
		case !mod.Enabled && !opts.All:
			plan.Mods = append(plan.Mods, DeployPlanMod{
				Ref:     domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID},
				Name:    mod.Name,
				Skipped: fmt.Sprintf("mod %s is disabled - use --all to deploy disabled mods, or enable it with 'lmm mod enable %s'", mod.Name, opts.ModID),
			})
		default:
			modsToDeploy = append(modsToDeploy, mod)
		}
	default:
		mods, err := s.GetInstalledModsInProfileOrder(ctx, game.ID, profileName)
		if err != nil {
			return nil, fmt.Errorf("getting installed mods: %w", err)
		}
		for i := range mods {
			if opts.All || mods[i].Enabled {
				modsToDeploy = append(modsToDeploy, &mods[i])
			}
		}
	}

	classes := s.classifyCompileDeployMods(ctx, game, profileName, modsToDeploy)
	for _, mod := range modsToDeploy {
		entry := DeployPlanMod{
			Ref:   domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID},
			Name:  mod.Name,
			Class: classes[domain.ModKey(mod.SourceID, mod.ID)],
		}
		if !gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
			// The deploy loop heals this by re-downloading from source and
			// then deploying it - this mod WOULD deploy, so it is not a
			// Skipped entry (Important #2). What it would then link is not
			// knowable without doing the fetch, so Link stays empty.
			entry.Redownload = true
			plan.Mods = append(plan.Mods, entry)
			continue
		}
		// One ListFiles call serves both directions: the cache's existence
		// was just confirmed above, so this mirrors deployedPathsFor's own
		// success path (its full removal-direction union IS this raw
		// listing) without listing the same entry a second time (Task 24
		// review, Minor #5).
		files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
		if err != nil {
			// A readout, never a reason to fail a plan: the deploy itself
			// would surface this as that mod's own skip reason.
			s.logger().Warn("resolving deployable files failed while planning a deploy",
				"game_id", game.ID, "profile", profileName, "mod", domain.ModKey(mod.SourceID, mod.ID), "err", err)
			plan.Mods = append(plan.Mods, entry)
			continue
		}
		link, err := deployableFilesFromListing(gameCache, game.ID, mod.SourceID, mod.ID, mod.Version, files)
		if err != nil {
			s.logger().Warn("resolving deployable files failed while planning a deploy",
				"game_id", game.ID, "profile", profileName, "mod", domain.ModKey(mod.SourceID, mod.ID), "err", err)
			plan.Mods = append(plan.Mods, entry)
			continue
		}
		entry.Link = link
		linked := make(map[string]bool, len(link))
		for _, f := range link {
			linked[f] = true
		}
		for _, f := range files {
			if !linked[f] {
				entry.Remove = append(entry.Remove, f)
			}
		}
		plan.Mods = append(plan.Mods, entry)
	}

	plan.Hooks = planDeployHooks(s.resolvedHooksForPlan(ctx, game, profileName), opts, purgeMods, len(modsToDeploy))
	plan.Merged = s.planMerge(game, plan.Mods)
	plan.NoChanges = len(plan.Mods) == 0 && len(plan.Purge) == 0
	return plan, nil
}

// deployedPathsFor returns the game-dir-relative paths an Installer.Uninstall
// of mod would remove, by the same two-step rule Uninstall itself uses: the
// cache entry's full ListFiles union, falling back to the DB's tracked
// deployed paths when that entry is wholly absent (#260). Best-effort - a
// listing failure is logged and reported as "nothing known", never an error
// that fails the plan.
func (s *Service) deployedPathsFor(ctx context.Context, game *domain.Game, profileName string, mod *domain.InstalledMod) []string {
	files, err := s.GetGameCache(game).ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err == nil {
		return files
	}
	if !errors.Is(err, fs.ErrNotExist) {
		s.logger().Warn("listing cached files failed while planning a deploy",
			"game_id", game.ID, "mod", domain.ModKey(mod.SourceID, mod.ID), "err", err)
		return nil
	}
	tracked, dbErr := s.db.GetDeployedFilesForMod(ctx, game.ID, profileName, mod.SourceID, mod.ID)
	if dbErr != nil {
		s.logger().Warn("listing tracked deployed files failed while planning a deploy",
			"game_id", game.ID, "mod", domain.ModKey(mod.SourceID, mod.ID), "err", dbErr)
		return nil
	}
	return tracked
}

// resolvedHooksForPlan is resolvedHooks for a read-only caller: hook
// resolution never actually fails (see its doc comment), and a plan has no
// business inventing an error path the flow itself does not have.
func (s *Service) resolvedHooksForPlan(ctx context.Context, game *domain.Game, profileName string) *ResolvedHooks {
	hooks, err := s.resolvedHooks(ctx, game, profileName)
	if err != nil {
		s.logger().Warn("resolving hooks failed while planning a deploy", "game_id", game.ID, "profile", profileName, "err", err)
		return ResolveHooks(game, nil)
	}
	return hooks
}

// planDeployHooks lists the hook names a deploy would run, in run order.
// A pass with nothing to work on runs none of its hooks (purgeMods == 0
// returns from purgeMods before its before_all; an empty selection returns
// from deployProfile before install.before_all), and SkipHooks suppresses
// every one of them.
func planDeployHooks(hooks *ResolvedHooks, opts DeployOptions, purgeMods, deployMods int) []string {
	if opts.SkipHooks {
		return nil
	}
	var names []string
	add := func(name, command string) {
		if command != "" {
			names = append(names, name)
		}
	}
	if opts.Purge && purgeMods > 0 {
		add("uninstall.before_all", hooks.GetUninstallBeforeAll())
		add("uninstall.before_each", hooks.GetUninstallBeforeEach())
		add("uninstall.after_each", hooks.GetUninstallAfterEach())
		add("uninstall.after_all", hooks.GetUninstallAfterAll())
	}
	if deployMods > 0 {
		add("install.before_all", hooks.GetInstallBeforeAll())
		add("install.before_each", hooks.GetInstallBeforeEach())
		add("install.after_each", hooks.GetInstallAfterEach())
		add("install.after_all", hooks.GetInstallAfterAll())
	}
	return names
}

// planMerge builds the DeployCompile merge readout from the already-computed
// per-mod classes, so the plan and the DeployDeployed events a later apply
// emits cannot disagree about which mod is which. Returns nil unless this is
// a compile game whose selection actually contributes to (or is excluded
// from) a merge.
func (s *Service) planMerge(game *domain.Game, mods []DeployPlanMod) *MergePlan {
	if game.DeployMode != domain.DeployCompile {
		return nil
	}
	merged := &MergePlan{}
	for _, m := range mods {
		switch m.Class {
		case DeployModMerged:
			merged.Sources = append(merged.Sources, m.Name)
		case DeployModRaw:
			merged.RawFallbacks = append(merged.RawFallbacks, m.Name)
		case DeployModIndividual:
		}
	}
	if len(merged.Sources) == 0 && len(merged.RawFallbacks) == 0 {
		return nil
	}
	mc, err := s.mergeCompilerForGame(game)
	if err != nil {
		// Same best-effort stance as classifyCompileDeployMods: name what
		// we can, never fail a readout.
		s.logger().Warn("resolving compile source failed while planning a deploy", "game_id", game.ID, "err", err)
		return merged
	}
	merged.Artifact = mc.MergedArtifactName()
	return merged
}

// ApplyDeploy carries out plan under the mutation lock. Ruling 5: the plan's
// recorded installed-mod set is re-derived first and a mismatch is refused
// with ErrStalePlan rather than applied.
//
// The plan supplies the profile and the freshness precondition; the deploy
// itself re-derives its own selection from opts, exactly as it always has -
// which is also why a selection PlanDeploy could only record as Skipped (an
// unknown or disabled mod ID) still fails here, at the historical point,
// after any --purge pass has run and recorded its diagnostics.
//
// sink may be nil. When non-nil, it is called synchronously for every
// notable event - see DeployPhase's constants for what each one means, and
// each event type's own doc comment for the payload it carries.
func (s *Service) ApplyDeploy(ctx context.Context, game *domain.Game, plan *DeployPlan, opts DeployOptions, sink EventSink) (*DeployResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &DeployResult{}, err
	}
	defer release()
	return s.applyDeploy(ctx, game, plan, opts, sink)
}

func (s *Service) applyDeploy(ctx context.Context, game *domain.Game, plan *DeployPlan, opts DeployOptions, sink EventSink) (*DeployResult, error) {
	if plan == nil {
		return &DeployResult{}, errors.New("deploy plan is nil: call PlanDeploy first")
	}
	if err := s.checkPlanFresh(ctx, game.ID, plan.Profile, plan.snapshot); err != nil {
		return &DeployResult{}, err
	}
	return s.deployProfile(ctx, game, plan.Profile, opts, sink)
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
// It is PlanDeploy + ApplyDeploy in one call, under a single mutation slot,
// for callers with no prompt to show between the two (core's own flows and
// tests). Frontends plan first, render, then apply.
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
	plan, err := s.planDeploy(ctx, game, profileName, opts)
	if err != nil {
		return &DeployResult{}, err
	}
	return s.applyDeploy(ctx, game, plan, opts, sink)
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
