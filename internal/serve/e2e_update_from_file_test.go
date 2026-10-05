package serve_test

// #530: "Update from file…" - updating an installed mod from an archive the
// user downloaded by hand, because its source refuses API downloads. Driven
// in a real browser, because the claims are about what a user can click: the
// button beside "Open on <source>" on the failed update, a real
// <input type="file"> carrying a real archive through POST /api/v1/uploads,
// the plan in the confirm modal, and the job that finishes the update.

import (
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

const (
	e2eManualUpPage = "https://example.test/mods/manualup"
	e2eManualUpFile = "Mods/manualup.pak"
	// e2eFromFileInput is the page's one file input (updatefromfile.js).
	e2eFromFileInput = `#update-from-file-input`
)

// newE2EFixtureWithManualUpdate is the searchable fixture plus "Manual Up",
// installed and deployed at 1.0 (file u1), whose source advertises 2.0 (its
// sole 2.0 file, u2) but refuses every download - a CurseForge author's
// third-party opt-out. A Classic build (u3, labelled 2.0c) is listed beside
// it; a second file labelled 2.0 would leave the check advertising nothing.
func newE2EFixtureWithManualUpdate(t *testing.T) e2eSearchFixture {
	t.Helper()
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.updatable = true
	f.Src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: "manualup", SourceID: "fake", Name: "Manual Up", Version: "2.0", SourceURL: e2eManualUpPage},
		files: []domain.DownloadableFile{
			{ID: "u1", Name: "Main 1.0", FileName: "ManualUp-1.0.zip", Version: "1.0", Category: "MAIN", Size: 16},
			{ID: "u2", Name: "Main 2.0", FileName: "ManualUp-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 16},
			{ID: "u3", Name: "Classic 2.0", FileName: "ManualUp-Classic-2.0.zip", Version: "2.0c", Category: "MAIN", Size: 16},
		},
	})
	if f.Src.urlErrs == nil {
		f.Src.urlErrs = map[string]error{}
	}
	f.Src.urlErrs["manualup"] = &source.ManualDownloadError{Reason: "the author has turned off API downloads"}

	mod := domain.Mod{ID: "manualup", SourceID: "fake", Name: "Manual Up", Version: "1.0", GameID: f.Game.ID, SourceURL: e2eManualUpPage}
	seedInstalledMod(t, f.Svc, f.Game, mod, true, map[string][]byte{e2eManualUpFile: []byte("v1 bytes")})
	row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "manualup", f.Game.ID, "default")
	require.NoError(t, err)
	row.FileIDs = []string{"u1"}
	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), row))
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "manualup", Version: "1.0", FileIDs: []string{"u1"}}))
	_, err = f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return f
}

// fromFileArchive writes a real zip named name holding Manual Up's new pak.
func fromFileArchive(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, e2eZipWith(e2eManualUpFile, content), 0o644))
	return path
}

// manualUpRow reads Manual Up's installed row.
func manualUpRow(t *testing.T, f e2eSearchFixture) *domain.InstalledMod {
	t.Helper()
	row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "manualup", f.Game.ID, "default")
	require.NoError(t, err)
	return row
}

// failManualUpdate drives the slide-over's Update into the source's refusal,
// leaving the failed item's download way out on screen.
func failManualUpdate() chromedp.Action {
	return chromedp.Tasks{
		clickWhenSettled(`.slide-over__actions button.button--primary`),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		chromedp.WaitVisible(`.slide-over .job-progress [data-testid="download-page"] [data-action="update-from-file"]`, chromedp.ByQuery),
	}
}

