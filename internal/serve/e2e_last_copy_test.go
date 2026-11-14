package serve_test

// #645: a mod imported from an archive, deployed by copy, whose cache entry
// lost a member is a Health card row naming the deployed file, with no
// Repair - the deployed copy is the only one left, and its tooltip says so.
// Driven in a real browser - the row exists only once the SPA has executed.

import (
	"archive/zip"
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

func TestE2E_HealthCard_LastCopyIsKeptNotRepaired(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	ctx := t.Context()
	f.Game.LinkMethod, f.Game.LinkMethodExplicit = domain.LinkCopy, true
	require.NoError(t, f.Svc.SaveGame(ctx, f.Game))
	archive := filepath.Join(t.TempDir(), "Keepsake-1.0.zip")
	writeE2EZip(t, archive, map[string]string{"Mods/keepsake.pak": "only copy", "Mods/other.pak": "b"})
	res, err := f.Svc.ImportArchive(ctx, f.Game, "default", archive, core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)
	deployed := filepath.Join(f.Game.ModPath, "Mods", "keepsake.pak")
	require.FileExists(t, deployed)
	k := cache.Key{GameID: f.Game.ID, SourceID: res.Mod.SourceID, ModID: res.Mod.ID, Version: res.Mod.Version, Variant: res.Mod.Variant}
	require.NoError(t, os.Remove(filepath.Join(f.Svc.GetGameCache(f.Game).ModPath(k), "Mods", "keepsake.pak")))

	const row = `.card--health .card__row[data-status="cache_incomplete"]`
	var rowText, title string
	var hasRepair, hasTitle bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		textContent(row, &rowText),
		chromedp.AttributeValue(row, "title", &title, &hasTitle, chromedp.ByQuery),
		chromedp.Evaluate(`!!document.querySelector(`+jsString(row+` [data-action="repair"]`)+`)`, &hasRepair),
	)
	assert.Contains(t, rowText, "the cache entry is missing Mods/keepsake.pak, which this profile deployed")
	assert.False(t, hasRepair, "nothing can rebuild it, so the row offers no Repair")
	assert.True(t, hasTitle)
	assert.Equal(t, core.LastCopyReason, title, "the tooltip says why")
	assert.Empty(t, f.BrowserErrors())

	b, err := os.ReadFile(deployed)
	require.NoError(t, err, "the deployed copy is kept")
	assert.Equal(t, "only copy", string(b))
}

// writeE2EZip writes a zip archive holding files at path.
func writeE2EZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	out, err := os.Create(path)
	require.NoError(t, err)
	w := zip.NewWriter(out)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	require.NoError(t, out.Close())
}
