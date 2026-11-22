package serve

// #666 over /api/v1: POST /plans/profile_import then POST /jobs with
// {"switch":true} - the import modal's "Switch to this profile when the
// import finishes" - ends with the imported profile active and deployed,
// and the profile active before it taken down.

import (
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlowProfileImport_SwitchMakesTheImportedProfileActiveAndDeployed(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	j := runFlow(t, s, game, "profile_import", importPlanBody(t, importDocument), `{"install":true,"switch":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	result, ok := j.status().Result.(*core.ProfileImportResult)
	require.True(t, ok)
	assert.True(t, result.RecordedOnly, "the import itself recorded the profile")
	assert.Equal(t, 2, result.Recorded)
	require.NotNil(t, result.Switch, "the result reports the switch beside the import")
	assert.True(t, result.Switch.Switched)
	assert.Empty(t, result.Switch.Error)
	assert.Equal(t, activeProfile, result.Switch.From)
	assert.Equal(t, importedProfileName, result.Switch.To)

	ctx := t.Context()
	live, err := svc.NewProfileManager().GetDefault(ctx, game.ID)
	require.NoError(t, err)
	assert.Equal(t, importedProfileName, live.Name, "the imported profile is the active one")
	for _, modID := range []string{"p1", "p2"} {
		assert.FileExists(t, deployedPath(game, profileModFile(modID)), "%s is deployed", modID)
		row, err := svc.GetInstalledMod(ctx, fixtureSourceID, modID, game.ID, importedProfileName)
		require.NoError(t, err)
		assert.True(t, row.Deployed, "%s's row is deployed", modID)
	}
	assert.NoFileExists(t, deployedPath(game, profileModFile("p3")), "the old profile's own mod came down")
}

func TestFlowProfileImport_WithoutSwitchTheActiveProfileStays(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	j := runFlow(t, s, game, "profile_import", importPlanBody(t, importDocument), `{"install":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	result, ok := j.status().Result.(*core.ProfileImportResult)
	require.True(t, ok)
	assert.Nil(t, result.Switch)
	live, err := svc.NewProfileManager().GetDefault(t.Context(), game.ID)
	require.NoError(t, err)
	assert.Equal(t, activeProfile, live.Name)
	assert.FileExists(t, deployedPath(game, profileModFile("p3")))
}

func TestFlowProfileImport_SwitchWithNoInstallIsRefused(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	planID, _ := planFlow(t, s, game, "profile_import", importPlanBody(t, importDocument))
	rec := doAPI(s, http.MethodPost, "/api/v1/jobs", `{"plan_id":"`+string(planID)+`","options":{"switch":true,"no_install":true}}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "no_install")
	_, err := svc.NewProfileManager().Get(t.Context(), game.ID, importedProfileName)
	assert.Error(t, err, "nothing was imported")
}
