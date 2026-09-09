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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
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

// TestE2E_OpeningSlideOverDoesNotRehydrateMissionControl guards the unit 2
// gate's I2 finding, updated for issue 330's real slide-over: opening or
// closing the ?mod= annotation dispatches a popstate, and main.js#go used
// to call hydrate() unconditionally on every route change - re-running the
// full Mission Control hydrate (five fetches, one of them the network-heavy
// full-tier verify) on a click that never changes which game/profile is on
// screen. That guarantee still holds and is still asserted here - but the
// slide-over itself now legitimately fetches ONE thing of its own (its
// changelog preview, core.ModDetail - the one section that cannot render
// from what Mission Control already loaded, modpanel.js's own header
// comment), so this asserts on WHICH paths were fetched rather than a bare
// count. A fetch spy installed AFTER the initial hydrate has settled proves
// neither open nor close re-touches any of Mission Control's own four
// documents or /api/v1/status.
func TestE2E_OpeningSlideOverDoesNotRehydrateMissionControl(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			window.__fetchedPaths = [];
			const origFetch = window.fetch;
			window.fetch = (...args) => {
				window.__fetchedPaths.push(new URL(args[0], window.location.origin).pathname);
				return origFetch(...args);
			};
		`, nil),
	)

	var afterOpen []string
	f.runInBrowser(t,
		chromedp.Click(`.mod-row__name`, chromedp.ByQuery),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		// Waits for the changelog's own lazy fetch to settle (modpanel.js's
		// data-changelog-status), rather than racing it: the slide-over's
		// other content renders synchronously from data Mission Control
		// already had, but the changelog section starts in "loading" and
		// only reaches "ready" once its fetch has actually happened.
		chromedp.WaitVisible(`.slide-over__section[data-changelog-status="ready"]`, chromedp.ByQuery),
		chromedp.Evaluate(`window.__fetchedPaths`, &afterOpen),
	)
	assert.NotContains(t, afterOpen, "/api/v1/mods", "opening the slide-over must not re-fetch the library")
	assert.NotContains(t, afterOpen, "/api/v1/updates")
	assert.NotContains(t, afterOpen, "/api/v1/health")
	assert.NotContains(t, afterOpen, "/api/v1/conflicts")
	assert.NotContains(t, afterOpen, "/api/v1/status")
	found := false
	for _, p := range afterOpen {
		if strings.HasPrefix(p, "/api/v1/mods/fake/") {
			found = true
		}
	}
	assert.True(t, found, "opening the slide-over must fetch that one mod's own ModDetail for its changelog: %v", afterOpen)

	var afterClose []string
	f.runInBrowser(t,
		chromedp.Click(`.slide-over__close`, chromedp.ByQuery),
		waitGone(`.slide-over`),
		chromedp.Evaluate(`window.__fetchedPaths`, &afterClose),
	)
	assert.Equal(t, afterOpen, afterClose, "closing the slide-over must not trigger any further /api/v1 fetch")
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
	// issue 332: the enabled checkbox is now LIVE (library.js's row toggle),
	// so its aria-label names the ACTION it would take next ("Enable"/
	// "Disable" - whichever the row's current state implies), not a fixed
	// placeholder word every row shared while it was still disabled.
	assert.True(t, strings.Contains(enabledBox, "Enable") || strings.Contains(enabledBox, "Disable"),
		"expected an Enable/Disable label, got %q", enabledBox)
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

// TestE2E_DeployOpensAConfirmModalRenderingThePlan is the confirm-plan
// framework's core promise: the SPA's sibling of the CLI's confirm prompt.
// Clicking a mutation control does NOT mutate - it computes the plan
// (POST /api/v1/plans/deploy) and renders that plan document, so what is
// about to happen is on screen before anything is asked of the machine.
//
// The assertion is on facts only the PLAN knows - the mods it would deploy
// and the file each would link - rather than on the button's own label,
// because a modal that renders its title and nothing else is exactly the
// failure this scenario exists to catch.
func TestE2E_DeployOpensAConfirmModalRenderingThePlan(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	var body, indicator string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		textContent(`.deploy-indicator`, &indicator),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="deploy"]`, &body),
	)

	assert.Contains(t, indicator, "2 changes undeployed")
	assert.Contains(t, body, "Alpha Mod")
	assert.Contains(t, body, "Beta Mod")
	assert.Contains(t, body, "alpha.pak", "the plan names the files it would link")
	assert.Contains(t, body, f.Profile)
	// #330 carry-4: planDeploy now stamps each row's Ref.Version, and
	// plan_deploy.js renders it - seedDeployableMods installs both mods at
	// "1.0".
	assert.Contains(t, body, "1.0", "the plan names the version each mod would deploy")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ConfirmModalCancelsWithoutMutating is the other half of a
// confirm: Cancel closes it and nothing happened. The proof that nothing
// happened is the undeployed indicator, which still counts both mods -
// a plan that had been applied would leave it reading "Deployed".
func TestE2E_ConfirmModalCancelsWithoutMutating(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	var indicator string
	var modals int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="cancel"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".modal").length`, &modals),
		textContent(`.deploy-indicator`, &indicator),
	)

	assert.Zero(t, modals)
	assert.Contains(t, indicator, "2 changes undeployed", "Cancel must not have deployed anything")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ConfirmModalEscapeClosesIt covers the keyboard route out of a
// modal, which is the one every desktop user reaches for first and the one
// a hand-rolled dialog most often forgets.
func TestE2E_ConfirmModalEscapeClosesIt(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
	)

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ConfirmModalRendersAPlanFailureHonestly is the I3 rule applied to
// the modal: a plan that could not be computed says so, with the envelope's
// own message, and offers no Confirm button at all. Rendering an empty plan
// with a live Confirm would invite the user to apply nothing.
func TestE2E_ConfirmModalRendersAPlanFailureHonestly(t *testing.T) {
	f, _ := newE2EFixtureWithFailingPath(t, "/api/v1/plans/deploy")

	var body string
	var confirms int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal .modal__error`, chromedp.ByQuery),
		textContent(`.modal`, &body),
		chromedp.Evaluate(`document.querySelectorAll('.modal [data-action="confirm"]').length`, &confirms),
	)

	assert.Contains(t, body, "simulated upstream failure")
	assert.Zero(t, confirms, "a plan that failed to compute must offer nothing to confirm")
}

// TestE2E_DeployRunsAsAJobAndMorphsTheControl is Unit 3's spine, end to
// end: the top bar's Deploy button opens the confirm modal, the confirm
// starts a real job, the BUTTON ITSELF becomes that job's progress
// (docs/plans/2026-08-31-serve-spa-design.md §Jobs: "the control you
// clicked morphs into its progress"), the phase text moves as core's own
// events arrive over the activity stream, and the outcome resurfaces in
// place. When it is over the undeployed indicator has caught up, because a
// finished mutation re-hydrates the documents it invalidated.
//
// The end state is asserted on DISK as well as on screen: a green progress
// bar over a game directory with nothing in it would be the worst possible
// pass. The fixture's deploy is slowed by a real install.after_each hook,
// which is what makes "while it is running" a window a browser can be
// driven through rather than a race (newE2EFixtureWithSlowDeploy).
func TestE2E_DeployRunsAsAJobAndMorphsTheControl(t *testing.T) {
	f := newE2EFixtureWithSlowDeploy(t)

	var running, finished, indicator string
	var toasts int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		// The modal closes the moment the job is accepted, and the control
		// it was opened from is now the job's progress.
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress`, chromedp.ByQuery),
		// A determinate bar means a real progress FRAME arrived: an
		// indeterminate one is what the control shows before core has said
		// anything at all, so waiting on this is waiting on the stream.
		chromedp.WaitVisible(`.job-progress__bar:not(.job-progress__bar--indeterminate)`, chromedp.ByQuery),
		textContent(`.job-progress__text`, &running),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
		textContent(`.job-progress__text`, &finished),
		chromedp.WaitVisible(`.deploy-indicator:not(.deploy-indicator--pending)`, chromedp.ByQuery),
		textContent(`.deploy-indicator`, &indicator),
		chromedp.Evaluate(`document.querySelectorAll(".toast").length`, &toasts),
	)

	assert.Contains(t, running, "Deployed", "the humanized phase, not the wire name")
	assert.NotContains(t, running, "deploy_", "the wire phase name must not reach the screen")
	assert.Contains(t, running, "of 2", "the batch position core reports")
	assert.Equal(t, "Done", finished)
	assert.Equal(t, "Deployed", indicator, "the undeployed count must re-hydrate after the job")
	assert.Zero(t, toasts, "a completion whose control is on screen must not also toast")

	for _, name := range []string{"alpha.pak", "beta.pak"} {
		_, err := os.Lstat(filepath.Join(f.Game.ModPath, name))
		assert.NoError(t, err, "the deploy must have put %s in the game directory", name)
	}
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_PostJobRehydrateFailureLeavesMissionControlOnScreen is M3: every
// job completion re-hydrates the route (main.js's onJobDone), and that
// re-hydrate can fail on its own (the API restarting, a network blip) for
// reasons that have nothing to do with the job that just finished. Before
// the fix that failure blanked the whole page into the fatal "Choose a
// different game" state - taking the just-finished job's own inline outcome
// and the tray down with it. The fix routes it into fetchErrors instead (the
// I3 rule already applied to the four supplementary reads), so the page and
// everything on it survive.
//
// The fault is a stubbed window.fetch, installed in the page AFTER the
// initial load has already succeeded, so what fails is specifically the
// POST-JOB re-hydrate - not the first one, which stays fatal on purpose
// (there is nothing on screen yet to protect). A server-side fault (the
// reverse-proxy harness startE2EServerWithFailingPath uses) is the wrong
// tool here: it rewrites the forwarded Host but not Origin, and the
// deploy this scenario needs to actually SUCCEED is a real POST that the
// Origin/Host mismatch would then get refused by the CSRF middleware before
// ever reaching core - failing the setup, not exercising the finding.
// Stubbing fetch in the page is the honest equivalent of "the network blipped
// for this one document", exactly as the finding's own wording allows.
func TestE2E_PostJobRehydrateFailureLeavesMissionControlOnScreen(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	var banner, indicator string
	var trayPresent bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const real = window.fetch.bind(window);
			window.fetch = (input, init) => {
				const url = typeof input === "string" ? input : (input && input.url) || "";
				if (url.includes("/api/v1/status")) {
					return Promise.reject(new Error("simulated status failure"));
				}
				return real(input, init);
			};
		})()`, nil),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		// The job itself succeeds (it never touches /api/v1/status) - it is
		// only the re-hydrate that follows it that hits the stub.
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
		// This selector is the crux: under the bug, the re-hydrate's catch
		// blanks `status`, MissionControl's early return replaces the WHOLE
		// subtree with the fatal branch, and `.mission-control` itself stops
		// existing - so this WaitVisible times out rather than merely
		// finding empty text.
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"] .app-error`, chromedp.ByQuery),
		textContent(`.mission-control .app-error`, &banner),
		textContent(`.deploy-indicator`, &indicator),
		chromedp.Evaluate(`document.querySelector(".activity-bell__trigger") !== null`, &trayPresent),
	)

	assert.Contains(t, banner, "simulated status failure")
	assert.True(t, trayPresent, "the tray must still render, not a blank fatal page")
	assert.NotEmpty(t, indicator, "the top bar's own last-known state must survive, not go blank")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_DeployDismissesBackToTheButton closes the morph's loop: a
// finished job's readout is dismissible, and dismissing it returns the
// control to the thing it was, ready to be used again. A progress readout
// that never goes away would leave the top bar with no Deploy button after
// the first deploy of the session.
func TestE2E_DeployDismissesBackToTheButton(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
		chromedp.Click(`.job-progress__dismiss`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-action="deploy"]`, chromedp.ByQuery),
	)

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_OffScreenCompletionRaisesAToast is the other half of the design's
// toast rule (§Jobs): a completion whose originating control is NOT on
// screen would otherwise be invisible, so it surfaces as a toast. Its twin
// assertion - that a completion the user IS watching does not also toast -
// lives in TestE2E_DeployRunsAsAJobAndMorphsTheControl.
//
// The job here is started by a second client, which is the honest version
// of "no origin on screen": another tab, or a script, or a `lmm` command in
// a terminal. Nothing in this page ever claimed it, so nothing in this page
// can show its outcome in place.
func TestE2E_OffScreenCompletionRaisesAToast(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	var toast string
	var morphed int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error {
			startDeployFromAnotherClient(t, f)
			return nil
		}),
		chromedp.WaitVisible(`.toast--success`, chromedp.ByQuery),
		textContent(`.toast--success .toast__title`, &toast),
		chromedp.Evaluate(`document.querySelectorAll(".job-progress").length`, &morphed),
	)

	assert.Contains(t, toast, "deploy", "the toast must name the mutation that finished")
	assert.Zero(t, morphed, "no control started this job, so none may claim it")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProgressVocabularyIsHumanized exercises progress.js directly in
// the browser, which is the only runner this application has (no Node
// anywhere, by design). The pure module is worth pinning on its own: it
// turns ~90 core phase names into English by RULE rather than by table, so
// a phase core adds later still renders - and the rule is exactly the kind
// of thing a rendering assertion cannot distinguish from a lucky match.
func TestE2E_ProgressVocabularyIsHumanized(t *testing.T) {
	f := newE2EFixture(t)

	var got []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { humanizePhase, progressText, jobStateLabel, formatBytes } =
				await import("/static/app/progress.js");
			return [
				humanizePhase("deploy_deployed"),
				humanizePhase("deploy_merge_synced"),
				humanizePhase("install_dep_installing"),
				humanizePhase("import_archive_fetching"),
				humanizePhase("a_phase_core_adds_later"),
				humanizePhase(""),
				progressText({ type: "mod", phase: "deploy_deployed", mod_name: "Alpha Mod", index: 1, total: 2 }),
				progressText({ type: "download", phase: "deploy_downloading", mod_name: "Alpha Mod", percent: 42.7, total_bytes: 1048576 }),
				progressText({ type: "download", phase: "deploy_downloading", downloaded: 3145728 }),
				jobStateLabel({ state: "running", event_count: 0 }),
				jobStateLabel({ state: "running", event_count: 0 }, { phase: "deploy_deployed" }),
				jobStateLabel({ state: "running", event_count: 3 }),
				jobStateLabel({ state: "failed" }),
				formatBytes(0),
			];
		})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	require.Len(t, got, 14)
	assert.Equal(t, "Deployed", got[0])
	assert.Equal(t, "Merge synced", got[1])
	assert.Equal(t, "Dependency installing", got[2],
		"a dependency phase must not read as if it were about the mod itself")
	assert.Equal(t, "Archive fetching", got[3])
	assert.Equal(t, "A phase core adds later", got[4],
		"an unknown phase must still render - core adds phases without the SPA")
	assert.Empty(t, got[5])
	assert.Equal(t, "Deployed · Alpha Mod · 1 of 2", got[6])
	assert.Equal(t, "Downloading · Alpha Mod · 43% of 1.0 MB", got[7])
	assert.Equal(t, "Downloading · 3.0 MB transferred", got[8],
		"a Content-Length-less download has no percent to show, only bytes")
	assert.Equal(t, "queued", got[9],
		"a running job that has emitted nothing has not started working (activity.go's heuristic)")
	assert.Equal(t, "running", got[10],
		"a frame proves the job is working even though its summary's event_count is frozen at 0")
	assert.Equal(t, "running", got[11])
	assert.Equal(t, "failed", got[12])
	assert.Empty(t, got[13])
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ResultTallyReadsAnOmittedFailedList pins progress.js#resultTally
// against the wire shape core actually emits, in the browser (the only
// runner this application has).
//
// The bug this exists for: resultTally discriminated a batch result on
// `applied` AND `failed` both being arrays, while
// core.UpdateBatchResult.Failed is `json:"failed,omitzero"` - so the exact
// document a locked-skip batch produces (applied rows, skipped rows, NO
// `failed` key at all) fell through to null, and the control that started
// the batch said a bare "Done" over a mod the engine had refused. The
// discriminator is `applied` alone: `json:"applied"` exists on no other
// core or serve type, and both omitzero lists default to 0.
func TestE2E_ResultTallyReadsAnOmittedFailedList(t *testing.T) {
	f := newE2EFixture(t)

	var got []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { resultTally, resultTallyLabel, resultTallyTone } =
				await import("/static/app/progress.js");
			// The shape cmd/lmm/testdata/json_golden/update_bulk_all.golden
			// carries: one applied row, one locked skip, and no "failed" key.
			const lockedSkip = {
				applied: [{ name: "Mod B", status: "updated" }],
				skipped: [{ name: "Mod A", status: "skipped", reason: "Mod A is locked at v1.0" }],
			};
			const allApplied = { applied: [{ name: "Mod B", status: "updated" }] };
			const oneFailed = {
				applied: [],
				failed: [{ mod: "test-src:modA", name: "Mod A", error: "boom" }],
			};
			const notABatch = { deployed: 3 };
			const describe = (r) => {
				const tally = resultTally(r);
				if (!tally) return "null";
				const notes = (tally.skippedNotes ?? []).join("; ");
				return resultTallyLabel(tally) + " [" + resultTallyTone(tally) + "] {" + notes + "}";
			};
			return [describe(lockedSkip), describe(allApplied), describe(oneFailed), describe(notABatch)];
		})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	require.Len(t, got, 4)
	assert.Equal(t, "1 applied / 1 skipped [succeeded] {Mod A — Mod A is locked at v1.0}", got[0],
		"a batch with no `failed` key must still tally, must call a lock a SKIP rather than a failure, and must carry the engine's own refusal sentence")
	assert.Equal(t, "1 applied / 0 failed [succeeded] {}", got[1],
		"an all-success batch reads exactly as it did before this fix")
	assert.Equal(t, "0 applied / 1 failed [failed] {}", got[2],
		"a real failure is still a failure, and a batch that applied nothing is not `mixed`")
	assert.Equal(t, "null", got[3],
		"a result that is not a batch at all still says Done rather than inventing a tally")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_TrayShowsARunningJobWithProgress opens the activity tray DURING a
// job, which is the state it exists for: the bell badge counts what is
// happening, the tray groups it under Running, and the row carries the same
// humanized phase the morphing control does - from the multiplexed session
// stream, which is open whether the tray is or not.
//
// The library's own header carries the live line too (the design's "cards
// show live counts"), asserted here because the two read the same frame and
// a divergence between them is the bug worth catching.
func TestE2E_TrayShowsARunningJobWithProgress(t *testing.T) {
	f := newE2EFixtureWithSlowDeploy(t)

	var badge, kind, detail, live string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress__bar:not(.job-progress__bar--indeterminate)`, chromedp.ByQuery),
		textContent(`.activity-bell__count`, &badge),
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__row[data-state="running"]`, chromedp.ByQuery),
		textContent(`.tray__row[data-state="running"] .tray__kind`, &kind),
		textContent(`.tray__row[data-state="running"] .tray__detail`, &detail),
		textContent(`.library__live`, &live),
	)

	assert.Equal(t, "1", badge, "the bell counts what is happening")
	assert.Equal(t, "deploy", kind)
	assert.Contains(t, detail, "of 2", "a running row carries its progress, not just its state")
	assert.Contains(t, live, "of 2", "the library's live line reads the same frame")
	assert.Contains(t, live, "Deploying", "M3: the header line must also NAME the kind (mutationLabel), not just carry the phase text")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_QueuedJobDoesNotRenderRunningProgress is M2: a job the registry
// reports as state "running" is not necessarily WORKING - jobStateLabel
// (progress.js) reads "queued" for one that has emitted nothing yet, which
// is core's beginOp serialising mutations by blocking rather than
// rejecting. Before the fix, TrayRow gated its progress block on the raw
// job.state, so a queued row rendered an indeterminate bar captioned
// "Working…" directly under its own "Queued" heading - a lie about what was
// actually happening (nothing).
//
// The queued job here is an enable (kind_toggle.go): it takes no
// core.EventSink at all, so once it does start running it STILL never gets
// a frame, staying "queued" by the same heuristic for its whole (otherwise
// near-instant) life - which is what makes the assertion window here the
// deploy's AfterEach sleep (newE2EFixtureWithQueuedToggle), not a race
// against the enable itself.
func TestE2E_QueuedJobDoesNotRenderRunningProgress(t *testing.T) {
	f := newE2EFixtureWithQueuedToggle(t)

	var jobID string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		// Wait for the modal to actually leave the DOM before doing
		// anything else: its scrim covers the whole viewport, and a click
		// issued while it is still present - even mid-teardown - can be
		// intercepted by the scrim instead of landing on the element
		// underneath it (observed: the bell click silently opened nothing).
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		// The deploy now holds core's one mutation slot for the duration of
		// its AfterEach sleep. Start a second, wholly independent job while
		// it does - the only way this enable can be blocked in beginOp,
		// which is the real (not simulated) condition "queued" exists for.
		chromedp.ActionFunc(func(context.Context) error {
			jobID = startEnableFromAnotherClient(t, f, "fake", "c")
			return nil
		}),
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__row[data-state="queued"]`, chromedp.ByQuery),
	)

	// Scoped by the enable job's OWN id rather than "the first queued row":
	// the slow deploy can still be queued too at this instant (it has not
	// necessarily emitted its own first event yet), and row order is an
	// implementation detail this assertion should not depend on.
	rowSel := `.tray__row[data-job="` + jobID + `"]`
	var kind, rowText, sectionTitle string
	var progressBlocks int
	f.runInBrowser(t,
		chromedp.WaitVisible(rowSel, chromedp.ByQuery),
		textContent(rowSel+` .tray__kind`, &kind),
		textContent(rowSel, &rowText),
		chromedp.Evaluate(
			`document.querySelectorAll('`+rowSel+` .tray__progress').length`,
			&progressBlocks),
		chromedp.Evaluate(
			`document.querySelector('`+rowSel+`').closest('.tray__section').querySelector('.tray__section-title').textContent`,
			&sectionTitle),
	)

	assert.Equal(t, "enable", kind)
	assert.Contains(t, sectionTitle, "Queued")
	assert.Zero(t, progressBlocks, "a queued row must not render the running-progress block at all")
	assert.NotContains(t, rowText, "Working…", `a row under "Queued" must never read "Working…"`)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_TrayEntryExpandsToTheEventStream is the "full path one click
