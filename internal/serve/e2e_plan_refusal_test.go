package serve_test

// #461: a plan on a game whose adapter core refuses is a 409 carrying the
// typed refusal, and the confirm modal renders it as one - core's sentence
// and where the fix is made - with nothing to confirm.

import (
	"strings"
	"testing"

	"github.com/chromedp/cdproto/runtime"
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
	assert.Contains(t, body, "games.yaml entry", "where the fix is made")
	assert.Contains(t, body, "lmm game edit "+f.Game.ID+" --adapter <name>")
	assert.Zero(t, confirms, "a refused plan offers nothing to confirm")
	// The 409 is the one expected failed request; nothing else may error.
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/plans/deploy", "/api/v1/conflicts")
}

// TestE2E_AdapterRefusalWithAReason_SkipsTheGenericRemedyAndDoesNotRepeatIt
// is review F3: an AdapterPreconditionError's `reason` IS the adapter's own
// remedy, and core's Error() already embeds it verbatim in the message this
// component sits under (confirmplan.js's codeSpans(error), above
// ErrorDetails). So AdapterRefusal must neither append the generic "change
// the adapter" sentence, which would point the user the wrong way, nor
// render the reason again itself. The real, unmodified component is
// rendered directly (the same pure-module idiom
// TestE2E_UnseenFailureBadgeIgnoresAJobWithNoEndTime uses), since no
// production adapter implements Preconditioner today (#461's own report)
// and so no plan refusal reaches this shape yet.
func TestE2E_AdapterRefusalWithAReason_SkipsTheGenericRemedyAndDoesNotRepeatIt(t *testing.T) {
	f := newE2EFixture(t)

	var html string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { render, h } = await import("/static/app/render.js");
			const { AdapterRefusal } = await import("/static/app/components/errordetails.js");

			const container = document.createElement("div");
			document.body.appendChild(container);

			render(h(AdapterRefusal, {
				refusal: { gameID: "g1", adapter: "bepinex", reason: "install BepInEx first" },
			}), container);

			const out = container.innerHTML;
			container.remove();
			return out;
		})()`, &html, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
	)
	assert.NotContains(t, html, "install BepInEx first", "the reason already shows in the message above - this component does not repeat it")
	assert.NotContains(t, html, "change it in its games.yaml entry", "the generic remedy is wrong when the adapter's own reason is the fix")
	assert.NotContains(t, html, "lmm game edit", "no generic --adapter command when the reason already names the fix")
	assert.Empty(t, f.BrowserErrors())
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
