package serve_test

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
)

// e2eSearchUpdatedAt is the search fixture's one date (#433): old enough
// that relativetime.js renders it as a date rather than an age, so the
// assertion does not depend on when the suite runs.
var e2eSearchUpdatedAt = time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)

// TestE2E_SearchAndInstallPickerShowTheLastUpdateDate is #433's web half: a
// search hit whose source dates it reads "Updated on <date>" with the exact
// moment in its title, the undated hit beside it reads nothing at all
// (never the year 1), and the install picker's version entries carry the
// same fact for the dated version only.
func TestE2E_SearchAndInstallPickerShowTheLastUpdateDate(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	dated := searchResultRow("fake", e2eSearchInstallModID)
	undated := searchResultRow("fake", e2eSearchConflictModID)
	var datedText, datedTitle, undatedText string
	var undatedHasTitle bool
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("o")),
		chromedp.WaitVisible(dated, chromedp.ByQuery),
		chromedp.WaitVisible(undated, chromedp.ByQuery),
		textContent(dated+` [data-testid="search-result-updated"]`, &datedText),
		chromedp.AttributeValue(dated+` [data-testid="search-result-updated"]`, "title", &datedTitle, nil, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('`+undated+` [data-testid="search-result-updated"]').textContent`, &undatedText),
		chromedp.Evaluate(`document.querySelector('`+undated+` [data-testid="search-result-updated"]').hasAttribute("title")`, &undatedHasTitle),
	)
	assert.Regexp(t, `^Updated on .*2024`, datedText)
	assert.Contains(t, datedTitle, "2024")
	assert.Empty(t, undatedText, "an undated hit renders no date")
	assert.False(t, undatedHasTitle)

	var options []string
	f.runInBrowser(t,
		chromedp.Click(dated+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`select[name="install-version"]`, chromedp.ByQuery),
		chromedp.Evaluate(
			`Array.from(document.querySelectorAll('select[name="install-version"] option')).map(o => o.textContent.trim())`,
			&options,
		),
	)
	if assert.Len(t, options, 2) {
		assert.Regexp(t, `^2\.0 · Updated on .*2024`, options[0], "the dated version carries its date")
		assert.Equal(t, "1.0", options[1], "the undated version reads exactly as before")
	}
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchResultShowsTheAuthorsNameWithTheIDOnHover is #420's web
// half: a hit whose author id resolved to a name shows the name, and the
// id is in the element's title.
func TestE2E_SearchResultShowsTheAuthorsNameWithTheIDOnHover(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	row := searchResultRow("fake", e2eSearchMultiFileModID)
	var text, title string
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("edition")),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		textContent(row+" .search-result__author", &text),
		chromedp.AttributeValue(row+" .search-result__author", "title", &title, nil, chromedp.ByQuery),
	)
	assert.Equal(t, "Cargo Captain", text)
	assert.Equal(t, "76561198000000000", title)
	assert.Empty(t, f.BrowserErrors())
}
