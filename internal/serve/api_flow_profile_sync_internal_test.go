package serve

// The profile-sync flow over /api/v1: POST /api/v1/plans/profile_sync ->
// POST /api/v1/jobs (#326, epic live review C-3 - `lmm profile sync` had
// no plan kind and no route). What is asserted here is the wire: the plan
// document, the job and the request's validation. How a sync classifies
// and applies its diff is internal/core's profile_sync_test.go.

import (
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dropFixtureProfileRef removes the seeded mod from the profile's own mod
// list while leaving the DB row installed and enabled - the exact drift
// `lmm profile sync` exists to repair.
func dropFixtureProfileRef(t *testing.T, svc *core.Service, gameID string) {
	t.Helper()
	require.NoError(t, svc.NewProfileManager().RemoveMod(t.Context(), gameID, "default", fixtureSourceID, "m1"))
	profile, err := svc.NewProfileManager().Get(t.Context(), gameID, "default")
	require.NoError(t, err)
	require.Empty(t, profile.Mods, "the drift the test is about must actually exist")
}

// TestFlowProfileSync_JobRestoresTheProfilesInstalledSet is the plan ->
// job wiring: the plan names the drift without writing the profile, and
// the job's result is core's ProfileSyncResult, counting the one add.
func TestFlowProfileSync_JobRestoresTheProfilesInstalledSet(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	dropFixtureProfileRef(t, svc, game.ID)

	id, raw := planFlow(t, s, game, "profile_sync", `{"profile":"default"}`)
	assert.Contains(t, string(raw), `"to_add"`)
	assert.Contains(t, string(raw), `"m1"`)
	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "planning must not write the profile")

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.ProfileSyncResult)
	require.True(t, ok, "the job's result is core.ProfileSyncResult, got %T", j.status().Result)
	assert.Equal(t, 1, result.Added)
}

// TestFlowProfileSync_UnknownProfileNamesAMissingProfile is M4 (coordinator
// correction, epic review M-3): an unrecognised profile name is NOT
// refused - it is CORE's deliberate behaviour, matching `lmm profile
// sync`'s own CLI parity, that PlanProfileSync computes the plan as if the
// profile were empty and reports Missing:true, and ApplyProfileSync then
// creates the profile.yaml. This pins the fact on the wire (200, not 404,
// with missing:true on the plan) and that Apply really does create it, so
// a regression toward "silently swallow the flag" or "quietly stop
// creating it" is caught either way. Task B's confirm modal must render
// missing:true rather than treat 200 as purely informational.
func TestFlowProfileSync_UnknownProfileNamesAMissingProfile(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/profile_sync", game), `{"profile":"nope"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"missing": true`)

	_, err := svc.NewProfileManager().Get(t.Context(), game.ID, "nope")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound, "planning must not create the profile")

	j := runFlow(t, s, game, "profile_sync", `{"profile":"nope"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	_, err = svc.NewProfileManager().Get(t.Context(), game.ID, "nope")
	assert.NoError(t, err, "applying a Missing:true plan must create the profile")
}

// TestFlowProfileSync_UsedPlanIsRefused is M2: the plan store's single-use
// 409, matching mod_relink/purge's own coverage - profile_sync shares the
// same generic machinery, but had no test pinning it.
func TestFlowProfileSync_UsedPlanIsRefused(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	id, _ := planFlow(t, s, game, "profile_sync", `{"profile":"default"}`)
	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	again := doAPI(s, http.MethodPost, "/api/v1/jobs", `{"plan_id":"`+string(id)+`"}`)
	assert.Equal(t, http.StatusConflict, again.Code, again.Body.String())
}

// TestFlowProfileSync_RequiresAProfile pins the request's one validation.
func TestFlowProfileSync_RequiresAProfile(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/profile_sync", game), `{}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "profile")
}

// TestFlowProfileSync_RejectsApplyOptions: the flow takes none, so passing
// one must be a 400 rather than a silent no-op.
func TestFlowProfileSync_RejectsApplyOptions(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	id, _ := planFlow(t, s, game, "profile_sync", `{"profile":"default"}`)
	rec := doAPI(s, http.MethodPost, "/api/v1/jobs",
		`{"plan_id":"`+string(id)+`","options":{"force":true}}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestFlowProfileSync_RequiresCSRF.
func TestFlowProfileSync_RequiresCSRF(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPIWithoutCSRF(s, http.MethodPost, scoped("/api/v1/plans/profile_sync", game), `{"profile":"default"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
