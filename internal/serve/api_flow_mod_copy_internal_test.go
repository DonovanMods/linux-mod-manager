package serve

// The mod-copy flow over /api/v1 (#437): POST /api/v1/plans/mod_copy ->
// POST /api/v1/jobs, `lmm mod copy` as the SPA drives it. The plan is
// computed for the SELECTED profile (?profile=, the copy's From) and names
// its destination in the body ("to"). The end state asserted is the
// destination's profile document, its installed rows and - for an active
// destination - the deployed tree; and, throughout, that the source was
// never asked anything: the cache already holds the bytes.
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

// TestFlowModCopy_IntoAnInactiveProfileRecordsOnly: p3 goes from the active
// profile into "modded" - listed there and installed there, enabled, but
// not deployed (the game directory is default's), and the source untouched.
func TestFlowModCopy_IntoAnInactiveProfileRecordsOnly(t *testing.T) {
	s, svc, game, src := newProfilesFixtureServerWithSource(t)
	before := src.calls.Load()

	id, plan := planCopy(t, s, game, activeProfile, copyBody(switchTargetProfile, "p3"))
	assert.Equal(t, activeProfile, plan.From)
	assert.Equal(t, switchTargetProfile, plan.To)
	assert.False(t, plan.DestinationActive, "modded is not the active profile")
	require.Len(t, plan.Mods, 1)
	assert.Equal(t, "p3", plan.Mods[0].ModID)
	assert.False(t, plan.Mods[0].AlreadyListed)

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	ctx := t.Context()
	profile, err := svc.NewProfileManager().Get(ctx, game.ID, switchTargetProfile)
	require.NoError(t, err)
	ref := profile.FindRef(fixtureSourceID, "p3")
	require.NotNil(t, ref, "the destination's document lists the copy")
	assert.Equal(t, "1.0", ref.Version)

	row, err := svc.GetInstalledMod(ctx, fixtureSourceID, "p3", game.ID, switchTargetProfile)
	require.NoError(t, err, "the destination has its own row")
	assert.True(t, row.Enabled)
	assert.False(t, row.Deployed, "an inactive destination is recorded only")

	src3, err := svc.GetInstalledMod(ctx, fixtureSourceID, "p3", game.ID, activeProfile)
	require.NoError(t, err, "the source profile keeps its own row")
	assert.True(t, src3.Deployed)
	assert.Equal(t, before, src.calls.Load(), "a copy never contacts the source")
}

// TestFlowModCopy_IntoTheActiveProfileDeploys: p4 goes from "curated" into
// the active profile, so the copy links its cached file into the game
// directory and marks the new row deployed.
func TestFlowModCopy_IntoTheActiveProfileDeploys(t *testing.T) {
	s, svc, game, src := newProfilesFixtureServerWithSource(t)
	before := src.calls.Load()
	require.NoFileExists(t, deployedPath(game, profileModFile("p4")))

	id, plan := planCopy(t, s, game, applyTargetProfile, copyBody(activeProfile, "p4"))
	assert.True(t, plan.DestinationActive)

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	result, ok := j.status().Result.(*core.CopyModResult)
	require.True(t, ok, "the job's result is core.CopyModResult, got %T", j.status().Result)
	require.Len(t, result.Copied, 1)
	assert.True(t, result.Copied[0].Deployed)

	assert.Equal(t, "p4 payload", deployedContent(t, game, profileModFile("p4")),
		"the copy is deployed from the cache into the game directory")
	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "p4", game.ID, activeProfile)
	require.NoError(t, err)
	assert.True(t, row.Deployed)
	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, activeProfile)
	require.NoError(t, err)
	assert.NotNil(t, profile.FindRef(fixtureSourceID, "p4"))
	assert.Equal(t, before, src.calls.Load(), "a copy never contacts the source")
}

// TestFlowModCopy_AlreadyListedIsRefusedThenReplaced is Ruling 1's round
// trip: "curated" already lists p1, which the plan says; a job without
// replace fails with the typed refusal and writes nothing, and a re-plan
// applied with {"replace":true} records the copy.
func TestFlowModCopy_AlreadyListedIsRefusedThenReplaced(t *testing.T) {
	s, svc, game, src := newProfilesFixtureServerWithSource(t)
	before := src.calls.Load()

	id, plan := planCopy(t, s, game, activeProfile, copyBody(applyTargetProfile, "p1"))
	require.Len(t, plan.Mods, 1)
	assert.True(t, plan.Mods[0].AlreadyListed, "the plan says the destination lists it already")

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobFailed, j.status().State)
	require.NotNil(t, j.status().Error)
	assert.Contains(t, j.status().Error.Error, "already lists")
	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "p1", game.ID, applyTargetProfile)
	require.ErrorIs(t, err, domain.ErrModNotFound, "a refused copy writes nothing")

	id, _ = planCopy(t, s, game, activeProfile, copyBody(applyTargetProfile, "p1"))
	j = startFlowJob(t, s, id, `{"replace":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	row, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "p1", game.ID, applyTargetProfile)
	require.NoError(t, err, "the replace wrote the destination's row")
	assert.False(t, row.Deployed)
	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, applyTargetProfile)
	require.NoError(t, err)
	n := 0
	for _, r := range profile.Mods {
		if r.ModID == "p1" {
			n++
		}
	}
	assert.Equal(t, 1, n, "a replace keeps one ref, in place")
	result, ok := j.status().Result.(*core.CopyModResult)
	require.True(t, ok)
	require.Len(t, result.Copied, 1)
	assert.True(t, result.Copied[0].Replaced)
	assert.Equal(t, before, src.calls.Load(), "a copy never contacts the source")
}

// TestFlowModCopy_CarriesTheLock: a locked source ref is copied locked.
func TestFlowModCopy_CarriesTheLock(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	_, err := svc.SetModLock(t.Context(), fixtureSourceID, "p3", game.ID, activeProfile, "")
	require.NoError(t, err)

	id, plan := planCopy(t, s, game, activeProfile, copyBody(switchTargetProfile, "p3"))
	require.Len(t, plan.Mods, 1)
	assert.True(t, plan.Mods[0].Locked)
	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, switchTargetProfile)
	require.NoError(t, err)
	ref := profile.FindRef(fixtureSourceID, "p3")
	require.NotNil(t, ref)
	assert.True(t, ref.Locked, "the copy carries the lock")
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
// every mod default lists goes into modded, the one modded already lists
// (p1) skipped without replace.
func TestFlowModCopy_AllCopiesEveryListedMod(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

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

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "p3", game.ID, switchTargetProfile)
	require.NoError(t, err)
}

// TestFlowModCopy_RequiresCSRF.
func TestFlowModCopy_RequiresCSRF(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	rec := doAPIWithoutCSRF(s, http.MethodPost, scopedTo("/api/v1/plans/mod_copy", game, activeProfile), copyBody(switchTargetProfile, "p3"))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
