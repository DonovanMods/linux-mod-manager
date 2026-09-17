package serve_test

// #442: three accessibility gaps in the web UI - the library row's glyph
// badges had no accessible name, the library's live lines were mounted
// together with their first words (so those words were not reliably
// announced), and the reorder modal faded the dragged row's name below
// WCAG AA.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rowBadgeNamesJS reads each row badge's role and accessible name, keyed by
// its test id - the name a screen reader is given, which title is not.
const rowBadgeNamesJS = `(() => {
	const out = {};
	for (const el of document.querySelectorAll('.mod-row [data-testid^="row-"]')) {
		const row = el.closest(".mod-row").querySelector(".mod-row__name").textContent.trim();
		out[row + "/" + el.dataset.testid] = (el.getAttribute("role") || "") + "|" + (el.getAttribute("aria-label") || "");
	}
	return out;
})()`

// TestE2E_LibraryRowBadges_HaveAccessibleNames covers the update, lock and
// conflict glyphs.
func TestE2E_LibraryRowBadges_HaveAccessibleNames(t *testing.T) {
	t.Run("update and lock", func(t *testing.T) {
		f := newE2EFixtureWithALockedAndAnUnlockedUpdate(t)
		var got map[string]string
		f.runInBrowser(t,
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
			pollUntil(`document.querySelector('[data-testid="row-locked"]') !== null && document.querySelectorAll('[data-testid="row-update"]').length === 2`),
			chromedp.Evaluate(rowBadgeNamesJS, &got),
		)
		assert.Equal(t, "img|Locked to 1.0", got["Better Boots/row-locked"])
		assert.Equal(t, "img|Update available: 2.0", got["Better Boots/row-update"])
		assert.Equal(t, "img|Update available: 2.0", got["Great Gloves/row-update"])
		assert.Empty(t, f.BrowserErrors())
	})

	t.Run("conflict", func(t *testing.T) {
		f := newE2EFixtureWithAttention(t)
		var got map[string]string
		f.runInBrowser(t,
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
			pollUntil(`document.querySelectorAll('[data-testid="row-conflict"]').length === 2`),
			chromedp.Evaluate(rowBadgeNamesJS, &got),
		)
		assert.Equal(t, "img|File conflict", got["Mod X/row-conflict"])
		assert.Equal(t, "img|File conflict", got["Mod Y/row-conflict"])
		assert.Empty(t, f.BrowserErrors())
	})
}

// TestE2E_LibraryLiveLine_IsPresentBeforeItSpeaks marks the library's live
// region before a job starts and checks the SAME element - still a status
// region - then carries the job's words: a region created alongside its
// text would have lost the mark.
func TestE2E_LibraryLiveLine_IsPresentBeforeItSpeaks(t *testing.T) {
	f := newE2EFixtureWithSlowDeploy(t)

	var before struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="library-live"]') !== null`),
		chromedp.Evaluate(`(() => {
			const el = document.querySelector('[data-testid="library-live"]');
			el.dataset.e2eMark = "before";
			return { role: el.getAttribute("role"), text: el.textContent };
		})()`, &before),
	)
	require.Equal(t, "status", before.Role, "the region exists, as a status region, before there is anything to say")
	assert.Empty(t, before.Text)

	var live, mark string
	f.runInBrowser(t,
		clickWhenSettled(`[data-action="deploy"]`),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="library-live"]').textContent.includes("Deploying")`),
		textContent(`[data-testid="library-live"]`, &live),
		chromedp.Evaluate(`document.querySelector('[data-testid="library-live"]').dataset.e2eMark ?? ""`, &mark),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)
	assert.Contains(t, live, "Deploying")
	assert.Equal(t, "before", mark, "the job's words land in the region that was already there")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ReorderDragRow_KeepsItsNameAtAA measures the dragged row's name
// mid-drag, composited the way the browser paints it.
func TestE2E_ReorderDragRow_KeepsItsNameAtAA(t *testing.T) {
	f := newE2EFixtureWithReorderableConflict(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--conflicts`, chromedp.ByQuery),
		clickWhenSettled(`.card--conflicts [data-action="resolve"]`),
		chromedp.WaitVisible(`[data-testid="reorder-list"]`, chromedp.ByQuery),
		// The modal fades in (motion.js); measure once its entrance is over,
		// so the only opacity left in the chain is the row's own.
		pollUntil(`(() => {
			let o = 1;
			for (let e = document.querySelector('[data-testid="reorder-list"]'); e; e = e.parentElement) o *= Number(getComputedStyle(e).opacity);
			return o === 1;
		})()`),
	)

	var got struct {
		Dragging bool    `json:"dragging"`
		Opacity  float64 `json:"opacity"`
		Ratio    float64 `json:"ratio"`
	}
	f.runInBrowser(t, dragRowToChecking("Mod X", "Mod Y", chromedp.Evaluate(`(() => {
		const row = document.querySelector(".reorder-row--dragging");
		if (!row) return { dragging: false, opacity: 0, ratio: 0 };
		const parse = (c) => (/rgba?\(([^)]+)\)/.exec(c)[1].split(/[\s,\/]+/).filter(Boolean).map(Number));
		let o = 1;
		for (let e = row; e; e = e.parentElement) o *= Number(getComputedStyle(e).opacity);
		const bgOf = (el) => {
			for (let e = el; e; e = e.parentElement) {
				const c = parse(getComputedStyle(e).backgroundColor);
				if (c.length < 4 || c[3] > 0) return c;
			}
			return [255, 255, 255];
		};
		const lum = (c) => {
			const ch = c.slice(0, 3).map((v) => { v /= 255; return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; });
			return 0.2126 * ch[0] + 0.7152 * ch[1] + 0.0722 * ch[2];
		};
		const name = [...row.querySelectorAll("*")].find((el) =>
			[...el.childNodes].some((n) => n.nodeType === 3 && n.textContent.includes("Mod X"))) ?? row;
		const fg = parse(getComputedStyle(name).color);
		const bg = bgOf(name);
		const mixed = fg.slice(0, 3).map((v, i) => v * o + bg[i] * (1 - o));
		const [a, b] = [lum(mixed), lum(bg)].sort((x, y) => y - x);
		return { dragging: true, opacity: o, ratio: (a + 0.05) / (b + 0.05) };
	})()`, &got)))

	require.True(t, got.Dragging, "the dragged row must be marked while the drag is in progress")
	assert.InDelta(t, 1.0, got.Opacity, 0.001, "the dragged row is marked, not faded")
	assert.GreaterOrEqual(t, got.Ratio, 4.5, "its name stays at WCAG AA")
	assert.Empty(t, f.BrowserErrors())
}
