package db_test

// #542: skipped_version is the one pending update the user chose to hold off
// on. Like update_policy it is a user setting on the row: SaveInstalledMod
// never resets it, and only the version change of an applied update clears
// it.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skippedVersionOf(t *testing.T, database *db.DB) string {
	t.Helper()
	got, err := database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	return got.SkippedVersion
}

func TestSetModSkippedVersion_RoundTripsAndClears(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	installTestMod(t, database)
	ctx := context.Background()

	assert.Empty(t, skippedVersionOf(t, database), "a new row skips nothing")

	require.NoError(t, database.SetModSkippedVersion(ctx, "nexusmods", "12345", "skyrim-se", "default", "1.1.0"))
	assert.Equal(t, "1.1.0", skippedVersionOf(t, database))

	mods, err := database.GetInstalledMods(ctx, "skyrim-se", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, "1.1.0", mods[0].SkippedVersion, "the list read carries it too")

	require.NoError(t, database.SetModSkippedVersion(ctx, "nexusmods", "12345", "skyrim-se", "default", ""))
	assert.Empty(t, skippedVersionOf(t, database), "an empty version clears the skip")

	err = database.SetModSkippedVersion(ctx, "nexusmods", "nope", "skyrim-se", "default", "1.1.0")
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

func TestSaveInstalledMod_PreservesSkippedVersionOnResave(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	installTestMod(t, database)
	ctx := context.Background()
	require.NoError(t, database.SetModSkippedVersion(ctx, "nexusmods", "12345", "skyrim-se", "default", "1.1.0"))

	installTestMod(t, database) // a re-save, as a reinstall or redeploy does
	assert.Equal(t, "1.1.0", skippedVersionOf(t, database))
}

func TestApplyModUpdate_ClearsSkippedVersion(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	installTestMod(t, database)
	ctx := context.Background()
	require.NoError(t, database.SetModSkippedVersion(ctx, "nexusmods", "12345", "skyrim-se", "default", "1.1.0"))

	require.NoError(t, database.ApplyModUpdate(ctx, "nexusmods", "12345", "skyrim-se", "default", "1.1.0", []string{"f2"}))
	assert.Empty(t, skippedVersionOf(t, database), "an applied update clears the skip")
}
