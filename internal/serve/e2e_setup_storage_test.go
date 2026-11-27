package serve_test

// e2e_setup_storage_test.go drives Setup > Storage (#678) in a real
// browser: the cache's size and what a prune frees, the prune's preview
// in the confirm modal, the job its confirm runs, and the setting.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// storageVersions are the versions seedStorageVersions caches for mod
// "old", oldest first; none is installed, so three are kept.
var storageVersions = []string{"1.0", "2.0", "3.0", "4.0", "5.0"}

// seedStorageVersions caches storageVersions for the fixture's game.
func seedStorageVersions(t *testing.T, f e2eFixture) {
	t.Helper()
	c := f.Svc.GetGameCache(f.Game)
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i, v := range storageVersions {
		k := cache.Key{GameID: f.Game.ID, SourceID: "fake", ModID: "old", Version: v}
		require.NoError(t, c.Store(k, "old.esp", []byte("payload "+v)))
		dir := c.ModPath(k)
		require.NoError(t, cache.MarkFileComplete(dir, "f"+v))
		require.NoError(t, cache.StampKey(dir, k))
		when := at.Add(time.Duration(i) * time.Hour)
		require.NoError(t, os.Chtimes(dir, when, when))
	}
	// Installed at a newer version no longer cached: a mod a profile uses.
	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "old", SourceID: "fake", Name: "Old Mod", Version: "6.0", GameID: f.Game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
}

// storageLeft is which storageVersions are still cached.
func storageLeft(t *testing.T, f e2eFixture) []string {
	t.Helper()
	c := f.Svc.GetGameCache(f.Game)
	var out []string
	for _, v := range storageVersions {
		if c.Exists(cache.Key{GameID: f.Game.ID, SourceID: "fake", ModID: "old", Version: v}) {
			out = append(out, v)
		}
	}
	return out
}

