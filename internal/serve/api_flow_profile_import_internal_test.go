package serve

// The profile-import flow over /api/v1 (#332): POST
// /plans/profile_import then POST /jobs, with end-state assertions - the
// profile file really exists, and its pending mods are installed or
// counted as skipped according to the option the confirm modal set.

import (
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// importedProfileName is the profile the fixture's import document creates.
const importedProfileName = "imported"

// importDocument is an exported profile naming two mods: p1, which the
// fixture already has installed and cached (nothing to do), and p2, which
// no profile has installed at all (the plan's Missing bucket, and the mod
// an install-enabled apply must actually fetch).
const importDocument = `name: imported
game_id: g1
mods:
  - source_id: fake
    mod_id: p1
    version: "1.0"
  - source_id: fake
    mod_id: p2
    version: "2.0"
`

// importPlanBody wraps a profile document as the plan request's body.
func importPlanBody(t *testing.T, doc string) string {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Data string `json:"data"`
	}{Data: doc})
	require.NoError(t, err)
	return string(encoded)
}

// TestFlowProfileImport_PlanSortsTheModsAndImportsNothing is the plan half:
// the three buckets are on the wire as the frozen core.ImportPlan document,
// and no profile was created.
func TestFlowProfileImport_PlanSortsTheModsAndImportsNothing(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	_, raw := planFlow(t, s, game, "profile_import", importPlanBody(t, importDocument))

	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.NotNil(t, resp.Plan.Profile)
	assert.Equal(t, importedProfileName, resp.Plan.Profile.Name)
	assert.False(t, resp.Plan.Exists, "the profile does not exist yet")
	require.Len(t, resp.Plan.Installed, 1)
	assert.Equal(t, "p1", resp.Plan.Installed[0].ModID)
	require.Len(t, resp.Plan.Missing, 1)
	assert.Equal(t, "p2", resp.Plan.Missing[0].ModID)

	_, err := svc.NewProfileManager().Get(t.Context(), game.ID, importedProfileName)
	require.ErrorIs(t, err, domain.ErrProfileNotFound, "a plan must import nothing")
}

