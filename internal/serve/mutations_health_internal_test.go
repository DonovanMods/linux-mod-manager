package serve

// Task 9 flow 5: health repair (docs/plans/2026-08-30-serve-impl.md Task
// 9). `lmm verify --fix` as a Plan -> confirm -> job flow, where the plan
// is the very same engine run with every repair withheld - so the confirm
// page shows the findings themselves, not a description of them.
//
// The fixture divergence is core's own smallest one (see
// internal/core/verify_test.go's strayDanglingSymlink): a symlink in the
// game directory pointing into the cache at content that was never stored.
// The convergence pass reports it as stale_deployment on a dry run and
// removes it under --fix, so "repaired" is a file that is gone.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strayDeployment plants the repairable divergence and returns its path.
func strayDeployment(t *testing.T, s *Server, game *domain.Game, name string) string {
	t.Helper()
	target := filepath.Join(s.svc.GetGameCachePath(game), game.ID, "src-stray", "1.0", name)
	link := filepath.Join(game.ModPath, name)
	require.NoError(t, os.Symlink(target, link))
	return link
}

// TestServer_Health_OffersRepairOnlyWhenSomethingIsFixable is the task's
// own rule: the action appears when - and only when - the report holds a
// finding a repair would act on.
func TestServer_Health_OffersRepairOnlyWhenSomethingIsFixable(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)

	clean := getPage(s, "/health")
	require.Equal(t, http.StatusOK, clean.Code)
	assert.NotContains(t, clean.Body.String(), `action="/health/fix"`,
		"a profile with nothing repairable must not be offered a repair")

	strayDeployment(t, s, game, "stray.pak")

	fixable := getPage(s, "/health")
	require.Equal(t, http.StatusOK, fixable.Code)
	body := fixable.Body.String()
	assert.Contains(t, body, `action="/health/fix"`)
	assert.Contains(t, body, "stale_deployment")
	assert.Regexp(t, `name="csrf_token" value="[0-9a-f]{64}"`, body)
}

// TestServer_HealthFix_ConfirmShowsTheDryRun is the plan half: the same
// engine, every repair withheld - so the page names the finding, and the
// divergence is still there afterwards.
func TestServer_HealthFix_ConfirmShowsTheDryRun(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)
	stray := strayDeployment(t, s, game, "stray.pak")

	rec := postForm(s, "/health/fix", formValues{"game": game.ID, "profile": "default"})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "stray.pak")
	assert.Contains(t, body, "stale_deployment")
	assert.Contains(t, body, "Findings a repair would act on")
	assert.Regexp(t, `name="plan_id" value="[0-9a-f]{32}"`, body)

	_, err := os.Lstat(stray)
	require.NoError(t, err, "a plan must repair nothing")
}

// TestServer_HealthFix_ConfirmRunsTheJobAndRepairs is the apply half: the
// divergence is gone, and the stored result says so.
func TestServer_HealthFix_ConfirmRunsTheJobAndRepairs(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)
	stray := strayDeployment(t, s, game, "stray.pak")
	entry := postForm(s, "/health/fix", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusOK, entry.Code)

	rec := postForm(s, "/health/fix", formValues{
		"game": game.ID, "profile": "default", "confirm": "1",
		"plan_id": hiddenField(t, entry.Body.String(), "plan_id"),
	})

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	_, err := os.Lstat(stray)
	require.True(t, os.IsNotExist(err), "the repair must remove the dangling deployment")

	report, ok := j.status().Result.(*core.VerifyReport)
	require.True(t, ok, "the stored result must be the core document")
	require.Len(t, report.Result.Findings, 1)
	assert.Equal(t, "fixed_stale_deployment", report.Result.Findings[0].Status)
	assert.Zero(t, report.Result.Warnings, "a repaired row is resolved, not outstanding")

	page := getPage(s, "/jobs/"+string(j.status().ID))
	require.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), "Repaired")

	// And the page that offered the action no longer does.
	after := getPage(s, "/health")
	require.Equal(t, http.StatusOK, after.Code)
	assert.NotContains(t, after.Body.String(), `action="/health/fix"`)
}

// TestServer_HealthFix_SyncFallback_MutatesIdentically is the no-JS path.
func TestServer_HealthFix_SyncFallback_MutatesIdentically(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)
	stray := strayDeployment(t, s, game, "stray.pak")
	entry := postForm(s, "/health/fix", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusOK, entry.Code)

	rec := postForm(s, "/health/fix?sync=1", formValues{
		"game": game.ID, "profile": "default", "confirm": "1",
		"plan_id": hiddenField(t, entry.Body.String(), "plan_id"),
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Done.")

	_, err := os.Lstat(stray)
	require.True(t, os.IsNotExist(err))
}

// TestServer_HealthFix_WithoutCSRF_IsRefused pins the CSRF rule on the
// repair route, and that a refused request repaired nothing.
func TestServer_HealthFix_WithoutCSRF_IsRefused(t *testing.T) {
	s, _, game := newMutationFixtureServer(t)
	stray := strayDeployment(t, s, game, "stray.pak")

	rec := postFormWithoutCSRF(s, "/health/fix", formValues{
		"game": game.ID, "profile": "default", "confirm": "1",
	})

	require.Equal(t, http.StatusForbidden, rec.Code)
	_, err := os.Lstat(stray)
	require.NoError(t, err, "a refused request must repair nothing")
}

// TestServer_HealthFix_HealthyFilesAreNotClaimedAsRepaired is Important 1
// from the Unit 5 gate review: core appends an "ok" finding for every
// healthy checksummed file - that is perFileWalk's baseline, not a repair
// outcome (internal/core/verify.go:624). Neither the confirm page nor the
// result page may claim a repair touched a file it never acted on, and
// neither may grow one row per healthy file in the profile.
func TestServer_HealthFix_HealthyFilesAreNotClaimedAsRepaired(t *testing.T) {
	s, svc, game := newMutationFixtureServer(t)
	stray := strayDeployment(t, s, game, "stray.pak")

	healthyFiles := []string{"Mods/two.pak", "Mods/three.pak", "Mods/four.pak"}
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      healthyFiles,
	}))
	for _, file := range healthyFiles {
		require.NoError(t, svc.SaveFileChecksum(t.Context(), fixtureSourceID, "m1", game.ID, "default", file, "deadbeef"))
	}

	entry := postForm(s, "/health/fix", formValues{"game": game.ID, "profile": "default"})
	require.Equal(t, http.StatusOK, entry.Code, entry.Body.String())
	confirmBody := entry.Body.String()
	for _, file := range healthyFiles {
		assert.NotContains(t, confirmBody, file,
			"a healthy file must not be listed among findings a repair cannot act on")
	}

	rec := postForm(s, "/health/fix", formValues{
		"game": game.ID, "profile": "default", "confirm": "1",
		"plan_id": hiddenField(t, confirmBody, "plan_id"),
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	j := awaitRedirectedJob(t, s, rec)
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	_, err := os.Lstat(stray)
	require.True(t, os.IsNotExist(err), "the repair must remove the dangling deployment")

	page := getPage(s, "/jobs/"+string(j.status().ID))
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()
	assert.Equal(t, 1, strings.Count(body, ">Repaired<"),
		"exactly the stray's fix should be labeled Repaired, not one row per healthy file")
	for _, file := range healthyFiles {
		assert.NotContains(t, body, file, "a healthy file must not appear as a per-file result row")
	}
}