func TestE2E_SetupStoragePreviewsAndPrunesTheCache(t *testing.T) {
	f := newE2EFixture(t)
	seedStorageVersions(t, f)

	var reclaim, total string
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("storage")),
		chromedp.WaitVisible(`[data-testid="setup-storage-table"]`, chromedp.ByQuery),
		textContent(`[data-testid="setup-storage-row"][data-game="`+f.Game.ID+`"] [data-testid="setup-storage-reclaim"]`, &reclaim),
		textContent(`[data-testid="setup-storage-total"]`, &total),
	)
	assert.Contains(t, reclaim, "2 old versions")
	assert.Contains(t, total, "of which a prune frees")

	var summary string
	var rows []string
	f.runInBrowser(t,
		clickWhenSettled(`[data-action="plan-cache-prune"]`),
		chromedp.WaitVisible(`.modal[data-kind="cache_prune"] .plan`, chromedp.ByQuery),
		textContent(`.modal [data-testid="cache-prune-summary"]`, &summary),
		chromedp.Evaluate(`[...document.querySelectorAll('.modal [data-testid="cache-prune-entry"]')].map(li => li.textContent.replace(/\s+/g, " ").trim())`, &rows),
	)
	assert.Contains(t, summary, "removes 2 old versions")
	require.Len(t, rows, 2)
	assert.Contains(t, rows[0], "2.0")
	assert.Contains(t, rows[1], "1.0")
	assert.Contains(t, rows[0], " B", "each version shows its size")
	assert.Equal(t, storageVersions, storageLeft(t, f), "the preview removes nothing")

	f.runInBrowser(t,
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		chromedp.WaitVisible(`[data-testid="setup-storage"] .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
		pollUntil(`(document.querySelector('[data-testid="setup-storage-row"] [data-testid="setup-storage-reclaim"]')?.textContent ?? "").includes("Nothing")`),
	)
	assert.Equal(t, []string{"3.0", "4.0", "5.0"}, storageLeft(t, f))
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_SetupStorageSettingWritesConfig(t *testing.T) {
	f := newE2EFixture(t)
	seedStorageVersions(t, f)

	const keep = `select[name="keep_previous_versions"]`
	var rule string
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("storage")),
		chromedp.WaitVisible(keep, chromedp.ByQuery),
		chromedp.SetValue(keep, "1", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('`+keep+`').dispatchEvent(new Event("change", {bubbles: true}))`, nil),
		pollUntil(`(document.querySelector('[data-testid="setup-storage-reclaim"]')?.textContent ?? "").includes("4 old versions")`),
		textContent(`[data-testid="setup-storage-rule"]`, &rule),
	)
	assert.Contains(t, rule, "newest 1 previous version")
	data, err := os.ReadFile(filepath.Join(f.Svc.ConfigDir(), "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "keep_previous_versions: 1")

	f.runInBrowser(t,
		chromedp.SetValue(keep, "-1", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('`+keep+`').dispatchEvent(new Event("change", {bubbles: true}))`, nil),
		pollUntil(`(document.querySelector('[data-testid="setup-storage-reclaim"]')?.textContent ?? "").includes("Nothing")`),
	)
	data, err = os.ReadFile(filepath.Join(f.Svc.ConfigDir(), "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "keep_previous_versions: unlimited")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SetupStorageUninstalledNeedTheOptIn (#678 review S5): a mod
// nothing references is listed in the status and left whole by the
// preview; ticking "Also remove mods not installed anywhere" re-plans with
// its versions, and the confirm removes them.
func TestE2E_SetupStorageUninstalledNeedTheOptIn(t *testing.T) {
	f := newE2EFixture(t)
	c := f.Svc.GetGameCache(f.Game)
	for _, v := range []string{"1.0", "2.0"} {
		k := cache.Key{GameID: f.Game.ID, SourceID: "fake", ModID: "gone", Version: v}
		require.NoError(t, c.Store(k, "gone.esp", []byte("payload "+v)))
		require.NoError(t, cache.MarkFileComplete(c.ModPath(k), "f"+v))
		require.NoError(t, cache.StampKey(c.ModPath(k), k))
	}
	exists := func(v string) bool {
		return c.Exists(cache.Key{GameID: f.Game.ID, SourceID: "fake", ModID: "gone", Version: v})
	}

	var uninstalled, announced string
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("storage")),
		chromedp.WaitVisible(`[data-testid="setup-storage-table"]`, chromedp.ByQuery),
		textContent(`[data-testid="setup-storage-row"][data-game="`+f.Game.ID+`"] [data-testid="setup-storage-uninstalled"]`, &uninstalled),
		pollUntil(`(document.querySelector('[data-testid="setup-storage-announcer"]')?.textContent ?? "").includes("Cache read")`),
		textContent(`[data-testid="setup-storage-announcer"]`, &announced),
	)
	assert.Contains(t, uninstalled, "1 mod")
	assert.Contains(t, announced, "a prune frees nothing")

	var rows int
	f.runInBrowser(t,
		clickWhenSettled(`[data-action="plan-cache-prune"]`),
		chromedp.WaitVisible(`.modal[data-kind="cache_prune"] [data-testid="cache-prune-nothing"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal [data-testid="cache-prune-uninstalled"] input[name="include_uninstalled"]`, chromedp.ByQuery),
		clickWhenSettled(`.modal input[name="include_uninstalled"]`),
		chromedp.WaitVisible(`.modal [data-testid="cache-prune-summary"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('.modal [data-testid="cache-prune-entry"]').length`, &rows),
	)
	assert.Equal(t, 2, rows, "both versions of the mod nothing uses")
	assert.True(t, exists("1.0") && exists("2.0"), "the preview removes nothing")

	f.runInBrowser(t,
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		chromedp.WaitVisible(`[data-testid="setup-storage"] .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)
	assert.False(t, exists("1.0") || exists("2.0"))
	assert.Empty(t, f.BrowserErrors())
}
