package serve_test

// The browser end-to-end scenarios. The harness they run on - browser
// discovery, the skip, the seeded server, the browser's own error log - is
// e2e_harness_test.go's.
//
// Unit 1's three (shell loads, theme persists, chooser renders) are
// extended here for Mission Control's real DOM; Unit 2 adds the chooser's
// redirect/card-grid branches, the library's filter and sort controls, and
// the attention cards' presence on a seeded fixture
// (docs/plans/2026-08-31-webui-impl.md Unit 2: "chromedp scenarios
// extended: chooser→home, filter, sort, theme toggle, card presence with
// the seeded fixture"). Each is a thing no httptest assertion can reach,
// because each needs the modules to have EXECUTED.

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_ShellLoadsAndStoreHydratesStatus is the whole boot path in one
// assertion: the server serves the shell at a deep app route, the browser
// admits the shell's inline theme bootstrap under the CSP, the module graph
// resolves over HTTP with no bundler, main.js fetches /api/v1/status, and
// the render loop puts Mission Control on screen.
//
// The proof that the STORE hydrated - rather than merely that a request was
// made - is the text: the URL carries the game's ID ("g1"), and the game
// picker's own label carries its NAME ("Fixture Game"), which only the
// fetched document knows.
func TestE2E_ShellLoadsAndStoreHydratesStatus(t *testing.T) {
	f := newE2EFixture(t)

	var picker, title string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Title(&title),
		textContent(`.game-picker__trigger`, &picker),
	)

	assert.Equal(t, "lmm", title)
	assert.Contains(t, picker, f.Game.Name,
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
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
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
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
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

// TestE2E_ChooserRedirectsToTheSingleDefaultGame covers the chooser's
// redirect branch: a single configured game (also its own default, via
// newFixtureServiceWithSource's SetDefaultGame) sends "/" straight to
// Mission Control rather than rendering a one-card chooser nobody needs to
// choose from.
func TestE2E_ChooserRedirectsToTheSingleDefaultGame(t *testing.T) {
	f := newE2EFixture(t)

	var url string
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Location(&url),
	)

	assert.Equal(t, f.HomePath(), url, "the redirect must land on the game's own active profile")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ChooserRendersGameCardsAndNavigatesOnClick covers the chooser's
// other branch - two games, neither marked default - and the click-through
// from a card into that game's Mission Control (the "chooser→home"
// scenario the redirect test above covers automatically; this covers it
// interactively).
//
// It also guards a real bug this unit hit and fixed: Preact's plain
// render() does not clear pre-existing DOM children on its first call into
// a container - it only ever diffs against what IT previously rendered
// there - so the shell's own static ".app-booting" placeholder
// (spa/index.html) was staying behind as a stray, permanently-orphaned
// sibling of every real render (main.js now clears the container once,
// before the first render). staleLoadingNodes below is that regression's
// own signature: the placeholder outliving the real content it was
// standing in for.
func TestE2E_ChooserRendersGameCardsAndNavigatesOnClick(t *testing.T) {
	f := newE2EMultiGameFixture(t)

	var cardCount, staleLoadingNodes int
	var names []string
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`.game-chooser[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".game-card").length`, &cardCount),
		chromedp.Evaluate(
			`Array.from(document.querySelectorAll(".game-card__name")).map(e => e.textContent)`,
			&names,
		),
		chromedp.Evaluate(`document.querySelectorAll(".app-booting").length`, &staleLoadingNodes),
	)

	assert.Equal(t, 2, cardCount)
	assert.ElementsMatch(t, []string{f.GameA.Name, f.GameB.Name}, names)
	assert.Zero(t, staleLoadingNodes,
		"the shell's static placeholder must not survive as a stray sibling of the real render")

	var url string
	f.runInBrowser(t,
		chromedp.Click(`.game-card`, chromedp.ByQuery),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Location(&url),
	)

	assert.True(t, strings.Contains(url, "/g/"+f.GameA.ID+"/") || strings.Contains(url, "/g/"+f.GameB.ID+"/"),
		"the click must land on one of the two games' Mission Control, got %q", url)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_OnlyOnePickerDropdownOpenAtATime guards Minor 5: the game,
