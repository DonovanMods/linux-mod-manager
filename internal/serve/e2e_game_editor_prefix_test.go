package serve_test

// #605: the game editor's Wine/Proton prefix fields. A refused prefix marks
// the prefix field (core's GameSpecError names it), and a valid one saves
// both fields to games.yaml - asserted on disk, not just in the DOM.

import (
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

func TestE2E_GameEditor_SetsThePrefix(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	pfx := filepath.Join(t.TempDir(), "Prefixes", "valheim")

	var invalid, fieldError string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("valheim"),
		typeInto(editorSel(`input[name="prefix"]`), "relative/pfx"),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-field-error"]`), chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-testid="game-editor"] input[name="prefix"]').getAttribute("aria-invalid") ?? ""`, &invalid),
		textContent(editorSel(`.game-editor__field:has(input[name="prefix"]) [data-testid="game-field-error"]`), &fieldError),

		typeInto(editorSel(`input[name="prefix"]`), pfx),
		typeInto(editorSel(`input[name="prefix-user"]`), "steamuser"),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
	)
	assert.Equal(t, "true", invalid, "the prefix field is the one marked")
	assert.Contains(t, fieldError, "absolute")

	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	require.NotNil(t, onDisk["valheim"])
	assert.Equal(t, pfx, onDisk["valheim"].Prefix)
	assert.Equal(t, "steamuser", onDisk["valheim"].PrefixUser)
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/games/valheim")
}
