package serve_test

// #595: a succeeded job's readout hands its control back after
// timers.js#succeededOriginReleaseMillis, and a test that waits for
// `.job-progress[data-state="succeeded"]` was racing that timer - a runner
// that stalled past it found the readout gone and burned the whole harness
// budget on a selector that was correct. The harness now holds every
// succeeded readout in every tab it opens (holdSucceededReadoutsJS); the
// scenarios that are ABOUT the release - or that wait for a toggle's button
// to come back through it - opt back in with releaseSucceededReadouts.
// TestE2E_SucceededDeployReleasesTheButtonOnItsOwn keeps the release
// itself covered.

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
)

// deployFromTopBar runs the top bar's Deploy through its confirm modal and
// waits for the succeeded readout.
func deployFromTopBar(f e2eFixture) chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="deploy"]`),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	}
}

// TestE2E_SucceededReadoutOutlastsAStalledTest is the issue's reproduction:
// a stall longer than the release window between the job finishing and the
// test looking at its readout. The release is scaled to 500ms here, so the
// stall is 1s.
func TestE2E_SucceededReadoutOutlastsAStalledTest(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var present bool
	f.runInBrowser(t,
		scaleUITimers(),
		deployFromTopBar(f),
		chromedp.Sleep(scaledUITimer(8*time.Second)),
		chromedp.Evaluate(`document.querySelector('.job-progress[data-state="succeeded"]') !== null`, &present),
	)
	assert.True(t, present, "the harness holds a succeeded readout for as long as the test takes")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
