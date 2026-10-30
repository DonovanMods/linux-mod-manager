package app

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/battlenet"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
)

// DetectOptions tunes a DetectGames scan. It is steam.DetectOptions,
// aliased here so a frontend can ask for the wider "every installed app"
// scan (#206) through the same domain-typed seam it already calls, without
// importing the concrete Steam source itself (Ruling 8). Its
// IncludeUnknown and Logger apply to the Battle.net scan as well (#545);
// NoWorkshop is Steam's alone.
type DetectOptions = steam.DetectOptions

// DetectGames scans for installed moddable games - Steam libraries, then
// Battle.net games in every Wine prefix (#545) - exposing both concrete
// scans as one domain-typed call so internal/core can consume detected
// games without importing a concrete source (v2 Phase 2 Task 21, Ruling
// 8). Every frontend reaches detection through here, so a detector added
// here is in `lmm game detect`, `game add --from-detected`, `init` and
// serve's detect and Add Game at once. Mirrors Open's convention: an
// already-cancelled ctx aborts before the scan runs. Neither scan has
// anything to cancel (local filesystem reads only), so ctx governs only
// that leading check.
//
// opts.IncludeUnknown widens both scans to every OTHER installed game as
// an unknown candidate (#206) - except Battle.net's deny-listed titles,
// which no option lists; the zero DetectOptions is the known-only scan
// every caller made before that option existed.
func DetectGames(ctx context.Context, configDir string, opts DetectOptions) ([]domain.DetectedGame, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	steamGames, warnings, err := steam.DetectGames(configDir, opts)
	if err != nil {
		return nil, nil, err
	}
	bnetGames, bnetWarnings, err := battlenet.DetectGames(configDir, battlenet.DetectOptions{
		IncludeUnknown: opts.IncludeUnknown, Logger: opts.Logger,
	})
	if err != nil {
		return nil, nil, err
	}
	return mergeDetected(steamGames, bnetGames), append(warnings, bnetWarnings...), nil
}

// mergeDetected appends the Battle.net rows to the Steam ones, renaming any
// Battle.net row whose slug a Steam row already has: a slug names a row in
// a detect selection, so two rows must never share one. Steam's keep
// theirs, since they are the rows every detect listing before #545 named.
func mergeDetected(steamGames, bnetGames []domain.DetectedGame) []domain.DetectedGame {
	if len(bnetGames) == 0 {
		return steamGames
	}
	taken := make(map[string]bool, len(steamGames)+len(bnetGames))
	for _, g := range steamGames {
		taken[g.Slug] = true
	}
	out := append([]domain.DetectedGame(nil), steamGames...)
	for _, g := range bnetGames {
		if taken[g.Slug] {
			base := g.Slug + "-" + domain.DetectorBattleNet
			g.Slug = base
			for n := 2; taken[g.Slug]; n++ {
				g.Slug = fmt.Sprintf("%s-%d", base, n)
			}
		}
		taken[g.Slug] = true
		out = append(out, g)
	}
	return out
}
