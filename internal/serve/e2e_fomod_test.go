package serve_test

// #615: the FOMOD installer wizard in a real browser - an install from the
// search page whose archive is an installer opens the wizard, which shows
// the option's image from the read-only route, walks a step, re-posts the
// job with the answer (the server asks again about the step that answer
// made visible), and finishes the install. Then "Reconfigure installer…"
// on the full mod page changes the choice and redeploys.

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// e2eFomodPNG is a 1x1 PNG the browser decodes.
var e2eFomodPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0xf0,
	0x1f, 0x00, 0x05, 0x00, 0x01, 0xff, 0x89, 0x99, 0x3d, 0x1d, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
	0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

const e2eFomodConfig = `<config><moduleName>Wizard Installer</moduleName>
<requiredInstallFiles><file source="core\plugin.esp" destination="Mods\plugin.esp"/></requiredInstallFiles>
<installSteps order="Explicit">
<installStep name="Size"><optionalFileGroups order="Explicit">
<group name="Texture" type="SelectExactlyOne"><plugins order="Explicit">
<plugin name="2K"><description>Light on VRAM.</description><image path="fomod\images\2k.png"/><files><file source="tex\2k.dds" destination="Mods\t.dds"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
<plugin name="4K"><description>Heavy.</description><files><file source="tex\4k.dds" destination="Mods\t.dds"/></files><conditionFlags><flag name="big">on</flag></conditionFlags><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
</plugins></group></optionalFileGroups></installStep>
<installStep name="Extras"><visible><flagDependency flag="big" value="on"/></visible><optionalFileGroups order="Explicit">
<group name="Docs" type="SelectAny"><plugins order="Explicit">
<plugin name="Readme"><description>A text file.</description><files><file source="docs\readme.txt" destination="Mods\readme.txt"/></files><typeDescriptor><type name="Recommended"/></typeDescriptor></plugin>
</plugins></group></optionalFileGroups></installStep>
</installSteps></config>`

