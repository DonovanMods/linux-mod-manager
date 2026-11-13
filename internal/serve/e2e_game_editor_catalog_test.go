package serve_test

// #606 owner request: the game editor's "Game catalog entry" select says
// which known game an install is. Its choices are lmm's game catalog (GET
// /api/v1/games/known); choosing one saves `catalog:` to games.yaml, and
// choosing "Not set" clears it - asserted on disk, not just in the DOM.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

func TestE2E_GameEditor_SetsTheCatalogEntry(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	f.Svc.SetKnownGames(func() []core.KnownGame {
		return []core.KnownGame{
			{Slug: "valheim", Name: "Valheim", SteamAppID: "892970", Adapter: "bepinex"},
			{Slug: "skyrim-se", Name: "Skyrim Special Edition", SteamAppID: "489830", Adapter: "bethesda"},
		}
	})

	var options []string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("valheim"),
		pollUntil(`document.querySelectorAll('[data-testid="game-editor"] select[name="catalog"] option').length === 3`),
		chromedp.Evaluate(`[...document.querySelectorAll('[data-testid="game-editor"] select[name="catalog"] option')].map(o => o.textContent.trim())`, &options),
		chromedp.SetValue(editorSel(`select[name="catalog"]`), "valheim", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-testid="game-editor"] select[name="catalog"]').dispatchEvent(new Event("change", {bubbles: true}))`, nil),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
	)
	require.Len(t, options, 3)
	assert.Contains(t, options[0], "Not set")
	assert.Equal(t, []string{"Skyrim Special Edition (skyrim-se)", "Valheim (valheim)"}, options[1:], "the catalog, by name")

	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	require.NotNil(t, onDisk["valheim"])
	assert.Equal(t, "valheim", onDisk["valheim"].Catalog)

	f.runInBrowser(t,
		chromedp.SetValue(editorSel(`select[name="catalog"]`), "", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-testid="game-editor"] select[name="catalog"]').dispatchEvent(new Event("change", {bubbles: true}))`, nil),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		pollUntil(`document.querySelector('[data-testid="game-saved"]') !== null`),
	)
	onDisk, err = config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	assert.Empty(t, onDisk["valheim"].Catalog, "Not set clears it")
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/games/valheim")
}
