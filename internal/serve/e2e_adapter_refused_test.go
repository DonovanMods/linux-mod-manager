package serve_test

// #449: a game whose adapter core refuses shows as refused - the configured
// adapter, marked, with core's sentence - in the Setup > Games cell and the
// game's loader panel, never as the generic-files it does not use.

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_SetupGames_ARefusedAdapterShowsAsRefused(t *testing.T) {
	f := newE2EFixture(t)
	f.Game.Adapter = "no-such-adapter"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	var cell, panel string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup?section=games"),
		chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
		// The Adapter column, by position: the cell as it stood before #449
		// read "generic-files" here.
		textContent(`[data-testid="setup-games"] tbody tr td:nth-child(4)`, &cell),
	)
	// The cell LEADS with the adapter it names; core's sentence below it
	// lists the registered adapters, generic-files among them.
	assert.True(t, strings.HasPrefix(cell, "no-such-adapter refused"),
		"a refused game names its configured adapter, marked refused - not the generic-files it does not use: %q", cell)
	assert.Contains(t, cell, `unknown adapter "no-such-adapter"`, "core's sentence, where the adapter is shown")

	f.runInBrowser(t,
		chromedp.Click(`button[data-action="edit-loader"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="loader-panel"] [data-testid="adapter-error"]') !== null`),
		textContent(`[data-testid="loader-panel"] [data-testid="adapter-error"]`, &panel),
	)
	assert.Contains(t, panel, `unknown adapter "no-such-adapter"`)
	assert.Contains(t, strings.ToLower(panel), "lmm game edit "+f.Game.ID+" --adapter")
	assert.Empty(t, f.BrowserErrors())
}
