// Package core provides business logic orchestration for lmm.
// purge.go holds the purge flow: purgeSpec/purgeMods - THE shared purge
// loop (#61), consumed by both `lmm purge` and deploy.go's purgeForDeploy -
// plus PurgeOptions/PurgeResult and PurgeProfile. Moved verbatim out of
// flows.go by v2 Phase 2 Unit M (#293), completing the split deploy.go's
// header comment anticipated.
package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

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
	skipped  *[]InstalledRef
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

	// The paths this purge actually removed, accumulated across the loop:
	// #415's bounded prune runs ONCE at the end over the whole set, which
	// is what the single trailing CleanupEmptyDirs has always been. Doing
	// it per mod would be equivalent but would re-read the same directories
	// once per mod.
	var removed []string

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
				*spec.skipped = append(*spec.skipped, skippedRef(&mod, detail))
				spec.emit(ModEvent{Scope: scope, Phase: PurgeModSkipped, Detail: detail})
			}
			continue
		}

		modRemoved, err := installer.uninstall(ctx, game, &mod.Mod, profileName)
		removed = append(removed, modRemoved...)
		if err != nil {
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
				*spec.skipped = append(*spec.skipped, skippedRef(&mod, fmt.Sprintf("failed to remove record: %v", err)))
				continue
			}
			// Ruling 16 (A): the record delete above already committed, so
			// the ref removal that completes it finishes regardless of
			// cancellation; the cancellation itself then ends the loop.
			if err := completeProfileWrite(ctx, func(ctx context.Context) error {
				return s.NewProfileManager().RemoveMod(ctx, game.ID, profileName, mod.SourceID, mod.ID)
			}); err != nil {
				if cerr := ctx.Err(); cerr != nil {
					return cerr
				}
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

	linker.CleanupEmptyDirs(game.ModPath, removed)
	spec.emit(StepEvent{Scope: Scope{Op: spec.op}, Phase: PurgeComplete})
	return nil
}

// PurgePlan is PlanPurge's side-effect-free description of what `lmm purge`
// would do: exactly which installed mods would be undeployed, whether their
// records go with them, and which hooks would run. Mods IS the set ApplyPurge
// purges - a frontend counts it in its confirmation prompt and hands the same
// object back, so the number shown and the number purged cannot disagree.
// ApplyPurge refuses a plan whose installed-mod set has changed since
// (Ruling 5).
type PurgePlan struct {
	Profile string `json:"profile"`

	// Mods is the profile's installed set, in GetInstalledMods' order -
	// the read the CLI used to do itself before prompting. Empty for a
	// profile with nothing installed, which is the frontend's "No mods
	// installed" early-out (PurgeProfile itself returns immediately for an
	// empty set: no hooks, no events).
	Mods []domain.InstalledMod `json:"mods"`

	// Uninstall echoes PurgeOptions.Uninstall: true also deletes each
	// purged mod's DB record and profile-YAML entry.
	Uninstall bool `json:"uninstall"`

	// Hooks names the uninstall.* hooks that would actually run, in run
	// order. Only configured hooks are listed, none at all under SkipHooks,
	// and none for an empty Mods set.
	Hooks []string `json:"hooks"`

	// External names every EXTERNAL mod in the profile (#269) - a Steam
	// Workshop item lmm tracks but never deploys - by display name. They
	// are absent from Mods because a purge does not touch them at all;
	// naming them here is how the preview says what it will NOT do, rather
	// than leaving the user to notice the count is short.
	External []string `json:"external,omitempty"`

	// MergedArtifact is what purgeMergedPak would do to the profile's
	// merged artifact on a DeployCompile game - an effect Mods cannot
	// express, since exmodz mods have no per-mod deployment of their own
	// (#197 I2). Always a removal when set; nil when the game does not
	// deploy by compilation, and nil when there is no deployed artifact to
	// remove (Ruling 8). See mergedArtifactEffectForPurge. omitzero
	// (phase-end review Minor 7): a non-compile game's plan omits the key
	// entirely instead of carrying merged_artifact: null, matching this
	// phase's own not-applicable-is-absent convention (convert_paks,
	// auth, cache_path, updated_at).
	MergedArtifact *MergedArtifactEffect `json:"merged_artifact,omitzero"`

	// RecordedOnly is set when Profile is not the game's active profile
	// (#445). The game directory holds the active profile's mods, so such a
	// purge is a cleanup of what Profile itself put there: it removes the
	// paths in Remove - the ones Profile has a deployed-file record for and
	// nothing else still claims - and nothing else. A path something else
	// claims (PurgeKeptReason) is left, file and record, and listed in Kept.
	// It runs no hooks, removes no records or profile entries (--uninstall
	// is refused with ErrProfileNotActive), and leaves a merged artifact to
	// the paths Profile recorded. Mods is then the installed mods with a path
	// in Remove.
	RecordedOnly bool `json:"recorded_only,omitzero"`
	// ActiveProfile names the game's active profile when RecordedOnly is
	// set.
	ActiveProfile string `json:"active_profile,omitempty"`
	// Remove is the paths a RecordedOnly purge removes, relative to the
	// game's mod directory, in path order.
	Remove []string `json:"remove,omitempty"`
	// Kept is the paths a RecordedOnly purge leaves, and why.
	Kept []PurgeKeptPath `json:"kept,omitempty"`

	// snapshot is Ruling 5's precondition: the installed-mod set this plan
	// was computed from, re-derived and compared by ApplyPurge.
	snapshot installedSnapshot `json:"-"`
}