func e2eFomodArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string][]byte{
		"fomod/ModuleConfig.xml": []byte(e2eFomodConfig),
		"fomod/images/2k.png":    e2eFomodPNG,
		"core/plugin.esp":        []byte("PLUGIN"),
		"tex/2k.dds":             []byte("2K"),
		"tex/4k.dds":             []byte("4K"),
		"docs/readme.txt":        []byte("README"),
	} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func e2eDeployed(t *testing.T, game *domain.Game, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(game.ModPath, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(b)
}

const fomodModal = `.modal[data-kind="fomod"]`

func TestE2E_FomodWizard_WalksRepostsShowsImagesAndReconfigures(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod:      domain.Mod{ID: "wizard", SourceID: "fake", Name: "Wizard Mod", Version: "1.0"},
		files:    []domain.DownloadableFile{{ID: "w1", Name: "Main", FileName: "wizard.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512}},
		archives: map[string][]byte{"w1": e2eFomodArchive(t)},
	})
	row := searchResultRow("fake", "wizard")

	var imageWidth int
	var step2Checked bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "Wizard", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		clickWhenSettled(row+" .search-result__install"),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),

		// The job fails with the installer's question and the wizard opens.
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"][data-step="Size"]`, chromedp.ByQuery),
		// The option image comes through the read-only route and decodes.
		pollUntil(`(() => { const img = document.querySelector('`+fomodModal+` [data-testid="fomod-image"]'); return img && img.complete && img.naturalWidth > 0; })()`),
		chromedp.Evaluate(`document.querySelector('`+fomodModal+` [data-testid="fomod-image"]').naturalWidth`, &imageWidth),

		// 4K changes what the installer shows next: Finish re-posts the job.
		chromedp.Click(fomodModal+` [data-option="4K"] input`, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-action="fomod-finish"]`),

		// The re-posted job asks again, about the Extras step 4K revealed,
		// with its recommended Readme preselected.
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"][data-step="Extras"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('`+fomodModal+` [data-option="Readme"] input').checked`, &step2Checked),
		clickWhenSettled(fomodModal+` [data-action="fomod-finish"]`),
		waitGone(fomodModal),
	)
	assert.Equal(t, 1, imageWidth)
	assert.True(t, step2Checked, "the revealed step's recommended option is preselected")
	require.Eventually(t, func() bool { return e2eDeployed(t, f.Game, "Mods/t.dds") == "4K" }, 30*time.Second, e2ePollInterval)
	assert.Equal(t, "README", e2eDeployed(t, f.Game, "Mods/readme.txt"))
	assert.Equal(t, "PLUGIN", e2eDeployed(t, f.Game, "Mods/plugin.esp"))

	// Reconfigure from the full mod page: the recorded 4K is preselected;
	// 2K redeploys (the Extras step disappears with it).
	var preselected4K bool
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "wizard")),
		clickWhenSettled(`[data-action="reconfigure-fomod"]`),
		chromedp.WaitVisible(`.modal[data-kind="mod_fomod"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"][data-step="Size"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('`+fomodModal+` [data-option="4K"] input').checked`, &preselected4K),
		chromedp.Click(fomodModal+` [data-option="2K"] input`, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-action="fomod-finish"]`),
		waitGone(fomodModal),
	)
	assert.True(t, preselected4K, "the recorded choice is preselected")
	require.Eventually(t, func() bool { return e2eDeployed(t, f.Game, "Mods/t.dds") == "2K" }, 30*time.Second, e2ePollInterval)
	assert.Empty(t, e2eDeployed(t, f.Game, "Mods/readme.txt"), "the Extras step's file left with it")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FomodWizard_ResultShapedQuestionAndEscape: an install with a
// dependency runs as a batch, which refuses with the question on its
// RESULT (the job succeeds, nothing written): the wizard opens from that
// too, and its answer installs both mods. Escape closes the wizard and
// returns focus to the control that started the flow.
func TestE2E_FomodWizard_ResultShapedQuestionAndEscape(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod:      domain.Mod{ID: "wizard", SourceID: "fake", Name: "Wizard Mod", Version: "1.0"},
		files:    []domain.DownloadableFile{{ID: "w1", Name: "Main", FileName: "wizard.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512}},
		archives: map[string][]byte{"w1": e2eFomodArchive(t)},
	})
	f.Src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "needs", SourceID: "fake", Name: "Needs Wizard", Version: "1.0"},
		files:   []domain.DownloadableFile{{ID: "n1", Name: "Main", FileName: "needs.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 16}},
		members: map[string]string{"n1": "Mods/needs.pak"},
	})
	if f.Src.deps == nil {
		f.Src.deps = map[string][]domain.ModReference{}
	}
	f.Src.deps["needs"] = []domain.ModReference{{SourceID: "fake", ModID: "wizard"}}
	row := searchResultRow("fake", "needs")

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "Needs", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		clickWhenSettled(row+" .search-result__install"),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		// The batch job succeeds, refused with the question on its result.
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"][data-mod="fake:wizard"]`, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-action="fomod-finish"]`),
		waitGone(fomodModal),
	)
	require.Eventually(t, func() bool { return e2eDeployed(t, f.Game, "Mods/needs.pak") != "" }, 30*time.Second, e2ePollInterval)
	assert.Equal(t, "2K", e2eDeployed(t, f.Game, "Mods/t.dds"), "the installer's preselection, answered")

	// Escape: reconfigure from the mod page, then dismiss the wizard.
	//
	// The job's end re-reads this page, and that re-read must not replace
	// the opener (issue 615 follow-up): a re-hydrate that blanked the
	// page's live detail for a moment unmounted the button and mounted a
	// new one, so focus blinked to <body> until modal.js's refocus caught
	// it. openerWatchJS records whether the button node ever leaves the
	// document, and counts this mod's detail reads so the test can wait
	// for the re-read to have landed before it asks.
	var replaced bool
	var focused string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "wizard")),
		chromedp.WaitVisible(`[data-action="reconfigure-fomod"]`, chromedp.ByQuery),
		chromedp.Evaluate(openerWatchJS(`[data-action="reconfigure-fomod"]`, "/api/v1/mods/fake/wizard"), nil),
		clickWhenSettled(`[data-action="reconfigure-fomod"]`),
		chromedp.WaitVisible(`.modal[data-kind="mod_fomod"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"]`, chromedp.ByQuery),
		// The shell's Escape listener is attached in an effect.
		settleEffects(),
		chromedp.KeyEvent(kb.Escape),
		waitGone(fomodModal),
		// The job's re-read has been asked for and has answered.
		pollUntil(`window.__openerWatch.detailReads >= 1 && window.__openerWatch.inflight === 0`),
		settleEffects(),
		chromedp.Evaluate(`window.__openerWatch.replaced`, &replaced),
		// Focus is on the opener, and stays there: two checks in a row,
		// each reading the attribute in the same evaluation.
		pollUntil(stableFocusJS(`[data-action="reconfigure-fomod"]`)),
		chromedp.Evaluate(`document.activeElement && document.activeElement.getAttribute("data-action")`, &focused),
	)
	assert.False(t, replaced, "the post-job re-read kept the opener's node")
	assert.Equal(t, "reconfigure-fomod", focused)
	assert.Equal(t, "2K", e2eDeployed(t, f.Game, "Mods/t.dds"), "a dismissed wizard changes nothing")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FomodWizard_UseTheInstallersDefaults: the web counterpart of