// away" half: an entry opens onto that job's own phase-by-phase events,
// which is a SECOND stream (GET /api/v1/jobs/{id}/events) opened only for
// the entry that was opened. A finished job replays its retained ring, so
// this is history as much as it is live progress.
func TestE2E_TrayEntryExpandsToTheEventStream(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	var events []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__row[data-state="succeeded"] .tray__summary`, chromedp.ByQuery),
		chromedp.Click(`.tray__row[data-state="succeeded"] .tray__summary`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__event:not(.tray__event--empty)`, chromedp.ByQuery),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".tray__event")).map(e => e.textContent.trim())`, &events),
	)

	require.NotEmpty(t, events)
	joined := strings.Join(events, "\n")
	assert.Contains(t, joined, "Alpha Mod")
	assert.Contains(t, joined, "Deployed")
	assert.NotContains(t, joined, "deploy_deployed", "the wire phase name must not reach the screen")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_TrayDeepLinkOpensOnTheNamedJob closes the carry-in loop: the
// deleted /jobs/{id} page's 301 now lands on the home URL annotated with
// ?job=, and that annotation opens the tray with that entry already
// expanded. The redirect is followed by the browser here, so this proves
// the Go half and the SPA half agree on the annotation's name.
func TestE2E_TrayDeepLinkOpensOnTheNamedJob(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	jobID := startDeployFromAnotherClient(t, f)

	var url, expanded string
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/jobs/"+jobID),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__events[data-job="`+jobID+`"]`, chromedp.ByQuery),
		chromedp.Location(&url),
		chromedp.AttributeValue(`.tray__row .tray__summary`, "aria-expanded", &expanded, nil, chromedp.ByQuery),
	)

	assert.Contains(t, url, "?job="+jobID, "the 301 must carry the id into the SPA's own scheme")
	assert.Equal(t, "true", expanded)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_TrayDeepLinkToAForgottenJobSaysSo is the honest-failure half of
// the deep link. The registry retains a bounded number of jobs and evicts
// silently by design (activity.go: "EVICTION HAS NO FRAME OF ITS OWN"), so
// a bookmarked ?job= will eventually name a job nobody has any more. An
// empty tray would read as "nothing ever happened".
func TestE2E_TrayDeepLinkToAForgottenJobSaysSo(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	var message string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"?job=deadbeefdeadbeefdeadbeefdeadbeef"),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__error`, chromedp.ByQuery),
		textContent(`.tray__error`, &message),
	)

	assert.Contains(t, message, "no longer retained")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FailedJobSurfacesInPlaceAndInTheTray covers the failure half of
// the morph and of the tray at once: a deploy that core refuses to run (a
// before_all hook exits non-zero, and without --force that is a hard stop)
// resurfaces on the control that started it AND is listed under the tray's
// Failed section with the envelope's own message - never as a silent
// non-event, and never as a success.
func TestE2E_FailedJobSurfacesInPlaceAndInTheTray(t *testing.T) {
	f := newE2EFixtureWithFailingDeploy(t)

	var inline, trayMessage, badge string
	var overwrites, toasts int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="failed"]`, chromedp.ByQuery),
		textContent(`.job-progress__text`, &inline),
		textContent(`.activity-bell__count`, &badge),
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__row[data-state="failed"]`, chromedp.ByQuery),
		textContent(`.tray__failure-message`, &trayMessage),
		chromedp.Evaluate(`document.querySelectorAll('[data-action="overwrite"]').length`, &overwrites),
		chromedp.Evaluate(`document.querySelectorAll(".toast").length`, &toasts),
	)

	assert.Contains(t, inline, "Failed")
	assert.Contains(t, inline, "before_all", "the inline readout carries the envelope's own message")
	assert.Contains(t, trayMessage, "before_all")
	assert.Equal(t, "1", badge, "with nothing running, the bell counts the failure nobody has seen")
	assert.Zero(t, overwrites,
		"this failure's details name no action, so no affordance may be invented for it")
	assert.Zero(t, toasts,
		"the control is on screen, so the failure must resurface there and NOT also toast - "+
			"this deploy fails before its own start response is read, which is how the "+
			"origin binding came to be checked too early")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FailureNextStepIsDecidedByTypedDetails pins failures.js directly.
// A conflict's next step must come from the envelope's typed `details` -
// core's own Details() extension point - and never from matching on the
// message text, which is prose and changes. The envelope below is the exact
// shape testdata/json/job_summary.golden pins for a failed install.
//
// Issue 331 landed the action live (main.js's retryInstallOverwrite): what
// was "present but not live until install lands" is now a real, enabled
// affordance with no `pending` reason - and nextStepFor now takes the whole
// job (job.kind gates the action to install, the one kind whose
// ConflictError this implies a next step for; a conflict shape attached to
// any OTHER kind - which core never actually produces today - must still
// render no invented affordance).
func TestE2E_FailureNextStepIsDecidedByTypedDetails(t *testing.T) {
	f := newE2EFixture(t)

	var got []any
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { nextStepFor } = await import("/static/app/failures.js");
			const conflictDetails = { conflicts: [
				{ relative_path: "Mods/a.pak", current_source_id: "fake", current_mod_id: "m9" },
			] };
			const conflict = nextStepFor({
				kind: "install",
				error: { error: "file conflicts detected", details: conflictDetails },
			});
			return [
				conflict.action,
				conflict.label,
				"pending" in conflict,
				nextStepFor({ kind: "install", error: { error: "install.before_all hook failed" } }),
				nextStepFor({ kind: "install", error: { error: "x", details: { conflicts: [] } } }),
				nextStepFor({ kind: "deploy", error: { error: "x", details: conflictDetails } }),
				nextStepFor(undefined),
			];
		})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	require.Len(t, got, 7)
	assert.Equal(t, "overwrite", got[0])
	assert.Equal(t, "Overwrite 1 file?", got[1])
	assert.Equal(t, false, got[2], "the affordance is live now - no pending reason left to carry")
	assert.Nil(t, got[3], "a failure whose details name no action gets no invented affordance")
	assert.Nil(t, got[4], "an empty conflict list is not a conflict")
	assert.Nil(t, got[5], "the action is gated to install - the only kind core's ConflictError comes from")
	assert.Nil(t, got[6])
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_UnseenFailureBadgeIgnoresAJobWithNoEndTime is M5: the real,
// unmodified ActivityBell rendered directly (the same pure-module idiom
// TestE2E_FailureNextStepIsDecidedByTypedDetails uses one layer down, here
// applied to a component instead of a bare function) against a failed job
// with no ended_at at all - a shape the real server never sends today (the
// registry always sets it on a failed job), but exactly the shape the old
// "|| 0" fallback existed to guard.
//
// Date.parse(undefined || 0) parses "0", which V8 reads as 2000-01-01 - a
// large positive timestamp that reads as "unseen" no matter what
// acknowledgedAt is, so the badge showed "1" for a job that never proved
// anything. Date.parse(undefined) is NaN, and NaN > acknowledgedAt is
// false: the honest default, and the badge renders nothing at all (a
// picker's count > 0 check, tray.js's ActivityBell).
func TestE2E_UnseenFailureBadgeIgnoresAJobWithNoEndTime(t *testing.T) {
	f := newE2EFixture(t)

	var badge string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { render, h } = await import("/static/app/render.js");
			const { ActivityBell } = await import("/static/app/components/tray.js");

			const container = document.createElement("div");
			document.body.appendChild(container);

			const state = {
				jobsIndex: [{ id: "j1", kind: "deploy", state: "failed" }],
				jobProgress: {},
			};
			render(h(ActivityBell, {
				state, open: false, onOpen: () => {}, onClose: () => {},
			}), container);

			const text = container.querySelector(".activity-bell__count")?.textContent ?? "";
			container.remove();
			return text;
		})()`, &badge, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	assert.Empty(t, badge,
		"a failed job with no readable end time must not be invented as unseen")
	assert.Empty(t, f.BrowserErrors())
}

// --- issue 330 (Unit 4): the slide-over, the full mod page, and the mod
// mutations both surfaces wire. ---

// clickModRow drives a browser click on the library row whose name is
// exactly name, found by text rather than position - the fixtures below
// seed more than one mod and ListMods' own ordering (a mod absent from the
// profile's load order sorts first, core/queries.go) is not something a
// scenario about WHICH mod opens should have to depend on.
func clickModRow(name string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`
		Array.from(document.querySelectorAll(".mod-row__name"))
			.find((b) => b.textContent.includes(%q))
			.click();
	`, name), nil)
}

// waitForPanelFocus waits until the slide-over panel is document.
// activeElement - modpanel.js focuses it in a useEffect that can still be
// pending on the very next chromedp action even after the panel itself is
// visible, and a key event (Escape, an arrow) sent before that settles is
// a real, observed race. Polls document.activeElement directly rather
// than the CSS ":focus" pseudo-class: headless Chrome can leave a window
// without OS-level focus, where ":focus" never matches even though
// .focus() genuinely made the element document.activeElement (a known
// headless quirk, confirmed against this exact page).
func waitForPanelFocus() chromedp.Action {
	return chromedp.Poll(
		`document.activeElement && document.activeElement.classList.contains("slide-over__panel")`,
		nil,
	)
}

// TestE2E_SlideOver_OpensWithModInfoAndDeepLinkWorks proves the slide-over
// renders the design's own inventory from a row click (name, author,
// version, summary) and that a fresh navigation straight to the ?mod= URL
// opens on the same panel - a bookmark or a shared link, not just a click
// in an already-loaded page.
func TestE2E_SlideOver_OpensWithModInfoAndDeepLinkWorks(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	var body string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		clickModRow("Alpha Mod"),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		textContent(`.slide-over__panel`, &body),
	)
	assert.Contains(t, body, "Alpha Mod")
	assert.Contains(t, body, "Ada Lovelace")
	assert.Contains(t, body, "1.0")
	assert.Contains(t, body, "A tidy little mod.", "the summary prose renders from ModListing, zero-fetch")

	var deepLinkBody string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "a")),
		// NOT `.slide-over` alone: on a cold load ModPanel renders a
		// `.slide-over` immediately either way, and until /api/v1/mods
		// resolves `rows` is still empty, so the FIRST paint is the "not
		// in this profile's library" not-found state - a real race,
		// observed under `go test -race` (which slows the server enough
		// to widen the window). `.slide-over__nav` only renders once a
		// row was actually found.
		chromedp.WaitVisible(`.slide-over__nav`, chromedp.ByQuery),
		textContent(`.slide-over__panel`, &deepLinkBody),
	)
	assert.Contains(t, deepLinkBody, "Alpha Mod")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SlideOver_EscapeClosesAndOutsideClickCloses covers the design
// doc's own words for how the slide-over closes: "Esc / outside click".
func TestE2E_SlideOver_EscapeClosesAndOutsideClickCloses(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		clickModRow("Alpha Mod"),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		waitForPanelFocus(),
		chromedp.KeyEvent(kb.Escape),
		waitGone(`.slide-over`),
	)

	f.runInBrowser(t,
		clickModRow("Alpha Mod"),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		// A fixed point near the top-left corner: the panel is right-
		// aligned at ~40% width (app.css), so this is scrim regardless of
		// viewport size - unlike clicking the ".slide-over" selector
		// itself, whose node-center click could land inside the panel on
		// a narrow viewport.
		chromedp.MouseClickXY(20, 20),
		waitGone(`.slide-over`),
	)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SlideOver_ArrowKeysStepThroughVisibleList proves </-> move
// between mods in the library's own current (sorted) order, and that
// arriving at either end disables the matching step button.
func TestE2E_SlideOver_ArrowKeysStepThroughVisibleList(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	var firstName string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		// A deterministic order to step through: sort by name.
		chromedp.SetValue(`select[name="sort"]`, "name", chromedp.ByQuery),
		clickModRow("Alpha Mod"),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		// See TestE2E_SlideOver_EscapeClosesAndOutsideClickCloses's own
		// comment: a key event sent before the panel's focus effect has
		// settled is a real race.
		waitForPanelFocus(),
		textContent(`.slide-over .section-header`, &firstName),
	)
	assert.Contains(t, firstName, "Alpha Mod")

	var afterRight string
	f.runInBrowser(t,
		chromedp.KeyEvent(kb.ArrowRight),
		textContent(`.slide-over .section-header`, &afterRight),
	)
	assert.Contains(t, afterRight, "Beta Mod", "-> must step to the NEXT mod in the sorted list")

	var afterLeft string
	f.runInBrowser(t,
		chromedp.KeyEvent(kb.ArrowLeft),
		textContent(`.slide-over .section-header`, &afterLeft),
	)
	assert.Contains(t, afterLeft, "Alpha Mod", "<- must step back to the PREVIOUS mod")

	var prevDisabled bool
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelector(".slide-over__step[aria-label='Previous mod']").disabled`, &prevDisabled),
	)
	assert.True(t, prevDisabled, "the first mod in the list has no previous mod to step to")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SlideOver_LockAndPolicyEditsPersistThroughTheAPI proves the
// slide-over's editable lock + update-policy controls are real mutations,
// not local-only UI state: reloading the page must still show them.
func TestE2E_SlideOver_LockAndPolicyEditsPersistThroughTheAPI(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		clickModRow("Alpha Mod"),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		chromedp.Click(`.slide-over__settings input[type="checkbox"]`, chromedp.ByQuery),
		// The lock write is a plain POST with no job/morph to wait on -
		// waiting for the checkbox to actually reflect the persisted value
		// (rather than a fixed sleep) is what proves the round trip
		// completed. It also re-enables the policy select below: both
		// controls share one busy flag (ModSettingsControls), so a second
		// edit fired while the first is still in flight would land on a
		// disabled control.
		chromedp.WaitVisible(`.slide-over__settings input[type="checkbox"]:checked`, chromedp.ByQuery),
		// M4: the checkbox's own `disabled` attribute (set while the write
		// is in flight) blurs it the instant the browser applies it, and
		// focus never returns on its own - waitForPanelFocus() is the
		// reviewer's own activeElement probe, pinning that the panel
		// reclaims focus once the write settles.
		waitForPanelFocus(),
		chromedp.SetValue(`.slide-over__settings select`, "pinned", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector(".slide-over__settings select").value === "pinned"`, nil),
		waitForPanelFocus(),
	)

	result, err := f.Svc.ModDetail(t.Context(), f.Game, f.Profile, "fake", "a")
	require.NoError(t, err)
	require.NotNil(t, result.Installed)
	assert.True(t, result.Installed.Locked, "the lock write must have reached the server")
	assert.Equal(t, domain.UpdatePinned, result.Installed.UpdatePolicy)

	// Reload proves it is not merely in-memory store state.
	var checked bool
	var policy string
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "a")),
		chromedp.WaitVisible(`.slide-over__settings`, chromedp.ByQuery),
		chromedp.WaitVisible(`.slide-over__settings input[type="checkbox"]:checked`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector(".slide-over__settings input[type='checkbox']").checked`, &checked),
		chromedp.Value(`.slide-over__settings select`, &policy, chromedp.ByQuery),
	)
	assert.True(t, checked)
	assert.Equal(t, "pinned", policy)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SlideOver_EnableDisableMorphsInline proves the slide-over's
// Enable/Disable toggle morphs to "Done" inline once the job succeeds -
// pinning the fix for a real bug this unit's own gate found: the control's
// origin used to be keyed on the CURRENT direction ("enable" vs "disable"),
// which flips the instant the toggle succeeds and the row re-hydrates, so
// the very next render looked up a DIFFERENT origin than the one the job
// was bound to and never found it - the button silently reverted to idle
// instead of showing its own outcome. The origin is a stable "toggle" now.
func TestE2E_SlideOver_EnableDisableMorphsInline(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		clickModRow("Alpha Mod"),
		chromedp.WaitVisible(`.slide-over__nav`, chromedp.ByQuery),
	)

	var label string
	f.runInBrowser(t, textContent(`.slide-over__actions button`, &label))
	require.Equal(t, "Disable", label, "Alpha Mod is seeded enabled")

	f.runInBrowser(t,
		chromedp.Click(`.slide-over__actions button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	mod, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SlideOver_UninstallThroughTheModal_RemovesFromDisk drives the
// confirm-plan framework's second registered kind end to end: Uninstall
// opens the modal rendering UninstallPlanView, Confirm runs it as a job,
// and the mod is genuinely gone from the game directory afterward.
func TestE2E_SlideOver_UninstallThroughTheModal_RemovesFromDisk(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)
	_, err := f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	deployedPath := filepath.Join(f.Game.ModPath, "alpha.esp")
	require.FileExists(t, deployedPath)

	var planBody string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		clickModRow("Alpha Mod"),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
	)
	// The uninstall button carries no data-action of its own (only the
	// modal's footer buttons do) - it is found by its own text instead.
	f.runInBrowser(t,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".slide-over__actions button"))
				.find((b) => b.textContent.trim() === "Uninstall").click();
		`, nil),
		chromedp.WaitVisible(`.modal[data-kind="uninstall"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="uninstall"]`, &planBody),
	)
	assert.Contains(t, planBody, "Alpha Mod")
	assert.Contains(t, planBody, "alpha.esp", "UninstallPlanView names the files it would remove")
	// I2: pins the registration itself, not just what its output happens to
	// say - GenericPlanView's raw DocumentView would satisfy every
	// assertion above too, which is exactly how this went unpinned.
	var uninstallRendererPresent bool
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelector('.modal[data-kind="uninstall"] .plan--uninstall') !== null`, &uninstallRendererPresent),
	)
	assert.True(t, uninstallRendererPresent, "UninstallPlanView, not GenericPlanView, must be what's registered for this kind")

	f.runInBrowser(t,
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
	)

	// The end state is asserted on the SERVICE, polled, rather than on any
	// one DOM state after Confirm: uninstalling removes the mod from
	// /api/v1/mods, so main.js's post-job re-hydrate can beat (or lose to)
	// the SSE job_done frame that would otherwise let the button morph to
	// "Done" inline - whichever wins, the row (and the InlineJob wrapping
	// the button that started it) is gone from the panel within the same
	// instant the job itself finishes, leaving no single DOM state a test
	// can reliably wait on. The mod being gone from the SERVICE - the fact
	// that actually matters - is not racy.
	require.Eventually(t, func() bool {
		_, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
		return errors.Is(err, domain.ErrModNotFound)
	}, 5*time.Second, 20*time.Millisecond, "the uninstall job must remove the mod")

	assert.NoFileExists(t, deployedPath)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FullModPage_RollbackRoundTrip drives the new "rollback" plan kind
