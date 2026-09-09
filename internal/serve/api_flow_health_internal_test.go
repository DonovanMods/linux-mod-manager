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
	"encoding/json/v2"
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

// --- #332: the per-finding repair's mod filter, and the fixable flag ---

// healthFilterFixture seeds two broken mods so a filtered repair has
// something to leave alone: m1 keeps its healthy file and gains one with no
// recorded checksum (a no_checksum row, repairable by redownload), and m2 is
// installed with nothing in the cache at all (a missing row, likewise
// repairable). A stray dangling deployment supplies the profile-scoped
// stale_deployment row that no filter narrows.
func healthFilterFixture(t *testing.T, s *Server, svc *core.Service, game *domain.Game) {
	t.Helper()
	ctx := t.Context()

	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{deployFixtureFile, "Mods/extra.pak"},
	}))
	require.NoError(t, svc.SaveFileChecksum(ctx, fixtureSourceID, "m1", game.ID, "default", deployFixtureFile, "deadbeef"))
	require.NoError(t, svc.SaveFileChecksum(ctx, fixtureSourceID, "m1", game.ID, "default", "Mods/extra.pak", ""))

	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m2", SourceID: fixtureSourceID, Name: "Mod Two", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"Mods/two.pak"},
	}))
	require.NoError(t, svc.SaveFileChecksum(ctx, fixtureSourceID, "m2", game.ID, "default", "Mods/two.pak", "cafebabe"))

	strayDeployment(t, s, game, "stray.pak")
}

// perModStatuses indexes a report's per-mod findings by "modID/fileID".
func perModStatuses(findings []core.VerifyFinding) map[string]core.VerifyFinding {
	out := make(map[string]core.VerifyFinding, len(findings))
	for _, f := range findings {
		if f.ModID == "" {
			continue
		}
		out[f.ModID+"/"+f.FileID] = f
	}
	return out
}

// planVerifyReport plans a verify_fix with the given options body and
// returns the plan document.
func planVerifyReport(t *testing.T, s *Server, game *domain.Game, body string) core.VerifyReport {
	t.Helper()
	_, raw := planFlow(t, s, game, "verify_fix", body)
	var resp struct {
		Plan core.VerifyReport `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.NotNil(t, resp.Plan.Result)
	return resp.Plan
}

// TestFlowVerifyFix_ModFilterNarrowsBothHalves is the per-finding *Repair*
// the health card offers: the plan previews only the named mod's findings,
// and the apply - which takes the filter from the STORED plan, never from a
// second request - repairs only that mod.
func TestFlowVerifyFix_ModFilterNarrowsBothHalves(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	healthFilterFixture(t, s, svc, game)

	unfiltered := planVerifyReport(t, s, game, "")
	require.Contains(t, perModStatuses(unfiltered.Result.Findings), "m2/Mods/two.pak",
		"the unfiltered plan sees both mods")

	filtered := planVerifyReport(t, s, game, `{"mod_filter":"m1"}`)
	for key, f := range perModStatuses(filtered.Result.Findings) {
		assert.Equal(t, "m1", f.ModID, "a filtered plan must preview only the named mod: %s", key)
	}

	j := runFlow(t, s, game, "verify_fix", `{"mod_filter":"m1"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)

	report, ok := j.status().Result.(*core.VerifyReport)
	require.True(t, ok, "the stored result must be the core document")
	for key, f := range perModStatuses(report.Result.Findings) {
		assert.Equal(t, "m1", f.ModID, "a filtered repair must act on only the named mod: %s", key)
	}

	// m2 was never touched: an unfiltered re-plan still reports it broken.
	after := planVerifyReport(t, s, game, "")
	assert.Equal(t, "missing", perModStatuses(after.Result.Findings)["m2/Mods/two.pak"].Status,
		"a filtered repair must leave every other mod exactly as it was")
}

// TestFlowVerifyFix_PlanDisclosesWhichFindingsAreFixable is carry-in 1: the
// plan document says, per finding, whether a repair would even be attempted
// - so the health card can offer *Repair* on exactly the rows that have one.
func TestFlowVerifyFix_PlanDisclosesWhichFindingsAreFixable(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	healthFilterFixture(t, s, svc, game)

	plan := planVerifyReport(t, s, game, "")
	byKey := perModStatuses(plan.Result.Findings)

	require.Contains(t, byKey, "m1/Mods/extra.pak")
	assert.Equal(t, "no_checksum", byKey["m1/Mods/extra.pak"].Status)
	assert.True(t, byKey["m1/Mods/extra.pak"].Fixable, "--fix redownloads to populate a checksum")

	require.Contains(t, byKey, "m2/Mods/two.pak")
	assert.Equal(t, "missing", byKey["m2/Mods/two.pak"].Status)
	assert.True(t, byKey["m2/Mods/two.pak"].Fixable, "--fix redownloads a missing cache entry")

	require.Contains(t, byKey, "m1/"+deployFixtureFile)
	assert.Equal(t, "ok", byKey["m1/"+deployFixtureFile].Status)
	assert.False(t, byKey["m1/"+deployFixtureFile].Fixable, "a healthy row has nothing to repair")

	var stale *core.VerifyFinding
	for i, f := range plan.Result.Findings {
		if f.Status == "stale_deployment" {
			stale = &plan.Result.Findings[i]
		}
	}
	require.NotNil(t, stale, "the stray deployment must be reported")
	assert.True(t, stale.Fixable, "convergence removes a stale deployment under --fix")
}

// TestFlowVerifyFix_ModFilterRejectsUnknownMembers: the request stays
// strictly decoded, so a misspelled option is a 400 rather than a silently
// unfiltered repair of the whole profile.
func TestFlowVerifyFix_ModFilterRejectsUnknownMembers(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/verify_fix", game), `{"mod_filtr":"m1"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
