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
	// kept receives the paths the loop left for another game (purge
	// command only; nil in deploy mode, which reports them as warnings).
	kept *[]PurgeKeptPath
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

		modRemoved, held, err := installer.uninstall(ctx, game, &mod.Mod, profileName)
		removed = append(removed, modRemoved...)
		// #445 gate 2, G2-1: the purge command reports what it left for
		// another game as a recorded-only purge does; deploy's pass puts it
		// on the deploy's Warnings.
		for _, h := range held {
			switch {
			case spec.kept == nil:
				installer.noteHeld(h.removalNote())
			case h.userReason != "":
				*spec.kept = append(*spec.kept, PurgeKeptPath{Path: h.path, Reason: PurgeKeptUserFile, Note: h.userReason})
			default:
				*spec.kept = append(*spec.kept, PurgeKeptPath{Path: h.path, Reason: PurgeKeptOtherGame, Games: h.games})
			}
		}
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
	// nothing else still claims, and the ones whose file is already gone -
	// and nothing else. A path it may not remove (PurgeKeptReason) is left
	// and listed in Kept; Profile's record of it goes unless it is kept as
	// PurgeKeptListed (PurgeKeptReason.DropsRecord), so every purge the
	// mod_path refusal names can clear the records it counts (#427), or the
	// refusal names what records the file under the active profile. It
	// runs no hooks, removes no mod records or profile entries (--uninstall
	// is refused with ErrProfileNotActive), and leaves a merged artifact to
	// the paths Profile recorded. Mods is then the installed mods with a
	// path in Remove or a record that goes.
	RecordedOnly bool `json:"recorded_only,omitzero"`
	// ActiveProfile names the game's active profile when RecordedOnly is
	// set.
	ActiveProfile string `json:"active_profile,omitempty"`
	// Remove is the paths a RecordedOnly purge removes, relative to the
	// game's mod directory, in path order.
	Remove []string `json:"remove,omitempty"`
	// Kept is the paths a RecordedOnly purge leaves, and why - and, for a
	// purge of the active profile, the files under a mod_path the game no
	// longer uses (#451) that it leaves (each with ModPath).
	Kept []PurgeKeptPath `json:"kept,omitempty"`
	// Stranded is the files Profile deployed under a mod_path the game no
	// longer uses (#451) that this purge removes from where they were
	// deployed, in path order - so a frontend can say which files go, even
	// when no installed mod is left to name them (#466 review F2). A
	// RecordedOnly purge lists them in Remove as well.
	Stranded []PurgeStrandedPath `json:"stranded,omitempty"`

	// snapshot is Ruling 5's precondition: the installed-mod set this plan
	// was computed from, re-derived and compared by ApplyPurge.
	snapshot installedSnapshot `json:"-"`
}

// PurgeKeptPath is a path a recorded-only purge (#445) leaves in place, and
// the first reason it found to (PurgeKeptReason). The purged profile's
// record of it goes too, unless the reason keeps it (Reason.DropsRecord).
type PurgeKeptPath struct {
	Path   string          `json:"path"`
	Reason PurgeKeptReason `json:"reason"`
	// Profiles names the other profiles recording the path
	// (PurgeKeptRecorded), or the active profile listing its mod
	// (PurgeKeptListed).
	Profiles []string `json:"profiles,omitempty"`
	// Games names the other games recording the path (PurgeKeptOtherGame).
	Games []string `json:"games,omitempty"`
	// Note says why a PurgeKeptUserFile path is the user's when the
	// adapter is not the reason (#466): a copy or hardlink whose content
	// is no longer what lmm deployed, as a clause.
	Note string `json:"note,omitempty"`
	// ModPath is the mod_path Path is under, when that is not the game's
	// current one (#451): the purge was clearing files deployed before the
	// mod_path changed.
	ModPath string `json:"mod_path,omitempty"`
}

// PurgeStrandedPath is a file a purge removes from a mod_path the game no
// longer uses (#451): Path relative to ModPath, the mod_path it was
// deployed under.
type PurgeStrandedPath struct {
	Path    string `json:"path"`
	ModPath string `json:"mod_path"`
}

