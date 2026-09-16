package db_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMeta_AbsentKeyIsEmptyNotAnError pins the accessor contract #431's
// one-time backfill relies on: "never recorded" and "recorded as empty" are
// one answer, because presence is what a marker means.
func TestMeta_AbsentKeyIsEmptyNotAnError(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	ctx := context.Background()

	value, err := database.GetMeta(ctx, "never-written")
	require.NoError(t, err)
	assert.Empty(t, value)

	require.NoError(t, database.SetMeta(ctx, "obligation", "2026-09-16T00:00:00Z"))
	value, err = database.GetMeta(ctx, "obligation")
	require.NoError(t, err)
	assert.Equal(t, "2026-09-16T00:00:00Z", value)

	// Replaces rather than duplicating - the key is the primary key.
	require.NoError(t, database.SetMeta(ctx, "obligation", "later"))
	value, err = database.GetMeta(ctx, "obligation")
	require.NoError(t, err)
	assert.Equal(t, "later", value)
}

// TestDisabledModRows_EveryGameAndProfileInOrder pins the query the
// profile-document backfill walks: every disabled row, whatever game or
// profile it belongs to, in an order that groups a profile's rows together
// (so the backfill loads each document once), and no enabled row.
func TestDisabledModRows_EveryGameAndProfileInOrder(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	ctx := context.Background()

	save := func(gameID, profileName, modID string, enabled, deployed, external bool) {
		t.Helper()
		require.NoError(t, database.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:          domain.Mod{ID: modID, SourceID: "src", Name: "Mod " + modID, Version: "1.0", GameID: gameID},
			ProfileName:  profileName,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      enabled,
			Deployed:     deployed,
			External:     external,
		}))
	}
	save("g2", "default", "zulu", false, false, false)
	save("g1", "other", "bravo", false, true, false)
	save("g1", "default", "alpha", false, false, false)
	save("g1", "default", "on", true, true, false)
	save("g1", "default", "workshop", false, false, true)

	rows, err := database.DisabledModRows(ctx)
	require.NoError(t, err)

	got := make([]string, 0, len(rows))
	for _, r := range rows {
		got = append(got, r.GameID+"/"+r.ProfileName+"/"+r.ModID)
	}
	assert.Equal(t, []string{
		"g1/default/alpha", "g1/default/workshop", "g1/other/bravo", "g2/default/zulu",
	}, got, "every disabled row, grouped by game and profile; the enabled one is absent")

	for _, r := range rows {
		assert.Equal(t, "Mod "+r.ModID, r.Name, "the display name travels with the row, for the notice")
		switch r.ModID {
		case "workshop":
			assert.True(t, r.External, "the external flag travels with the row, so the caller can skip it (#269)")
		case "bravo":
			assert.True(t, r.Deployed, "so does the deployed flag: (0,1) is what a profile switch leaves, not a disable")
		default:
			assert.False(t, r.Deployed)
		}
	}
}

// TestMeta_DeleteAndPrefix pins the two accessors the per-profile
// obligations use: a delete of an absent key is not an error, and a prefix
// match is exact - a LIKE wildcard character in the prefix (the `_` in
// every key this table holds) must not match anything but itself.
func TestMeta_DeleteAndPrefix(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	ctx := context.Background()

	for _, key := range []string{"a_b", "a_b:g/p", "a_b:g/q", "aXb:g/p", "a_c"} {
		require.NoError(t, database.SetMeta(ctx, key, "v-"+key))
	}

	got, err := database.MetaWithPrefix(ctx, "a_b")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a_b": "v-a_b", "a_b:g/p": "v-a_b:g/p", "a_b:g/q": "v-a_b:g/q"}, got)

	require.NoError(t, database.DeleteMeta(ctx, "a_b:g/p"))
	require.NoError(t, database.DeleteMeta(ctx, "never-written"))
	value, err := database.GetMeta(ctx, "a_b:g/p")
	require.NoError(t, err)
	assert.Empty(t, value)

	got, err = database.MetaWithPrefix(ctx, "nothing")
	require.NoError(t, err)
	assert.Empty(t, got)
}
