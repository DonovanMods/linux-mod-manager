package db_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_CreatesDatabase(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	assert.NotNil(t, database)
}

func TestNew_RunsMigrations(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	// Verify v1 tables exist
	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM installed_mods").Scan(&count)
	assert.NoError(t, err)

	err = database.QueryRow("SELECT COUNT(*) FROM auth_tokens").Scan(&count)
	assert.NoError(t, err)
}

// TestNew_DropsModCacheTable pins the removal of the mod_cache table. It was
// created in v1 and never read or written — caching is keyed by directory layout
// in internal/storage/cache, with no DB mirror.
func TestNew_DropsModCacheTable(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'mod_cache'").Scan(&count)
	require.NoError(t, err)
	assert.Zero(t, count, "mod_cache should have been dropped")
}

// TestNew_DropsModCacheTableOnUpgrade covers the upgrade path rather than a fresh
// install: a database left at v10 still has the table and must lose it on open.
func TestNew_DropsModCacheTableOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lmm.db")

	database, err := db.New(path)
	require.NoError(t, err)

	// Rewind to v10 by reverting every post-v10 schema change. v11 dropped
	// mod_cache; v12 added convert_paks; v13 added auth_tokens.created_at;
	// v15 added installed_mods.external and external_path; v16 added
	// installed_mods.updated_at.
	for _, col := range []string{"convert_paks", "external", "external_path", "updated_at"} {
		_, err = database.Exec("ALTER TABLE installed_mods DROP COLUMN " + col)
		require.NoError(t, err, "revert post-v10 installed_mods change before rewinding version tracker")
	}
	_, err = database.Exec("ALTER TABLE auth_tokens DROP COLUMN created_at")
	require.NoError(t, err, "revert v13 schema change before rewinding version tracker")
	_, err = database.Exec("DELETE FROM schema_migrations WHERE version >= 11")
	require.NoError(t, err)
	_, err = database.Exec(`CREATE TABLE mod_cache (
		source_id TEXT NOT NULL,
		mod_id TEXT NOT NULL,
		game_id TEXT NOT NULL,
		metadata TEXT,
		cached_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY(source_id, mod_id, game_id)
	)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	reopened, err := db.New(path)
	require.NoError(t, err)
	defer func() { _ = reopened.Close() }()

	var count int
	err = reopened.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'mod_cache'").Scan(&count)
	require.NoError(t, err)
	assert.Zero(t, count, "upgrading from v10 should drop mod_cache")
}

// TestNew_AppliesAllMigrations verifies migrations v4–v7 are applied (installed_mod_files, deployed, checksum, deployed_files).
func TestNew_AppliesAllMigrations(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	// v4: installed_mod_files
	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM installed_mod_files").Scan(&count)
	assert.NoError(t, err)

	// v5: deployed column on installed_mods
	_, err = database.Exec("SELECT deployed FROM installed_mods LIMIT 1")
	assert.NoError(t, err)

	// v6: checksum column on installed_mod_files
	_, err = database.Exec("SELECT checksum FROM installed_mod_files LIMIT 1")
	assert.NoError(t, err)

	// v7: deployed_files table
	err = database.QueryRow("SELECT COUNT(*) FROM deployed_files").Scan(&count)
	assert.NoError(t, err)

	// schema_migrations should record current version
	var version int
	err = database.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, version, 7, "schema_migrations should have at least version 7")
}

// TestNew_RestrictsFilePermissions pins that the database is owner-only. It holds
// auth tokens in plaintext (#79), and SQLite creates it 0644 under a typical umask.
func TestNew_RestrictsFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lmm.db")

	database, err := db.New(path)
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0600), info.Mode().Perm(), "database must not be group- or world-readable")

	// WAL mode is on, so the sidecars carry the same token bytes. They are
	// required to exist rather than skipped-if-absent: migrations write, which
	// creates them, and a skip would make this assertion vacuous.
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar := path + suffix
		info, err := os.Stat(sidecar)
		require.NoError(t, err, "%s should exist once migrations have written", sidecar)
		assert.Equal(t, fs.FileMode(0600), info.Mode().Perm(), "%s must not be group- or world-readable", sidecar)
	}
}

// TestNew_TightensExistingPermissions covers installs predating the fix: an already
// world-readable database must be tightened on open, not just on creation.
func TestNew_TightensExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lmm.db")

	database, err := db.New(path)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	require.NoError(t, os.Chmod(path, 0644))

	reopened, err := db.New(path)
	require.NoError(t, err)
	defer func() { _ = reopened.Close() }()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0600), info.Mode().Perm(), "existing permissive database should be tightened on open")
}

// TestNew_BadPath pins the error path taken when the database file can't be
// opened at all (parent directory doesn't exist): the modernc.org/sqlite
// driver defers the actual open past sql.Open, so New pings the connection
// explicitly to surface the failure there rather than on the first query.
func TestNew_BadPath(t *testing.T) {
	_, err := db.New("/nonexistent-root-dir-xyz/sub/lmm.db")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "opening database")
}

func TestInstalledMods_SaveAndGet(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

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

	err = database.SaveInstalledMod(context.Background(), mod)
	require.NoError(t, err)

	retrieved, err := database.GetInstalledMods(context.Background(), "skyrim-se", "default")
	require.NoError(t, err)
	require.Len(t, retrieved, 1)

	assert.Equal(t, mod.ID, retrieved[0].ID)
	assert.Equal(t, mod.Name, retrieved[0].Name)
	assert.Equal(t, mod.Version, retrieved[0].Version)
}

func TestInstalledMods_Delete(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

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

	err = database.SaveInstalledMod(context.Background(), mod)
	require.NoError(t, err)

	err = database.DeleteInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)

	mods, err := database.GetInstalledMods(context.Background(), "skyrim-se", "default")
	require.NoError(t, err)
	assert.Empty(t, mods)
}

func TestMigrationV2_PreviousVersionColumn(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

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
	defer func() { _ = database.Close() }()

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
	err = database.SaveInstalledMod(context.Background(), mod)
	require.NoError(t, err)

	// Update version
	err = database.UpdateModVersion(context.Background(), "nexusmods", "12345", "skyrim-se", "default", "2.0.0")
	require.NoError(t, err)

	// Retrieve and verify
	retrieved, err := database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", retrieved.Version)
	assert.Equal(t, "1.0.0", retrieved.PreviousVersion)
}

func TestSwapModVersions(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

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
	err = database.SaveInstalledMod(context.Background(), mod)
	require.NoError(t, err)

	// Swap versions (rollback)
	err = database.SwapModVersions(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)

	// Verify swap
	retrieved, err := database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", retrieved.Version)
	assert.Equal(t, "2.0.0", retrieved.PreviousVersion)
}

func TestApplyModUpdateAndRollbackRestoresFileIDs(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "12345",
			SourceID: "nexusmods",
			Name:     "Test Mod",
			Version:  "1.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName: "default",
		FileIDs:     []string{"old-main", "old-patch"},
	}
	require.NoError(t, database.SaveInstalledMod(context.Background(), mod))

	require.NoError(t, database.ApplyModUpdate(context.Background(), "nexusmods", "12345", "skyrim-se", "default", "2.0.0", []string{"new-main"}))

	updated, err := database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0.0", updated.Version)
	assert.Equal(t, "1.0.0", updated.PreviousVersion)
	assert.Equal(t, []string{"new-main"}, updated.FileIDs)
	assert.Equal(t, []string{"old-main", "old-patch"}, updated.PreviousFileIDs)

	require.NoError(t, database.SwapModVersions(context.Background(), "nexusmods", "12345", "skyrim-se", "default"))

	rolledBack, err := database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", rolledBack.Version)
	assert.Equal(t, "2.0.0", rolledBack.PreviousVersion)
	assert.Equal(t, []string{"old-main", "old-patch"}, rolledBack.FileIDs)
	assert.Equal(t, []string{"new-main"}, rolledBack.PreviousFileIDs)
}

func TestSwapModVersions_NoPreviousVersion(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

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
	err = database.SaveInstalledMod(context.Background(), mod)
	require.NoError(t, err)

	// Swap should fail - no previous version
	err = database.SwapModVersions(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	assert.Error(t, err)
}

// TestOpen_LogsRunningMigrationsOnlyWhenMigrationsRun pins review Minor 5:
// "running migrations" is Debug-level progress noise about the loop that
// follows it, so it must only fire when that loop will actually execute -
// not unconditionally on every open, including the steady state where the
// database is already at the current schema version.
func TestOpen_LogsRunningMigrationsOnlyWhenMigrationsRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lmm.db")
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	database, err := db.Open(path, log)
	require.NoError(t, err)
	require.NoError(t, database.Close())
	assert.Contains(t, buf.String(), "running migrations", "a fresh database has migrations to run and must log it")

	buf.Reset()
	reopened, err := db.Open(path, log)
	require.NoError(t, err)
	defer func() { _ = reopened.Close() }()
	assert.NotContains(t, buf.String(), "running migrations", "an already-migrated database has nothing to run and must not log it")
}

func TestGetInstalledMod(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

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
	err = database.SaveInstalledMod(context.Background(), mod)
	require.NoError(t, err)

	retrieved, err := database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.Equal(t, mod.ID, retrieved.ID)
	assert.Equal(t, mod.Name, retrieved.Name)
	assert.Equal(t, mod.UpdatePolicy, retrieved.UpdatePolicy)
}

func TestGetInstalledMod_NotFound(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	_, err = database.GetInstalledMod(context.Background(), "nexusmods", "nonexistent", "skyrim-se", "default")
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

func TestSetModDeployed(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

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
	err = database.SaveInstalledMod(context.Background(), mod)
	require.NoError(t, err)

	// Verify initial deployed state
	retrieved, err := database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, retrieved.Deployed)

	// Set deployed to false (purge scenario)
	err = database.SetModDeployed(context.Background(), "nexusmods", "12345", "skyrim-se", "default", false)
	require.NoError(t, err)

	// Verify deployed is now false but enabled unchanged
	retrieved, err = database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.False(t, retrieved.Deployed)
	assert.True(t, retrieved.Enabled) // Enabled should remain true

	// Set deployed back to true (deploy scenario)
	err = database.SetModDeployed(context.Background(), "nexusmods", "12345", "skyrim-se", "default", true)
	require.NoError(t, err)

	retrieved, err = database.GetInstalledMod(context.Background(), "nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, retrieved.Deployed)
}

func TestSetModDeployed_NotFound(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	err = database.SetModDeployed(context.Background(), "nexusmods", "nonexistent", "skyrim-se", "default", false)
	assert.ErrorIs(t, err, domain.ErrModNotFound)
}

func TestMigrationV5_DeployedColumn(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

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
	defer func() { _ = database.Close() }()

	// Verify checksum column exists by querying it
	var checksum interface{}
	err = database.QueryRow(`
		SELECT checksum FROM installed_mod_files LIMIT 1
	`).Scan(&checksum)
	// This should not error on column not found - only on no rows
	assert.ErrorContains(t, err, "no rows")
}

func TestSaveFileChecksum(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	// Create a mod first
	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "12345",
			SourceID: "nexusmods",
			Name:     "Test Mod",
			Version:  "1.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName: "default",
		FileIDs:     []string{"67890"},
	}
	err = database.SaveInstalledMod(context.Background(), mod)
	require.NoError(t, err)

	// Save checksum
	err = database.SaveFileChecksum(context.Background(), "nexusmods", "12345", "skyrim-se", "default", "67890", "a1b2c3d4e5f6")
	require.NoError(t, err)

	// Retrieve checksum
	checksum, err := database.GetFileChecksum(context.Background(), "nexusmods", "12345", "skyrim-se", "default", "67890")
	require.NoError(t, err)
	assert.Equal(t, "a1b2c3d4e5f6", checksum)

	// Re-saving the SAME value must stay a success: SQLite's changes()
	// counts rows matched by the UPDATE even when the new value equals the
	// old (unlike MySQL), so the RowsAffected guard added for #164 must not
	// misread an idempotent re-save as "row missing".
	err = database.SaveFileChecksum(context.Background(), "nexusmods", "12345", "skyrim-se", "default", "67890", "a1b2c3d4e5f6")
	require.NoError(t, err, "idempotent same-value checksum re-save must not trip the 0-rows guard")
}

// TestSaveFileChecksum_NoMatchingRow_ReturnsError guards the latent defect
// from #164: SaveFileChecksum is an UPDATE, and with no RowsAffected check a
// write against a row that doesn't exist silently no-ops - the caller
// believes the checksum was persisted when nothing happened.
func TestSaveFileChecksum_NoMatchingRow_ReturnsError(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	err = database.SaveFileChecksum(context.Background(), "nexusmods", "nonexistent", "skyrim-se", "default", "99999", "a1b2c3d4e5f6")
	require.Error(t, err, "updating a nonexistent installed_mod_files row must fail loudly, not silently no-op")
	assert.Contains(t, err.Error(), "no installed file row")
}

func TestGetFileChecksum_NotFound(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	checksum, err := database.GetFileChecksum(context.Background(), "nexusmods", "nonexistent", "skyrim-se", "default", "99999")
	require.NoError(t, err)
	assert.Equal(t, "", checksum) // Empty string for missing checksum
}

func TestGetFilesWithChecksums(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	// Create a mod with multiple files
	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "12345",
			SourceID: "nexusmods",
			Name:     "Test Mod",
			Version:  "1.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName: "default",
		FileIDs:     []string{"111", "222"},
	}
	err = database.SaveInstalledMod(context.Background(), mod)
	require.NoError(t, err)

	// Save checksums
	err = database.SaveFileChecksum(context.Background(), "nexusmods", "12345", "skyrim-se", "default", "111", "hash111")
	require.NoError(t, err)
	err = database.SaveFileChecksum(context.Background(), "nexusmods", "12345", "skyrim-se", "default", "222", "hash222")
	require.NoError(t, err)

	// Retrieve all files with checksums
	files, err := database.GetFilesWithChecksums(context.Background(), "skyrim-se", "default")
	require.NoError(t, err)
	require.Len(t, files, 2)

	// Verify both files have checksums
	checksumMap := make(map[string]string)
	for _, f := range files {
		checksumMap[f.FileID] = f.Checksum
	}
	assert.Equal(t, "hash111", checksumMap["111"])
	assert.Equal(t, "hash222", checksumMap["222"])
}

func TestMigrationV7_DeployedFilesTable(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	// Verify deployed_files table exists
	var tableName string
	err = database.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='deployed_files'
	`).Scan(&tableName)
	require.NoError(t, err)
	assert.Equal(t, "deployed_files", tableName)

	// Verify we can insert and query
	_, err = database.Exec(`
		INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id)
		VALUES ('skyrim-se', 'default', 'meshes/test.nif', 'nexusmods', '12345')
	`)
	require.NoError(t, err)

	var path, sourceID, modID string
	err = database.QueryRow(`
		SELECT relative_path, source_id, mod_id FROM deployed_files
		WHERE game_id = 'skyrim-se' AND profile_name = 'default'
	`).Scan(&path, &sourceID, &modID)
	require.NoError(t, err)
	assert.Equal(t, "meshes/test.nif", path)
	assert.Equal(t, "nexusmods", sourceID)
	assert.Equal(t, "12345", modID)
}

