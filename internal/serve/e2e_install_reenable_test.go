package serve_test

// #448: installing a mod whose dependency the profile has switched off
// switches that dependency back on - and the install confirmation says so
// before Confirm, as the CLI does.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func TestE2E_InstallPlan_ListsTheDependenciesItSwitchesBackOn(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "needy", SourceID: "fake", Name: "Needy Mod", Version: "1.0"},
		files:   []domain.DownloadableFile{{ID: "n1", Name: "Main", FileName: "needy.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 16}},
		members: map[string]string{"n1": "Mods/needy.pak"},
	})
	f.Src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "helper", SourceID: "fake", Name: "Helper Mod", Version: "1.0"},
		files:   []domain.DownloadableFile{{ID: "h1", Name: "Main", FileName: "helper.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 16}},
		members: map[string]string{"h1": "Mods/helper.pak"},
	})
	f.Src.deps = map[string][]domain.ModReference{"needy": {{SourceID: "fake", ModID: "helper"}}}
	// The profile lists Helper, switched off - an imported profile, say.
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "helper", Version: "1.0", Disabled: true}))

	row := searchResultRow("fake", "needy")
	var section string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "needy", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="install-reenabled"]') !== null`),
		textContent(`[data-testid="install-reenabled"]`, &section),
	)
	assert.Contains(t, section, "Switched back on (1)")
	assert.Contains(t, section, "Helper Mod")
	assert.Contains(t, section, "switched off")
	assert.Contains(t, section, "Needy Mod can't work without it")
	assert.Empty(t, f.BrowserErrors())
}
