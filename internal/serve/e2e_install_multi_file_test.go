package serve_test

// #637: the install picker selects several files of one mod, as `lmm
// install --file a,b` does. The primary file starts checked (today's
// default), the plan lists every selected file, Confirm waits for at least
// one, and a mod whose files each carry a FOMOD installer is asked about
// one file at a time.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

const (
	installFilesPicker = `.modal[data-kind="install"] [data-testid="install-files-picker"]`
	installFilesList   = `.modal[data-kind="install"] [data-testid="install-files"]`
)

// installFileBox is the picker's checkbox for one file id.
func installFileBox(id string) string {
	return installFilesPicker + ` input[name="install-file"][value="` + id + `"]`
}

func TestE2E_InstallPicker_SelectsTwoFilesAndDeploysBoth(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	row := searchResultRow("fake", e2eSearchMultiFileModID)

	var legend string
	var primaryChecked, otherChecked, confirmDisabledWithNone bool
	var noneNote string
	var listed []string
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("edition")),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		clickWhenSettled(row+" .search-result__install"),
		chromedp.WaitVisible(installFilesPicker, chromedp.ByQuery),
		textContent(installFilesPicker+` legend`, &legend),
		// Today's default: the primary file, and only it, starts checked.
		chromedp.Evaluate(`document.querySelector('`+installFileBox("m1")+`').checked`, &primaryChecked),
		chromedp.Evaluate(`document.querySelector('`+installFileBox("m2")+`').checked`, &otherChecked),

		// None checked: nothing to install, so Confirm waits.
		clickWhenSettled(installFileBox("m1")),
		chromedp.WaitVisible(`.modal[data-kind="install"] [data-testid="install-files-none"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="install"] [data-testid="install-files-none"]`, &noneNote),
		chromedp.Evaluate(`document.querySelector('.modal [data-action="confirm"]').disabled`, &confirmDisabledWithNone),

		// Both, the second by keyboard: a checkbox toggles on Space.
		clickWhenSettled(installFileBox("m1")),
		chromedp.Focus(installFileBox("m2"), chromedp.ByQuery),
		chromedp.KeyEvent(" "),
		pollUntil(`document.querySelector('`+installFileBox("m2")+`').checked`),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('`+installFilesList+` li')).map(li => li.textContent.trim())`, &listed),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		chromedp.WaitVisible(row+` .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)
	assert.Contains(t, legend, "Files")
	assert.True(t, primaryChecked, "the primary file is preselected")
	assert.False(t, otherChecked, "only the primary file is preselected")
	assert.Contains(t, noneNote, "Select at least one file")
	assert.True(t, confirmDisabledWithNone, "Confirm waits for a file")
	if assert.Len(t, listed, 2, "the plan lists every selected file") {
		assert.Contains(t, listed[0], "Regular Edition")
		assert.Contains(t, listed[1], "Definitive Edition")
	}

	for rel, want := range map[string]string{
		"Mods/multi-regular.pak":    "payload for multi/m1",
		"Mods/multi-definitive.pak": "payload for multi/m2",
	} {
		got, err := os.ReadFile(filepath.Join(f.Game.ModPath, filepath.FromSlash(rel)))
		require.NoError(t, err, rel)
		assert.Equal(t, want, string(got), rel)
	}
	installed, err := f.Svc.GetInstalledMod(t.Context(), "fake", e2eSearchMultiFileModID, f.Game.ID, "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"m1", "m2"}, installed.FileIDs)
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_InstallPicker_TwoFomodFilesAskPerFile(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: "bugs", SourceID: "fake", Name: "Scrambled Bugs", Version: "1.0"},
		files: []domain.DownloadableFile{
			{ID: "b1", Name: "Main", FileName: "bugs.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512},
			{ID: "b2", Name: "Patches", FileName: "bugs-patches.zip", Version: "1.0", Category: "OPTIONAL", Size: 512},
		},
		archives: map[string][]byte{"b1": e2eFomodArchive(t), "b2": e2ePatchArchive(t)},
	})
	row := searchResultRow("fake", "bugs")
	mainStep := fomodModal + ` [data-testid="fomod-step"][data-file="b1"]`
	patchStep := fomodModal + ` [data-testid="fomod-step"][data-file="b2"]`

	var mainFile, patchFile string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "Scrambled", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		clickWhenSettled(row+" .search-result__install"),
		chromedp.WaitVisible(installFilesPicker, chromedp.ByQuery),
		clickWhenSettled(installFileBox("b2")),
		pollUntil(`document.querySelectorAll('`+installFilesList+` li').length === 2`),
		clickWhenSettled(`.modal [data-action="confirm"]`),

		// One labelled page per file's installer.
		chromedp.WaitVisible(mainStep, chromedp.ByQuery),
		textContent(mainStep+` [data-testid="fomod-file"]`, &mainFile),
		clickWhenSettled(fomodModal+` [data-action="fomod-next"]`),
		chromedp.WaitVisible(patchStep, chromedp.ByQuery),
		textContent(patchStep+` [data-testid="fomod-file"]`, &patchFile),
		chromedp.Click(patchStep+` [data-option="Vanilla"] input`, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-action="fomod-finish"]`),
		waitGone(fomodModal),
	)
	assert.Contains(t, mainFile, "this one is in Main")
	assert.Contains(t, patchFile, "this one is in Patches")
	require.Eventually(t, func() bool { return e2eDeployed(t, f.Game, "Mods/bugs_patch.esp") == "VANILLA" }, 30*time.Second, e2ePollInterval)
	assert.Equal(t, "2K", e2eDeployed(t, f.Game, "Mods/t.dds"))

	p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "default")
	require.NoError(t, err)
	ref := p.FindRef("fake", "bugs")
	require.NotNil(t, ref)
	assert.ElementsMatch(t, []string{"b1", "b2"}, ref.FileIDs)
	assert.Len(t, ref.FomodFiles, 2, "each file's answers are recorded under it")
	assert.Empty(t, f.BrowserErrors())
}
