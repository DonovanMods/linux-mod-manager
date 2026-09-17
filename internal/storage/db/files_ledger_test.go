package db_test

import (
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNew_V18AddsTheLedgerColumnsToAV17Database pins migrateV18 (#466,
// #451) on a v17 database: its deployed_files rows survive the upgrade
// with no fingerprint and no recorded mod_path - nothing is backfilled -
// and the next record of a path fills both in.
func TestNew_V18AddsTheLedgerColumnsToAV17Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lmm.db")
	ctx := t.Context()

	// A v17 database: the current schema with v18's columns taken away
	// and its record of v18 gone.
	seed, err := db.New(path)
	require.NoError(t, err)
	for _, col := range []string{"checksum", "size", "mtime", "ctime", "mod_path"} {
		_, err = seed.Exec("ALTER TABLE deployed_files DROP COLUMN " + col)
		require.NoError(t, err)
	}
	_, err = seed.Exec("DELETE FROM schema_migrations WHERE version >= 18")
	require.NoError(t, err)
	_, err = seed.Exec(`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id)
		VALUES ('g', 'default', 'a.pak', 'src', 'm')`)
	require.NoError(t, err)
	require.NoError(t, seed.Close())

	upgraded, err := db.New(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, upgraded.Close()) })

	var version int
	require.NoError(t, upgraded.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.Equal(t, 18, version)

	states, err := upgraded.DeployedFileStates(ctx, "g", "a.pak")
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Nil(t, states[0].Fingerprint, "an existing row is not backfilled")
	assert.Empty(t, states[0].ModPath)
	assert.Equal(t, "m", states[0].ModID)

	require.NoError(t, upgraded.RecordDeployedFile(ctx, db.DeployedFileRecord{
		GameID: "g", Profile: "default", RelativePath: "a.pak", SourceID: "src", ModID: "m",
		ModPath:     "/games/g/mods",
		Fingerprint: &db.FileFingerprint{Checksum: "abc", Size: 3, MTime: 42, CTime: 43},
	}))
	states, err = upgraded.DeployedFileStates(ctx, "g", "a.pak")
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, &db.FileFingerprint{Checksum: "abc", Size: 3, MTime: 42, CTime: 43}, states[0].Fingerprint)
	assert.Equal(t, "/games/g/mods", states[0].ModPath)
}

// TestNew_V18RerunsHarmlessly: a database whose record of v18 is lost
// still opens, with its columns and values intact.
func TestNew_V18RerunsHarmlessly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lmm.db")
	ctx := t.Context()
	seed, err := db.New(path)
	require.NoError(t, err)
	require.NoError(t, seed.RecordDeployedFile(ctx, db.DeployedFileRecord{
		GameID: "g", Profile: "p", RelativePath: "x", SourceID: "s", ModID: "m",
		Fingerprint: &db.FileFingerprint{Checksum: "ff", Size: 1, MTime: 2},
	}))
	_, err = seed.Exec("DELETE FROM schema_migrations WHERE version >= 18")
	require.NoError(t, err)
	require.NoError(t, seed.Close())

	again, err := db.New(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, again.Close()) })
	states, err := again.DeployedFileStates(ctx, "g", "x")
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, "ff", states[0].Fingerprint.Checksum)
}

// TestRecordDeployedFile_AnUpsertReplacesTheFingerprintAndRoot: a path
// that changes hands takes the new record's fingerprint and mod_path, and
// a record carrying none clears the old ones - a symlink deployed over a
// copy is not the copy's content.
func TestRecordDeployedFile_AnUpsertReplacesTheFingerprintAndRoot(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	ctx := t.Context()

	require.NoError(t, database.RecordDeployedFile(ctx, db.DeployedFileRecord{
		GameID: "g", Profile: "p", RelativePath: "x", SourceID: "s", ModID: "a",
		ModPath: "/old", Fingerprint: &db.FileFingerprint{Checksum: "aa", Size: 1, MTime: 1},
	}))
	require.NoError(t, database.SaveDeployedFile(ctx, "g", "p", "x", "s", "b"))

	states, err := database.DeployedFileStates(ctx, "g", "x")
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, "b", states[0].ModID)
	assert.Nil(t, states[0].Fingerprint)
	assert.Empty(t, states[0].ModPath)
	assert.False(t, states[0].DeployedAt.IsZero())

	// An empty checksum is no fingerprint.
	require.NoError(t, database.RecordDeployedFile(ctx, db.DeployedFileRecord{
		GameID: "g", Profile: "p", RelativePath: "x", SourceID: "s", ModID: "b",
		Fingerprint: &db.FileFingerprint{Size: 9, MTime: 9},
	}))
	states, err = database.DeployedFileStates(ctx, "g", "x")
	require.NoError(t, err)
	assert.Nil(t, states[0].Fingerprint)
}