// end to end from the full mod page's versions section: the button opens
// the confirm modal rendering RollbackPlanView, Confirm runs it as a job,
// and the mod's version and deployed content both genuinely revert.
func TestE2E_FullModPage_RollbackRoundTrip(t *testing.T) {
	f := newE2EFixtureWithRollbackReadyMod(t)
	deployedPath := filepath.Join(f.Game.ModPath, "alpha.esp")
	before, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "new content", string(before))

	var planBody string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`.mod-page`, chromedp.ByQuery),
		// The rollback button lives in VersionsSection, which starts in a
		// "Loading versions…" state (modPage.versions is fetched
		// separately from the page's primary ModFiles read) - waiting for
		// its own text is what waits out that fetch rather than clicking
		// nothing.
		chromedp.Poll(`Array.from(document.querySelectorAll("button")).some((b) => b.textContent.trim() === "Roll back to the previous version")`, nil),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll("button"))
				.find((b) => b.textContent.trim() === "Roll back to the previous version").click();
		`, nil),
		chromedp.WaitVisible(`.modal[data-kind="rollback"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="rollback"]`, &planBody),
	)
	assert.Contains(t, planBody, "2.0")
	assert.Contains(t, planBody, "1.0")
	// I2: pins the registration itself, not just what its output happens to
	// say - GenericPlanView's raw DocumentView would satisfy the two
	// assertions above too, which is exactly how this went unpinned.
	var rollbackRendererPresent bool
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelector('.modal[data-kind="rollback"] .plan--rollback') !== null`, &rollbackRendererPresent),
	)
	assert.True(t, rollbackRendererPresent, "RollbackPlanView, not GenericPlanView, must be what's registered for this kind")

	f.runInBrowser(t,
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	mod, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", mod.Version)
	after, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	assert.Equal(t, "old content", string(after))
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FullModPage_RendersFilesAndVersions proves the files table (core.
// ModFilesReport) and the versions table (the AvailableModVersions wrapper,
// #97's first real consumer) both render real per-mod data.
func TestE2E_FullModPage_RendersFilesAndVersions(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)
	// ModFiles reports DEPLOYED paths (GetDeployedFilesForMod), not merely
	// cached ones - a mod cached but never deployed shows an empty table.
	_, err := f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	var filesBody, versionsBody string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`.mod-page__table`, chromedp.ByQuery),
		textContent(`.mod-page`, &filesBody),
	)
	assert.Contains(t, filesBody, "alpha.esp")
	assert.Contains(t, filesBody, "Files")
	assert.Contains(t, filesBody, "Versions")

	f.runInBrowser(t, textContent(`.mod-page`, &versionsBody))
	assert.Contains(t, versionsBody, "1.0")
	assert.Contains(t, versionsBody, "2.0")
	assert.Contains(t, versionsBody, "installed")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FullModPage_EnableDisableWorks proves issue 330's explicit task
// brief - enable/disable are wired "from slide-over + full page" - holds
// on the full page too: a direct deep link into it can toggle the mod
// without a detour back through the slide-over.
func TestE2E_FullModPage_EnableDisableWorks(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	var label string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`.mod-page`, chromedp.ByQuery),
		textContent(`.mod-page__section button`, &label),
	)
	require.Equal(t, "Disable", label, "Alpha Mod is seeded enabled")

	f.runInBrowser(t,
		chromedp.Click(`.mod-page__section button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	mod, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.Enabled, "the disable job must have reached the server")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FullModPage_JobHistoryListsFinishedJobs proves the job-history
// section (jobhistory.js's own first consumer of api.js#jobStatus) lists a
// finished rollback job for the mod it concerned.
func TestE2E_FullModPage_JobHistoryListsFinishedJobs(t *testing.T) {
	f := newE2EFixtureWithRollbackReadyMod(t)
	post := postAsAnotherClient(t, f)
	plan := post("/api/v1/plans/rollback?game="+f.Game.ID+"&profile="+f.Profile, `{"source_id":"fake","mod_id":"a"}`)
	planID, ok := plan["plan_id"].(string)
	require.True(t, ok, "%v", plan)
	job := post("/api/v1/jobs", `{"plan_id":"`+planID+`"}`)
	_, ok = job["job_id"].(string)
	require.True(t, ok, "%v", job)

	// The mod page is navigated to AFTER the job has been started but with
	// no wait for it to finish - the job history section's own effect
	// re-runs on every jobsIndex change (jobhistory.js), so a still-running
	// job that finishes moments later, over the activity stream this page
	// already subscribes to at boot, still lands here. `.mod-page li` only
	// ever appears once the job history has a real entry (this fixture's
	// mod carries no dependencies, the page's other <li>-bearing section),
	// so waiting for it also waits out that race rather than sampling too
	// early.
	var body string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`.mod-page`, chromedp.ByQuery),
		chromedp.WaitVisible(`.mod-page li`, chromedp.ByQuery),
		textContent(`.mod-page`, &body),
	)
	assert.Contains(t, body, "Rolling back", "the job history names the mutation kind")
	assert.NotContains(t, body, "No update or rollback jobs recorded")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_UpdatesBatch_DropsARowAndAppliesTheRest replaces the retired
// TestE2E_UpdatesKind_RendersThroughGenericPlanView (issue 330 carry-2's own
// placeholder): "updates" now gets its real renderer (plan_updates.js,
// issue 332) instead of the GenericPlanView fallback that placeholder
// pinned - checkboxes to drop a row, not raw DocumentView. The library
// batch bar selects two mods with an update each; the modal opens with both
// checked, unchecking one re-plans down to the other, and Confirm applies
// only the mod that stayed checked - the drop is real, not cosmetic.
func TestE2E_UpdatesBatch_DropsARowAndAppliesTheRest(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "2.0"},
		Files: []domain.DownloadableFile{{ID: "fa", Version: "2.0", IsPrimary: true}},
	})
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "b", SourceID: "fake", Name: "Beta Mod", Version: "2.0"},
		Files: []domain.DownloadableFile{{ID: "fb", Version: "2.0", IsPrimary: true}},
	})
	f := newE2EFixtureFromSource(t, src)
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", GameID: f.Game.ID},
		true, map[string][]byte{"alpha.esp": []byte("alpha")})
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "b", SourceID: "fake", Name: "Beta Mod", Version: "1.0", GameID: f.Game.ID},
		true, map[string][]byte{"beta.esp": []byte("beta")})
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "a", Version: "1.0"}))
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "b", Version: "1.0"}))

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		// Select every row's own batch checkbox - two mods, two boxes.
		chromedp.Evaluate(`
			document.querySelectorAll(".mod-row td.col--select input")
				.forEach((cb) => cb.click());
		`, nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Click(`[data-action="batch-update"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="updates-batch-rows"]`, chromedp.ByQuery),
	)

	var rowsText string
	var rendererPresent bool
	f.runInBrowser(t,
		textContent(`[data-testid="updates-batch-rows"]`, &rowsText),
		chromedp.Evaluate(`document.querySelector('.modal[data-kind="updates"] .plan--updates') !== null`, &rendererPresent),
	)
	assert.Contains(t, rowsText, "Alpha Mod")
	assert.Contains(t, rowsText, "Beta Mod")
	assert.True(t, rendererPresent, "UpdatesBatchPlanView, not GenericPlanView, must be what's registered for this kind")

	// Drop Beta by unchecking ITS OWN row - the re-plan this fires must
	// leave the modal open with only Alpha's row left.
	f.runInBrowser(t,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('[data-testid="updates-batch-rows"] input[type=checkbox]'))
				.find((cb) => cb.closest("li").textContent.includes("Beta Mod")).click();
		`, nil),
		// Null-safe: the row list briefly unmounts while the re-plan this
		// click fires is in flight (confirmplan.js renders "Computing the
		// plan…" during that window), so a bare .textContent read here would
		// throw against a null element mid-transition rather than just
		// polling again.
		chromedp.Poll(`(() => {
			const el = document.querySelector('[data-testid="updates-batch-rows"]');
			return el !== null && !el.textContent.includes("Beta Mod");
		})()`, nil),
	)
	f.runInBrowser(t,
		textContent(`[data-testid="updates-batch-rows"]`, &rowsText),
	)
	assert.Contains(t, rowsText, "Alpha Mod")
	assert.NotContains(t, rowsText, "Beta Mod", "the dropped row must actually leave the re-planned document")

	f.runInBrowser(t,
		chromedp.Click(`.modal[data-kind="updates"] [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal[data-kind="updates"]`, chromedp.ByQuery),
	)

	// The end state is asserted on the job's own RESULT document, not the
	// installed mod's version: this suite's shared fakeSource.GetDownloadURL
	// (testhelpers_test.go) always answers source.ErrNotSupported, so the
	// attempt this fixture can genuinely prove is "was Alpha the only mod
	// the batch TRIED" - Beta's own exclusion (never attempted, so it
	// appears in neither applied nor failed) IS the drop mechanic this test
	// exists to prove, independent of whether the download itself succeeds.
	var jobDetail string
	require.Eventually(t, func() bool {
		f.runInBrowser(t, chromedp.Evaluate(`
			fetch('/api/v1/jobs').then(r=>r.json())
				.then(j => fetch('/api/v1/jobs/'+j.jobs[0].id))
				.then(r=>r.text())
		`, &jobDetail, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
		return strings.Contains(jobDetail, `"state": "succeeded"`) || strings.Contains(jobDetail, `"state": "failed"`)
	}, 5*time.Second, 100*time.Millisecond, "the batch job must reach a terminal state")

	assert.Contains(t, jobDetail, "fake:a", "the row that stayed checked must have been attempted")
	assert.NotContains(t, jobDetail, "fake:b", "the dropped row must appear in neither applied nor failed - it was never attempted")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FullModPage_VersionsTableUpdateButtonTargetsTheCheckedVersion is
// C1: the versions table's own header comment (fullmodpage.js) promises an
// Update action on exactly the row whose version matches the version
// CheckGameUpdates actually found - before the fix every non-installed row
// got an identically-wired button that planned whatever the check found
// regardless of which row's button was clicked. Reproduced exactly the
// review's own repro: 1.0 installed, 2.0/3.0 available, the check finds
// 3.0 (fakeSource.CheckUpdates: catalog Mod.Version vs installed.Version).
func TestE2E_FullModPage_VersionsTableUpdateButtonTargetsTheCheckedVersion(t *testing.T) {
	f := newE2EFixtureWithThreeVersionsAndACheckedUpdate(t)

	var buttonCount int
	var rowsText string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`.mod-page`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelectorAll(".mod-page__table tbody tr").length === 3`, nil),
		chromedp.Evaluate(`document.querySelectorAll(".mod-page__table tbody button").length`, &buttonCount),
		textContent(`.mod-page__table`, &rowsText),
	)
	assert.Contains(t, rowsText, "2.0")
	assert.Contains(t, rowsText, "3.0")
	assert.Equal(t, 1, buttonCount, "only the row matching the checked update target (3.0) may offer a button - 2.0 is informational, core cannot land there")

	var buttonLabel string
	f.runInBrowser(t, textContent(`.mod-page__table tbody button`, &buttonLabel))
	assert.Equal(t, "Update to 3.0", buttonLabel)

	var modalTitle, planBody string
	f.runInBrowser(t,
		chromedp.Click(`.mod-page__table tbody button`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		textContent(`.modal__title`, &modalTitle),
		textContent(`.modal[data-kind="updates"]`, &planBody),
	)
	assert.Equal(t, "Update to 3.0", modalTitle)
	assert.Contains(t, planBody, "3.0", "the plan must name the version it will actually apply")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FullModPage_JobCompletionDoesNotDropToLoading is I1:
// onJobDone (main.js) re-hydrates the current route after EVERY job
// completion, not just a route change - for the full mod page that reaches
// hydrateModPage, whose first write used to blank filesReport
// unconditionally, tripping the page's own loading guard and unmounting
// everything on screen (including the InlineJob readout the user is
// looking at) for the duration of the re-fetch. A MutationObserver watches
// for the EXACT markup the top-level guard renders (`.app-main` with a
// DIRECT `.app-booting` child - VersionsTable's own "Loading versions…" is
// nested inside `.mod-page__section` and never means the page unmounted),
// since a poll taken after the fact could miss a flash this short.
func TestE2E_FullModPage_JobCompletionDoesNotDropToLoading(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	var flashed bool
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`.mod-page`, chromedp.ByQuery),
		chromedp.Evaluate(`
			window.__modPageFlashed = false;
			window.__modPageObserver = new MutationObserver(() => {
				if (document.querySelector(".app-main > .app-booting")) {
					window.__modPageFlashed = true;
				}
			});
			window.__modPageObserver.observe(document.getElementById("app"), { childList: true, subtree: true });
		`, nil),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-page__section button"))
				.find((b) => ["Enable", "Disable"].includes(b.textContent.trim()))
				.click();
		`, nil),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
		// onJobDone's own re-hydrate is a real (if fast) network round trip
		// against the in-process test server - this waits it out; the
		// MutationObserver above is what actually catches a flash.
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(`window.__modPageFlashed`, &flashed),
	)
	assert.False(t, flashed, "a job completion must not blank the full mod page back to its Loading state")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FullModPage_RollbackHiddenWithNoPreviousVersion is M1: a mod that
// has never been updated (no PreviousVersion) has nothing core can roll it
// back to - the button used to render anyway, opening a plan that failed
// with PlanRollback's own honest "no previous version available" error,
// but only after presenting a fully clickable action as if it worked. The
// page's PRIMARY read (ModFilesReport.Mod, a full domain.InstalledMod)
// already carries previous_version when there is one - a plain fixture mod
// with none must show no rollback control at all.
func TestE2E_FullModPage_RollbackHiddenWithNoPreviousVersion(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	var present bool
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`.mod-page`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelectorAll(".mod-page__table tbody tr").length > 0`, nil),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll("button"))
				.some((b) => b.textContent.trim() === "Roll back to the previous version")
		`, &present),
	)
	assert.False(t, present, "a mod with no PreviousVersion must not offer a rollback control at all")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SlideOver_ClosingMidJobLeavesTheRowsLiveLine is M3's row-level
// half: closing the panel while one of its own mutations is still running
// must not lose the mutation's own live indication - it moves from the
// panel's InlineJob onto the row it concerns
// (modrows.js#runningMutations), which only "the slide-over covers the
// library while open" (issue 330 carry-3's own note) leaves unreachable
// any other way.
func TestE2E_SlideOver_ClosingMidJobLeavesTheRowsLiveLine(t *testing.T) {
	f := newE2EFixtureWithDrillInModsAndSlowDeploy(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		// Holds core's one mutation slot for the AfterEach sleep - the same
		// lever newE2EFixtureWithQueuedToggle uses to make "running with no
		// progress" a real, driveable window rather than a race against a
		// toggle that finishes in microseconds.
		chromedp.ActionFunc(func(context.Context) error {
			startDeployFromAnotherClient(t, f)
			return nil
		}),
		clickModRow("Alpha Mod"),
		chromedp.WaitVisible(`.slide-over__nav`, chromedp.ByQuery),
		chromedp.Click(`.slide-over__actions button`, chromedp.ByQuery),
		// The toggle is now blocked in beginOp behind the deploy - its
		// origin is bound (the control has morphed) while it sits queued.
		chromedp.WaitVisible(`.job-progress`, chromedp.ByQuery),
		chromedp.Click(`.slide-over__close`, chromedp.ByQuery),
		waitGone(`.slide-over`),
	)

	var rowLive string
	f.runInBrowser(t,
		chromedp.WaitVisible(`.mod-row__live`, chromedp.ByQuery),
		textContent(`.mod-row__live`, &rowLive),
	)
	assert.Contains(t, rowLive, "Disabling", "the row must name the mutation, not just show a bare dot")
	assert.Empty(t, f.BrowserErrors())
}

// --- issue 331 (Unit 5): search and install - the omnibar's live filter and
// fan-out, the dedicated search page, and installing (with a version pick
// and a conflict round trip) through the confirm-plan framework's install
// renderer. Fixtures live in e2e_harness_test.go (newE2EFixtureWithSearchableMods,
// newE2EFixtureWithManySearchResults). ---

// searchResultRow finds the "From sources"/search-page row for sourceID/modID.
func searchResultRow(sourceID, modID string) string {
	return fmt.Sprintf(`.search-result[data-mod=%q]`, sourceID+"/"+modID)
}

// TestE2E_OmnibarLiveFilterNarrowsLibraryWithoutFanningOut proves typing in
// the omnibar still only narrows the INSTALLED library (design doc §Search:
// "typing live-filters the library") and never fans out on its own - the
// fan-out is Enter (or the "search sources" button), a deliberate second
// step, not a side effect of every keystroke.
func TestE2E_OmnibarLiveFilterNarrowsLibraryWithoutFanningOut(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	var header string
	var fanoutPresent bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "alpha", chromedp.ByQuery),
		textContent(`.library .section-header`, &header),
		chromedp.Evaluate(`document.querySelector(".omnibar-results") !== null`, &fanoutPresent),
	)

	assert.Equal(t, "In your library (1)", header, "the live filter narrows the installed library alone")
	assert.False(t, fanoutPresent, "typing without Enter must not fan out to the sources")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_OmnibarFanOutAppendsSourceRows is the design's headline promise:
// Enter (here, the "search sources ↵" button - functionally identical, and
// a more reliable chromedp interaction than a synthetic Enter keypress on a
// search input) fans out to the game's sources and appends "From sources
// (n)" rows IN PLACE below the library - "you never leave home".
func TestE2E_OmnibarFanOutAppendsSourceRows(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	var heading, url string
	var names []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(`.omnibar-results .search-result`, chromedp.ByQuery),
		textContent(`.omnibar-results .section-header`, &heading),
		chromedp.Evaluate(
			`Array.from(document.querySelectorAll(".omnibar-results .search-result__name")).map(e => e.textContent)`,
			&names,
		),
		chromedp.Location(&url),
	)

	// The fixture's second source ("flaky") always fails Search regardless of
	// query, so the heading's own caption (M3) is part of this heading's
	// honest count too - see TestE2E_FailingSourceRendersWarningRowNotSwallowed
	// for that caption pinned in isolation.
	assert.Equal(t, "From sources (1) · 1 source failed", heading)
	assert.Contains(t, names, "Better Boots")
	assert.Contains(t, url, f.HomePath(), "the fan-out must never navigate away from Mission Control")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FailingSourceRendersWarningRowNotSwallowed covers the design's
// explicit rule: "Source failures surface as a warning row, never
// swallowed." The fixture's second source ("flaky") always fails Search -
// its failure must appear ALONGSIDE the working source's real hit, not
// instead of it - AFTER it (M3, unit 5 fix wave: a warning ahead of the real
// hits it sits beside read as the headline result, not a footnote), and the
// heading's own count must caption the failure rather than leave it
// uncounted.
func TestE2E_FailingSourceRendersWarningRowNotSwallowed(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	var warning, heading string
	var hitNames []string
	var rowClasses []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(`.omnibar-results .search-result--warning`, chromedp.ByQuery),
		textContent(`.omnibar-results .search-result--warning`, &warning),
		textContent(`.omnibar-results .section-header`, &heading),
		chromedp.Evaluate(
			`Array.from(document.querySelectorAll(".omnibar-results .search-result__name")).map(e => e.textContent)`,
			&hitNames,
		),
		chromedp.Evaluate(
			`Array.from(document.querySelectorAll(".omnibar-results .search-result")).map(e => e.className)`,
			&rowClasses,
		),
	)

	assert.Contains(t, warning, "flaky")
	assert.Contains(t, warning, "upstream unavailable")
	assert.Contains(t, hitNames, "Better Boots", "the working source's hit must still render beside the warning")
	assert.Equal(t, "From sources (1) · 1 source failed", heading,
		"the heading must caption the failure, not just count the hits")
	require.Len(t, rowClasses, 2)
	assert.NotContains(t, rowClasses[0], "search-result--warning", "the real hit must render FIRST")
	assert.Contains(t, rowClasses[1], "search-result--warning", "the warning must render AFTER the hits, not ahead of them")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_OmnibarFanOutIsCapped is M8 (unit 5 fix wave): the omnibar's
// fan-out set no limit at all, so a catalog with more matches than a real
// source's own default page (NexusMods' can run past a hundred) could
// append an unbounded list below the library. The 25-mod fixture's "item"
// query matches all of them; the fan-out must cap at OMNIBAR_FANOUT_LIMIT
// (20), not append all 25 - the dedicated search page is the escape hatch
// for the rest.
func TestE2E_OmnibarFanOutIsCapped(t *testing.T) {
	f := newE2EFixtureWithManySearchResults(t)

	var heading string
	var rowCount int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		// This fixture seeds no INSTALLED mods, only a searchable catalog -
		// the library renders its empty state, not a table.
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "item", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(`.omnibar-results .search-result`, chromedp.ByQuery),
		textContent(`.omnibar-results .section-header`, &heading),
		chromedp.Evaluate(`document.querySelectorAll(".omnibar-results .search-result").length`, &rowCount),
	)

	assert.Equal(t, "From sources (20)", heading, "the fan-out must cap at OMNIBAR_FANOUT_LIMIT, not the full 25-mod catalog")
	assert.Equal(t, 20, rowCount)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_InlineInstallWithVersionPickWritesToDisk is #331's central
