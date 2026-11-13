package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrateV24_AddsVariantColumnsAndIsIdempotent covers #614's migration:
// installed_mods gains variant and previous_variant, an existing row reads
// both as the plain tree (”), and a re-run is a no-op.
func TestMigrateV24_AddsVariantColumnsAndIsIdempotent(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := context.Background()

	for _, col := range []string{"variant", "previous_variant"} {
		var have int
		require.NoError(t, d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('installed_mods') WHERE name = ?`, col).Scan(&have))
		assert.Equal(t, 1, have, col)
	}

	var version int
	require.NoError(t, d.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.Equal(t, 26, version)

	_, err = d.Exec(`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, summary, source_url, link_method)
		VALUES ('s', 'm', 'g', 'default', 'n', '1', '', '', '', 0)`)
	require.NoError(t, err)
	var variant, previous string
	require.NoError(t, d.QueryRow("SELECT variant, previous_variant FROM installed_mods WHERE mod_id = 'm'").Scan(&variant, &previous))
	assert.Empty(t, variant, "an existing row is the plain tree")
	assert.Empty(t, previous)

	require.NoError(t, migrateV24(ctx, d))
}

// TestMigrateV24_MigratesAV23Database: a database at V23 with an installed
// row (the columns absent) migrates to V24 on open; the row reads as the
// plain tree and survives with every other column intact.
func TestMigrateV24_MigratesAV23Database(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := context.Background()

	// Back to V23: drop the columns and the migration's record.
	for _, stmt := range []string{
		`ALTER TABLE installed_mods DROP COLUMN variant`,
		`ALTER TABLE installed_mods DROP COLUMN previous_variant`,
		`ALTER TABLE installed_mods DROP COLUMN previous_fomod`,
		`DELETE FROM schema_migrations WHERE version >= 24`,
	} {
		_, err := d.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	_, err = d.Exec(`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, summary, source_url, link_method, previous_version)
		VALUES ('s', 'm', 'g', 'default', 'Mod', '2.0', '', '', '', 0, '1.0')`)
	require.NoError(t, err)

	require.NoError(t, d.migrate(ctx))

	var version int
	require.NoError(t, d.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.Equal(t, 26, version, "V24 and V25 both ran")
	row, err := d.GetInstalledMod(ctx, "s", "m", "g", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.Equal(t, "1.0", row.PreviousVersion)
	assert.Empty(t, row.Variant, "a pre-existing row is the plain tree")
	assert.Empty(t, row.PreviousVariant)
	assert.True(t, row.PreviousFomod.IsZero())
}
