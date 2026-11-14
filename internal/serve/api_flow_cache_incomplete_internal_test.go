package serve

// #642 over /api/v1: a cache entry that lost a member and its completion
// marker is a fixable cache_incomplete finding on the Health page's plan,
// and the verify_fix job re-downloads it - the deployed file is never
// removed as stale.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lostMemberFixture installs "Lost Mod" (file lf1, member Lost/lost.pak)
// through the real installer, then deletes the member and its marker from
// the cache entry. It returns the deployed path.
func lostMemberFixture(t *testing.T, svc *core.Service, game *domain.Game, src *installSource) string {
	t.Helper()
	ctx := t.Context()
	src.mods["lm"] = &installSourceMod{
		mod:     domain.Mod{ID: "lm", SourceID: fixtureSourceID, Name: "Lost Mod", Version: "1.0", GameID: game.ID},
		files:   []domain.DownloadableFile{{ID: "lf1", Name: "Main", FileName: "lm.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 32}},
		members: map[string]string{"lf1": "Lost/lost.pak"},
	}
	plan, err := svc.PlanInstall(ctx, game, "default", fixtureSourceID, "lm", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	deployed := filepath.Join(game.ModPath, "Lost", "lost.pak")
	require.FileExists(t, deployed)

	entry := svc.GetGameCache(game).ModPath(cache.Key{GameID: game.ID, SourceID: fixtureSourceID, ModID: "lm", Version: "1.0"})
	require.NoError(t, os.Remove(filepath.Join(entry, "Lost", "lost.pak")))
	require.NoError(t, os.Remove(filepath.Join(entry, cache.ReservedPrefix+"file-lf1")))
	return deployed
}

func TestFlowHealthFix_CacheIncompleteIsReportedAndRepaired(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	deployed := lostMemberFixture(t, svc, game, src)

	plan := planVerifyReport(t, s, game, "")
	row, ok := perModStatuses(plan.Result.Findings)["lm/lf1"]
	require.True(t, ok, "%+v", plan.Result.Findings)
	assert.Equal(t, core.VerifyStatusCacheIncomplete, row.Status)
	assert.Equal(t, "the cache entry is missing Lost/lost.pak, which this profile deployed", row.Note)
	assert.True(t, row.Fixable, "the Health page offers Repair")
	for _, f := range plan.Result.Findings {
		assert.NotEqual(t, "stale_deployment", f.Status, "a lost cache member is not a stale deployment")
	}

	j := runFlow(t, s, game, "verify_fix", `{"mod_filter":"lm"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	report, ok := j.status().Result.(*core.VerifyReport)
	require.True(t, ok)
	fixed := perModStatuses(report.Result.Findings)["lm/lf1"]
	assert.Equal(t, "ok", fixed.Status)
	assert.Equal(t, "Re-downloaded OK", fixed.Repair)
	for _, f := range report.Result.Findings {
		assert.NotEqual(t, "fixed_stale_deployment", f.Status, "the deployed file is never removed")
	}
	_, err := os.Stat(deployed)
	require.NoError(t, err, "the deployed file is still there, and resolves")

	after := doAPI(s, http.MethodGet, scoped("/api/v1/health", game), "")
	require.Equal(t, http.StatusOK, after.Code)
	assert.NotContains(t, after.Body.String(), core.VerifyStatusCacheIncomplete)
}