// TestNew_OwesTheProfileBackfillOnlyForADisabledUndeployedRow pins
// migrateV17 (#431 fix round 2). The one-time profile-document backfill is
// recorded as OWED by the migration, when a database written by an older lmm
// already holds a row it might act on - a managed row that says both
// enabled = 0 and deployed = 0. Every other database never owes it: a fresh
// one has no rows, and a row a profile switch left at (0, 1) is not one the
// backfill will ever mark. Deciding it HERE, before any flow of the new
// binary can write a row, is what keeps those flows from manufacturing
// evidence the backfill would then misread.
func TestNew_OwesTheProfileBackfillOnlyForADisabledUndeployedRow(t *testing.T) {
	type row struct{ enabled, deployed, external bool }
	tests := []struct {
		name string
		rows []row
		owed bool
	}{
		{name: "no rows"},
		{name: "enabled rows only", rows: []row{{true, true, false}, {true, false, false}}},
		{name: "a switched-away row", rows: []row{{false, true, false}}},
		{name: "an external disabled row", rows: []row{{false, false, true}}},
		{name: "a disabled, undeployed managed row", rows: []row{{true, true, false}, {false, false, false}}, owed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lmm.db")
			ctx := t.Context()

			fresh, err := db.New(path)
			require.NoError(t, err)
			value, err := fresh.GetMeta(ctx, db.MetaProfileDisabledBackfill)
			require.NoError(t, err)
			require.Empty(t, value, "a fresh database never owes the backfill")
			require.False(t, fresh.OwesProfileBackfill())

			// An older lmm's database: the rows exist before v17 runs.
			for i, r := range tt.rows {
				require.NoError(t, fresh.SaveInstalledMod(ctx, &domain.InstalledMod{
					Mod:          domain.Mod{ID: fmt.Sprintf("m%d", i), SourceID: "src", Name: "M", Version: "1", GameID: "g"},
					ProfileName:  "default",
					UpdatePolicy: domain.UpdateNotify,
					Enabled:      r.enabled,
					Deployed:     r.deployed,
					External:     r.external,
				}))
			}
			_, err = fresh.Exec("DELETE FROM schema_migrations WHERE version >= 17")
			require.NoError(t, err)
			require.NoError(t, fresh.Close())

			upgraded, err := db.New(path)
			require.NoError(t, err)
			defer func() { require.NoError(t, upgraded.Close()) }()
			value, err = upgraded.GetMeta(ctx, db.MetaProfileDisabledBackfill)
			require.NoError(t, err)
			assert.Equal(t, tt.owed, value != "")
			assert.Equal(t, tt.owed, upgraded.OwesProfileBackfill(), "and the handle says so, read at open")
		})
	}
}