// scenario: fan out, install a search result INLINE (no navigation away),
// pick a non-default version in the confirm modal's picker (#225's version
// selection, re-surfaced from InstallPlan.FilePool), confirm, and the job
// really lands the SELECTED version's bytes on disk - not the default pick.
func TestE2E_InlineInstallWithVersionPickWritesToDisk(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	row := searchResultRow("fake", e2eSearchInstallModID)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
	)

	var versionOptions []string
	f.runInBrowser(t,
		chromedp.WaitVisible(`select[name="install-version"]`, chromedp.ByQuery),
		chromedp.Evaluate(
			`Array.from(document.querySelectorAll('select[name="install-version"] option')).map(o => o.value)`,
			&versionOptions,
		),
	)
	assert.ElementsMatch(t, []string{"2.0", "1.0"}, versionOptions,
		"the picker must offer every version InstallPlan.FilePool carries")

	f.runInBrowser(t,
		chromedp.SetValue(`select[name="install-version"]`, "1.0", chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	deployed, err := os.ReadFile(filepath.Join(f.Game.ModPath, "Mods", "boots.pak"))
	require.NoError(t, err)
	assert.Equal(t, "payload for boots/f1", string(deployed),
		"the SELECTED version's file (f1, 1.0) must be what landed on disk, not the primary (f2, 2.0)")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchRowReadsInstalledAfterItsOwnJobSucceeds is M5 (unit 5 fix
// wave): a search row's own hit.installed is only as fresh as the report
// that produced it, and search reports are outside hydrate()'s own
// route-scoped refresh - without main.js#refreshSearchResults, dismissing a
// just-succeeded install's job chip returned the row straight back to an
// enabled "Install" button, offering to install the very thing it just
// finished installing.
func TestE2E_SearchRowReadsInstalledAfterItsOwnJobSucceeds(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	row := searchResultRow("fake", e2eSearchInstallModID)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
		chromedp.Click(row+` .job-progress__dismiss`, chromedp.ByQuery),
	)

	var installButtonPresent bool
	var installedBadge string
	f.runInBrowser(t,
		chromedp.WaitVisible(row+` .badge--good`, chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q) !== null`, row+" .search-result__install"), &installButtonPresent),
		textContent(row+` .badge--good`, &installedBadge),
	)
	assert.False(t, installButtonPresent, "a just-installed row must not offer to install itself again")
	assert.Equal(t, "Installed", installedBadge)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_InlineInstallWithFilePickWritesToDisk covers plan_install.js's
// OTHER picker: the FILE sub-select that only renders once the chosen
// version itself resolves to more than one file (unlike Better Boots
// above, whose two files each carry a DIFFERENT version and so exercise
// only the version ▾). "Multi Edition Mod" has one version and two files -
// the version ▾ does NOT render (M6, unit 5 fix wave: gated on more than one
// distinct VERSION, not merely a pool of more than one file - a single-
// option version dropdown had nothing to decide), only the file ▾, and
// picking a file must be what decides which one is deployed.
func TestE2E_InlineInstallWithFilePickWritesToDisk(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	row := searchResultRow("fake", e2eSearchMultiFileModID)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "multi edition", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
	)

	var fileOptions []string
	var versionPickerPresent bool
	f.runInBrowser(t,
		chromedp.WaitVisible(`select[name="install-file"]`, chromedp.ByQuery),
		chromedp.Evaluate(
			`Array.from(document.querySelectorAll('select[name="install-file"] option')).map(o => o.textContent)`,
			&fileOptions,
		),
		chromedp.Evaluate(`document.querySelector('select[name="install-version"]') !== null`, &versionPickerPresent),
	)
	assert.Contains(t, fileOptions, "Regular Edition")
	assert.Contains(t, fileOptions, "Definitive Edition")
	assert.False(t, versionPickerPresent,
		"a one-version pool with several files must show only the file picker (M6)")

	f.runInBrowser(t,
		chromedp.SetValue(`select[name="install-file"]`, "m2", chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	deployed, err := os.ReadFile(filepath.Join(f.Game.ModPath, "Mods", "multi-definitive.pak"))
	require.NoError(t, err)
	assert.Equal(t, "payload for multi/m2", string(deployed),
		"the SELECTED file (m2, Definitive Edition) must be what landed on disk, not the primary (m1)")
	assert.NoFileExists(t, filepath.Join(f.Game.ModPath, "Mods", "multi-regular.pak"))
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ConflictOverwriteRoundTripSucceeds is the conflict round trip end
// to end: installing "Clashing Mod" fails inline with *core.ConflictError
// (its archive collides with the already-deployed Alpha Mod's file), the
// tray's failed entry offers a live Overwrite affordance (failures.js,
// wired for real in #331 - see TestE2E_FailureNextStepIsDecidedByTypedDetails
// for the same wiring pinned in isolation), and taking it re-plans/re-applies
// with accept_conflicts, landing the new mod's bytes - downloading nothing
// the second time, because the refused attempt already warmed the cache.
func TestE2E_ConflictOverwriteRoundTripSucceeds(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	deployedPath := filepath.Join(f.Game.ModPath, filepath.FromSlash(e2eSearchDeployedFile))
	before, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "alpha content", string(before))

	row := searchResultRow("fake", e2eSearchConflictModID)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "clash", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="failed"]`, chromedp.ByQuery),
	)

	afterRefusal := f.Src.downloadCount()
	assert.Positive(t, afterRefusal, "the refused attempt must have downloaded - that is why the cache is warm")

	current, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	assert.Equal(t, "alpha content", string(current), "a refused conflict must not have touched the deployed file")

	f.runInBrowser(t,
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__row[data-state="failed"] button[data-action="overwrite"]`, chromedp.ByQuery),
		chromedp.Click(`.tray__row[data-state="failed"] button[data-action="overwrite"]`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	after, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	assert.Equal(t, "payload for clash/c1", string(after),
		"the overwrite must have replaced the contested path with the NEW mod's file")
	assert.Equal(t, afterRefusal, f.Src.downloadCount(),
		"the overwrite re-run must download nothing: the refused attempt already filled the cache")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ConflictOverwriteRoundTripSucceedsFromTheSearchPage is I4 (unit 5
// fix wave): installing from the DEDICATED SEARCH PAGE and hitting a
// conflict used to be a dead end there - app.js renders SearchPage with no
// TopBar, so there is no activity bell/tray on that route at all, and the
// tray was the ONLY place the Overwrite affordance rendered. tray.js's own
// OverwriteButton now renders INLINE beside the failed row's own job chip
// (jobprogress.js), so this scenario completes without a tray anywhere in
// reach.
func TestE2E_ConflictOverwriteRoundTripSucceedsFromTheSearchPage(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	deployedPath := filepath.Join(f.Game.ModPath, filepath.FromSlash(e2eSearchDeployedFile))

	row := searchResultRow("fake", e2eSearchConflictModID)
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("clash")),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="failed"]`, chromedp.ByQuery),
	)

	var trayPresent bool
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelector(".activity-bell__trigger") !== null`, &trayPresent),
	)
	require.False(t, trayPresent, "the search page has no top bar/tray at all - the row is the only way in")

	f.runInBrowser(t,
		chromedp.Click(row+` button[data-action="overwrite"]`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	after, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	assert.Equal(t, "payload for clash/c1", string(after),
		"the overwrite must have replaced the contested path with the NEW mod's file")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_OverwriteButtonAfterReloadIsHonestlyDisabled is I3 (unit 5 fix
// wave): main.js's installRequests (the only record of what a failed
// install's Overwrite should re-plan) is a page-lifetime Map, while the
// failed job itself is server-side and reload-durable (GET /api/v1/jobs) -
// before this fix, a reload left the tray's button present, enabled, and
// silently doing nothing when clicked. The row's own inline copy of the
// button doesn't even reach this case: state.origins (which job belongs to
// which control) is ALSO page-lifetime, so after a reload the row shows a
// plain Install button again and the tray is the only place the stale
// failure - and this scenario - still exists at all.
func TestE2E_OverwriteButtonAfterReloadIsHonestlyDisabled(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	row := searchResultRow("fake", e2eSearchConflictModID)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "clash", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="failed"]`, chromedp.ByQuery),
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__row[data-state="failed"] button[data-action="overwrite"]`, chromedp.ByQuery),
	)

	var disabledBeforeReload bool
	f.runInBrowser(t,
		chromedp.Evaluate(
			`document.querySelector('.tray__row[data-state="failed"] button[data-action="overwrite"]').disabled`,
			&disabledBeforeReload,
		),
	)
	assert.False(t, disabledBeforeReload, "same-session, the request is still reconstructable")

	f.runInBrowser(t,
		chromedp.Reload(),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__row[data-state="failed"] button[data-action="overwrite"]`, chromedp.ByQuery),
	)

	var disabledAfterReload bool
	var title string
	f.runInBrowser(t,
		chromedp.Evaluate(
			`document.querySelector('.tray__row[data-state="failed"] button[data-action="overwrite"]').disabled`,
			&disabledAfterReload,
		),
		chromedp.Evaluate(
			`document.querySelector('.tray__row[data-state="failed"] button[data-action="overwrite"]').title`,
			&title,
		),
	)
	assert.True(t, disabledAfterReload,
		"a reload loses installRequests - the button must say so honestly, not silently do nothing when clicked")
	assert.Contains(t, title, "fresh install attempt")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchPagePaginatesAndFiltersByCategory covers the escape
// hatch's own two features the inline fan-out never needs: a real second
// PAGE (Next fetches page 1 from the server, not a client-side slice of
// page 0's own results) and a category filter now applied SERVER-SIDE
// (Important 1b, unit 5 fix wave: it used to slice whatever page was
// currently on screen, so filtering to Armor read "10" when the catalog
// actually holds 13 - the "dishonest count" the owner demo found).
func TestE2E_SearchPagePaginatesAndFiltersByCategory(t *testing.T) {
	f := newE2EFixtureWithManySearchResults(t)

	var page1Count int
	var page1Header string
	var nextDisabled bool
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/g/"+f.Game.ID+"/"+f.Profile+"/search?q=item"),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".search-result").length`, &page1Count),
		textContent(`.search-page .section-header`, &page1Header),
		chromedp.Evaluate(`document.querySelector(".search-page__pager button:last-child").disabled`, &nextDisabled),
	)
	assert.Equal(t, e2eManyResultsPageSize, page1Count, "page 1 holds exactly SEARCH_PAGE_SIZE hits")
	assert.Contains(t, page1Header, "Page 1 · 20 on this page", "the header must say what its number IS (Important 1a)")
	assert.Contains(t, page1Header, "more available", "25 catalog mods over a page size of 20 must cue that more exist")
	assert.False(t, nextDisabled, "25 catalog mods over a page size of 20 must offer a next page")

	// design doc §Search's search-PAGE bullet ("source badges, star/download
	// counts, summaries") - the terser omnibar fan-out never renders these,
	// so this is the ONE surface that must.
	var firstSummary, firstDownloads string
	f.runInBrowser(t,
		textContent(`.search-result__summary`, &firstSummary),
		textContent(`.search-result__downloads`, &firstDownloads),
	)
	assert.Contains(t, firstSummary, "Summary text for item")
	assert.Contains(t, firstDownloads, "100 downloads")

	var page2Count int
	var firstNameOnPage2 string
	f.runInBrowser(t,
		chromedp.Click(`.search-page__pager button:last-child`, chromedp.ByQuery),
		chromedp.WaitVisible(`.search-results`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".search-result").length`, &page2Count),
		textContent(`.search-result__name`, &firstNameOnPage2),
	)
	assert.Equal(t, 5, page2Count, "the remaining 5 catalog mods, not a repeat of page 1")
	assert.Equal(t, "Item 21", firstNameOnPage2, "a genuinely DIFFERENT page, not page 1 truncated again")

	var filteredCount int
	var filteredHeader string
	var filteredNextDisabled bool
	f.runInBrowser(t,
		chromedp.Click(`.search-page__pager button:first-child`, chromedp.ByQuery),
		chromedp.WaitVisible(`select[name="category"]`, chromedp.ByQuery),
		chromedp.SetValue(`select[name="category"]`, "Armor", chromedp.ByQuery),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"]`, chromedp.ByQuery),
	)
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelectorAll(".search-result").length`, &filteredCount),
		textContent(`.search-page .section-header`, &filteredHeader),
		chromedp.Evaluate(`document.querySelector(".search-page__pager button:last-child").disabled`, &filteredNextDisabled),
	)
	assert.Equal(t, 13, filteredCount,
		"the CATALOG holds 13 Armor-category mods (odd item numbers 1..25) - not the 10 that happened to be on page 1")
	assert.Contains(t, filteredHeader, "Page 1 · 13 on this page")
	assert.True(t, filteredNextDisabled, "all 13 Armor mods fit on one page - paging still works, it just has nothing left to page to")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchPageSourceFilterNarrowsResults is M4 (unit 5 fix wave): the
// search page's source filter had no fixture where more than one source's
// own hits ever rendered (sourceIDs.length > 1 was never true in any E2E
// fixture), so the control shipped untested end to end. Two WORKING
// sources contribute to the same "gizmo" query; selecting one must narrow
// the results server-side (Important 1b) to exactly that source's own hits.
func TestE2E_SearchPageSourceFilterNarrowsResults(t *testing.T) {
	f := newE2EFixtureWithTwoWorkingSearchSources(t)

	var allCount int
	var sourceOptionPresent bool
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/g/"+f.Game.ID+"/"+f.Profile+"/search?q=gizmo"),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".search-result").length`, &allCount),
		chromedp.Evaluate(`document.querySelector('select[name="source"]') !== null`, &sourceOptionPresent),
	)
	require.True(t, sourceOptionPresent, "two sources contributing hits must render the source filter")
	require.Equal(t, 5, allCount, "both sources' hits appear with no filter applied")

	var filteredCount int
	var names []string
	f.runInBrowser(t,
		chromedp.SetValue(`select[name="source"]`, "fake2", chromedp.ByQuery),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"]`, chromedp.ByQuery),
	)
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelectorAll(".search-result").length`, &filteredCount),
		chromedp.Evaluate(
			`Array.from(document.querySelectorAll(".search-result__name")).map(e => e.textContent)`,
			&names,
		),
	)
	assert.Equal(t, 2, filteredCount, "only fake2's own two mods must remain")
	assert.ElementsMatch(t, []string{"Gizmo Four", "Gizmo Five"}, names)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_DeepLinkToSearchPage proves /search?q= is reachable on its own,
// not merely as a client-side navigation from the omnibar - a bookmark or a
// shared link must land the same result.
func TestE2E_DeepLinkToSearchPage(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	var names []string
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("boots")),
		chromedp.WaitVisible(`.search-results`, chromedp.ByQuery),
		chromedp.Evaluate(
			`Array.from(document.querySelectorAll(".search-result__name")).map(e => e.textContent)`,
			&names,
		),
	)

	assert.Contains(t, names, "Better Boots")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchPageRowClickOpensSlideOverAndCloseReturnsToResults proves
// the design's "slide-over on click for source results too" (design doc
// §Search) holds on the DEDICATED search page as well as the omnibar's
// inline fan-out - SourceResultsList (searchresults.js) is the same shared
// component either way, and a row's name click must not be a dead click
// that merely rewrites the URL with nothing to show for it.
func TestE2E_SearchPageRowClickOpensSlideOverAndCloseReturnsToResults(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	row := searchResultRow("fake", e2eSearchInstallModID)
	var name, url string
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("boots")),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__name", chromedp.ByQuery),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		textContent(`.slide-over .section-header`, &name),
		chromedp.Location(&url),
	)
	assert.Equal(t, "Better Boots", name)
	assert.Contains(t, url, "mod=fake%2Fboots")
	assert.Contains(t, url, "q=boots", "opening the panel must preserve the search page's own query")

	f.runInBrowser(t,
		// waitForPanelFocus (see its own doc comment / TestE2E_SlideOver_
		// EscapeClosesAndOutsideClickCloses): ModPanel's Escape listener
		// attaches from a useEffect, deferred past the synchronous render
		// WaitVisible(".slide-over") observes - sending Escape before that
		// effect has actually run reaches no listener at all.
		waitForPanelFocus(),
		chromedp.KeyEvent(kb.Escape),
		waitGone(`.slide-over`),
		chromedp.WaitVisible(`.search-results`, chromedp.ByQuery),
		chromedp.Location(&url),
	)
	assert.Contains(t, url, "/search?q=boots", "closing the panel must return to the search results, not navigate home")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfileSwitchClearsStaleOmnibarSearch is I5 (unit 5 fix wave):
// neither the omnibar's own text (MissionControl's local state) nor
// state.omnibarSearch (main.js) were ever cleared on a game/profile switch -
// the pickers navigate() rather than reload, so both survived it, leaving
// the PREVIOUS profile's "From sources" rows (with a live, wrongly-scoped
// Install button) sitting under the new profile's own library.
func TestE2E_ProfileSwitchClearsStaleOmnibarSearch(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(`.omnibar-results .search-result`, chromedp.ByQuery),
	)

	f.runInBrowser(t,
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu .picker__item"))
				.find((b) => b.textContent === "other")
				.click()
		`, nil),
	)

	var url, omnibarValue string
	var fanoutPresent bool
	f.runInBrowser(t,
		// "other" is seeded with no mods at all (an empty-state, not a
		// table) - the universal hydrated marker, not the table itself.
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Location(&url),
		chromedp.Evaluate(`document.querySelector(".omnibar").value`, &omnibarValue),
		chromedp.Evaluate(`document.querySelector(".omnibar-results") !== null`, &fanoutPresent),
	)
	require.Contains(t, url, "/other", "the picker must have actually switched profiles")
	assert.Empty(t, omnibarValue, "the omnibar's own text must not survive a profile switch")
	assert.False(t, fanoutPresent, "the previous profile's fan-out rows must not survive a profile switch")

	// Retyping the SAME query "boots" (without pressing Enter/fanout again)
	// must not resurrect the stale report either: if state.omnibarSearch
	// itself had merely been HIDDEN by the fresh MissionControl's own reset
	// local text (rather than actually cleared, main.js#go), its query would
	// still read "boots" and immediately re-match the moment the text does,
	// rendering the OLD profile's rows in the NEW one with no fetch at all.
	f.runInBrowser(t,
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector(".omnibar-results") !== null`, &fanoutPresent),
	)
	assert.False(t, fanoutPresent,
		"retyping the previous query must not immediately re-match a stale, uncleared omnibarSearch")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_OverlappingInstallAndToggleBothTrackCorrectly is #331's carry-in
// proof: main.js's single module-level bindingJob slot was correct only
// while one modal implied one in-flight job start; Unit 5's inline
// per-search-row install means a SECOND origin (here, a slide-over toggle,
// which needs no modal and so is never locked out by one) can start while
// the first is still genuinely in flight - bindingJobs (main.js) is now a
// Map keyed by origin for exactly this reason.
//
// The install's own POST /api/v1/jobs is deliberately delayed (the fixture's
// startE2EServerWithDelayedJobStart), so its origin binding is a real,
// observable in-flight promise - not a race against microsecond-fast local
// HTTP round trips - for the whole window in which the toggle starts and
// finishes. The proof is END STATE, not timing: each origin's own outcome
// must land on ITS OWN mod, not be lost or attributed to the other.
//
// That end-state proof alone does NOT discriminate main.js's Map from Unit
// 3's single module-level slot (Important 2, unit 5 fix wave review): with
// the install's start slow and the toggle's fast, every assertion below
// passes unchanged under either implementation - verified by reverting
// bindingJobs to a single slot in a scratch copy and re-running, 6/6 green.
// The toggle's own start is now ALSO delayed (startE2EServerWithDelayedJob
// Start), and window.__lmmBindingJobsSize() (main.js, test-only
// introspection) is sampled while both starts are genuinely still
// in-flight at once - the one thing a single slot cannot ever report as 2.
func TestE2E_OverlappingInstallAndToggleBothTrackCorrectly(t *testing.T) {
	f := newE2EFixtureWithSearchableModsAndDelayedJobStart(t, 600*time.Millisecond, 200*time.Millisecond)

	row := searchResultRow("fake", e2eSearchInstallModID)
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		// The install's own start call is now sleeping server-side (still
		// unresolved in bindingJobs); the modal's own busy state proves it.
		chromedp.WaitVisible(`.modal [data-action="confirm"][disabled]`, chromedp.ByQuery),
		// Clear the omnibar's own live filter - it still reads "boots" from
		// the fan-out above, which would filter Gamma Mod (an installed
		// row, not a search hit) out of the library entirely. Setting
		// .value and dispatching "input" directly is what actually fires
		// Preact's onInput; chromedp.Clear's select-all+Backspace key
		// sequence did not reliably clear this field under automated key
		// dispatch.
		chromedp.Evaluate(`(() => {
			const el = document.querySelector(".omnibar");
			el.value = "";
			el.dispatchEvent(new Event("input", { bubbles: true }));
		})()`, nil),
		chromedp.WaitVisible(`.library .section-header`, chromedp.ByQuery),
		clickModRow("Gamma Mod"),
		chromedp.WaitVisible(`.slide-over__actions`, chromedp.ByQuery),
		// The install's confirm modal is STILL ON SCREEN (locked
		// "starting") throughout this whole scenario - that overlay's own
		// scrim sits above the slide-over in DOM order and intercepts a
		// coordinate-based click aimed at its Disable button. A
		// programmatic click (the same technique clickModRow already uses
		// to reach the row underneath it) bypasses hit-testing entirely,
		// which is the right tool here: this scenario is about
		// bindingJobs' overlap, not about whether a modal's scrim can be
		// clicked through - a separate, real UX question of its own.
		chromedp.Evaluate(`document.querySelector(".slide-over__actions button").click()`, nil),
	)

	// Both starts are now genuinely in flight: the install's from its own
	// slow POST /api/v1/jobs, the toggle's own start just issued and (like
	// the install's) sleeping server-side behind the same delaying proxy.
	// This is the window Important 2's fix targets - sampled immediately,
	// before either can possibly have resolved.
	var concurrentBindings int
	f.runInBrowser(t,
		chromedp.Evaluate(`window.__lmmBindingJobsSize()`, &concurrentBindings),
	)
	assert.Equal(t, 2, concurrentBindings,
		"both the install's and the toggle's own starts must be tracked while genuinely overlapping - "+
			"a single module-level slot can never report more than 1 here")

	f.runInBrowser(t,
		chromedp.WaitVisible(`.slide-over .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector(".slide-over__close").click()`, nil),
	)

	// Re-fan-out for "boots": clearing the omnibar above hid the search row
	// entirely (OmnibarResults only renders while its own query still
	// matches the live omnibar text), so the install's own eventual
	// completion toasted instead of resurfacing on it - re-searching
	// brings the row back, and origins["install:fake/boots"] (untouched by
	// any of this - a completely separate store slice from the search
	// results themselves) still names whichever job actually finished, so
	// InlineJob picks its CURRENT state straight back up.
	//
	// That job FAILS - correctly, not a bug: the toggle really did change
	// the installed-mods set the install's plan was computed against
	// (Ruling 5's own freshness precondition, already covered on its own
	// terms by TestFlowInstall_StalePlan_FailsTheJobAndAFreshPlanSucceeds),
	// so ApplyInstall refuses it as stale. That refusal is this scenario's
	// own proof: the failure must resurface on the RIGHT row, naming the
	// RIGHT reason, with the toggle's own success nowhere near it - which
	// is exactly what a clobbered bindingJobs slot could get wrong.
	var jobHTML string
	f.runInBrowser(t,
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="failed"]`, chromedp.ByQuery),
		textContent(row+` .job-progress__text`, &jobHTML),
	)
	assert.Contains(t, jobHTML, "stale",
		"the install's own origin must show ITS OWN outcome (the real staleness conflict), not the toggle's success")

	var gammaEnabled bool
	f.runInBrowser(t,
		// The omnibar still reads "boots" from the re-search above, which
		// would filter Gamma Mod out of the library the same way it did
		// earlier - clear it again before reading the library's own state.
		chromedp.Evaluate(`(() => {
			const el = document.querySelector(".omnibar");
			el.value = "";
			el.dispatchEvent(new Event("input", { bubbles: true }));
		})()`, nil),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row"))
				.find((r) => r.textContent.includes("Gamma Mod"))
				.querySelector("td.col--enabled input").checked
		`, &gammaEnabled),
	)
	assert.False(t, gammaEnabled, "the toggle's own origin must have landed on GAMMA - not lost, not misattributed to the install")

	require.NoFileExists(t, filepath.Join(f.Game.ModPath, "Mods", "boots.pak"),
		"a refused (stale) install must not have installed anything")
	assert.Empty(t, f.BrowserErrors())
}

// --- issue 332 (Unit 6): reorder, profiles, health repair, updates batch,
// and the library batch bar / row menu. ---

// clickInRow finds the .profiles-row/.mod-row whose own text contains
// rowText and clicks the button inside it whose text is exactly
// buttonText - the row-scoped sibling of clickModRow, needed once a table
// has more than one button per row (Rename/Delete/Set default/Export, or
// the ⋯ menu's own items).
// profilesListContains/profilesListNotContains poll the profiles modal's
// own list null-safely: the list briefly unmounts while a mutation's own
// afterMutation() re-fetch is in flight (the same transient "Computing
// plan…"-style gap the updates-batch renderer's own drop poll already
// guards against), so a bare .textContent read races a null element
// mid-transition instead of just polling again.
func profilesListContains(text string) string {
	return fmt.Sprintf(`(() => {
		const el = document.querySelector('[data-testid="profiles-list"]');
		return el !== null && el.textContent.includes(%q);
	})()`, text)
}

func profilesListNotContains(text string) string {
	return fmt.Sprintf(`(() => {
		const el = document.querySelector('[data-testid="profiles-list"]');
		return el !== null && !el.textContent.includes(%q);
	})()`, text)
}

func clickInRow(rowSelector, rowText, buttonText string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`
		Array.from(document.querySelectorAll(%q))
			.find((r) => r.textContent.includes(%q))
			.querySelectorAll("button, a")
			.forEach((b) => { if (b.textContent.trim() === %q) b.click(); });
	`, rowSelector, rowText, buttonText), nil)
}

// TestE2E_LibraryRow_ToggleAndMenu proves the row's own enabled checkbox is
// LIVE (no longer NOT_YET) and the ⋯ menu's Lock/Unlock/Uninstall actually
// reach core, not just render.
func TestE2E_LibraryRow_ToggleAndMenu(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes("Alpha Mod"))
				.querySelector("td.col--enabled input").click();
		`, nil),
	)
	require.Eventually(t, func() bool {
		m, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
		return err == nil && !m.Enabled
	}, 5*time.Second, 20*time.Millisecond, "the row toggle must actually disable the mod")

	openBetaMenu := chromedp.Tasks{
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes("Beta Mod"))
				.querySelector("td.col--menu button").click();
		`, nil),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
	}

	// A row's own lock badge (🔒) only appears once the reload that follows
	// setModLock/clearModLock has landed - polled on the BROWSER'S state
	// (not just the service's) before reopening the menu, since the click
	// handler fires the mutation without awaiting it (library.js) and a
	// same-instant re-open would otherwise race the reload that updates
	// which label ("Lock" vs "Unlock") the menu is about to show.
	betaLockBadge := `Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes("Beta Mod")).querySelector('.badge[title^="Locked"]')`

	f.runInBrowser(t, openBetaMenu,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".row-menu__item")).find((b) => b.textContent.trim() === "Lock").click();
		`, nil),
		chromedp.Poll(betaLockBadge+` !== null`, nil),
	)
	p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "default")
	require.NoError(t, err)
	var betaLocked bool
	for _, ref := range p.Mods {
		if ref.ModID == "b" {
			betaLocked = ref.Locked
		}
	}
	assert.True(t, betaLocked, "the menu's Lock action must actually lock the ref")

	f.runInBrowser(t, openBetaMenu,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".row-menu__item")).find((b) => b.textContent.trim() === "Unlock").click();
		`, nil),
		chromedp.Poll(betaLockBadge+` === null`, nil),
	)
	p, err = f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "default")
	require.NoError(t, err)
	for _, ref := range p.Mods {
		if ref.ModID == "b" {
			assert.False(t, ref.Locked, "the menu's Unlock action must actually unlock the ref")
		}
	}

	// The menu's own Uninstall reaches the SAME single-mod confirm modal
	// the slide-over's own Uninstall button opens.
	f.runInBrowser(t, openBetaMenu,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".row-menu__item")).find((b) => b.textContent.trim() === "Uninstall").click();
		`, nil),
		chromedp.WaitVisible(`.modal[data-kind="uninstall"] .plan--uninstall`, chromedp.ByQuery),
	)
	var body string
	f.runInBrowser(t, textContent(`.modal[data-kind="uninstall"]`, &body))
	assert.Contains(t, body, "Beta Mod")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryBatchBar_EnableDisableUninstallToDisk drives the batch bar
