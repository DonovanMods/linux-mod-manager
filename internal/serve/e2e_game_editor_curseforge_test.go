package serve_test

// #544: the game editor sets and clears a CurseForge game's accepted game
// version types, and only a game that maps CurseForge shows the field.
// Asserted against games.yaml, the end state.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

func TestE2E_GameEditor_SetsAndClearsCurseForgeVersionTypes(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	field := editorSel(`input[name="curseforge-types"]`)

	var valheimHasField bool
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("valheim"),
		chromedp.Evaluate(`document.querySelector('[data-testid="game-editor"] input[name="curseforge-types"]') !== null`, &valheimHasField),
		clickWhenSettled(editorSel(`[data-action="cancel-edit-game"]`)),
		pollUntil(`!document.querySelector('[data-testid="game-editor"]')`),
		openEditor("drive"),
		typeInto(field, "67408, 77522"),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
	)
	assert.False(t, valheimHasField, "a game without CurseForge has no CurseForge flavor to pick")

	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	require.NotNil(t, onDisk["drive"].CurseForge)
	assert.Equal(t, []int{67408, 77522}, onDisk["drive"].CurseForge.GameVersionTypes)

	f.runInBrowser(t,
		chromedp.Evaluate(countPUTs, nil),
		chromedp.Evaluate(`(() => {
			const i = document.querySelector('[data-testid="game-editor"] input[name="curseforge-types"]');
			i.value = "";
			i.dispatchEvent(new Event("input", { bubbles: true }));
		})()`, nil),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		pollUntil(`window.__puts === 1 &&
			document.querySelector('[data-testid="game-editor"] [data-action="save-game"]').textContent.trim() === "Save" &&
			document.querySelector('[data-testid="game-editor"] [data-testid="game-saved"]') !== null`),
	)
	onDisk, err = config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	assert.Nil(t, onDisk["drive"].CurseForge, "a blank field clears the setting")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// A value that is not an id is refused on the field, and nothing is written.
func TestE2E_GameEditor_ACurseForgeTypoMarksTheField(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	field := editorSel(`input[name="curseforge-types"]`)

	var invalid string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("drive"),
		typeInto(field, "classic"),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-field-error"]`), chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-testid="game-editor"] input[name="curseforge-types"]').getAttribute("aria-invalid") ?? ""`, &invalid),
	)
	assert.Equal(t, "true", invalid)

	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	assert.Nil(t, onDisk["drive"].CurseForge)
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/games/drive")
}
