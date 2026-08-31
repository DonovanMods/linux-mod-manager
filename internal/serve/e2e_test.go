package serve_test

// The browser end-to-end scenarios. The harness they run on - browser
// discovery, the skip, the seeded server, the browser's own error log - is
// e2e_harness_test.go's; later units add scenarios beside these three
// (install with its conflict round trip, batch update, deploy progress,
// reorder - docs/plans/2026-08-31-serve-spa-design.md §Testing).
//
// Unit 1's three are the foundation ones: the shell really loads, the store
// really hydrates, and the theme override really survives a reload. Each is
// a thing no httptest assertion can reach, because each needs the modules
// to have EXECUTED.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_ShellLoadsAndStoreHydratesStatus is the whole boot path in one
// assertion: the server serves the shell at a deep app route, the browser
// admits the shell's inline theme bootstrap under the CSP, the module graph
// resolves over HTTP with no bundler, main.js fetches /api/v1/status, and
// the render loop puts the result on screen.
//
// The proof that the STORE hydrated - rather than merely that a request was
// made - is the text: the URL carries the game's ID ("g1"), and the
// rendered line carries its NAME ("Fixture Game"), which only the fetched
// document knows.
func TestE2E_ShellLoadsAndStoreHydratesStatus(t *testing.T) {
	f := newE2EFixture(t)

	var heading, ready, title string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.app-ready[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Title(&title),
		textContent(`.section-header`, &heading),
		textContent(`.app-ready`, &ready),
	)

	assert.Equal(t, "lmm", title)
	assert.Equal(t, f.Game.ID+" / "+f.Profile, heading,
		"the router reads the context out of the path")
	assert.Contains(t, ready, f.Game.Name,
		"the rendered text must carry a fact only /api/v1/status knows")
	assert.Empty(t, f.BrowserErrors(),
		"the browser must report nothing - a CSP refusal or a failed module load lands here")
}

// TestE2E_ThemeOverridePersistsAcrossReload covers the three-state theme
// and the one sanctioned inline script together.
//
// Two clicks take the toggle system -> light -> dark. The reload is the
// real test: the override has to be stamped on <html> from localStorage
// before the first paint, by the shell's inline script, which the CSP
// admits by the SHA-256 of its exact bytes. If that hash ever drifts from
// what the server sends, the script is refused, and the ONLY places that
// shows are the reloaded page's missing attribute and the browser error log
// - both asserted here.
//
// The computed background is checked as well, because a data-theme
// attribute that no token set responds to is a passing test and a broken
// UI.
func TestE2E_ThemeOverridePersistsAcrossReload(t *testing.T) {
	f := newE2EFixture(t)

	var label, attr, stored, darkBackground string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.theme-toggle`, chromedp.ByQuery),
		chromedp.Click(`.theme-toggle`, chromedp.ByQuery),
		chromedp.Click(`.theme-toggle`, chromedp.ByQuery),
		textContent(`.theme-toggle`, &label),
		chromedp.Evaluate(`document.documentElement.getAttribute("data-theme")`, &attr),
		chromedp.Evaluate(`localStorage.getItem("lmm-theme")`, &stored),
		chromedp.Evaluate(`getComputedStyle(document.body).backgroundColor`, &darkBackground),
	)

	require.Equal(t, "Theme: dark", label, "system -> light -> dark")
	assert.Equal(t, "dark", attr)
	assert.Equal(t, "dark", stored, "the override is persisted, not just applied")

	var afterAttr, afterStored, afterLabel string
	f.runInBrowser(t,
		chromedp.Reload(),
		chromedp.WaitVisible(`.app-ready[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.documentElement.getAttribute("data-theme")`, &afterAttr),
		chromedp.Evaluate(`localStorage.getItem("lmm-theme")`, &afterStored),
		textContent(`.theme-toggle`, &afterLabel),
	)

	assert.Equal(t, "dark", afterAttr, "the pre-paint bootstrap must restore the override")
	assert.Equal(t, "dark", afterStored)
	assert.Equal(t, "Theme: dark", afterLabel, "and the module must agree with it")
	assert.Empty(t, f.BrowserErrors(),
		"a refused inline script is a console error and nothing else")

	// A third click returns to "system"; the background must move with it,
	// which is what proves both token sets are actually wired to paint.
	var systemLabel, clearedAttr, lightBackground string
	f.runInBrowser(t,
		chromedp.Click(`.theme-toggle`, chromedp.ByQuery),
		chromedp.Click(`.theme-toggle`, chromedp.ByQuery),
		textContent(`.theme-toggle`, &systemLabel),
		chromedp.Evaluate(`document.documentElement.getAttribute("data-theme")`, &clearedAttr),
		chromedp.Evaluate(`getComputedStyle(document.body).backgroundColor`, &lightBackground),
	)

	assert.Equal(t, "Theme: light", systemLabel)
	assert.Equal(t, "light", clearedAttr)
	assert.NotEqual(t, darkBackground, lightBackground,
		"the two Launcher token sets must resolve to different paint")
}

// TestE2E_ChooserRouteRendersWithoutAContext covers the other route the
// shell answers: "/" has no game and no profile in the path, so the store
// hydrates the unscoped status document instead - the same boot path, one
// screen earlier.
func TestE2E_ChooserRouteRendersWithoutAContext(t *testing.T) {
	f := newE2EFixture(t)

	var heading, ready string
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`.app-ready[data-hydrated="true"]`, chromedp.ByQuery),
		textContent(`.section-header`, &heading),
		textContent(`.app-ready`, &ready),
	)

	assert.Equal(t, "Choose a game", heading)
	assert.Contains(t, ready, "1 game configured")
	assert.Empty(t, f.BrowserErrors())
}
