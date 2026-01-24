package db_test

import (
	"testing"

	"lmm/internal/domain"
	"lmm/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_CreatesDatabase(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	assert.NotNil(t, database)
}

func TestNew_RunsMigrations(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	// Verify tables exist by querying them
	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM installed_mods").Scan(&count)
	assert.NoError(t, err)

	err = database.QueryRow("SELECT COUNT(*) FROM mod_cache").Scan(&count)
	assert.NoError(t, err)

	err = database.QueryRow("SELECT COUNT(*) FROM auth_tokens").Scan(&count)
	assert.NoError(t, err)
}

func TestInstalledMods_SaveAndGet(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "12345",
			SourceID: "nexusmods",
			Name:     "Test Mod",
			Version:  "1.0.0",
			Author:   "TestAuthor",
			GameID:   "skyrim-se",
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}

	err = database.SaveInstalledMod(mod)
	require.NoError(t, err)

	retrieved, err := database.GetInstalledMods("skyrim-se", "default")
	require.NoError(t, err)
	require.Len(t, retrieved, 1)

	assert.Equal(t, mod.ID, retrieved[0].ID)
	assert.Equal(t, mod.Name, retrieved[0].Name)
	assert.Equal(t, mod.Version, retrieved[0].Version)
}

func TestInstalledMods_Delete(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "12345",
			SourceID: "nexusmods",
			Name:     "Test Mod",
			Version:  "1.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName: "default",
	}

	err = database.SaveInstalledMod(mod)
	require.NoError(t, err)

	err = database.DeleteInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)

	mods, err := database.GetInstalledMods("skyrim-se", "default")
	require.NoError(t, err)
	assert.Empty(t, mods)
}