// --fomod-defaults. A reconfigure of a mod recorded with 4K and the readme
// opens preselected with them; one click answers with the installer's own
// defaults (2K, no Extras step) and re-posts, without walking a step.
func TestE2E_FomodWizard_UseTheInstallersDefaults(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod:      domain.Mod{ID: "wizard", SourceID: "fake", Name: "Wizard Mod", Version: "1.0"},
		files:    []domain.DownloadableFile{{ID: "w1", Name: "Main", FileName: "wizard.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512}},
		archives: map[string][]byte{"w1": e2eFomodArchive(t)},
	})
	ctx := t.Context()
	plan, err := f.Svc.PlanInstall(ctx, f.Game, "default", "fake", "wizard", false)
	require.NoError(t, err)
	_, err = f.Svc.ApplyInstall(ctx, f.Game, plan, core.InstallOptions{Fomod: map[string]domain.FomodChoices{"fake:wizard": {Steps: []domain.FomodStepChoice{
		{Name: "Size", Groups: []domain.FomodGroupChoice{{Name: "Texture", Choices: []domain.FomodOptionChoice{{Name: "4K", Idx: 1}}}}},
		{Name: "Extras", Groups: []domain.FomodGroupChoice{{Name: "Docs", Choices: []domain.FomodOptionChoice{{Name: "Readme", Idx: 0}}}}},
	}}}}, nil)
	require.NoError(t, err)
	require.Equal(t, "4K", e2eDeployed(t, f.Game, "Mods/t.dds"))
	require.Equal(t, "README", e2eDeployed(t, f.Game, "Mods/readme.txt"))

	var preselected4K bool
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "wizard")),
		clickWhenSettled(`[data-action="reconfigure-fomod"]`),
		chromedp.WaitVisible(`.modal[data-kind="mod_fomod"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"][data-step="Size"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('`+fomodModal+` [data-option="4K"] input').checked`, &preselected4K),
		clickWhenSettled(fomodModal+` [data-action="fomod-defaults"]`),
		waitGone(fomodModal),
	)
	assert.True(t, preselected4K, "the recording is the preselection")
	require.Eventually(t, func() bool { return e2eDeployed(t, f.Game, "Mods/t.dds") == "2K" }, 30*time.Second, e2ePollInterval)
	assert.Empty(t, e2eDeployed(t, f.Game, "Mods/readme.txt"), "the installer's defaults hide the Extras step")
	var reopened bool
	require.NoError(t, chromedp.Run(f.Ctx, chromedp.Evaluate(`document.querySelector('`+fomodModal+`') !== null`, &reopened)))
	assert.False(t, reopened, "one click, one round: nothing asked again")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FomodWizard_ACollectionWithStaleRecordedChoices: a collection
