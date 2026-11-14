package app

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/modtypes"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #635: detection prefills the mod-type adapters for their games' Steam app
// ids, as #606 did for the Bethesda games, and those games deploy into the
// mod_path the adapters' tables are relative to.
func TestKnownGames_ModTypeAdaptersArePrefilled(t *testing.T) {
	known, err := steam.LoadKnownGames(t.TempDir())
	require.NoError(t, err)
	for appID, want := range map[string]struct{ adapter, modPath string }{
		"292030":  {modtypes.Witcher3ID, "mods"},     // The Witcher 3: Wild Hunt
		"1086940": {modtypes.BaldursGate3ID, "Data"}, // Baldur's Gate 3
	} {
		info, ok := known[appID]
		require.True(t, ok, appID)
		assert.Equal(t, want.adapter, info.Adapter, "app id %s (%s)", appID, info.Slug)
		assert.Equal(t, want.modPath, info.ModPath, "app id %s (%s)", appID, info.Slug)
	}
}
