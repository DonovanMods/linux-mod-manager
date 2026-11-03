package db_test

// #592: installed_mods.category_name is the source's human-readable
// category for the mod (NexusMods' resolved name, CurseForge's primary
// category name). It is written from Mod.CategoryName on a save, but an
// empty CategoryName never erases a stored one - a flow that saves a row
// from metadata with no category (an archive import, a from-file update)
// must not lose what an earlier fetch recorded. SetModCategoryName is the
// update check's single-column backfill.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func categoryTestMod(category string) *domain.InstalledMod {
	return &domain.InstalledMod{
		Mod: domain.Mod{
			ID: "12345", SourceID: "nexusmods", Name: "Test Mod", Version: "1.0.0",
			GameID: "skyrim-se", CategoryName: category,
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}
}

func categoryNameOf(t *testing.T, database *db.DB) string {
	t.Helper()
	got, err := database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	mods, err := database.GetInstalledMods(context.Background(), "skyrim-se", "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, got.CategoryName, mods[0].CategoryName, "the list read and the single read agree")
	return got.CategoryName
}

func TestSaveInstalledMod_PersistsCategoryName(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	ctx := context.Background()

	require.NoError(t, database.SaveInstalledMod(ctx, categoryTestMod("User Interface")))
	assert.Equal(t, "User Interface", categoryNameOf(t, database))

	require.NoError(t, database.SaveInstalledMod(ctx, categoryTestMod("Gameplay")))
	assert.Equal(t, "Gameplay", categoryNameOf(t, database), "a fresh non-empty name replaces the stored one")

	require.NoError(t, database.SaveInstalledMod(ctx, categoryTestMod("")))
	assert.Equal(t, "Gameplay", categoryNameOf(t, database), "an empty name never erases a stored one")
}

func TestSaveInstalledMod_NoCategoryReadsEmpty(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	installTestMod(t, database)
	assert.Empty(t, categoryNameOf(t, database))
}

func TestSetModCategoryName_TouchesOnlyTheColumn(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	ctx := context.Background()
	require.NoError(t, database.SaveInstalledMod(ctx, categoryTestMod("")))
	require.NoError(t, database.SetModSkippedVersion(ctx, "nexusmods", "12345", "skyrim-se", "default", "1.1.0"))
	before, err := database.GetInstalledMod(ctx, "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)

	require.NoError(t, database.SetModCategoryName(ctx, "nexusmods", "12345", "skyrim-se", "default", "Armour"))
	after, err := database.GetInstalledMod(ctx, "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "Armour", after.CategoryName)
	after.CategoryName = ""
	assert.Equal(t, before, after, "nothing but category_name moved")

	err = database.SetModCategoryName(ctx, "nexusmods", "nope", "skyrim-se", "default", "Armour")
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

func TestRelinkInstalledMod_KeepsCategoryName(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	ctx := context.Background()
	require.NoError(t, database.SaveInstalledMod(ctx, categoryTestMod("Armour")))

	moved := categoryTestMod("")
	require.NoError(t, database.RelinkInstalledMod(ctx, "nexusmods", "12345", moved))
	assert.Equal(t, "Armour", categoryNameOf(t, database), "a relink with no category keeps the stored one")
}
