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

	save := func(gameID, profileName, modID string, enabled, external bool) {
		t.Helper()
		require.NoError(t, database.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod:          domain.Mod{ID: modID, SourceID: "src", Name: modID, Version: "1.0", GameID: gameID},
			ProfileName:  profileName,
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      enabled,
			External:     external,
		}))
	}
	save("g2", "default", "zulu", false, false)
	save("g1", "other", "bravo", false, false)
	save("g1", "default", "alpha", false, false)
	save("g1", "default", "on", true, false)
	save("g1", "default", "workshop", false, true)

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
		if r.ModID == "workshop" {
			assert.True(t, r.External, "the external flag travels with the row, so the caller can skip it (#269)")
		}
	}
}
