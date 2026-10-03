package serve_test

// #526: two leftovers from #520's and #521's screenshot reviews - the
// library table's "Load order" heading wrapped onto two lines in every face,
// and the add-game form's "Hide installed games" toggle stretched across its
// whole panel. Both are claims about where a BROWSER puts things.

import (
	"fmt"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// headerLinesJS counts, for every VISIBLE library header cell, the distinct
// lines its TEXT is laid out on (a hidden progressive column has no boxes at
// all; the select-all checkbox and a visually-hidden label are not text a
// reader sees wrap), keyed by the cell's column class.
const headerLinesJS = `Object.fromEntries([...document.querySelectorAll(".library__table thead th")]
	.filter((th) => th.getClientRects().length > 0)
	.map((th) => {
		const tops = new Set();
		const walker = document.createTreeWalker(th, NodeFilter.SHOW_TEXT);
		for (let node = walker.nextNode(); node; node = walker.nextNode()) {
			if (!node.data.trim() || node.parentElement.closest(".visually-hidden")) continue;
			const range = document.createRange();
			range.selectNodeContents(node);
			for (const r of range.getClientRects()) {
				if (r.width > 0 && r.height > 0) tops.add(Math.round(r.top));
			}
		}
		return [th.className || th.textContent.trim(), tops.size];
	}))`

// TestE2E_LibraryHeaderCellsAreOneLine holds every library header cell to a
// single line at 1280px and at each wider step the progressive columns add
// (1440: author + installed, 1920: source + method), in every layout face.
func TestE2E_LibraryHeaderCellsAreOneLine(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		for _, width := range []int{1280, 1440, 1920, 2560} {
			var lines map[string]int
			f.runInBrowser(t,
				chromedp.EmulateViewport(int64(width), 900),
				chromedp.Navigate(f.HomePath()),
				pollUntil(`document.querySelectorAll(".library__table .mod-row").length === 3`),
				faceInEffect(face),
				chromedp.Evaluate(headerLinesJS, &lines),
			)
			assert.Equal(t, 1, lines["col--order"], "%dpx: the load-order header is drawn, on one line", width)
			for cell, n := range lines {
				// The actions column's heading is visually hidden: no lines at all.
				assert.LessOrEqual(t, n, 1, "%dpx: header cell %q is on %d lines", width, cell, n)
			}
		}
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_AddGamePickToggleSizesToItsLabel holds the add-game form's
// "Pick an installed game…" / "Hide installed games" toggle to its own
// label's width - a column flex container stretched it across the panel -
// and checks it is still a working toggle.
func TestE2E_AddGamePickToggleSizesToItsLabel(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	writeE2ESteamDetectFixture(t, f.Svc.ConfigDir())

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		const toggle = `[data-action="pick-installed"]`
		widths := func() (button, panel float64) {
			f.runInBrowser(t,
				chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).getBoundingClientRect().width`, toggle), &button),
				chromedp.Evaluate(`document.querySelector('[data-testid="setup-add-game"]').getBoundingClientRect().width`, &panel),
			)
			require.Positive(t, panel)
			return button, panel
		}

		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.BaseURL+"/"),
			pollUntil(`document.querySelector('[data-testid="setup-add-game"]') !== null`),
			faceInEffect(face),
		)
		button, panel := widths()
		assert.Less(t, button, panel/2, "Pick an installed game… is %.0fpx wide in a %.0fpx form", button, panel)

		var label string
		f.runInBrowser(t,
			clickWhenSettled(toggle),
			pollUntil(`document.querySelector('[data-testid="setup-add-picker"]') !== null`),
			chromedp.Text(toggle, &label, chromedp.ByQuery),
		)
		assert.Equal(t, "Hide installed games", label)
		button, panel = widths()
		assert.Less(t, button, panel/2, "Hide installed games is %.0fpx wide in a %.0fpx form", button, panel)

		f.runInBrowser(t,
			clickWhenSettled(toggle),
			pollUntil(`document.querySelector('[data-testid="setup-add-picker"]') === null`),
			chromedp.Text(toggle, &label, chromedp.ByQuery),
		)
		assert.Equal(t, "Pick an installed game…", label, "a second press closes the picker again")
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_AddGameFormFieldsShareOneWidth holds every field of the add-game
// form to one width - the Source select, the text inputs, the Advanced game
// id, and the mod-loader select and its follow-up fields, which sit in a
// wrapper the form's own width rule did not reach - and its submit button
// to its label's width, in every face.
func TestE2E_AddGameFormFieldsShareOneWidth(t *testing.T) {
	f := newE2EFixtureNoGames(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var got struct {
			Fields map[string]float64 `json:"fields"`
			Submit float64            `json:"submit"`
			Form   float64            `json:"form"`
		}
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.BaseURL+"/"),
			pollUntil(`document.querySelector('[data-testid="setup-add-game"]') !== null`),
			faceInEffect(face),
			// Open everything that hides a field: Advanced, and the
			// loader's follow-up fields behind a chosen loader.
			chromedp.Evaluate(`document.querySelector('[data-testid="setup-add-advanced"]').open = true`, nil),
			chromedp.SetValue(`select[name="loader-kind"]`, "bepinex", chromedp.ByQuery),
			pollUntil(`document.querySelector('select[name="loader-runtime"]') !== null`),
			chromedp.Evaluate(`(() => {
				const form = document.querySelector('[data-testid="setup-add-game"]');
				const fields = {};
				for (const el of form.querySelectorAll('input:not([type=checkbox]), select')) {
					if (el.getClientRects().length === 0) continue;
					fields[el.name || el.getAttribute("aria-label")] = el.getBoundingClientRect().width;
				}
				return {
					fields,
					submit: form.querySelector('[data-action="add-game"]').getBoundingClientRect().width,
					form: form.getBoundingClientRect().width,
				};
			})()`, &got),
		)
		require.GreaterOrEqual(t, len(got.Fields), 7, "source, identifier, game id, name, install path, mod path, loader fields: %v", got.Fields)
		require.Positive(t, got.Form)
		first := got.Fields["add-source"]
		require.Positive(t, first)
		for name, width := range got.Fields {
			assert.InDelta(t, first, width, 1, "field %q is %.0fpx wide, the Source select %.0fpx", name, width, first)
		}
		assert.Less(t, got.Submit, got.Form/2, "Add game is %.0fpx wide in a %.0fpx form", got.Submit, got.Form)
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
