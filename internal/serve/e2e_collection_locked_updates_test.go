package serve_test

// Issue 667 in the browser: a mod the profile's collection locks is not a
// row on the Updates card - one line counts it, with "Update collection…" -
// "Skip all" empties the card, "Unskip all" brings its rows back, and the
// slide-over's Locked box says, before the click, that unlocking takes the
// mod out of the collection's hands.

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newE2EFixtureWithACollectionLock is two mods at 1.0, each offered 3.0:
// Alpha Mod locked by the profile's collection "Night City" (which records
// an origin), Beta Mod not locked at all.
func newE2EFixtureWithACollectionLock(t *testing.T) e2eFixture {
	t.Helper()
	src := newFakeSource("fake")
	for _, m := range []domain.Mod{
		{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "3.0"},
		{ID: "b", SourceID: "fake", Name: "Beta Mod", Version: "3.0"},
	} {
		src.addMod(fakeSourceMod{Mod: m, Files: []domain.DownloadableFile{{ID: m.ID + "3", Version: "3.0"}}})
	}
	f := newE2EFixtureFromSource(t, src)
	for _, m := range []domain.Mod{
		{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", GameID: f.Game.ID},
		{ID: "b", SourceID: "fake", Name: "Beta Mod", Version: "1.0", GameID: f.Game.ID},
	} {
		seedInstalledMod(t, f.Svc, f.Game, m, true, map[string][]byte{m.ID + ".esp": []byte(m.ID)})
	}
	prof, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "default")
	require.NoError(t, err)
	prof.Mods = []domain.ModReference{
		{SourceID: "fake", ModID: "a", Version: "1.0", Locked: true, FromCollection: true},
		{SourceID: "fake", ModID: "b", Version: "1.0"},
	}
	prof.Collection = &domain.CollectionInfo{ID: "night-city", Version: 1, Title: "Night City",
		Origin: &domain.CollectionOrigin{SourceID: "fake", Slug: "nightcity", Revision: 1}}
	require.NoError(t, config.SaveProfile(f.Svc.ConfigDir(), prof))
	return f
}

func TestE2E_UpdatesCard_CollectionLockIsOneLineAndSkipAllEmptiesIt(t *testing.T) {
	f := newE2EFixtureWithACollectionLock(t)

	var note, rows string
	var rowCount int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates .card__row`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--updates [data-testid="updates-collection-locked"]`, chromedp.ByQuery),
		textContent(`.card--updates [data-testid="updates-collection-locked"]`, &note),
		textContent(`.card--updates .card__list`, &rows),
		chromedp.Evaluate(`document.querySelectorAll(".card--updates .card__row").length`, &rowCount),
	)
	assert.Contains(t, note, "1 mod locked by collection Night City (not checked).")
	assert.Contains(t, note, "Update collection…", "the collection is the update unit")
	assert.Equal(t, 1, rowCount, "the collection's mod is counted, never a row")
	assert.Contains(t, rows, "Beta Mod")
	assert.NotContains(t, rows, "Alpha Mod")

	var line string
	f.runInBrowser(t,
		chromedp.Click(`.card--updates [data-action="skip-all"]`, chromedp.ByQuery),
		waitGone(`.card--updates`),
		chromedp.WaitVisible(`[data-testid="skipped-updates"]`, chromedp.ByQuery),
		textContent(`[data-testid="skipped-updates"] summary`, &line),
		// With no card left, the collection's line stays, quietly.
		chromedp.WaitVisible(`[data-testid="updates-collection-locked"]`, chromedp.ByQuery),
	)
	assert.Equal(t, "1 skipped update", strings.TrimSpace(line))
	row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "b", f.Game.ID, f.Profile)
	require.NoError(t, err)
	assert.Equal(t, "3.0", row.SkippedVersion)
	locked, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, f.Profile)
	require.NoError(t, err)
	assert.Empty(t, locked.SkippedVersion, "Skip all never touches the collection's mods")

	f.runInBrowser(t,
		chromedp.Click(`[data-testid="skipped-updates"] summary`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="skipped-updates"] [data-action="unskip-all"]`, chromedp.ByQuery),
		chromedp.Click(`[data-testid="skipped-updates"] [data-action="unskip-all"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--updates .card__row`, chromedp.ByQuery),
		waitGone(`[data-testid="skipped-updates"]`),
	)
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_SlideOver_UnlockingACollectionLockSaysSo(t *testing.T) {
	f := newE2EFixtureWithACollectionLock(t)

	var note string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		clickModRow("Alpha Mod"),
		chromedp.WaitVisible(`[data-testid="unlock-collection-note"]`, chromedp.ByQuery),
		textContent(`[data-testid="unlock-collection-note"]`, &note),
	)
	assert.Contains(t, note, "the collection will no longer manage its version")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ModPage_UnlockingACollectionLockSaysSo(t *testing.T) {
	f := newE2EFixtureWithACollectionLock(t)

	var note string
	var handNotes int
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`.mod-page [data-testid="unlock-collection-note"]`, chromedp.ByQuery),
		textContent(`.mod-page [data-testid="unlock-collection-note"]`, &note),
		chromedp.Navigate(f.ModPagePath("fake", "b")),
		chromedp.WaitVisible(`.mod-page .slide-over__settings`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="unlock-collection-note"]').length`, &handNotes),
	)
	assert.Contains(t, note, "the collection will no longer manage its version")
	assert.Zero(t, handNotes, "a mod the collection does not lock gets no note")
	assert.Empty(t, f.BrowserErrors())
}

// #667 review: with one source refusing for want of a key, the check is
// partial - the card shows the rows it did get beside the key notice - and
// Skip all still skips exactly those rows.
func TestE2E_UpdatesCard_SkipAllWorksBesideAKeyNeededNotice(t *testing.T) {
	f := newE2EFixtureWithACollectionLock(t)
	f.Svc.RegisterSource(&e2eKeylessSource{newE2EAuthSource("keyless", "never")})
	f.Game.SourceIDs["keyless"] = ""
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "km", SourceID: "keyless", Name: "Keyed Mod", Version: "1.0", GameID: f.Game.ID},
		ProfileName:  f.Profile,
		Enabled:      true,
		UpdatePolicy: domain.UpdateNotify,
	}))

	var rowCount int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates [data-testid="updates-auth-required"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--updates .card__row`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".card--updates .card__row").length`, &rowCount),
	)
	require.Equal(t, 1, rowCount, "Beta Mod's row, beside the key notice")

	var rowsLeft int
	f.runInBrowser(t,
		chromedp.Click(`.card--updates [data-action="skip-all"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="skipped-updates"]`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".card--updates .card__row").length === 0`),
		chromedp.Evaluate(`document.querySelectorAll(".card--updates .card__row").length`, &rowsLeft),
		// The key notice stays: the card is still the place that says so.
		chromedp.WaitVisible(`.card--updates [data-testid="updates-auth-required"]`, chromedp.ByQuery),
	)
	assert.Zero(t, rowsLeft, "Skip all hid the rows shown")
	row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "b", f.Game.ID, f.Profile)
	require.NoError(t, err)
	assert.Equal(t, "3.0", row.SkippedVersion)
	assert.Empty(t, f.BrowserErrors())
}
