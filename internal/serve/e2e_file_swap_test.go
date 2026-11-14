package serve_test

// #639 in the browser: a switch to a profile that pins another file of the
// version installed says it reinstalls the mod because its files changed,
// and confirming it swaps the deployed file.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_SwitchPlan_NamesASameVersionFileSwapAsAReinstall(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	ctx := context.Background()
	pm := f.Svc.NewProfileManager()
	// Multi Edition Mod ships two files at 1.0: default installs the
	// Regular Edition (m1), "other" pins the Definitive Edition (m2).
	require.NoError(t, pm.AddMod(ctx, f.Game.ID, "default", domain.ModReference{SourceID: "fake", ModID: e2eSearchMultiFileModID, Version: "1.0", FileIDs: []string{"m1"}}))
	plan, err := f.Svc.PlanProfileApply(ctx, f.Game, "default")
	require.NoError(t, err)
	_, err = f.Svc.ApplyProfileApply(ctx, f.Game, plan, core.ProfileApplyOptions{}, nil)
	require.NoError(t, err)
	regular := filepath.Join(f.Game.ModPath, "Mods", "multi-regular.pak")
	definitive := filepath.Join(f.Game.ModPath, "Mods", "multi-definitive.pak")
	_, err = os.Lstat(regular)
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(ctx, f.Game.ID, "other", domain.ModReference{SourceID: "fake", ModID: e2eSearchMultiFileModID, Version: "1.0", FileIDs: []string{"m2"}}))

	var reason string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Click(`[data-action="switch"][data-profile="other"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="switch-to-install"] [data-testid="reinstall-reason"]') !== null`),
		textContent(`[data-testid="switch-to-install"] [data-testid="reinstall-reason"]`, &reason),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)
	assert.Equal(t, "reinstall: files changed", reason)
	assert.Eventually(t, func() bool {
		_, errDef := os.Lstat(definitive)
		_, errReg := os.Lstat(regular)
		return errDef == nil && os.IsNotExist(errReg)
	}, 10*time.Second, 50*time.Millisecond, "the switch swapped the deployed file")
	assert.Empty(t, f.BrowserErrors())
}