// through all three of its wired-to-jobs actions on real, deployed mods:
// disable-selected clears the deployed symlinks, enable-selected restores
// them, and uninstall-selected removes the mods (and their files) for good.
// The wire's own sequencing caveat (task-A report §8: "start the next only
// after the previous job's job_done frame") is exercised implicitly - two
// mods selected at once, asserted on the END STATE of both.
func TestE2E_LibraryBatchBar_EnableDisableUninstallToDisk(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)
	_, err := f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	alphaPath := filepath.Join(f.Game.ModPath, "alpha.pak")
	betaPath := filepath.Join(f.Game.ModPath, "beta.pak")
	require.FileExists(t, alphaPath)
	require.FileExists(t, betaPath)

	selectBoth := chromedp.Tasks{
		chromedp.Evaluate(`
			document.querySelectorAll(".mod-row td.col--select input").forEach((cb) => cb.click());
		`, nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
	}

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		selectBoth,
		chromedp.Click(`[data-action="batch-disable"]`, chromedp.ByQuery),
	)
	require.Eventually(t, func() bool {
		a, errA := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
		b, errB := f.Svc.GetInstalledMod(t.Context(), "fake", "b", f.Game.ID, "default")
		return errA == nil && errB == nil && !a.Enabled && !b.Enabled
	}, 5*time.Second, 20*time.Millisecond, "batch disable must reach both mods")
	_, err = f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	assert.NoFileExists(t, alphaPath, "a disabled mod must not stay deployed")
	assert.NoFileExists(t, betaPath, "a disabled mod must not stay deployed")

	f.runInBrowser(t,
		selectBoth,
		chromedp.Click(`[data-action="batch-enable"]`, chromedp.ByQuery),
	)
	require.Eventually(t, func() bool {
		a, errA := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
		b, errB := f.Svc.GetInstalledMod(t.Context(), "fake", "b", f.Game.ID, "default")
		return errA == nil && errB == nil && a.Enabled && b.Enabled
	}, 5*time.Second, 20*time.Millisecond, "batch enable must reach both mods")

	// Batch uninstall: ONE confirm modal listing both mods' own uninstall
	// plans (design doc §Modals: "modals stack at most one deep").
	f.runInBrowser(t,
		selectBoth,
		chromedp.Click(`[data-action="batch-uninstall"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="uninstall-batch"]`, chromedp.ByQuery),
		// Each mod's own uninstall plan is computed asynchronously
		// (uninstallbatchmodal.js's own mount effect) - wait for both to
		// settle (Confirm's own label names the ready count) before reading
		// the modal's content.
		chromedp.WaitVisible(`.modal[data-kind="uninstall-batch"] [data-action="confirm"]:not([disabled])`, chromedp.ByQuery),
	)
	var batchBody string
	f.runInBrowser(t, textContent(`.modal[data-kind="uninstall-batch"]`, &batchBody))
	assert.Contains(t, batchBody, "Alpha Mod")
	assert.Contains(t, batchBody, "Beta Mod")

	// A plain chromedp.Click misses this button (the modal body's own
	// scroll, with two full uninstall plans in it, leaves the footer
	// outside the click target chromedp computes) - a raw DOM click sends
	// the identical trusted event Preact's handler needs without that
	// coordinate dependency.
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelector('.modal[data-kind="uninstall-batch"] [data-action="confirm"]').click()`, nil),
		chromedp.WaitNotPresent(`.modal[data-kind="uninstall-batch"]`, chromedp.ByQuery),
	)
	require.Eventually(t, func() bool {
		_, errA := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
		_, errB := f.Svc.GetInstalledMod(t.Context(), "fake", "b", f.Game.ID, "default")
		return errors.Is(errA, domain.ErrModNotFound) && errors.Is(errB, domain.ErrModNotFound)
	}, 5*time.Second, 20*time.Millisecond, "batch uninstall must remove both mods")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryRowMenu_ClosesOnOutsideClickAndEscape is m2's own scenario
// (unit 6 gate review): the ⋯ row menu used to close only by re-clicking
// ⋯, leaving it open over the rest of the page once the user had clearly
// moved on.
func TestE2E_LibraryRowMenu_ClosesOnOutsideClickAndEscape(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	openMenu := chromedp.Tasks{
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes("Alpha Mod"))
				.querySelector("td.col--menu button").click();
		`, nil),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
	}

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		openMenu,
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Click(`.section-header`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.row-menu`, chromedp.ByQuery),
	)

	f.runInBrowser(t,
		openMenu,
		// The listener this closes through is attached by an EFFECT, which
		// (per this suite's own established pattern - see
		// TestE2E_ProfilesModal_CRUDExportImport's settle()) can lag the
		// menu's own DOM appearance by a handful of animation frames in a
		// headless browser; WaitVisible above only proves the DOM is there,
		// not that the effect has run yet.
		chromedp.Sleep(300*time.Millisecond),
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`.row-menu`, chromedp.ByQuery),
	)

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryBatchBar_UpdateFiltersToRowsWithAnUpdate is m5's own
// scenario (unit 6 gate review): "Update" on a selection with no update at
// all used to open a live Confirm over an empty plan, and a MIXED selection
// used to send every row - including ones with nothing to update - straight
// into kind_updates.go's own NotFound bucket.
func TestE2E_LibraryBatchBar_UpdateFiltersToRowsWithAnUpdate(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "2.0"},
		Files: []domain.DownloadableFile{{ID: "fa", Version: "2.0", IsPrimary: true}},
	})
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "b", SourceID: "fake", Name: "Beta Mod", Version: "1.0"},
		Files: []domain.DownloadableFile{{ID: "fb", Version: "1.0", IsPrimary: true}},
	})
	f := newE2EFixtureFromSource(t, src)
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", GameID: f.Game.ID},
		true, map[string][]byte{"alpha.esp": []byte("alpha")})
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "b", SourceID: "fake", Name: "Beta Mod", Version: "1.0", GameID: f.Game.ID},
		true, map[string][]byte{"beta.esp": []byte("beta")})
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "a", Version: "1.0"}))
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "b", Version: "1.0"}))

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		// Beta ALONE first: no update at all - the batch bar's own Update
		// must refuse to open anything.
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes("Beta Mod"))
				.querySelector("td.col--select input").click();
		`, nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
	)
	var betaOnlyDisabled bool
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector('[data-action="batch-update"]').disabled`, &betaOnlyDisabled))
	assert.True(t, betaOnlyDisabled, "Update must be disabled when none of the selection has an update")

	// Add Alpha (which DOES have one) to the selection: the button enables,
	// and opening it shows ONLY Alpha - Beta silently dropped rather than
	// landing in NotFound.
	f.runInBrowser(t, chromedp.Evaluate(`
		Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes("Alpha Mod"))
			.querySelector("td.col--select input").click();
	`, nil))
	var mixedEnabled bool
	f.runInBrowser(t, chromedp.Evaluate(`!document.querySelector('[data-action="batch-update"]').disabled`, &mixedEnabled))
	assert.True(t, mixedEnabled, "a MIXED selection with at least one update must enable the button")

	f.runInBrowser(t,
		chromedp.Click(`[data-action="batch-update"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="updates-batch-rows"]`, chromedp.ByQuery),
	)
	var rowsText string
	f.runInBrowser(t, textContent(`[data-testid="updates-batch-rows"]`, &rowsText))
	assert.Contains(t, rowsText, "Alpha Mod")
	assert.NotContains(t, rowsText, "Beta Mod", "a selected row with no update must be filtered out before the plan is even opened")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ConflictsCard_ResolveOpensReorderFocusedOnTheConflict is demo
// item 9 (unit 6 gate review): "Resolve…" used to always land the reorder
// modal at the top of a possibly long list; it now passes a focusKey - the
// conflict's own deployed owner - the same way the row menu's own "Reorder
// here" already does.
func TestE2E_ConflictsCard_ResolveOpensReorderFocusedOnTheConflict(t *testing.T) {
	f := newE2EFixtureWithReorderableConflict(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--conflicts`, chromedp.ByQuery),
		chromedp.Click(`.card--conflicts [data-action="resolve"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="reorder-list"]`, chromedp.ByQuery),
	)

	var focusedMod string
	require.Eventually(t, func() bool {
		f.runInBrowser(t, chromedp.Evaluate(`document.activeElement?.getAttribute("data-mod") ?? ""`, &focusedMod))
		return focusedMod != ""
	}, 2*time.Second, 50*time.Millisecond, "the reorder modal must land focus on a row, not the panel or <body>")
	assert.Equal(t, "fake:y", focusedMod, `"Resolve…" must focus the conflict's own deployed owner (Y, the winner at deploy time), not the top of the list`)

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_UpdatesCard_FailedBatchReportsAnHonestTally is I3's own scenario
// (unit 6 gate review): an "updates" job whose Apply loop could not
// download anything still finishes with job STATE "succeeded" - only its
// own result document says every item failed (kind_updates.go's
// applyUpdatesKind returns (result, nil) even when result.Failed is every
// item) - so before this fix the Updates card kept showing the bare "Done"
// over a still-pending update, and the tray read "updates succeeded". The
// fixture's shared fakeSource.GetDownloadURL always answers
// source.ErrNotSupported (testhelpers_test.go), which is what makes the
// download - and the whole update - fail deterministically.
func TestE2E_UpdatesCard_FailedBatchReportsAnHonestTally(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "2.0"},
		Files: []domain.DownloadableFile{{ID: "fa", Version: "2.0", IsPrimary: true}},
	})
	f := newE2EFixtureFromSource(t, src)
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0", GameID: f.Game.ID},
		true, map[string][]byte{"alpha.esp": []byte("alpha")})
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "a", Version: "1.0"}))

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates`, chromedp.ByQuery),
		chromedp.Evaluate(`
			document.querySelector(".card--updates .card__row input[type=checkbox]").click();
		`, nil),
		chromedp.Click(`.card--updates [data-action="update-selected"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="updates"] [data-action="confirm"]:not([disabled])`, chromedp.ByQuery),
		chromedp.Click(`.modal[data-kind="updates"] [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal[data-kind="updates"]`, chromedp.ByQuery),
	)

	var cardText string
	require.Eventually(t, func() bool {
		f.runInBrowser(t, textContent(`.card--updates`, &cardText))
		return strings.Contains(cardText, "0 applied / 1 failed")
	}, 5*time.Second, 100*time.Millisecond, `the Updates card must report the batch's own honest outcome, not "Done" over a still-pending update`)
	assert.Contains(t, cardText, "1.0 → 2.0", "the update must still be listed as pending - it was never actually applied")

	f.runInBrowser(t,
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray`, chromedp.ByQuery),
	)
	var trayText string
	f.runInBrowser(t, textContent(`.tray`, &trayText))
	assert.Contains(t, trayText, "0 applied / 1 failed", `the tray must not read "updates succeeded" over a batch that applied nothing`)

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryBatchBar_CancelKeepsTheSelectionAndReturnsFocus is m4/I2's
// own scenario (unit 6 gate review, folded together per the review's own
// note: "the same edit fixes half of I2"): library.js used to clear the
// multi-select the INSTANT the batch modal opened, which discarded the
// user's work on a mere Cancel and - because that removed the batch bar's
// own Uninstall button from the DOM in the same render the modal mounted -
// left modal.js's captured-at-mount activeElement already stale, so focus
// fell through to <body> instead of back to that button.
func TestE2E_LibraryBatchBar_CancelKeepsTheSelectionAndReturnsFocus(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes("Alpha Mod"))
				.querySelector("td.col--select input").click();
		`, nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Click(`[data-action="batch-uninstall"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="uninstall-batch"]`, chromedp.ByQuery),
	)

	var activeAction string
	f.runInBrowser(t,
		// modal.js's own Escape listener is attached by an effect, which can
		// lag the modal's own DOM appearance by a handful of animation
		// frames in a headless browser (this suite's own established
		// pattern - see TestE2E_LibraryRowMenu_ClosesOnOutsideClickAndEscape).
		chromedp.Sleep(300*time.Millisecond),
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`.modal[data-kind="uninstall-batch"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.activeElement?.getAttribute("data-action") ?? document.activeElement?.tagName`, &activeAction),
	)
	assert.Equal(t, "batch-uninstall", activeAction, "I2: focus must return to the batch bar's own Uninstall control, not <body>")

	var alphaStillSelected bool
	f.runInBrowser(t, chromedp.Evaluate(`
		Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes("Alpha Mod"))
			.querySelector("td.col--select input").checked
	`, &alphaStillSelected))
	assert.True(t, alphaStillSelected, "m4: the selection must survive a Cancel, cleared only on Confirm")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfilesModal_EscapeReturnsFocusToThePickerTrigger is I2's own
// profiles scenario (unit 6 gate review): the actual clicked opener
// ("Manage profiles…") lives inside a dropdown that must close for the
// modal to make sense on screen, so the shell's own captured-at-mount
// activeElement is always stale for this one modal - modal.js's own
// openerSelector (queried FRESH at close time) is the fix, pointed at the
// picker's own trigger button, which is always mounted.
func TestE2E_ProfilesModal_EscapeReturnsFocusToThePickerTrigger(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
	)
	f.runInBrowser(t, chromedp.Evaluate(`
		Array.from(document.querySelectorAll(".profile-picker__menu button"))
			.find((b) => b.textContent.includes("Manage profiles"))?.click();
	`, nil))
	f.runInBrowser(t, chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery))

	var activeClass string
	f.runInBrowser(t,
		// modal.js's own Escape listener is attached by an effect, which can
		// lag the modal's own DOM appearance by a handful of animation
		// frames in a headless browser (this suite's own established
		// pattern - see TestE2E_LibraryRowMenu_ClosesOnOutsideClickAndEscape's
		// identical note); WaitVisible above only proves the DOM is there.
		chromedp.Sleep(300*time.Millisecond),
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`[data-testid="profiles-list"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.activeElement?.className ?? ""`, &activeClass),
	)
	assert.Contains(t, activeClass, "profile-picker__trigger", "focus must return to the picker's own trigger, not <body>")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibraryRowMenu_RefusedLockSurfacesAsAToast is I1's own scenario
