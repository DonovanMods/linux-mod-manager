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

// #645 over /api/v1: a mod imported from an archive, deployed by copy,
// whose cache entry lost a member. The Health page's plan reports the
// deployed path as cache_incomplete with no Repair and the reason, and the
// verify_fix job keeps the deployed copy - it is the only one left.
func TestFlowHealthFix_LastCopyIsReportedAndKept(t *testing.T) {
	s, svc, game, _ := newInstallFixtureServer(t)
	ctx := t.Context()
	game.LinkMethod, game.LinkMethodExplicit = domain.LinkCopy, true
	require.NoError(t, svc.SaveGame(ctx, game))
	archive := filepath.Join(t.TempDir(), "LocalMod-1.0.zip")
	require.NoError(t, os.WriteFile(archive, zipBytes(t, map[string]string{"Local/a.pak": "a", "Local/b.pak": "b"}), 0o644))
	res, err := svc.ImportArchive(ctx, game, "default", archive, core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)
	k := cache.Key{GameID: game.ID, SourceID: res.Mod.SourceID, ModID: res.Mod.ID, Version: res.Mod.Version, Variant: res.Mod.Variant}
	require.NoError(t, os.Remove(filepath.Join(svc.GetGameCache(game).ModPath(k), "Local", "a.pak")))
	deployed := filepath.Join(game.ModPath, "Local", "a.pak")
	key := res.Mod.ID + "/Local/a.pak"

	plan := planVerifyReport(t, s, game, "")
	row, ok := perModStatuses(plan.Result.Findings)[key]
	require.True(t, ok, "%+v", plan.Result.Findings)
	assert.Equal(t, core.VerifyStatusCacheIncomplete, row.Status)
	assert.Equal(t, "the cache entry is missing Local/a.pak, which this profile deployed", row.Note)
	assert.False(t, row.Fixable, "the Health page offers no Repair")
	assert.Equal(t, core.LastCopyReason, row.FixableReason)
	for _, f := range plan.Result.Findings {
		assert.NotEqual(t, "stale_deployment", f.Status, "the last copy is not a stale deployment")
	}

	j := runFlow(t, s, game, "verify_fix", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	report, ok := j.status().Result.(*core.VerifyReport)
	require.True(t, ok)
	kept := perModStatuses(report.Result.Findings)[key]
	assert.Equal(t, core.VerifyStatusCacheIncomplete, kept.Status)
	assert.Equal(t, core.LastCopyReason, kept.FixableReason)
	for _, f := range report.Result.Findings {
		assert.NotEqual(t, "fixed_stale_deployment", f.Status, "the deployed copy is never removed")
	}
	b, err := os.ReadFile(deployed)
	require.NoError(t, err, "the deployed copy is kept")
	assert.Equal(t, "a", string(b))

	after := doAPI(s, http.MethodGet, scoped("/api/v1/health", game), "")
	require.Equal(t, http.StatusOK, after.Code)
	assert.Contains(t, after.Body.String(), core.LastCopyReason, "the Health card still reports it")
}
