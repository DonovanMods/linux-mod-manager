package app

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/steam"
)

// DetectGames scans Steam libraries for known moddable games, exposing
// steam.DetectGames as a domain-typed call so internal/core can consume
// detected games without importing a concrete source (v2 Phase 2 Task 21,
// Ruling 8). Mirrors Open's convention: an already-cancelled ctx aborts
// before the scan runs. steam.DetectGames itself has nothing to cancel
// (local filesystem reads only), so ctx governs only that leading check.
func DetectGames(ctx context.Context, configDir string) ([]domain.DetectedGame, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return steam.DetectGames(configDir)
}