// profile and activity-bell dropdowns each used to hold their own
// independent open state, so opening a second one left the first standing
// open behind it (verified live: opening the game picker then the profile
// picker left BOTH `.picker__menu` nodes in the DOM). They now share one
// "which picker is open" state in TopBar, plus an outside-click/Escape
// listener that closes whichever one is open.
func TestE2E_OnlyOnePickerDropdownOpenAtATime(t *testing.T) {
	f := newE2EFixture(t)

	var afterSecondOpen int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`.game-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.game-picker__menu`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".picker__menu").length`, &afterSecondOpen),
	)
	assert.Equal(t, 1, afterSecondOpen, "opening the profile picker must close the game picker")

	var afterEscape int
	f.runInBrowser(t,
		chromedp.Sleep(200*time.Millisecond),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(`document.querySelectorAll(".picker__menu").length`, &afterEscape),
	)
	assert.Zero(t, afterEscape, "Escape must close whichever picker is open")

	// A real coordinate-based click risks landing on the tray's own
	// absolutely-positioned dropdown if it happens to overlap the target
	// point; dispatching the pointerdown directly on document.body proves
	// the LISTENER's outside-of-header check without depending on layout.
	// The preact/hooks vendor batches useEffect through its own
	// requestAnimationFrame-driven flush (measured empirically at up to a
	// handful of frames in a headless browser), so the sleeps give both the
	// listener's attachment and its resulting re-render room to land before
	// each half is asserted.
	var afterOutsideClick int
	f.runInBrowser(t,
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(`document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }))`, nil),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(`document.querySelectorAll(".picker__menu").length`, &afterOutsideClick),
	)
	assert.Zero(t, afterOutsideClick, "a click outside every picker must close the open one")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_MissionControlBodyHasGutterPadding guards the unit 2 gate's I1
// finding: `.mission-control__body` had no CSS rule at all, so the
// attention cards and the library ran flat against both viewport edges and
// the top bar's bottom edge (measured live: `padding: "0px"`, `cardsLeft:
// 0`). getComputedStyle is the same instrument the review used to catch it.
func TestE2E_MissionControlBodyHasGutterPadding(t *testing.T) {
	f := newE2EFixture(t)

	var padding string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(
			`getComputedStyle(document.querySelector(".mission-control__body")).paddingLeft`,
			&padding,
		),
	)

	assert.NotEqual(t, "0px", padding, "the body must carry a real gutter, not run flat against the viewport edge")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_EmptyLibraryShowsInlineHint covers the other empty state: a
// resolvable game/profile with nothing installed in it yet.
func TestE2E_EmptyLibraryShowsInlineHint(t *testing.T) {
	f := newE2EFixture(t)

	var hint string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		textContent(`.library .empty-state`, &hint),
	)

	assert.Contains(t, hint, "No mods installed yet")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_RowNameOpensSlideOverByKeyboard guards Minor 6: the row-open
// affordance was a bare `<td onClick>` - not focusable, not activatable by
// keyboard - making the primary navigation on the primary screen
// mouse-only. It is now a real <button>, reachable by Tab and activatable
// by Enter, with no mouse action anywhere in this scenario.
func TestE2E_RowNameOpensSlideOverByKeyboard(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var url string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Focus(`.mod-row__name`, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		chromedp.Location(&url),
	)

	assert.Contains(t, url, "?mod=", "Enter on the focused row name must open the slide-over, same as a click")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryFilterNarrowsRows proves the filter control actually
// changes what's on screen, not just its own selected option: three seeded
// mods, two enabled, and selecting "Enabled" leaves exactly those two.
func TestE2E_LibraryFilterNarrowsRows(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var beforeCount int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".mod-row").length`, &beforeCount),
	)
	require.Equal(t, 3, beforeCount)

	var names []string
	var afterCount int
	var header string
	f.runInBrowser(t,
		chromedp.SetValue(`select[name="filter"]`, "enabled", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".mod-row").length`, &afterCount),
		chromedp.Evaluate(
			`Array.from(document.querySelectorAll(".mod-row__name")).map(e => e.textContent)`,
			&names,
		),
		textContent(`.library .section-header`, &header),
	)

	assert.Equal(t, 2, afterCount, "only the two enabled mods must remain")
	assert.ElementsMatch(t, []string{"Alpha Mod", "Zebra Mod"}, names)
	assert.Equal(t, "Library (2)", header,
		"M3: the header must count the FILTERED rows, not every installed mod")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_OmnibarNarrowsAndRelabelsLibraryHeader covers the design doc's
