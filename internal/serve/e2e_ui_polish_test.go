package serve_test

// #520: the search result rows share one set of columns, a source's search
// failure is a labelled notice, and four small layout leftovers from #518's
// review (the pager's arrows, the archive picker's width, the Setup tabs'
// widths and the attention cards' title spacing). Each is a claim about
// where a BROWSER puts things, so each is measured in one.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
)

// resultColumn is one result row's column edges, by the row's mod name.
type resultColumn struct {
	Name    string   `json:"name"`
	Version *float64 `json:"version"`
	Actions *float64 `json:"actions"`
}

// resultColumnsJS measures, under root, every hit row's version cell and
// actions cell left edges (null when the row has no such cell).
const resultColumnsJS = `((root) => [...document.querySelectorAll(root + " .search-result:not(.search-result--warning)")].map((row) => {
	const left = (sel) => {
		const el = row.querySelector(sel);
		return el ? el.getBoundingClientRect().left : null;
	};
	return {
		name: row.querySelector(".search-result__name").textContent.trim(),
		version: left(".search-result__version"),
		actions: left(".search-result__actions"),
	};
}))`

// assertColumnsAlign fails unless the rows under where include one with an
// author ("Multi Edition Mod"), one with a date and no author ("Better
// Boots") and one with neither ("Clashing Mod"), and every row's version and
// actions cells start at the same x as every other row's, to within a pixel.
func assertColumnsAlign(t *testing.T, where string, rows []resultColumn) {
	t.Helper()
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	require.Subset(t, names, []string{"Better Boots", "Clashing Mod", "Multi Edition Mod"}, "%s: the mixed rows the alignment is about", where)
	for _, r := range rows {
		require.NotNil(t, r.Version, "%s: %q has no version cell", where, r.Name)
		require.NotNil(t, r.Actions, "%s: %q has no actions cell", where, r.Name)
	}
	first := rows[0]
	for _, r := range rows[1:] {
		assert.InDelta(t, *first.Version, *r.Version, 1,
			"%s: %q's version starts at %.1fpx and %q's at %.1fpx - the version column is one column",
			where, first.Name, *first.Version, r.Name, *r.Version)
		assert.InDelta(t, *first.Actions, *r.Actions, 1,
			"%s: %q's actions start at %.1fpx and %q's at %.1fpx - the actions column is one column",
			where, first.Name, *first.Actions, r.Name, *r.Actions)
	}
}

// warningRegionName finds the element that shows text (a source's search
// failure), and returns the accessible name of the nearest region the
// browser's accessibility tree puts it in ("" when it is in none).
func warningRegionName(text string, out *string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var marked bool
		if err := chromedp.Evaluate(fmt.Sprintf(`(() => {
			const all = [...document.querySelectorAll("main *")].filter((el) => el.textContent.includes(%q));
			const leaf = all.find((el) => ![...el.children].some((c) => c.textContent.includes(%q)));
			if (!leaf) return false;
			leaf.setAttribute("data-e2e-warning", "");
			return true;
		})()`, text, text), &marked).Do(ctx); err != nil {
			return err
		}
		if !marked {
			return fmt.Errorf("no element shows %q", text)
		}
		var nodes []*cdp.Node
		if err := chromedp.Nodes(`[data-e2e-warning]`, &nodes, chromedp.ByQuery).Do(ctx); err != nil {
			return err
		}
		tree, err := accessibility.GetPartialAXTree().WithBackendNodeID(nodes[0].BackendNodeID).WithFetchRelatives(true).Do(ctx)
		if err != nil {
			return err
		}
		// The partial tree is the node, its ancestors, siblings and
		// children: walk up from the node by parent id.
		byID := map[accessibility.NodeID]*accessibility.Node{}
		var self *accessibility.Node
		for _, n := range tree {
			byID[n.NodeID] = n
			if n.BackendDOMNodeID == nodes[0].BackendNodeID {
				self = n
			}
		}
		*out = ""
		for n := self; n != nil; n = byID[n.ParentID] {
			if axString(n.Role) == "region" {
				*out = axString(n.Name)
				return nil
			}
		}
		return nil
	})
}

// axString is an accessibility value's string, or "".
func axString(v *accessibility.Value) string {
	if v == nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(v.Value, &s)
	return s
}

