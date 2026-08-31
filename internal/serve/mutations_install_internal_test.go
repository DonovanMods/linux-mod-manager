package serve

// Task 8 flow 3: install (docs/plans/2026-08-30-serve-impl.md Task 8) - the
// flow the whole unit is shaped around, because install is the one mutation
// whose conflicts cannot be known until AFTER the download (see
// core.InstallOptions.AcceptConflicts). Its refusal therefore arrives from
// the Apply, as a failed job, and the answer is a round trip: the job page
// renders the stored conflict list and offers Overwrite, which re-plans and
// re-applies with AcceptConflicts against a cache that is already warm.

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServer_ModInstall_EntryPostRendersVersionsAndFiles is #225: the
// confirm page offers the versions AvailableModVersions reports and a file
// picker, because this mod's candidate pool really does hold more than one
// file.
func TestServer_ModInstall_EntryPostRendersVersionsAndFiles(t *testing.T) {
	s, svc, game, _ := newInstallFixtureServer(t)

	rec := postForm(s, "/mods/fake/"+installModID+"/install", formValues{"game": game.ID, "profile": "default"})

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Better Boots")
	assert.Contains(t, body, `name="version"`, "#225: the version select must render")
	assert.Contains(t, body, `<option value="1.0"`)
	assert.Contains(t, body, `<option value="2.0"`)
	assert.Contains(t, body, `name="file" value="f1"`, "#225: the file picker must offer each candidate")
	assert.Contains(t, body, `name="file" value="f2"`)
	assert.Regexp(t, `name="plan_id" value="[0-9a-f]{32}"`, body)

	_, err := svc.GetInstalledMod(t.Context(), "fake", installModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "a plan must install nothing")
}

// TestServer_ModInstall_SingleCandidate_OffersNoFilePicker is the other half
// of #225's "file selection only when the plan offers it": a mod with one
// candidate file renders no picker at all.
func TestServer_ModInstall_SingleCandidate_OffersNoFilePicker(t *testing.T) {
	s, _, game, _ := newInstallFixtureServer(t)

	rec := postForm(s, "/mods/fake/"+conflictModID+"/install", formValues{"game": game.ID, "profile": "default"})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), `name="file"`)
}

// TestServer_ModInstall_ConfirmRunsTheJobAndInstalls is the Apply half: the
// mod really downloads, lands in the database and the profile, and deploys.
func TestServer_ModInstall_ConfirmRunsTheJobAndInstalls(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	pid := confirmPlanID(t, s, game, "install", installModID, nil)

	rec := postForm(s, "/mods/fake/"+installModID+"/install", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "plan_id": pid,
	})

	require.Equal(t, http.StatusSeeOther, rec.Code)
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	installed, err := svc.GetInstalledMod(t.Context(), "fake", installModID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", installed.Version, "the primary file's version is what gets recorded")

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Len(t, profile.Mods, 2, "the profile's load order gains the new mod")

	assert.Positive(t, src.downloadCount(), "the install must really have downloaded")
}

