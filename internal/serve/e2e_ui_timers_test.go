package serve_test

// #576: the scenarios that prove a dialog, a readout or a toast outlasts one
// of the page's own timers used to wait those timers out in real time - 12s
// per hold, over a minute and a half for one test. timers.js reads a scale
// the harness injects before the page's scripts run (scaleUITimers), and the
// holds are scaled from the same value, so each still waits strictly longer
// than the timer it outlasts.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eUITimerScale is how much scaleUITimers shortens the page's UI timers
// by: the 8s toast lifetime becomes 1s.
const e2eUITimerScale = 0.125

// scaledUITimer is d - one of timers.js's production durations, or a hold
// window measured against them - at e2eUITimerScale.
func scaledUITimer(d time.Duration) time.Duration {
	return time.Duration(float64(d) * e2eUITimerScale)
}

// scaleUITimersJS sets the one value timers.js reads, before any of the
// page's own scripts run.
func scaleUITimersJS(scale any) string {
	return fmt.Sprintf(`window.__lmmUITimerScale = %v;`, scale)
}

// scaleUITimers runs the page's UI timers at e2eUITimerScale from the next
// navigation on. CDP evaluates the script ahead of the page's own, whatever
// the page's CSP says, so nothing in the shell or the CSP changes for it; a
// scenario opts in before its first Navigate.
func scaleUITimers() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(scaleUITimersJS(e2eUITimerScale)).Do(ctx)
		return err
	})
}

// uiTimersJS reads every timer timers.js exports.
const uiTimersJS = `(async () => {
	const t = await import("/static/app/timers.js");
	return {
		toast: t.toastDismissMillis,
		release: t.succeededOriginReleaseMillis,
		reread: t.unreadRereadDelayMillis,
		reopen: t.activityReopenMillis,
	};
})()`

type e2eUITimers struct {
	Toast   float64 `json:"toast"`
	Release float64 `json:"release"`
	Reread  float64 `json:"reread"`
	Reopen  float64 `json:"reopen"`
}

// productionUITimers are timers.js's own values: what every browser not
// driven by this harness runs.
var productionUITimers = e2eUITimers{Toast: 8000, Release: 4000, Reread: 3000, Reopen: 3000}

func readUITimers(t *testing.T, f e2eFixture, setup ...chromedp.Action) e2eUITimers {
	t.Helper()
	var got e2eUITimers
	actions := append(setup,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(uiTimersJS, &got, awaitPromise),
	)
	f.runInBrowser(t, actions...)
	return got
}

func TestE2E_UITimers_RunAtProductionLengthsUnlessTheHarnessScalesThem(t *testing.T) {
	t.Run("unscaled", func(t *testing.T) {
		f := newE2EFixture(t)
		assert.Equal(t, productionUITimers, readUITimers(t, f))
		assert.Empty(t, f.BrowserErrors())
	})

	t.Run("scaled", func(t *testing.T) {
		f := newE2EFixture(t)
		got := readUITimers(t, f, scaleUITimers())
		want := e2eUITimers{
			Toast:   productionUITimers.Toast * e2eUITimerScale,
			Release: productionUITimers.Release * e2eUITimerScale,
			Reread:  productionUITimers.Reread * e2eUITimerScale,
			Reopen:  productionUITimers.Reopen * e2eUITimerScale,
		}
		assert.Equal(t, want, got)
		assert.Empty(t, f.BrowserErrors())
	})

	// Only a shortening in (0, 1] is a scale; anything else is ignored and
	// the page keeps its own lengths.
	for _, bad := range []string{`0`, `-1`, `2`, `NaN`, `Infinity`, `"0.5"`, `null`, `{}`} {
		t.Run("ignores "+bad, func(t *testing.T) {
			f := newE2EFixture(t)
			got := readUITimers(t, f, chromedp.ActionFunc(func(ctx context.Context) error {
				_, err := page.AddScriptToEvaluateOnNewDocument(scaleUITimersJS(bad)).Do(ctx)
				return err
			}))
			assert.Equal(t, productionUITimers, got)
			assert.Empty(t, f.BrowserErrors())
		})
	}
}

// TestScaledE2EHoldsOutlastTheirTimers pins the ordering the
// survival scenarios rely on: each scaled hold is strictly longer, with room
// to spare, than the scaled timer it outlasts.
func TestScaledE2EHoldsOutlastTheirTimers(t *testing.T) {
	const margin = 200 * time.Millisecond
	toast := scaledUITimer(time.Duration(productionUITimers.Toast) * time.Millisecond)
	release := scaledUITimer(time.Duration(productionUITimers.Release) * time.Millisecond)
	reread := scaledUITimer(time.Duration(productionUITimers.Reread) * time.Millisecond)
	for _, timer := range []time.Duration{toast, release, reread} {
		require.Greater(t, dialogSurvivalWait, timer+margin)
	}
	require.Greater(t, readoutReleaseWait, release+margin)
	require.Greater(t, toastSurvivalWait, toast+margin)
}