// HasWork reports whether applying p would change anything: a mod to
// purge, a file to remove, or a record to drop.
func (p *PurgePlan) HasWork() bool {
	if len(p.Mods) > 0 || len(p.Remove) > 0 || len(p.Stranded) > 0 {
		return true
	}
	for _, k := range p.Kept {
		if k.Reason.DropsRecord() {
			return true
		}
	}
	return false
}

// PurgeKeptReason is why a recorded-only purge left a path the purged
// profile recorded. A path is removed only on proof that it is that
// profile's and nothing else anyone wants live (#445 review), so each of
// these keeps its file.
//
// A path is decided in this order (#445 audit, final gate F-A), and the
// first answer holds:
//
//  1. its file is already gone: nothing is kept, and the record goes (the
//     path is in PurgePlan.Remove);
//  2. PurgeKeptUserFile - the adapter's route, or (#466) a copy or
//     hardlink whose content is no longer what lmm deployed;
//  3. PurgeKeptListed - unless another record of the path keeps the file
//     for the same reason (listedHandedOn), or another game's record keeps
//     it for that game's active profile (otherGameRecords.heldForActive);
//  4. PurgeKeptRecorded, then PurgeKeptOtherGame;
//  5. otherwise the path is the purged profile's alone: it is removed.
//
// Only a listed path keeps the purged profile's record (DropsRecord).
// Every other kept file is still tracked by whoever else claims it, or is
// never lmm's to remove, so the record would only keep the game's mod_path
// locked (refuseModPathMove) with no purge able to clear it: two profiles
// recording one file used to keep it for each other forever. A listed path
// is different: the other claimant's purge asks whether the active profile
// lists ITS mod, in ITS game, so dropping this record could hand the file to
// a purge that removes it - a v1.30.1 import minted a key per profile, two
// mods can ship one file, and two games can share a directory.
type PurgeKeptReason string

const (
	// PurgeKeptUserFile: the game's adapter hands the file to the user
	// after its first deploy (adapter.RouteCopyOnce - BepInEx's config
	// files) or never deploys it, so it is never lmm's to remove - the rule
	// every other removal already follows (#413, review F1). Its record
	// goes, as an ordinary purge's does: v2 never records such a file, so
	// the record is a legacy one (a v1.30.1 deploy's). It is tested before
	// the claims below because it holds whoever else claims the file - and
	// no apply or deploy would ever record it for the active profile.
	//
	// It is also the reason for a copy or hardlink whose content no longer
	// matches the fingerprint its deploy recorded (#466), with Note saying
	// so: the user replaced lmm's file, and a purge keeps theirs.
	PurgeKeptUserFile PurgeKeptReason = "user_file"
	// PurgeKeptRecorded: another profile of the game records the path too.
	// That profile still tracks the file, and its own purge decides it.
	PurgeKeptRecorded PurgeKeptReason = "recorded"
	// PurgeKeptOtherGame: another game whose mod directory holds the path
	// records it (review F7), and still tracks it.
	PurgeKeptOtherGame PurgeKeptReason = "other_game"
	// PurgeKeptListed: the active profile's document lists the path's mod,
	// not marked off, and no other record keeps the file for that reason -
	// none whose mod it lists, under it or another profile, and none of
	// another game's that keeps it for that game's active profile - so the
	// file may be live for the active profile, and the purged profile's
	// record is its only claim to be lmm's. What a v1.30.1 switch between
	// profiles sharing a mod left (review F3). Asked on every purge, so the
	// last claimant's purge never removes a file the active profile lists.
	// Another profile or game may still record the path (Profiles names
	// only the active profile then): its purge would not keep the file.
	PurgeKeptListed PurgeKeptReason = "listed"
)

// DropsRecord reports whether a recorded-only purge drops the purged
// profile's record of a path it keeps for this reason: every reason but
// PurgeKeptListed (see PurgeKeptReason).
func (r PurgeKeptReason) DropsRecord() bool { return r != PurgeKeptListed }