// TestServer_ModInstall_FileSelectionIsHonoured pins #225's picker onto
// core's own option: ticking the 1.0 file installs 1.0, not the primary.
func TestServer_ModInstall_FileSelectionIsHonoured(t *testing.T) {
	s, svc, game, _ := newInstallFixtureServer(t)
	pid := confirmPlanID(t, s, game, "install", installModID, nil)

	rec := postFormMulti(s, "/mods/fake/"+installModID+"/install", url.Values{
		"game": {game.ID}, "profile": {"default"}, "confirm": {"1"}, "plan_id": {pid},
		"version": {"1.0"}, "file": {"f1"},
	})

	require.Equal(t, http.StatusSeeOther, rec.Code)
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	installed, err := svc.GetInstalledMod(t.Context(), "fake", installModID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
}

// TestServer_ModInstall_SyncFallback_MutatesIdentically is the no-JS
// fallback: the same install, run inline, reaching the same end state.
func TestServer_ModInstall_SyncFallback_MutatesIdentically(t *testing.T) {
	s, svc, game, _ := newInstallFixtureServer(t)
	pid := confirmPlanID(t, s, game, "install", installModID, nil)

	rec := postForm(s, "/mods/fake/"+installModID+"/install?sync=1", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "plan_id": pid,
	})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Done.")

	installed, err := svc.GetInstalledMod(t.Context(), "fake", installModID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", installed.Version)
}

// TestServer_ModInstall_ConflictRoundTrip_OverwriteIsDownloadFree is the
// heart of Task 8. Installing a mod whose archive holds a path another
// installed mod already deployed fails the job with *core.ConflictError; the
// job page renders the conflict list from the STORED job and offers
// Overwrite; taking it re-plans, re-applies with AcceptConflicts, and
// installs - having downloaded nothing the second time, because the refused
// attempt left the cache warm (core.InstallOptions.AcceptConflicts).
func TestServer_ModInstall_ConflictRoundTrip_OverwriteIsDownloadFree(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	deployFixtureProfile(t, s, game)
	require.FileExists(t, deployedFixturePath(game))

	pid := confirmPlanID(t, s, game, "install", conflictModID, nil)
	rec := postForm(s, "/mods/fake/"+conflictModID+"/install", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "plan_id": pid,
	})
	require.Equal(t, http.StatusSeeOther, rec.Code)

	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobFailed, j.status().State, "the conflict gate must refuse this install")

	var conflictErr *core.ConflictError
	require.ErrorAs(t, j.failure(), &conflictErr, "the job must store the typed conflict error")
	require.NotNil(t, j.status().Error.Details, "the stored envelope must keep Details()")

	// Nothing was installed: a refused conflict leaves a warm cache and
	// nothing else.
	_, err := svc.GetInstalledMod(t.Context(), "fake", conflictModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
	afterRefusal := src.downloadCount()
	require.Positive(t, afterRefusal, "the refused attempt did download, which is why the cache is warm")

	// The job page renders the conflict list and the Overwrite action.
	page := getPage(s, "/jobs/"+string(j.status().ID))
	require.Equal(t, http.StatusOK, page.Code)
	pageBody := page.Body.String()
	assert.Contains(t, pageBody, deployFixtureFile, "the conflicting path must be named on the page")
	assert.Contains(t, pageBody, "m1", "the mod that owns it must be named")
	assert.Contains(t, pageBody, "Overwrite")
	assert.Contains(t, pageBody, `action="/mods/fake/`+conflictModID+`/install"`)
	assert.Contains(t, pageBody, `name="accept_conflicts" value="1"`)
	assert.Contains(t, pageBody, `name="replan" value="1"`)

	// Take it.
	overwrite := postForm(s, "/mods/fake/"+conflictModID+"/install", formValues{
		"game": game.ID, "profile": "default",
		"confirm": "1", "replan": "1", "accept_conflicts": "1",
	})
	require.Equal(t, http.StatusSeeOther, overwrite.Code)
	oj := awaitRedirectedJob(t, s, overwrite)
	require.Equal(t, jobSucceeded, oj.status().State, "overwrite failed: %+v", oj.status().Error)

	installed, err := svc.GetInstalledMod(t.Context(), "fake", conflictModID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", installed.Version)
	assert.Equal(t, afterRefusal, src.downloadCount(),
		"the overwrite re-run must download nothing: the refused attempt already filled the cache")
}

// TestServer_ModInstall_StalePlan_LandsOnARePlanConfirmPage covers
// core.ErrStalePlan on both paths: the job page offers a re-plan action, and
// the inline (?sync=1) Apply renders the re-plan confirm page directly.
func TestServer_ModInstall_StalePlan_LandsOnARePlanConfirmPage(t *testing.T) {
	s, svc, game, _ := newInstallFixtureServer(t)
	pid := confirmPlanID(t, s, game, "install", installModID, nil)

	// Move the installed set out from under the plan.
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m9", SourceID: "fake", Name: "Interloper", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))

	rec := postForm(s, "/mods/fake/"+installModID+"/install?sync=1", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "plan_id": pid,
	})

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Something changed in this profile")
	assert.Regexp(t, `name="plan_id" value="[0-9a-f]{32}"`, body)

	_, err := svc.GetInstalledMod(t.Context(), "fake", installModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "a stale plan must not have applied")

	// The job path reports the same refusal, and its page offers the
	// re-plan action rather than leaving the user at a dead end.
	pid2 := confirmPlanID(t, s, game, "install", installModID, nil)
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m8", SourceID: "fake", Name: "Interloper Two", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	jobRec := postForm(s, "/mods/fake/"+installModID+"/install", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "plan_id": pid2,
	})
	require.Equal(t, http.StatusSeeOther, jobRec.Code)
	sj := awaitRedirectedJob(t, s, jobRec)
	require.Equal(t, jobFailed, sj.status().State)
	require.ErrorIs(t, sj.failure(), core.ErrStalePlan)

	page := getPage(s, "/jobs/"+string(sj.status().ID))
	require.Equal(t, http.StatusOK, page.Code)
	pageBody := page.Body.String()
	assert.Contains(t, pageBody, "Re-plan")
	// The re-plan action strips every lifecycle flag, so taking it submits
	// as a fresh entry and lands on a confirm page rather than applying.
	assert.NotContains(t, pageBody, `name="confirm" value="1"`)

	replan := postForm(s, "/mods/fake/"+installModID+"/install", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusOK, replan.Code)
	assert.Regexp(t, `name="plan_id" value="[0-9a-f]{32}"`, replan.Body.String())
}