// PurgeKeptPath is a path a recorded-only purge (#445) leaves in place -
// its file and the purged profile's record of it - and the first reason it
// found to (PurgeKeptReason).
type PurgeKeptPath struct {
	Path   string          `json:"path"`
	Reason PurgeKeptReason `json:"reason"`
	// Profiles names the other profiles recording the path
	// (PurgeKeptRecorded), or the active profile listing its mod
	// (PurgeKeptListed).
	Profiles []string `json:"profiles,omitempty"`
	// Games names the other games recording the path (PurgeKeptOtherGame).
	Games []string `json:"games,omitempty"`
}

// PurgeKeptReason is why a recorded-only purge left a path the purged
// profile recorded. A path is removed only on proof that it is that
// profile's and nothing else anyone wants live (#445 review), so each of
// these keeps it.
type PurgeKeptReason string

const (
	// PurgeKeptRecorded: another profile of the game records the path too.
	PurgeKeptRecorded PurgeKeptReason = "recorded"
	// PurgeKeptListed: the active profile's document lists the path's mod,
	// not marked off, so the file may be live for it without a record of
	// its own - what a v1.30.1 switch between profiles sharing a mod left
	// (review F3).
	PurgeKeptListed PurgeKeptReason = "listed"
	// PurgeKeptOtherGame: another game whose mod directory holds the path
	// records it (review F7).
	PurgeKeptOtherGame PurgeKeptReason = "other_game"
	// PurgeKeptUserFile: the game's adapter hands the file to the user
	// after its first deploy (adapter.RouteCopyOnce - BepInEx's config
	// files) or never deploys it, so it is never lmm's to remove - the rule
	// every other removal already follows (#413, review F1).
	PurgeKeptUserFile PurgeKeptReason = "user_file"
)

