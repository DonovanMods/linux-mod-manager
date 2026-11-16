package serve_test

// #638 in the browser: the uninstall confirm names the mod a shared file
// passes to, and confirming it hands the file to that mod.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_UninstallPlan_NamesTheModASharedFilePassesTo(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)
	gameCache := f.Svc.GetGameCache(f.Game)
	// Alpha then Beta in load order both ship shared.esp: Beta wins it.
	for id, content := range map[string]string{"a": "alpha's", "b": "beta's"} {
		require.NoError(t, gameCache.Store(cache.Key{GameID: f.Game.ID, SourceID: "fake", ModID: id, Version: "1.0"}, "shared.esp", []byte(content)))
	}
	_, err := f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	shared := filepath.Join(f.Game.ModPath, "shared.esp")
	got, err := os.ReadFile(shared)
	require.NoError(t, err)
	require.Equal(t, "beta's", string(got))

	var listed, heir string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		clickModRow("Beta Mod"),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".slide-over__actions button"))
			.find((b) => b.textContent.trim() === "Uninstall").click()`, nil),
		chromedp.WaitVisible(`.modal[data-kind="uninstall"] [data-testid="uninstall-handoffs"]`, chromedp.ByQuery),
		textContent(`[data-testid="uninstall-handoffs"]`, &listed),
		chromedp.AttributeValue(`[data-testid="uninstall-handoffs"] li`, "data-heir", &heir, nil, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)
	assert.Contains(t, listed, "shared.esp")
	assert.Contains(t, listed, "passes to Alpha Mod")
	assert.Contains(t, listed, "from Beta Mod")
	assert.Equal(t, "fake:a", heir)
	assert.Eventually(t, func() bool {
		b, err := os.ReadFile(shared)
		return err == nil && string(b) == "alpha's"
	}, 10*time.Second, 50*time.Millisecond, "the uninstall handed the file to the mod the plan named")
	assert.Empty(t, f.BrowserErrors())
}
