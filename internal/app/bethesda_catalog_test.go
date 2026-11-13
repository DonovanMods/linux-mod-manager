package app

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bethesda"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tier1Bethesda are the Steam app ids of the bethesda adapter's Tier 1
// games (#606): Skyrim SE/AE, Skyrim VR, Fallout 4, Fallout 4 VR, Starfield
// and Skyrim LE.
var tier1Bethesda = []string{"489830", "611670", "377160", "611660", "1716740", "72850"}

// Every adapter the shipped catalog names must be one this process
// registers: detection writes the value into games.yaml verbatim, and an
// unregistered one would resolve to nothing.
func TestKnownGames_AdaptersAreRegistered(t *testing.T) {
	known, err := steam.LoadKnownGames(t.TempDir())
	require.NoError(t, err)
	svc := newTestService(t)
	RegisterAdapters(svc)
	registered := svc.ListAdapters()
	for appID, info := range known {
		if info.Adapter != "" {
			assert.Contains(t, registered, info.Adapter, "app id %s (%s)", appID, info.Slug)
		}
	}
}

// The catalog and the adapter's own table must agree on which games are
// Tier 1 Bethesda games: detection prefills `adapter: bethesda` from the
// catalog, and the adapter recognises a game from its slug or app id, so
// a game detection prefills must be one the adapter recognises, both ways.
func TestKnownGames_BethesdaEntriesAgreeWithTheAdapter(t *testing.T) {
	known, err := steam.LoadKnownGames(t.TempDir())
	require.NoError(t, err)
	a := bethesda.New()

	for _, appID := range tier1Bethesda {
		info, ok := known[appID]
		require.True(t, ok, "Tier 1 app id %s is not in the catalog", appID)
		assert.Equal(t, bethesda.ID, info.Adapter, "app id %s (%s) must prefill adapter: bethesda", appID, info.Slug)
		assert.Equal(t, "Data", info.ModPath, "app id %s (%s) deploys into Data/", appID, info.Slug)

		_, bySlug := adapter.PluginRules(a, &domain.Game{ID: info.Slug})
		assert.True(t, bySlug, "the adapter must recognise detection's slug %q", info.Slug)
		_, byID := adapter.PluginRules(a, &domain.Game{ID: "x", Launch: &domain.GameLaunch{SteamAppID: appID}})
		assert.True(t, byID, "the adapter must recognise app id %s", appID)
	}
	for appID, info := range known {
		if info.Adapter == bethesda.ID {
			assert.Contains(t, tier1Bethesda, appID, "%s names adapter: bethesda but is not a Tier 1 game", info.Slug)
		}
	}
}
