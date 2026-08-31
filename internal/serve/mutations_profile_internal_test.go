package serve

// Task 9 flows 2 and 3: profile switch and profile apply
// (docs/plans/2026-08-30-serve-impl.md Task 9). Both are Plan -> confirm ->
// job like every other flow; what is specific to them is the warning
// surface #294 exists for - the diagnostics that must reach a user
// unconditionally, both BEFORE they commit (what the plan already knows)
// and AFTER (what the result reports).

import (
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_ProfileSwitch_ConfirmSurfacesThePlanAndItsWarnings is the plan
// half: the diff is on the page, the locked ref that will refuse its
// profile write is called out before anything runs, and nothing has moved.
func TestServer_ProfileSwitch_ConfirmSurfacesThePlanAndItsWarnings(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	rec := postForm(s, "/profiles/"+switchTargetProfile+"/switch", formValues{"game": game.ID})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, activeProfile)
	assert.Contains(t, body, switchTargetProfile)
	assert.Contains(t, body, "P3", "the mod the switch would disable must be named")
	assert.Contains(t, body, "fake:p2", "the mod the switch would install must be named (a plan's install list carries refs, not names)")
	assert.Contains(t, body, "locked", "a locked ref is warning-class: its profile record cannot be updated")
	assert.Regexp(t, `name="plan_id" value="[0-9a-f]{32}"`, body)

	active, err := svc.NewProfileManager().GetDefault(t.Context(), game.ID)
	require.NoError(t, err)
	assert.Equal(t, activeProfile, active.Name, "a plan must not switch anything")
}

// TestServer_ProfileSwitch_ConfirmRunsTheJobAndSwitches is the apply half:
// the active profile really moves, the dropped mod is undeployed, the new
// one is installed and deployed, and the #294 warning the locked ref
// produces is carried on the STORED job result - not just emitted and lost.
func TestServer_ProfileSwitch_ConfirmRunsTheJobAndSwitches(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	entry := postForm(s, "/profiles/"+switchTargetProfile+"/switch", formValues{"game": game.ID})
	require.Equal(t, http.StatusOK, entry.Code)

	rec := postForm(s, "/profiles/"+switchTargetProfile+"/switch", formValues{
		"game": game.ID, "confirm": "1", "plan_id": hiddenField(t, entry.Body.String(), "plan_id"),
	})

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	active, err := svc.NewProfileManager().GetDefault(t.Context(), game.ID)
	require.NoError(t, err)
	assert.Equal(t, switchTargetProfile, active.Name, "the switch must change the active profile")

	require.FileExists(t, deployedPath(game, profileModFile("p2")), "the target profile's new mod must be deployed")
	require.NoFileExists(t, deployedPath(game, profileModFile("p3")), "a mod the target profile omits must be undeployed")

	result, ok := j.status().Result.(*core.SwitchResult)
	require.True(t, ok, "the stored result must be the core document")
	require.NotEmpty(t, result.Warnings, "#294: the refused profile write must reach the result, not just the event stream")
	assert.Contains(t, result.Warnings[0], "could not update profile")

	page := getPage(s, "/jobs/"+string(j.status().ID))
	require.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), "could not update profile",
		"and the job page must show it rather than hiding it in the JSON")
}

// TestServer_ProfileSwitch_SyncFallback_MutatesIdentically is the no-JS
// path for the switch.
func TestServer_ProfileSwitch_SyncFallback_MutatesIdentically(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	entry := postForm(s, "/profiles/"+switchTargetProfile+"/switch", formValues{"game": game.ID})
	require.Equal(t, http.StatusOK, entry.Code)

	rec := postForm(s, "/profiles/"+switchTargetProfile+"/switch?sync=1", formValues{
		"game": game.ID, "confirm": "1", "plan_id": hiddenField(t, entry.Body.String(), "plan_id"),
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "could not update profile", "the inline result carries the warning too")

	active, err := svc.NewProfileManager().GetDefault(t.Context(), game.ID)
	require.NoError(t, err)
	assert.Equal(t, switchTargetProfile, active.Name)
}

// TestServer_ProfileSwitch_WithoutCSRF_IsRefused pins the CSRF rule on the
// switch route, and that a refused request switched nothing.
func TestServer_ProfileSwitch_WithoutCSRF_IsRefused(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	rec := postFormWithoutCSRF(s, "/profiles/"+switchTargetProfile+"/switch", formValues{
		"game": game.ID, "confirm": "1",
	})

	require.Equal(t, http.StatusForbidden, rec.Code)
	active, err := svc.NewProfileManager().GetDefault(t.Context(), game.ID)
	require.NoError(t, err)
	assert.Equal(t, activeProfile, active.Name)
}

// TestServer_ProfileApply_ConfirmListsInstallsAndRemovals is the apply
// plan: what would be installed, what would be removed, and the entry the
// source could not resolve - which the plan carries as data rather than
// failing on, so it has to be shown before the user commits.
func TestServer_ProfileApply_ConfirmListsInstallsAndRemovals(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	rec := postForm(s, "/profiles/"+applyTargetProfile+"/apply", formValues{"game": game.ID})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "P1", "the mod that would be installed must be named")
	assert.Contains(t, body, "P4", "the mod that would be removed must be named")
	assert.Contains(t, body, unresolvableModID, "the unresolvable entry must be named")
	assert.Contains(t, body, "could not be resolved")
	assert.Regexp(t, `name="plan_id" value="[0-9a-f]{32}"`, body)

	installed, err := svc.GetInstalledMod(t.Context(), "fake", "p4", game.ID, applyTargetProfile)
	require.NoError(t, err)
	assert.True(t, installed.Enabled, "a plan must disable nothing")
}