// TestNew_V17DropsRoundOnesBackfillKey (fix round 3, F6): a database a
// fix-round-1 development build touched carries its `_done` key into v17,
// which deletes it - so it no longer keeps the backfill looking owed.
func TestNew_V17DropsRoundOnesBackfillKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lmm.db")
	ctx := t.Context()
	seed, err := db.New(path)
	require.NoError(t, err)
	require.NoError(t, seed.SetMeta(ctx, db.MetaProfileDisabledBackfillLegacy, "2026-09-14T00:00:00Z"))
	_, err = seed.Exec("DELETE FROM schema_migrations WHERE version >= 17")
	require.NoError(t, err)
	require.NoError(t, seed.Close())

	upgraded, err := db.New(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, upgraded.Close()) }()
	keys, err := upgraded.MetaWithPrefix(ctx, db.MetaProfileDisabledBackfill)
	require.NoError(t, err)
	assert.Empty(t, keys)
	assert.False(t, upgraded.OwesProfileBackfill())
}

// TestOpen_ConcurrentFirstOpensMigrateExactlyOnce: several lmm processes
// opening an older database at once - `lmm serve` started beside a CLI
// command right after an upgrade. Each read the schema version, ran the
// pending migrations, and all but one then failed to record them
// (UNIQUE constraint failed: schema_migrations.version), refusing to start.
// Worse for migrateV17, a migration run that lands AFTER another process
// has already discharged the obligation it records would record it again.
// Pending migrations therefore run under one write transaction that
// re-reads the version first: every open succeeds, and each migration runs
// once.
//
// A brand-new database file is the same race one step earlier: the DSN's
// journal_mode(WAL) pragma needs the write lock the first time, and it used
// to run before busy_timeout(5000) was set, so a second opener got
// "database is locked" at once instead of waiting its turn.
func TestOpen_ConcurrentFirstOpensMigrateExactlyOnce(t *testing.T) {
	openAll := func(t *testing.T, path string, round int) {
		t.Helper()
		const openers = 8
		start := make(chan struct{})
		errs := make(chan error, openers)
		for range openers {
			go func() {
				<-start
				opened, err := db.New(path)
				if err == nil {
					err = opened.Close()
				}
				errs <- err
			}()
		}
		close(start)
		for range openers {
			require.NoError(t, <-errs, "round %d: every concurrent open must succeed", round)
		}
	}

	t.Run("a brand-new file", func(t *testing.T) {
		for round := range 20 {
			path := filepath.Join(t.TempDir(), "lmm.db")
			openAll(t, path, round)

			check, err := db.New(path)
			require.NoError(t, err)
			var recorded int
			require.NoError(t, check.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&recorded))
			assert.Equal(t, 18, recorded)
			require.NoError(t, check.Close())
		}
	})

	for round := range 5 {
		path := filepath.Join(t.TempDir(), "lmm.db")
		ctx := t.Context()
		seed, err := db.New(path)
		require.NoError(t, err)
		require.NoError(t, seed.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:          domain.Mod{ID: "off", SourceID: "src", Name: "Off", Version: "1", GameID: "g"},
			ProfileName:  "default",
			UpdatePolicy: domain.UpdateNotify,
		}))
		_, err = seed.Exec("DELETE FROM schema_migrations WHERE version >= 17")
		require.NoError(t, err)
		require.NoError(t, seed.Close())

		openAll(t, path, round)

		check, err := db.New(path)
		require.NoError(t, err)
		var recorded int
		require.NoError(t, check.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = 17").Scan(&recorded))
		assert.Equal(t, 1, recorded)
		value, err := check.GetMeta(ctx, db.MetaProfileDisabledBackfill)
		require.NoError(t, err)
		assert.NotEmpty(t, value)
		require.NoError(t, check.Close())
	}
}

// TestOpen_AMigrationRunsOnceEvenAfterItsWorkIsUndone pins the
// exactly-once half directly: once v17 is recorded, a later open never runs
// it again, so an obligation core has discharged stays discharged.
func TestOpen_AMigrationRunsOnceEvenAfterItsWorkIsUndone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lmm.db")
	ctx := t.Context()
	seed, err := db.New(path)
	require.NoError(t, err)
	require.NoError(t, seed.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: "off", SourceID: "src", Name: "Off", Version: "1", GameID: "g"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
	}))
	_, err = seed.Exec("DELETE FROM schema_migrations WHERE version >= 17")
	require.NoError(t, err)
	require.NoError(t, seed.Close())

	first, err := db.New(path)
	require.NoError(t, err)
	require.NoError(t, first.DeleteMeta(ctx, db.MetaProfileDisabledBackfill)) // core discharged it
	require.NoError(t, first.Close())

	again, err := db.New(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, again.Close()) }()
	value, err := again.GetMeta(ctx, db.MetaProfileDisabledBackfill)
	require.NoError(t, err)
	assert.Empty(t, value)
}
