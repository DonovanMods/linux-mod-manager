package serve_test

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
)

// omnibarResultNames is the display names the omnibar's fan-out shows.
const omnibarResultNames = `Array.from(document.querySelectorAll(".omnibar-results .search-result__name")).map(e => e.textContent)`

// TestE2E_OmnibarResubmitReplacesTheFirstSearchsResults is #500 A: edit the
// omnibar's text after a fan-out landed and submit again - the rows shown
// must answer the NEW term, never the first one's.
func TestE2E_OmnibarResubmitReplacesTheFirstSearchsResults(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	var first, second []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(`.omnibar-results .search-result`, chromedp.ByQuery),
		chromedp.Evaluate(omnibarResultNames, &first),
		// Edit the term in place, then submit.
		chromedp.Focus(`.omnibar`, chromedp.ByQuery),
		chromedp.KeyEvent(strings.Repeat(kb.Backspace, len("boots"))),
		chromedp.SendKeys(`.omnibar`, "clash", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		pollUntil(`Array.from(document.querySelectorAll(".omnibar-results .search-result__name")).some(e => e.textContent === "Clashing Mod")`),
		chromedp.Evaluate(omnibarResultNames, &second),
	)
	assert.Equal(t, []string{"Better Boots"}, first)
	assert.Equal(t, []string{"Clashing Mod"}, second)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_OmnibarFanOutLinksToThePagedSearchPage is #500 B: the fan-out is
// capped at 20 rows and never pages, and the dedicated search page - which
// does - was reachable only by typing its URL. The fan-out must offer the
// way there, for the same query, and Next on that page must work.
func TestE2E_OmnibarFanOutLinksToThePagedSearchPage(t *testing.T) {
	f := newE2EFixtureWithManySearchResults(t)

	var linkText, url, header, firstName string
	var page1Count int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "item", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(`.omnibar-results .search-result`, chromedp.ByQuery),
		textContent(`.omnibar-results [data-testid="see-all-results"]`, &linkText),
		chromedp.Click(`.omnibar-results [data-testid="see-all-results"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Location(&url),
		textContent(`.search-page .section-header`, &header),
		chromedp.Evaluate(`document.querySelectorAll(".search-result").length`, &page1Count),
		chromedp.Click(`.search-page__pager button:last-child`, chromedp.ByQuery),
		pollUntil(`document.querySelector(".search-result__name")?.textContent === "Item 21"`),
		textContent(`.search-result__name`, &firstName),
	)
	assert.Contains(t, linkText, "See all results")
	assert.True(t, strings.HasSuffix(url, f.ContextPath()+"/search?q=item"), "lands on the search route for the same query, got %s", url)
	assert.Contains(t, header, "“item”")
	assert.Equal(t, e2eManyResultsPageSize, page1Count)
	assert.Equal(t, "Item 21", firstName, "Next lands on the second page")
	assert.Empty(t, f.BrowserErrors())
}
