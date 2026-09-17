package serve_test

// #461: a plan on a game whose adapter core refuses is a 409 carrying the
// typed refusal, and the confirm modal renders it as one - core's sentence
// and where the fix is made - with nothing to confirm.

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_DeployOnARefusedAdapter_ConfirmShowsTheRefusalAndItsRemedy(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)
	f.Game.Adapter = "no-such-adapter"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	var body string
	var confirms int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="deploy"]`),
		pollUntil(`document.querySelector('.modal[data-kind="deploy"] [data-testid="adapter-refusal"]') !== null`),
		textContent(`.modal[data-kind="deploy"]`, &body),
		chromedp.Evaluate(`document.querySelectorAll('.modal[data-kind="deploy"] [data-action="confirm"]').length`, &confirms),
	)
	assert.Contains(t, body, `unknown adapter "no-such-adapter"`, "core's sentence, verbatim")
	assert.Contains(t, body, "Setup → Games", "where the fix is made")
	assert.Contains(t, body, "lmm game edit "+f.Game.ID+" --adapter <name>")
	assert.Zero(t, confirms, "a refused plan offers nothing to confirm")
	// The 409 is the one expected failed request; nothing else may error.
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/plans/deploy", "/api/v1/conflicts")
}

// assertOnlyExpectedErrors fails on any browser error that is not a failed
// request to one of the named paths - the refusals a scenario provokes on
// purpose, which a browser logs as a network error like any other 4xx.
func assertOnlyExpectedErrors(t *testing.T, errs []string, paths ...string) {
	t.Helper()
	var unexpected []string
next:
	for _, e := range errs {
		for _, p := range paths {
			if strings.Contains(e, p) {
				continue next
			}
		}
		unexpected = append(unexpected, e)
	}
	assert.Empty(t, unexpected)
}
