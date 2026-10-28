package serve_test

// #535: "Install from file…" - installing a mod from an archive the user
// downloaded by hand, because its source refused the install's download.
// Driven in a real browser, because the claims are about what a user sees
// and clicks: the failure leading with what it means beside the button, a
// real <input type="file"> carrying a real archive, the plan in the confirm
// modal, the job that finishes the install - and the readout keeping clear
// of its search row in every layout face.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

const (
	e2eMapUtilsPage = "https://example.test/mods/maputils"
	e2eMapUtilsFile = "Mods/maputils.pak"
	// e2eMapUtilsName is the owner's mod: a name long enough to wrap.
	e2eMapUtilsName = "MapUtils (Dungeon Maps, Dungeon Entrance, Travel-Routes, ...) (Leatrix Maps Alternative)"
)

// newE2EFixtureWithManualInstall is the searchable fixture plus the owner's
// MapUtils, not installed, whose source refuses every download - a
// CurseForge author's third-party opt-out. It lists the primary
// 1.2.24-classic build (mu1), another flavor's (mu1r) and a newer build
// (mu2) its update check offers to a row recording mu1 - a check by file ID,
// as CurseForge's is.
func newE2EFixtureWithManualInstall(t *testing.T) e2eSearchFixture {
	t.Helper()
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: "maputils", SourceID: "fake", Name: e2eMapUtilsName, Version: "1.2.24-classic", SourceURL: e2eMapUtilsPage},
		files: []domain.DownloadableFile{
			{ID: "mu1", Name: "Classic", FileName: "MapUtils-1.2.24-classic.zip", Version: "1.2.24-classic", Category: "MAIN", IsPrimary: true, Size: 16},
			{ID: "mu1r", Name: "Retail", FileName: "MapUtils-1.2.24-retail.zip", Version: "1.2.24-retail", Category: "MAIN", Size: 16},
			{ID: "mu2", Name: "Classic (next)", FileName: "MapUtils-1.2.25-classic.zip", Version: "1.2.25-classic", Category: "MAIN", Size: 16},
		},
	})
	if f.Src.urlErrs == nil {
		f.Src.urlErrs = map[string]error{}
	}
	f.Src.urlErrs["maputils"] = &source.ManualDownloadError{Reason: "mod author has disabled third-party downloads"}
	f.Src.fileIDUpdates = map[string]string{"mu1": "mu2"}
	return f
}

// mapUtilsArchive writes a real zip named name holding MapUtils' pak.
func mapUtilsArchive(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, e2eZipWith(e2eMapUtilsFile, content), 0o644))
	return path
}

// failSearchInstall drives the omnibar's fan-out to MapUtils' failed install,
// leaving its readout under the row.
func failSearchInstall(f e2eSearchFixture) chromedp.Action {
	row := searchResultRow("fake", "maputils")
	return chromedp.Tasks{
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "MapUtils", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		clickWhenSettled(row + " .search-result__install"),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		chromedp.WaitVisible(row+` .job-progress[data-kind="install"][data-state="failed"] [data-action="install-from-file"]`, chromedp.ByQuery),
	}
}

type installFailureView struct {
	Headline  string `json:"headline"`
	Hint      string `json:"hint"`
	Raw       string `json:"raw"`
	RawOpen   bool   `json:"rawOpen"`
	FromFile  string `json:"fromFile"`
	FromLabel string `json:"fromLabel"`
}

const installFailureJS = `(() => {
	const ro = document.querySelector('.search-result[data-mod="fake/maputils"] .job-progress[data-kind="install"]');
	const text = (el) => (el ? el.textContent.replace(/\s+/g, " ").trim() : "");
	const raw = ro.querySelector('[data-testid="raw-error"]');
	const btn = ro.querySelector('[data-action="install-from-file"]');
	return {
		headline: text(ro.querySelector(".job-progress__text")),
		hint: text(ro.querySelector(".download-page__hint")),
		raw: text(raw?.querySelector(".raw-error__text")),
		rawOpen: Boolean(raw?.open),
		fromFile: text(btn),
		fromLabel: btn?.getAttribute("aria-label") ?? "",
	};
})()`

