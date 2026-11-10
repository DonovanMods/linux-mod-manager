package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// #516: InstalledMod.AdoptedInPlace is stored in installed_mods'
// manual_download column, which keeps its historical name so an older lmm
// can still read the database. A row written by an older lmm reads as
// adopted in place, and the field round-trips through every writer.
func TestAdoptedInPlace_StoredInManualDownloadColumn(t *testing.T) {
	ctx := t.Context()
	d := openMemory(t)

	_, err := d.Exec(`INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, summary, source_url, link_method, manual_download)
		VALUES ('s', 'legacy', 'g', 'default', 'Legacy', '1', '', '', '', 0, 1)`)
	require.NoError(t, err)
	got, err := d.GetInstalledMod(ctx, "s", "legacy", "g", "default")
	require.NoError(t, err)
	assert.True(t, got.AdoptedInPlace, "an older lmm's manual_download=1 row")

	mod := &domain.InstalledMod{
		Mod:            domain.Mod{ID: "m", SourceID: "s", Name: "M", Version: "1", GameID: "g"},
		ProfileName:    "default",
		Enabled:        true,
		AdoptedInPlace: true,
	}
	require.NoError(t, d.SaveInstalledMod(ctx, mod))
	var raw int
	require.NoError(t, d.QueryRow(`SELECT manual_download FROM installed_mods WHERE mod_id = 'm'`).Scan(&raw))
	assert.Equal(t, 1, raw)

	all, err := d.GetInstalledMods(ctx, "g", "default")
	require.NoError(t, err)
	require.Len(t, all, 2)
	for _, m := range all {
		assert.True(t, m.AdoptedInPlace, m.ID)
	}

	mod.AdoptedInPlace = false
	require.NoError(t, d.SaveInstalledMod(ctx, mod))
	got, err = d.GetInstalledMod(ctx, "s", "m", "g", "default")
	require.NoError(t, err)
	assert.False(t, got.AdoptedInPlace)
}
