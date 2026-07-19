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
