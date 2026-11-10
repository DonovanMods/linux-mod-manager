package serve

// The mod-copy flow over /api/v1 (#437): POST /api/v1/plans/mod_copy ->
// POST /api/v1/jobs, `lmm mod copy` as the SPA drives it. The plan is
// computed for the SELECTED profile (?profile=, the copy's From) and names
// its destination in the body ("to"). What is asserted here is the wire:
// the request's members reaching the plan, the job's options reaching
// Apply, the result document and the HTTP status of a refusal. The copy's
// end state - rows, profile documents, the deployed tree, the lock carried
// over, a source never contacted - is internal/core's mod_copy_test.go.
//
// The world is the profiles fixture (profiles_fixture_internal_test.go):
// "default" is active with p1 and p3 deployed; "modded" lists p1 and p2
// with no rows; "curated" has p4 installed but unlisted, and lists p1.

import (
	"encoding/json/v2"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopedTo is scoped() for a profile other than "default": the copy's From
// is the selected profile, so a copy out of another profile names it here.
func scopedTo(target string, game *domain.Game, profile string) string {
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	return target + sep + gameParam + "=" + url.QueryEscape(game.ID) + "&" + profileParam + "=" + url.QueryEscape(profile)
}

// planCopy drives POST /api/v1/plans/mod_copy out of profile from and
// returns the handle and the decoded plan document.
func planCopy(t *testing.T, s *Server, game *domain.Game, from, body string) (planID, core.CopyModPlan) {
	t.Helper()
	rec := doAPI(s, http.MethodPost, scopedTo("/api/v1/plans/mod_copy", game, from), body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		PlanID planID           `json:"plan_id"`
		Kind   string           `json:"kind"`
		Plan   core.CopyModPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "mod_copy", resp.Kind)
	require.NotEmpty(t, resp.PlanID)
	return resp.PlanID, resp.Plan
}

// copyBody is a single-mod copy request into to.
func copyBody(to, modID string) string {
	return `{"to":"` + to + `","source_id":"` + fixtureSourceID + `","mod_id":"` + modID + `"}`
}

// TestFlowModCopy_IntoTheActiveProfileDeploys is the plan -> job happy
// path: p4 goes from "curated" (the selected profile) into the active one,
// and the job's result is core's CopyModResult, reporting the deploy.
func TestFlowModCopy_IntoTheActiveProfileDeploys(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	id, plan := planCopy(t, s, game, applyTargetProfile, copyBody(activeProfile, "p4"))
	assert.Equal(t, applyTargetProfile, plan.From, "the selected profile is the copy's From")
	assert.Equal(t, activeProfile, plan.To)
	assert.True(t, plan.DestinationActive)
	require.Len(t, plan.Mods, 1)
	assert.Equal(t, "p4", plan.Mods[0].ModID)

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	result, ok := j.status().Result.(*core.CopyModResult)
	require.True(t, ok, "the job's result is core.CopyModResult, got %T", j.status().Result)
	require.Len(t, result.Copied, 1)
	assert.True(t, result.Copied[0].Deployed)
}

// TestFlowModCopy_AlreadyListedIsRefusedThenReplaced is Ruling 1's round
// trip over the wire: "curated" already lists p1, which the plan says; a
// job without replace fails with the typed refusal, and a re-plan applied
// with {"replace":true} - the job option this pins reaching Apply -
// succeeds as a replace.
func TestFlowModCopy_AlreadyListedIsRefusedThenReplaced(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	id, plan := planCopy(t, s, game, activeProfile, copyBody(applyTargetProfile, "p1"))
	require.Len(t, plan.Mods, 1)
	assert.True(t, plan.Mods[0].AlreadyListed, "the plan says the destination lists it already")

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobFailed, j.status().State)
	require.NotNil(t, j.status().Error)
	assert.Contains(t, j.status().Error.Error, "already lists")

	id, _ = planCopy(t, s, game, activeProfile, copyBody(applyTargetProfile, "p1"))
	j = startFlowJob(t, s, id, `{"replace":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.CopyModResult)
	require.True(t, ok)
	require.Len(t, result.Copied, 1)
	assert.True(t, result.Copied[0].Replaced)
}

// TestFlowModCopy_ExternalModIsRefusedAt409: a Steam Workshop item is
// game-global, so the plan refuses it as the game's state, with the reason.
func TestFlowModCopy_ExternalModIsRefusedAt409(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "w1", SourceID: fixtureSourceID, Name: "Workshop Item", Version: "1", GameID: game.ID},
		ProfileName:  activeProfile,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		External:     true,
		Deployed:     true,
	}))

	rec := doAPI(s, http.MethodPost, scopedTo("/api/v1/plans/mod_copy", game, activeProfile), copyBody(switchTargetProfile, "w1"))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var env apiErrorEnvelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Contains(t, env.Error, "game-global")

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "w1", game.ID, switchTargetProfile)
	require.ErrorIs(t, err, domain.ErrModNotFound)
}

// TestFlowModCopy_PlanRefusals pins the request boundary: a destination is
// required, as is a mod (or all), a copy into the selected profile itself
// is bad input, and an unknown destination or mod is 404.
func TestFlowModCopy_PlanRefusals(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"no destination", `{"source_id":"fake","mod_id":"p1"}`, http.StatusBadRequest},
		{"no mod", `{"to":"modded"}`, http.StatusBadRequest},
		{"mod and all", `{"to":"modded","source_id":"fake","mod_id":"p1","all":true}`, http.StatusBadRequest},
		{"unknown option", `{"to":"modded","source_id":"fake","mod_id":"p1","overwrite":true}`, http.StatusBadRequest},
		{"into itself", copyBody(activeProfile, "p1"), http.StatusBadRequest},
		{"unknown destination", copyBody("ghost", "p1"), http.StatusNotFound},
		{"unknown mod", copyBody(switchTargetProfile, "nope"), http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _, game := newProfilesFixtureServer(t)
			rec := doAPI(s, http.MethodPost, scopedTo("/api/v1/plans/mod_copy", game, activeProfile), tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
		})
	}
}

// TestFlowModCopy_AllCopiesEveryListedMod: the CLI's --all, over the wire -
// the "all" member reaches the plan, and the result reports every mod
// default lists, the one modded already lists (p1) skipped without replace.
func TestFlowModCopy_AllCopiesEveryListedMod(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	id, plan := planCopy(t, s, game, activeProfile, `{"to":"`+switchTargetProfile+`","all":true}`)
	assert.True(t, plan.All)
	require.Len(t, plan.Mods, 2)

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	result, ok := j.status().Result.(*core.CopyModResult)
	require.True(t, ok)
	require.Len(t, result.Copied, 1)
	assert.Equal(t, "p3", result.Copied[0].ModID)
	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "p1", result.Skipped[0].ModID)
}

// TestFlowModCopy_RequiresCSRF.
func TestFlowModCopy_RequiresCSRF(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	rec := doAPIWithoutCSRF(s, http.MethodPost, scopedTo("/api/v1/plans/mod_copy", game, activeProfile), copyBody(switchTargetProfile, "p3"))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
