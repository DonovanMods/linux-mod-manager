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

// Note: the result.RowsAffected() error-classification branch shared by
// DeleteInstalledMod, UpdateModPolicy, SetModEnabled, SetModDeployed,
// UpdateModVersion, SetModVersion, and SetModLinkMethod (a driver error
// there must be returned, not misread as "0 rows affected" ->
// domain.ErrModNotFound) is
// not covered by a dedicated test. modernc.org/sqlite's
// Result.RowsAffected() is a locally-computed value from the just-executed
// statement, not a fresh round-trip that can fail independently of Exec's
// own error return (already checked above it) - there's no realistic way to
// make it error with the standard driver without a custom sql.Driver mock,
// which isn't otherwise justified here. Verified by inspection: each site
// wraps and returns the error with operation context instead of discarding
// it via `_`.

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

// TestSetModVersion guards the epic98 audit's Finding 1 (Critical):
// SaveInstalledMod's full-row upsert always replaces installed_mod_files
// via replaceModFileIDsTx (DELETE + re-INSERT with only file_id, no
// checksum column), so ANY SaveInstalledMod call - even one that only
// intends to correct the version string, with FileIDs completely unchanged
// - silently wipes every stored checksum for that mod. SetModVersion is a
// plain UPDATE of the version column alone: no file-row replacement (so
// stored checksums survive), and no PreviousVersion/PreviousFileIDs shift
// either (unlike UpdateModVersion, which is for real version bumps that
// SHOULD be rollback-able - a version-record *correction* isn't one).
func TestSetModVersion(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "12345",
			SourceID: "nexusmods",
			Name:     "Test Mod",
			Version:  "1.5",
			GameID:   "skyrim-se",
		},
		ProfileName: "default",
		Enabled:     true,
		FileIDs:     []string{"111"},
	}
	require.NoError(t, database.SaveInstalledMod(mod))
	require.NoError(t, database.SaveFileChecksum("nexusmods", "12345", "skyrim-se", "default", "111", "deadbeef"))

	err = database.SetModVersion("nexusmods", "12345", "skyrim-se", "default", "1.0")
	require.NoError(t, err)

	retrieved, err := database.GetInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", retrieved.Version, "version must be corrected")
	assert.Empty(t, retrieved.PreviousVersion, "SetModVersion must not shift PreviousVersion - unlike UpdateModVersion, this is a correction, not an update with a rollback path")
	assert.Equal(t, []string{"111"}, retrieved.FileIDs, "FileIDs must be untouched")

	files, err := database.GetFilesWithChecksums("skyrim-se", "default")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "deadbeef", files[0].Checksum, "SetModVersion must NOT wipe the stored checksum - unlike SaveInstalledMod's full-row upsert, which replaces installed_mod_files and loses it")
}

func TestSetModVersion_NotFound(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })

	err = database.SetModVersion("nexusmods", "nonexistent", "skyrim-se", "default", "1.0")
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
