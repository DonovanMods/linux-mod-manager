package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// TestModFileLists_StoreReadTouchForget pins #676's file-list store: one
// list per source, game and mod (not per profile - the list is the
// source's), its time moved forward only, and forgotten on request.
func TestModFileLists_StoreReadTouchForget(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	ctx := context.Background()
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	_, ok, err := database.ModFileList(ctx, "nexusmods", "g1", "7")
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, database.SaveModFileList(ctx, db.ModFileList{
		SourceID: "nexusmods", GameID: "g1", ModID: "7", Files: `[{"id":"f1"}]`, CheckedAt: at, Format: 2,
	}))
	got, ok, err := database.ModFileList(ctx, "nexusmods", "g1", "7")
	require.NoError(t, err)
	require.True(t, ok)
	assert.JSONEq(t, `[{"id":"f1"}]`, got.Files)
	assert.True(t, at.Equal(got.CheckedAt))
	assert.Equal(t, 2, got.Format)

	all, err := database.ModFileLists(ctx, "nexusmods", "g1")
	require.NoError(t, err)
	assert.Len(t, all, 1)

	require.NoError(t, database.TouchModFileLists(ctx, "nexusmods", "g1", map[string]time.Time{"7": at.Add(time.Hour)}))
	require.NoError(t, database.TouchModFileLists(ctx, "nexusmods", "g1", map[string]time.Time{"7": at}))
	got, _, err = database.ModFileList(ctx, "nexusmods", "g1", "7")
	require.NoError(t, err)
	assert.True(t, at.Add(time.Hour).Equal(got.CheckedAt), "forward only")

	require.NoError(t, database.ForgetModFileList(ctx, "nexusmods", "g1", "7"))
	_, ok, err = database.ModFileList(ctx, "nexusmods", "g1", "7")
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestModFileLists_PruneDropsWhatNoProfileHasAndWhatIsOld: the store is
// bounded - a list for a mod no profile has is dropped once it is older
// than the grace it gets (an install plan's list, before the install), and
// any list older than the oldest a source ever vouches for is dropped.
func TestModFileLists_PruneDropsWhatNoProfileHasAndWhatIsOld(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	seedCheckMod(t, database, "1") // nexusmods:1 installed in g1

	save := func(modID string, at time.Time) {
		require.NoError(t, database.SaveModFileList(ctx, db.ModFileList{SourceID: "nexusmods", GameID: "g1", ModID: modID, Files: `[]`, CheckedAt: at}))
	}
	save("1", now.Add(-2*time.Hour)) // installed: kept
	save("2", now.Add(-2*time.Hour)) // not installed, past the grace: dropped
	save("3", now.Add(-time.Minute)) // not installed, within the grace: kept
	require.NoError(t, database.SaveModFileList(ctx, db.ModFileList{SourceID: "nexusmods", GameID: "g1", ModID: "1", Files: `[]`, CheckedAt: now.Add(-40 * 24 * time.Hour)}))

	removed, err := database.PruneModFileLists(ctx, now.Add(-time.Hour), now.Add(-30*24*time.Hour), 1000)
	require.NoError(t, err)
	assert.Equal(t, 2, removed, "the uninstalled mod's, and the installed mod's month-old list")
	all, err := database.ModFileLists(ctx, "nexusmods", "g1")
	require.NoError(t, err)
	assert.Equal(t, []string{"3"}, keysOf(all))

	for i := range 5 {
		save(string(rune('a'+i)), now.Add(-time.Duration(i)*time.Second))
	}
	_, err = database.PruneModFileLists(ctx, now.Add(-time.Hour), now.Add(-30*24*time.Hour), 3)
	require.NoError(t, err)
	all, err = database.ModFileLists(ctx, "nexusmods", "g1")
	require.NoError(t, err)
	assert.Len(t, all, 3, "capped, the oldest first to go")
	assert.Contains(t, all, "a")
}

func keysOf(m map[string]db.ModFileList) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
