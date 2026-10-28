package serve

// The purge flow over /api/v1: POST /api/v1/plans/purge -> POST
// /api/v1/jobs (#326, epic live review C-3 - `lmm purge` had no web path
// at all). Every assertion is on the END STATE: the game directory, the DB
// row and the profile's own mod list.

import (
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlowPurge_PlanListsWhatWouldGoAndChangesNothing is the Plan half.
func TestFlowPurge_PlanListsWhatWouldGoAndChangesNothing(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	_, raw := planFlow(t, s, game, "purge", "")
	assert.Contains(t, string(raw), `"m1"`, "the plan must name the mods it would purge")

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.FileExists(t, deployedFixturePath(game), "planning must not undeploy anything")
}

// TestFlowPurge_JobEmptiesTheProfileOnDisk is the Apply half, and the
// point of the whole flow: the game directory loses every deployed file,
// while the records stay (so `lmm deploy` restores them).
func TestFlowPurge_JobEmptiesTheProfileOnDisk(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	j := runFlow(t, s, game, "purge", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	assert.NoFileExists(t, deployedFixturePath(game), "purge must undeploy the profile's files")

	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err, "without --uninstall the record survives")
	assert.False(t, mod.Deployed, "the record must be marked not-deployed")

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Len(t, profile.Mods, 1, "the profile's load order is untouched without uninstall")
}

// TestFlowPurge_UninstallOptionRemovesTheRecords pins the apply-time
// option: with it, the DB row and the profile ref go too.
func TestFlowPurge_UninstallOptionRemovesTheRecords(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	j := runFlow(t, s, game, "purge", `{"uninstall":true}`, `{"uninstall":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	assert.NoFileExists(t, deployedFixturePath(game))
	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "--uninstall removes the profile ref too")
}

// TestFlowPurge_PlanEchoesTheUninstallOption pins that the preview tells
// the truth about what the confirm modal is about to do.
func TestFlowPurge_PlanEchoesTheUninstallOption(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	_, raw := planFlow(t, s, game, "purge", `{"uninstall":true}`)
	assert.Contains(t, string(raw), `"uninstall": true`)
}

// TestFlowPurge_RejectsUnknownOptions pins the strict decode every kind's
// options get.
func TestFlowPurge_RejectsUnknownOptions(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/purge", game), `{"uninstal":true}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestFlowPurge_UsedPlanIsRefused is the plan store's single-use rule.
func TestFlowPurge_UsedPlanIsRefused(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	id, _ := planFlow(t, s, game, "purge", "")
	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	again := doAPI(s, http.MethodPost, "/api/v1/jobs", `{"plan_id":"`+string(id)+`"}`)
	assert.Equal(t, http.StatusConflict, again.Code, again.Body.String())
}

// TestFlowPurge_RequiresCSRF - the plan entry point is state-changing like
// every other.
func TestFlowPurge_RequiresCSRF(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPIWithoutCSRF(s, http.MethodPost, scoped("/api/v1/plans/purge", game), "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
