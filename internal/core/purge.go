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

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
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
				*spec.skipped = append(*spec.skipped, skippedRef(&mod, fmt.Sprintf("failed to remove record: %v", err)))
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

	// MergedArtifact is what purgeMergedPak would do to the profile's
	// merged artifact on a DeployCompile game - an effect Mods cannot
	// express, since exmodz mods have no per-mod deployment of their own
	// (#197 I2). Always a removal when set; nil when the game does not
	// deploy by compilation, and nil when there is no deployed artifact to
	// remove (Ruling 8). See mergedArtifactEffectForPurge.
	MergedArtifact *MergedArtifactEffect `json:"merged_artifact"`

	// snapshot is Ruling 5's precondition: the installed-mod set this plan
	// was computed from, re-derived and compared by ApplyPurge.
	snapshot installedSnapshot `json:"-"`
}

// PlanPurge computes what PurgeProfile would do for game/profileName under
// opts, without touching anything - including the installed-mods read the
// pre-lift cmd/lmm/purge.go did itself before prompting (its "getting
// installed mods: …" wording is preserved on that read's failure).
//
// The returned plan is a snapshot: pass it to ApplyPurge promptly, and be
// ready for ErrStalePlan if the installed set moved underneath it.
func (s *Service) PlanPurge(ctx context.Context, game *domain.Game, profileName string, opts PurgeOptions) (*PurgePlan, error) {
	mods, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}
	plan := &PurgePlan{
		Profile:        profileName,
		Mods:           mods,
		Uninstall:      opts.Uninstall,
		MergedArtifact: s.mergedArtifactEffectForPurge(game),
		snapshot:       snapshotOf(mods),
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
	if err := s.checkPlanFresh(ctx, game.ID, plan.Profile, plan.snapshot); err != nil {
		return &PurgeResult{}, err
	}
	return s.purgeProfile(ctx, game, plan.Profile, plan.Mods, opts, sink)
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
type PurgeResult struct {
	Purged   int            `json:"purged"`
	Skipped  []InstalledRef `json:"skipped,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
	Notes    []string       `json:"notes,omitempty"`
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