// (unit 6 gate review): the ⋯ menu's Lock/Unlock used to call
// actions.setModLock/clearModLock fire-and-forget, so a rejected ApiError
// surfaced only as an unhandled promise rejection in the console - the menu
// had already closed, leaving nothing on screen to say what went wrong.
// Charlie is installed under "default" (so ListMods/the library shows it)
// but never added to the profile's own Mods list (seedInstalledMod's own
// contract - no pm.AddMod call), which is exactly what
// ProfileManager.SetModLock refuses ("mod fake:charlie not found in profile
// \"default\"").
func TestE2E_LibraryRowMenu_RefusedLockSurfacesAsAToast(t *testing.T) {
	f := newE2EFixture(t)
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "charlie", SourceID: "fake", Name: "Charlie Mod", Version: "1.0", GameID: f.Game.ID},
		true, map[string][]byte{"charlie.pak": []byte("charlie")})

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes("Charlie Mod"))
				.querySelector("td.col--menu button").click();
		`, nil),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".row-menu__item")).find((b) => b.textContent.trim() === "Lock").click();
		`, nil),
		chromedp.WaitVisible(`.toast--failure`, chromedp.ByQuery),
	)

	var toastText string
	f.runInBrowser(t, textContent(`.toast--failure`, &toastText))
	assert.Contains(t, toastText, "Charlie Mod", "the toast must name what it was trying to do")
	assert.Contains(t, toastText, "not found in profile", "the toast must carry the real ApiError message, not a generic one")

	// The 500 itself still logs as a network-level browser entry (Chrome's
	// own behavior for any non-2xx fetch, independent of whether the page's
	// own JS handled it) - assertNoUncaughtErrors is this suite's own way of
	// separating that from what I1 actually guards against: an UNCAUGHT
	// promise rejection, which library.js's previous fire-and-forget call
	// would have produced here.
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_LibraryBatchBar_UninstallSequencesOneJobAtATime is m6's own
// property test (unit 6 gate review): the existing batch-bar scenario only
// ever asserted the END state, which a downstream stale-plan symptom can
// fail even when nothing measures concurrency directly (m6's own finding -
// removing the sequencing wait broke a DIFFERENT assertion, not this one).
// Here a concurrent poller samples GET /api/v1/jobs throughout a real
// four-mod uninstall batch (slowed by a hook so the window is wide enough
// to sample more than once or twice) and asserts running jobs never
// exceeds 1 - the wire's own sequencing caveat, and core's own beginOp
// serialisation, pinned directly rather than through a side effect of it.
func TestE2E_LibraryBatchBar_UninstallSequencesOneJobAtATime(t *testing.T) {
	f := newE2EFixtureWithSlowUninstallAndFourMods(t)

	stop := make(chan struct{})
	var samples, maxRunning int64
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			// N4 (unit 6 re-review): this poller used to busy-spin with no
			// pause at all - 256,782 requests over one ~10s batch, measured
			// live - which burns a core and adds server load to every suite
			// run for no benefit: the assertion below only needs "sampled
			// more than once or twice", not tens of thousands of samples a
			// second.
			time.Sleep(5 * time.Millisecond)
			resp, err := http.Get(f.BaseURL + "/api/v1/jobs")
			if err != nil {
				continue
			}
			var index struct {
				Jobs []struct {
					State string `json:"state"`
				} `json:"jobs"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&index)
			_ = resp.Body.Close()
			if decodeErr != nil {
				continue
			}
			atomic.AddInt64(&samples, 1)
			var running int64
			for _, j := range index.Jobs {
				if j.State == "running" {
					running++
				}
			}
			for {
				current := atomic.LoadInt64(&maxRunning)
				if running <= current || atomic.CompareAndSwapInt64(&maxRunning, current, running) {
					break
				}
			}
		}
	}()

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			document.querySelectorAll(".mod-row td.col--select input").forEach((cb) => cb.click());
		`, nil),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Click(`[data-action="batch-uninstall"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="uninstall-batch"] [data-action="confirm"]:not([disabled])`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.modal[data-kind="uninstall-batch"] [data-action="confirm"]').click()`, nil),
		chromedp.WaitNotPresent(`.modal[data-kind="uninstall-batch"]`, chromedp.ByQuery),
	)

	// N3 (unit 6 re-review): this end-state check used to be a require, which
	// FailNows before the concurrency assertion below ever runs - so a real
	// sequencing regression would report only this check's downstream
	// symptom ("all four mods must actually be uninstalled"), never the
	// property m6 exists to pin (maxRunning). assert.Eventually lets
	// execution reach that assertion regardless, so a future reader sees the
	// actual measured maxRunning rather than having to make this check
	// non-fatal by hand to find it, the way this re-review did.
	assert.Eventually(t, func() bool {
		for _, id := range []string{"a", "b", "c", "d"} {
			if _, err := f.Svc.GetInstalledMod(t.Context(), "fake", id, f.Game.ID, "default"); !errors.Is(err, domain.ErrModNotFound) {
				return false
			}
		}
		return true
	}, 10*time.Second, 50*time.Millisecond, "all four mods must actually be uninstalled")

	close(stop)
	require.Greater(t, atomic.LoadInt64(&samples), int64(2), "the poller must have actually sampled the jobs index more than once or twice during the batch")
	assert.LessOrEqual(t, atomic.LoadInt64(&maxRunning), int64(1), "the batch must never run more than one job at a time")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_HealthCard_NotFixableRowNamesTheReason is m3's own scenario (unit
// 6 gate review): "Not fixable" used to say only that, with a tooltip that
// restated the same bare sentence. A LOCKED ref's version_mismatch is the
// most common not-fixable case in practice (verify.go's own Fixable table:
// "a non-local source AND an UNLOCKED ref").
//
// Issue 334 moved the sentence to where the decision is made: the card
// renders core.VerifyFinding.FixableReason verbatim, instead of the
// hand-maintained table plus a lock guess cross-referenced from the
// already-fetched library rows. So this asserts the ENGINE's wording now -
// which names the locked version, something the client-side guess never
// could.
func TestE2E_HealthCard_NotFixableRowNamesTheReason(t *testing.T) {
	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "boots", SourceID: "fake", Name: "Better Boots", Version: "2.0"},
		Files: []domain.DownloadableFile{{ID: "f1", Version: "2.0", IsPrimary: true}},
	})
	svc, game := newFixtureServiceWithSource(t, src)

	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake", "boots", "1.0", "f1", []byte("content")))
	require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "boots", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{"f1"},
		UpdatePolicy: domain.UpdateNotify,
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "boots", Version: "1.0", FileIDs: []string{"f1"}, Locked: true}))

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	f := e2eFixture{Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default", BrowserErrors: browserErrors}

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--health`, chromedp.ByQuery),
	)
	var rowText string
	f.runInBrowser(t, textContent(`.card--health .card__row`, &rowText))
	assert.Contains(t, rowText, "Not fixable", "a locked ref's version_mismatch must still say it is not fixable")
	assert.Contains(t, rowText, "locked at v1.0", "and now say WHY, in the engine's own words - the finding IS a locked version_mismatch")
	assert.Contains(t, rowText, "unlock it first")
	assert.NotContains(t, rowText, "Repair", "no Repair control may render for a not-fixable row")

	assert.Empty(t, f.BrowserErrors())
}

// newE2EFixtureWithTwoFixableFindings seeds two mods each recording a
// version_mismatch the boots pattern (newE2EFixtureWithAttention) already
// proves is fixable and network-free: the DB row is stamped at "1.0" while
// the matched cache entry's own content is stored under that same version,
// but the SOURCE's catalog reports "2.0" for the matched file - so
// EffectiveInstalledVersion disagrees with the recorded version without a
// download ever being involved (repairModVersion just renames the cache
// dir and rewrites the DB/profile record, verify_repair.go's own doc
// comment). Two independent mods, so a per-finding Repair on one leaves the
// other's own finding untouched - the scope this test exists to prove.
func newE2EFixtureWithTwoFixableFindings(t *testing.T) e2eFixture {
	t.Helper()
	sandboxE2EEnv(t)

	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "alpha", SourceID: "fake", Name: "Alpha Mod", Version: "2.0"},
		Files: []domain.DownloadableFile{{ID: "fa", Version: "2.0", IsPrimary: true}},
	})
	src.addMod(fakeSourceMod{
		Mod:   domain.Mod{ID: "beta", SourceID: "fake", Name: "Beta Mod", Version: "2.0"},
		Files: []domain.DownloadableFile{{ID: "fb", Version: "2.0", IsPrimary: true}},
	})
	svc, game := newFixtureServiceWithSource(t, src)

	for _, m := range []struct{ id, name, fileID string }{
		{"alpha", "Alpha Mod", "fa"}, {"beta", "Beta Mod", "fb"},
	} {
		gameCache := svc.GetGameCache(game)
		require.NoError(t, gameCache.Store(game.ID, "fake", m.id, "1.0", m.fileID, []byte("content")))
		require.NoError(t, svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
			Mod:          domain.Mod{ID: m.id, SourceID: "fake", Name: m.name, Version: "1.0", GameID: game.ID},
			ProfileName:  "default",
			Enabled:      true,
			FileIDs:      []string{m.fileID},
			UpdatePolicy: domain.UpdateNotify,
		}))
		require.NoError(t, svc.NewProfileManager().AddMod(t.Context(), game.ID, "default",
			domain.ModReference{SourceID: "fake", ModID: m.id, Version: "1.0", FileIDs: []string{m.fileID}}))
	}

	baseURL := startE2EServer(t, svc)
	ctx, browserErrors := newE2EBrowser(t)
	return e2eFixture{
		Ctx: ctx, BaseURL: baseURL, Svc: svc, Game: game, Profile: "default",
		BrowserErrors: browserErrors,
	}
}

// TestE2E_HealthCard_PerFindingRepairScopesToOneMod is #332's own health
// repair scenario: Repair on Alpha's own finding must fix Alpha and leave
// Beta's version_mismatch exactly as it was.
func TestE2E_HealthCard_PerFindingRepairScopesToOneMod(t *testing.T) {
	f := newE2EFixtureWithTwoFixableFindings(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--health`, chromedp.ByQuery),
	)
	var cardBody string
	f.runInBrowser(t, textContent(`.card--health`, &cardBody))
	assert.Contains(t, cardBody, "Alpha")
	assert.Contains(t, cardBody, "Beta")

	f.runInBrowser(t,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".card--health .card__row"))
				.find((r) => r.textContent.includes("Alpha Mod"))
				.querySelector('[data-action="repair"]').click();
		`, nil),
		chromedp.WaitVisible(`.modal[data-kind="verify_fix"] .plan--verify-fix`, chromedp.ByQuery),
	)
	var planBody string
	f.runInBrowser(t, textContent(`.modal[data-kind="verify_fix"]`, &planBody))
	assert.Contains(t, planBody, "alpha", "the scope line must name the mod this Repair was opened for")

	f.runInBrowser(t,
		chromedp.Click(`.modal[data-kind="verify_fix"] [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal[data-kind="verify_fix"]`, chromedp.ByQuery),
	)

	require.Eventually(t, func() bool {
		alpha, err := f.Svc.GetInstalledMod(t.Context(), "fake", "alpha", f.Game.ID, "default")
		return err == nil && alpha.Version == "2.0"
	}, 5*time.Second, 20*time.Millisecond, "the repaired mod must actually move to the effective version")

	beta, err := f.Svc.GetInstalledMod(t.Context(), "fake", "beta", f.Game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", beta.Version, "an untouched finding must NOT be repaired by another mod's scoped Repair")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ReorderModal_KeyboardAndDragFlipTheWinnerAndPersist is #332's own
// reorder scenario: Mod Y starts as the winner of a real file conflict
// (added to the profile after X); moving X below Y with the KEYBOARD/button
// path flips the live preview to X, saving commits it, and re-reading
// conflicts confirms the persisted order actually changed who wins - then
// the same round trip runs again the OTHER direction via a DRAG.
func TestE2E_ReorderModal_KeyboardAndDragFlipTheWinnerAndPersist(t *testing.T) {
	f := newE2EFixtureWithReorderableConflict(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--conflicts`, chromedp.ByQuery),
	)
	var conflictBody string
	f.runInBrowser(t, textContent(`.card--conflicts`, &conflictBody))
	assert.Contains(t, conflictBody, "wins: Mod Y", "Y (added second, higher priority) must start as the winner")

	f.runInBrowser(t,
		chromedp.Click(`.card--conflicts [data-action="resolve"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="reorder-list"]`, chromedp.ByQuery),
	)

	// --- Keyboard/button path: move X to the bottom (highest priority). ---
	var moved bool
	f.runInBrowser(t,
		chromedp.Evaluate(`
			(() => {
				const btn = Array.from(document.querySelectorAll(".reorder-row")).find((r) => r.textContent.includes("Mod X"))
					?.querySelector('[aria-label="Move Mod X to highest priority"]');
				if (!btn || btn.disabled) return false;
				btn.click();
				return true;
			})()
		`, &moved),
	)
	require.True(t, moved, `"Move Mod X to highest priority" must be found, enabled, and clicked`)

	var proposedWinner string
	require.Eventually(t, func() bool {
		f.runInBrowser(t, chromedp.Evaluate(`
			(() => {
				const row = Array.from(document.querySelectorAll(".reorder-preview tbody tr")).find((r) => r.textContent.includes("shared.esp"));
				return row ? row.children[2].textContent.trim() : "";
			})()
		`, &proposedWinner))
		return proposedWinner == "Mod X"
	}, 5*time.Second, 100*time.Millisecond, "the live preview must flip to X once it is moved below Y")

	f.runInBrowser(t,
		chromedp.Click(`[data-action="save-order"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`[data-testid="reorder-list"]`, chromedp.ByQuery),
	)

	require.Eventually(t, func() bool {
		report, err := f.Svc.GetProfileConflictsForOrder(t.Context(), f.Game, "default", nil)
		return err == nil && len(report) == 1 && report[0].LoadOrderWinner.Key == "fake:x"
	}, 5*time.Second, 50*time.Millisecond, "the saved order must persist through the API - X now wins")

	// --- Drag path: reopen and drag Y back below X, flipping it again. ---
	f.runInBrowser(t,
		chromedp.Click(`.card--conflicts [data-action="resolve"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="reorder-list"]`, chromedp.ByQuery),
		dragRowTo("Mod Y", "Mod X"),
	)
	require.Eventually(t, func() bool {
		f.runInBrowser(t, chromedp.Evaluate(`
			(() => {
				const row = Array.from(document.querySelectorAll(".reorder-preview tbody tr")).find((r) => r.textContent.includes("shared.esp"));
				return row ? row.children[2].textContent.trim() : "";
			})()
		`, &proposedWinner))
		return proposedWinner == "Mod Y"
	}, 5*time.Second, 100*time.Millisecond, "dragging Y below X must flip the preview back to Y")

	f.runInBrowser(t,
		chromedp.Click(`[data-action="save-order"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`[data-testid="reorder-list"]`, chromedp.ByQuery),
	)
	require.Eventually(t, func() bool {
		report, err := f.Svc.GetProfileConflictsForOrder(t.Context(), f.Game, "default", nil)
		return err == nil && len(report) == 1 && report[0].LoadOrderWinner.Key == "fake:y"
	}, 5*time.Second, 50*time.Millisecond, "the drag's own save must persist through the API too")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfilesModal_CRUDExportImport is issue 332's own profiles
// scenario: create/rename/set-default/delete inline in the modal, the
// export route's own document fetched through the literal link the row
// renders, and an import that lands a new profile with real, version-
// matched mod references, already Installed (both mods are pre-cached, so
// the plan's pending set is empty and no Install checkbox even renders).
func TestE2E_ProfilesModal_CRUDExportImport(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
	)

	// A short real (Go-level) pause between browser round trips below, on
	// top of this file's usual chromedp.Poll-for-settled-state pattern:
	// Preact/hooks' own effect flush runs on requestAnimationFrame,
	// measured elsewhere in this suite (the picker's own outside-click
	// scenario) at up to a handful of frames in a headless browser, and
	// this modal's own mount effect additionally kicks off an async
	// reloadProfiles() fetch. Cheap insurance once the ACTUAL bug this
	// scenario tripped over (below) was found and fixed.
	settle := func() { time.Sleep(300 * time.Millisecond) }

	openProfilesModal := func() {
		f.runInBrowser(t,
			chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
			chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		)
		settle()
		f.runInBrowser(t,
			chromedp.Evaluate(`
				Array.from(document.querySelectorAll(".profile-picker__menu button"))
					.find((b) => b.textContent.includes("Manage profiles"))?.click();
			`, nil),
		)
		settle()
		f.runInBrowser(t, chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery))
		settle()
	}
	openProfilesModal()

	// Create "survival".
	f.runInBrowser(t,
		chromedp.SetValue(`.profiles-create input`, "survival", chromedp.ByQuery),
		// A real submit-button CLICK, not chromedp.Submit: that action calls
		// the native HTMLFormElement.submit() directly, which bypasses
		// Preact's onSubmit handler entirely (the DOM spec's own submit()
		// method does not fire a "submit" event) and - since this form has
		// no action/method - performs a real page navigation to the
		// current URL, invalidating the execution context every action
		// after it needs (observed: "Cannot find context with specified
		// id" on the very next command, every time).
		chromedp.Click(`.profiles-create button[type="submit"]`, chromedp.ByQuery),
		chromedp.Poll(profilesListContains("survival"), nil),
	)
	_, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "survival")
	require.NoError(t, err, "the create form must actually create the profile")
	settle()

	// Rename survival -> outpost.
	f.runInBrowser(t,
		clickInRow(".profiles-row", "survival", "Rename"),
		chromedp.WaitVisible(`.profiles-row__rename-form`, chromedp.ByQuery),
		chromedp.SetValue(`.profiles-row__rename-form input`, "outpost", chromedp.ByQuery),
		chromedp.Click(`.profiles-row__rename-form button[type="submit"]`, chromedp.ByQuery),
		chromedp.Poll(profilesListContains("outpost"), nil),
	)
	_, err = f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "outpost")
	require.NoError(t, err, "the rename must actually rename the profile")
	settle()

	// Set outpost as default.
	f.runInBrowser(t,
		clickInRow(".profiles-row", "outpost", "Set default"),
		chromedp.Poll(`Array.from(document.querySelectorAll(".profiles-row")).find((r) => r.textContent.includes("outpost")).textContent.includes("default")`, nil),
	)
	require.Eventually(t, func() bool {
		listing, err := f.Svc.ListProfiles(t.Context(), f.Game.ID)
		if err != nil {
			return false
		}
		for _, p := range listing.Profiles {
			if p.Name == "outpost" {
				return p.IsDefault
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "set-default must actually flip the default")
	settle()

	// Delete outpost (inline confirm - no nested modal).
	f.runInBrowser(t,
		clickInRow(".profiles-row", "outpost", "Delete"),
		chromedp.WaitVisible(`.profiles-row--confirm`, chromedp.ByQuery),
		chromedp.Click(`.profiles-row--confirm button.button--danger`, chromedp.ByQuery),
		chromedp.Poll(profilesListNotContains("outpost"), nil),
	)
	_, err = f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "outpost")
	assert.ErrorIs(t, err, domain.ErrProfileNotFound, "the inline confirm must actually delete the profile")
	settle()

	// Export "default": a plain <a href> to the real GET route (the server
	// sets Content-Disposition itself - api_profiles.go's own doc comment:
	// "no blob, no synthesized filename"), asserted here by reading exactly
	// the href the row renders and fetching it - the same request a click
	// on that literal anchor issues, without headless Chrome's own download
	// manager (a real but separate mechanism this reaches through, not
	// around) in the loop.
	var exportHref string
	f.runInBrowser(t, chromedp.Evaluate(`
		Array.from(document.querySelectorAll(".profiles-row")).find((r) => r.textContent.includes("default"))
			.querySelector('a[href*="/export"]').getAttribute("href")
	`, &exportHref))
	require.NotEmpty(t, exportHref, "the Export control must be a real link to the export route")
	assert.Contains(t, exportHref, "/api/v1/profiles/default/export")

	var exported string
	f.runInBrowser(t, chromedp.Evaluate(fmt.Sprintf(`fetch(%q).then((r) => r.text())`, exportHref), &exported,
		func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	assert.Contains(t, exported, `"a"`, "the export must carry the profile's own mods")
	assert.Contains(t, exported, `"b"`)

	// Doctor the export into a NEW profile's document and import it -
	// both mods are already fully cached (newE2EFixtureWithDeployableMods),
	// so Install triggers no network at all; ApplyImport just saves the
	// profile (kind_profile_import.go's own doc comment: "nothing to do
	// for these").
	restoredDoc := strings.Replace(string(exported), `"name": "default"`, `"name": "restored"`, 1)
	require.NotEqual(t, string(exported), restoredDoc, "the exported document's own name field must actually be found and replaced")
	importPath := filepath.Join(t.TempDir(), "restored.json")
	require.NoError(t, os.WriteFile(importPath, []byte(restoredDoc), 0o644))

	// The profiles modal is still open from the CRUD steps above (nothing
	// closed it) - reused here rather than reopened, which also avoids
	// clicking the top bar's own picker trigger through the still-open
	// modal's scrim (that click would land on the scrim, not the trigger,
	// and close the modal instead of opening a picker menu).
	f.runInBrowser(t,
		chromedp.SetUploadFiles(`.profiles-import input[type="file"]`, []string{importPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] .plan--profile-import`, chromedp.ByQuery),
	)
	var importPlanBody string
	f.runInBrowser(t, textContent(`.modal[data-kind="profile_import"]`, &importPlanBody))
	assert.Contains(t, importPlanBody, "restored")
	assert.Contains(t, importPlanBody, "Already installed")

	f.runInBrowser(t,
		chromedp.Click(`.modal[data-kind="profile_import"] [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal[data-kind="profile_import"]`, chromedp.ByQuery),
	)
	// Both mods land in the imported profile as real, version-matched
	// references - core.ProfileImportResult's own bucket (Installed, since
	// both are already cached at the imported version: api_profiles.go/
	// kind_profile_import.go's own doc comment, "nothing to do for these")
	// rather than skipped or missing - proving the import produced usable
	// references, not just names that happen to parse.
	var restored *domain.Profile
	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "restored")
		if err != nil || len(p.Mods) != 2 {
			return false
		}
		restored = p
		return true
	}, 5*time.Second, 20*time.Millisecond, "the import job must actually create the new profile with both mods")

	byID := map[string]domain.ModReference{}
	for _, ref := range restored.Mods {
		byID[ref.ModID] = ref
	}
	require.Contains(t, byID, "a")
	require.Contains(t, byID, "b")
	assert.Equal(t, "fake", byID["a"].SourceID)
	assert.Equal(t, "1.0", byID["a"].Version)
	assert.Equal(t, "fake", byID["b"].SourceID)
	assert.Equal(t, "1.0", byID["b"].Version)

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfilesModal_ImportWithoutInstallReportsSkippedNotFailed is N1's
// own scenario (unit 6 re-review of the fix wave): the I3 fix folded
// core.ProfileImportResult.Skipped into "failed", so a profile_import that
// leaves the plan's own "Download and install" checkbox unchecked - the
// flow's documented default (plan_profile_import.js's own note: "Left
// unchecked, the profile is still saved and every pending mod is recorded
// as skipped") - used to render as a total failure ("0 applied / 2 failed"
// in the failure tone) even though the job succeeds and the profile is
// created. Skipped is not failed.
func TestE2E_ProfilesModal_ImportWithoutInstallReportsSkippedNotFailed(t *testing.T) {
	f := newE2EFixture(t)

	doc := fmt.Sprintf(`name: imported
game_id: %s
mods:
  - source_id: fake
    mod_id: newmod1
    version: "1.0"
  - source_id: fake
    mod_id: newmod2
    version: "1.0"
`, f.Game.ID)
	importPath := filepath.Join(t.TempDir(), "imported.yaml")
	require.NoError(t, os.WriteFile(importPath, []byte(doc), 0o644))

	settle := func() { time.Sleep(300 * time.Millisecond) }

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
	)
	settle()
	f.runInBrowser(t,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil),
	)
	settle()
	f.runInBrowser(t, chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery))
	settle()

	f.runInBrowser(t,
		chromedp.SetUploadFiles(`.profiles-import input[type="file"]`, []string{importPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] .plan--profile-import`, chromedp.ByQuery),
	)
	var planText string
	f.runInBrowser(t, textContent(`.modal[data-kind="profile_import"]`, &planText))
	require.Contains(t, planText, "Download and install", "the plan must offer the install checkbox - the one this scenario deliberately leaves unchecked")

	// The install checkbox is left UNCHECKED - the flow's own default.
	f.runInBrowser(t,
		chromedp.Click(`.modal[data-kind="profile_import"] [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal[data-kind="profile_import"]`, chromedp.ByQuery),
	)

	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "imported")
		return err == nil && len(p.Mods) == 2
	}, 5*time.Second, 20*time.Millisecond, "the import job must succeed and save the profile even though nothing was installed")

	f.runInBrowser(t,
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray`, chromedp.ByQuery),
	)
	var trayText string
	require.Eventually(t, func() bool {
		f.runInBrowser(t, textContent(`.tray`, &trayText))
		return strings.Contains(trayText, "profile_import") &&
			(strings.Contains(trayText, "skipped") || strings.Contains(trayText, "failed"))
	}, 5*time.Second, 100*time.Millisecond, "the tray must show the finished profile_import job's own tally")

	var toneClass string
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector(".tray__row .tray__state")?.className ?? ""`, &toneClass))

	assert.NotContains(t, trayText, "failed", `a fully successful import must never say "failed" - Skipped is not a failure`)
	assert.Contains(t, trayText, "skipped", "the tray must name the skipped count honestly")
	assert.Contains(t, toneClass, "tray__state--succeeded", "the tone must be success, not failure or mixed, when nothing actually failed")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfilesModal_ImportOfAlreadyInstalledModsReadsDone is N2's own
// scenario (unit 6 re-review): a profile_import whose every mod is already
// installed (the plan's Installed bucket, pending == 0) returns a
// core.ProfileImportResult that is all zeroes - {installed: 0, failed: 0,
// skipped: 0} - which resultTally used to still read as a tally-shaped
// result, replacing the tray's usual bare state word with an honest-looking
// but empty "0 installed · 0 skipped" - worse copy for a batch that did
// not, in truth, batch anything. resultTally now returns null for an
// all-zero result, same as a kind with no tally shape at all, so every
// caller's own existing "no tally" fallback applies - the tray's own bare
// state word here (jobprogress.js's InlineJob, the only surface that
// renders the literal word "Done", never mounts for profile_import: it has
// no owning mod row to attach to).
func TestE2E_ProfilesModal_ImportOfAlreadyInstalledModsReadsDone(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	doc := fmt.Sprintf(`name: reimport
game_id: %s
mods:
  - source_id: fake
    mod_id: a
    version: "1.0"
  - source_id: fake
    mod_id: b
    version: "1.0"
`, f.Game.ID)
	importPath := filepath.Join(t.TempDir(), "reimport.yaml")
	require.NoError(t, os.WriteFile(importPath, []byte(doc), 0o644))

	settle := func() { time.Sleep(300 * time.Millisecond) }

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
	)
	settle()
	f.runInBrowser(t,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil),
	)
	settle()
	f.runInBrowser(t, chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery))
	settle()

	f.runInBrowser(t,
		chromedp.SetUploadFiles(`.profiles-import input[type="file"]`, []string{importPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] .plan--profile-import`, chromedp.ByQuery),
	)
	var planText string
	f.runInBrowser(t, textContent(`.modal[data-kind="profile_import"]`, &planText))
	require.NotContains(t, planText, "Download and install", "both mods are already installed - there is nothing pending to offer a checkbox for")

	f.runInBrowser(t,
		chromedp.Click(`.modal[data-kind="profile_import"] [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal[data-kind="profile_import"]`, chromedp.ByQuery),
	)

	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "reimport")
		return err == nil && len(p.Mods) == 2
	}, 5*time.Second, 20*time.Millisecond, "the import job must succeed and save the profile")

	f.runInBrowser(t,
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray`, chromedp.ByQuery),
	)
	var trayText string
	require.Eventually(t, func() bool {
		f.runInBrowser(t, textContent(`.tray`, &trayText))
		return strings.Contains(trayText, "profile_import")
	}, 5*time.Second, 100*time.Millisecond, "the tray must show the finished profile_import job")

	assert.Contains(t, trayText, "succeeded", "an all-zero tally must fall back to the tray's own bare state word, not an honest-looking but empty tally")
	assert.NotContains(t, trayText, "installed ·", "no zero-count tally text may leak through")

	var toneClass string
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector(".tray__row .tray__state")?.className ?? ""`, &toneClass))
	assert.Contains(t, toneClass, "tray__state--succeeded")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_UninstallBatchModal_ReselectingAfterCancelUninstallsOnlyTheNewSelection
// is C1's own destructive reproduction (unit 6 gate review): ReorderModal
// and UninstallBatchModal used to call useState/useEffect AFTER a
// conditional `return null`, mounted PERMANENTLY as siblings in app.js -
// and Preact does not discard a component's hook list on a null render, so
// a hook slot filled during the FIRST open is REUSED, untouched, on every
// later one. For this modal that meant `entries` (the previous open's own
// computed uninstall plans) survived a close and leaked into the next
// open's confirm - selecting Alpha, cancelling, then selecting ONLY Beta
// would still uninstall Alpha, because `entries` was never recomputed for
// Beta's plan. Reproduced here exactly as the review found it live.
func TestE2E_UninstallBatchModal_ReselectingAfterCancelUninstallsOnlyTheNewSelection(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	selectByName := func(name string) chromedp.Action {
		return chromedp.Evaluate(fmt.Sprintf(`
			Array.from(document.querySelectorAll(".mod-row")).find((r) => r.textContent.includes(%q))
				.querySelector("td.col--select input").click();
		`, name), nil)
	}

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		selectByName("Alpha Mod"),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Click(`[data-action="batch-uninstall"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="uninstall-batch"]`, chromedp.ByQuery),
		// Wait for Alpha's own preview plan to actually land - otherwise
		// there is nothing stale yet for the bug below to replay.
		chromedp.WaitVisible(`.modal[data-kind="uninstall-batch"] [data-action="confirm"]:not([disabled])`, chromedp.ByQuery),
	)

	// Cancel via Escape - the design's own "Cancel restores". A settle
	// before it (this suite's own established pattern against modal.js's
	// effect-attached Escape listener lagging the DOM by a frame or two)
	// even though the WaitVisible above already did real async work.
	f.runInBrowser(t,
		chromedp.Sleep(300*time.Millisecond),
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`.modal[data-kind="uninstall-batch"]`, chromedp.ByQuery),
	)

	// Change the selection to Beta ONLY (re-selecting from scratch, since
	// this test's own concern is C1 alone - m4/I2's own "does Cancel keep
	// the selection" is a separate scenario), then open and confirm.
	f.runInBrowser(t,
		chromedp.Evaluate(`
			document.querySelectorAll(".mod-row td.col--select input:checked").forEach((cb) => cb.click());
		`, nil),
		selectByName("Beta Mod"),
		chromedp.WaitVisible(`.batch-bar`, chromedp.ByQuery),
		chromedp.Click(`[data-action="batch-uninstall"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="uninstall-batch"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="uninstall-batch"] [data-action="confirm"]:not([disabled])`, chromedp.ByQuery),
	)
	var modalBody string
	f.runInBrowser(t, textContent(`.modal[data-kind="uninstall-batch"]`, &modalBody))
	assert.Contains(t, modalBody, "Beta Mod", "the modal must plan the CURRENT selection")
	assert.NotContains(t, modalBody, "Alpha Mod", "a stale plan from the previous open must not leak into this one")

	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelector('.modal[data-kind="uninstall-batch"] [data-action="confirm"]').click()`, nil),
		chromedp.WaitNotPresent(`.modal[data-kind="uninstall-batch"]`, chromedp.ByQuery),
	)
	require.Eventually(t, func() bool {
		_, err := f.Svc.GetInstalledMod(t.Context(), "fake", "b", f.Game.ID, "default")
		return errors.Is(err, domain.ErrModNotFound)
	}, 5*time.Second, 20*time.Millisecond, "Beta - the mod actually selected at confirm time - must be uninstalled")

	alpha, err := f.Svc.GetInstalledMod(t.Context(), "fake", "a", f.Game.ID, "default")
	require.NoError(t, err, "Alpha - never selected the second time around - must NOT have been uninstalled")
	assert.Equal(t, "1.0", alpha.Version)
	gameCache := f.Svc.GetGameCache(f.Game)
	assert.True(t, gameCache.Exists(f.Game.ID, "fake", "a", "1.0"), "Alpha's own cache entry must be untouched")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ReorderModal_EscapeDiscardsTheEditOnReopen is C1's other
