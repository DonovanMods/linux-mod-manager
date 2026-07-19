package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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

	if err := runUninstallHook(ctx, opts.HookRunner, &hookCtx, "uninstall.before_all", opts.Hooks.GetUninstallBeforeAll()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("uninstall.before_all hook failed: %w", err)
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("uninstall.before_all hook failed (forced): %v", err))
	}

	hookCtx.ModID = mod.ID
	hookCtx.ModName = mod.Name
	hookCtx.ModVersion = mod.Version
	if err := runUninstallHook(ctx, opts.HookRunner, &hookCtx, "uninstall.before_each", opts.Hooks.GetUninstallBeforeEach()); err != nil {
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

	if err := runUninstallHook(ctx, opts.HookRunner, &hookCtx, "uninstall.after_each", opts.Hooks.GetUninstallAfterEach()); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("uninstall.after_each hook failed: %v", err))
	}

	hookCtx.ModID = ""
	hookCtx.ModName = ""
	hookCtx.ModVersion = ""
	if err := runUninstallHook(ctx, opts.HookRunner, &hookCtx, "uninstall.after_all", opts.Hooks.GetUninstallAfterAll()); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("uninstall.after_all hook failed: %v", err))
	}

	return result, nil
}

// runUninstallHook runs command (a hook script path) via runner if both are
// set, updating hookCtx.HookName first. No-op if runner is nil or command
// is empty (hooks disabled, or that particular hook isn't configured).
func runUninstallHook(ctx context.Context, runner *HookRunner, hookCtx *HookContext, hookName, command string) error {
	if runner == nil || command == "" {
		return nil
	}
	hookCtx.HookName = hookName
	_, err := runner.Run(ctx, command, *hookCtx)
	return err
}