// third "Missing" item: the omnibar's live filter already narrowed the
// table, but the header stayed "LIBRARY (3)" instead of the design's own
// "In your library (n)" (§Search).
func TestE2E_OmnibarNarrowsAndRelabelsLibraryHeader(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var before string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		textContent(`.library .section-header`, &before),
	)
	require.Equal(t, "Library (3)", before)

	var after string
	var rowCount int
	f.runInBrowser(t,
		chromedp.SendKeys(`.omnibar`, "alpha", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".mod-row").length`, &rowCount),
		textContent(`.library .section-header`, &after),
	)

	require.Equal(t, 1, rowCount)
	assert.Equal(t, "In your library (1)", after)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibrarySortReordersRowsByName proves the sort control actually
// reorders the DOM: three seeded mods in an unspecified default order,
// alphabetical after selecting "Name".
func TestE2E_LibrarySortReordersRowsByName(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var names []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SetValue(`select[name="sort"]`, "name", chromedp.ByQuery),
		chromedp.Evaluate(
			`Array.from(document.querySelectorAll(".mod-row__name")).map(e => e.textContent)`,
			&names,
		),
	)

	assert.Equal(t, []string{"Alpha Mod", "Middle Mod", "Zebra Mod"}, names)
	assert.Empty(t, f.BrowserErrors())
}

// TestSortRows_RecentToleratesMissingInstalledAt guards Minor 10:
// modrows.js#sortRows's "recent" comparator used to be a bare
// `new Date(x) - new Date(y)`, which is NaN for a missing or unparsable
// installed_at, and Array.prototype.sort's behavior on a NaN-returning
// comparator is not spec-guaranteed. modrows.js has no DOM, so it is
// exercised directly here via a dynamic import in a real browser (the same
// module the library component runs), rather than through the table -
// there is no installed_mods row that reaches the wire without an
// installed_at today, so this proves the guard rather than a live bug.
func TestSortRows_RecentToleratesMissingInstalledAt(t *testing.T) {
	f := newE2EFixture(t)

	var order []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { sortRows } = await import("/static/app/modrows.js");
			const rows = [
				{ name: "Newer", installed_at: "2025-06-01T00:00:00Z" },
				{ name: "Missing" },
				{ name: "Older", installed_at: "2020-01-01T00:00:00Z" },
				{ name: "Malformed", installed_at: "not-a-date" },
			];
			return sortRows(rows, "recent").map((r) => r.name);
		})()`, &order, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	require.Len(t, order, 4, "the guard must never drop a row")
	assert.Equal(t, []string{"Newer", "Older"}, order[:2],
		"the two real dates must still sort correctly relative to each other")
	assert.ElementsMatch(t, []string{"Missing", "Malformed"}, order[2:],
		"an absent/unparsable installed_at must fall back to the epoch, not produce an arbitrary position")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_OpeningSlideOverDoesNotRehydrate guards the unit 2 gate's I2
// finding: opening or closing the ?mod= slide-over annotation dispatched a
// popstate, and main.js#go used to call hydrate() unconditionally on every
// route change - re-running the full Mission Control hydrate (five fetches,
// one of them the network-heavy full-tier verify) on a click that never
// changes which game/profile is on screen. A fetch spy installed AFTER the
// initial hydrate has settled proves neither the open nor the close costs a
// single additional request.
func TestE2E_OpeningSlideOverDoesNotRehydrate(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			window.__fetchCount = 0;
			const origFetch = window.fetch;
			window.fetch = (...args) => {
				window.__fetchCount++;
				return origFetch(...args);
			};
		`, nil),
	)

	var afterOpen int
	f.runInBrowser(t,
		chromedp.Click(`.mod-row__name`, chromedp.ByQuery),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		chromedp.Evaluate(`window.__fetchCount`, &afterOpen),
	)
	assert.Zero(t, afterOpen, "opening the slide-over must not trigger any /api/v1 fetch")

	var afterClose int
	f.runInBrowser(t,
		chromedp.Click(`.slide-over__close`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.slide-over`, chromedp.ByQuery),
		chromedp.Evaluate(`window.__fetchCount`, &afterClose),
	)
	assert.Zero(t, afterClose, "closing the slide-over must not trigger any /api/v1 fetch either")
	assert.Empty(t, f.BrowserErrors())
}

// assertNoUncaughtErrors is BrowserErrors() filtered for the I3 failure
// scenarios: they deliberately provoke a real network 500, which Chrome
// itself logs as an error-level "network:" entry independently of whether
// the SPA handled it - that entry is the fixture working as intended, not a
// bug. An uncaught JS exception is not, and still fails the test.
func assertNoUncaughtErrors(t *testing.T, errs []string) {
	t.Helper()
	for _, e := range errs {
		assert.NotContains(t, e, "uncaught:", "the failure must be caught, not thrown: %s", e)
	}
}

