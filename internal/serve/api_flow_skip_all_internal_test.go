package serve

// Issue 667 through the endpoints the SPA drives: POST
// /api/v1/updates/skip-all skips every offered update (#542's skip in bulk)
// and unskip-all offers them again; a mod the profile's collection locks is
// left out of GET /api/v1/updates entirely and counted in skipped.locked.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipAllRoute(game *domain.Game, action string) string {
	return scoped("/api/v1/updates/"+action, game)
}

func decodeSkipAll(t *testing.T, body []byte) core.UpdateSkipAllResult {
	t.Helper()
	var result core.UpdateSkipAllResult
	require.NoError(t, json.Unmarshal(body, &result))
	return result
}

func skipAllModIDs(result core.UpdateSkipAllResult) []string {
	var ids []string
	for _, m := range result.Mods {
		ids = append(ids, m.ModID)
	}
	return ids
}

// lockFixtureModByCollection makes modID locked by the profile's collection,
// "Night City", which records an origin to sync from.
func lockFixtureModByCollection(t *testing.T, svc *core.Service, game *domain.Game, modID string) {
	t.Helper()
	prof, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	prof.Collection = &domain.CollectionInfo{ID: "night-city", Version: 1, Title: "Night City",
		Origin: &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abc123", Revision: 1}}
	for i := range prof.Mods {
		if prof.Mods[i].ModID == modID {
			prof.Mods[i].Locked, prof.Mods[i].FromCollection = true, true
		}
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), prof))
}

func TestAPIFlow_SkipAll_SkipsEveryUpdateAndUnskipAllOffersThemAgain(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, skipAllRoute(game, "skip-all"), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	result := decodeSkipAll(t, rec.Body.Bytes())
	assert.Equal(t, game.ID, result.GameID)
	assert.Equal(t, "default", result.Profile)
	assert.ElementsMatch(t, updatableModIDs, skipAllModIDs(result))
	for _, id := range updatableModIDs {
		row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, id, game.ID, "default")
		require.NoError(t, err)
		assert.Equal(t, updateToVersion, row.SkippedVersion)
	}

	report := getUpdatesReport(t, s, game)
	assert.Empty(t, report.Updates, "Skip all empties the offered updates")
	assert.ElementsMatch(t, updatableModIDs, updateIDs(report.SkippedUpdates))

	rec = doAPI(s, http.MethodPost, skipAllRoute(game, "skip-all"), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, decodeSkipAll(t, rec.Body.Bytes()).Mods, "nothing left to skip is not an error")

	rec = doAPI(s, http.MethodPost, skipAllRoute(game, "unskip-all"), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.ElementsMatch(t, updatableModIDs, skipAllModIDs(decodeSkipAll(t, rec.Body.Bytes())))

	report = getUpdatesReport(t, s, game)
	assert.ElementsMatch(t, updatableModIDs, updateIDs(report.Updates))
	assert.Empty(t, report.SkippedUpdates)
}

func TestAPIFlow_SkipAll_RefusesABody(t *testing.T) {
	s, _, game := newUpdatesFixtureServer(t)
	for _, action := range []string{"skip-all", "unskip-all"} {
		rec := doAPI(s, http.MethodPost, skipAllRoute(game, action), `{"mods":["u1"]}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", action, rec.Body.String())
	}
}

func TestAPIFlow_Updates_CollectionLockedModIsCountedNotListed(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)
	lockFixtureModByCollection(t, svc, game, "u1")

	report := getUpdatesReport(t, s, game)
	assert.NotContains(t, updateIDs(report.Updates), "u1")
	assert.Equal(t, 1, report.Skipped.Locked)
	assert.Equal(t, "Night City", report.Skipped.Collection)
	assert.True(t, report.Skipped.CollectionSync)

	rec := doAPI(s, http.MethodPost, skipAllRoute(game, "skip-all"), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.ElementsMatch(t, []string{"u2", "u3"}, skipAllModIDs(decodeSkipAll(t, rec.Body.Bytes())),
		"Skip all skips what was offered, never the collection's mods")
}
