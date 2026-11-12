package bethesda

import (
	"context"
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
)

// StatusGameUnknown is the verify status of a game set to `adapter:
// bethesda` that the adapter cannot identify.
const StatusGameUnknown = "bethesda_game_unknown"

// Verify reports, as a note, a game the adapter cannot tell is one of its
// Tier 1 games: such a game has no plugin rules, so lmm will not manage its
// plugin list. A note counts towards neither tally, so it never changes
// `lmm verify`'s exit code. Read-only, like every adapter check.
func (*Adapter) Verify(_ context.Context, req adapter.VerifyRequest) ([]adapter.Finding, error) {
	if _, ok := kindFor(req.Game); ok || req.Game == nil {
		return nil, nil
	}
	return []adapter.Finding{{
		Status: StatusGameUnknown,
		Note: fmt.Sprintf(
			"lmm cannot tell which Bethesda game %q is, so it has no plugin rules for it. "+
				"It recognises a game by the Steam app id games.yaml states (launch.steam_app_id) "+
				"or by the game ids %s",
			req.Game.ID, strings.Join(recognisedSlugs(), ", ")),
		Severity:      adapter.SeverityNote,
		FixableReason: "lmm never renames a game; add the game again from `lmm game detect` to get its recognised id",
	}}, nil
}
