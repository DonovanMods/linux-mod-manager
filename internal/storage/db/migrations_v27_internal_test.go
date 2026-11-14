package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrateV27_MigratesAV26Database covers #607's migration: a database
// at V26 with an installed row migrates on open, installed_mods gains
// mod_type, the row reads as the game's default type (”), and a re-run is
// a no-op.
func TestMigrateV27_MigratesAV26Database(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := context.Background()

	for _, stmt := range []string{
		`ALTER TABLE installed_mods DROP COLUMN mod_type`,
		`DELETE FROM schema_migrations WHERE version >= 27`,
	} {
		_, err := d.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	_, err = d.Exec(`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, summary, source_url, link_method)
		VALUES ('s', 'm', 'g', 'default', 'Mod', '2.0', '', '', '', 0)`)
	require.NoError(t, err)

	require.NoError(t, d.migrate(ctx))
	var version int
	require.NoError(t, d.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.Equal(t, 27, version)
	row, err := d.GetInstalledMod(ctx, "s", "m", "g", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.Empty(t, row.ModType, "a pre-existing row has the game's default type")

	require.NoError(t, migrateV27(ctx, d), "a re-run is a no-op")
}
