package serve_test

// Issue 542 in the browser: "Skip" on the Updates card's only row takes the
// card away, leaves a quiet "1 skipped update" line with an Unskip, and the
// full mod page says the version was skipped while still offering the
// explicit update.

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_SkippingTheOnlyUpdateHidesTheUpdatesCard(t *testing.T) {
	f := newE2EFixtureWithThreeVersionsAndACheckedUpdate(t)

	var line string
	var cardGone bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates .card__row`, chromedp.ByQuery),
		chromedp.Click(`.card--updates [data-action="skip-update"]`, chromedp.ByQuery),
		waitGone(`.card--updates`),
		chromedp.WaitVisible(`[data-testid="skipped-updates"]`, chromedp.ByQuery),
		textContent(`[data-testid="skipped-updates"] summary`, &line),
		chromedp.Evaluate(`!document.querySelector(".card--updates")`, &cardGone),
	)
	assert.True(t, cardGone, "a card of only skipped updates is not shown")
	assert.Equal(t, "1 skipped update", strings.TrimSpace(line))

	row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, f.Profile)
	require.NoError(t, err)
	assert.Equal(t, "3.0", row.SkippedVersion, "the skip names the version the row offered")

	// Unskip brings it back.
	f.runInBrowser(t,
		chromedp.Click(`[data-testid="skipped-updates"] summary`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="skipped-updates"] [data-action="unskip-update"]`, chromedp.ByQuery),
		chromedp.Click(`[data-testid="skipped-updates"] [data-action="unskip-update"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--updates .card__row`, chromedp.ByQuery),
		waitGone(`[data-testid="skipped-updates"]`),
	)
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ModPageSaysTheUpdateIsSkippedAndStillOffersIt(t *testing.T) {
	f := newE2EFixtureWithThreeVersionsAndACheckedUpdate(t)
	_, err := f.Svc.SkipModUpdate(t.Context(), f.Game, "fake", "a", f.Profile, "")
	require.NoError(t, err)

	var notice, button, table string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`[data-testid="mod-skipped-update"]`, chromedp.ByQuery),
		textContent(`[data-testid="mod-skipped-update"]`, &notice),
		pollUntil(`document.querySelectorAll(".mod-page__table tbody button").length === 1`),
		textContent(`.mod-page__table tbody button`, &button),
		textContent(`.mod-page__table`, &table),
	)
	assert.Contains(t, notice, "The update to 3.0 is skipped")
	assert.Equal(t, "Update to 3.0", strings.TrimSpace(button), "the explicit update still applies a skipped version")
	assert.Contains(t, table, "available (skipped)")

	f.runInBrowser(t,
		chromedp.Click(`[data-testid="mod-skipped-update"] [data-action="unskip-update"]`, chromedp.ByQuery),
		waitGone(`[data-testid="mod-skipped-update"]`),
	)
	row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, f.Profile)
	require.NoError(t, err)
	assert.Empty(t, row.SkippedVersion)
	assert.Empty(t, f.BrowserErrors())
}
