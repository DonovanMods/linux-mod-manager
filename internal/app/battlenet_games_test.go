package app

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/battlenet"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBattleNetGamesListIsWellFormed is TestKnownGamesListIsWellFormed for
// #545's embedded battlenet-games.yaml: slugs well formed and unique - also
// against steam-games.yaml's, since app.DetectGames lists both - every
// source a built-in one, every CurseForge version type valid.
func TestBattleNetGamesListIsWellFormed(t *testing.T) {
	sandboxHome(t)
	registered := make(map[string]bool, len(builtinSourceIDs))
	for _, id := range builtinSourceIDs {
		registered[id] = true
	}

	games, err := battlenet.LoadKnownGames(t.TempDir())
	require.NoError(t, err)
	require.NotEmpty(t, games)
	steamGames, err := steam.LoadKnownGames(t.TempDir())
	require.NoError(t, err)

	slugOwner := map[string]string{}
	for appID, info := range steamGames {
		slugOwner[info.Slug] = "steam " + appID
	}
	for code, info := range games {
		assert.Regexp(t, slugPattern, info.Slug, code)
		if other, taken := slugOwner[info.Slug]; taken {
			t.Errorf("slug %q is used by both %s and battlenet %s", info.Slug, other, code)
		}
		slugOwner[info.Slug] = "battlenet " + code
		for id, gameID := range info.Sources {
			assert.True(t, registered[id], "%s maps source %q, which no built-in source registers", code, id)
			assert.NotEmpty(t, gameID, code)
		}
		_, err := domain.NewGameCurseForge(info.CurseForgeVersionTypes)
		assert.NoError(t, err, code)
	}
}
