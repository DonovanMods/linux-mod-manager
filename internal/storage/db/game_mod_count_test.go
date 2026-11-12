package db_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #606: how many mods lmm has ingested for a game, whatever profile holds
// them - the mods an adapter switch leaves in their old layout. A mod in two
// profiles counts once, and an external (Steam Workshop) mod, whose files lmm
// never lays out, not at all.
func TestCountGameMods(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	ctx := context.Background()

	save := func(gameID, profile, sourceID, modID string, external bool) {
		t.Helper()
		require.NoError(t, database.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: modID, Version: "1.0", GameID: gameID},
			ProfileName:  profile,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			External:     external,
		}))
	}
	save("g", "default", "nexusmods", "1", false)
	save("g", "other", "nexusmods", "1", false)  // same mod, second profile
	save("g", "other", "curseforge", "1", false) // same id, other source
	save("g", "default", "steamworkshop", "9", true)
	save("h", "default", "nexusmods", "2", false)

	n, err := database.CountGameMods(ctx, "g")
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	n, err = database.CountGameMods(ctx, "none")
	require.NoError(t, err)
	assert.Zero(t, n)
}
