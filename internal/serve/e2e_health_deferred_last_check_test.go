package serve_test

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_DeferredHealthIsOnlyTheLastCheckEverywhere is #588's review: while
// another lmm holds the operation lock, health answers with the last check,
// deferred. Every surface that shows findings treats them as that - the
// library offers no Repair and no "Unhealthy" match from them, the mod panel
// and the full mod page head them "Last check" - and the full mod page, like
// Mission Control, asks again on its own once the lock is let go: no job in
// this server ends to tell it to.
func TestE2E_DeferredHealthIsOnlyTheLastCheckEverywhere(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".oplock")
	f := newE2EFixtureWithAttentionLocked(t, lockPath)

	// The last check: Better Boots has a finding.
	f.runInBrowser(t,
		scaleUITimers(),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="row-health"][data-health="issues"]') !== null`),
	)

	// The other process takes the lock.
	holder, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Close() })
	require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_UN))
		}
	}
	t.Cleanup(unlock)

	var items []string
	var unhealthyRows int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--health [data-testid="health-deferred"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(openRowMenuJS("Better Boots"), nil),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
		chromedp.Evaluate(rowMenuItemsJS, &items),
		chromedp.KeyEvent("\x1b"),
		chromedp.SetValue(`.library__toolbar select[name="filter"]`, "unhealthy", chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`document.querySelectorAll(".mod-row").length`, &unhealthyRows),
	)
	assert.Contains(t, items, "Verify")
	assert.NotContains(t, items, "Repair…", "no repair offered from a check the job may be overtaking")
	assert.Zero(t, unhealthyRows, "the stale findings make no row Unhealthy")

	var panelHeading, pageHeading, pageAfter string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "boots")),
		chromedp.WaitVisible(`[data-testid="mod-findings"]`, chromedp.ByQuery),
		textContent(`[data-testid="mod-findings"] h3`, &panelHeading),
		chromedp.Navigate(f.ModPagePath("fake", "boots")),
		chromedp.WaitVisible(`[data-testid="mod-page-findings"]`, chromedp.ByQuery),
		textContent(`[data-testid="mod-page-findings"] h2`, &pageHeading),
		chromedp.ActionFunc(func(context.Context) error { unlock(); return nil }),
		pollUntil(`!document.querySelector('[data-testid="mod-page-findings"]').hasAttribute("data-deferred")`),
		textContent(`[data-testid="mod-page-findings"] h2`, &pageAfter),
	)
	assert.Equal(t, "Last check: findings (2)", panelHeading)
	assert.Equal(t, "Last check: findings (2)", pageHeading)
	assert.Equal(t, "Findings (2)", pageAfter, "the full mod page asked again once the lock was free")
	assert.Empty(t, f.BrowserErrors())
}