// picked in the profiles modal shows `requires: fomod` and its recorded
// installer choices in the plan; its choices no longer fit the installer,
// so the import comes back with the (stale) question, the wizard asks it,
// and its answer - re-posted over the same file, uploaded again - imports
// the profile with the new choices (#615 web parity).
func TestE2E_FomodWizard_ACollectionWithStaleRecordedChoices(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod:      domain.Mod{ID: "wizard", SourceID: "fake", Name: "Wizard Mod", Version: "1.0"},
		files:    []domain.DownloadableFile{{ID: "w1", Name: "Main", FileName: "wizard.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512}},
		archives: map[string][]byte{"w1": e2eFomodArchive(t)},
	})
	doc := "lmm_collection: 1\nname: Wizard Pack\nrequires: [fomod]\nmods:\n" +
		"  - {source_id: fake, mod_id: wizard, version: \"1.0\", fomod: {config_sha256: " + strings.Repeat("ab", 32) +
		", steps: [{name: Size, groups: [{name: Texture, choices: [{name: Gone, idx: 0}]}]}]}}\n"
	importPath := filepath.Join(t.TempDir(), "pack.yaml")
	require.NoError(t, os.WriteFile(importPath, []byte(doc), 0o644))

	var plan string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil),
		chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.SetUploadFiles(`.profiles-import input[type="file"]`, []string{importPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] [data-testid="collection-installer-choices"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_import"]`, &plan),
	)
	assert.Contains(t, plan, "Requires: fomod")
	assert.Contains(t, plan, "Size > Texture: Gone")
	assert.Contains(t, plan, "Replayed as written")

	var stale string
	f.runInBrowser(t,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('.modal[data-kind="profile_import"] label.plan__control'))
				.find((l) => l.textContent.includes("Download and install"))?.querySelector("input").click();
		`, nil),
		settleEffects(),
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"][data-mod="fake:wizard"]`, chromedp.ByQuery),
		textContent(fomodModal, &stale),
		chromedp.Click(fomodModal+` [data-option="2K"] input`, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-action="fomod-finish"]`),
		waitGone(fomodModal),
	)
	assert.Contains(t, stale, "no longer fit", "the wizard says the recorded choices are stale")
	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Wizard Pack")
		if err != nil {
			return false
		}
		ref := p.FindRef("fake", "wizard")
		return ref != nil && len(ref.Fomod.Steps) > 0 && len(ref.Fomod.Steps[0].Groups) > 0 &&
			len(ref.Fomod.Steps[0].Groups[0].Choices) == 1 && ref.Fomod.Steps[0].Groups[0].Choices[0].Name == "2K"
	}, 30*time.Second, e2ePollInterval, "the profile records the answered choices")
	assert.Empty(t, f.BrowserErrors())
}

// e2eFomodArchiveFile writes the wizard's installer archive as name.
func e2eFomodArchiveFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, e2eFomodArchive(t), 0o644))
	return path
}

// TestE2E_FomodWizard_InstallFromFile: an install a source refuses is
// finished from a downloaded file (import_archive) that is a FOMOD
// installer: the job asks, the wizard answers, and its Finish re-posts the
// same upload - which the failed Apply kept - and the mod installs (#615
// review blocker: the re-post used to demand a re-upload it could not do).
func TestE2E_FomodWizard_InstallFromFile(t *testing.T) {
	f := newE2EFixtureWithManualInstall(t)
	archive := e2eFomodArchiveFile(t, "MapUtils-1.2.24-classic (1).zip")
	row := searchResultRow("fake", "maputils")

	f.runInBrowser(t,
		failSearchInstall(f),
		clickWhenSettled(row+` .job-progress [data-action="install-from-file"]`),
		chromedp.SetUploadFiles(e2eFromFileInput, []string{archive}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="import_archive"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"][data-step="Size"]`, chromedp.ByQuery),
		chromedp.Click(fomodModal+` [data-option="4K"] input`, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-action="fomod-finish"]`),
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"][data-step="Extras"]`, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-action="fomod-finish"]`),
		waitGone(fomodModal),
	)
	require.Eventually(t, func() bool { return e2eDeployed(t, f.Game, "Mods/t.dds") == "4K" }, 30*time.Second, e2ePollInterval)
	assert.Equal(t, "README", e2eDeployed(t, f.Game, "Mods/readme.txt"))
	mod, err := f.Svc.GetInstalledMod(t.Context(), "fake", "maputils", f.Game.ID, "default")
	require.NoError(t, err)
	assert.NotEmpty(t, mod.Variant, "installed as the answered tree")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FomodWizard_UpdateFromFile: an update finished from a downloaded
// file (update_from_archive) whose new version is a FOMOD installer asks,
// and the wizard's answer re-posts the kept upload and updates the mod.
func TestE2E_FomodWizard_UpdateFromFile(t *testing.T) {
	f := newE2EFixtureWithManualUpdate(t)
	archive := e2eFomodArchiveFile(t, "ManualUp-2.0 (1).zip")

	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "manualup")),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.slide-over__actions`, chromedp.ByQuery),
		failManualUpdate(),
		clickWhenSettled(`.slide-over .job-progress [data-testid="download-page"] [data-action="update-from-file"]`),
		chromedp.SetUploadFiles(e2eFromFileInput, []string{archive}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="update_from_archive"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"][data-step="Size"]`, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-action="fomod-defaults"]`),
		waitGone(fomodModal),
	)
	require.Eventually(t, func() bool {
		row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "manualup", f.Game.ID, "default")
		return err == nil && row.Version == "2.0"
	}, 30*time.Second, e2ePollInterval, "the update applied")
	assert.Equal(t, "2K", e2eDeployed(t, f.Game, "Mods/t.dds"), "the installer's defaults")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FomodWizard_ANewModalKeepsFocusFromTheOldOpener: closing the