// PlanPurge computes what PurgeProfile would do for game/profileName under
// opts, without touching anything - including the installed-mods read the
// pre-lift cmd/lmm/purge.go did itself before prompting (its "getting
// installed mods: …" wording is preserved on that read's failure).
//
// A profile that is not the game's active one gets a recorded-only plan
// (#445, PurgePlan.RecordedOnly): its files are not the ones the game
// directory is meant to hold, so only what it recorded putting there, and
// nothing else still claims (PurgeKeptReason), is removed.
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
	// #451: what the profile deployed under a mod_path the game no longer
	// uses goes too, and the plan says which files.
	remove, kept, err := s.recordedPaths(ctx, game, profileName, live, nil, true)
	if err != nil {
		return nil, err
	}
	plan.Stranded = strandedOf(game, remove)
	for _, k := range kept {
		plan.Kept = append(plan.Kept, k.PurgeKeptPath)
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
	return s.purgeProfile(ctx, game, plan.Profile, plan.Mods, plan, opts, sink)
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
	others, err := s.otherGamesRecording(ctx, game)
	if err != nil {
		return nil, nil, err
	}
	remove, kept, err := s.recordedPaths(ctx, game, profileName, live, others, false)
	if err != nil {
		return nil, nil, err
	}
	withPaths := make(map[string]bool, len(remove))
	plan := &PurgePlan{
		Profile:       profileName,
		Mods:          []domain.InstalledMod{},
		RecordedOnly:  true,
		ActiveProfile: live,
	}
	for _, row := range remove {
		plan.Remove = append(plan.Remove, row.RelativePath)
		withPaths[domain.ModKey(row.SourceID, row.ModID)] = true
	}
	plan.Stranded = strandedOf(game, remove)
	for _, k := range kept {
		plan.Kept = append(plan.Kept, k.PurgeKeptPath)
		if k.Reason.DropsRecord() {
			withPaths[domain.ModKey(k.row.SourceID, k.row.ModID)] = true
		}
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

// strandedOf is the rows among remove recorded under a mod_path other
// than their game's current one, as PurgeStrandedPath.
func strandedOf(game *domain.Game, remove []db.DeployedPath) []PurgeStrandedPath {
	var out []PurgeStrandedPath
	for _, row := range remove {
		if rowGame := gameUnderRoot(game, row.ModPath); rowGame != game {
			out = append(out, PurgeStrandedPath{Path: row.RelativePath, ModPath: rowGame.ModPath})
		}
	}
	return out
}

// keptRecord is a record recordedPaths keeps the path of, and why.
type keptRecord struct {
	PurgeKeptPath
	row db.DeployedPath
}

// recordedPaths splits profileName's deployed-file records into the ones a
// recorded-only purge may remove - a path whose file is already gone among
// them - and the ones whose file it keeps, each in path order, deciding
// each in PurgeKeptReason's order. live is the game's active profile, and
// others what the games overlapping its mod directory record
// (otherGamesRecording).
//
// Each record is judged under the mod_path it was deployed under (#451).
// One recorded under a mod_path the game no longer uses is not the active
// profile's live file wherever the active profile lists its mod, so
// PurgeKeptListed never keeps it; strandedOnly limits the split to those
// records, which is what a purge of the active profile clears this way.
//
// Anything that cannot be read to decide that is an error: the purge fails
// closed. A path that cannot be checked is not taken for gone.
func (s *Service) recordedPaths(ctx context.Context, game *domain.Game, profileName, live string, others *otherGameRecords, strandedOnly bool) (remove []db.DeployedPath, kept []keptRecord, err error) {
	rows, err := s.db.ListDeployedFiles(ctx, game.ID, profileName)
	if err != nil {
		return nil, nil, fmt.Errorf("listing deployed files: %w", err)
	}
	records, err := s.db.DeployedPathRecords(ctx, game.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing deployed files: %w", err)
	}
	listed, err := s.liveListedMods(game.ID, live)
	if err != nil {
		return nil, nil, err
	}
	// The adapter's routing does not depend on the link method.
	installer := s.getInstaller(game)
	cached, err := s.installedCacheLookup(ctx, game, profileName)
	if err != nil {
		return nil, nil, err
	}
	othersUnder := map[string]*otherGameRecords{}
	for _, row := range rows {
		rowGame := gameUnderRoot(game, row.ModPath)
		stranded := rowGame != game
		if strandedOnly && !stranded {
			continue
		}
		rowOthers, rowListed := others, listed
		if stranded {
			rowListed = nil
			if rowOthers = othersUnder[rowGame.ModPath]; rowOthers == nil {
				if rowOthers, err = s.otherGamesRecording(ctx, rowGame); err != nil {
					return nil, nil, err
				}
				othersUnder[rowGame.ModPath] = rowOthers
			}
		}
		dst := filepath.Join(rowGame.ModPath, filepath.FromSlash(row.RelativePath))
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			remove = append(remove, row)
			continue
		}
		pathRecords := recordsUnder(game, rowGame, records[row.RelativePath])
		jd := deployedJudge{db: s.db, game: rowGame, profile: profileName, cacheRoots: s.cacheRoots(game), others: rowOthers, cached: cached}
		if k, ok := keptPath(ctx, row, profileName, live, pathRecords, rowListed, rowOthers, installer, jd); ok {
			if stranded {
				k.ModPath = rowGame.ModPath
			}
			kept = append(kept, keptRecord{PurgeKeptPath: k, row: row})
			continue
		}
		remove = append(remove, row)
	}
	return remove, kept, nil
}

// installedCacheLookup is a deployedJudge.cached for profileName's
// installed mods in game: each record's mod's cached copy at the version
// the profile has installed (#466 review D10).
func (s *Service) installedCacheLookup(ctx context.Context, game *domain.Game, profileName string) (func(db.DeployedFileState, string) string, error) {
	installed, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}
	byKey := make(map[string]*cachedMod, len(installed))
	gameCache := s.GetGameCache(game)
	for i := range installed {
		byKey[domain.ModKey(installed[i].SourceID, installed[i].ID)] = &cachedMod{cache: gameCache, game: game, mod: &installed[i].Mod, method: installed[i].LinkMethod}
	}
	return func(st db.DeployedFileState, rel string) string {
		return byKey[domain.ModKey(st.SourceID, st.ModID)].fileFor(st, rel)
	}, nil
}

