package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #516: installed_mods.manual_download was only ever read as "adopted in
// place by `lmm import` scan mode" (verify's adoptedInPlace and versionPass),
// while its name said "the source will not serve this file" - the fact #543
// keeps in manual_only_mods. migrateV22 renames the column to what it means.
// Every existing row must read back exactly as it did.

// rewindToV21 puts the column back under its old name and forgets v22, so
// the next migrate runs v22 against a database an older lmm left.
func rewindToV21(t *testing.T, d *DB) {
	t.Helper()
	_, err := d.Exec("ALTER TABLE installed_mods RENAME COLUMN adopted_in_place TO manual_download")
	require.NoError(t, err)
	_, err = d.Exec("DELETE FROM schema_migrations WHERE version >= 22")
	require.NoError(t, err)
}

func installedColumns(t *testing.T, d *DB) map[string]bool {
	t.Helper()
	rows, err := d.Query("SELECT name FROM pragma_table_info('installed_mods')")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	cols := make(map[string]bool)
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		cols[name] = true
	}
	require.NoError(t, rows.Err())
	return cols
}

func TestMigrateV22_RenamesManualDownloadToAdoptedInPlace(t *testing.T) {
	ctx := t.Context()
	d := openMemory(t)
	rewindToV21(t, d)

	for _, r := range []struct {
		id     string
		manual any // nil -> the column's DEFAULT
	}{{"adopted", 1}, {"downloaded", 0}, {"defaulted", nil}} {
		if r.manual == nil {
			_, err := d.Exec(`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, summary, source_url, link_method)
				VALUES ('s', ?, 'g', 'default', ?, '1', '', '', '', 0)`, r.id, r.id)
			require.NoError(t, err)
			continue
		}
		_, err := d.Exec(`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, summary, source_url, link_method, manual_download)
			VALUES ('s', ?, 'g', 'default', ?, '1', '', '', '', 0, ?)`, r.id, r.id, r.manual)
		require.NoError(t, err)
	}

	require.NoError(t, d.migrate(ctx))

	var version int
	require.NoError(t, d.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	assert.Equal(t, 22, version)
	cols := installedColumns(t, d)
	assert.True(t, cols["adopted_in_place"])
	assert.False(t, cols["manual_download"], "the misleading name is gone")

	want := map[string]bool{"adopted": true, "downloaded": false, "defaulted": false}
	for id, adopted := range want {
		got, err := d.GetInstalledMod(ctx, "s", id, "g", "default")
		require.NoError(t, err)
		assert.Equal(t, adopted, got.AdoptedInPlace, id)
	}
	all, err := d.GetInstalledMods(ctx, "g", "default")
	require.NoError(t, err)
	require.Len(t, all, 3)
	for _, m := range all {
		assert.Equal(t, want[m.ID], m.AdoptedInPlace, m.ID)
	}
}

// A schema whose recorded version was rolled back past v22 while the column
// already carries its new name (the v17/v19 tests re-run every later
// migration) re-applies v22 as a no-op.
func TestMigrateV22_IsIdempotent(t *testing.T) {
	ctx := t.Context()
	d := openMemory(t)
	_, err := d.Exec(`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, summary, source_url, link_method, adopted_in_place)
		VALUES ('s', 'm', 'g', 'default', 'M', '1', '', '', '', 0, 1)`)
	require.NoError(t, err)
	_, err = d.Exec("DELETE FROM schema_migrations WHERE version >= 22")
	require.NoError(t, err)

	require.NoError(t, d.migrate(ctx))

	got, err := d.GetInstalledMod(ctx, "s", "m", "g", "default")
	require.NoError(t, err)
	assert.True(t, got.AdoptedInPlace)
	assert.False(t, installedColumns(t, d)["manual_download"])
}
