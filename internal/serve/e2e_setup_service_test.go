package serve_test

// #649: Setup > Updates names the lmm binary lmm-serve.service runs, and
// warns - with the command that fixes it - when that is not the binary
// this web UI runs. The service status is a fake: no test asks the real
// user manager.

import (
	"errors"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
)

// newE2EFixtureForService serves a fixture whose GET /api/v1/service is
// status.
func newE2EFixtureForService(t *testing.T, status serve.ServiceStatuser) e2eFixture {
	t.Helper()
	sandboxE2EEnv(t)
	svc, game := newFixtureServiceWithSource(t, newFakeSource("fake"))
	baseURL := startE2EServerWithOptions(t, svc, func(o *serve.Options) { o.ServiceStatus = status })
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}
}

func TestE2E_SetupUpdatesWarnsTheServiceRunsAnotherBinary(t *testing.T) {
	f := newE2EFixtureForService(t, fakeServiceStatus{report: mismatchReport})

	var row, warning string
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("updates")),
		chromedp.WaitVisible(`[data-testid="setup-updates-service-mismatch"]`, chromedp.ByQuery),
		textContent(`[data-testid="setup-updates-service"]`, &row),
		textContent(`[data-testid="setup-updates-service-mismatch"]`, &warning),
	)
	assert.Contains(t, row, "/home/u/go/1.27.3/bin/lmm (2.2.0)")
	assert.Contains(t, row, "running")
	assert.Contains(t, warning, "this web UI runs /home/u/go/1.27.4/bin/lmm (2.3.0)")
	assert.Contains(t, warning, "lmm service restart --switch")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

func TestE2E_SetupUpdatesServiceOnThisBinaryHasNoWarning(t *testing.T) {
	report := mismatchReport
	unit := report.Units[0]
	unit.Binary, unit.BinaryMismatch = report.Running, false
	report.Units = []core.UnitStatus{unit}
	f := newE2EFixtureForService(t, fakeServiceStatus{report: report})

	var row string
	var warned bool
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("updates")),
		chromedp.WaitVisible(`[data-testid="setup-updates-service"]`, chromedp.ByQuery),
		textContent(`[data-testid="setup-updates-service"]`, &row),
		chromedp.Evaluate(`document.querySelector('[data-testid="setup-updates-service-mismatch"]') !== null`, &warned),
	)
	assert.Contains(t, row, "/home/u/go/1.27.4/bin/lmm (2.3.0)")
	assert.False(t, warned)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// No systemd user manager is said, not hidden: the row names the reason.
func TestE2E_SetupUpdatesServiceUnavailable(t *testing.T) {
	f := newE2EFixtureForService(t, fakeServiceStatus{err: &core.SystemdUnavailableError{Reason: "systemctl is not installed", Err: errors.New("exec")}})

	var row string
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("updates")),
		chromedp.WaitVisible(`[data-testid="setup-updates-service"]`, chromedp.ByQuery),
		textContent(`[data-testid="setup-updates-service"]`, &row),
	)
	assert.Contains(t, row, "systemctl is not installed")
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/service")
}

// A drop-in that resets ExecStart leaves no program to name: the row says
// so, and there is nothing to warn about (#649 review).
func TestE2E_SetupUpdatesServiceUnknownBinary(t *testing.T) {
	report := mismatchReport
	unit := report.Units[0]
	unit.Binary, unit.BinaryMismatch = &core.LmmBinary{Version: core.UnknownBinaryVersion}, false
	report.Units = []core.UnitStatus{unit}
	f := newE2EFixtureForService(t, fakeServiceStatus{report: report})

	var row string
	var warned bool
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("updates")),
		chromedp.WaitVisible(`[data-testid="setup-updates-service"]`, chromedp.ByQuery),
		textContent(`[data-testid="setup-updates-service"]`, &row),
		chromedp.Evaluate(`document.querySelector('[data-testid="setup-updates-service-mismatch"]') !== null`, &warned),
	)
	assert.Contains(t, row, "a program lmm cannot name")
	assert.Contains(t, row, "systemctl --user cat lmm-serve")
	assert.False(t, warned)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
