// Package core provides business logic orchestration for lmm.
// purge.go holds the purge flow: purgeSpec/purgeMods - THE shared purge
// loop (#61), consumed by both `lmm purge` and deploy.go's purgeForDeploy -
// plus PurgeOptions/PurgeResult and PurgeProfile. Moved verbatim out of
// flows.go by v2 Phase 2 Unit M (#293), completing the split deploy.go's
// header comment anticipated.
package core

import (
	"context"
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