// gameUnderRoot is game as it was when a record under root was deployed
// (#451): game itself when root is its current mod_path or unrecorded,
// otherwise a copy whose ModPath is root.
func gameUnderRoot(game *domain.Game, root string) *domain.Game {
	if underCurrentRoot(game, root) {
		return game
	}
	moved := *game
	moved.ModPath = root
	return &moved
}

// recordsUnder keeps the records, of a path in game, that were deployed
// under rowGame's mod_path: a record of the same relative path under
// another mod_path names another file.
func recordsUnder(game, rowGame *domain.Game, records []db.PathRecord) []db.PathRecord {
	var out []db.PathRecord
	for _, r := range records {
		if gameUnderRoot(game, r.ModPath).ModPath == rowGame.ModPath {
			out = append(out, r)
		}
	}
	return out
}

// keptPath is recordedPaths' decision for one row whose file is there (or
// could not be checked), in PurgeKeptReason's order. records are every
// record of the row's path in its game, the purged profile's included.
func keptPath(ctx context.Context, row db.DeployedPath, profileName, live string, records []db.PathRecord, listed map[string]string, others *otherGameRecords, installer *Installer, jd deployedJudge) (PurgeKeptPath, bool) {
	game := jd.game
	k := PurgeKeptPath{Path: row.RelativePath}
	if installer.notLinkerOwned(game, row.RelativePath) {
		k.Reason = PurgeKeptUserFile
		return k, true
	}
	// #466: a copy or hardlink the user has replaced is theirs, whoever
	// else claims the path. One that cannot be judged is decided at the
	// removal, which keeps it with its record (removeRecordedPaths), and so
	// is one only its difference from the mod's cached copy says is the
	// user's: the claims below still decide whether its record stays, so a
	// purge order cannot change what is left recorded (#445's handoffs).
	dst := filepath.Join(game.ModPath, filepath.FromSlash(row.RelativePath))
	if j := jd.judge(ctx, row.RelativePath, dst); j.verdict == deployedUsers && !j.unchecked && !j.cacheProof {
		k.Reason, k.Note = PurgeKeptUserFile, j.reason
		return k, true
	}
	var profiles []string
	for _, r := range records {
		if r.Profile != profileName {
			profiles = append(profiles, r.Profile)
		}
	}
	if profile, ok := listed[domain.ModKey(row.SourceID, row.ModID)]; ok && !listedHandedOn(records, profileName, listed) && len(others.heldForActive(row.RelativePath)) == 0 {
		k.Reason, k.Profiles = PurgeKeptListed, []string{profile}
		return k, true
	}
	if len(profiles) > 0 {
		k.Reason, k.Profiles = PurgeKeptRecorded, profiles
		return k, true
	}
	if games := others.recording(row.RelativePath); len(games) > 0 {
		k.Reason, k.Games = PurgeKeptOtherGame, games
		return k, true
	}
	return PurgeKeptPath{}, false
}

