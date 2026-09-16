package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// recordProfileDisabled writes #431's per-profile off marker onto the
// profile ref for sourceID/modID, completing the installed_mods write the
// enable/disable flow has ALREADY applied - the class-(A) pair Ruling 16
// covers, so the profile file is written even under a cancelled ctx rather
// than left disagreeing with the row.
//
// Returns the diagnostic to record as a Note, or "" when there is nothing
// to say. A profile that does not list the mod (domain.ErrModNotFound) is
// one of those: there is no desired-state entry to mark, and no converge
// pass will look for one - see ProfileManager.SetModDisabled.
func (s *Service) recordProfileDisabled(ctx context.Context, gameID, profileName, sourceID, modID string, disabled bool) string {
	pm := s.NewProfileManager()
	err := completeProfileWrite(ctx, func(ctx context.Context) error {
		return pm.SetModDisabled(ctx, gameID, profileName, sourceID, modID, disabled)
	})
	switch {
	case err == nil, errors.Is(err, domain.ErrModNotFound), errors.Is(err, domain.ErrProfileNotFound):
		return ""
	default:
		return fmt.Sprintf("Warning: could not record the profile's %s state: %v", disabledWord(disabled), err)
	}
}

// disabledWord names the state recordProfileDisabled was writing, for its
// one diagnostic.
func disabledWord(disabled bool) string {
	if disabled {
		return "disabled"
	}
	return "enabled"
}

// EnableResult reports the outcome of EnableMod. Changed is true iff the
// mod was actually deployed and flipped to enabled — false (not an error)
// when it was already enabled, mirroring EnableMod's pre-Task-6 (bool,
// error) return. Notes carries operational diagnostics using the same
// display-contract convention as UninstallResult/DeployResult (Task 2's
// convention, extended here in Task 6 item a for result-struct
// convergence, and by #183's SetModDeployed note below): a caller wanting
// byte-identical pre-5a output should print each entry to stdout ONLY
// under --verbose, verbatim, e.g. `fmt.Printf("  %s\n", n)`.
type EnableResult struct {
	Changed bool     `json:"changed"`
	Notes   []string `json:"notes,omitempty"`
	// Warnings holds diagnostics that must reach the user unconditionally
	// (#197 postsmoke fix), unlike Notes' --verbose-only display contract -
	// today, only a merged-pak sync failure. A silent sync failure here is
	// exactly the class of bug the postsmoke fix-wave exists to close: the
	// mod's Enabled bit flips, but the game directory may not actually
	// reflect it.
	Warnings []string `json:"warnings,omitempty"`
}

// DisableResult reports the outcome of DisableMod. Changed mirrors
// EnableResult.Changed. Notes carries the diagnostics DisableMod can
// produce — a non-fatal undeploy failure (see DisableMod's doc comment) and
// (#183) a non-fatal SetModDeployed failure — using the same
// historical-prefix-baked-into-the-text convention UninstallResult's doc
// comment documents: a caller wanting byte-identical pre-5a output should
// print each entry to stdout ONLY under --verbose, verbatim, e.g.
// `fmt.Printf("  %s\n", n)`.
type DisableResult struct {
	Changed bool     `json:"changed"`
	Notes   []string `json:"notes,omitempty"`
	// Warnings mirrors EnableResult.Warnings' identical rationale
	// (#197 postsmoke fix): unconditional display, unlike Notes.
	Warnings []string `json:"warnings,omitempty"`
}

// EnableMod deploys an installed-but-disabled mod's files from the cache to
// the game directory and marks it enabled (and deployed, #183) in the
// database. Returns a result with Changed false — not an error — if the
// mod was already enabled.
//
// A SetModDeployed failure is non-fatal (recorded in Notes) — mirroring
// both DisableMod's own treatment of the identical call and
// DeployProfile's/PurgeProfile's existing SetModDeployed call sites: the
// files are already live on disk at this point, and refusing to record the
// user's intent to enable the mod over a secondary bookkeeping-write
// failure would leave it stuck exactly like the undeploy-failure case
// DisableMod already accepts. SetModEnabled's own failure, in contrast,
// stays fatal (pre-existing behavior, unchanged) — it is the write that
// makes "the mod is enabled" true at all, unlike the deployed flag, which
// is a cache of already-true, already-observable state.
func (s *Service) EnableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*EnableResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.enableMod(ctx, game, profileName, sourceID, modID)
}

