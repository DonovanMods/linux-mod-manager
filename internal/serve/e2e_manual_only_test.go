package serve_test

// #543: a mod whose source will not serve its files through the API is
// marked "Manual" wherever it is shown, and the Updates card gives it no
// checkbox - "Update all" and select-all leave it out - but offers its page
// and "Update from file…" on the row itself. Driven in a real browser,
// because the claims are about what a user sees and can click.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// newE2EFixtureWithManualOnlyMod is newE2EFixtureWithManualUpdate after lmm
// has learned Manual Up is manual-only: an explicit update of it met the
// source's refusal, which is what records the fact.
func newE2EFixtureWithManualOnlyMod(t *testing.T) e2eSearchFixture {
	t.Helper()
	f := newE2EFixtureWithManualUpdate(t)
	row := manualUpRow(t, f)
	plan, err := f.Svc.PlanUpdateFrom(t.Context(), f.Game, "default", domain.Update{InstalledMod: *row, NewVersion: "2.0"})
	require.NoError(t, err)
	_, err = f.Svc.ApplyUpdate(t.Context(), f.Game, plan, core.UpdateOptions{}, nil)
	require.ErrorIs(t, err, source.ErrManualDownload)
	require.True(t, manualUpRow(t, f).ManualOnly, "precondition: core recorded the refusal")
	return f
}

// manualCardView is what the Updates card and the library say about Manual
// Up, read in one pass.
type manualCardView struct {
	Checkbox    bool   `json:"checkbox"`
	Badge       string `json:"badge"`
	Link        string `json:"link"`
	FromFile    string `json:"fromFile"`
	UpdateAll   string `json:"updateAll"`
	SelectAll   string `json:"selectAll"`
	Others      int    `json:"others"`
	LibraryMark string `json:"libraryMark"`
	RowUpdate   bool   `json:"rowUpdate"`
}

const manualCardJS = `(() => {
	const rows = [...document.querySelectorAll(".card--updates .card__row")];
	const row = rows.find((r) => r.textContent.includes("Manual Up"));
	if (!row) return null;
	const libRow = document.querySelector('.library__table tr[data-mod="fake:manualup"]');
	return {
		checkbox: Boolean(row.querySelector('input[type="checkbox"]')),
		badge: row.querySelector('[data-testid="update-manual"]')?.getAttribute("aria-label") ?? "",
		link: row.querySelector("a.mod-page-link")?.getAttribute("href") ?? "",
		fromFile: row.querySelector('[data-action="update-from-file"]')?.getAttribute("aria-label") ?? "",
		updateAll: document.querySelector('.card--updates [data-action="update-all"]').textContent.trim(),
		selectAll: document.querySelector('.card--updates [data-testid="select-all"]').getAttribute("aria-label"),
		others: rows.filter((r) => r !== row).length,
		libraryMark: libRow?.querySelector('[data-testid="row-manual"]')?.textContent.trim() ?? "",
		rowUpdate: Boolean(libRow?.querySelector('[data-action="row-update"]')),
	};
})()`

// TestE2E_ManualOnly_UpdatesCardOffersThePageAndUpdateFromFile: the row is
// marked, has no checkbox, is left out of "Update all" and select-all, and
// its own "Update from file…" takes the downloaded archive through to a
// finished update. The library row carries the same mark and no Update.
func TestE2E_ManualOnly_UpdatesCardOffersThePageAndUpdateFromFile(t *testing.T) {
	f := newE2EFixtureWithManualOnlyMod(t)
	archive := fromFileArchive(t, "ManualUp-2.0.zip", "v2 bytes")

	var view manualCardView
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates [data-testid="update-manual"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.library__table tr[data-mod="fake:manualup"] [data-testid="row-manual"]`, chromedp.ByQuery),
		chromedp.Evaluate(manualCardJS, &view),
	)
	assert.False(t, view.Checkbox, "no batch will download it, so there is nothing for a tick to mean")
	assert.Contains(t, view.Badge, "Manual download")
	assert.Equal(t, e2eManualUpPage, view.Link, "the row links to where the file has to come from")
	assert.Equal(t, "Update from file…: Manual Up", view.FromFile)
	assert.Equal(t, fmt.Sprintf("Update all (%d)", view.Others), view.UpdateAll, "Update all leaves it out")
	assert.Contains(t, view.SelectAll, fmt.Sprintf("%d mod", view.Others), "and so does select-all")
	assert.Equal(t, "Manual", view.LibraryMark)
	assert.False(t, view.RowUpdate, "the library row offers no Update the batch would only skip")

	f.runInBrowser(t,
		clickWhenSettled(`.card--updates [data-action="update-from-file"]`),
		chromedp.SetUploadFiles(e2eFromFileInput, []string{archive}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="update_from_archive"] .plan[data-match="advertised"]`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
	)
	require.Eventually(t, func() bool { return manualUpRow(t, f).Version == "2.0" }, e2eTimeout, 50*time.Millisecond)
	assert.Equal(t, []string{"u2"}, manualUpRow(t, f).FileIDs, "the advertised file's ID is adopted")
	got, err := os.ReadFile(filepath.Join(f.Game.ModPath, e2eManualUpFile))
	require.NoError(t, err)
	assert.Equal(t, "v2 bytes", string(got))
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_ManualOnly_SlideOverAndModPageSaySo: both detail surfaces carry
// the mark and what it means, beside the "Update from file…" they already
// offer; the slide-over's Update is disabled rather than offering an update
// the batch would only skip.
func TestE2E_ManualOnly_SlideOverAndModPageSaySo(t *testing.T) {
	f := newE2EFixtureWithManualOnlyMod(t)

	var panelNote, pageNote string
	var updateDisabled bool
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "manualup")),
		chromedp.WaitVisible(`.slide-over [data-testid="manual-download"]`, chromedp.ByQuery),
		textContent(`.slide-over [data-testid="manual-download"]`, &panelNote),
		chromedp.Evaluate(`document.querySelector(".slide-over__actions button.button--primary").disabled`, &updateDisabled),
		chromedp.WaitVisible(`.slide-over__actions [data-action="update-from-file"]`, chromedp.ByQuery),
		chromedp.Navigate(f.ModPagePath("fake", "manualup")),
		chromedp.WaitVisible(`.mod-page [data-testid="manual-download"]`, chromedp.ByQuery),
		textContent(`.mod-page [data-testid="manual-download"]`, &pageNote),
		chromedp.WaitVisible(`.mod-page__actions [data-action="update-from-file"]`, chromedp.ByQuery),
	)
	for name, note := range map[string]string{"slide-over": panelNote, "mod page": pageNote} {
		assert.Contains(t, note, "Manual", "%s carries the mark", name)
		assert.Contains(t, note, "will not serve its files to lmm", "%s says what it means", name)
		assert.Contains(t, note, "Update from file…", "%s names the way out", name)
	}
	assert.True(t, updateDisabled, "the slide-over's Update would only be skipped")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