// listedHandedOn reports whether a path the active profile lists stays
// protected once profileName's record of it goes (#445 final gate F-A):
// another record of it in the same game names a mod the active profile
// lists - under the active profile, whose purge or deploy decides it, or
// under another, whose purge keeps the file as PurgeKeptListed in turn. A
// record under the active profile of a mod its document does not list
// protects nothing (#445 gate 2, V9): the document was edited and not yet
// applied, and the apply takes that mod's file down. Another game's record
// qualifies only as otherGameRecords.heldForActive says: its purge asks
// about that game's active profile.
func listedHandedOn(records []db.PathRecord, profileName string, listed map[string]string) bool {
	for _, r := range records {
		if r.Profile == profileName {
			continue
		}
		if _, ok := listed[domain.ModKey(r.SourceID, r.ModID)]; ok {
			return true
		}
	}
	return false
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
	games []*otherGame
	// claimsOf reads a game's active profile and the mods its document
	// lists, for heldForActive (Service.gameClaims).
	claimsOf func(gameID string) gameClaims
	// statesOf reads a game's records of one of its paths, for
	// fingerprints (db.DeployedFileStates).
	statesOf func(ctx context.Context, gameID, rel string) ([]db.DeployedFileState, error)
}

type otherGame struct {
	id      string
	modPath string                     // its mod directory, as configured
	root    string                     // its mod directory, links resolved
	paths   map[string][]db.PathRecord // its recorded paths (DeployedPathRecords)
	// claims is claimsOf's answer for the game, read on first use.
	claims *gameClaims
}

// gameClaims is a game's active profile and the mods its document lists
// (liveListedMods), or err when its profile files do not say.
type gameClaims struct {
	live   string
	listed map[string]string
	err    error
}

// gameClaims reads gameID's gameClaims.
func (s *Service) gameClaims(ctx context.Context, gameID string) gameClaims {
	live, err := s.liveProfile(ctx, gameID)
	if err != nil {
		return gameClaims{err: err}
	}
	listed, err := s.liveListedMods(gameID, live)
	if err != nil {
		return gameClaims{err: err}
	}
	return gameClaims{live: live, listed: listed}
}

// otherGamesRecording reads the records of every other configured game
// whose mod directory overlaps game's (review F7).
func (s *Service) otherGamesRecording(ctx context.Context, game *domain.Game) (*otherGameRecords, error) {
	records := &otherGameRecords{
		root:     resolvedDir(game.ModPath),
		claimsOf: func(id string) gameClaims { return s.gameClaims(ctx, id) },
		statesOf: s.db.DeployedFileStates,
	}
	for _, other := range s.ListGames() {
		if other.ID == game.ID || other.ModPath == "" {
			continue
		}
		root := resolvedDir(other.ModPath)
		if !pathWithin(records.root, root) && !pathWithin(root, records.root) {
			continue
		}
		paths, err := s.db.DeployedPathRecords(ctx, other.ID)
		if err != nil {
			return nil, fmt.Errorf("listing %s's deployed files: %w", other.ID, err)
		}
		records.games = append(records.games, &otherGame{id: other.ID, modPath: other.ModPath, root: root, paths: paths})
	}
	return records, nil
}

// recording names the other games that record rel, a path under the purged
// game's mod directory.
func (r *otherGameRecords) recording(rel string) []string {
	var games []string
	for _, h := range r.holding(rel) {
		games = append(games, h.game.id)
	}
	return games
}

// otherGameHold is one other game's records of a path.
type otherGameHold struct {
	game    *otherGame
	records []db.PathRecord
}