// TestE2E_UpdateFromFile_FailedManualUpdateOffersItAndCompletes: the update
// fails because the source will not serve the file; beside "Open on
// <source>" the failure offers "Update from file…"; choosing the downloaded
// archive - under the name a browser gives a second download - previews it
// as the advertised file and completes the update.
func TestE2E_UpdateFromFile_FailedManualUpdateOffersItAndCompletes(t *testing.T) {
	f := newE2EFixtureWithManualUpdate(t)
	archive := fromFileArchive(t, "ManualUp-2.0 (1).zip", "v2 bytes")

	var link *e2eLink
	var hint, normalized string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "manualup")),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.slide-over__actions`, chromedp.ByQuery),
		failManualUpdate(),
		linkIn(`.slide-over .job-progress [data-testid="download-page"]`, &link),
		textContent(`.slide-over .job-progress .download-page__hint`, &hint),
		clickWhenSettled(`.slide-over .job-progress [data-testid="download-page"] [data-action="update-from-file"]`),
		chromedp.SetUploadFiles(e2eFromFileInput, []string{archive}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="update_from_archive"] .plan[data-match="advertised"]`, chromedp.ByQuery),
		textContent(`.modal [data-testid="match-normalized"]`, &normalized),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		// Scoped to THIS job's kind (#534): the failed update's readout stays
		// on screen (#531), and a batch with failed items is still
		// "succeeded", so an unscoped wait matched it before this job ended.
		chromedp.WaitVisible(`.slide-over .job-progress[data-kind="update_from_archive"][data-state="succeeded"]`, chromedp.ByQuery),
	)
	assertModPageLink(t, link, e2eManualUpPage, "Manual Up")
	assert.Contains(t, hint, "Update from file…", "the hint names the control beside it")
	assert.Contains(t, normalized, "ManualUp-2.0.zip", "the plan names the file the archive matched")

	row := manualUpRow(t, f)
	assert.Equal(t, "2.0", row.Version)
	assert.Equal(t, []string{"u2"}, row.FileIDs, "the advertised file's ID is adopted")
	assert.Equal(t, "1.0", row.PreviousVersion, "rollback has somewhere to go")
	got, err := os.ReadFile(filepath.Join(f.Game.ModPath, e2eManualUpFile))
	require.NoError(t, err)
	assert.Equal(t, "v2 bytes", string(got))
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_UpdateFromFile_SlideOverEntryPoint_MismatchIsUpdateAnyway: the
// slide-over's own "Update from file…" takes another flavor's build; the
// plan says it is not the advertised file, Confirm reads "Update anyway",
// and the update records the file the archive actually is.
func TestE2E_UpdateFromFile_SlideOverEntryPoint_MismatchIsUpdateAnyway(t *testing.T) {
	f := newE2EFixtureWithManualUpdate(t)
	archive := fromFileArchive(t, "ManualUp-Classic-2.0.zip", "classic bytes")

	var confirm, warning string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "manualup")),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.slide-over__actions [data-action="update-from-file"]`),
		chromedp.SetUploadFiles(e2eFromFileInput, []string{archive}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="update_from_archive"] .plan[data-match="mismatch"]`, chromedp.ByQuery),
		textContent(`.modal [data-testid="archive-mismatch"]`, &warning),
		textContent(`.modal [data-action="confirm"]`, &confirm),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		chromedp.WaitVisible(`.slide-over__actions .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)
	assert.Equal(t, "Update anyway", confirm)
	assert.Contains(t, warning, "ManualUp-2.0.zip", "the warning names the advertised file")

	row := manualUpRow(t, f)
	assert.Equal(t, "2.0c", row.Version, "the file the archive is decides the version")
	assert.Equal(t, []string{"u3"}, row.FileIDs)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_UpdateFromFile_RowMenuAsksForAVersionNothingNames: the library row
// menu's entry point, with an archive whose name says nothing: the modal asks
// for the version, re-plans with it, and the update records it.
func TestE2E_UpdateFromFile_RowMenuAsksForAVersionNothingNames(t *testing.T) {
	f := newE2EFixtureWithManualUpdate(t)
	archive := fromFileArchive(t, "download.zip", "renamed bytes")

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		clickWhenSettled(`.library__table tr[data-mod="fake:manualup"] [data-action="row-menu"]`),
		chromedp.WaitVisible(`.row-menu [data-action="update-from-file"]`, chromedp.ByQuery),
		clickWhenSettled(`.row-menu [data-action="update-from-file"]`),
		waitGone(`.row-menu`),
		chromedp.SetUploadFiles(e2eFromFileInput, []string{archive}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal [data-testid="version-prompt"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.modal [data-testid="version-prompt"] input`, "2.1", chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="replan-version"]`),
		chromedp.WaitVisible(`.modal[data-kind="update_from_archive"] .plan[data-match="mismatch"]`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
	)
	require.Eventually(t, func() bool { return manualUpRow(t, f).Version == "2.1" }, e2eTimeout, 50*time.Millisecond)
	assert.Empty(t, manualUpRow(t, f).FileIDs, "a name the source does not list records no file ID")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_UpdateFromFile_LabelsHoldInEveryFace is #521's stress pass over
// every surface the control was added to: the slide-over's actions, the full
// mod page's, the row menu, the failed update's download way out and the
// mismatch plan's footer - each label centred on one line, in every layout
// face, at 1280px.
func TestE2E_UpdateFromFile_LabelsHoldInEveryFace(t *testing.T) {
	f := newE2EFixtureWithManualUpdate(t)
	archive := fromFileArchive(t, "ManualUp-Classic-2.0.zip", "classic bytes")

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var panel, page, menu, download, modal map[string][]labelBox
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SlideOverPath("fake", "manualup")),
			chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.slide-over__actions [data-action="update-from-file"]`, chromedp.ByQuery),
			faceInEffect(face),
			measureLabels(`{"slide-over": ".slide-over__actions"}`, &panel),
			failManualUpdate(),
			measureLabels(`{"download page": ".slide-over .job-progress [data-testid=\"download-page\"]"}`, &download),
			clickWhenSettled(`.slide-over__actions [data-action="update-from-file"]`),
			chromedp.SetUploadFiles(e2eFromFileInput, []string{archive}, chromedp.ByQuery),
			chromedp.WaitVisible(`.modal[data-kind="update_from_archive"] .plan[data-match="mismatch"]`, chromedp.ByQuery),
			measureLabels(`{"plan modal": ".modal"}`, &modal),
			clickWhenSettled(`.modal [data-action="cancel"]`),
			waitGone(`.modal`),
			chromedp.Navigate(f.ModPagePath("fake", "manualup")),
			chromedp.WaitVisible(`.mod-page__actions [data-action="update-from-file"]`, chromedp.ByQuery),
			measureLabels(`{"mod page": ".mod-page__actions"}`, &page),
			chromedp.Navigate(f.HomePath()),
			chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
			clickWhenSettled(`.library__table tr[data-mod="fake:manualup"] [data-action="row-menu"]`),
			chromedp.WaitVisible(`.row-menu [data-action="update-from-file"]`, chromedp.ByQuery),
			measureLabels(`{"row menu": ".row-menu"}`, &menu),
		)
		assertLabelsCentred(t, "slide-over", panel["slide-over"])
		assertLabelsCentred(t, "download page", download["download page"])
		assertLabelsCentred(t, "plan modal", modal["plan modal"])
		assertLabelsCentred(t, "mod page", page["mod page"])
		assertLabelsCentred(t, "row menu", menu["row menu"])
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
