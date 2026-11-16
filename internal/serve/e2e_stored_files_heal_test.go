package serve_test

// #662 in the browser: the install picker offers another version's
// optional file beside the chosen version's files, labelled with its
// version, and lists only the main files' versions; and the profile apply
// plan names a mod whose stored main files it heals to one version.

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// mixedVersionSourceMod is "Mixed Mod": 2.0's main file, 1.0's main file,
// a 1.0 optional patch and a 0.3 update of its own version.
func mixedVersionSourceMod() e2eSearchSourceMod {
	return e2eSearchSourceMod{
		mod: domain.Mod{ID: "mixed", SourceID: "fake", Name: "Mixed Mod", Version: "2.0"},
		files: []domain.DownloadableFile{
			{ID: "x2", Name: "Main 2.0", FileName: "mixed-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 64},
			{ID: "x1", Name: "Main 1.0", FileName: "mixed-1.0.zip", Version: "1.0", Category: "MAIN", Size: 64},
			{ID: "xp", Name: "Patch", FileName: "mixed-patch-1.0.zip", Version: "1.0", Category: "OPTIONAL", Size: 64},
			{ID: "xu", Name: "Hotfix", FileName: "mixed-hotfix-0.3.zip", Version: "0.3", Category: "UPDATE", Size: 64},
		},
		members: map[string]string{"x2": "Mods/mixed.pak", "x1": "Mods/mixed.pak", "xp": "Mods/mixed-patch.pak", "xu": "Mods/mixed-hotfix.pak"},
	}
}

func TestE2E_InstallPicker_OffersAnotherVersionsOptionalFile(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(mixedVersionSourceMod())
	row := searchResultRow("fake", "mixed")

	var versions, offered []string
	var patchLabel string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "Mixed", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		clickWhenSettled(row+" .search-result__install"),
		chromedp.WaitVisible(installFilesPicker, chromedp.ByQuery),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('.modal[data-kind="install"] select[name="install-version"] option')).map(o => o.value)`, &versions),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('`+installFilesPicker+` input[name="install-file"]')).map(i => i.value)`, &offered),
		textContent(installFilesPicker+` label:has(input[value="xp"])`, &patchLabel),
		clickWhenSettled(installFileBox("xp")),
		pollUntil(`document.querySelectorAll('`+installFilesList+` li').length === 2`),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		chromedp.WaitVisible(row+` .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)
	assert.Equal(t, []string{"2.0", "1.0"}, versions, "only the main files' versions are a choice")
	assert.Equal(t, []string{"x2", "xp", "xu"}, offered, "2.0's file, then the other versions' secondary files")
	assert.Contains(t, patchLabel, "version 1.0")

	installed, err := f.Svc.GetInstalledMod(t.Context(), "fake", "mixed", f.Game.ID, "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"x2", "xp"}, installed.FileIDs)
	assert.Equal(t, "2.0", installed.Version)
	assert.Equal(t, "payload for mixed/xp", e2eDeployed(t, f.Game, "Mods/mixed-patch.pak"))
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ProfileApplyPlan_NamesTheHeal(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(mixedVersionSourceMod())
	pm := f.Svc.NewProfileManager()
	require.NoError(t, pm.AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "mixed", Version: "2.0", FileIDs: []string{"x1", "x2", "xp"}}))

	var heals string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="apply-profile"]`),
		chromedp.WaitVisible(`.modal[data-kind="profile_apply"] [data-testid="profile-apply-heals"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_apply"] [data-testid="profile-apply-heals"]`, &heals),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)
	assert.Contains(t, heals, "Mixed Mod")
	assert.Contains(t, heals, "keeps version 2.0, drops mixed-1.0.zip (1.0)")

	assert.Eventually(t, func() bool {
		p, err := pm.Get(t.Context(), f.Game.ID, "default")
		if err != nil {
			return false
		}
		ref := p.FindRef("fake", "mixed")
		return ref != nil && len(ref.FileIDs) == 2 && ref.FileIDs[0] == "x2" && ref.FileIDs[1] == "xp"
	}, 10*time.Second, 50*time.Millisecond, "the apply records the healed ids")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_InstallPicker_SeparateComponentsGoWithAnyVersion (#662 review):
// Core's two copies are the only version choice; Textures 1.0, a separate
// main component, is offered beside Core 1.2 and installs with it.
func TestE2E_InstallPicker_SeparateComponentsGoWithAnyVersion(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: "parts", SourceID: "fake", Name: "Parted Mod", Version: "1.2"},
		files: []domain.DownloadableFile{
			{ID: "c12", Name: "Core", FileName: "Core-1.2.zip", Version: "1.2", Category: "MAIN", IsPrimary: true, Size: 64},
			{ID: "c11", Name: "Core", FileName: "Core-1.1.zip", Version: "1.1", Category: "MAIN", Size: 64},
			{ID: "t10", Name: "Textures", FileName: "Textures-1.0.zip", Version: "1.0", Category: "MAIN", Size: 64},
		},
		members: map[string]string{"c12": "Mods/core.pak", "c11": "Mods/core.pak", "t10": "Mods/textures.pak"},
	})
	row := searchResultRow("fake", "parts")

	var versions, offered []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "Parted", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		clickWhenSettled(row+" .search-result__install"),
		chromedp.WaitVisible(installFilesPicker, chromedp.ByQuery),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('.modal[data-kind="install"] select[name="install-version"] option')).map(o => o.value)`, &versions),
		chromedp.Evaluate(`Array.from(document.querySelectorAll('`+installFilesPicker+` input[name="install-file"]')).map(i => i.value)`, &offered),
		clickWhenSettled(installFileBox("t10")),
		pollUntil(`document.querySelectorAll('`+installFilesList+` li').length === 2`),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		chromedp.WaitVisible(row+` .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)
	assert.Equal(t, []string{"1.2", "1.1"}, versions, "a separate component's version is no choice")
	assert.Equal(t, []string{"c12", "t10"}, offered)
	installed, err := f.Svc.GetInstalledMod(t.Context(), "fake", "parts", f.Game.ID, "default")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"c12", "t10"}, installed.FileIDs)
	assert.Equal(t, "1.2", installed.Version)
	assert.Empty(t, f.BrowserErrors())
}