// TestDeployedFileStates_EveryProfilesRecord: the states of one path come
// back for every profile of its game, and no other game's.
func TestDeployedFileStates_EveryProfilesRecord(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	ctx := t.Context()

	for _, rec := range []db.DeployedFileRecord{
		{GameID: "g", Profile: "b", RelativePath: "x", SourceID: "s", ModID: "m", Fingerprint: &db.FileFingerprint{Checksum: "bb"}},
		{GameID: "g", Profile: "a", RelativePath: "x", SourceID: "s", ModID: "m"},
		{GameID: "g", Profile: "a", RelativePath: "y", SourceID: "s", ModID: "m"},
		{GameID: "h", Profile: "a", RelativePath: "x", SourceID: "s", ModID: "m"},
	} {
		require.NoError(t, database.RecordDeployedFile(ctx, rec))
	}
	states, err := database.DeployedFileStates(ctx, "g", "x")
	require.NoError(t, err)
	require.Len(t, states, 2)
	assert.Equal(t, "a", states[0].Profile)
	assert.Nil(t, states[0].Fingerprint)
	assert.Equal(t, "b", states[1].Profile)
	assert.Equal(t, "bb", states[1].Fingerprint.Checksum)

	none, err := database.DeployedFileStates(ctx, "g", "z")
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestDeployedFileRoots_CountsPerProfileAndRecordedModPath (#451).
func TestDeployedFileRoots_CountsPerProfileAndRecordedModPath(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	ctx := t.Context()

	for _, rec := range []db.DeployedFileRecord{
		{GameID: "g", Profile: "p", RelativePath: "1", SourceID: "s", ModID: "m", ModPath: "/old"},
		{GameID: "g", Profile: "p", RelativePath: "2", SourceID: "s", ModID: "m", ModPath: "/old"},
		{GameID: "g", Profile: "p", RelativePath: "3", SourceID: "s", ModID: "m"},
		{GameID: "g", Profile: "q", RelativePath: "1", SourceID: "s", ModID: "m", ModPath: "/new"},
		{GameID: "h", Profile: "p", RelativePath: "1", SourceID: "s", ModID: "m", ModPath: "/old"},
	} {
		require.NoError(t, database.RecordDeployedFile(ctx, rec))
	}
	roots, err := database.DeployedFileRoots(ctx, "g")
	require.NoError(t, err)
	assert.Equal(t, []db.DeployedRoot{
		{Profile: "p", ModPath: "", Files: 1},
		{Profile: "p", ModPath: "/old", Files: 2},
		{Profile: "q", ModPath: "/new", Files: 1},
	}, roots)

	files, err := database.ListDeployedFiles(ctx, "g", "p")
	require.NoError(t, err)
	require.Len(t, files, 3)
	assert.Equal(t, "/old", files[0].ModPath)
	assert.Empty(t, files[2].ModPath)
}

// TestDeleteDeployedFilesExcept_KeepsRowsUnderTheNamedRoots (#451).
func TestDeleteDeployedFilesExcept_KeepsRowsUnderTheNamedRoots(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	ctx := t.Context()

	for _, rec := range []db.DeployedFileRecord{
		{GameID: "g", Profile: "p", RelativePath: "legacy", SourceID: "s", ModID: "m"},
		{GameID: "g", Profile: "p", RelativePath: "here", SourceID: "s", ModID: "m", ModPath: "/new"},
		{GameID: "g", Profile: "p", RelativePath: "there", SourceID: "s", ModID: "m", ModPath: "/old"},
		{GameID: "g", Profile: "p", RelativePath: "other", SourceID: "s", ModID: "n", ModPath: "/new"},
	} {
		require.NoError(t, database.RecordDeployedFile(ctx, rec))
	}
	require.NoError(t, database.DeleteDeployedFilesExcept(ctx, "g", "p", "s", "m", []string{"/old"}, nil))
	files, err := database.ListDeployedFiles(ctx, "g", "p")
	require.NoError(t, err)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.RelativePath)
	}
	assert.Equal(t, []string{"other", "there"}, paths)

	require.NoError(t, database.DeleteDeployedFilesExcept(ctx, "g", "p", "s", "m", nil, nil))
	files, err = database.ListDeployedFiles(ctx, "g", "p")
	require.NoError(t, err)
	assert.Len(t, files, 1)
}

// TestDeleteDeployedFilesExcept_KeepsRowsForTheNamedPaths: a row for a
// file an uninstall could not judge stays exactly as it is (#466).
func TestDeleteDeployedFilesExcept_KeepsRowsForTheNamedPaths(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	ctx := t.Context()

	fp := &db.FileFingerprint{Checksum: "abc", Size: 3, MTime: 1, CTime: 2}
	for _, rec := range []db.DeployedFileRecord{
		{GameID: "g", Profile: "p", RelativePath: "a", SourceID: "s", ModID: "m", ModPath: "/new", Fingerprint: fp},
		{GameID: "g", Profile: "p", RelativePath: "b", SourceID: "s", ModID: "m", ModPath: "/new"},
		{GameID: "g", Profile: "p", RelativePath: "c", SourceID: "s", ModID: "m", ModPath: "/old"},
	} {
		require.NoError(t, database.RecordDeployedFile(ctx, rec))
	}
	require.NoError(t, database.DeleteDeployedFilesExcept(ctx, "g", "p", "s", "m", []string{"/old"}, []string{"a"}))

	records, err := database.DeployedFileRecordsForMod(ctx, "g", "p", "s", "m")
	require.NoError(t, err)
	assert.Equal(t, []db.DeployedFileRecord{
		{GameID: "g", Profile: "p", RelativePath: "a", SourceID: "s", ModID: "m", ModPath: "/new", Fingerprint: fp},
		{GameID: "g", Profile: "p", RelativePath: "c", SourceID: "s", ModID: "m", ModPath: "/old"},
	}, records)
}
