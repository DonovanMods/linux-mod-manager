package serve

// The install flow over /api/v1 - POST /api/v1/plans/install, then POST
// /api/v1/jobs - which is the entry point the SPA drives
// (docs/plans/2026-08-31-serve-spa-design.md §Architecture).
//
// Install is the mutation the whole job surface is shaped around, because
// its conflicts cannot be known until AFTER the download (see
// core.InstallOptions.AcceptConflicts). Its refusal therefore arrives from
// the Apply, as a failed job carrying *core.ConflictError, and the answer
// is a round trip: re-plan and re-apply with accept_conflicts against a
// cache the refused attempt already warmed.
//
// These tests are the ported outcome-asserting half of the deleted
// mutations_install_internal_test.go: same fixtures, same end-state
// assertions (database row, profile load order, deployed tree, the
// download counter), different entry point.

import (
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installPlanBody is the plan request naming one mod of the install
// fixture's source.
func installPlanBody(modID string) string {
	return `{"source_id":"` + fixtureSourceID + `","mod_id":"` + modID + `"}`
}

// TestFlowInstall_PlanInstallsNothing pins the half of Plan/Apply that is
// easy to lose: computing a plan must leave the world untouched.
func TestFlowInstall_PlanInstallsNothing(t *testing.T) {
	s, svc, game, _ := newInstallFixtureServer(t)

	_, raw := planFlow(t, s, game, "install", installPlanBody(installModID))
	assert.Contains(t, string(raw), "Better Boots", "the plan document names the mod")

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, installModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "a plan must install nothing")
	require.NoFileExists(t, deployedPath(game, installModFile), "a plan must deploy nothing either")
}

// TestFlowInstall_JobInstallsAndDeploys is the Apply half: the mod really
// downloads, lands in the database and the profile, and deploys.
func TestFlowInstall_JobInstallsAndDeploys(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)

	j := runFlow(t, s, game, "install", installPlanBody(installModID), "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	installed, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, installModID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", installed.Version, "the primary file's version is what gets recorded")

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Len(t, profile.Mods, 2, "the profile's load order gains the new mod")

	// The tree, not just the record: an install that stopped deploying
	// would leave every assertion above green.
	require.FileExists(t, deployedPath(game, installModFile))
	assert.Equal(t, "payload for m2/f2", deployedContent(t, game, installModFile),
		"the deployed link must resolve to the PRIMARY file's cached content")

	assert.Positive(t, src.downloadCount(), "the install must really have downloaded")
}

// TestFlowInstall_FileSelectionIsHonoured pins #225's selection onto core's
// own option: asking for the 1.0 file installs 1.0, not the primary. The
// confirm page's checkboxes are gone with the page layer; the apply-time
// options they submitted are the same ones the SPA sends.
func TestFlowInstall_FileSelectionIsHonoured(t *testing.T) {
	s, svc, game, _ := newInstallFixtureServer(t)

	j := runFlow(t, s, game, "install", installPlanBody(installModID),
		`{"version":"1.0","file_ids":["f1"]}`)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	installed, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, installModID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, "payload for m2/f1", deployedContent(t, game, installModFile),
		"the SELECTED file's content is what reached the game directory")
}

// TestFlowInstall_ConflictRoundTrip_OverwriteIsDownloadFree is the heart of
// the install flow. Installing a mod whose archive holds a path another
// installed mod already deployed fails the job with *core.ConflictError;
// the stored job keeps both the typed error and the wire envelope's
// Details, which is what a client renders its Overwrite offer from; taking
// it re-plans, re-applies with accept_conflicts, and installs - having
// downloaded nothing the second time, because the refused attempt left the
// cache warm (core.InstallOptions.AcceptConflicts).
func TestFlowInstall_ConflictRoundTrip_OverwriteIsDownloadFree(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	deployFixtureProfile(t, s, game)
	require.FileExists(t, deployedFixturePath(game))

	j := runFlow(t, s, game, "install", installPlanBody(conflictModID), "")
	require.Equal(t, jobFailed, j.status().State, "the conflict gate must refuse this install")

	var conflictErr *core.ConflictError
	require.ErrorAs(t, j.failure(), &conflictErr, "the job must store the typed conflict error")
	require.NotNil(t, j.status().Error, "the failed job must carry the wire envelope")
	require.NotNil(t, j.status().Error.Details, "the stored envelope must keep Details()")

	// The envelope a client reads names the contested path and the mod that
	// owns it - the material the Overwrite offer is built from.
	status := doAPI(s, http.MethodGet, "/api/v1/jobs/"+string(j.status().ID), "")
	require.Equal(t, http.StatusOK, status.Code)
	assert.Contains(t, status.Body.String(), deployFixtureFile, "the conflicting path must be on the wire")
	assert.Contains(t, status.Body.String(), "m1", "so must the mod that owns it")

	// Nothing was installed: a refused conflict leaves a warm cache and
	// nothing else.
	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, conflictModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
	afterRefusal := src.downloadCount()
	require.Positive(t, afterRefusal, "the refused attempt did download, which is why the cache is warm")

	// Take the overwrite: a fresh plan, applied with the decision set.
	oj := runFlow(t, s, game, "install", installPlanBody(conflictModID), `{"accept_conflicts":true}`)
	require.Equal(t, jobSucceeded, oj.status().State, "overwrite failed: %+v", oj.status().Error)

	installed, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, conflictModID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, "payload for m3/c1", deployedContent(t, game, deployFixtureFile),
		"an overwrite means the contested path now resolves to the NEW mod's file")
	assert.Equal(t, afterRefusal, src.downloadCount(),
		"the overwrite re-run must download nothing: the refused attempt already filled the cache")
}

// TestFlowInstall_StalePlan_FailsTheJobAndAFreshPlanSucceeds covers
// core.ErrStalePlan: a plan computed before the installed set moved is
// refused by its own Apply, applies nothing, and a fresh plan goes through.
func TestFlowInstall_StalePlan_FailsTheJobAndAFreshPlanSucceeds(t *testing.T) {
	s, svc, game, _ := newInstallFixtureServer(t)
	id, _ := planFlow(t, s, game, "install", installPlanBody(installModID))

	// Move the installed set out from under the plan.
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m9", SourceID: fixtureSourceID, Name: "Interloper", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))

	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobFailed, j.status().State)
	require.ErrorIs(t, j.failure(), core.ErrStalePlan)

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, installModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "a stale plan must not have applied")

	// Re-planning is the whole recovery: the fresh plan applies.
	fresh := runFlow(t, s, game, "install", installPlanBody(installModID), "")
	require.Equal(t, jobSucceeded, fresh.status().State, "re-plan failed: %+v", fresh.status().Error)
	_, err = svc.GetInstalledMod(t.Context(), fixtureSourceID, installModID, game.ID, "default")
	require.NoError(t, err)
}

// TestFlowInstall_WithoutCSRF_IsRefused pins the CSRF rule on the plan
// endpoint: no token, no plan, and nothing installed.
func TestFlowInstall_WithoutCSRF_IsRefused(t *testing.T) {
	s, svc, game, _ := newInstallFixtureServer(t)

	rec := doAPIWithoutCSRF(s, http.MethodPost, scoped("/api/v1/plans/install", game), installPlanBody(installModID))
	require.Equal(t, http.StatusForbidden, rec.Code)

	_, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, installModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
}
