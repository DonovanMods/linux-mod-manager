package serve_test

// A game whose adapter core refuses, driven through the SPA (#413): the
// single-step enable toggle is a deploy, so it fails with core's own
// refusal - rendered where the user clicked - while nothing on disk or in
// the library moves.

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_EnableOnARefusedGame_ShowsTheRefusal: the slide-over's Enable
// button on a refused game resurfaces the refusal inline, and the mod stays
// disabled.
func TestE2E_EnableOnARefusedGame_ShowsTheRefusal(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)
	_, err := f.Svc.DisableMod(t.Context(), f.Game, "default", "fake", "a")
	require.NoError(t, err)
	f.Game.Adapter = "no-such-adapter"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		clickModRow("Alpha Mod"),
		chromedp.WaitVisible(`.slide-over__nav`, chromedp.ByQuery),
	)
	var label string
	f.runInBrowser(t, textContent(`.slide-over__actions button`, &label))
	require.Equal(t, "Enable", label, "Alpha Mod was disabled before the refusal")

	var inline string
	f.runInBrowser(t,
		chromedp.Click(`.slide-over__actions button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="failed"]`, chromedp.ByQuery),
		textContent(`.job-progress__text`, &inline),
	)
	assert.Contains(t, inline, "Failed")
	assert.Contains(t, inline, `unknown adapter "no-such-adapter"`, "the refusal core gives, where the user clicked")

	mod, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled)
	// The conflicts card cannot compute conflicts through a refused
	// adapter either; it says so on the card ("Couldn't check for
	// conflicts: <the refusal>"), and its failed fetch is the one expected
	// console line. Nothing else may error.
	var unexpected []string
	for _, e := range f.BrowserErrors() {
		if !strings.Contains(e, "/api/v1/conflicts?") {
			unexpected = append(unexpected, e)
		}
	}
	assert.Empty(t, unexpected)
}
