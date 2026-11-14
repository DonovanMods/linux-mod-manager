package serve_test

// #642: a mod whose cache entry lost a member and its completion marker is a
// row on the Health card that says what is gone and offers Repair; the
// repair re-downloads it, and the deployed file stays. Driven in a real
// browser - the row and the button exist only once the SPA has executed.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

func TestE2E_HealthCard_CacheIncompleteIsRepaired(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	ctx := t.Context()
	f.Src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "lost", SourceID: "fake", Name: "Lost Mod", Version: "1.0"},
		files:   []domain.DownloadableFile{{ID: "lf1", Name: "Main", FileName: "lost.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 32}},
		members: map[string]string{"lf1": "Mods/lost.pak"},
	})
	plan, err := f.Svc.PlanInstall(ctx, f.Game, "default", "fake", "lost", false)
	require.NoError(t, err)
	_, err = f.Svc.ApplyInstall(ctx, f.Game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	deployed := filepath.Join(f.Game.ModPath, "Mods", "lost.pak")
	require.FileExists(t, deployed)
	entry := f.Svc.GetGameCache(f.Game).ModPath(cache.Key{GameID: f.Game.ID, SourceID: "fake", ModID: "lost", Version: "1.0"})
	require.NoError(t, os.Remove(filepath.Join(entry, "Mods", "lost.pak")))
	require.NoError(t, os.Remove(filepath.Join(entry, cache.ReservedPrefix+"file-lf1")))

	const row = `.card--health .card__row[data-status="cache_incomplete"]`
	var rowText string
	var hasRepair bool
	var view *e2eVerifyResultView
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		textContent(row, &rowText),
		chromedp.Evaluate(`!!document.querySelector(`+jsString(row+` [data-action="repair"]`)+`)`, &hasRepair),
	)
	assert.Contains(t, rowText, "Lost Mod")
	assert.Contains(t, rowText, "the cache entry is missing Mods/lost.pak, which this profile deployed")
	assert.True(t, hasRepair, "the row offers Repair")

	// Everything repaired, the Health card has nothing left to show and
	// steps aside; the finished job's result is in the activity tray.
	f.runInBrowser(t, repairAllFromHealthCard(f)...)
	f.runInBrowser(t,
		waitGone(row),
		clickWhenSettled(`.activity-bell__trigger`),
		chromedp.WaitVisible(`.tray__row[data-state="succeeded"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.tray__row [data-testid="verify-fix-result"] li[data-mod="lost"]') !== null`),
		chromedp.Evaluate(resultRowsJS(".tray__row"), &view),
	)
	require.NotNil(t, view)
	var lost *e2eVerifyResultRow
	for i := range view.Rows {
		if view.Rows[i].Mod == "lost" {
			lost = &view.Rows[i]
		}
	}
	require.NotNil(t, lost, "%+v", view.Rows)
	assert.Equal(t, "repaired", lost.Group)
	assert.Contains(t, lost.Text, "Re-downloaded OK")
	for _, r := range view.Rows {
		assert.NotContains(t, r.Text, "no longer provided", "the deployed file is never removed as stale")
	}
	assert.Empty(t, f.BrowserErrors())

	_, err = os.Stat(deployed)
	require.NoError(t, err, "the deployed file is still there, and resolves")
}
