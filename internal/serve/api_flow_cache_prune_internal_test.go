package serve

// The cache prune over /api/v1 (#678): POST /api/v1/plans/cache_prune ->
// POST /api/v1/jobs, GET /api/v1/cache and PUT /api/v1/settings/cache.
// Asserted on the cache's end state; what a prune keeps is internal/core's
// cache_prune_test.go.

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pruneFixtureVersions are the versions seedPruneFixture caches for mod
// "old" of the fixture game, oldest first; none is installed, so the
// default setting keeps the newest three.
var pruneFixtureVersions = []string{"1.0", "2.0", "3.0", "4.0", "5.0"}

// seedPruneFixture caches pruneFixtureVersions for the fixture game.
func seedPruneFixture(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	c := svc.GetGameCache(game)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i, v := range pruneFixtureVersions {
		k := cache.Key{GameID: game.ID, SourceID: fixtureSourceID, ModID: "old", Version: v}
		require.NoError(t, c.Store(k, "old.esp", []byte("payload "+v)))
		dir := c.ModPath(k)
		require.NoError(t, cache.MarkFileComplete(dir, "f"+v))
		require.NoError(t, cache.StampKey(dir, k))
		when := at.Add(time.Duration(i) * time.Hour)
		require.NoError(t, os.Chtimes(dir, when, when))
	}
}

// prunedFixtureLeft is which pruneFixtureVersions are still cached.
func prunedFixtureLeft(t *testing.T, svc *core.Service, game *domain.Game) []string {
	t.Helper()
	c := svc.GetGameCache(game)
	var out []string
	for _, v := range pruneFixtureVersions {
		if c.Exists(cache.Key{GameID: game.ID, SourceID: fixtureSourceID, ModID: "old", Version: v}) {
			out = append(out, v)
		}
	}
	return out
}

// TestFlowCachePrune_PlanListsWhatWouldGoAndRemovesNothing is the Plan
// half: the preview names the versions and their sizes, and planning
// removes nothing.
func TestFlowCachePrune_PlanListsWhatWouldGoAndRemovesNothing(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedPruneFixture(t, svc, game)

	_, raw := planFlow(t, s, game, "cache_prune", "")
	var plan core.CachePrunePlan
	require.NoError(t, json.Unmarshal(raw, &struct {
		Plan *core.CachePrunePlan `json:"plan"`
	}{&plan}))
	require.Len(t, plan.Entries, 2)
	assert.Equal(t, "2.0", plan.Entries[0].Version)
	assert.Equal(t, "1.0", plan.Entries[1].Version)
	assert.Positive(t, plan.Bytes)
	assert.Equal(t, []string{game.ID}, plan.Games)
	assert.Equal(t, pruneFixtureVersions, prunedFixtureLeft(t, svc, game), "planning removes nothing")
}

// TestFlowCachePrune_JobRemovesTheOldVersions is the Apply half.
func TestFlowCachePrune_JobRemovesTheOldVersions(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedPruneFixture(t, svc, game)

	j := runFlow(t, s, game, "cache_prune", `{"all_games":true}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.CachePruneResult)
	require.True(t, ok, "the job's result is core.CachePruneResult, got %T", j.status().Result)
	assert.Equal(t, 2, result.Versions)
	assert.Equal(t, []string{"3.0", "4.0", "5.0"}, prunedFixtureLeft(t, svc, game))
}

// TestFlowCachePrune_AStalePlanIsRefused: a version a profile starts
// referencing between the plan and the job keeps the job from removing
// anything.
func TestFlowCachePrune_AStalePlanIsRefused(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedPruneFixture(t, svc, game)

	id, _ := planFlow(t, s, game, "cache_prune", "")
	require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: fixtureSourceID, ModID: "old", Version: "1.0"}))
	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobFailed, j.status().State)
	assert.Equal(t, pruneFixtureVersions, prunedFixtureLeft(t, svc, game))
}

// TestAPICache_StatusAndSetting: GET /api/v1/cache is the status, and PUT
// /api/v1/settings/cache writes the setting and answers with the status
// it changes.
func TestAPICache_StatusAndSetting(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedPruneFixture(t, svc, game)

	rec := doAPI(s, http.MethodGet, "/api/v1/cache", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var status core.CacheStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.Equal(t, 3, status.KeepPreviousVersions)
	assert.Equal(t, 2, status.ReclaimableVersions)

	rec = doAPI(s, http.MethodPut, "/api/v1/settings/cache", `{"keep_previous_versions":1}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.Equal(t, 1, status.KeepPreviousVersions)
	assert.Equal(t, 4, status.ReclaimableVersions)
	assert.Equal(t, 1, svc.CacheKeepPreviousVersions())

	rec = doAPI(s, http.MethodPut, "/api/v1/settings/cache", `{"keep_previous_versions":-1}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.Equal(t, -1, status.KeepPreviousVersions)
	assert.Zero(t, status.ReclaimableVersions)

	rec = doAPI(s, http.MethodPut, "/api/v1/settings/cache", `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = doAPIWithoutCSRF(s, http.MethodPut, "/api/v1/settings/cache", `{"keep_previous_versions":2}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, -1, svc.CacheKeepPreviousVersions(), "a request without the CSRF token changes nothing")
}
