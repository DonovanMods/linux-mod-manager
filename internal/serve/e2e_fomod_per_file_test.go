package serve_test

// #634: the FOMOD wizard for a mod whose files each carry their own
// installer, in a real browser. "Reconfigure installer…" lists the
// installers to pick from; "Every installer" walks one labelled question
// per file and re-posts the answers keyed per file; picking one file asks
// only that file's installer and leaves the other's choices alone.

import (
	"archive/zip"
	"bytes"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

const e2ePatchConfig = `<config><moduleName>Patch Installer</moduleName>
<installSteps order="Explicit"><installStep name="Patches"><optionalFileGroups order="Explicit">
<group name="Which" type="SelectExactlyOne"><plugins order="Explicit">
<plugin name="USSEP"><description>For USSEP.</description><files><file source="p\ussep.esp" destination="Mods\bugs_patch.esp"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
<plugin name="Vanilla"><description>Without USSEP.</description><files><file source="p\vanilla.esp" destination="Mods\bugs_patch.esp"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
</plugins></group></optionalFileGroups></installStep></installSteps></config>`

func e2ePatchArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"fomod/ModuleConfig.xml": e2ePatchConfig,
		"p/ussep.esp":            "USSEP",
		"p/vanilla.esp":          "VANILLA",
	} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestE2E_FomodWizard_TwoInstallersOnePerFile(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	files := []domain.DownloadableFile{
		{ID: "b1", Name: "Main", FileName: "bugs.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512},
		{ID: "b2", Name: "Patches", FileName: "bugs-patches.zip", Version: "1.0", Category: "OPTIONAL", Size: 512},
	}
	f.Src.addMod(e2eSearchSourceMod{
		mod:      domain.Mod{ID: "bugs", SourceID: "fake", Name: "Scrambled Bugs", Version: "1.0"},
		files:    files,
		archives: map[string][]byte{"b1": e2eFomodArchive(t), "b2": e2ePatchArchive(t)},
	})
	// Installed with both files, each installer answered (the web UI's
	// install picker takes one file; a collection brings both).
	ctx := t.Context()
	plan, err := f.Svc.PlanInstall(ctx, f.Game, "default", "fake", "bugs", false)
	require.NoError(t, err)
	plan.Files = files
	_, err = f.Svc.ApplyInstall(ctx, f.Game, plan, core.InstallOptions{TargetFileIDs: []string{"b1", "b2"}, Fomod: map[string]domain.FomodChoices{
		domain.FomodFileKey("fake", "bugs", "b1"): {Steps: []domain.FomodStepChoice{{Name: "Size", Groups: []domain.FomodGroupChoice{{Name: "Texture", Choices: []domain.FomodOptionChoice{{Name: "2K", Idx: 0}}}}}}},
		domain.FomodFileKey("fake", "bugs", "b2"): {Steps: []domain.FomodStepChoice{{Name: "Patches", Groups: []domain.FomodGroupChoice{{Name: "Which", Choices: []domain.FomodOptionChoice{{Name: "USSEP", Idx: 0}}}}}}},
	}}, nil)
	require.NoError(t, err)
	require.Equal(t, "2K", e2eDeployed(t, f.Game, "Mods/t.dds"))
	require.Equal(t, "USSEP", e2eDeployed(t, f.Game, "Mods/bugs_patch.esp"))

	const picker = `.modal[data-kind="mod_fomod"] [data-testid="fomod-installer-picker"]`
	mainStep := fomodModal + ` [data-testid="fomod-step"][data-file="b1"]`
	patchStep := fomodModal + ` [data-testid="fomod-step"][data-file="b2"]`

	// Every installer: one labelled page per file, answers keyed per file.
	var everyChecked bool
	var mainTitle, patchTitle string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "bugs")),
		clickWhenSettled(`[data-action="reconfigure-fomod"]`),
		chromedp.WaitVisible(picker, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('`+picker+` [data-installer=""] input').checked`, &everyChecked),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		chromedp.WaitVisible(mainStep, chromedp.ByQuery),
		chromedp.Text(mainStep+` .fomod__mod`, &mainTitle, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-action="fomod-next"]`),
		chromedp.WaitVisible(patchStep, chromedp.ByQuery),
		chromedp.Text(patchStep+` .fomod__mod`, &patchTitle, chromedp.ByQuery),
		chromedp.Click(patchStep+` [data-option="Vanilla"] input`, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-action="fomod-finish"]`),
		waitGone(fomodModal),
	)
	assert.True(t, everyChecked, "every installer is the default pick")
	assert.Contains(t, mainTitle, "Scrambled Bugs - Main")
	assert.Contains(t, patchTitle, "Scrambled Bugs - Patches")
	require.Eventually(t, func() bool { return e2eDeployed(t, f.Game, "Mods/bugs_patch.esp") == "VANILLA" }, 30*time.Second, e2ePollInterval)
	assert.Equal(t, "2K", e2eDeployed(t, f.Game, "Mods/t.dds"))

	// One file: only its installer asks; the other keeps its choices.
	var mainAsked bool
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "bugs")),
		clickWhenSettled(`[data-action="reconfigure-fomod"]`),
		chromedp.WaitVisible(picker, chromedp.ByQuery),
		clickWhenSettled(picker+` [data-installer="b2"] input`),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		chromedp.WaitVisible(patchStep, chromedp.ByQuery),
		chromedp.Evaluate(`Boolean(document.querySelector('`+mainStep+`'))`, &mainAsked),
		chromedp.Click(patchStep+` [data-option="USSEP"] input`, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-action="fomod-finish"]`),
		waitGone(fomodModal),
	)
	assert.False(t, mainAsked, "the main file's installer is not asked")
	require.Eventually(t, func() bool { return e2eDeployed(t, f.Game, "Mods/bugs_patch.esp") == "USSEP" }, 30*time.Second, e2ePollInterval)
	assert.Equal(t, "2K", e2eDeployed(t, f.Game, "Mods/t.dds"))
	p, err := f.Svc.NewProfileManager().Get(ctx, f.Game.ID, "default")
	require.NoError(t, err)
	ref := p.FindRef("fake", "bugs")
	require.NotNil(t, ref)
	assert.Len(t, ref.FomodFiles, 2)
	assert.Empty(t, f.BrowserErrors())
}
