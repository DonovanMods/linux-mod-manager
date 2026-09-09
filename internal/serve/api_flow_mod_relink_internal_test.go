package serve

// The mod-relink flow over /api/v1: POST /api/v1/plans/mod_relink -> POST
// /api/v1/jobs (#326, epic live review C-3 - `lmm mod edit` had no plan
// kind, no route and no control). The end state asserted is the DATABASE:
// which source_id/mod_id the installed row is keyed by once the job has
// run.

import (
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// relinkPlanBody re-points the fixture's one installed mod at a different
// id on the same (configured) source - the only re-link the fixture game
// can express, since ApplyRelinkMod refuses a target source the game does
// not map.
const relinkPlanBody = `{"source_id":"` + fixtureSourceID + `","mod_id":"m1","new_mod_id":"m2"}`

// TestFlowModRelink_PlanDescribesTheMoveAndChangesNothing is the Plan
// half: From/To are on the wire and the database has not moved.
func TestFlowModRelink_PlanDescribesTheMoveAndChangesNothing(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)

	_, raw := planFlow(t, s, game, "mod_relink", relinkPlanBody)
	assert.Contains(t, string(raw), `"from"`)
	assert.Contains(t, string(raw), `"to"`)
	assert.Contains(t, string(raw), `"m2"`)

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	assert.NoError(t, err, "planning must not move the row")
}

// TestFlowModRelink_JobMovesTheRowToItsNewIdentity is the Apply half and
// the whole point: the old key is gone, the new one holds the mod, and the
// profile's ref followed it.
func TestFlowModRelink_JobMovesTheRowToItsNewIdentity(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)

	j := runFlow(t, s, game, "mod_relink", relinkPlanBody, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "the old identity must be gone")

	moved, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m2", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, fixtureSourceID, moved.SourceID)
	assert.Equal(t, "m2", moved.ID)

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 1)
	assert.Equal(t, "m2", profile.Mods[0].ModID, "the profile ref must follow the re-link")
}

// TestFlowModRelink_MetadataOverridesApplyWithoutARelink pins the
// apply-time half: no new source/id at all, just `lmm mod edit --name
// --version --author`.
func TestFlowModRelink_MetadataOverridesApplyWithoutARelink(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)

	j := runFlow(t, s, game, "mod_relink",
		`{"source_id":"`+fixtureSourceID+`","mod_id":"m1"}`,
		`{"name":"Renamed","author":"Somebody"}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "Renamed", mod.Name)
	assert.Equal(t, "Somebody", mod.Author)
}

// TestFlowModRelink_RequiresAModID pins the request's one validation.
func TestFlowModRelink_RequiresAModID(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/mod_relink", game), `{"source_id":"fake"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "mod_id")
}

// TestFlowModRelink_UnknownModIs404WithTheEnvelope is M3: a mod that is not
// installed cannot be planned, and PlanRelinkMod's failure IS
// domain.ErrModNotFound (it comes straight from GetInstalledMod) - so the
// plan boundary must answer 404, not the generic plan-failure 500 every
// other unclassified core error gets.
func TestFlowModRelink_UnknownModIs404WithTheEnvelope(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/mod_relink", game),
		`{"source_id":"`+fixtureSourceID+`","mod_id":"nope"}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, apiContentType, rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `"error"`)
}

// TestFlowModRelink_RejectsUnknownOptions pins the strict decode.
func TestFlowModRelink_RejectsUnknownOptions(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/mod_relink", game),
		`{"mod_id":"m1","new_source":"typo"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestFlowModRelink_UsedPlanIsRefused is the plan store's single-use rule.
func TestFlowModRelink_UsedPlanIsRefused(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	id, _ := planFlow(t, s, game, "mod_relink", relinkPlanBody)
	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	again := doAPI(s, http.MethodPost, "/api/v1/jobs", `{"plan_id":"`+string(id)+`"}`)
	assert.Equal(t, http.StatusConflict, again.Code, again.Body.String())
}

// TestFlowModRelink_RequiresCSRF.
func TestFlowModRelink_RequiresCSRF(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPIWithoutCSRF(s, http.MethodPost, scoped("/api/v1/plans/mod_relink", game), relinkPlanBody)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