// TestServer_ModInstall_SyncConflict_RendersTheOverwriteConfirmPage is the
// conflict round trip on the no-JS (?sync=1) path: the refusal comes back
// inline, so it renders the confirm page directly - conflict list, Overwrite
// submit, and the same download-free re-run.
func TestServer_ModInstall_SyncConflict_RendersTheOverwriteConfirmPage(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	deployFixtureProfile(t, s, game)
	pid := confirmPlanID(t, s, game, "install", conflictModID, nil)

	rec := postForm(s, "/mods/fake/"+conflictModID+"/install?sync=1", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "plan_id": pid,
	})

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "would overwrite files another installed mod owns")
	assert.Contains(t, body, deployFixtureFile)
	assert.Contains(t, body, `name="accept_conflicts" value="1"`)
	assert.Contains(t, body, "Overwrite and Install")

	_, err := svc.GetInstalledMod(t.Context(), "fake", conflictModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
	afterRefusal := src.downloadCount()

	// Take the offered plan_id straight through, with accept_conflicts set
	// exactly as the rendered form carries it.
	overwrite := postForm(s, "/mods/fake/"+conflictModID+"/install?sync=1", formValues{
		"game": game.ID, "profile": "default", "confirm": "1",
		"plan_id": hiddenField(t, body, "plan_id"), "accept_conflicts": "1",
	})
	require.Equal(t, http.StatusOK, overwrite.Code)
	assert.Contains(t, overwrite.Body.String(), "Done.")

	_, err = svc.GetInstalledMod(t.Context(), "fake", conflictModID, game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, afterRefusal, src.downloadCount(), "the inline overwrite must download nothing either")
}

// TestServer_ModInstall_WithoutCSRF_IsRefused pins the CSRF rule on install.
func TestServer_ModInstall_WithoutCSRF_IsRefused(t *testing.T) {
	s, svc, game, _ := newInstallFixtureServer(t)

	rec := postFormWithoutCSRF(s, "/mods/fake/"+installModID+"/install", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusForbidden, rec.Code)

	_, err := svc.GetInstalledMod(t.Context(), "fake", installModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound)
}

