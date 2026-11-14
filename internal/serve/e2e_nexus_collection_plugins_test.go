package serve_test

// #611: a NexusMods collection's plugin order in the browser - the plan
// view summarises the plugin list the import saves, says lmm writes it at
// every deploy (a game the bethesda adapter manages), lists the load order
// behind a disclosure, and the import saves it on the profile.

import (
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const e2eNexusPluginRecipe = `{"Name": "Three Mods", "Mods": [
	{"Name": "Ten", "Version": "1.0", "SourceType": "nexus", "ModID": "10", "FileID": "100", "UpdatePolicy": "exact"}],
	"Plugins": [{"Name": "Ten.esp", "Enabled": true}, {"Name": "Ten - Extra.esp", "Enabled": false}],
	"PluginRules": {"Plugins": [{"Name": "Ten - Extra.esp", "Group": "Late", "After": ["Ten.esp"]}],
		"Groups": [{"Name": "Late", "After": ["default"]}]}}`

func TestE2E_NexusCollection_PlanShowsThePluginOrder(t *testing.T) {
	f := newE2EFixture(t)
	app.RegisterAdapters(f.Svc)
	f.Svc.RegisterSource(e2eNexusSource{newFakeSource("nexusmods")})
	f.Game.SourceIDs["nexusmods"] = "skyrimspecialedition"
	f.Game.Adapter, f.Game.Catalog = "bethesda", "skyrim-se"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	seedE2ENexusRecipe(t, f, "plugins", e2eNexusPluginRecipe)

	var summary, applied, order string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openNexusImport("plugins"),
		chromedp.WaitVisible(`[data-testid="collection-plugins"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-plugins-summary"]`, &summary),
		textContent(`[data-testid="collection-plugins-applied"]`, &applied),
		// The load order is behind a disclosure, closed by default.
		pollUntil(`document.querySelector('[data-testid="collection-plugins"] details')?.open === false`),
		clickWhenSettled(`[data-testid="collection-plugins"] summary`),
		chromedp.WaitVisible(`[data-testid="collection-plugins-order"]`, chromedp.ByQuery),
		textContent(`[data-testid="collection-plugins-order"]`, &order),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	)
	assert.Contains(t, summary, "2 plugins, 1 active; 1 plugin rule, groups: Late.")
	assert.Contains(t, applied, "written to the game's plugin list at every deploy")
	assert.Contains(t, order, "1. Ten.esp")
	assert.Contains(t, order, "2. Ten - Extra.esp (inactive)")

	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Three Mods")
		return err == nil && len(p.Plugins.Order) == 2
	}, 15*time.Second, 50*time.Millisecond, "the import saves the plugin list on the profile")
	p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, []domain.PluginEntry{{Name: "Ten.esp", Enabled: true}, {Name: "Ten - Extra.esp"}}, p.Plugins.Order)
	assert.Empty(t, f.BrowserErrors())
}