// holding is each other game that records rel, a path under the purged
// game's mod directory, with its records of it.
func (r *otherGameRecords) holding(rel string) []otherGameHold {
	if r == nil {
		return nil
	}
	abs := filepath.Join(r.root, filepath.FromSlash(rel))
	var holds []otherGameHold
	for _, g := range r.games {
		if theirs, ok := relWithin(g.root, abs); ok && len(g.paths[filepath.ToSlash(theirs)]) > 0 {
			holds = append(holds, otherGameHold{game: g, records: g.paths[filepath.ToSlash(theirs)]})
		}
	}
	return holds
}

// fingerprints is every fingerprinted record the other games hold of rel,
// a path under the purged game's mod directory, deployed under their
// current mod_path (#466 review D5): what they deployed there.
func (r *otherGameRecords) fingerprints(ctx context.Context, rel string) ([]db.DeployedFileState, error) {
	var out []db.DeployedFileState
	for _, h := range r.holding(rel) {
		theirs, _ := relWithin(h.game.root, filepath.Join(r.root, filepath.FromSlash(rel)))
		states, err := r.statesOf(ctx, h.game.id, filepath.ToSlash(theirs))
		if err != nil {
			return nil, fmt.Errorf("reading %s's record of %s: %w", h.game.id, rel, err)
		}
		for _, st := range states {
			if st.Fingerprint != nil && (st.ModPath == "" || samePath(st.ModPath, h.game.modPath)) {
				out = append(out, st)
			}
		}
	}
	return out, nil
}

// claims is g's gameClaims, read once.
func (r *otherGameRecords) claims(g *otherGame) gameClaims {
	if g.claims == nil {
		c := r.claimsOf(g.id)
		g.claims = &c
	}
	return *g.claims
}

