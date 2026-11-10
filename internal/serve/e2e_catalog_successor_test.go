package serve_test

// Issue 590 in the browser: a mod its source's catalog no longer has, with
// a same-name, same-author mod in that source, is offered a "Re-link to
// <new-id>" on the Updates card and on its Health row. The button opens the
// existing mod_relink plan with the new id filled in - Confirm runs the job,
// nothing re-links on its own. Several candidates are listed, and no button
// picks one.

import (
	"slices"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installGoneWithSuccessors installs "Bear Mount" (by Donovan) under an id
// the gonecat source's catalog no longer has, and puts successors - the mods
// a name search in that source finds - in the catalog instead.
func installGoneWithSuccessors(t *testing.T, f e2eFixture, successors ...string) {
	t.Helper()
	src := newFakeSource("gonecat")
	for _, id := range successors {
		src.addMod(fakeSourceMod{
			Mod:   domain.Mod{ID: id, SourceID: "gonecat", Name: "Bear Mount", Author: "Donovan", Version: "1.0", GameID: f.Game.ID},
			Files: []domain.DownloadableFile{{ID: "f1", FileName: "bear.zip", Version: "1.0", IsPrimary: true}},
		})
	}
	f.Svc.RegisterSource(goneCatalogSource{src})
	// A re-link only targets a source the game maps, as Icarus is mapped
	// on a real Icarus game.
	f.Game.SourceIDs["gonecat"] = ""
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: "dLs3nvmWj5uOPnXxezGe", SourceID: "gonecat", Name: "Bear Mount", Author: "Donovan", Version: "1.0", GameID: f.Game.ID},
		ProfileName: f.Profile, UpdatePolicy: domain.UpdateNotify, Enabled: true, FileIDs: []string{"f1"},
	}))
	require.NoError(t, f.Svc.NewProfileManager().UpsertMod(t.Context(), f.Game.ID, f.Profile,
		domain.ModReference{SourceID: "gonecat", ModID: "dLs3nvmWj5uOPnXxezGe", Version: "1.0"}))
}

// relinkedTo waits for the installed mod to have moved to newID.
func relinkedTo(t *testing.T, f e2eFixture, newID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		list, err := f.Svc.ListMods(t.Context(), f.Game, f.Profile)
		return err == nil && slices.ContainsFunc(list.Mods, func(m core.ModListing) bool { return m.ID == newID })
	}, 10*time.Second, 100*time.Millisecond, "the confirmed re-link must have moved the mod to its new id")
}

func TestE2E_UpdatesCard_ReLinksAGoneModToItsSuccessor(t *testing.T) {
	f := newE2EFixture(t)
	installGoneWithSuccessors(t, f, "8m1z04vNo2CCdKcD2C0H")

	const button = `.card--updates [data-action="relink-successor"]`
	var label, note, fromTo string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(button, chromedp.ByQuery),
		textContent(button, &label),
		textContent(`[data-testid="catalog-successor"]`, &note),
		chromedp.Click(button, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="mod_relink"] .plan`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('[data-testid="relink-from-to"]')?.textContent.includes("8m1z04vNo2CCdKcD2C0H")`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		textContent(`[data-testid="relink-from-to"]`, &fromTo),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)

	assert.Contains(t, label, "Re-link to 8m1z04vNo2CCdKcD2C0H")
	assert.Contains(t, note, "Bear Mount")
	assert.Contains(t, note, "same name and author")
	assert.Contains(t, fromTo, "gonecat:dLs3nvmWj5uOPnXxezGe")
	relinkedTo(t, f, "8m1z04vNo2CCdKcD2C0H")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_UpdatesCard_SeveralSuccessorsAreListedNotPicked(t *testing.T) {
	f := newE2EFixture(t)
	installGoneWithSuccessors(t, f, "newA", "newB")

	var note string
	var buttons int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`[data-testid="catalog-successor"]`, chromedp.ByQuery),
		textContent(`[data-testid="catalog-successor"]`, &note),
		chromedp.Evaluate(`document.querySelectorAll('[data-action="relink-successor"]').length`, &buttons),
	)

	assert.Contains(t, note, "newA")
	assert.Contains(t, note, "newB")
	assert.Equal(t, 0, buttons, "an ambiguous match picks none")
	assert.Empty(t, f.BrowserErrors())
}

// Health says what the Updates card says - "no longer in the catalog", not
// "could not check version" - and offers the same re-link.
func TestE2E_HealthCard_CatalogMissingRowReLinksToItsSuccessor(t *testing.T) {
	f := newE2EFixture(t)
	installGoneWithSuccessors(t, f, "8m1z04vNo2CCdKcD2C0H")

	const row = `.card--health [data-status="catalog_missing"]`
	var text string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(row+` [data-action="relink-successor"]`, chromedp.ByQuery),
		textContent(row, &text),
		chromedp.Click(row+` [data-action="relink-successor"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="mod_relink"] .plan`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('[data-testid="relink-from-to"]')?.textContent.includes("8m1z04vNo2CCdKcD2C0H")`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)

	assert.Contains(t, text, "no longer in")
	assert.NotContains(t, text, "could not check version")
	assert.Contains(t, text, "Re-link to 8m1z04vNo2CCdKcD2C0H")
	relinkedTo(t, f, "8m1z04vNo2CCdKcD2C0H")
	assert.Empty(t, f.BrowserErrors())
}
