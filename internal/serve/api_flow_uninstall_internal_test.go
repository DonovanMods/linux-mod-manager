package serve

// The uninstall flow over /api/v1: PlanUninstall -> POST /api/v1/jobs.
// Every assertion here is on the end state - the database row, the
// profile's load order, the deployed tree and the cache entry - which is
// what makes it a port of the deleted mutations_uninstall_internal_test.go
// rather than a rewrite: only the entry point changed.

import (
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uninstallPlanBody names the fixture's one installed mod.
const uninstallPlanBody = `{"source_id":"` + fixtureSourceID + `","mod_id":"m1"}`

// TestFlowUninstall_PlanRemovesNothing is the Plan half: the plan lists the
// files that would go, and nothing has happened yet.
func TestFlowUninstall_PlanRemovesNothing(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	_, raw := planFlow(t, s, game, "uninstall", uninstallPlanBody)
	assert.Contains(t, string(raw), deployFixtureFile, "the plan must list the files that would be removed")

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.FileExists(t, deployedFixturePath(game))
}

// TestFlowUninstall_JobRemovesEverything is the Apply half: the mod is gone
// from the database, the profile and the game directory, and its cache
// entry is deleted.
func TestFlowUninstall_JobRemovesEverything(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	j := runFlow(t, s, game, "uninstall", uninstallPlanBody, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
	assert.NoFileExists(t, deployedFixturePath(game))

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "the profile's load order must lose the ref too")

	assert.False(t, svc.GetGameCache(game).Exists(game.ID, fixtureSourceID, "m1", "1.0"),
		"without keep_cache the cache entry is deleted")
}

// TestFlowUninstall_KeepCacheOptionIsHonoured pins #226: keep_cache is an
// apply-time option, so the cache entry survives when the job carries it.
func TestFlowUninstall_KeepCacheOptionIsHonoured(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	j := runFlow(t, s, game, "uninstall", uninstallPlanBody, `{"keep_cache":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
	assert.True(t, svc.GetGameCache(game).Exists(game.ID, fixtureSourceID, "m1", "1.0"),
		"keep_cache must leave the cache entry in place")
}

// TestFlowUninstall_UsedPlanIsRefusedWithoutMutatingTwice is the plan
// store's single-use rule seen from a double submission: the second
// redemption is a 409 and the mod is removed exactly once.
func TestFlowUninstall_UsedPlanIsRefusedWithoutMutatingTwice(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	id, _ := planFlow(t, s, game, "uninstall", uninstallPlanBody)
	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	again := doAPI(s, http.MethodPost, "/api/v1/jobs", `{"plan_id":"`+string(id)+`"}`)
	require.Equal(t, http.StatusConflict, again.Code, again.Body.String())

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
}