// heldForActive names the other games whose records of rel keep the file
// for their own active profile (#445 gate 2, G2-1): one is that profile's
// own, or names a mod it lists. Such a game's own purges and deploys decide
// the file, as the active profile's own record does in its game, and lmm
// never removes or replaces a file another game records - so a profile of
// this game that lists the path's mod cannot take the file over, and its
// file is that game's. A game whose active profile lmm cannot tell is not
// among them: nothing is handed to it.
func (r *otherGameRecords) heldForActive(rel string) []string {
	var games []string
	for _, h := range r.holding(rel) {
		c := r.claims(h.game)
		if c.err != nil {
			continue
		}
		for _, rec := range h.records {
			if _, listed := c.listed[domain.ModKey(rec.SourceID, rec.ModID)]; listed || rec.Profile == c.live {
				games = append(games, h.game.id)
				break
			}
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
	return resolvedPath(dir)
}

// relWithin returns path relative to root, when path is root or below it
// (pathWithin).
func relWithin(root, path string) (string, bool) {
	if !pathWithin(path, root) {
		return "", false
	}
	rel, err := filepath.Rel(root, path)
	return rel, err == nil
}

// purgeRecorded carries out a recorded-only purge plan (#445). The records
// are read again: a path is removed only when the plan named it and no
// other profile records it NOW - one that another profile started
// recording since the plan is kept and reported - and a path's record goes
// only once its file has, or, for a kept file whose record goes
// (PurgeKeptReason.DropsRecord), when the plan listed it so. A mod whose
// recorded paths all went is marked not deployed; one with a record left
// is reported as skipped, and stays as it is. Removing a file puts back
// whatever it had replaced.
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
	others, err := s.otherGamesRecording(ctx, game)
	if err != nil {
		return result, err
	}
	remove, kept, err := s.recordedPaths(ctx, game, plan.Profile, live, others, false)
	if err != nil {
		return result, err
	}
	approved := make(map[string]bool, len(plan.Remove))
	for _, path := range plan.Remove {
		approved[path] = true
	}
	untrack := make(map[string]bool)
	for _, k := range plan.Kept {
		if k.Reason.DropsRecord() {
			untrack[k.Path] = true
		}
	}
	emit(StepEvent{Scope: Scope{Op: OpPurge, Total: len(plan.Mods)}, Phase: DeployPurging})
	left, err := s.removeRecordedPaths(ctx, game, plan.Profile, remove, kept, approved, untrack, result, emit)
	if err != nil {
		return result, err
	}

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

	emit(StepEvent{Scope: Scope{Op: OpPurge}, Phase: PurgeComplete})
	s.takeCaptureWarnings(game.ID, OpPurge, PurgeWarning, &result.Warnings, emit)
	return result, nil
}

// purgeStranded clears the active profile's records under a mod_path the
// game no longer uses (#451) before a purge of it removes the rest: each
// file there is removed on the recorded-only purge's terms, since nothing
// the active profile deploys lives there any more. With a plan, only the
// files it named are removed and only the records it said go are dropped
// (#466 review F2); a file stranded since stays, with its record, for the
// next purge. What it removed and kept goes on result.
func (s *Service) purgeStranded(ctx context.Context, game *domain.Game, profileName string, plan *PurgePlan, result *PurgeResult, emit func(Event)) error {
	live, err := s.liveProfile(ctx, game.ID)
	if err != nil {
		return err
	}
	remove, kept, err := s.recordedPaths(ctx, game, profileName, live, nil, true)
	if err != nil || len(remove)+len(kept) == 0 {
		return err
	}
	approved := make(map[string]bool, len(remove))
	untrack := make(map[string]bool)
	if plan != nil {
		for _, st := range plan.Stranded {
			approved[st.Path] = true
		}
		for _, k := range plan.Kept {
			if k.ModPath != "" && k.Reason.DropsRecord() {
				untrack[k.Path] = true
			}
		}
	} else {
		for _, row := range remove {
			approved[row.RelativePath] = true
		}
		for _, k := range kept {
			if k.Reason.DropsRecord() {
				untrack[k.Path] = true
			}
		}
	}
	_, err = s.removeRecordedPaths(ctx, game, profileName, remove, kept, approved, untrack, result, emit)
	return err
}

// removeRecordedPaths removes the recorded paths recordedPaths split out
// for profileName - those approved, each under the mod_path its record
// names - drops the records of the kept paths in untrack, and puts the kept
// paths, the count removed and any warning on result. It returns, per mod
// key, how many of that mod's records it did not clear.
func (s *Service) removeRecordedPaths(ctx context.Context, game *domain.Game, profileName string, remove []db.DeployedPath, kept []keptRecord, approved, untrack map[string]bool, result *PurgeResult, emit func(Event)) (map[string]int, error) {
	for _, k := range kept {
		result.Kept = append(result.Kept, k.PurgeKeptPath)
	}
	installed, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}
	rowsByKey := make(map[string]*domain.InstalledMod, len(installed))
	for i := range installed {
		rowsByKey[domain.ModKey(installed[i].SourceID, installed[i].ID)] = &installed[i]
	}
	profileMethod, err := s.GetEffectiveLinkMethod(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	cached, err := s.installedCacheLookup(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	othersUnder := map[string]*otherGameRecords{}

	emitNote := func(msg string) {
		result.Notes = append(result.Notes, msg)
		emit(StepEvent{Scope: Scope{Op: OpPurge}, Phase: PurgeNote, Detail: msg})
	}
	left := make(map[string]int) // mod key -> recorded paths not cleared
	for _, k := range kept {
		key := domain.ModKey(k.row.SourceID, k.row.ModID)
		// The file stays either way; only a record the plan said goes, goes -
		// so a path another profile started recording since the plan, which
		// the plan meant to remove, keeps its record too.
		if !k.Reason.DropsRecord() || !untrack[k.Path] {
			left[key]++
			continue
		}
		if err := s.db.DeleteDeployedFile(ctx, game.ID, profileName, k.Path); err != nil {
			left[key]++
			emitNote(fmt.Sprintf("⚠ %s - %v", k.Path, err))
			continue
		}
		if k.Reason == PurgeKeptUserFile && k.Note != "" && k.ModPath == "" {
			if store := s.originalsStoreFor(game.ID); store != nil {
				store.markKeptUser(k.Path)
			}
		}
	}
	// A path this purge meant to remove and did not is a warning, not a
	// --verbose note: the plan the user confirmed said it would go.
	warn := func(path string, err error, what string) {
		msg := fmt.Sprintf("%s was left in place, with its record: %s: %v", path, what, err)
		result.Warnings = append(result.Warnings, msg)
		emit(WarningEvent{Scope: Scope{Op: OpPurge}, Phase: PurgeWarning, Message: msg})
	}

	installers := make(map[domain.LinkMethod]*Installer)
	removed := make(map[string][]string) // mod_path -> paths removed under it
	var roots []string
	for _, row := range remove {
		if err := ctx.Err(); err != nil {
			return left, err
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
		rowGame := gameUnderRoot(game, row.ModPath)
		dst := filepath.Join(rowGame.ModPath, filepath.FromSlash(row.RelativePath))
		_, err := os.Lstat(dst)
		switch {
		case err == nil:
			// #466: judged again at the moment of removal, which is what
			// counts. A file found to be the user's is kept and no longer
			// tracked; one that could not be judged keeps its record too.
			others := othersUnder[rowGame.ModPath]
			if others == nil {
				if others, err = s.otherGamesRecording(ctx, rowGame); err != nil {
					return left, err
				}
				othersUnder[rowGame.ModPath] = others
			}
			j := deployedJudge{db: s.db, game: rowGame, profile: profileName, cacheRoots: s.cacheRoots(game), others: others, cached: cached}.judge(ctx, row.RelativePath, dst)
			switch {
			case j.verdict == deployedUsers && j.unchecked:
				left[key]++
				warn(row.RelativePath, errors.New(j.reason), "it could not be compared with what lmm deployed")
				continue
			case j.verdict == deployedUsers:
				k := PurgeKeptPath{Path: row.RelativePath, Reason: PurgeKeptUserFile, Note: j.reason}
				if rowGame != game {
					k.ModPath = rowGame.ModPath
				}
				result.Kept = append(result.Kept, k)
				if err := s.db.DeleteDeployedFile(ctx, game.ID, profileName, row.RelativePath); err != nil {
					left[key]++
					emitNote(fmt.Sprintf("⚠ %s - %v", row.RelativePath, err))
					continue
				}
				if rowGame == game {
					installer.markKeptUser(row.RelativePath)
				}
				continue
			}
			if err := installer.linker.Undeploy(dst); err != nil {
				left[key]++
				warn(row.RelativePath, err, "it could not be removed")
				continue
			}
			if j.verdict == deployedUnverified {
				installer.noteUnverified(row.RelativePath, j.legacy)
			}
			installer.restoreReplacedOriginal(row.RelativePath, dst)
		case !errors.Is(err, fs.ErrNotExist):
			// Only "not there" lets the record go without the file
			// (review F4): anything else means it was not checked.
			left[key]++
			warn(row.RelativePath, err, "it could not be checked")
			continue
		}
		if err := s.db.DeleteDeployedFile(ctx, game.ID, profileName, row.RelativePath); err != nil {
			left[key]++
			emitNote(fmt.Sprintf("⚠ %s - %v", row.RelativePath, err))
			continue
		}
		if _, ok := removed[rowGame.ModPath]; !ok {
			roots = append(roots, rowGame.ModPath)
		}
		removed[rowGame.ModPath] = append(removed[rowGame.ModPath], row.RelativePath)
		result.RemovedPaths++
	}
	for _, root := range roots {
		linker.CleanupEmptyDirs(root, removed[root])
	}
	return left, nil
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
// meant to remove and could not remove, or check, is a Warnings entry. A
// purge of the active profile reports in Kept the paths it left because
// another game records them (PurgeKeptOtherGame, #445 gate 2, G2-1) or
// because the user changed them (PurgeKeptUserFile, #466), and in
// RemovedPaths the files it removed from a mod_path the game no longer
// uses (#451) - its other removals are counted per mod, in Purged.
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
	return s.purgeProfile(ctx, game, profileName, mods, nil, opts, sink)
}

// purgeProfile purges mods from profileName, the active profile. plan, when
// set, is the plan the user confirmed: its stranded files (#451) are the
// only ones removed from an earlier mod_path. Nil removes what is stranded
// now.
func (s *Service) purgeProfile(ctx context.Context, game *domain.Game, profileName string, mods []domain.InstalledMod, plan *PurgePlan, opts PurgeOptions, sink EventSink) (*PurgeResult, error) {
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

	// #451: what this profile deployed under a mod_path the game no longer
	// uses goes first, from where it was deployed.
	if err := s.purgeStranded(ctx, game, profileName, plan, result, func(e Event) {
		if sink != nil {
			sink(e)
		}
	}); err != nil {
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
		kept:     &result.Kept,
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