// TestE2E_SearchResultsShareColumns holds the dedicated search page and the
// omnibar's inline fan-out to one column layout: a row with an author, a
// row with only a date and a row with neither start their version and their
// actions at the same x. Each failed source is a notice region whose name
// says which source, and the page's list takes the content width.
func TestE2E_SearchResultsShareColumns(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var page []resultColumn
		var pageRegion string
		var listWidth, mainWidth float64
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SearchPagePath("o")),
			chromedp.WaitVisible(searchResultRow("fake", e2eSearchInstallModID), chromedp.ByQuery),
			chromedp.WaitVisible(`.search-result--warning`, chromedp.ByQuery),
			faceInEffect(face),
			chromedp.Evaluate(resultColumnsJS+`(".search-page")`, &page),
			warningRegionName("upstream unavailable", &pageRegion),
			chromedp.Evaluate(`document.querySelector(".search-page .search-results").getBoundingClientRect().width`, &listWidth),
			chromedp.Evaluate(`(() => { const m = document.querySelector(".search-page"); const s = getComputedStyle(m);
				return m.clientWidth - parseFloat(s.paddingLeft) - parseFloat(s.paddingRight); })()`, &mainWidth),
		)
		assertColumnsAlign(t, "search page", page)
		assert.Contains(t, pageRegion, "flaky", "search page: the failure is a region named for its source")
		assert.InDelta(t, mainWidth, listWidth, 1, "search page: the result list takes the page's content width, as the library does")

		var omnibar []resultColumn
		var omnibarRegion string
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
			chromedp.SendKeys(`.omnibar`, "o", chromedp.ByQuery),
			chromedp.KeyEvent(kb.Enter),
			chromedp.WaitVisible(`.omnibar-results .search-result--warning`, chromedp.ByQuery),
			faceInEffect(face),
			chromedp.Evaluate(resultColumnsJS+`(".omnibar-results")`, &omnibar),
			warningRegionName("upstream unavailable", &omnibarRegion),
		)
		assertColumnsAlign(t, "omnibar fan-out", omnibar)
		assert.Contains(t, omnibarRegion, "flaky", "omnibar fan-out: the failure is a region named for its source")
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SearchPagerArrowsAreCentred holds the pager's ← and → to the
// middle of their buttons: as text they came from a fallback font whose
// glyphs sit below the label's own centre line. An icon drawn in the label's
// own box centres with it.
func TestE2E_SearchPagerArrowsAreCentred(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var arrows []struct {
			Label  string   `json:"label"`
			Arrow  *float64 `json:"arrow"`
			Button float64  `json:"button"`
		}
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SearchPagePath("o")),
			chromedp.WaitVisible(`.search-page__pager`, chromedp.ByQuery),
			faceInEffect(face),
			chromedp.Evaluate(`[...document.querySelectorAll(".search-page__pager .button, .app-bar__nav a")].map((el) => {
				const mid = (r) => (r.top + r.bottom) / 2;
				const icon = el.querySelector("svg");
				const box = el.getBoundingClientRect();
				return { label: el.textContent.trim(), arrow: icon ? mid(icon.getBoundingClientRect()) : null, button: mid(box) };
			})`, &arrows),
		)
		require.Len(t, arrows, 3, "Prev, Next and Back to library")
		for _, a := range arrows {
			require.NotNil(t, a.Arrow, "%q draws its arrow as an icon, not a fallback-font glyph", a.Label)
			assert.InDelta(t, a.Button, *a.Arrow, 1, "%q: its arrow's centre is %.1fpx from the control's", a.Label, *a.Arrow-a.Button)
			assert.NotContains(t, a.Label, "←", "%q: no text arrow beside the icon", a.Label)
			assert.NotContains(t, a.Label, "→", "%q: no text arrow beside the icon", a.Label)
		}
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SetupTabsKeepTheirWidths switches through every Setup tab and
// holds each tab to the width it had before: the active tab's bold label
// is wider than its regular one, which shifted every tab after it.
func TestE2E_SetupTabsKeepTheirWidths(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		tabWidths := `[...document.querySelectorAll(".setup-nav__tab")].map((b) => b.getBoundingClientRect().width)`
		var initial []float64
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SetupPath("games")),
			chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
			faceInEffect(face),
			chromedp.Evaluate(tabWidths, &initial),
		)
		require.Len(t, initial, 5)
		for _, section := range []string{"auth", "sources", "archive", "adopt", "games"} {
			var now []float64
			f.runInBrowser(t,
				clickWhenSettled(`.setup-nav__tab[data-section="`+section+`"]`),
				pollUntil(`document.querySelector('.setup-nav__tab[data-section="`+section+`"]').getAttribute("aria-selected") === "true"`),
				chromedp.Evaluate(tabWidths, &now),
			)
			require.Len(t, now, len(initial))
			for i := range initial {
				assert.InDelta(t, initial[i], now[i], 0.5, "with %s active, tab %d is %.2fpx wide (was %.2fpx)", section, i, now[i], initial[i])
			}
		}
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_ArchivePickerSizesToItsLabel holds Setup's "Choose archive…" to
// its own label's width: a column flex container stretched it across the
// whole panel.
func TestE2E_ArchivePickerSizesToItsLabel(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var picker, panel float64
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SetupPath("archive")),
			chromedp.WaitVisible(`[data-testid="setup-import-archive"] .button`, chromedp.ByQuery),
			faceInEffect(face),
			chromedp.Evaluate(`document.querySelector('[data-testid="setup-import-archive"] .button').getBoundingClientRect().width`, &picker),
			chromedp.Evaluate(`document.querySelector('[data-testid="setup-import-archive"]').getBoundingClientRect().width`, &panel),
		)
		require.Positive(t, panel)
		assert.Less(t, picker, panel/2, "Choose archive… is %.0fpx wide in a %.0fpx panel", picker, panel)
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_AttentionCardTitlesSitAtTheirCardsTop holds each attention card's
// title to the card's own padding: a 1em margin above it left a gap twice
// the padding before the first line of the card.
func TestE2E_AttentionCardTitlesSitAtTheirCardsTop(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var gaps map[string]float64
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.card--updates .card__title`, chromedp.ByQuery),
			faceInEffect(face),
			chromedp.Evaluate(`Object.fromEntries([...document.querySelectorAll(".card")].map((card) => {
				const title = card.querySelector(".card__title");
				const inner = card.getBoundingClientRect().top + card.clientTop + parseFloat(getComputedStyle(card).paddingTop);
				return [card.className, title.getBoundingClientRect().top - inner];
			}))`, &gaps),
		)
		require.Contains(t, gaps, "card card--updates")
		for card, gap := range gaps {
			assert.InDelta(t, 0, gap, 1, "%s: its title starts %.1fpx below the card's padding", card, gap)
		}
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_SetupSourcesTables_NothingWraps seeds Setup -> Sources with a
// custom source (Download, Edit, Delete) and two local search indexes (one
// with Refresh index), and holds both tables to #518's rules at 1280px:
// every control's label centred on one line, every row's actions on one
// row, and nothing wider than the page.
func TestE2E_SetupSourcesTables_NothingWraps(t *testing.T) {
	f, _ := newE2EIndexFixture(t, nil)
	yaml := "id: my-mods\nname: My Mods\ntype: directory\ndirectory:\n  path: " + t.TempDir() + "\n"
	_, err := app.SaveSourceDefinition(f.Ctx, f.Svc, "", []byte(yaml))
	require.NoError(t, err)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var got map[string][]labelBox
		var actionRows map[string]int
		var overflows bool
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SetupPath("sources")),
			chromedp.WaitVisible(`tr[data-source="my-mods"] [data-action="edit-source"]`, chromedp.ByQuery),
			chromedp.WaitVisible(indexRow("lethal-company")+` [data-action="refresh-index"]`, chromedp.ByQuery),
			faceInEffect(face),
			measureLabels(`{
				"sources table": "[data-testid=\"setup-sources\"] table",
				"indexes table": "[data-testid=\"source-indexes\"] table",
			}`, &got),
			chromedp.Evaluate(`Object.fromEntries([...document.querySelectorAll(".setup-table__actions")]
				.filter((cell) => cell.querySelector(".button"))
				.map((cell, i) => [cell.closest("tr").dataset.index ?? cell.closest("tr").dataset.source ?? String(i),
					new Set([...cell.querySelectorAll(".button")].map((b) => Math.round(b.getBoundingClientRect().top))).size]))`, &actionRows),
			chromedp.Evaluate(`document.documentElement.scrollWidth > window.innerWidth`, &overflows),
		)
		assertLabelsCentred(t, "sources table", got["sources table"])
		assertLabelsCentred(t, "indexes table", got["indexes table"])
		labels := []string{}
		for _, b := range got["sources table"] {
			labels = append(labels, b.Label)
		}
		assert.Subset(t, labels, []string{"Download", "Edit", "Delete"}, "the custom source's own actions are measured")
		require.Contains(t, actionRows, "my-mods")
		require.Contains(t, actionRows, "lethal-company")
		for row, n := range actionRows {
			assert.Equal(t, 1, n, "%s: its actions wrap onto %d rows", row, n)
		}
		assert.False(t, overflows, "the sources tables fit the page")
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
