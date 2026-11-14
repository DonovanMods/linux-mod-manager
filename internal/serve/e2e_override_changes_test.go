package serve_test

// #631 in the browser: a switch's confirm lists the override changes it
// makes - here a stock INI the outgoing profile's override replaced, put
// back - and confirming it does so.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_SwitchPlan_ListsTheOverrideChanges(t *testing.T) {
	f := newE2EFixtureWithASharedDeployment(t)
	ini := filepath.Join(f.Game.InstallPath, "Engine.ini")
	require.NoError(t, os.WriteFile(ini, []byte("stock"), 0o644))
	p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "default")
	require.NoError(t, err)
	p.Overrides = map[string][]byte{"Engine.ini": []byte("tweaked")}
	require.NoError(t, config.SaveProfile(f.Svc.ConfigDir(), p))
	_, err = f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	got, err := os.ReadFile(ini)
	require.NoError(t, err)
	require.Equal(t, "tweaked", string(got))

	var listed, action string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Click(`[data-action="switch"][data-profile="alt"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="switch-overrides"]') !== null`),
		textContent(`[data-testid="switch-overrides"]`, &listed),
		chromedp.AttributeValue(`[data-testid="switch-overrides"] li`, "data-action", &action, nil, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)
	assert.Contains(t, listed, "<game root>/Engine.ini")
	assert.Contains(t, listed, "take the override away")
	assert.Equal(t, "restore", action)
	assert.Eventually(t, func() bool {
		b, err := os.ReadFile(ini)
		return err == nil && string(b) == "stock"
	}, 10*time.Second, 50*time.Millisecond, "the switch put the game's own file back")
	assert.Empty(t, f.BrowserErrors())
}
