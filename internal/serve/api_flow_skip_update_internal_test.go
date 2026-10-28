package serve

// Issue 542 through the endpoints the SPA drives: skipping a pending update
// takes it out of GET /api/v1/updates' "updates" (into "skipped_updates")
// and out of an "update all" plan, a one-key selection (the full mod page's
// "Update to vX") still updates it and clears the skip, and unskipping
// offers it again.

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipUpdateRoute(game *domain.Game, action, modID string) string {
	return scoped("/api/v1/mods/"+fixtureSourceID+"/"+modID+"/"+action, game)
}

func getUpdatesReport(t *testing.T, s *Server, game *domain.Game) core.UpdateCheckReport {
	t.Helper()
	rec := doAPI(s, http.MethodGet, scoped("/api/v1/updates", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.UpdateCheckReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
	return report
}

func updateIDs(updates []domain.Update) []string {
	var ids []string
	for _, u := range updates {
		ids = append(ids, u.InstalledMod.ID)
	}
	return ids
}

func TestAPIFlow_SkipUpdate_HidesItAndUnskipOffersItAgain(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, skipUpdateRoute(game, "skip-update", "u1"), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var result core.ModSettingResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, updateToVersion, result.SkippedVersion, "no version skips the one offered now")

	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "u1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, updateToVersion, row.SkippedVersion)
	assert.Equal(t, domain.UpdateNotify, row.UpdatePolicy, "a skip is not a pin")

	report := getUpdatesReport(t, s, game)
	assert.NotContains(t, updateIDs(report.Updates), "u1")
	assert.Equal(t, []string{"u1"}, updateIDs(report.SkippedUpdates))
	assert.Equal(t, 1, report.Skipped.Updates)

	rec = doAPI(s, http.MethodPost, skipUpdateRoute(game, "unskip-update", "u1"), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	report = getUpdatesReport(t, s, game)
	assert.Contains(t, updateIDs(report.Updates), "u1")
	assert.Empty(t, report.SkippedUpdates)
}

func TestAPIFlow_SkipUpdate_ExplicitVersion(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, skipUpdateRoute(game, "skip-update", "u2"), `{"version":"1.5"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "u2", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.5", row.SkippedVersion)
	assert.Contains(t, updateIDs(getUpdatesReport(t, s, game).Updates), "u2", "2.0 is newer than the skipped 1.5")
}

func TestAPIFlow_SkipUpdate_UnknownModIs404(t *testing.T) {
	s, _, game := newUpdatesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, skipUpdateRoute(game, "skip-update", "nope"), `{"version":"1.5"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	rec = doAPI(s, http.MethodPost, skipUpdateRoute(game, "unskip-update", "nope"), "")
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestAPIFlow_SkipUpdate_RejectsUnknownMembers(t *testing.T) {
	s, _, game := newUpdatesFixtureServer(t)

	rec := doAPI(s, http.MethodPost, skipUpdateRoute(game, "skip-update", "u1"), `{"verison":"1.5"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// The full mod page's "Update to vX" is a one-key "updates" plan: it still
// updates a skipped mod, end to end, and clears the skip.
func TestAPIFlow_SkipUpdate_ExplicitSelectionStillUpdatesAndClears(t *testing.T) {
	s, svc, game := newUpdatesFixtureServer(t)
	_, err := svc.SkipModUpdate(t.Context(), game, fixtureSourceID, "u1", "default", "")
	require.NoError(t, err)

	j := runFlow(t, s, game, "updates", `{"mods":["`+fixtureSourceID+`:u1"]}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "u1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, updateToVersion, row.Version)
	assert.Empty(t, row.SkippedVersion, "applying the update clears the skip")
}
