package nexusmods

// #592: a NexusMods mod carries only category_id; its name comes from the
// game endpoint's category list (testdata/v1-mod-status/game-categories.json,
// the v1 game document's shape with its list cut to three entries), fetched
// once per game domain. A failed lookup never fails GetMod.

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modWithCategory(id string, category int) recorded {
	return recorded{http.StatusOK, `{"mod_id":` + id + `,"name":"m","domain_name":"skyrimspecialedition","version":"1.0","status":"published","available":true,"category_id":` + strconv.Itoa(category) + `}`}
}

func TestNexusMods_GetMod_ResolvesCategoryNameOncePerGame(t *testing.T) {
	nm, hits := statusServer(t, map[string]recorded{
		sse + "/mods/1.json": modWithCategory("1", 42),
		sse + "/mods/2.json": modWithCategory("2", 54),
		sse + "/mods/3.json": modWithCategory("3", 999),
		sse + ".json":        {http.StatusOK, "game-categories.json"},
	})
	ctx := context.Background()

	mod, err := nm.GetMod(ctx, "skyrimspecialedition", "1")
	require.NoError(t, err)
	assert.Equal(t, "42", mod.Category, "Category keeps the id")
	assert.Equal(t, "User Interface", mod.CategoryName)

	mod, err = nm.GetMod(ctx, "skyrimspecialedition", "2")
	require.NoError(t, err)
	assert.Equal(t, "Armour", mod.CategoryName)

	mod, err = nm.GetMod(ctx, "skyrimspecialedition", "3")
	require.NoError(t, err)
	assert.Empty(t, mod.CategoryName, "an id the list does not have has no name")

	assert.Equal(t, int32(1), hits[sse+".json"].Load(), "one game fetch for every mod of the game")
}

func TestNexusMods_GetMod_CategoryLookupFailureIsNotAnError(t *testing.T) {
	nm, hits := statusServer(t, map[string]recorded{
		sse + "/mods/1.json": modWithCategory("1", 42),
		sse + ".json":        {http.StatusInternalServerError, `{"code":500,"message":"backend unavailable"}`},
	})
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	nm.client.now = func() time.Time { return now }
	ctx := context.Background()

	for range 3 {
		mod, err := nm.GetMod(ctx, "skyrimspecialedition", "1")
		require.NoError(t, err, "a failed category lookup never fails the read")
		assert.Empty(t, mod.CategoryName)
	}
	assert.Equal(t, int32(1), hits[sse+".json"].Load(), "a failed fetch is not retried at once")

	now = now.Add(categoryRetryAfter)
	_, err := nm.GetMod(ctx, "skyrimspecialedition", "1")
	require.NoError(t, err)
	assert.Equal(t, int32(2), hits[sse+".json"].Load(), "and is retried once the back-off has passed")
}

func TestNexusMods_GetMod_NoCategoryMakesNoGameFetch(t *testing.T) {
	nm, hits := statusServer(t, map[string]recorded{
		sse + "/mods/1.json": modWithCategory("1", 0),
		sse + ".json":        gameOK,
	})
	mod, err := nm.GetMod(context.Background(), "skyrimspecialedition", "1")
	require.NoError(t, err)
	assert.Empty(t, mod.CategoryName)
	assert.Zero(t, hits[sse+".json"].Load())
}

// The update check reports every mod it fetched, so core can backfill the
// stored name with no request of its own.
func TestNexusMods_CheckUpdates_ReportsFetchedMods(t *testing.T) {
	nm, _ := statusServer(t, map[string]recorded{
		sse + "/mods/1.json":       modWithCategory("1", 42),
		sse + "/mods/1/files.json": {http.StatusOK, `{"files":[],"file_updates":[]}`},
		sse + ".json":              {http.StatusOK, "game-categories.json"},
	})
	var seen []domain.Mod
	ctx := source.WithFetchedModObserver(context.Background(), func(m domain.Mod) { seen = append(seen, m) })
	updates, err := nm.CheckUpdates(ctx, []domain.InstalledMod{{Mod: domain.Mod{ID: "1", Name: "m", Version: "1.0", GameID: "skyrimspecialedition"}}})
	require.NoError(t, err)
	assert.Empty(t, updates)
	require.Len(t, seen, 1)
	assert.Equal(t, "User Interface", seen[0].CategoryName)
}
