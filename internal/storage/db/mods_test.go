package db_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installTestMod is a small helper mirroring the SaveInstalledMod usage
// already established in db_test.go, so each mutation test only has to state
// what differs from the default mod.
func installTestMod(t *testing.T, database *db.DB) {
	t.Helper()
	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "12345",
			SourceID: "nexusmods",
			Name:     "Test Mod",
			Version:  "1.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}
	require.NoError(t, database.SaveInstalledMod(mod))
}

func TestUpdateModPolicy(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	installTestMod(t, database)

	err = database.UpdateModPolicy("nexusmods", "12345", "skyrim-se", "default", domain.UpdateAuto)
	require.NoError(t, err)

	retrieved, err := database.GetInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, retrieved.UpdatePolicy)
}

func TestUpdateModPolicy_NotFound(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	err = database.UpdateModPolicy("nexusmods", "nonexistent", "skyrim-se", "default", domain.UpdateAuto)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

func TestSetModEnabled(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	installTestMod(t, database)

	err = database.SetModEnabled("nexusmods", "12345", "skyrim-se", "default", false)
	require.NoError(t, err)

	retrieved, err := database.GetInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.False(t, retrieved.Enabled)

	err = database.SetModEnabled("nexusmods", "12345", "skyrim-se", "default", true)
	require.NoError(t, err)

	retrieved, err = database.GetInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, retrieved.Enabled)
}

func TestSetModEnabled_NotFound(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	err = database.SetModEnabled("nexusmods", "nonexistent", "skyrim-se", "default", false)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

func TestSetModLinkMethod(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	installTestMod(t, database)

	err = database.SetModLinkMethod("nexusmods", "12345", "skyrim-se", "default", domain.LinkHardlink)
	require.NoError(t, err)

	retrieved, err := database.GetInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, domain.LinkHardlink, retrieved.LinkMethod)
}

func TestSetModLinkMethod_NotFound(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	err = database.SetModLinkMethod("nexusmods", "nonexistent", "skyrim-se", "default", domain.LinkCopy)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

func TestSetModFileIDs(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	installTestMod(t, database)
	require.NoError(t, database.SetModFileIDs("nexusmods", "12345", "skyrim-se", "default", []string{"111", "222"}))

	fileIDs, err := database.GetModFileIDs("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"111", "222"}, fileIDs)

	// A second call must replace, not append.
	require.NoError(t, database.SetModFileIDs("nexusmods", "12345", "skyrim-se", "default", []string{"333"}))

	fileIDs, err = database.GetModFileIDs("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"333"}, fileIDs)
}

// TestSetModFileIDs_EmptyOnMissingModIsANoOp pins a surprising-but-real
// behavior: unlike UpdateModPolicy/SetModEnabled/SetModLinkMethod,
// SetModFileIDs has no "mod must exist" check of its own. It DELETEs any
// existing file rows (matching zero rows is not an error) and then INSERTs
// one row per file ID. With an empty slice there is nothing to insert, so the
// transaction commits successfully even though the mod was never installed.
func TestSetModFileIDs_EmptyOnMissingModIsANoOp(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	err = database.SetModFileIDs("nexusmods", "nonexistent", "skyrim-se", "default", []string{})
	assert.NoError(t, err)
}

// TestSetModFileIDs_NonEmptyOnMissingModFailsForeignKey pins the other half
// of that same gap: a non-empty file ID list on a mod that doesn't exist
// hits the installed_mod_files -> installed_mods foreign key and fails, but
// with a raw wrapped SQLite constraint error rather than domain.ErrModNotFound.
func TestSetModFileIDs_NonEmptyOnMissingModFailsForeignKey(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	err = database.SetModFileIDs("nexusmods", "nonexistent", "skyrim-se", "default", []string{"111"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrModNotFound, "the error is a raw FK-constraint failure, not the usual not-found sentinel")
	assert.Contains(t, err.Error(), "FOREIGN KEY")
}

// TestSetModFileIDs_SkipsEmptyStringIDs pins that replaceModFileIDsTx filters
// out blank file IDs rather than storing them.
func TestSetModFileIDs_SkipsEmptyStringIDs(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	installTestMod(t, database)
	require.NoError(t, database.SetModFileIDs("nexusmods", "12345", "skyrim-se", "default", []string{"111", "", "222"}))

	fileIDs, err := database.GetModFileIDs("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"111", "222"}, fileIDs)
}
