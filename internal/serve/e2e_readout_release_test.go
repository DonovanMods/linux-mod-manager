package serve_test

// #531's audit: main.js#releaseSucceededOrigin hands a finished job's control
// back four seconds after it succeeds, on the rule that "a success has nothing
// left to say". A job whose state is "succeeded" can still have something to
// say - an update batch whose items failed, a repair that left a mod still
// failing, a profile switch's recovery notice - and each readout below
// carried its next step for four seconds and then took it away. Each test
// holds the readout on screen well past the release.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

// readoutReleaseWait is past the 4s release, with room for a slow runner.
const readoutReleaseWait = 8 * time.Second

func TestE2E_ReadoutRelease_FailedUpdateBatchStaysUntilDismissed(t *testing.T) {
	f := newE2EFixtureWithManualUpdate(t)
	// Not a manual-download refusal: a plain failure has no way out to offer,
	// so only the tally says the batch did not work.
	f.Src.urlErrs["manualup"] = errors.New("the download server is down")

	const readout = `.library__toolbar .job-progress[data-state="succeeded"]`
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.library__toolbar [data-action="update-all"]')?.textContent.includes("1")`),
		clickWhenSettled(`.library__toolbar [data-action="update-all"]`),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		chromedp.WaitVisible(readout+`.job-progress--failed`, chromedp.ByQuery),
	)
	holdsFor(t, f.Ctx, readoutReleaseWait, "a batch whose only item failed",
		fmt.Sprintf(`document.querySelector(%q) !== null`, readout))
	f.runInBrowser(t,
		clickWhenSettled(`.library__toolbar .job-progress__dismiss`),
		waitGone(`.library__toolbar .job-progress`),
	)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

func TestE2E_ReadoutRelease_VerifyFixResultStaysUntilDismissed(t *testing.T) {
	f := newE2EFixtureWithRepairableMods(t)

	f.runInBrowser(t, repairAllFromHealthCard(f)...)
	f.runInBrowser(t,
		chromedp.WaitVisible(`.card--health [data-testid="verify-fix-result"]`, chromedp.ByQuery),
	)
	holdsFor(t, f.Ctx, readoutReleaseWait, "a repair that left a mod still failing",
		`document.querySelector('.card--health [data-testid="verify-fix-result"]') !== null`)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

func TestE2E_ReadoutRelease_FlagOnlySwitchNoticeStaysUntilDismissed(t *testing.T) {
	f := newE2EFixtureWithASharedDeployment(t)
	path := filepath.Join(f.Svc.ConfigDir(), "games", f.Game.ID, "profiles", "default.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "is_default: true\n", "")), 0o644))

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Click(`[data-action="switch"][data-profile="alt"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="switch-flag-only"]') !== null`),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		pollUntil(`document.querySelector('[data-testid="job-result-warning"]') !== null`),
	)
	holdsFor(t, f.Ctx, readoutReleaseWait, "a flag-only switch's recovery notice",
		`document.querySelector('[data-testid="job-result-warning"]') !== null`)
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/plans/deploy")
}
