package serve

// #659 over /api/v1: a symlink standing where a mod's recorded member
// belongs, and a FIFO beside it, are fixable cache_link findings on the
// Health page's plan; the verify_fix job removes both, never touching the
// link's target, and re-downloads the member the link stood for.

import (
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlowHealthFix_CacheLinkIsReportedAndRemoved(t *testing.T) {
	s, svc, game, src := newInstallFixtureServer(t)
	ctx := t.Context()
	src.mods["lk"] = &installSourceMod{
		mod:     domain.Mod{ID: "lk", SourceID: fixtureSourceID, Name: "Linked Mod", Version: "1.0", GameID: game.ID},
		files:   []domain.DownloadableFile{{ID: "kf1", Name: "Main", FileName: "lk.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 32}},
		members: map[string]string{"kf1": "Linked/linked.pak"},
	}
	plan, err := svc.PlanInstall(ctx, game, "default", fixtureSourceID, "lk", false)
	require.NoError(t, err)
	_, err = svc.ApplyInstall(ctx, game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)

	target := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(target, []byte("SECRET"), 0o644))
	entry := svc.GetGameCache(game).ModPath(cache.Key{GameID: game.ID, SourceID: fixtureSourceID, ModID: "lk", Version: "1.0"})
	member := filepath.Join(entry, "Linked", "linked.pak")
	require.NoError(t, os.Remove(member))
	require.NoError(t, os.Symlink(target, member))
	require.NoError(t, syscall.Mkfifo(filepath.Join(entry, "Linked", "pipe"), 0o644))

	report := planVerifyReport(t, s, game, "")
	statuses := perModStatuses(report.Result.Findings)
	for _, key := range []string{"lk/Linked/linked.pak", "lk/Linked/pipe"} {
		row, ok := statuses[key]
		require.True(t, ok, "%s: %+v", key, report.Result.Findings)
		assert.Equal(t, core.VerifyStatusCacheLink, row.Status)
		assert.True(t, row.Fixable, "the Health page offers Repair")
	}
	assert.Contains(t, statuses["lk/Linked/linked.pak"].Note, "where one of the mod's files belongs")
	assert.Contains(t, statuses["lk/Linked/pipe"].Note, "named pipe")

	j := runFlow(t, s, game, "verify_fix", `{"mod_filter":"lk"}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	fixedReport, ok := j.status().Result.(*core.VerifyReport)
	require.True(t, ok)
	fixed := perModStatuses(fixedReport.Result.Findings)
	assert.Equal(t, core.VerifyStatusFixedCacheLink, fixed["lk/Linked/linked.pak"].Status)
	assert.Equal(t, core.VerifyStatusFixedCacheLink, fixed["lk/Linked/pipe"].Status)
	assert.Equal(t, "Re-downloaded OK", fixed["lk/kf1"].Repair, "%+v", fixedReport.Result.Findings)

	info, err := os.Lstat(member)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the member is a real file again")
	_, err = os.Lstat(filepath.Join(entry, "Linked", "pipe"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	b, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "SECRET", string(b), "the link's target is untouched")

	after := doAPI(s, http.MethodGet, scoped("/api/v1/health", game), "")
	require.Equal(t, http.StatusOK, after.Code)
	assert.NotContains(t, after.Body.String(), core.VerifyStatusCacheLink)
}
