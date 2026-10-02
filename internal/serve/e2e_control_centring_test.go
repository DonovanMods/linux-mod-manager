package serve_test

// #518: a button's label sits in the middle of the button, on one line, and
// a row of default-size controls still fits where it did before the control
// system made them all default size. A min-height alone leaves the label on
// a line box taller than its text, with the spare height below it; only a
// browser can say where the text actually lands.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// labelCentringJS measures every visible text-labelled control under each
// named root - a .button, a <button> or a link drawn as one (bar the text-shaped link and the card-shaped
// tile) or a .menu-item: the control's border box against the box of its
// own text (a Range over its contents), and how many lines that text takes.
const labelCentringJS = `((roots) => {
	const out = {};
	for (const [name, sel] of Object.entries(roots)) {
		const root = document.querySelector(sel);
		if (!root) { out[name] = null; continue; }
		out[name] = [...root.querySelectorAll("button.button, a.button, button.menu-item")]
			.filter((b) => {
				const r = b.getBoundingClientRect();
				return r.width > 0 && r.height > 0 && b.textContent.trim() !== "" &&
					!b.classList.contains("button--link") && !b.classList.contains("button--tile");
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

// textLinesJS defines textLines(el): how many distinct lines el's text is
// laid out on (a word on one line has one client rect per line it spans).
const textLinesJS = `const textLines = (el) => {
	const tops = new Set();
	const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
	for (let node = walker.nextNode(); node; node = walker.nextNode()) {
		if (node.data.trim() === "") continue;
		const range = document.createRange();
		range.selectNodeContents(node);
		for (const r of range.getClientRects()) if (r.width > 0) tops.add(Math.round(r.top));
	}
	return tops.size;
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
// the 1280px width the review screenshots use.
func TestE2E_ButtonLabelsAreCentred(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var home, menu map[string][]labelBox
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.library__toolbar`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--updates .card__actions`, chromedp.ByQuery),
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
		chromedp.WaitVisible(`.slide-over__actions`, chromedp.ByQuery),
		measureLabels(`{"slide-over": ".slide-over__actions"}`, &panel),
	)
	assertLabelsCentred(t, "slide-over", panel["slide-over"])

	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SetupGamesTable_NothingWraps opens Setup's Games table at 1280px:
// every button label sits on one line (a fixed column share too narrow for
// "Edit sources…" broke it across two), and so does each game's name cell.
func TestE2E_SetupGamesTable_NothingWraps(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var got map[string][]labelBox
	var nameLines []int
	var overflows bool
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		chromedp.WaitVisible(`[data-testid="setup-games"] tbody tr`, chromedp.ByQuery),
		measureLabels(`{"games table": "[data-testid=\"setup-games\"]"}`, &got),
		chromedp.Evaluate(`(() => { `+textLinesJS+` return [...document.querySelectorAll('[data-testid="setup-games"] .setup-table__name')].map(textLines); })()`, &nameLines),
		chromedp.Evaluate(`document.documentElement.scrollWidth > window.innerWidth`, &overflows),
	)
	assertLabelsCentred(t, "setup games table", got["games table"])
	require.NotEmpty(t, nameLines)
	for i, n := range nameLines {
		assert.Equal(t, 1, n, "game %d's name cell is laid out on %d lines - a short name and its id fit one", i, n)
	}
	assert.False(t, overflows, "the games table fits the page")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_CardFootersFitOneLine holds every attention card's footer to one
// row of buttons at 1280x900 - where the Updates card's three actions fit
// before the control system made "Check again" default size - so a card is
// not taller than its row neighbours because its footer wrapped.
func TestE2E_CardFootersFitOneLine(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var rows map[string]int
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates .card__actions`, chromedp.ByQuery),
		chromedp.Evaluate(`Object.fromEntries([...document.querySelectorAll(".attention-cards .card")].map((card) => [
			card.className,
			new Set([...card.querySelectorAll(".card__actions > button")].map((b) => Math.round(b.getBoundingClientRect().top))).size,
		]))`, &rows),
	)
	require.Contains(t, rows, "card card--updates")
	for card, n := range rows {
		assert.LessOrEqual(t, n, 1, "%s: its footer's buttons wrap onto %d rows at 1280px", card, n)
	}
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_OmnibarPlaceholderShowsWhole holds the omnibar's placeholder to
// the room the top bar leaves it at 1280px: it is the omnibar's only
// instruction (filter as you type, Enter searches sources), and a bar whose
// controls grew had cut it off mid-word.
func TestE2E_OmnibarPlaceholderShowsWhole(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var got struct {
		Placeholder string  `json:"placeholder"`
		Text        float64 `json:"text"`
		Room        float64 `json:"room"`
	}
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const o = document.querySelector(".omnibar");
			const s = getComputedStyle(o);
			const ctx = document.createElement("canvas").getContext("2d");
			ctx.font = s.font;
			return {
				placeholder: o.placeholder,
				text: ctx.measureText(o.placeholder).width,
				room: o.clientWidth - parseFloat(s.paddingLeft) - parseFloat(s.paddingRight),
			};
		})()`, &got),
	)
	require.NotEmpty(t, got.Placeholder)
	assert.LessOrEqual(t, got.Text, got.Room,
		"the omnibar's placeholder %q needs %.0fpx and has %.0fpx at 1280px", got.Placeholder, got.Text, got.Room)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