// PlanPurge computes what PurgeProfile would do for game/profileName under
// opts, without touching anything - including the installed-mods read the
// pre-lift cmd/lmm/purge.go did itself before prompting (its "getting
// installed mods: …" wording is preserved on that read's failure).
//
// A profile that is not the game's active one gets a recorded-only plan
// (#445, PurgePlan.RecordedOnly): its files are not the ones the game
// directory is meant to hold, so only what it recorded putting there, and
// no other profile records, is removed.
//
// The returned plan is a snapshot: pass it to ApplyPurge promptly, and be
// ready for ErrStalePlan if the installed set moved underneath it.
func (s *Service) PlanPurge(ctx context.Context, game *domain.Game, profileName string, opts PurgeOptions) (*PurgePlan, error) {
	live, err := s.liveProfile(ctx, game.ID)
	if err != nil {
		return nil, err
	}
	if live != profileName {
		plan, installed, err := s.planRecordedPurge(ctx, game, profileName, live, opts)
		if err != nil {
			return nil, err
		}
		// A removal: the adapter has no say (removalSnapshotOf).
		plan.snapshot = removalSnapshotOf(installed)
		return plan, nil
	}
	installed, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}
	// #269: external mods are excluded from the purge set and named
	// separately. The snapshot still covers the FULL installed set - the
	// staleness precondition is about the profile changing underneath the
	// plan, which an external mod appearing or vanishing absolutely is.
	mods, external := partitionExternal(installed)
	plan := &PurgePlan{
		Profile:        profileName,
		Mods:           mods,
		External:       external,
		Uninstall:      opts.Uninstall,
		MergedArtifact: s.mergedArtifactEffectForPurge(ctx, game, profileName),
		// A removal: the adapter has no say (removalSnapshotOf).
		snapshot: removalSnapshotOf(installed),
	}
	if len(mods) > 0 {
		plan.Hooks = uninstallHookNames(s.resolvedHooksForPlan(ctx, game, profileName), opts.SkipHooks)
	}
	return plan, nil
}

// ApplyPurge carries out plan under the mutation lock. Ruling 5: the plan's
// recorded installed-mod set is re-derived first and a mismatch is refused
// with ErrStalePlan rather than applied.
//
// The mods purged are the plan's own - the same objects a frontend counted
// in its confirmation prompt - never a fresh read that could have grown or
// shrunk between the prompt and the answer.
//
// sink may be nil; see PurgeProfile for what it receives.
func (s *Service) ApplyPurge(ctx context.Context, game *domain.Game, plan *PurgePlan, opts PurgeOptions, sink EventSink) (*PurgeResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &PurgeResult{}, err
	}
	defer release()
	if plan == nil {
		return &PurgeResult{}, errors.New("purge plan is nil: call PlanPurge first")
	}
	live, err := s.liveProfile(ctx, game.ID)
	if err != nil {
		return &PurgeResult{}, err
	}
	switch {
	case plan.RecordedOnly && live == plan.Profile:
		return &PurgeResult{}, fmt.Errorf("%w: %s became the active profile of %s after this recorded-only purge was planned", ErrStalePlan, plan.Profile, game.ID)
	case plan.RecordedOnly:
		if err := s.refuseRecordedUninstall(game.ID, plan.Profile, live, opts); err != nil {
			return &PurgeResult{}, err
		}
		if err := s.checkRemovalPlanFresh(ctx, game.ID, plan.Profile, plan.snapshot); err != nil {
			return &PurgeResult{}, err
		}
		return s.purgeRecorded(ctx, game, plan, sink)
	}
	// A plan that is not recorded-only acts on the live directory for its
	// profile, so that profile has to be the live one.
	if err := s.refuseInactive(ctx, game.ID, plan.Profile, "purge"); err != nil {
		return &PurgeResult{}, err
	}
	if err := s.checkRemovalPlanFresh(ctx, game.ID, plan.Profile, plan.snapshot); err != nil {
		return &PurgeResult{}, err
	}
	return s.purgeProfile(ctx, game, plan.Profile, plan.Mods, opts, sink)
}

// refuseRecordedUninstall refuses --uninstall for a recorded-only purge of
// profileName (#445): it clears files, never records.
func (s *Service) refuseRecordedUninstall(gameID, profileName, live string, opts PurgeOptions) error {
	if !opts.Uninstall {
		return nil
	}
	return fmt.Errorf("%w: cannot purge --uninstall profile %q of %s - the game directory holds the active profile %q's mods, so a purge of %s only clears the files it recorded as deployed; run it without --uninstall, or `lmm profile switch %s` first",
		ErrProfileNotActive, profileName, gameID, live, profileName, profileName)
}