// reproduction: Cancel is supposed to restore, but the same stale-hook-list
// bug (see the uninstall-batch scenario above) meant `order` and the
// preview's own state survived a close, so re-opening replayed the
// abandoned edit instead of the saved order, and Save then committed it.
func TestE2E_ReorderModal_EscapeDiscardsTheEditOnReopen(t *testing.T) {
	f := newE2EFixtureWithReorderableConflict(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--conflicts`, chromedp.ByQuery),
		chromedp.Click(`.card--conflicts [data-action="resolve"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="reorder-list"]`, chromedp.ByQuery),
	)

	var moved bool
	f.runInBrowser(t,
		chromedp.Evaluate(`
			(() => {
				const btn = Array.from(document.querySelectorAll(".reorder-row")).find((r) => r.textContent.includes("Mod X"))
					?.querySelector('[aria-label="Move Mod X to highest priority"]');
				if (!btn || btn.disabled) return false;
				btn.click();
				return true;
			})()
		`, &moved),
	)
	require.True(t, moved, `"Move Mod X to highest priority" must be found, enabled, and clicked`)

	// Cancel via Escape ("Cancel restores" - design doc §Modals). A settle
	// first - this suite's own established pattern against modal.js's
	// effect-attached Escape listener lagging the DOM by a frame or two.
	f.runInBrowser(t,
		chromedp.Sleep(300*time.Millisecond),
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`[data-testid="reorder-list"]`, chromedp.ByQuery),
	)

	// Re-open and read the list back BEFORE touching anything: it must show
	// the SAVED order (Y then X), never the abandoned edit (X then Y).
	f.runInBrowser(t,
		chromedp.Click(`.card--conflicts [data-action="resolve"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="reorder-list"]`, chromedp.ByQuery),
	)
	var reopenedOrder []string
	f.runInBrowser(t, chromedp.Evaluate(`
		Array.from(document.querySelectorAll(".reorder-row__name")).map((n) => n.textContent)
	`, &reopenedOrder))
	require.Equal(t, []string{"Mod X", "Mod Y"}, reopenedOrder, "a fresh open must re-seed from the SAVED order (X then Y - X added first, lowest priority), not the cancelled edit that moved X to the end")

	// Save without touching anything: this must be a no-op against the
	// saved order, never a silent commit of the abandoned edit.
	f.runInBrowser(t,
		chromedp.Click(`[data-action="save-order"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`[data-testid="reorder-list"]`, chromedp.ByQuery),
	)
	require.Eventually(t, func() bool {
		report, err := f.Svc.GetProfileConflictsForOrder(t.Context(), f.Game, "default", nil)
		return err == nil && len(report) == 1 && report[0].LoadOrderWinner.Key == "fake:y"
	}, 5*time.Second, 50*time.Millisecond, "Y, the ORIGINAL winner, must still win - Cancel must have actually discarded the edit")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfilesModal_ReopenDoesNotResumeAHalfTypedRename audits
// ProfilesModal for C1's own pattern (unit 6 gate review: "ProfilesModal
// gets away with it because it calls its one hook BEFORE the guard").
// ProfileRow's own rename-in-progress state lives in a child component that
// unmounts along with everything else under the modal's `if (!open) return
// null`, so this is expected to pass even before the app.js mount fix -
// kept as the shared slot's third regression case, per the review's own
// "EVERY shape in the shared slot".
func TestE2E_ProfilesModal_ReopenDoesNotResumeAHalfTypedRename(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	// See TestE2E_ProfilesModal_CRUDExportImport's own settle(): the
	// picker's outside-click/Escape state and this modal's own mount effect
	// both flush on requestAnimationFrame, up to a handful of frames in a
	// headless browser - real insurance, not superstition, once observed.
	settle := func() { time.Sleep(300 * time.Millisecond) }
	openProfilesModal := func() {
		f.runInBrowser(t,
			chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
			chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		)
		settle()
		f.runInBrowser(t, chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil))
		settle()
		f.runInBrowser(t, chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery))
		settle()
	}

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
	)
	openProfilesModal()
	f.runInBrowser(t,
		clickInRow(".profiles-row", "default", "Rename"),
		chromedp.WaitVisible(`.profiles-row__rename-form`, chromedp.ByQuery),
		chromedp.SetValue(`.profiles-row__rename-form input`, "half-typed", chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`[data-testid="profiles-list"]`, chromedp.ByQuery),
	)
	settle()

	openProfilesModal()

	var stillRenaming bool
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector(".profiles-row__rename-form") !== null`, &stillRenaming))
	assert.False(t, stillRenaming, "a fresh open must not resume a half-typed rename left over from a previous session")

	_, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "default")
	require.NoError(t, err, "Escape must not have renamed the profile")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfilePicker_SwitchAndDeployRoundTrip drives `lmm profile
// switch` from the browser: the profile picker offers "Switch and deploy…"
// beside every profile that is not already the active one, that opens the
// confirm modal over the SwitchPlan, and confirming runs the real job.
//
// The end state is asserted on DISK and in the service, not just on screen:
// Alpha's link leaves the game directory, Beta's arrives, and the game's
// default profile has actually moved. A green bar over an unchanged game
// directory would be the worst possible pass.
func TestE2E_ProfilePicker_SwitchAndDeployRoundTrip(t *testing.T) {
	f := newE2EFixtureWithASwitchTarget(t)

	var plan, outcome string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.Click(`[data-action="switch"][data-profile="hardcore"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="switch"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="switch"]`, &plan),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
		textContent(`.job-progress__text`, &outcome),
	)

	assert.Contains(t, plan, "default")
	assert.Contains(t, plan, "hardcore")
	assert.Contains(t, plan, "Alpha Mod", "the plan names what it would disable")
	assert.Contains(t, plan, "Beta Mod", "the plan names what it would enable")
	assert.NotEmpty(t, outcome)

	_, err := os.Lstat(filepath.Join(f.Game.ModPath, "alpha.pak"))
	assert.Error(t, err, "the switch must have undeployed the profile it left")
	_, err = os.Lstat(filepath.Join(f.Game.ModPath, "beta.pak"))
	assert.NoError(t, err, "the switch must have deployed the profile it moved to")

	active, err := f.Svc.NewProfileManager().GetDefault(t.Context(), f.Game.ID)
	require.NoError(t, err)
	assert.Equal(t, "hardcore", active.Name, "the switch must have moved the active profile")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfilePicker_PlainNavigationSurvivesTheSwitchAffordance is the
