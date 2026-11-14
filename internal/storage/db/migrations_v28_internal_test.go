package db

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrateV28_WidensTheRootCheck covers #636's migration: a V27
// deployed_files table, whose root CHECK knows two roots, is rebuilt to
// accept 'prefix' with every row and column intact, and a re-run is a
// no-op.
func TestMigrateV28_WidensTheRootCheck(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := context.Background()

	// Back to V27: the table as migrateV26 left it.
	for _, stmt := range []string{
		`DROP TABLE deployed_files`,
		`CREATE TABLE deployed_files (
			game_id TEXT NOT NULL,
			profile_name TEXT NOT NULL,
			root TEXT NOT NULL DEFAULT 'mod_path' CHECK (root IN ('mod_path', 'install_path')),
			relative_path TEXT NOT NULL,
			source_id TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			deployed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			checksum TEXT, size INTEGER, mtime INTEGER, ctime INTEGER, mod_path TEXT,
			PRIMARY KEY (game_id, profile_name, root, relative_path)
		)`,
		`INSERT INTO deployed_files (game_id, profile_name, root, relative_path, source_id, mod_id, checksum, size, mtime, ctime, mod_path)
			VALUES ('g', 'default', 'install_path', 'd3d11.dll', 'src', 'm', 'abc', 3, 4, 5, '/games/g/Data')`,
		`DELETE FROM schema_migrations WHERE version >= 28`,
	} {
		_, err := d.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	_, err = d.Exec(`INSERT INTO deployed_files (game_id, profile_name, root, relative_path, source_id, mod_id) VALUES ('g', 'p', 'prefix', 'a', 's', 'm')`)
	require.Error(t, err, "V27 refuses the prefix root")

	require.NoError(t, d.migrate(ctx))
	var version int
	require.NoError(t, d.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.Equal(t, 28, version)

	recs, err := d.DeployedFileRecordsForMod(ctx, "g", "default", "src", "m")
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, domain.GameRootFile("d3d11.dll"), recs[0].Path)
	assert.Equal(t, "/games/g/Data", recs[0].ModPath)
	assert.Equal(t, &FileFingerprint{Checksum: "abc", Size: 3, MTime: 4, CTime: 5}, recs[0].Fingerprint)

	pak := domain.PrefixFile("AppData/Local/Larian Studios/Baldur's Gate 3/Mods/a.pak")
	require.NoError(t, d.RecordDeployedFile(ctx, DeployedFileRecord{GameID: "g", Profile: "default", Path: pak, SourceID: "src", ModID: "m"}))
	recs, err = d.DeployedFileRecordsForMod(ctx, "g", "default", "src", "m")
	require.NoError(t, err)
	require.Len(t, recs, 2)
	assert.Equal(t, pak, recs[1].Path, "a prefix file sorts after the game-root ones")

	require.NoError(t, migrateV28(ctx, d), "a re-run is a no-op")
	var n int
	require.NoError(t, d.QueryRow("SELECT COUNT(*) FROM deployed_files").Scan(&n))
	assert.Equal(t, 2, n)
	_, err = d.Exec(`INSERT INTO deployed_files (game_id, profile_name, root, relative_path, source_id, mod_id) VALUES ('g', 'p', 'modpath', 'a', 's', 'm')`)
	require.Error(t, err, "an unknown root is still refused")
}

// The conflict and delete queries name every root: a prefix path is found,
// and kept, by its own root.
func TestDeployedFiles_PrefixRootQueries(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := context.Background()
	pak := domain.PrefixFile("AppData/Local/L/Mods/a.pak")
	has, err := d.HasDeployedFilesUnder(ctx, "g", domain.RootPrefix)
	require.NoError(t, err)
	assert.False(t, has)
	require.NoError(t, d.RecordDeployedFile(ctx, DeployedFileRecord{GameID: "g", Profile: "p", Path: pak, SourceID: "s", ModID: "m"}))
	has, err = d.HasDeployedFilesUnder(ctx, "g", domain.RootPrefix)
	require.NoError(t, err)
	assert.True(t, has)
	has, err = d.HasDeployedFilesUnder(ctx, "g", domain.RootInstallPath)
	require.NoError(t, err)
	assert.False(t, has)
	conflicts, err := d.CheckFileConflicts(ctx, "g", "p", []domain.DeployPath{pak, domain.ModPathFile(pak.Rel)})
	require.NoError(t, err)
	require.Len(t, conflicts, 1)
	assert.Equal(t, pak, conflicts[0].Path)
}
