package db_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"

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

func TestMigrationV2_PreviousVersionColumn(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	// Verify previous_version column exists by querying it
	var prevVersion interface{}
	err = database.QueryRow(`
		SELECT previous_version FROM installed_mods LIMIT 1
	`).Scan(&prevVersion)
	// This should not error on column not found - only on no rows
	// which is expected since table is empty
	assert.ErrorContains(t, err, "no rows")
}

func TestUpdateModVersion(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	// Create initial mod
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

	// Update version
	err = database.UpdateModVersion("nexusmods", "12345", "skyrim-se", "default", "2.0.0")
	require.NoError(t, err)

	// Retrieve and verify
	retrieved, err := database.GetInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", retrieved.Version)
	assert.Equal(t, "1.0.0", retrieved.PreviousVersion)
}

func TestSwapModVersions(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	// Create mod with previous version
	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "12345",
			SourceID: "nexusmods",
			Name:     "Test Mod",
			Version:  "2.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName:     "default",
		PreviousVersion: "1.0.0",
	}
	err = database.SaveInstalledMod(mod)
	require.NoError(t, err)

	// Swap versions (rollback)
	err = database.SwapModVersions("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)

	// Verify swap
	retrieved, err := database.GetInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", retrieved.Version)
	assert.Equal(t, "2.0.0", retrieved.PreviousVersion)
}

func TestSwapModVersions_NoPreviousVersion(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	// Create mod without previous version
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

	// Swap should fail - no previous version
	err = database.SwapModVersions("nexusmods", "12345", "skyrim-se", "default")
	assert.Error(t, err)
}

func TestGetInstalledMod(t *testing.T) {
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
		UpdatePolicy: domain.UpdateAuto,
		Enabled:      true,
	}
	err = database.SaveInstalledMod(mod)
	require.NoError(t, err)

	retrieved, err := database.GetInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, mod.ID, retrieved.ID)
	assert.Equal(t, mod.Name, retrieved.Name)
	assert.Equal(t, mod.UpdatePolicy, retrieved.UpdatePolicy)
}

func TestGetInstalledMod_NotFound(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	_, err = database.GetInstalledMod("nexusmods", "nonexistent", "skyrim-se", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

func TestSetModDeployed(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	// Create a deployed mod
	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "12345",
			SourceID: "nexusmods",
			Name:     "Test Mod",
			Version:  "1.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName: "default",
		Enabled:     true,
		Deployed:    true,
	}
	err = database.SaveInstalledMod(mod)
	require.NoError(t, err)

	// Verify initial deployed state
	retrieved, err := database.GetInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, retrieved.Deployed)

	// Set deployed to false (purge scenario)
	err = database.SetModDeployed("nexusmods", "12345", "skyrim-se", "default", false)
	require.NoError(t, err)

	// Verify deployed is now false but enabled unchanged
	retrieved, err = database.GetInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.False(t, retrieved.Deployed)
	assert.True(t, retrieved.Enabled) // Enabled should remain true

	// Set deployed back to true (deploy scenario)
	err = database.SetModDeployed("nexusmods", "12345", "skyrim-se", "default", true)
	require.NoError(t, err)

	retrieved, err = database.GetInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, retrieved.Deployed)
}

func TestSetModDeployed_NotFound(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	err = database.SetModDeployed("nexusmods", "nonexistent", "skyrim-se", "default", false)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

func TestMigrationV5_DeployedColumn(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	// Verify deployed column exists by querying it
	var deployed interface{}
	err = database.QueryRow(`
		SELECT deployed FROM installed_mods LIMIT 1
	`).Scan(&deployed)
	// This should not error on column not found - only on no rows
	assert.ErrorContains(t, err, "no rows")
}

func TestMigrationV6_ChecksumColumn(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	// Verify checksum column exists by querying it
	var checksum interface{}
	err = database.QueryRow(`
		SELECT checksum FROM installed_mod_files LIMIT 1
	`).Scan(&checksum)
	// This should not error on column not found - only on no rows
	assert.ErrorContains(t, err, "no rows")
}