// TestE2E_InstallFromFile_FailedSearchInstallCompletesFromTheFile is the
// owner's case: a search install of a mod whose source refuses the download
// fails with an explanation that leads - the engine's text collapsed under
// "Details" - and "Install from file…" beside "Open on <source>". Choosing
// the downloaded archive (under the name a browser gives a second download)
// previews it as the file the install tried and completes the install: the
// mod is in the library at that file's version and file ID, so a later
// update check - by file ID - finds its update.
func TestE2E_InstallFromFile_FailedSearchInstallCompletesFromTheFile(t *testing.T) {
	f := newE2EFixtureWithManualInstall(t)
	archive := mapUtilsArchive(t, "MapUtils-1.2.24-classic (1).zip", "maputils bytes")
	row := searchResultRow("fake", "maputils")

	var view installFailureView
	var link *e2eLink
	var normalized string
	f.runInBrowser(t,
		failSearchInstall(f),
		awaitSourceNamed(row+" .job-progress"),
		chromedp.Evaluate(installFailureJS, &view),
		linkIn(row+" .job-progress", &link),
		clickWhenSettled(row+` .job-progress [data-action="install-from-file"]`),
		chromedp.SetUploadFiles(e2eFromFileInput, []string{archive}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="import_archive"] .plan[data-match="advertised"]`, chromedp.ByQuery),
		textContent(`.modal [data-testid="match-normalized"]`, &normalized),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		// Scoped to THIS job's kind (#534): the failed install's readout was
		// on screen under the same origin a moment ago.
		chromedp.WaitVisible(row+` .job-progress[data-kind="import_archive"][data-state="succeeded"]`, chromedp.ByQuery),
	)
	assert.Equal(t, e2eSourceName+" won't let lmm download "+e2eMapUtilsName+".", view.Headline, "the explanation leads")
	assert.Contains(t, view.Hint, "MapUtils-1.2.24-classic.zip", "it names the file to download")
	assert.Contains(t, view.Hint, `"Install from file…" below`, "and points at the button beside it")
	assert.NotContains(t, view.Hint, "Import an archive")
	assert.Contains(t, view.Raw, "mod author has disabled third-party downloads", "the engine's text is kept")
	assert.False(t, view.RawOpen, "but collapsed under Details")
	assert.Equal(t, "Install from file…", view.FromFile)
	assert.Equal(t, "Install from file…: "+e2eMapUtilsName, view.FromLabel)
	assertModPageLink(t, link, e2eMapUtilsPage, e2eMapUtilsName)
	assert.Contains(t, normalized, "MapUtils-1.2.24-classic.zip", "the plan names the file the archive matched")

	ctx := t.Context()
	mod, err := f.Svc.GetInstalledMod(ctx, "fake", "maputils", f.Game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.2.24-classic", mod.Version)
	assert.Equal(t, []string{"mu1"}, mod.FileIDs, "the file the install tried")
	got, err := os.ReadFile(filepath.Join(f.Game.ModPath, e2eMapUtilsFile))
	require.NoError(t, err)
	assert.Equal(t, "maputils bytes", string(got))

	updates, err := f.Svc.CheckGameUpdates(ctx, f.Game, "default", []domain.InstalledMod{*mod}, nil, core.UpdateCheckOptions{})
	require.NoError(t, err)
	require.Len(t, updates, 1, "the recorded file ID is what the update check compares")
	assert.Equal(t, "1.2.25-classic", updates[0].NewVersion)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_InstallFromFile_FromTheTray_MismatchIsInstallAnyway: the tray's
// failure offers the same button; another flavor's build is not the file
// the install tried, so the plan says so, Confirm reads "Install anyway",
// and the install records the file the archive actually is.
func TestE2E_InstallFromFile_FromTheTray_MismatchIsInstallAnyway(t *testing.T) {
	f := newE2EFixtureWithManualInstall(t)
	archive := mapUtilsArchive(t, "MapUtils-1.2.24-retail.zip", "retail bytes")

	var confirm, warning, trayHeadline string
	f.runInBrowser(t,
		failSearchInstall(f),
		clickWhenSettled(`.activity-bell__trigger`),
		chromedp.WaitVisible(`.tray__failure [data-action="install-from-file"]`, chromedp.ByQuery),
		awaitSourceNamed(`.tray__failure`),
		textContent(`.tray__failure-message`, &trayHeadline),
		clickWhenSettled(`.tray__failure [data-action="install-from-file"]`),
		chromedp.SetUploadFiles(e2eFromFileInput, []string{archive}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="import_archive"] .plan[data-match="mismatch"]`, chromedp.ByQuery),
		textContent(`.modal [data-testid="archive-mismatch"]`, &warning),
		textContent(`.modal [data-action="confirm"]`, &confirm),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
	)
	assert.Contains(t, trayHeadline, e2eSourceName+" won't let lmm download", "the tray leads with the explanation too")
	assert.Equal(t, "Install anyway", confirm)
	assert.Contains(t, warning, "MapUtils-1.2.24-classic.zip", "the warning names the file the install tried")
	require.Eventually(t, func() bool {
		mod, err := f.Svc.GetInstalledMod(t.Context(), "fake", "maputils", f.Game.ID, "default")
		return err == nil && mod.Version == "1.2.24-retail"
	}, e2eTimeout, 50*time.Millisecond)
	mod, err := f.Svc.GetInstalledMod(t.Context(), "fake", "maputils", f.Game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, []string{"mu1r"}, mod.FileIDs)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_InstallFromFile_ReadoutHoldsInEveryFace is #521's stress pass over
// the owner's screenshot: in every layout face, at 1280 and 720, the failed
// install's readout sits inside its search result on a line of its own,
// clear of the row's name, link and Install control, every control in it
// reachable and every label centred on one line.
func TestE2E_InstallFromFile_ReadoutHoldsInEveryFace(t *testing.T) {
	f := newE2EFixtureWithManualInstall(t)
	row := searchResultRow("fake", "maputils")

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		for _, width := range []int64{1280, 720} {
			var layout *readoutLayout
			var labels map[string][]labelBox
			f.runInBrowser(t,
				chromedp.EmulateViewport(width, 900),
				failSearchInstall(f),
				faceInEffect(face),
				chromedp.Evaluate(readoutLayoutJS(row, `.search-result__row button, .search-result__row a`), &layout),
				measureLabels(`{"download page": `+jsString(row+` [data-testid="download-page"]`)+`}`, &labels),
			)
			where := fmt.Sprintf("%s/%dpx", face.name, width)
			assertReadoutLayout(t, where, layout)
			assertLabelsCentred(t, where, labels["download page"])
		}
		assertNoUncaughtErrors(t, f.BrowserErrors())
	})
}
