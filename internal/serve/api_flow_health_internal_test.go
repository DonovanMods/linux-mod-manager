package serve

// The health repair flow over /api/v1: `lmm verify --fix` as a Plan -> job
// pair, where the plan is the very same engine run with every repair
// withheld - so the plan document holds the findings themselves, not a
// description of them.
//
// The fixture divergence is core's own smallest one (see
// internal/core/verify_test.go's strayDanglingSymlink): a symlink in the
// game directory pointing into the cache at content that was never stored.
// The convergence pass reports it as stale_deployment on a dry run and
// removes it under --fix, so "repaired" is a file that is gone.
//
// Ported from the deleted mutations_health_internal_test.go; its
// confirm-page and result-page assertions are replaced by the equivalent
// assertions on the plan and result DOCUMENTS, which is where the SPA reads
// the same facts from.

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

// TestFlowHealthFix_PlanReportsTheFindingAndRepairsNothing is the Plan
// half: the dry run names the divergence and leaves it in place.
func TestFlowHealthFix_PlanReportsTheFindingAndRepairsNothing(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	stray := strayDeployment(t, s, game, "stray.pak")

	_, raw := planFlow(t, s, game, "verify_fix", "")
	assert.Contains(t, string(raw), "stale_deployment", "the dry run must report the divergence")

	_, err := os.Lstat(stray)
	require.NoError(t, err, "a plan must repair nothing")
}

// TestFlowHealthFix_JobRepairs is the apply half: the divergence is gone,
// and the stored result says so.
func TestFlowHealthFix_JobRepairs(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	stray := strayDeployment(t, s, game, "stray.pak")

	j := runFlow(t, s, game, "verify_fix", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	_, err := os.Lstat(stray)
	require.True(t, os.IsNotExist(err), "the repair must remove the dangling deployment")

	report, ok := j.status().Result.(*core.VerifyReport)
	require.True(t, ok, "the stored result must be the core document")
	require.Len(t, report.Result.Findings, 1)
	assert.Equal(t, "fixed_stale_deployment", report.Result.Findings[0].Status)
	assert.Zero(t, report.Result.Warnings, "a repaired row is resolved, not outstanding")

	// And the surface that offered the action reports a clean sheet.
	after := doAPI(s, http.MethodGet, scoped("/api/v1/health", game), "")
	require.Equal(t, http.StatusOK, after.Code)
	assert.NotContains(t, after.Body.String(), "stale_deployment")
}

// TestFlowHealthFix_WithoutCSRF_IsRefused pins the CSRF rule on the repair
// flow, and that a refused request repaired nothing.
func TestFlowHealthFix_WithoutCSRF_IsRefused(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	stray := strayDeployment(t, s, game, "stray.pak")

	rec := doAPIWithoutCSRF(s, http.MethodPost, scoped("/api/v1/plans/verify_fix", game), "")

	require.Equal(t, http.StatusForbidden, rec.Code)
	_, err := os.Lstat(stray)
	require.NoError(t, err, "a refused request must repair nothing")
}

// TestFlowHealthFix_HealthyFilesAreNotClaimedAsRepaired is Important 1 from
// the Unit 5 gate review: core appends an "ok" finding for every healthy
// checksummed file - that is perFileWalk's baseline, not a repair outcome
// (internal/core/verify.go) - so a repair must never claim it acted on one.
//
// The original test asserted this by the healthy files being ABSENT from
// the rendered confirm and result pages. That half was a display rule: the
// wire document is the frozen core VerifyReport, which legitimately carries
// every "ok" finding, and always did. What ports is the rule underneath it,
// asserted where it now lives: a healthy file's finding must still read
// "ok", and exactly ONE finding may carry a repair status.
func TestFlowHealthFix_HealthyFilesAreNotClaimedAsRepaired(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
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

	id, _ := planFlow(t, s, game, "verify_fix", "")
	j := startFlowJob(t, s, id, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	_, err := os.Lstat(stray)
	require.True(t, os.IsNotExist(err), "the repair must remove the dangling deployment")

	report, ok := j.status().Result.(*core.VerifyReport)
	require.True(t, ok, "the stored result must be the core document")

	statusOf := map[string]string{}
	repaired := 0
	for _, finding := range report.Result.Findings {
		statusOf[finding.FileID] = finding.Status
		if strings.HasPrefix(finding.Status, "fixed_") {
			repaired++
		}
	}
	for _, file := range healthyFiles {
		assert.Equal(t, "ok", statusOf[file],
			"%s was healthy: the repair must not relabel it as anything it acted on", file)
	}
	assert.Equal(t, 1, repaired,
		"exactly the stray's fix is a repair, not one repair row per healthy file")
	assert.Equal(t, "fixed_stale_deployment", statusOf["stray.pak"])
}