// TestE2E_FailedMods_RendersErrorStateNotEmpty guards the unit 2 gate's I3
// finding for the library: mods === null used to render the same "Loading
// library…" it shows while a fetch is still in flight, forever, with no
// visible distinction from a genuine failure. It must instead say what
// failed and offer a retry that recovers once the fault clears.
func TestE2E_FailedMods_RendersErrorStateNotEmpty(t *testing.T) {
	f, setFailing := newE2EFixtureWithFailingPath(t, "/api/v1/mods")

	var errorText string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.empty-state--error`, chromedp.ByQuery),
		textContent(`.empty-state--error`, &errorText),
	)
	assert.Contains(t, errorText, "Couldn't load your library")
	assert.NotContains(t, errorText, "No mods installed yet",
		"a failed fetch must never render as the empty-library all-clear")

	setFailing(false)
	var rowCount string
	f.runInBrowser(t,
		chromedp.Click(`.empty-state--error .button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".mod-row").length + ""`, &rowCount),
	)
	assert.Equal(t, "1", rowCount, "the retry must recover once the fault clears")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_FailedUpdates_RendersErrorStateNotAbsent guards the same I3
// finding for the Updates attention card: a failed fetch and "nothing to
// report" both leave `updates` null, and AttentionCards must tell them
// apart rather than rendering neither card nor section. No update is
// seeded, so a successful retry finds nothing to report either - the card
// (correctly) disappears again, same as the design's own all-clear rule;
// the assertion is that the ERROR half clears, not that a card stays.
func TestE2E_FailedUpdates_RendersErrorStateNotAbsent(t *testing.T) {
	f, setFailing := newE2EFixtureWithFailingPath(t, "/api/v1/updates")

	var errorText string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates .card__error`, chromedp.ByQuery),
		textContent(`.card--updates .card__error`, &errorText),
	)
	assert.Contains(t, errorText, "Couldn't check for updates")

	setFailing(false)
	f.runInBrowser(t,
		chromedp.Click(`.card--updates .card__error + .button`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.card--updates`, chromedp.ByQuery),
	)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_FailedHealth_RendersErrorStateNotHealthy guards I3's most
// consequential case: /api/v1/health is the network-heavy full-tier verify
// and the endpoint most likely to fail in the field, and a swallowed
// failure used to read as "healthy" - the exact opposite of the truth.
func TestE2E_FailedHealth_RendersErrorStateNotHealthy(t *testing.T) {
	f, setFailing := newE2EFixtureWithFailingPath(t, "/api/v1/health")

	var errorText string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--health .card__error`, chromedp.ByQuery),
		textContent(`.card--health .card__error`, &errorText),
	)
	assert.Contains(t, errorText, "Couldn't check health")

	setFailing(false)
	f.runInBrowser(t,
		chromedp.Click(`.card--health .card__error + .button`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.card--health`, chromedp.ByQuery),
	)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_FailedConflicts_RendersErrorStateNotAbsent is I3's fourth
// scenario: the Conflicts card. No conflict is seeded, so - like Updates -
// a successful retry makes the card disappear rather than repopulate.
func TestE2E_FailedConflicts_RendersErrorStateNotAbsent(t *testing.T) {
	f, setFailing := newE2EFixtureWithFailingPath(t, "/api/v1/conflicts")

	var errorText string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--conflicts .card__error`, chromedp.ByQuery),
		textContent(`.card--conflicts .card__error`, &errorText),
	)
	assert.Contains(t, errorText, "Couldn't check for conflicts")

	setFailing(false)
	f.runInBrowser(t,
		chromedp.Click(`.card--conflicts .card__error + .button`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.card--conflicts`, chromedp.ByQuery),
	)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_AttentionCardsRenderFromSeededFixture is the "card presence"
// scenario: on a fixture where all three cards have something to say, all
// three actually render, each naming what it found.
func TestE2E_AttentionCardsRenderFromSeededFixture(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var updates, health, conflicts string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.attention-cards`, chromedp.ByQuery),
		textContent(`.card--updates`, &updates),
		textContent(`.card--health`, &health),
		textContent(`.card--conflicts`, &conflicts),
	)

	assert.Contains(t, updates, "Better Boots")
	assert.Contains(t, updates, "2.0", "the update target version must be named")
	assert.Contains(t, health, "Better Boots", "the version-mismatch finding must name the mod")
	assert.Contains(t, health, "recorded 1.0, source reports 2.0",
		"M2: the version-mismatch finding must carry its own recorded/effective versions, matching the CLI")
	assert.Contains(t, conflicts, "shared.esp", "the conflicting path must be named")
	assert.Contains(t, conflicts, "wins: Mod Y",
		"the spec's Missing 2: a conflict must name the winning rule, not just the contenders (Mod Y was added to the load order last)")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryCheckboxColumnsAreLabelled answers the owner demo's own
