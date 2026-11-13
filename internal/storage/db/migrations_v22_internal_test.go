package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrateV22_AddsCategoryNameAndIsIdempotent covers #592's migration:
// installed_mods gains category_name, existing rows read it as NULL, and a
// re-run (a database some other path already gave the column) is a no-op.
func TestMigrateV22_AddsCategoryNameAndIsIdempotent(t *testing.T) {
	d, err := New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	ctx := context.Background()

	var have int
	require.NoError(t, d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('installed_mods') WHERE name = 'category_name'`).Scan(&have))
	assert.Equal(t, 1, have)

	var version int
	require.NoError(t, d.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.Equal(t, 25, version)

	_, err = d.Exec(`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, summary, source_url, link_method)
		VALUES ('s', 'm', 'g', 'default', 'n', '1', '', '', '', 0)`)
	require.NoError(t, err)
	var got *string
	require.NoError(t, d.QueryRow("SELECT category_name FROM installed_mods WHERE mod_id = 'm'").Scan(&got))
	assert.Nil(t, got, "an existing row has no category until a fetch records one")

	require.NoError(t, migrateV22(ctx, d))
}
