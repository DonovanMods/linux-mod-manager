package serve_test

// The SPA's per-key slice fence (issue 370).
//
// Two scenarios, one property. The first drives spa/app/slicefence.js and
// spa/app/store.js directly - no DOM, no server - with the two responses
// forced to resolve in the wrong order, which is the race stated as
// plainly as it can be. The second proves main.js's real reload path
// actually carries the fence, by holding the FIRST /api/v1/mods response
// in the browser and driving two ordinary UI mutations over it - held
// until the reload issued after it has committed, so the wrong order is
// what the scenario ARRANGES rather than what it hopes for.

import (
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSliceFence_TheOlderAnswerIsDropped is the race at store level: two
// loads of the same slice, the first one resolving LAST. Without the fence
// both commit and the store keeps whichever landed last - the older
// document. With it, the load that was issued last owns the slice and the
// late answer is dropped.
//
// Run against the real modules in a real browser (the same dynamic-import
// pattern TestSortRows_RecentToleratesMissingInstalledAt uses) because
// there is no other runtime for this application's JavaScript.
func TestSliceFence_TheOlderAnswerIsDropped(t *testing.T) {
	f := newE2EFixture(t)

	var got struct {
		Mods    string  `json:"mods"`
		Updates string  `json:"updates"`
		Error   *string `json:"error"`
		First   bool    `json:"first"`
		Second  bool    `json:"second"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.Evaluate(`(async () => {
			const { createStore } = await import("/static/app/store.js");
			const { createSliceFence } = await import("/static/app/slicefence.js");

			const store = createStore();
			const fence = createSliceFence(store);

			// Two reloads of the SAME key, claimed in issue order, with the
			// FIRST one's response resolving last.
			const reload = (key, value, delayMs) => {
				const claim = fence.claim([key]);
				return new Promise((resolve) => setTimeout(resolve, delayMs))
					.then(() => fence.commit(claim, { [key]: value }, { [key]: null }));
			};

			const first = reload("mods", "older", 60);
			const second = reload("mods", "newer", 0);
			// A DIFFERENT slice claimed in between must be untouched by
			// either: the fence is per-key, not global.
			const other = reload("updates", "updates-answer", 20);
			const [firstWrote, secondWrote] = await Promise.all([first, second, other]);

			return {
				mods: store.get().mods,
				updates: store.get().updates,
				error: store.get().fetchErrors.mods,
				first: firstWrote,
				second: secondWrote,
			};
		})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	assert.Equal(t, "newer", got.Mods,
		"the answer asked for LAST must own the slice, whatever order the responses arrived in")
	assert.False(t, got.First, "the superseded load must report that it wrote nothing")
	assert.True(t, got.Second, "the newest load must still commit")
	assert.Nil(t, got.Error, "a dropped answer must not drag its fetchErrors entry in with it")
	assert.Equal(t, "updates-answer", got.Updates,
		"a claim on one key must not invalidate another key's load")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_TwoModSettingsInFlightKeepTheNewerLibrary is issue 370 through
// the application itself: Lock one row, then Lock another before the first
// one's /api/v1/mods reload has come back.
//
// Each lock is a thin synchronous write followed by
// main.js#refreshAfterModSetting's reload of the library, so two clicks in
// quick succession leave two GETs of the same slice racing. The first one's
// response is held here (a fetch wrapper installed after the initial
// hydrate has settled) so it lands LAST while carrying the pre-second-lock
// library - the exact ordering that, before the fence, put the older
// document back and left a badge that was never coming.
//
// The ordering is driven by two OBSERVED facts rather than by timers, so
// the scenario reproduces the bug every run rather than in whichever runs
// the server happens to lose (issue 370's review, Important 1): the second
// Lock is not clicked until the held GET has been ANSWERED by the server,
// which is what makes its body provably older, and the assertions do not
// run until that answer has been DELIVERED to the SPA, which is what makes
// it provably last. Neither edge can land the other way round.
func TestE2E_TwoModSettingsInFlightKeepTheNewerLibrary(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	lockBadge := func(name string) string {
		return `Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes("` +
			name + `")).querySelector('.badge[title^="Locked"]')`
	}
	lockRow := func(name string) chromedp.Tasks {
		return chromedp.Tasks{
			chromedp.Evaluate(`
				Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes("`+name+`"))
					.querySelector("td.col--menu button").click();
			`, nil),
			chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
			chromedp.Evaluate(`
				Array.from(document.querySelectorAll(".row-menu__item")).find((b) => b.textContent.trim() === "Lock").click();
			`, nil),
		}
	}

	// Hold the NEXT /api/v1/mods response - and only that one - until the
	// reload issued after it has not merely resolved but COMMITTED, which
	// the rendered library says out loud by carrying both locks.
	holdFirstLibraryReload := chromedp.Evaluate(`(() => {
		const real = window.fetch;
		let held = false;
		window.__heldAnswered = false;
		window.__olderDelivered = false;
		window.__bailed = false;
		const newerCommitted = () => {
			try {
				return `+lockBadge("Alpha Mod")+` !== null && `+lockBadge("Beta Mod")+` !== null;
			} catch (e) {
				// Mid-render the rows can be momentarily absent; that is
				// simply "not yet", never a failure to report.
				return false;
			}
		};
		window.fetch = (input, init) => {
			const raw = input && input.url ? input.url : input;
			const url = new URL(String(raw), location.origin);
			const method = (init && init.method) || (input && input.method) || "GET";
			const answer = real(input, init);
			// The library LISTING only - the lock writes themselves live
			// under /api/v1/mods/... and holding one of those would
			// serialise the very pair this scenario needs overlapping.
			if (held || method !== "GET" || url.pathname !== "/api/v1/mods") return answer;
			held = true;
			return answer.then((r) => {
				// The SERVER has answered: this response carries the
				// library from before the second Lock, which the test does
				// not click until it sees this flag. Only its DELIVERY is
				// delayed from here on.
				window.__heldAnswered = true;
				return new Promise((res) => {
					const deliver = () => {
						window.__olderDelivered = true;
						res(r);
					};
					const poll = setInterval(() => {
						if (!newerCommitted()) return;
						clearInterval(poll);
						clearTimeout(bail);
						deliver();
					}, 20);
					// A deadline, so a scenario that never gets its newer
					// commit fails an assertion instead of hanging inside a
					// fetch nobody is ever going to resolve.
					const bail = setTimeout(() => {
						clearInterval(poll);
						window.__bailed = true;
						deliver();
					}, 15000);
				});
			});
		};
	})()`, nil)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		holdFirstLibraryReload,
		lockRow("Beta Mod"),
		pollUntil(`window.__heldAnswered === true`),
		lockRow("Alpha Mod"),
	)

	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "default")
		if err != nil {
			return false
		}
		locked := 0
		for _, ref := range p.Mods {
			if ref.Locked {
				locked++
			}
		}
		return locked == 2
	}, 10*time.Second, 20*time.Millisecond, "both Lock actions must reach core")

	// Both badges, and they must STAY: the held reload is delivered only
	// after the newer one has committed, carrying a library where only Beta
	// is locked, and must be dropped rather than applied.
	var alpha, beta, bailed bool
	f.runInBrowser(t,
		pollUntil(`window.__olderDelivered === true`),
		// The older answer is in the SPA's hands; give the commit it would
		// make without the fence room to land and repaint.
		chromedp.Sleep(time.Second),
		chromedp.Evaluate(lockBadge("Alpha Mod")+` !== null`, &alpha),
		chromedp.Evaluate(lockBadge("Beta Mod")+` !== null`, &beta),
		chromedp.Evaluate(`window.__bailed === true`, &bailed),
	)
	require.False(t, bailed,
		"the newer reload must have committed before the held one was released - otherwise this scenario proves nothing")
	assert.True(t, alpha, "a superseded reload must not put the pre-lock library back")
	assert.True(t, beta, "the earlier lock must survive the later one's reload")
	assert.Empty(t, f.BrowserErrors())
}