// TestFlowProfileImport_JobSavesTheProfileAndInstallsThePendingMod is the
// apply half with install on: the profile file exists and p2 is really
// installed under it.
func TestFlowProfileImport_JobSavesTheProfileAndInstallsThePendingMod(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	j := runFlow(t, s, game, "profile_import", importPlanBody(t, importDocument), `{"install":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	result, ok := j.status().Result.(*core.ProfileImportResult)
	require.True(t, ok, "the stored result must be the core document")
	assert.Equal(t, importedProfileName, result.ProfileName)
	assert.Equal(t, 1, result.Installed, "the missing mod must have been installed")
	assert.Equal(t, 0, result.Skipped)
	assert.Equal(t, 0, result.Failed, "warnings: %v", result.Warnings)

	ctx := t.Context()
	imported, err := svc.NewProfileManager().Get(ctx, game.ID, importedProfileName)
	require.NoError(t, err)
	require.Len(t, imported.Mods, 2)

	installed, err := svc.GetInstalledMod(ctx, fixtureSourceID, "p2", game.ID, importedProfileName)
	require.NoError(t, err, "the pending mod must now have an install row under the imported profile")
	assert.True(t, installed.Enabled)
}

// TestFlowProfileImport_JobWithoutInstallSavesTheProfileAndSkips is the
// declined half: the profile is still saved, and every pending mod is
// counted as skipped rather than silently dropped.
func TestFlowProfileImport_JobWithoutInstallSavesTheProfileAndSkips(t *testing.T) {
	s, svc, game := newProfilesFixtureServer(t)

	j := runFlow(t, s, game, "profile_import", importPlanBody(t, importDocument), "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	result, ok := j.status().Result.(*core.ProfileImportResult)
	require.True(t, ok)
	assert.Equal(t, 0, result.Installed)
	assert.Equal(t, 1, result.Skipped, "the pending mod must be counted, not dropped")

	ctx := t.Context()
	_, err := svc.NewProfileManager().Get(ctx, game.ID, importedProfileName)
	require.NoError(t, err, "the profile is saved whether or not its mods are installed")

	_, err = svc.GetInstalledMod(ctx, fixtureSourceID, "p2", game.ID, importedProfileName)
	require.Error(t, err, "nothing may have been installed")
}

// TestFlowProfileImport_NoInstallOverridesInstall pins the hard override
// the CLI's --no-install is.
func TestFlowProfileImport_NoInstallOverridesInstall(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	j := runFlow(t, s, game, "profile_import", importPlanBody(t, importDocument), `{"install":true,"no_install":true}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	result, ok := j.status().Result.(*core.ProfileImportResult)
	require.True(t, ok)
	assert.Equal(t, 0, result.Installed)
	assert.Equal(t, 1, result.Skipped)
}

// TestFlowProfileImport_ExistingProfileNeedsForce: the second import of the
// same document fails the job unless force is set, and reports why.
func TestFlowProfileImport_ExistingProfileNeedsForce(t *testing.T) {
	s, _, game := newProfilesFixtureServer(t)

	first := runFlow(t, s, game, "profile_import", importPlanBody(t, importDocument), "")
	require.Equal(t, jobSucceeded, first.status().State)

	// A fresh plan now reports the profile as existing...
	_, raw := planFlow(t, s, game, "profile_import", importPlanBody(t, importDocument))
	var resp struct {
		Plan core.ImportPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	assert.True(t, resp.Plan.Exists, "the plan must warn that the name is taken")

	// ...and applying without force fails rather than overwriting.
	second := runFlow(t, s, game, "profile_import", importPlanBody(t, importDocument), "")
	require.Equal(t, jobFailed, second.status().State)
	require.NotNil(t, second.status().Error)
	assert.Contains(t, second.status().Error.Error, "already exists")

	// With force, it succeeds.
	forced := runFlow(t, s, game, "profile_import", importPlanBody(t, importDocument), `{"force":true}`)
	require.Equal(t, jobSucceeded, forced.status().State, "job failed: %+v", forced.status().Error)
}

// TestFlowProfileImport_PlanRefusals: a missing or unparseable document is
// refused at the plan step with the JSON envelope, never as a job.
func TestFlowProfileImport_PlanRefusals(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"no body", ``},
		{"empty data", `{"data":""}`},
		{"unknown member", `{"data":"name: x","nope":1}`},
		{"unparseable document", `{"data":"\t: not yaml ["}`},
		// #332 M2: config.ImportProfile never checks Name, so a
		// parseable-but-nameless mapping used to plan 200 and only fail
		// once the job reached SaveProfile.
		{"nameless document", `{"data":"foo: bar"}`},
		// #332 M2 sibling (unit 6 re-review): config.ImportProfile never
		// checks GameID either, so a document with a name but no game_id -
		// or one for a DIFFERENT game than the one selected - used to plan
		// 200 too, and only fail once the job reached SaveProfile
		// ("importing profile: invalid game ID: value is empty"), or worse,
		// silently save under the WRONG game's directory (ImportWithOptions
		// saves profile.GameID from the document verbatim, never the
		// selected game's own ID).
		{"missing game_id", `{"data":"name: x"}`},
		{"mismatched game_id", `{"data":"name: x\ngame_id: some-other-game"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _, game := newProfilesFixtureServer(t)
			rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/profile_import", game), tc.body)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

			var env apiErrorEnvelope
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.NotEmpty(t, env.Error)
		})
	}
}

// TestProfileImportKind_IsRegistered: the kind is in the closed table, so
// an unknown-kind envelope names it.
func TestProfileImportKind_IsRegistered(t *testing.T) {
	assert.Contains(t, supportedPlanKinds(), "profile_import")
	assert.True(t, strings.Contains(strings.Join(supportedPlanKinds(), ","), "profile_import"))
}