// finding: the library's two leading checkbox columns are DIFFERENT things -
// the first selects a row for the batch bar, the second is the mod's own
// enabled state - and both shipped as blank headings, which is exactly the
// ambiguity that was called out (docs/plans/unit3-carry.md, OWNER DEMO 1).
func TestE2E_LibraryCheckboxColumnsAreLabelled(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var selectHead, enabledHead, selectBox, enabledBox string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		textContent(`.library__table th.col--select`, &selectHead),
		textContent(`.library__table th.col--enabled`, &enabledHead),
		chromedp.AttributeValue(`.mod-row td.col--select input`, "aria-label", &selectBox, nil, chromedp.ByQuery),
		chromedp.AttributeValue(`.mod-row td.col--enabled input`, "aria-label", &enabledBox, nil, chromedp.ByQuery),
	)

	assert.Equal(t, "Select", selectHead)
	assert.Equal(t, "Enabled", enabledHead)
	assert.Contains(t, selectBox, "batch",
		"the select checkbox must name what it selects FOR, not just repeat the heading")
	assert.Contains(t, enabledBox, "Enable")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryColumnsProgressivelyRevealOnWideScreens covers the other
// half of OWNER DEMO 1: a 1080p-minimum tool that renders the same seven
// columns on a 2560px display is wasting most of it. Author and the install
// date arrive at >= 1440px, the source and link method at >= 1920px - all
// four already carried by the /api/v1/mods document, so nothing new is
// fetched to fill them.
//
// The assertion is on the COMPUTED display of the real cells at four real
// viewport widths, which is the only way to prove a media query actually
// fires; a class-name assertion would pass against a stylesheet that
// defines nothing.
func TestE2E_LibraryColumnsProgressivelyRevealOnWideScreens(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	shown := func(width int, wide, xwide *bool) chromedp.Tasks {
		return chromedp.Tasks{
			chromedp.EmulateViewport(int64(width), 1080),
			chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
			chromedp.Evaluate(`getComputedStyle(document.querySelector(".library__table th.col--author")).display !== "none"`, wide),
			chromedp.Evaluate(`getComputedStyle(document.querySelector(".library__table th.col--source")).display !== "none"`, xwide),
		}
	}

	var wide1280, xwide1280, wide1600, xwide1600, wide1920, xwide1920, wide2560, xwide2560 bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		shown(1280, &wide1280, &xwide1280),
		shown(1600, &wide1600, &xwide1600),
		shown(1920, &wide1920, &xwide1920),
		shown(2560, &wide2560, &xwide2560),
	)

	assert.False(t, wide1280, "author must be hidden below the first breakpoint")
	assert.False(t, xwide1280, "source must be hidden below the first breakpoint")
	assert.True(t, wide1600, "author arrives at >= 1440px")
	assert.False(t, xwide1600, "source must still be hidden at 1600px")
	assert.True(t, wide1920, "author stays once revealed")
	assert.True(t, xwide1920, "source arrives at >= 1920px")
	assert.True(t, wide2560)
	assert.True(t, xwide2560)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_WideColumnsCarryTheDocumentsOwnData is the value half of the
// scenario above: a revealed column that renders nothing is worse than no
// column at all. At 1920px every one of the four carries the fact its
// heading promises, read from the seeded fixture's own mods.
func TestE2E_WideColumnsCarryTheDocumentsOwnData(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var author, source, method, installed string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1920, 1080),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		textContent(`.mod-row td.col--author`, &author),
		textContent(`.mod-row td.col--source`, &source),
		textContent(`.mod-row td.col--method`, &method),
		textContent(`.mod-row td.col--installed`, &installed),
	)

	assert.Equal(t, "Ada Lovelace", author)
	assert.Equal(t, "fake", source)
	assert.Equal(t, "symlink", method)
	assert.NotEmpty(t, installed)
	assert.NotEqual(t, "—", installed, "the seeded mods all carry an installed_at")
	assert.Empty(t, f.BrowserErrors())
}