// TestServer_ModInstall_VersionSelectDefaultsToThePlansOwnPick pins the
// version select to what the plan actually previews (#225): a select whose
// visible value differed from the plan above it would pin a version the user
// never chose the moment they pressed the button.
func TestServer_ModInstall_VersionSelectDefaultsToThePlansOwnPick(t *testing.T) {
	s, _, game, _ := newInstallFixtureServer(t)

	rec := postForm(s, "/mods/fake/"+installModID+"/install", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusOK, rec.Code)
	// The primary file is 2.0, so that is what the plan would install.
	assert.Contains(t, rec.Body.String(), `<option value="2.0" selected>`)

	// An explicit pick wins over the default, and the preview follows it.
	picked := postForm(s, "/mods/fake/"+installModID+"/install", formValues{
		"game": game.ID, "profile": "default", "version": "1.0",
	})
	require.Equal(t, http.StatusOK, picked.Code)
	body := picked.Body.String()
	assert.Contains(t, body, `<option value="1.0" selected>`)
	assert.NotContains(t, body, `name="file" value="f2"`,
		"a pinned version narrows the candidate pool to that version's files")
}

// TestServer_ModInstall_UpdatePlanRePlansWithTheNewOptions covers the
// confirm page's third button - the no-JS way to see what a changed option
// would do before committing to it. It submits the same form WITHOUT the
// confirm flag (which is why "confirm" rides on the submit buttons rather
// than on a hidden field), so the page re-plans instead of applying.
func TestServer_ModInstall_UpdatePlanRePlansWithTheNewOptions(t *testing.T) {
	s, svc, game, _ := newInstallFixtureServer(t)
	first := postForm(s, "/mods/fake/"+installModID+"/install", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusOK, first.Code)
	firstPlanID := hiddenField(t, first.Body.String(), "plan_id")

	// The version select moved; the stale file checkbox from the old pool
	// comes along, exactly as a browser would send it.
	updated := postFormMulti(s, "/mods/fake/"+installModID+"/install", url.Values{
		"game": {game.ID}, "profile": {"default"},
		"plan_id": {firstPlanID}, "version": {"1.0"}, "file": {"f2"},
	})

	require.Equal(t, http.StatusOK, updated.Code, "no confirm flag means re-plan, not apply")
	body := updated.Body.String()
	assert.Contains(t, body, `<option value="1.0" selected>`)
	assert.NotEqual(t, firstPlanID, hiddenField(t, body, "plan_id"), "a re-plan issues a fresh handle")

	_, err := svc.GetInstalledMod(t.Context(), "fake", installModID, game.ID, "default")
	require.ErrorIs(t, err, domain.ErrModNotFound, "Update plan must apply nothing")
}

// TestServer_ModInstall_OverwriteDecisionSurvivesAnUpdatePlan pins the
// sticky half of the same button: once a conflict has been answered,
// re-planning must not silently drop the answer and walk the user back into
// the same refusal.
func TestServer_ModInstall_OverwriteDecisionSurvivesAnUpdatePlan(t *testing.T) {
	s, _, game, _ := newInstallFixtureServer(t)
	deployFixtureProfile(t, s, game)
	pid := confirmPlanID(t, s, game, "install", conflictModID, nil)

	conflicted := postForm(s, "/mods/fake/"+conflictModID+"/install?sync=1", formValues{
		"game": game.ID, "profile": "default", "confirm": "1", "plan_id": pid,
	})
	require.Equal(t, http.StatusOK, conflicted.Code)
	require.Contains(t, conflicted.Body.String(), `name="accept_conflicts" value="1"`)

	updated := postForm(s, "/mods/fake/"+conflictModID+"/install", formValues{
		"game": game.ID, "profile": "default",
		"plan_id": hiddenField(t, conflicted.Body.String(), "plan_id"), "accept_conflicts": "1",
	})
	require.Equal(t, http.StatusOK, updated.Code)
	assert.Contains(t, updated.Body.String(), `name="accept_conflicts" value="1"`)
	assert.Contains(t, updated.Body.String(), "Overwrite and Install")
}
