package serve_test

// #518: a button's label sits in the middle of the button, on one line, and
// a row of default-size controls still fits where it did before the control
// system made them all default size. A min-height alone leaves the label on
// a line box taller than its text, with the spare height below it; only a
// browser can say where the text actually lands.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// labelCentringJS measures every visible text-labelled control under each
// named root - a .button, a <button> or a link drawn as one (bar the
// prose-shaped ones: the text-shaped link, the card-shaped tile and the
// title-shaped prose button, which wrap by design) or a .menu-item: the
// control's border box against the box of its own text (a Range over its
// contents), and how many lines that text takes.
const labelCentringJS = `((roots) => {
	const out = {};
	for (const [name, sel] of Object.entries(roots)) {
		const root = document.querySelector(sel);
		if (!root) { out[name] = null; continue; }
		out[name] = [...root.querySelectorAll("button.button, a.button, button.menu-item")]
			.filter((b) => {
				const r = b.getBoundingClientRect();
				return r.width > 0 && r.height > 0 && b.textContent.trim() !== "" &&
					!["button--link", "button--tile", "button--prose"].some((c) => b.classList.contains(c));
			})
			.map((b) => {
				const box = b.getBoundingClientRect();
				const range = document.createRange();
				range.selectNodeContents(b);
				const text = range.getBoundingClientRect();
				return {
					label: b.textContent.trim(),
					above: text.top - box.top,
					below: box.bottom - text.bottom,
					lines: textLines(b),
				};
			});
	}
	return out;
})`

// textLinesJS defines textLines(el): how many lines el's text is laid out
// on. Each text fragment's box is one rect per line it spans; rects that
// share any vertical extent are one line, so a value and a smaller button
// label beside it - whose boxes start at different heights - are still one
// (#521: counting distinct tops called that two), and a wrap, whose next
// line starts below the last one's bottom, is two.
const textLinesJS = `const textLines = (el) => {
	const rects = [];
	const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
	for (let node = walker.nextNode(); node; node = walker.nextNode()) {
		if (node.data.trim() === "") continue;
		const range = document.createRange();
		range.selectNodeContents(node);
		for (const r of range.getClientRects()) if (r.width > 0) rects.push(r);
	}
	rects.sort((a, b) => a.top - b.top);
	let lines = 0, bottom = -Infinity;
	for (const r of rects) {
		if (r.top >= bottom - 0.5) { lines++; bottom = r.bottom; }
		else bottom = Math.max(bottom, r.bottom);
	}
	return lines;
};`

type labelBox struct {
	Label string  `json:"label"`
	Above float64 `json:"above"`
	Below float64 `json:"below"`
	Lines int     `json:"lines"`
}

// measureLabels evaluates labelCentringJS over roots (name -> selector).
func measureLabels(roots string, out *map[string][]labelBox) chromedp.Action {
	return chromedp.Evaluate(`(() => { `+textLinesJS+` return `+labelCentringJS+`(`+roots+`); })()`, out)
}

// assertLabelsCentred fails unless every measured control's label has the
// same space above it as below it, to within a pixel, on one line.
func assertLabelsCentred(t *testing.T, where string, buttons []labelBox) {
	t.Helper()
	require.NotEmpty(t, buttons, "%s: no text controls measured", where)
	for _, b := range buttons {
		assert.InDelta(t, b.Above, b.Below, 1,
			"%s: %q has %.2fpx above its label and %.2fpx below - a label is vertically centred in its control",
			where, b.Label, b.Above, b.Below)
		assert.Equal(t, 1, b.Lines, "%s: %q is laid out on %d lines - a control's label never wraps", where, b.Label, b.Lines)
	}
}