// other half of the picker's contract (issue 334): clicking a profile's
// NAME still only changes what this browser is looking at - no plan, no
// modal, nothing on disk - and the profile that is already active offers no
// "Switch and deploy…" at all, because switching to it is the plan's own
// AlreadyActive no-op.
func TestE2E_ProfilePicker_PlainNavigationSurvivesTheSwitchAffordance(t *testing.T) {
	f := newE2EFixtureWithASwitchTarget(t)

	var switchActions, activeSwitchActions, modals int
	var path string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('[data-action="switch"]').length`, &switchActions),
		chromedp.Evaluate(`document.querySelectorAll('[data-action="switch"][data-profile="default"]').length`, &activeSwitchActions),
		// The NAME, not the action beside it.
		chromedp.Click(`.profile-picker__menu .picker__row[data-profile="hardcore"] .picker__item`, chromedp.ByQuery),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".modal").length`, &modals),
		chromedp.Location(&path),
	)

	assert.Equal(t, 1, switchActions, "only the non-active profile offers a switch")
	assert.Zero(t, activeSwitchActions, "the active profile has nothing to switch to")
	assert.Zero(t, modals, "browsing to a profile must not open a confirm modal")
	assert.Contains(t, path, "/hardcore", "the name is still a plain route change")

	_, err := os.Lstat(filepath.Join(f.Game.ModPath, "alpha.pak"))
	assert.NoError(t, err, "looking at another profile must not undeploy anything")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfileCard_ApplyProfileInstallsWhatTheProfileLists drives `lmm
// profile apply` from the browser (issue 334): Mission Control notices that
// the profile lists more mods than are installed, says so as an attention
// card, and the card's own "Apply profile…" runs the real convergence
// through the confirm-plan framework.
//
// The end state is asserted on disk: the mod the profile listed but nobody
// had installed is downloaded, extracted and deployed into the game
// directory. A card that merely renders would prove nothing.
func TestE2E_ProfileCard_ApplyProfileInstallsWhatTheProfileLists(t *testing.T) {
	f := newE2EFixtureWithAnUnappliedProfile(t)

	var card, plan string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--profile`, chromedp.ByQuery),
		textContent(`.card--profile`, &card),
		chromedp.Click(`[data-action="apply-profile"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_apply"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="profile_apply"]`, &plan),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	assert.Contains(t, card, "1 mod in this profile is not installed")
	assert.Contains(t, plan, "Better Boots", "the plan names what it would install")

	deployed, err := os.ReadFile(filepath.Join(f.Game.ModPath, "Mods", "boots.pak"))
	require.NoError(t, err, "apply must have installed and deployed the listed mod")
	assert.Contains(t, string(deployed), "payload for boots")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfileCard_AbsentWhenTheProfileIsAlreadyApplied is the card's
// own silence rule: an attention card that renders when nothing needs
// attention is noise, and noise is what makes people stop reading them.
func TestE2E_ProfileCard_AbsentWhenTheProfileIsAlreadyApplied(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	var cards int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.library`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".card--profile").length`, &cards),
	)

	assert.Zero(t, cards, "every listed mod is installed - there is nothing to apply")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_GenericPlanView_RendersAnUnknownKindsPlanAsData keeps the
// confirm-plan framework's FALLBACK renderer honest (the m8 carry from
// issue 332's review, closed here).
//
// Every kind plankinds.go registers now has a renderer of its own, so the
// fallback is by construction unreachable from any control in the
// application - which is exactly why it needs a driver: it exists for the
// NEXT kind, wired before its renderer is written, and a mutation in that
// state must still preview honestly rather than show an empty modal with a
// live Confirm button under it.
//
// plankind_fallback_test.go registers such a kind (test-only, mutating
// nothing), and this drives it through the real modal, the real Confirm and
// a real job: the plan document's own content renders, the "no dedicated
// preview yet" note says so plainly, and the outcome resurfaces.
func TestE2E_GenericPlanView_RendersAnUnknownKindsPlanAsData(t *testing.T) {
	f := newE2EFixture(t)

	var body string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		// The top bar's own Deploy origin, so this job has a control on
		// screen to morph into - the fallback kind has no control of its
		// own by definition, and an origin nobody has mounted would
		// resurface as a toast instead, which is a different rule's test.
		chromedp.Evaluate(`window.__lmmOpenPlan({
			kind: "e2e_no_renderer",
			origin: "deploy",
			title: "A kind with no renderer",
			confirmLabel: "Run it",
			options: {},
		})`, nil),
		chromedp.WaitVisible(`.modal[data-kind="e2e_no_renderer"] .plan--generic`, chromedp.ByQuery),
		textContent(`.modal[data-kind="e2e_no_renderer"]`, &body),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	assert.Contains(t, body, "no dedicated preview yet",
		"the fallback must say what it is, not pretend to be a designed preview")
	assert.Contains(t, body, "first unrendered step",
		"the plan document's own content must reach the screen")
	assert.Contains(t, body, f.Profile)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_HealthCard_SaysWhenItLastVerified renders the other half of the
// design's "last-verify timestamp + re-run" (§Mission Control), which issue
// 332 had to carry because core.VerifyResult had no such field: issue 334
// added checked_at, and the card now says how OLD the health it is showing
// is, which is the only thing a reader actually wants from that timestamp.
//
// The verify runs when the page loads, so the honest assertion is "just
// now" - the phrase relativetime.js produces for anything under a minute.
func TestE2E_HealthCard_SaysWhenItLastVerified(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var line string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--health`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="health-last-verified"]`, chromedp.ByQuery),
		textContent(`[data-testid="health-last-verified"]`, &line),
	)

	assert.Equal(t, "Last verified just now", line)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfilesModal_ImportOffersAWayBackToProfiles closes the N7 carry
// (unit 6 re-review): importing from the profiles modal REPLACES it with
// the confirm-plan modal in the shared slot ("modals stack at most one
// deep"), so when the import finishes there is nothing on screen that shows
// profiles - and the profile the user just created is only findable by
// knowing to re-open "Manage profiles…".
//
// The completion toast now carries that door. The scenario clicks it and
// asserts the profiles modal really opens on the imported profile - not
// that a button merely rendered.
func TestE2E_ProfilesModal_ImportOffersAWayBackToProfiles(t *testing.T) {
	f := newE2EFixture(t)

	doc := fmt.Sprintf(`name: imported
game_id: %s
mods: []
`, f.Game.ID)
	importPath := filepath.Join(t.TempDir(), "imported.yaml")
	require.NoError(t, os.WriteFile(importPath, []byte(doc), 0o644))

	settle := func() { time.Sleep(300 * time.Millisecond) }

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
	)
	settle()
	f.runInBrowser(t,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil),
	)
	settle()
	f.runInBrowser(t, chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery))
	settle()

	f.runInBrowser(t,
		chromedp.SetUploadFiles(`.profiles-import input[type="file"]`, []string{importPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] .plan--profile-import`, chromedp.ByQuery),
		chromedp.Click(`.modal[data-kind="profile_import"] [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		// The import's own origin has no control left on screen, so its
		// completion becomes a toast - which is exactly the state N7 is
		// about.
		chromedp.WaitVisible(`.toast [data-action="toast-action"]`, chromedp.ByQuery),
	)

	var label string
	f.runInBrowser(t, textContent(`.toast [data-action="toast-action"]`, &label))
	assert.Equal(t, "Open profiles", label)

	f.runInBrowser(t,
		chromedp.Click(`.toast [data-action="toast-action"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('[data-testid="profiles-list"]')?.textContent.includes("imported") ?? false`, nil,
			chromedp.WithPollingInterval(50*time.Millisecond)),
	)

	var listText string
	f.runInBrowser(t, textContent(`[data-testid="profiles-list"]`, &listText))
	assert.Contains(t, listText, "imported",
		"the offer must land on the profiles list showing what the import created")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Keyboard_EveryScreenIsTraversable is issue 334's keyboard pass,
// asserted rather than eyeballed: on every screen this application has, one
// full Tab pass reaches every control and never drops focus to the
// document.
//
// Dropping focus is the failure that matters and the one that is invisible
// in a screenshot: a control that removes itself from the DOM while it has
// focus (a menu item, a row that a mutation just replaced) leaves the
// keyboard cursor nowhere, and the user's next Tab restarts at the top of
// the page. Its own subtests are the screens, so a failure names which one.
func TestE2E_Keyboard_EveryScreenIsTraversable(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	screens := []struct {
		name  string
		path  string
		ready string
	}{
		{"mission-control", f.HomePath(), `.mission-control[data-hydrated="true"]`},
		{"slide-over", f.SlideOverPath("fake", "boots"), `.slide-over__panel`},
		{"mod-page", f.ModPagePath("fake", "boots"), `.mod-page`},
		{"search-page", f.HomePath() + "/search?q=boots", `.search-page[data-hydrated="true"]`},
		// The setup page's own panel loads its section asynchronously, so
		// the readiness selector is a control INSIDE the panel - waiting on
		// the page frame alone would walk a half-rendered screen.
		{"setup", f.BaseURL + "/g/" + f.Game.ID + "/" + f.Profile + "/setup", `.setup-page__body button`},
	}

	for _, screen := range screens {
		t.Run(screen.name, func(t *testing.T) {
			f.runInBrowser(t,
				chromedp.Navigate(screen.path),
				chromedp.WaitVisible(screen.ready, chromedp.ByQuery),
			)
			assertKeyboardTraversal(t, f, screen.name)
		})
	}

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Keyboard_ModalContainsFocusAndGivesItBack covers the containment
// modal.js's own comment promised and issue 332 deferred: a dialog over a
// scrim must not let Tab walk the page behind it, and closing it must put
// the cursor back where it came from.
//
// Both halves matter to the same person. Without the trap a keyboard user
// tabs out of the dialog into a library they cannot see and starts pressing
// Enter on rows behind a scrim; without the return they land back at the
// top of the document after every confirmation.
func TestE2E_Keyboard_ModalContainsFocusAndGivesItBack(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	var inside []bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Focus(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		// The trap, like the picker's Escape handler, is installed by an
		// effect that runs after the dialog is already on screen.
		settleEffects(),
		// Comfortably more presses than the dialog has controls, so the
		// walk goes round its end at least twice.
		chromedp.ActionFunc(func(ctx context.Context) error {
			for range 12 {
				if err := chromedp.KeyEvent(kb.Tab).Do(ctx); err != nil {
					return err
				}
				var in bool
				if err := chromedp.Evaluate(
					`Boolean(document.querySelector(".modal")?.contains(document.activeElement))`,
					&in).Do(ctx); err != nil {
					return err
				}
				inside = append(inside, in)
			}
			return nil
		}),
	)
	for i, in := range inside {
		assert.Truef(t, in, "Tab press %d walked out of the dialog", i+1)
	}

	var focused string
	f.runInBrowser(t,
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.Evaluate(`document.activeElement?.getAttribute("data-action") ?? ""`, &focused),
	)
	assert.Equal(t, "deploy", focused,
		"closing must return focus to the control that opened it")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Keyboard_EscapeFromAPickerReturnsFocusToItsTrigger is the same
// rule for the top bar's three dropdowns (issue 334): Escape closes the
// menu, and because the focused menu item is removed from the document by
// that very close, something has to put the cursor back - or the next Tab
// starts over at the top of the page.
func TestE2E_Keyboard_EscapeFromAPickerReturnsFocusToItsTrigger(t *testing.T) {
	f := newE2EFixtureWithASwitchTarget(t)

	var expandedWhileOpen, focusedAfter, expandedAfter string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector(".profile-picker__trigger").getAttribute("aria-expanded")`, &expandedWhileOpen),
		// Focus a control INSIDE the menu, which is what a keyboard user
		// would be on when they press Escape - and what the close removes.
		// The page's own .focus() rather than chromedp.Focus, which scrolls
		// and re-queries for a result this scenario does not need.
		chromedp.Evaluate(`document.querySelector('.picker__row[data-profile="hardcore"] .picker__item').focus()`, nil),
		// The picker's Escape handler is installed by an effect that runs
		// after the menu is already on screen (settleEffects).
		settleEffects(),
		chromedp.KeyEvent(kb.Escape),
		waitGone(`.profile-picker__menu`),
		chromedp.Evaluate(`document.activeElement?.className ?? ""`, &focusedAfter),
		chromedp.Evaluate(`document.querySelector(".profile-picker__trigger").getAttribute("aria-expanded")`, &expandedAfter),
	)

	assert.Equal(t, "true", expandedWhileOpen, "an open dropdown must say so")
	assert.Equal(t, "false", expandedAfter)
	assert.Contains(t, focusedAfter, "profile-picker__trigger",
		"Escape must hand the cursor back to the trigger it came from")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Keyboard_SkipLinkJumpsPastTheTopBar covers the shell's own skip
// link (issue 334): the first Tab on any screen offers a way past the top
// bar's seven-odd controls, which a keyboard user would otherwise walk
// through on every single route.
func TestE2E_Keyboard_SkipLinkJumpsPastTheTopBar(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var firstStop, hash string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Tab),
		chromedp.Evaluate(`document.activeElement?.className ?? ""`, &firstStop),
		chromedp.KeyEvent(kb.Enter),
		chromedp.Evaluate(`location.hash`, &hash),
	)

	assert.Equal(t, "skip-link", firstStop, "the skip link must be the first tab stop")
	assert.Equal(t, "#main", hash)

	var mainExists bool
	f.runInBrowser(t, chromedp.Evaluate(`document.getElementById("main") !== null`, &mainExists))
	assert.True(t, mainExists, "the skip link must point at a target that exists")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ReducedMotion_AnimatesNothing is the other half of issue 334's
// motion pass: a user who has asked their system for less motion gets
// none of it.
//
// It emulates the media feature rather than trusting the stylesheet by
// reading, because "prefers-reduced-motion is honoured" is a claim about
// what the BROWSER computes, and the mechanism has two halves that can
// fail independently - the CSS tokens collapsing to zero, and motion.js's
// exit timer collapsing with them. A panel held on screen for a fifth of a
// second waiting for an animation that was disabled is exactly the sluggish
// UI the preference exists to avoid.
func TestE2E_ReducedMotion_AnimatesNothing(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	var panelDuration, scrimDuration string
	var closedWithin time.Duration
	f.runInBrowser(t,
		emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{
			{Name: "prefers-reduced-motion", Value: "reduce"},
		}),
		chromedp.Navigate(f.SlideOverPath("fake", "a")),
		chromedp.WaitVisible(`.slide-over__panel`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`getComputedStyle(document.querySelector(".slide-over__panel")).animationDuration`, &panelDuration),
		chromedp.Evaluate(`getComputedStyle(document.querySelector(".slide-over")).animationDuration`, &scrimDuration),
		chromedp.ActionFunc(func(ctx context.Context) error {
			started := time.Now()
			if err := chromedp.KeyEvent(kb.Escape).Do(ctx); err != nil {
				return err
			}
			if err := waitGone(`.slide-over`).Do(ctx); err != nil {
				return err
			}
			closedWithin = time.Since(started)
			return nil
		}),
	)

	assert.Equal(t, "0s", panelDuration, "the panel must not animate in")
	assert.Equal(t, "0s", scrimDuration, "nor must its scrim")
	assert.Lessf(t, closedWithin, 500*time.Millisecond,
		"the exit must not wait out an animation that was disabled (took %s)", closedWithin)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_Motion_TheSlideOverPlaysItsExitBeforeItGoes is the default-motion
// half: with no reduced-motion preference the panel stays mounted, wearing
// the closing class, for long enough for its exit keyframes to run - which
// is the whole reason modpanel.js holds the route change back at all.
func TestE2E_Motion_TheSlideOverPlaysItsExitBeforeItGoes(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	var sawClosing bool
	f.runInBrowser(t,
		chromedp.Navigate(f.SlideOverPath("fake", "a")),
		chromedp.WaitVisible(`.slide-over__panel`, chromedp.ByQuery),
		settleEffects(),
		chromedp.KeyEvent(kb.Escape),
		// The panel is still there, now playing its exit - which is exactly
		// what would be impossible if the route change had gone straight
		// through and Preact had unmounted the subtree.
		chromedp.Poll(`document.querySelector(".slide-over--closing") !== null`, &sawClosing,
			chromedp.WithPollingInterval(20*time.Millisecond)),
		waitGone(`.slide-over`),
	)

	assert.True(t, sawClosing, "the panel must wear its closing state before it goes")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LockedUpdateIsReportedAsASkipNotAsDone is I1's own end-to-end
// reproduction: tick a LOCKED mod's update alongside an unlocked one, apply
// the batch, and read what the screen says about it.
//
// Before the fix the control that started the batch read a bare "Done" and
// the tray read "updates succeeded", with the lock refusal named nowhere at
// all - while the CLI, for the same batch, withheld the row's tick and
// ended with "1 locked mod(s) not applied". That is the parity gap #324
// closed in core and this surface lost on the way to the screen.
//
// The disk assertions are what stop this from being a test of wording: the
// unlocked mod really moves to 2.0 and the locked one really does not, so
// the tally is checked against what actually happened.
func TestE2E_LockedUpdateIsReportedAsASkipNotAsDone(t *testing.T) {
	f := newE2EFixtureWithALockedAndAnUnlockedUpdate(t)

	var inline, trayState, traySkips string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates`, chromedp.ByQuery),
		chromedp.Click(`.card--updates input[aria-label="Select Better Boots for update"]`, chromedp.ByQuery),
		chromedp.Click(`.card--updates input[aria-label="Select Great Gloves for update"]`, chromedp.ByQuery),
		chromedp.Click(`.card--updates [data-action="update-selected"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--updates .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
		textContent(`.card--updates .job-progress__text`, &inline),
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray__row .tray__skips`, chromedp.ByQuery),
		textContent(`.tray__row .tray__state`, &trayState),
		textContent(`.tray__row .tray__skips`, &traySkips),
	)

	assert.Equal(t, "1 applied / 1 skipped", strings.TrimSpace(inline),
		"the control that started the batch must count the refusal as a SKIP, never say a bare Done")
	assert.Equal(t, "1 applied / 1 skipped", strings.TrimSpace(trayState),
		"the tray must say the same thing the control does")
	assert.Contains(t, traySkips, "Better Boots",
		"the skipped row must be NAMED, not merely counted")
	assert.Contains(t, traySkips, "locked",
		"and the engine's own reason must be on screen - a bare count is a guessing game")

	boots, err := os.ReadFile(filepath.Join(f.Game.ModPath, "Mods", "boots.pak"))
	require.NoError(t, err)
	assert.Contains(t, string(boots), "boots-1.0",
		"the locked mod must still be deployed at the version its lock names")
	gloves, err := os.ReadFile(filepath.Join(f.Game.ModPath, "Mods", "gloves.pak"))
	require.NoError(t, err)
	assert.Contains(t, string(gloves), "gloves-2.0",
		"the unlocked mod must really have been updated - otherwise the tally counts nothing")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_UpdatesCardAndConfirmMarkALockedRow is Owner item 3 of the unit-8
// gate review: the two controls a user meets BEFORE the outcome does.
//
// The Updates card rendered a locked row as a plain checkbox like any
// other, and the confirm modal listed it as "Better Boots 1.0 → 2.0" - so
// the step whose entire job is to say what will happen promised an update
// ApplyUpdateBatch was always going to refuse (#97). Both now say so, in
// the engine's own terms, naming the version the LOCK holds rather than the
// one the install is on.
func TestE2E_UpdatesCardAndConfirmMarkALockedRow(t *testing.T) {
	f := newE2EFixtureWithALockedAndAnUnlockedUpdate(t)

	var lockedRow, openRow, modal string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates`, chromedp.ByQuery),
		chromedp.Text(`.card--updates .card__row:has([aria-label="Select Better Boots for update"])`, &lockedRow, chromedp.ByQuery),
		chromedp.Text(`.card--updates .card__row:has([aria-label="Select Great Gloves for update"])`, &openRow, chromedp.ByQuery),
		chromedp.Click(`.card--updates input[aria-label="Select Better Boots for update"]`, chromedp.ByQuery),
		chromedp.Click(`.card--updates input[aria-label="Select Great Gloves for update"]`, chromedp.ByQuery),
		chromedp.Click(`.card--updates [data-action="update-selected"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		chromedp.Text(`.modal[data-kind="updates"] .plan`, &modal, chromedp.ByQuery),
	)

	assert.Contains(t, lockedRow, "🔒", "a locked row must be marked on the control the user actually ticks")
	assert.Contains(t, lockedRow, "locked at v1.0", "and must name the version the lock holds")
	assert.NotContains(t, openRow, "🔒", "an unlocked row must carry no such mark")

	assert.Contains(t, modal, "will be skipped — locked at v1.0",
		"the confirm step must not promise an update that will not happen")
	// Case-folded: .plan__heading is rendered in small caps by app.css, and
	// this assertion is about the sentence, not about the CSS.
	assert.Contains(t, strings.ToLower(modal), "1 of them will be skipped — locked.",
		"and must say up front how much of the selection it will not touch")
	assert.Contains(t, modal, "1.0 → 2.0",
		"the row that WILL be updated still shows what it will move to")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfileSwitchKeepsThePickerAndFollowsTheProfile is I2 of the
// unit-8 gate review.
//
// <ProfilePicker> was wrapped whole in <InlineJob>, so a finished switch
// morphed the PICKER: the application's primary context control became a
// "Done ✕" that did not time out, and the route, the library and the deploy
// indicator all went on describing the profile the user had just switched
// AWAY from - the indicator reading "1 change undeployed" for a profile
// whose Deploy plan says there is nothing to deploy.
//
// A button that becomes its own job's progress is the design (Deploy does
// exactly that); a NAVIGATION control that does is a control the user has
// lost. So the switch morphs a slot of its own, and the screen follows the
// machine when the job lands.
func TestE2E_ProfileSwitchKeepsThePickerAndFollowsTheProfile(t *testing.T) {
	f := newE2EFixtureWithASwitchTarget(t)

	var location, indicator, library string
	var pickerInsideJob bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.Click(`[data-action="switch"][data-profile="hardcore"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="switch"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		// The route follows the machine: this is the assertion, not a
		// convenience wait - it never arrives without the fix.
		chromedp.WaitVisible(`.profile-picker__trigger[data-profile="hardcore"]`, chromedp.ByQuery),
		chromedp.Location(&location),
		chromedp.Evaluate(`Boolean(document.querySelector(".job-progress .profile-picker"))`, &pickerInsideJob),
		textContent(`.deploy-indicator`, &indicator),
		chromedp.Text(`.library`, &library, chromedp.ByQuery),
	)

	assert.True(t, strings.HasSuffix(location, "/g/g1/hardcore"),
		"a confirmed switch must take the screen with it, not leave the URL on the profile it left")
	assert.False(t, pickerInsideJob,
		"the profile picker must never be the control a switch job morphs - it is how you navigate")
	assert.Equal(t, "Deployed", strings.TrimSpace(indicator),
		"the indicator must describe the profile now on screen, whose deploy is a no-op after the switch")
	assert.Contains(t, library, "Beta Mod", "the library must list what the profile it moved to holds")
	assert.NotContains(t, library, "Alpha Mod", "and not what the profile it left held")
	assert.Empty(t, f.BrowserErrors())
}