// reconfigure plan arms its opener's focus return, and the wizard opens
// inside that window. Focus that falls to <body> while the wizard is open
// must not jump to "Reconfigure installer…" behind its scrim, where Enter
// would fire it (#615 review).
func TestE2E_FomodWizard_ANewModalKeepsFocusFromTheOldOpener(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod:      domain.Mod{ID: "wizard", SourceID: "fake", Name: "Wizard Mod", Version: "1.0"},
		files:    []domain.DownloadableFile{{ID: "w1", Name: "Main", FileName: "wizard.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512}},
		archives: map[string][]byte{"w1": e2eFomodArchive(t)},
	})
	plan, err := f.Svc.PlanInstall(t.Context(), f.Game, "default", "fake", "wizard", false)
	require.NoError(t, err)
	_, err = f.Svc.ApplyInstall(t.Context(), f.Game, plan, core.InstallOptions{Fomod: map[string]domain.FomodChoices{"fake:wizard": {Steps: []domain.FomodStepChoice{
		{Name: "Size", Groups: []domain.FomodGroupChoice{{Name: "Texture", Choices: []domain.FomodOptionChoice{{Name: "2K", Idx: 0}}}}},
	}}}}, nil)
	require.NoError(t, err)

	var stolen bool
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "wizard")),
		clickWhenSettled(`[data-action="reconfigure-fomod"]`),
		chromedp.WaitVisible(`.modal[data-kind="mod_fomod"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"]`, chromedp.ByQuery),
		// Focus falls to <body> while the wizard is open, inside the
		// closed plan modal's refocus window.
		chromedp.Evaluate(`document.activeElement && document.activeElement.blur()`, nil),
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Evaluate(`document.activeElement !== null && document.activeElement.matches('[data-action="reconfigure-fomod"]')`, &stolen),
	)
	assert.False(t, stolen, "focus stayed off the control behind the wizard's scrim")
	assert.Empty(t, f.BrowserErrors())
}

// openerWatchJS installs window.__openerWatch on the page: replaced turns
// true the moment the node sel names now leaves the document (a re-render
// that unmounted it and mounted a new one), and detailReads/inflight count
// the reads of detailPath (a mod's detail document, not its /files or
// /versions) issued from here on and the fetches still outstanding, so a
// test can wait for a re-read to have landed.
func openerWatchJS(sel, detailPath string) string {
	return fmt.Sprintf(`(() => {
		const el = document.querySelector(%q);
		const watch = { replaced: false, detailReads: 0, inflight: 0 };
		window.__openerWatch = watch;
		new MutationObserver(() => {
			if (!el.isConnected) watch.replaced = true;
		}).observe(document.body, { childList: true, subtree: true });
		const fetchFn = window.fetch;
		window.fetch = (input, init) => {
			const path = new URL(String(input), location.href).pathname;
			const detail = path === %q;
			watch.inflight++;
			return fetchFn(input, init).finally(() => {
				watch.inflight--;
				if (detail) watch.detailReads++;
			});
		};
	})()`, sel, detailPath)
}

// stableFocusJS is truthy once sel has held focus on two consecutive
// checks: the first records the focused node, the next (a poll interval
// later) confirms the same node still matches. A focus that blinks away
// and back between two checks resets it.
func stableFocusJS(sel string) string {
	return fmt.Sprintf(`(() => {
		const el = document.activeElement;
		const ok = el !== null && el.matches(%q);
		const prev = window.__stableFocus;
		window.__stableFocus = ok ? el : null;
		return ok && prev === el;
	})()`, sel)
}
