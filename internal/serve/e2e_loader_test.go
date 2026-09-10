package serve_test

import (
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_SetupGames_DeclareALoaderAndReadTheLaunchOption is the browser half
// of issue 359's web surface, and it is E2E because every claim in it is
// something only a browser computes: that the loader editor's selects reach
// the PUT, that the panel below re-reads the game directory afterwards, and
// that the launch option lmm will not paste for you actually appears on
// screen where a user can copy it.
//
// It drives the real Setup > Games row controls rather than the API, because
// the API half is already covered by api_games_loader_internal_test.go - what
// is untested until a browser runs is the wiring between them.
func TestE2E_SetupGames_DeclareALoaderAndReadTheLaunchOption(t *testing.T) {
	f := newE2EFixture(t)

	// The game directory is what the server reads the bootstrap off: a
	// Windows Unity build, which on Linux runs under Proton.
	writeE2EGameMarkers(t, f.Game.InstallPath, "UnityPlayer.dll")

	var cell, launchOption string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup"),
		chromedp.WaitVisible(`.setup-page`, chromedp.ByQuery),
		chromedp.Click(`.setup-nav__tab[data-section="games"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),

		// No loader yet.
		chromedp.Text(`[data-testid="loader-cell"]`, &cell, chromedp.ByQuery),

		chromedp.Click(`button[data-action="edit-loader"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="loader-editor"]`, chromedp.ByQuery),
		chromedp.SetValue(`select[name="loader-kind"]`, "bepinex", chromedp.ByQuery),
		chromedp.WaitVisible(`input[name="loader-version"]`, chromedp.ByQuery),
		chromedp.SendKeys(`input[name="loader-version"]`, "5.4.23.5", chromedp.ByQuery),
		chromedp.Click(`button[data-action="save-loader"]`, chromedp.ByQuery),

		// The row re-reads, and the panel below it re-reads the game
		// directory - which is where the launch option comes from.
		chromedp.Poll(`document.querySelector('[data-testid="loader-cell"]')?.textContent.trim() === "bepinex"`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		chromedp.WaitVisible(`[data-testid="launch-option"]`, chromedp.ByQuery),
		chromedp.Text(`[data-testid="launch-option"]`, &launchOption, chromedp.ByQuery),
	)

	assert.Equal(t, "—", cell, "a game with no declaration shows an em dash, not an empty cell")
	assert.Equal(t, `WINEDLLOVERRIDES="winhttp=n,b" %command%`, launchOption,
		"the Proton launch option is detected from the game directory and rendered verbatim")
	assert.Empty(t, f.BrowserErrors())

	// And it reached games.yaml, not just the DOM.
	game, err := f.Svc.GetGame(f.Game.ID)
	require.NoError(t, err)
	require.NotNil(t, game.Loader)
	assert.Equal(t, "5.4.23.5", game.Loader.Version)
}

// TestE2E_SetupGames_LoaderPanelReportsWhatIsMissing: the panel is honest
// about a game whose loader is declared nowhere and installed nowhere. It
// says the preloader is absent rather than rendering a facts list that
// implies a working setup - which is the one thing a status panel must never
// do, because "configured" and "working" are exactly what this feature exists
// to keep apart.
func TestE2E_SetupGames_LoaderPanelReportsWhatIsMissing(t *testing.T) {
	f := newE2EFixture(t)
	writeE2EGameMarkers(t, f.Game.InstallPath, "UnityPlayer.so")

	var installed string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup"),
		chromedp.WaitVisible(`.setup-page`, chromedp.ByQuery),
		chromedp.Click(`.setup-nav__tab[data-section="games"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
		chromedp.Click(`button[data-action="edit-loader"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="loader-panel"]`, chromedp.ByQuery),
		chromedp.Text(`[data-testid="loader-installed"]`, &installed, chromedp.ByQuery),
	)

	assert.Contains(t, installed, "no",
		"the panel says the preloader is absent rather than implying a working setup")
	assert.Empty(t, f.BrowserErrors())
}
