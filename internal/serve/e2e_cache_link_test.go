package serve_test

// #659: a symlink left in a mod's cache entry is a row on the Health card
// that names the mod and the path and offers Repair; the repair removes it,
// and the finished job lists the removal under what it repaired. Driven in
// a real browser - the row and the button exist only once the SPA has
// executed.

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

func TestE2E_HealthCard_CacheLinkIsRemoved(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	ctx := t.Context()
	f.Src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "linky", SourceID: "fake", Name: "Linky Mod", Version: "1.0"},
		files:   []domain.DownloadableFile{{ID: "kf1", Name: "Main", FileName: "linky.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 32}},
		members: map[string]string{"kf1": "Mods/linky.pak"},
	})
	plan, err := f.Svc.PlanInstall(ctx, f.Game, "default", "fake", "linky", false)
	require.NoError(t, err)
	_, err = f.Svc.ApplyInstall(ctx, f.Game, plan, core.InstallOptions{}, nil)
	require.NoError(t, err)
	entry := f.Svc.GetGameCache(f.Game).ModPath(cache.Key{GameID: f.Game.ID, SourceID: "fake", ModID: "linky", Version: "1.0"})
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(target, []byte("T"), 0o644))
	link := filepath.Join(entry, "Mods", "evil.lua")
	require.NoError(t, os.Symlink(target, link))

	const row = `.card--health .card__row[data-status="cache_link"]`
	var rowText string
	var hasRepair bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		textContent(row, &rowText),
		chromedp.Evaluate(`!!document.querySelector(`+jsString(row+` [data-action="repair"]`)+`)`, &hasRepair),
	)
	assert.Contains(t, rowText, "Linky Mod")
	assert.Contains(t, rowText, "Mods/evil.lua in the cache entry is a symbolic link")
	assert.True(t, hasRepair, "the row offers Repair")

	var view *e2eVerifyResultView
	f.runInBrowser(t, repairAllFromHealthCard(f)...)
	f.runInBrowser(t,
		waitGone(row),
		clickWhenSettled(`.activity-bell__trigger`),
		chromedp.WaitVisible(`.tray__row[data-state="succeeded"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.tray__row [data-testid="verify-fix-result"] li[data-mod="linky"]') !== null`),
		chromedp.Evaluate(resultRowsJS(".tray__row"), &view),
	)
	require.NotNil(t, view)
	var removed *e2eVerifyResultRow
	for i := range view.Rows {
		if view.Rows[i].Mod == "linky" {
			removed = &view.Rows[i]
		}
	}
	require.NotNil(t, removed, "%+v", view.Rows)
	assert.Equal(t, "repaired", removed.Group)
	assert.Contains(t, removed.Text, "removed Mods/evil.lua, a symbolic link, from the cache entry")
	assert.Empty(t, f.BrowserErrors())

	_, err = os.Lstat(link)
	assert.ErrorIs(t, err, os.ErrNotExist, "the link is gone")
	b, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "T", string(b), "its target is untouched")
}
