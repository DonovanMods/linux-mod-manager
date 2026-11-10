package serve

// The purge flow over /api/v1: POST /api/v1/plans/purge -> POST
// /api/v1/jobs (#326, epic live review C-3 - `lmm purge` had no web path
// at all). What is asserted here is the wire: that planning applies
// nothing, that the job runs the purge, and that each option reaches the
// half that reads it. What a purge leaves behind - records kept or
// removed, profile entries, the game directory - is internal/core's
// purge_test.go.

import (
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
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

// TestFlowPurge_JobEmptiesTheProfileOnDisk is the Apply half: the job
// succeeds, its result is core's PurgeResult, and the game directory has
// lost the deployed file.
func TestFlowPurge_JobEmptiesTheProfileOnDisk(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	j := runFlow(t, s, game, "purge", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	result, ok := j.status().Result.(*core.PurgeResult)
	require.True(t, ok, "the job's result is core.PurgeResult, got %T", j.status().Result)
	assert.Equal(t, 1, result.Purged)
	assert.NoFileExists(t, deployedFixturePath(game), "the job ran the purge")
}

// TestFlowPurge_UninstallOptionRemovesTheRecords pins the apply-time
// option reaching Apply: Apply reads the job's own options, not the
// plan's, and with uninstall the record goes. Core's
// TestService_PurgeProfile_Uninstall_RemovesRecordsAndProfileEntries pins
// the rest of what it removes.
func TestFlowPurge_UninstallOptionRemovesTheRecords(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	deployFixtureProfile(t, s, game)

	j := runFlow(t, s, game, "purge", `{"uninstall":true}`, `{"uninstall":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "the job's uninstall option reached Apply")
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