// planRecordedPurge is PlanPurge for a profileName that is not live (see
// PurgePlan.RecordedOnly), without the freshness snapshot - the caller
// takes that from the installed set it also returns.
func (s *Service) planRecordedPurge(ctx context.Context, game *domain.Game, profileName, live string, opts PurgeOptions) (*PurgePlan, []domain.InstalledMod, error) {
	if err := s.refuseRecordedUninstall(game.ID, profileName, live, opts); err != nil {
		return nil, nil, err
	}
	installed, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, nil, fmt.Errorf("getting installed mods: %w", err)
	}
	remove, kept, err := s.recordedPaths(ctx, game, profileName, live)
	if err != nil {
		return nil, nil, err
	}
	withPaths := make(map[string]bool, len(remove))
	plan := &PurgePlan{
		Profile:       profileName,
		Mods:          []domain.InstalledMod{},
		RecordedOnly:  true,
		ActiveProfile: live,
		Kept:          kept,
	}
	for _, row := range remove {
		plan.Remove = append(plan.Remove, row.RelativePath)
		withPaths[domain.ModKey(row.SourceID, row.ModID)] = true
	}
	mods, external := partitionExternal(installed)
	plan.External = external
	for _, m := range mods {
		if withPaths[domain.ModKey(m.SourceID, m.ID)] {
			plan.Mods = append(plan.Mods, m)
		}
	}
	return plan, installed, nil
}

// recordedPaths splits profileName's deployed-file records into the ones a
// recorded-only purge may remove and the ones it keeps, each in path order.
// live is the game's active profile. A path is kept - with the first reason
// found, in PurgeKeptReason's order - when another profile records it, the
// active profile lists its mod, another game whose mod directory holds it
// records it, or the game's adapter does not let lmm remove it. Anything
// that cannot be read to decide that is an error: the purge fails closed.
func (s *Service) recordedPaths(ctx context.Context, game *domain.Game, profileName, live string) (remove []db.DeployedPath, kept []PurgeKeptPath, err error) {
	rows, err := s.db.ListDeployedFiles(ctx, game.ID, profileName)
	if err != nil {
		return nil, nil, fmt.Errorf("listing deployed files: %w", err)
	}
	owners, err := s.db.DeployedPathProfiles(ctx, game.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing deployed files: %w", err)
	}
	listed, err := s.liveListedMods(game.ID, live)
	if err != nil {
		return nil, nil, err
	}
	others, err := s.otherGamesRecording(ctx, game)
	if err != nil {
		return nil, nil, err
	}
	// The adapter's routing does not depend on the link method.
	installer := s.getInstaller(game)
	for _, row := range rows {
		if k, ok := keptPath(row, profileName, owners, listed, others, installer, game); ok {
			kept = append(kept, k)
			continue
		}
		remove = append(remove, row)
	}
	return remove, kept, nil
}

// keptPath is recordedPaths' decision for one row.
func keptPath(row db.DeployedPath, profileName string, owners map[string][]string, listed map[string]string, others *otherGameRecords, installer *Installer, game *domain.Game) (PurgeKeptPath, bool) {
	k := PurgeKeptPath{Path: row.RelativePath}
	if profiles := slices.DeleteFunc(slices.Clone(owners[row.RelativePath]), func(p string) bool { return p == profileName }); len(profiles) > 0 {
		k.Reason, k.Profiles = PurgeKeptRecorded, profiles
		return k, true
	}
	if profile, ok := listed[domain.ModKey(row.SourceID, row.ModID)]; ok {
		k.Reason, k.Profiles = PurgeKeptListed, []string{profile}
		return k, true
	}
	if games := others.recording(row.RelativePath); len(games) > 0 {
		k.Reason, k.Games = PurgeKeptOtherGame, games
		return k, true
	}
	if installer.notLinkerOwned(game, row.RelativePath) {
		k.Reason = PurgeKeptUserFile
		return k, true
	}
	return PurgeKeptPath{}, false
}

