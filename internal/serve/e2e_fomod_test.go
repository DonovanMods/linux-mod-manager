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
