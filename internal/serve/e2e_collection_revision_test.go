package serve_test

// Issue 673 in the browser: a collection profile's collection is checked as
// the update unit. A newer revision brings the Updates card on screen with
// "revision N available" - even when no mod has an update - and Update
// collection… opens the sync plan; an up-to-date collection says so on the
// quiet line.

import (
	"context"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eRevisionSource is the fake source that also answers a collection's
// latest revision.
type e2eRevisionSource struct {
	*fakeSource
	latest int
}

func (s *e2eRevisionSource) LatestCollectionRevision(context.Context, string, string) (int, error) {
	return s.latest, nil
}

// newE2EFixtureWithACollectionRevision is newE2EFixtureWithACollectionLock
// (installed revision 1) over a source whose latest revision is latest,
// with Beta Mod's own update skipped, so the collection is the only update
// there could be.
func newE2EFixtureWithACollectionRevision(t *testing.T, latest int) e2eFixture {
	t.Helper()
	f := newE2EFixtureWithACollectionLock(t)
	got, err := f.Svc.GetSource("fake")
	require.NoError(t, err)
	f.Svc.RegisterSource(&e2eRevisionSource{fakeSource: got.(*fakeSource), latest: latest})
	f.Game.SourceIDs["fake"] = "fixturegame"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	_, err = f.Svc.SkipModUpdate(t.Context(), f.Game, "fake", "b", f.Profile, "3.0")
	require.NoError(t, err)
	return f
}

func TestE2E_UpdatesCard_ANewCollectionRevisionOpensTheSyncPlan(t *testing.T) {
	f := newE2EFixtureWithACollectionRevision(t, 2)

	var note string
	var rows int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates [data-testid="updates-collection-locked"][data-state="available"]`, chromedp.ByQuery),
		textContent(`.card--updates [data-testid="updates-collection-locked"]`, &note),
		chromedp.Evaluate(`document.querySelectorAll(".card--updates .card__row").length`, &rows),
	)
	assert.Contains(t, note, "Collection Night City: revision 2 available (installed 1).")
	assert.Contains(t, note, "1 mod locked by collection Night City (not checked).")
	assert.Zero(t, rows, "the card is there for the collection alone: no mod row")

	var title string
	f.runInBrowser(t,
		chromedp.Click(`.card--updates [data-action="update-collection"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="collection_update"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="collection_update"] .modal__title`, &title),
	)
	assert.Equal(t, "Update default's collection", strings.TrimSpace(title))
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_UpdatesCard_AnUpToDateCollectionIsAQuietLine(t *testing.T) {
	f := newE2EFixtureWithACollectionRevision(t, 1)

	var note string
	var cardShown bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`[data-testid="updates-collection-locked"][data-state="current"]`, chromedp.ByQuery),
		textContent(`[data-testid="updates-collection-locked"]`, &note),
		chromedp.Evaluate(`!!document.querySelector(".card--updates")`, &cardShown),
	)
	assert.Contains(t, note, "Collection Night City: up to date (revision 1).")
	assert.False(t, cardShown, "nothing needs attention: no Updates card")
	assert.Empty(t, f.BrowserErrors())
}
