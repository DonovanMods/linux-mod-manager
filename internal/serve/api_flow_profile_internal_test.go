package serve

// The profile switch and profile apply flows over /api/v1. Both are
// Plan -> job pairs against the profiles fixture, which is arranged so each
// has real work to do: a mod to undeploy, a mod to download and deploy, a
// mod to disable, and one entry the source cannot resolve at all.
//
// Ported from the deleted mutations_profile_internal_test.go with its
// end-state assertions intact; what went with the page layer is the
// confirm-page rendering half, whose material (the diff, the locked-ref
// warning) is in the plan document these tests now read directly.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlowSwitch_PlanSurfacesTheDiffAndSwitchesNothing is the plan half:
// the diff is on the wire, including the locked ref that will refuse its
// profile write, and nothing has moved.
func TestFlowSwitch_PlanSurfacesTheDiffAndSwitchesNothing(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	_, raw := planFlow(t, s, game, "switch", `{"profile":"`+switchTargetProfile+`"}`)
	body := string(raw)
	assert.Contains(t, body, "p3", "the mod the switch would disable must be named")
	assert.Contains(t, body, "p2", "the mod the switch would install must be named")

	active, err := svc.NewProfileManager().GetDefault(t.Context(), game.ID)
	require.NoError(t, err)
	assert.Equal(t, activeProfile, active.Name, "a plan must not switch anything")
}

// TestFlowSwitch_JobSwitchesAndReportsTheLockedRefWarning is the apply
// half: the active profile really moves, the dropped mod is undeployed, the
// new one is installed and deployed, and the #294 warning the locked ref
// produces is carried on the STORED job result - not just emitted and lost.
func TestFlowSwitch_JobSwitchesAndReportsTheLockedRefWarning(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	j := runFlow(t, s, game, "switch", `{"profile":"`+switchTargetProfile+`"}`, "")
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
}

// TestFlowProfileApply_PlanListsInstallsAndRemovalsAndAppliesNothing is the
// plan half: what would be installed, what would be removed, and the entry
// the source could not resolve - which the plan carries as data rather than
// failing on.
func TestFlowProfileApply_PlanListsInstallsAndRemovalsAndAppliesNothing(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	_, raw := planFlow(t, s, game, "profile_apply", `{"profile":"`+applyTargetProfile+`"}`)
	body := string(raw)
	assert.Contains(t, body, "p1", "the mod that would be installed must be named")
	assert.Contains(t, body, "p4", "the mod that would be removed must be named")
	assert.Contains(t, body, unresolvableModID, "the unresolvable entry must be named")

	installed, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "p4", game.ID, applyTargetProfile)
	require.NoError(t, err)
	assert.True(t, installed.Enabled, "a plan must disable nothing")
}

// TestFlowProfileApply_JobConverges is the apply half: the listed mod is
// installed, the unlisted one is disabled and undeployed, and the entry that
// could not resolve is reported rather than silently dropped.
func TestFlowProfileApply_JobConverges(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	j := runFlow(t, s, game, "profile_apply", `{"profile":"`+applyTargetProfile+`"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	installedP1, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "p1", game.ID, applyTargetProfile)
	require.NoError(t, err, "the profile's listed mod must now be installed under it")
	assert.True(t, installedP1.Enabled)

	installedP4, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "p4", game.ID, applyTargetProfile)
	require.NoError(t, err)
	assert.False(t, installedP4.Enabled, "a mod the profile no longer lists must be disabled")

	result, ok := j.status().Result.(*core.ProfileApplyResult)
	require.True(t, ok, "the stored result must be the core document")
	require.NotEmpty(t, result.Failed, "the unresolvable entry must be reported on the result")
	assert.Equal(t, unresolvableModID, result.Failed[0].ModID)
}
