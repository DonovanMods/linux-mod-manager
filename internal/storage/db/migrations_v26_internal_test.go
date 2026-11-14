package db

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrateV26_MigratesAV25Database covers #629's migration: a V25
// deployed_files table (no root column, the three-column key) is rebuilt
// with a root column, every existing row reads as mod_path with all its
// columns intact, and a re-run is a no-op.
func TestMigrateV26_MigratesAV25Database(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := context.Background()

	// Back to V25: the table as migrateV7 + migrateV18 left it.
	for _, stmt := range []string{
		`DROP TABLE deployed_files`,
		`CREATE TABLE deployed_files (
			game_id TEXT NOT NULL,
			profile_name TEXT NOT NULL,
			relative_path TEXT NOT NULL,
			source_id TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			deployed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			checksum TEXT, size INTEGER, mtime INTEGER, ctime INTEGER, mod_path TEXT,
			PRIMARY KEY (game_id, profile_name, relative_path)
		)`,
		`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id, checksum, size, mtime, ctime, mod_path)
			VALUES ('g', 'default', 'textures/a.dds', 'src', 'm', 'abc', 3, 4, 5, '/games/g/Data')`,
		`DELETE FROM schema_migrations WHERE version >= 26`,
	} {
		_, err := d.Exec(stmt)
		require.NoError(t, err, stmt)
	}

	require.NoError(t, d.migrate(ctx))

	var version int
	require.NoError(t, d.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.Equal(t, 28, version)
	var root string
	require.NoError(t, d.QueryRow("SELECT root FROM deployed_files WHERE relative_path = 'textures/a.dds'").Scan(&root))
	assert.Equal(t, "mod_path", root, "an existing row is under the mod_path")

	recs, err := d.DeployedFileRecordsForMod(ctx, "g", "default", "src", "m")
	require.NoError(t, err)
	require.Len(t, recs, 1)
	assert.Equal(t, domain.ModPathFile("textures/a.dds"), recs[0].Path)
	assert.Equal(t, "/games/g/Data", recs[0].ModPath)
	assert.Equal(t, &FileFingerprint{Checksum: "abc", Size: 3, MTime: 4, CTime: 5}, recs[0].Fingerprint)

	// The same relative path under the game root is another file.
	require.NoError(t, d.RecordDeployedFile(ctx, DeployedFileRecord{GameID: "g", Profile: "default", Path: domain.GameRootFile("textures/a.dds"), SourceID: "src", ModID: "enb"}))
	var n int
	require.NoError(t, d.QueryRow("SELECT COUNT(*) FROM deployed_files").Scan(&n))
	assert.Equal(t, 2, n)

	require.NoError(t, migrateV26(ctx, d), "a re-run is a no-op")
	require.NoError(t, d.QueryRow("SELECT COUNT(*) FROM deployed_files").Scan(&n))
	assert.Equal(t, 2, n)
}

// The root column holds only the two roots lmm knows: a writer cannot store
// a value a reader would have to guess at.
func TestMigrateV26_RootIsChecked(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	_, err = d.Exec(`INSERT INTO deployed_files (game_id, profile_name, root, relative_path, source_id, mod_id)
		VALUES ('g', 'p', 'modpath', 'a', 's', 'm')`)
	require.Error(t, err, "an unknown root is refused by the CHECK")
}