// liveListedMods is the mods live's document lists and does not mark off,
// each decided by its first reference, mapped to live - none for a game
// with no profile file, whose "default" has no document. A document that
// cannot be read now is an error.
func (s *Service) liveListedMods(gameID, live string) (map[string]string, error) {
	names, err := config.ListProfiles(s.configDir, gameID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}
	if !slices.Contains(names, live) {
		return nil, nil
	}
	profile, err := config.LoadProfile(s.configDir, gameID, live)
	if err != nil {
		return nil, fmt.Errorf("%w for %s: reading %s's profile file: %w", ErrActiveProfileUnknown, gameID, live, err)
	}
	listed := make(map[string]string)
	for key, ref := range firstRefs(profile.Mods) {
		if !ref.Disabled {
			listed[key] = live
		}
	}
	return listed, nil
}

// otherGameRecords is every path, as a path under one game's mod
// directory, that another game whose mod directory overlaps it records -
// the same directory, one inside the other, or either reached through a
// link.
type otherGameRecords struct {
	root  string // the purged game's mod directory, links resolved
	games []otherGame
}

type otherGame struct {
	id    string
	root  string              // its mod directory, links resolved
	paths map[string][]string // its recorded paths (DeployedPathProfiles)
}

// otherGamesRecording reads the records of every other configured game
// whose mod directory overlaps game's (review F7).
func (s *Service) otherGamesRecording(ctx context.Context, game *domain.Game) (*otherGameRecords, error) {
	records := &otherGameRecords{root: resolvedDir(game.ModPath)}
	for _, other := range s.ListGames() {
		if other.ID == game.ID || other.ModPath == "" {
			continue
		}
		root := resolvedDir(other.ModPath)
		if _, ok := pathWithin(root, records.root); !ok {
			if _, ok := pathWithin(records.root, root); !ok {
				continue
			}
		}
		paths, err := s.db.DeployedPathProfiles(ctx, other.ID)
		if err != nil {
			return nil, fmt.Errorf("listing %s's deployed files: %w", other.ID, err)
		}
		records.games = append(records.games, otherGame{id: other.ID, root: root, paths: paths})
	}
	return records, nil
}

// recording names the other games that record rel, a path under the purged
// game's mod directory.
func (r *otherGameRecords) recording(rel string) []string {
	if r == nil {
		return nil
	}
	abs := filepath.Join(r.root, filepath.FromSlash(rel))
	var games []string
	for _, g := range r.games {
		if theirs, ok := pathWithin(g.root, abs); ok && len(g.paths[filepath.ToSlash(theirs)]) > 0 {
			games = append(games, g.id)
		}
	}
	return games
}

// resolvedDir is dir made absolute and clean, with its links resolved when
// it exists.
func resolvedDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return filepath.Clean(dir)
}

