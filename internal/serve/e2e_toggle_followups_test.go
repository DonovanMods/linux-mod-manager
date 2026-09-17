package serve_test

// #454: two toggle-ledger follow-ups. N1 - the full mod page showed the
// pre-job button after a job whose library read failed, though it held a
// fresher files report, and nothing re-read afterwards. N2 - a re-hydrate
// whose status read failed skipped the library read, so a toggle waited out
// its whole deadline and was then blamed on a server that had answered.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// modPageButtonJS is the full mod page's toggle label, or "" before it
// renders.
const modPageButtonJS = `(() => {
	const b = document.querySelector('.mod-page [data-action="toggle"]');
	return b === null ? "" : b.textContent.trim();
})()`

// TestE2E_FullModPage_AFailedSettlingReadShowsTheFresherFilesReport is N1.
func TestE2E_FullModPage_AFailedSettlingReadShowsTheFresherFilesReport(t *testing.T) {
	f, wire := newToggleWireFixture(t, catalogSource(map[string]string{"a": "Alpha Mod", "b": "Beta Mod"}), 1<<30)
	seedDeployableMods(t, f.Svc, f.Game)

	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		pollUntil(modPageButtonJS+` === "Disable"`),
		pollUntil(`document.querySelector('.mod-page__description, .mod-page__section') !== null`),
	)

	// Every library read fails from here on: the job's own re-read too.
	wire.failMods.Store(true)
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector('.mod-page [data-action="toggle"]').click()`, nil))
	awaitJobsOver(t, f, wire, map[string]bool{"a": false})

	runWithin(t, f, 20*time.Second, pollUntil(toastSaysJS("could not be read")))
	// The server recovers well inside the page's re-read delay.
	readsAtToast := wire.modsReads.Load()
	wire.failMods.Store(false)

	var label string
	var toasts []string
	runWithin(t, f, 20*time.Second,
		pollUntil(`(() => {
			const b = document.querySelector('.mod-page [data-action="toggle"]');
			return b !== null && !b.disabled;
		})()`),
		settleEffects(),
		chromedp.Evaluate(modPageButtonJS, &label),
		chromedp.Evaluate(toastTextsJS, &toasts),
	)
	assert.Equal(t, "Enable", label,
		"the page shows the files report read after the job, not the library document from before it")
	assert.Contains(t, strings.Join(toasts, " | "), "Alpha Mod")

	// The page asks again on its own, and a read that succeeds keeps the
	// answer it already shows.
	require.Eventually(t, func() bool { return wire.modsReads.Load() > readsAtToast },
		15*time.Second, 50*time.Millisecond, "a failed settling read schedules a re-read")
	runWithin(t, f, 20*time.Second,
		pollUntil(`(() => {
			const b = document.querySelector('.mod-page [data-action="toggle"]');
			return b !== null && b.textContent.trim() === "Enable" && !b.disabled;
		})()`),
	)
	assert.False(t, modEnabled(t, f, "a"))
	assert.Empty(t, uncaughtErrors(f))
}

// TestE2E_LibraryToggle_AFailedStatusReadStillSettlesOnTheLibraryRead is N2.
func TestE2E_LibraryToggle_AFailedStatusReadStillSettlesOnTheLibraryRead(t *testing.T) {
	f, wire := newToggleWireFixture(t, newFakeSource("fake"), 1<<30)
	seedDeployableMods(t, f.Svc, f.Game)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
	)

	wire.failStatus.Store(true)
	start := time.Now()
	f.runInBrowser(t, chromedp.Evaluate(libraryToggleJS("Alpha Mod"), nil))
	awaitJobsOver(t, f, wire, map[string]bool{"a": false})

	var row e2eRowToggleState
	var toasts []string
	runWithin(t, f, 15*time.Second,
		pollUntil(noRowPendingJS),
		chromedp.Evaluate(libraryRowStateJS("Alpha Mod"), &row),
		chromedp.Evaluate(toastTextsJS, &toasts),
	)
	assert.Less(t, time.Since(start), 30*time.Second,
		"the library read settles the request; it does not wait out the 60-second deadline")
	assert.False(t, row.Checked, "the row shows the library read taken after the job")
	assert.False(t, row.Pending)
	assert.NotContains(t, strings.Join(toasts, " | "), "no answer within",
		"no toast blames a server that answered")

	wire.failStatus.Store(false)
	f.runInBrowser(t, chromedp.ActionFunc(func(context.Context) error { return nil }))
	assert.Empty(t, uncaughtErrors(f))
}
