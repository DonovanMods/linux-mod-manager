package db_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManualOnlyMods_RecordClearAndScope pins #543's record of the mods a
// source will not serve through its API: keyed by game, source and mod
// (a NexusMods id is only unique within its game), idempotent both ways, and
// independent of installed_mods - an install the source refused has no row,
// and the fact must outlive that.
func TestManualOnlyMods_RecordClearAndScope(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	ctx := context.Background()

	got, err := database.ManualOnlyMods(ctx, "g1")
	require.NoError(t, err)
	assert.Empty(t, got)

	require.NoError(t, database.RecordManualOnly(ctx, "g1", "curseforge", "42"))
	require.NoError(t, database.RecordManualOnly(ctx, "g1", "curseforge", "42"), "recording twice is not an error")
	require.NoError(t, database.RecordManualOnly(ctx, "g1", "nexusmods", "7"))
	require.NoError(t, database.RecordManualOnly(ctx, "g2", "nexusmods", "8"))

	got, err = database.ManualOnlyMods(ctx, "g1")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"curseforge:42": true, "nexusmods:7": true}, got)

	require.NoError(t, database.ClearManualOnly(ctx, "g1", "curseforge", "42"))
	require.NoError(t, database.ClearManualOnly(ctx, "g1", "curseforge", "42"), "clearing what is not there is not an error")
	got, err = database.ManualOnlyMods(ctx, "g1")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"nexusmods:7": true}, got)

	got, err = database.ManualOnlyMods(ctx, "g2")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"nexusmods:8": true}, got)
}