// TestE2E_ButtonLabelsAreCentred holds the top bar, the library toolbar, the
// attention cards' footers, an open picker's menu items and the
// slide-over's actions to labels centred on both axes and on one line, at
// the 1280px width the review screenshots use, in every layout face.
func TestE2E_ButtonLabelsAreCentred(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var home, menu map[string][]labelBox
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.library__toolbar`, chromedp.ByQuery),
			chromedp.WaitVisible(`.card--updates .card__actions`, chromedp.ByQuery),
			faceInEffect(face),
			measureLabels(`{
				"top bar": ".app-bar",
				"library toolbar": ".library__toolbar",
				"updates card footer": ".card--updates .card__actions",
				"health card footer": ".card--health .card__actions",
			}`, &home),
			clickWhenSettled(`[data-picker="profile"]`),
			chromedp.WaitVisible(`.profile-picker__menu .menu-item`, chromedp.ByQuery),
			measureLabels(`{"profile picker menu": ".profile-picker__menu"}`, &menu),
		)
		for _, where := range []string{"top bar", "library toolbar", "updates card footer", "health card footer"} {
			assertLabelsCentred(t, where, home[where])
		}
		assertLabelsCentred(t, "profile picker menu", menu["profile picker menu"])

		var panel map[string][]labelBox
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SlideOverPath("fake", "boots")),
			chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.slide-over__actions`, chromedp.ByQuery),
			faceInEffect(face),
			measureLabels(`{"slide-over": ".slide-over__actions"}`, &panel),
		)
		assertLabelsCentred(t, "slide-over", panel["slide-over"])
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SetupGamesTable_NothingWraps opens Setup's Games table at 1280px:
// every button label sits on one line (a fixed column share too narrow for
// "Edit sources…" broke it across two), and so does every cell but the two
// paths (which ellipsise): a short name and its id, an adapter id, a value
// and the button that edits it. In every layout face, since a wider face
// than the developer's is all it took to wrap the name, the adapter and the
// sources and loader cells.
func TestE2E_SetupGamesTable_NothingWraps(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var got map[string][]labelBox
		var cellLines []struct {
			Cell  string `json:"cell"`
			Lines int    `json:"lines"`
		}
		var overflows bool
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SetupPath("games")),
			chromedp.WaitVisible(`[data-testid="setup-games"] tbody tr`, chromedp.ByQuery),
			faceInEffect(face),
			measureLabels(`{"games table": "[data-testid=\"setup-games\"]"}`, &got),
			chromedp.Evaluate(`(() => { `+textLinesJS+` return [...document.querySelectorAll('[data-testid="setup-games"] tbody tr:not(.setup-table__editor) > td:not(.col--path)')]
				.map((td) => ({ cell: td.closest("table").querySelectorAll("thead th")[td.cellIndex].textContent.trim() || "actions", lines: textLines(td) })); })()`, &cellLines),
			chromedp.Evaluate(`document.documentElement.scrollWidth > window.innerWidth`, &overflows),
		)
		assertLabelsCentred(t, "setup games table", got["games table"])
		require.Len(t, cellLines, 6, "name, adapter, sources, loader, default and the row's actions")
		for _, c := range cellLines {
			assert.Equal(t, 1, c.Lines, "the %s cell is laid out on %d lines - a short name and its id, an id, and a value with its button each fit one", c.Cell, c.Lines)
		}
		assert.False(t, overflows, "the games table fits the page")
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_CardFootersFitOneLine holds every attention card's footer to one
// row of buttons at 1280x900 - where the Updates card's three actions fit
// before the control system made "Check again" default size - so a card is
// not taller than its row neighbours because its footer wrapped.
func TestE2E_CardFootersFitOneLine(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var rows map[string]int
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.card--updates .card__actions`, chromedp.ByQuery),
			faceInEffect(face),
			chromedp.Evaluate(`Object.fromEntries([...document.querySelectorAll(".attention-cards .card")].map((card) => [
				card.className,
				new Set([...card.querySelectorAll(".card__actions > button")].map((b) => Math.round(b.getBoundingClientRect().top))).size,
			]))`, &rows),
		)
		require.Contains(t, rows, "card card--updates")
		for card, n := range rows {
			assert.LessOrEqual(t, n, 1, "%s: its footer's buttons wrap onto %d rows at 1280px", card, n)
		}
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_OmnibarPlaceholderShowsWhole holds the omnibar's placeholder to
// the room the top bar leaves it at 1280px: it is the omnibar's only
// instruction (filter as you type, Enter searches sources), and a bar whose
// controls grew had cut it off mid-word. Both sides are the input's own
// geometry - the placeholder measured in the input's computed font against
// its content box - so it holds in whatever face the host draws it in. In a
// face too wide for one row the bar wraps rather than clipping it (#521), and
// still never reaches past the page; typing (which adds the clear and
// "search sources" controls) does not change the search's footprint, so
// the bar does not re-wrap under the cursor.
func TestE2E_OmnibarPlaceholderShowsWhole(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var got struct {
			Placeholder string  `json:"placeholder"`
			Text        float64 `json:"text"`
			Room        float64 `json:"room"`
			Search      float64 `json:"search"`
			Overflows   bool    `json:"overflows"`
		}
		var typing float64
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
			faceInEffect(face),
			chromedp.Evaluate(`(() => {
				const o = document.querySelector(".omnibar");
				const s = getComputedStyle(o);
				const ctx = document.createElement("canvas").getContext("2d");
				ctx.font = s.font;
				return {
					placeholder: o.placeholder,
					text: ctx.measureText(o.placeholder).width,
					room: o.clientWidth - parseFloat(s.paddingLeft) - parseFloat(s.paddingRight),
					search: document.querySelector(".app-bar__search").getBoundingClientRect().width,
					overflows: document.documentElement.scrollWidth > window.innerWidth,
				};
			})()`, &got),
			chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
			chromedp.WaitVisible(`.omnibar__fanout`, chromedp.ByQuery),
			chromedp.Evaluate(`document.querySelector(".app-bar__search").getBoundingClientRect().width`, &typing),
		)
		require.NotEmpty(t, got.Placeholder)
		assert.LessOrEqual(t, got.Text, got.Room,
			"the omnibar's placeholder %q needs %.0fpx and has %.0fpx at 1280px", got.Placeholder, got.Text, got.Room)
		assert.False(t, got.Overflows, "the top bar fits the page")
		assert.InDelta(t, got.Search, typing, 0.5, "the search is %.1fpx wide empty and %.1fpx while typing", got.Search, typing)
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_LibraryToolbarHeadingStaysOneLine holds the library's heading to
// one line at 1280px in every layout face - with and without the omnibar's
// fan-out, whose heading ("In your library (N)") is the longer one - and the
// toolbar to the page's width: a wide face used to break the heading across
// lines while the controls beside it kept theirs (#521).
func TestE2E_LibraryToolbarHeadingStaysOneLine(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var plain, fannedOut int
		var overflows bool
		heading := `(() => { ` + textLinesJS + ` return textLines(document.querySelector(".library__toolbar .section-header")); })()`
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.library__toolbar`, chromedp.ByQuery),
			faceInEffect(face),
			chromedp.Evaluate(heading, &plain),
			chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
			chromedp.KeyEvent(kb.Enter),
			pollUntil(`/in your library/i.test(document.querySelector(".library__toolbar .section-header")?.textContent ?? "")`),
			chromedp.Evaluate(heading, &fannedOut),
			chromedp.Evaluate(`document.documentElement.scrollWidth > window.innerWidth`, &overflows),
		)
		assert.Equal(t, 1, plain, "the library heading is laid out on %d lines", plain)
		assert.Equal(t, 1, fannedOut, "the fanned-out library heading is laid out on %d lines", fannedOut)
		assert.False(t, overflows, "the library toolbar fits the page")
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