// TestServer_ProfileApply_ConfirmRunsTheJobAndConverges is the apply half:
// the listed mod is installed, the unlisted one is disabled and undeployed,
// and the entry that could not resolve is reported rather than silently
// dropped.
func TestServer_ProfileApply_ConfirmRunsTheJobAndConverges(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	entry := postForm(s, "/profiles/"+applyTargetProfile+"/apply", formValues{"game": game.ID})
	require.Equal(t, http.StatusOK, entry.Code)

	rec := postForm(s, "/profiles/"+applyTargetProfile+"/apply", formValues{
		"game": game.ID, "confirm": "1", "plan_id": hiddenField(t, entry.Body.String(), "plan_id"),
	})

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	installedP1, err := svc.GetInstalledMod(t.Context(), "fake", "p1", game.ID, applyTargetProfile)
	require.NoError(t, err, "the profile's listed mod must now be installed under it")
	assert.True(t, installedP1.Enabled)

	installedP4, err := svc.GetInstalledMod(t.Context(), "fake", "p4", game.ID, applyTargetProfile)
	require.NoError(t, err)
	assert.False(t, installedP4.Enabled, "a mod the profile no longer lists must be disabled")

	result, ok := j.status().Result.(*core.ProfileApplyResult)
	require.True(t, ok, "the stored result must be the core document")
	require.NotEmpty(t, result.Failed, "the unresolvable entry must be reported on the result")
	assert.Equal(t, unresolvableModID, result.Failed[0].ModID)

	page := getPage(s, "/jobs/"+string(j.status().ID))
	require.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), unresolvableModID)
}

// TestServer_ProfileApply_SyncFallback_MutatesIdentically is the no-JS path
// for the apply.
func TestServer_ProfileApply_SyncFallback_MutatesIdentically(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)
	entry := postForm(s, "/profiles/"+applyTargetProfile+"/apply", formValues{"game": game.ID})
	require.Equal(t, http.StatusOK, entry.Code)

	rec := postForm(s, "/profiles/"+applyTargetProfile+"/apply?sync=1", formValues{
		"game": game.ID, "confirm": "1", "plan_id": hiddenField(t, entry.Body.String(), "plan_id"),
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Done.")

	installedP4, err := svc.GetInstalledMod(t.Context(), "fake", "p4", game.ID, applyTargetProfile)
	require.NoError(t, err)
	assert.False(t, installedP4.Enabled)
}

// TestServer_ProfileApply_WithoutCSRF_IsRefused pins the CSRF rule on the
// apply route, and that a refused request converged nothing.
func TestServer_ProfileApply_WithoutCSRF_IsRefused(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	rec := postFormWithoutCSRF(s, "/profiles/"+applyTargetProfile+"/apply", formValues{
		"game": game.ID, "confirm": "1",
	})

	require.Equal(t, http.StatusForbidden, rec.Code)
	installed, err := svc.GetInstalledMod(t.Context(), "fake", "p4", game.ID, applyTargetProfile)
	require.NoError(t, err)
	assert.True(t, installed.Enabled)
}

// TestServer_Profiles_PageOffersTheSwitchAndApplyForms proves Task 4's
// disabled shells are live - all three of them, with nothing left disabled
// on the page.
func TestServer_Profiles_PageOffersTheSwitchAndApplyForms(t *testing.T) {
	s, _, _ := newProfilesFixtureServer(t)

	rec := getPage(s, "/profiles")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `action="/profiles/`+switchTargetProfile+`/switch"`)
	assert.Contains(t, body, `action="/profiles/`+applyTargetProfile+`/apply"`)
	// Deploy carries no decorative {name} - the row scopes it with a hidden
	// "profile" field instead (gate review Minor 1).
	assert.Contains(t, body, `action="/deploy"`)
	assert.Contains(t, body, `name="profile" value="`+activeProfile+`"`)
	assert.NotContains(t, body, "disabled")
	assert.NotContains(t, body, "coming in this release")
}
