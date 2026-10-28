package app

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
)

// DetectOptions tunes a DetectGames scan. It is steam.DetectOptions,
// aliased here so a frontend can ask for the wider "every installed app"
// scan (#206) through the same domain-typed seam it already calls, without
// importing the concrete Steam source itself (Ruling 8).
type DetectOptions = steam.DetectOptions

// DetectGames scans Steam libraries for known moddable games, exposing
// steam.DetectGames as a domain-typed call so internal/core can consume
// detected games without importing a concrete source (v2 Phase 2 Task 21,
// Ruling 8). Mirrors Open's convention: an already-cancelled ctx aborts
// before the scan runs. steam.DetectGames itself has nothing to cancel
// (local filesystem reads only), so ctx governs only that leading check.
//
// opts.IncludeUnknown widens the scan to every OTHER installed Steam app
// as an unknown candidate (#206); the zero DetectOptions is the known-only
// scan every caller made before that option existed.
func DetectGames(ctx context.Context, configDir string, opts DetectOptions) ([]domain.DetectedGame, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return steam.DetectGames(configDir, opts)
}
