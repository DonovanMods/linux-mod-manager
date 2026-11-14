package db_test

// #607: installed_mods.mod_type is the row's copy of its profile
// reference's stated mod type (a collection's "dinput"), which every deploy
// of the row routes by.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readModType(t *testing.T, database *db.DB) string {
	t.Helper()
	got, err := database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	mods, err := database.GetInstalledMods(context.Background(), "skyrim-se", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, got.ModType, mods[0].ModType, "the list read agrees")
	return got.ModType
}

func TestSaveInstalledMod_RoundTripsModType(t *testing.T) {
	database := variantTestDB(t)
	row := variantRow("")
	row.ModType = "dinput"
	require.NoError(t, database.SaveInstalledMod(context.Background(), row))
	assert.Equal(t, "dinput", readModType(t, database))

	// A re-save that does not state a type keeps the stored one, as a
	// category name is kept: a flow that rebuilt the row from the source's
	// metadata must not route the mod back to the mod_path.
	require.NoError(t, database.SaveInstalledMod(context.Background(), variantRow("")))
	assert.Equal(t, "dinput", readModType(t, database))

	again := variantRow("")
	again.ModType = "enb"
	require.NoError(t, database.SaveInstalledMod(context.Background(), again))
	assert.Equal(t, "enb", readModType(t, database), "a stated type replaces the stored one")
}

func TestRelinkInstalledMod_CarriesModType(t *testing.T) {
	database := variantTestDB(t)
	row := variantRow("")
	row.ModType = "dinput"
	require.NoError(t, database.SaveInstalledMod(context.Background(), row))
	moved := variantRow("")
	require.NoError(t, database.RelinkInstalledMod(context.Background(), "nexusmods", "12345", moved))
	assert.Equal(t, "dinput", readModType(t, database))
}
