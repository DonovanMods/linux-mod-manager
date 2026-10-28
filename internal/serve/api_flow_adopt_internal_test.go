package serve

// The adopt flow end to end through the entry points the SPA drives: POST
// /api/v1/plans/adopt, then POST /api/v1/jobs - with assertions on the end
// state (the DB row the adoption created) and on the ONE result document
// carrying both applies' counts.

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedUntrackedMod drops an untracked mod directory into the fixture game's
// mod_path - what an adopt exists to take on. The fixture game is in
// extract mode, where a scan looks at directories.
func seedUntrackedMod(t *testing.T, modPath, name string) {
	t.Helper()
	dir := filepath.Join(modPath, name)
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "data.esp"), []byte("mod bytes"), 0644))
}

func TestAPIFlow_Adopt_PlansTheScanAndAdoptsIt(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedUntrackedMod(t, game.ModPath, "FoundMod")

	planID, planBody := planFlow(t, s, game, "adopt", `{"skip_match":true}`)

	// The plan document is core.AdoptPlan verbatim - the scan facts the SPA
	// renders before asking.
	var planned struct {
		Plan core.AdoptPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(planBody, &planned))
	require.Len(t, planned.Plan.Scan.Untracked, 1)
	assert.Equal(t, "FoundMod", planned.Plan.Scan.Untracked[0].FileName)
	assert.Len(t, planned.Plan.Matches, 1, "one match entry per untracked entry, in the same order")
	assert.True(t, planned.Plan.SkipMatch, "the plan echoes the option it was computed with")

	before, err := svc.GetInstalledMods(t.Context(), game.ID, "default")
	require.NoError(t, err)

	j := startFlowJob(t, s, planID, "")
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	result, ok := j.status().Result.(*core.AdoptResult)
	require.True(t, ok)
	assert.Equal(t, 1, result.Adopted)
	assert.Equal(t, 0, result.Failed)

	after, err := svc.GetInstalledMods(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Len(t, after, len(before)+1, "the adopted mod has an installed_mods row")
}

// TestAPIFlow_Adopt_FoldsTheBackfillCountIntoTheOneResult pins #333's
// additive AdoptResult.Backfilled: a browser confirms between the plan and
// the job, so BOTH applies run inside the job, and one document reports
// them. The fixture's seeded mod is source-linked with no author, which is
// exactly what ScanLocal counts as a backfill candidate.
func TestAPIFlow_Adopt_FoldsTheBackfillCountIntoTheOneResult(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	seedUntrackedMod(t, game.ModPath, "FoundMod")

	planID, planBody := planFlow(t, s, game, "adopt", "")
	var planned struct {
		Plan core.AdoptPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(planBody, &planned))
	require.Len(t, planned.Plan.Scan.Backfill, 1, "the plan names the rows the backfill will process")

	j := startFlowJob(t, s, planID, "")
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	result, ok := j.status().Result.(*core.AdoptResult)
	require.True(t, ok)
	assert.Equal(t, 1, result.Backfilled, "the backfill's own count reaches the job's single result")
	assert.Equal(t, 1, result.Adopted)
}

// TestAPIFlow_Adopt_SkipMatchSuppressesTheBackfill pins that --skip-match
// carries through: with it, the plan has nothing for the backfill Apply to
// do, so the folded count is zero and the document drops the member.
func TestAPIFlow_Adopt_SkipMatchSuppressesTheBackfill(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	seedUntrackedMod(t, game.ModPath, "FoundMod")

	planID, planBody := planFlow(t, s, game, "adopt", `{"skip_match":true}`)
	var planned struct {
		Plan core.AdoptPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(planBody, &planned))
	assert.Empty(t, planned.Plan.Scan.Backfill)

	j := startFlowJob(t, s, planID, "")
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	body, err := json.Marshal(j.status())
	require.NoError(t, err)
	assert.NotContains(t, string(body), "backfilled",
		"omitzero: a document with no backfill is what it always was")
}

// TestAPIFlow_Adopt_DryRunIsPlanOnly pins the ruling in kind_adopt.go's
// file comment: previewing is the dry run, so the plan endpoint mutates
// nothing and the kind offers no dry-run option to misinterpret.
func TestAPIFlow_Adopt_DryRunIsPlanOnly(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedUntrackedMod(t, game.ModPath, "FoundMod")

	before, err := svc.GetInstalledMods(t.Context(), game.ID, "default")
	require.NoError(t, err)

	_, _ = planFlow(t, s, game, "adopt", "")

	after, err := svc.GetInstalledMods(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.Len(t, after, len(before), "planning wrote nothing")

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/adopt", game), `{"dry_run":true}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "an unknown option is refused, never silently ignored")
}

func TestAPIFlow_Adopt_RequiresTheCSRFToken(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)
	rec := doAPIWithoutCSRF(s, http.MethodPost, scoped("/api/v1/plans/adopt", game), "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