// pathWithin returns path relative to root, when path is root or below it.
func pathWithin(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// purgeRecorded carries out a recorded-only purge plan (#445). The records
// are read again: a path is removed only when the plan named it and no
// other profile records it NOW - one that another profile started
// recording since the plan is kept and reported - and a path's record goes
// only once its file has. A mod whose recorded paths all went is marked not
// deployed; one with a path kept is reported as skipped, and stays as it
// is. Removing a file puts back whatever it had replaced.
func (s *Service) purgeRecorded(ctx context.Context, game *domain.Game, plan *PurgePlan, sink EventSink) (*PurgeResult, error) {
	result := &PurgeResult{}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	live, err := s.liveProfile(ctx, game.ID)
	if err != nil {
		return result, err
	}
	remove, kept, err := s.recordedPaths(ctx, game, plan.Profile, live)
	if err != nil {
		return result, err
	}
	result.Kept = kept
	installed, err := s.GetInstalledMods(ctx, game.ID, plan.Profile)
	if err != nil {
		return result, fmt.Errorf("getting installed mods: %w", err)
	}
	rowsByKey := make(map[string]*domain.InstalledMod, len(installed))
	for i := range installed {
		rowsByKey[domain.ModKey(installed[i].SourceID, installed[i].ID)] = &installed[i]
	}
	profileMethod, err := s.GetEffectiveLinkMethod(ctx, game, plan.Profile)
	if err != nil {
		return result, err
	}

	approved := make(map[string]bool, len(plan.Remove))
	for _, path := range plan.Remove {
		approved[path] = true
	}
	left := make(map[string]int) // mod key -> recorded paths not removed
	for _, k := range kept {
		if owner, err := s.db.GetFileOwner(ctx, game.ID, plan.Profile, k.Path); err == nil && owner != nil {
			left[domain.ModKey(owner.SourceID, owner.ModID)]++
		}
	}
	// A path this purge meant to remove and did not is a warning, not a
	// --verbose note: the plan the user confirmed said it would go.
	warn := func(path string, err error, what string) {
		msg := fmt.Sprintf("%s was left in place, with its record: %s: %v", path, what, err)
		result.Warnings = append(result.Warnings, msg)
		emit(WarningEvent{Scope: Scope{Op: OpPurge}, Phase: PurgeWarning, Message: msg})
	}

	emit(StepEvent{Scope: Scope{Op: OpPurge, Total: len(plan.Mods)}, Phase: DeployPurging})
	installers := make(map[domain.LinkMethod]*Installer)
	var removed []string
	for _, row := range remove {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		key := domain.ModKey(row.SourceID, row.ModID)
		if !approved[row.RelativePath] {
			left[key]++
			continue
		}
		method := profileMethod
		if mod, ok := rowsByKey[key]; ok {
			method = mod.LinkMethod
		}
		installer, ok := installers[method]
		if !ok {
			installer = s.newInstallerWithLinker(game, s.getLinker(method))
			installers[method] = installer
		}
		dst := filepath.Join(game.ModPath, filepath.FromSlash(row.RelativePath))
		_, err := os.Lstat(dst)
		switch {
		case err == nil:
			if err := installer.linker.Undeploy(dst); err != nil {
				left[key]++
				warn(row.RelativePath, err, "it could not be removed")
				continue
			}
			installer.restoreReplacedOriginal(row.RelativePath, dst)
		case !errors.Is(err, fs.ErrNotExist):
			// Only "not there" lets the record go without the file
			// (review F4): anything else means it was not checked.
			left[key]++
			warn(row.RelativePath, err, "it could not be checked")
			continue
		}
		if err := s.db.DeleteDeployedFile(ctx, game.ID, plan.Profile, row.RelativePath); err != nil {
			left[key]++
			msg := fmt.Sprintf("⚠ %s - %v", row.RelativePath, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: Scope{Op: OpPurge}, Phase: PurgeNote, Detail: msg})
			continue
		}
		removed = append(removed, row.RelativePath)
	}
	result.RemovedPaths = len(removed)

	total := len(plan.Mods)
	for idx := range plan.Mods {
		mod := plan.Mods[idx]
		scope := Scope{Op: OpPurge, Index: idx + 1, Total: total, ModName: mod.Name, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}}
		if n := left[domain.ModKey(mod.SourceID, mod.ID)]; n > 0 {
			detail := fmt.Sprintf("%d of its file(s) left in place: something else still claims them, or they could not be removed", n)
			result.Skipped = append(result.Skipped, skippedRef(&mod, detail))
			emit(ModEvent{Scope: scope, Phase: PurgeModSkipped, Detail: detail})
			continue
		}
		if err := s.setModDeployed(ctx, mod.SourceID, mod.ID, game.ID, plan.Profile, false); err != nil {
			msg := fmt.Sprintf("⚠ %s - failed to mark as not deployed: %v", mod.Name, err)
			result.Notes = append(result.Notes, msg)
			emit(StepEvent{Scope: scope, Phase: PurgeNote, Detail: msg})
		}
		result.Purged++
		emit(ModEvent{Scope: scope, Phase: PurgeModPurged})
	}

	linker.CleanupEmptyDirs(game.ModPath, removed)
	emit(StepEvent{Scope: Scope{Op: OpPurge}, Phase: PurgeComplete})
	s.takeCaptureWarnings(game.ID, OpPurge, PurgeWarning, &result.Warnings, emit)
	return result, nil
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
// one InstalledRef per mod that was NOT fully purged (a before_each-skipped
// mod, or an --uninstall record-delete failure), naming the mod and
// carrying the reason as data rather than as a pre-formatted
// "<name>: <reason>" line (spec §4); len(Skipped) is doPurge's historical
// `failed` counter, so the CLI's "Purged: N, Failed: M" summary comes from
// Purged and len(Skipped).
//
// A recorded-only purge (#445, PurgePlan.RecordedOnly) also reports how
// many recorded paths it removed and which it kept, and why; a path it
// meant to remove and could not remove, or check, is a Warnings entry.
type PurgeResult struct {
	Purged   int            `json:"purged"`
	Skipped  []InstalledRef `json:"skipped,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
	Notes    []string       `json:"notes,omitempty"`

	RemovedPaths int             `json:"removed_paths,omitzero"`
	Kept         []PurgeKeptPath `json:"kept,omitempty"`
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
//
// It is PlanPurge + ApplyPurge in one call, under a single mutation slot,
// for callers with no prompt to show between the two (core's own tests) -
// which is why it takes the mod set directly. Frontends plan first, prompt
// against the plan, then apply it.
//
// Convenience = PlanPurge + ApplyPurge; kept exported for core tests and
// for frontends that want one call, even though it has no non-test caller
// today (Task 25 review Important #1; ruling recorded in the 2026-08-29
// decisions-log row of docs/plans/2026-08-27-v2-core-refactor-design.md).
// No freshness check: with no plan, there is nothing for ApplyPurge's
// checkPlanFresh to compare against, so a profile mutated between the
// caller's mod fetch and this call is not caught the way the Plan+Apply
// pair catches it (Task 25 review Minor #5).
func (s *Service) PurgeProfile(ctx context.Context, game *domain.Game, profileName string, mods []domain.InstalledMod, opts PurgeOptions, sink EventSink) (*PurgeResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &PurgeResult{}, err
	}
	defer release()
	// A profile that is not live gets the recorded-only purge PlanPurge
	// would plan for it (#445), whatever mods says.
	live, err := s.liveProfile(ctx, game.ID)
	if err != nil {
		return &PurgeResult{}, err
	}
	if live != profileName {
		plan, _, err := s.planRecordedPurge(ctx, game, profileName, live, opts)
		if err != nil {
			return &PurgeResult{}, err
		}
		return s.purgeRecorded(ctx, game, plan, sink)
	}
	return s.purgeProfile(ctx, game, profileName, mods, opts, sink)
}

func (s *Service) purgeProfile(ctx context.Context, game *domain.Game, profileName string, mods []domain.InstalledMod, opts PurgeOptions, sink EventSink) (*PurgeResult, error) {
	result := &PurgeResult{}

	// #269: a caller may hand this the full installed set (PurgeProfile's
	// own signature invites it), so the exclusion is enforced HERE as well
	// as in PlanPurge - a purge must never remove a Steam Workshop item's
	// tracking or reach for files Steam owns, whichever entry point was used.
	mods, externalMods := partitionExternal(mods)
	if sink != nil {
		for _, name := range externalMods {
			sink(StepEvent{Scope: Scope{Op: OpPurge, ModName: name}, Phase: PurgeExternalSkipped,
				Detail: "tracked from Steam - purge leaves it alone"})
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

	// #350 re-review finding N2: a purge is now a REMOVAL path that puts
	// originals back (ruling (a)), so it is a path a put-back can fail on -
	// and WarnWriter alone is the CLI's stderr but the SERVER's stderr under
	// `lmm serve`, where a browser user would never see it and the pending
	// entry would be drained onto whatever deploy/install/update ran next.
	// Drained here, exactly as deployProfile/ApplyInstall/applyUpdate do it.
	s.takeCaptureWarnings(game.ID, OpPurge, PurgeWarning, &result.Warnings, func(e Event) {
		if sink != nil {
			sink(e)
		}
	})

	return result, nil
}