func (s *Service) enableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*EnableResult, error) {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mod %s: %w", modID, err)
	}

	// #269: lmm cannot make a Steam Workshop item active or inactive - the
	// game loads it from Steam's own directory either way. The refusal comes
	// BEFORE the already-enabled short-circuit so the answer is the same
	// whatever the row happens to say.
	if err := refuseExternal("enable", mod, ReasonExternalNoToggle); err != nil {
		return nil, err
	}

	if mod.Enabled {
		return &EnableResult{}, nil
	}

	if !s.GetGameCache(game).Exists(game.ID, sourceID, modID, mod.Version) {
		return nil, fmt.Errorf("mod not found in cache - try reinstalling with 'lmm install --id %s'", modID)
	}

	installer, err := s.getInstallerForProfile(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	if err := installer.Install(ctx, game, &mod.Mod, profileName); err != nil {
		return nil, fmt.Errorf("failed to deploy mod: %w", err)
	}

	result := &EnableResult{}
	if err := s.setModDeployed(ctx, sourceID, modID, game.ID, profileName, true); err != nil {
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: could not mark as deployed: %v", err))
	}

	if err := s.setModEnabled(ctx, sourceID, modID, game.ID, profileName, true); err != nil {
		return result, fmt.Errorf("failed to update mod status: %w", err)
	}

	// #431: the row alone is not where this intent belongs - a profile
	// switch overwrites it and an export never carried it. Clearing the
	// marker is what makes the profile document say "on" again.
	if msg := s.recordProfileDisabled(ctx, game.ID, profileName, sourceID, modID, false); msg != "" {
		result.Notes = append(result.Notes, msg)
	}

	// #197 postsmoke fix: Warnings, not Notes - Notes is --verbose-gated in
	// the CLI (printModNotes), so a sync failure here used to be silent by
	// default.
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not sync merged pak: %v", syncErr))
	} else {
		result.Warnings = append(result.Warnings, syncWarnings...)
	}

	result.Changed = true
	return result, nil
}

// DisableMod undeploys the mod's files from the game directory — the cache
// entry is kept so the mod can be re-enabled later without downloading again
// — and marks it disabled (and not-deployed, #183) in the database. Returns
// a result with Changed false — not an error — if the mod was already
// disabled. That already-disabled path still self-heals a stale
// deployed=true left behind by a pre-#183 disable (or any other drift):
// it clears the flag, non-fatally, before returning, so calling disable
// again converges deployed state even when enabled was already false.
//
// Undeploy failures are treated as non-fatal: the game files may already
// have been removed manually, and refusing to record the user's intent to
// disable the mod would leave it stuck. This mirrors the pre-extraction CLI,
// which warned (under --verbose) but always continued to flip the DB state
// — DisableResult.Notes (Task 6 item a) restores that diagnostic for
// callers that want it, rather than discarding it as the (bool, error)
// signature this replaces was forced to.
//
// A SetModDeployed failure gets the identical non-fatal treatment (#183),
// for the identical reason and matching DeployProfile's/PurgeProfile's own
// SetModDeployed call sites: it is attempted unconditionally, even when the
// undeploy above already failed, because the deployed flag should reflect
// "disable was requested" regardless of whether the file-level undeploy
// itself succeeded — an undeploy failure already means the flag may not
// match reality either way, and the alternative (skipping the write
// because undeploy failed) would leave the mod stuck reporting DEPLOYED
// forever, which is #183 itself. SetModEnabled's own failure stays fatal,
// unchanged: it is the write that makes "the mod is disabled" true at all,
// not a cache of already-true state.
func (s *Service) DisableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*DisableResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.disableMod(ctx, game, profileName, sourceID, modID)
}

func (s *Service) disableMod(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*DisableResult, error) {
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mod %s: %w", modID, err)
	}

	// #269: refused for the same reason enable is - a bookkeeping-only
	// "disabled" flag on a mod the game still loads is a lie.
	if err := refuseExternal("disable", mod, ReasonExternalNoToggle); err != nil {
		return nil, err
	}

	if !mod.Enabled {
		// Self-heal (#183): a mod disabled before this fix shipped can be
		// stuck with enabled=false but deployed=true forever, since nothing
		// else clears the flag once the mod is already disabled. Clear it
		// here too, under the same non-fatal Note convention as the
		// already-enabled path below, so disable converges the flag even
		// when called on an already-disabled mod.
		result := &DisableResult{}
		if mod.Deployed {
			if err := s.setModDeployed(ctx, sourceID, modID, game.ID, profileName, false); err != nil {
				result.Notes = append(result.Notes, fmt.Sprintf("Warning: could not mark as not deployed: %v", err))
			}
		}
		// #431, same self-heal reasoning: a mod disabled before the marker
		// existed has a disabled row and an unmarked ref, so the next
		// switch or apply would switch it back on. Disabling it again
		// records the intent where a converge pass reads it.
		if msg := s.recordProfileDisabled(ctx, game.ID, profileName, sourceID, modID, true); msg != "" {
			result.Notes = append(result.Notes, msg)
		}
		return result, nil
	}

	result := &DisableResult{}
	installer, err := s.getInstallerForProfile(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
		// Non-fatal — see doc comment. Historical "Warning: " prefix baked
		// into the text itself, matching UninstallResult's own convention.
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: failed to undeploy some files: %v", err))
	}

	if err := s.setModDeployed(ctx, sourceID, modID, game.ID, profileName, false); err != nil {
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: could not mark as not deployed: %v", err))
	}

	if err := s.setModEnabled(ctx, sourceID, modID, game.ID, profileName, false); err != nil {
		return result, fmt.Errorf("failed to update mod status: %w", err)
	}

	// #431: the profile document is the desired state a converge run
	// restores, so "off" has to be recorded there too - otherwise the next
	// `profile switch` or `profile apply` reads a document that still says
	// "on" and deploys the mod again.
	if msg := s.recordProfileDisabled(ctx, game.ID, profileName, sourceID, modID, true); msg != "" {
		result.Notes = append(result.Notes, msg)
	}

	// #197 postsmoke fix: Warnings, not Notes (see EnableMod's identical fix).
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not sync merged pak: %v", syncErr))
	} else {
		result.Warnings = append(result.Warnings, syncWarnings...)
	}

	result.Changed = true
	return result, nil
}
