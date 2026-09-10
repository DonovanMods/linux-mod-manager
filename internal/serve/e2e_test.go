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
	"slices"
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

	// Issue 399 replaced the shell's static <title>lmm</title>: main.js#go
	// names the route it just entered. The application's own name is still
	// in there, so a tab is still identifiable as lmm's.
	assert.Equal(t, "Mission Control — "+f.Game.ID+"/"+f.Profile+" · lmm", title)
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

// openAddModsMenu opens the "Add mods ▾" dropdown wherever it currently
// renders (the library toolbar or the empty-library state - issue 339 puts
// the SAME component in both) and clicks the menu item whose text contains
// label.
func openAddModsMenu(label string) chromedp.Action {
	return chromedp.Tasks{
		chromedp.Click(`[data-action="add-mods"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.add-mods-menu__menu`, chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(`
			Array.from(document.querySelectorAll(".add-mods-menu__menu button"))
				.find((b) => b.textContent.includes(%q))?.click();
		`, label), nil),
	}
}

// TestE2E_AddModsMenu_SearchSourcesFocusesTheOmnibar is issue 339 (owner
// Demo 3): once a library has mods, Archive import and Adopt lived only
// under ⚙ Setup, and a populated library offered no "add mods" affordance
// at all - a user with a downloaded archive had to already know to go
// there. "Add mods ▾"'s own "Search sources…" lands on the design's
// EXISTING flow (the omnibar's own fan-out) rather than a fourth one, so
// its whole job is handing the keyboard to that field.
func TestE2E_AddModsMenu_SearchSourcesFocusesTheOmnibar(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		openAddModsMenu("Search sources"),
	)

	var focusedIsOmnibar bool
	f.runInBrowser(t, chromedp.Evaluate(
		`document.activeElement === document.querySelector(".omnibar")`, &focusedIsOmnibar,
	))
	assert.True(t, focusedIsOmnibar, `"Search sources…" must focus the omnibar`)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_AddModsMenu_ImportAnArchiveOpensSetup covers the second entry,
// against a POPULATED library (library.js's own toolbar placement).
func TestE2E_AddModsMenu_ImportAnArchiveOpensSetup(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		openAddModsMenu("Import an archive"),
		chromedp.WaitVisible(`.setup-page`, chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector("#setup-panel h2")?.textContent.trim() === "Archive import"`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond),
		),
	)

	var heading string
	f.runInBrowser(t, textContent(`#setup-panel h2`, &heading))
	assert.Equal(t, "Archive import", heading)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_AddModsMenu_AdoptOpensSetup covers the third entry, against the
// EMPTY library state - issue 339's own "consider the same entries in the
// empty-library state so both states share one component", proven here by
// using the identical [data-action="add-mods"]/.add-mods-menu__menu
// selectors the populated-library scenarios above use.
func TestE2E_AddModsMenu_AdoptOpensSetup(t *testing.T) {
	f := newE2EFixture(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library .empty-state`, chromedp.ByQuery),
		openAddModsMenu("Adopt untracked mods"),
		chromedp.WaitVisible(`.setup-page`, chromedp.ByQuery),
		chromedp.Poll(
			`document.querySelector("#setup-panel h2")?.textContent.trim() === "Adopt"`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond),
		),
	)

	var heading string
	f.runInBrowser(t, textContent(`#setup-panel h2`, &heading))
	assert.Equal(t, "Adopt", heading)
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
//
// I-1 of the epic live review: this scenario timed out once in three suite
// runs (30.5 s, a timeout rather than an assertion) while passing 10/10 in
// isolation - a contention-sensitive wait, not a defect. chromedp.WaitVisible
// depends on the browser's node tracking noticing a mutation, which the
// harness has documented failing to do for a later removal (waitGone); asked
// of the PAGE with an explicit polling interval instead, the same question
// is answered by the page's own DOM every 50 ms and cannot be missed.
//
// The wait and the dismissing click are ONE in-page expression rather than
// two round trips. A succeeded job now releases its own control after a few
// seconds (I-2), so a separate Poll-then-Click would be racing that timer to
// prove the manual dismiss works; clicking inside the poll closes the window
// entirely.
func TestE2E_DeployDismissesBackToTheButton(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.Poll(`(() => {
			const el = document.querySelector('.job-progress[data-state="succeeded"] .job-progress__dismiss');
			if (!el) return false;
			el.click();
			return true;
		})()`, nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		chromedp.Poll(`document.querySelector('[data-action="deploy"]') !== null`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
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
	return pollUntil(`document.activeElement && document.activeElement.classList.contains("slide-over__panel")`)
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
		pollUntil(`document.querySelector(".slide-over__settings select").value === "pinned"`),
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
		pollUntil(`Array.from(document.querySelectorAll("button")).some((b) => b.textContent.trim() === "Roll back to the previous version")`),
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

	f.runInBrowser(t,
		// VersionsSection starts in its own "Loading versions…" state -
		// modPage.versions is fetched separately from the page's primary
		// ModFiles read - and the `.mod-page__table` waited on above is the
		// FILES table, so it is visible while the versions fetch is still in
		// flight. Polling the section out of that state is what waits the
		// fetch out; without it this read raced the fetch and, under the
		// load of a full -race package run, lost. Same idiom, same reason,
		// as the rollback scenario above.
		pollUntil(`!document.querySelector(".mod-page").textContent.includes("Loading versions\u2026")`),
		textContent(`.mod-page`, &versionsBody),
	)
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
		pollUntil(`(() => {
			const el = document.querySelector('[data-testid="updates-batch-rows"]');
			return el !== null && !el.textContent.includes("Beta Mod");
		})()`),
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
		pollUntil(`document.querySelectorAll(".mod-page__table tbody tr").length === 3`),
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
		pollUntil(`document.querySelectorAll(".mod-page__table tbody tr").length > 0`),
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

// TestE2E_OmnibarClearRestoresTheLibrary is issue 340 (owner Demo 3): once
// the omnibar has fanned out to sources, there was no obvious way back to
// the plain library short of erasing the text by hand. A ✕ control (visible
// whenever the query is non-empty, same as the fan-out button beside it)
// and Escape while the field has focus both do the same thing: empty the
// query, drop the "From sources" rows (main.js#searchSources' own "a blank
// query clears whatever fan-out is showing" rule) and restore the plain
// "Library (n)" heading - all without dropping focus out of the field, so a
// person can start a fresh search immediately.
func TestE2E_OmnibarClearRestoresTheLibrary(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(`.omnibar-results .search-result`, chromedp.ByQuery),
	)

	var libraryHeading string
	f.runInBrowser(t, textContent(`.library__toolbar .section-header`, &libraryHeading))
	require.Equal(t, "In your library (0)", libraryHeading,
		"the fixture's own Alpha Mod must not match \"boots\", or this scenario proves nothing about the heading")

	// --- The ✕ button. ---
	f.runInBrowser(t,
		chromedp.Click(`.omnibar__clear`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.omnibar-results`, chromedp.ByQuery),
	)

	var omnibarText string
	var focusedIsOmnibar bool
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelector(".omnibar").value`, &omnibarText),
		chromedp.Evaluate(`document.activeElement === document.querySelector(".omnibar")`, &focusedIsOmnibar),
		textContent(`.library__toolbar .section-header`, &libraryHeading),
	)
	assert.Empty(t, omnibarText, "✕ must empty the omnibar")
	assert.True(t, focusedIsOmnibar, "✕ must leave focus in the omnibar")
	assert.Equal(t, "Library (1)", libraryHeading, "the plain Library(n) heading must be restored")

	// --- Escape, while the omnibar itself has focus, does the same. ---
	f.runInBrowser(t,
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(`.omnibar-results .search-result`, chromedp.ByQuery),
		chromedp.Focus(`.omnibar`, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`.omnibar-results`, chromedp.ByQuery),
	)

	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelector(".omnibar").value`, &omnibarText),
		chromedp.Evaluate(`document.activeElement === document.querySelector(".omnibar")`, &focusedIsOmnibar),
		textContent(`.library__toolbar .section-header`, &libraryHeading),
	)
	assert.Empty(t, omnibarText, "Escape must empty the omnibar too")
	assert.True(t, focusedIsOmnibar, "Escape must leave focus in the omnibar rather than closing/blurring it")
	assert.Equal(t, "Library (1)", libraryHeading)

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

// TestE2E_ForceHintNamesTheConflictBypassOnInstallAndImport is N-8 of the
// epic re-review: every kind's Advanced Force option rendered the same
// generic sentence ("Carry on past a failure that would otherwise stop the
// flow"), but on install and archive import Force does something a lot
// louder - it bypasses the conflict refusal outright and overwrites without
// the Overwrite round-trip (internal/core/install.go's own `if !opts.Force
// && !opts.AcceptConflicts`) - and the CLI's own help is blunter about it:
// "install without conflict prompts". Deploy's Force is checked too, to pin
// that the generic sentence is deliberately unchanged for the kinds it
// still describes accurately.
func TestE2E_ForceHintNamesTheConflictBypassOnInstallAndImport(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	row := searchResultRow("fake", e2eSearchInstallModID)
	var installHint string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "boots", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`[data-testid="plan-advanced"] summary`, chromedp.ByQuery),
		textContent(`.modal label:has(input[name="force"])`, &installHint),
	)

	assert.Contains(t, installHint, "install without conflict prompts",
		"install's Force hint must mirror the CLI's own wording")
	assert.Contains(t, installHint, "Overwrite round-trip",
		"and name what it actually bypasses, not just \"carry on past a failure\"")

	f.runInBrowser(t, chromedp.Click(`.modal [data-action="cancel"]`, chromedp.ByQuery))

	var deployHint string
	f.runInBrowser(t,
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`[data-testid="plan-advanced"] summary`, chromedp.ByQuery),
		textContent(`.modal label:has(input[name="force"])`, &deployHint),
	)
	assert.Contains(t, deployHint, "Carry on past a failure",
		"deploy's Force does not bypass a conflict refusal, so the generic sentence must stay")
	assert.NotContains(t, deployHint, "conflict prompts",
		"the install-specific wording must not leak into a kind it does not describe")

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
// TopBar, so there was no activity bell/tray on that route at all, and the
// tray was the ONLY place the Overwrite affordance rendered. tray.js's own
// OverwriteButton now renders INLINE beside the failed row's own job chip
// (jobprogress.js), so this scenario completes on the ROW alone.
//
// The search page has since regained the frame's own bell (I-6, epic live
// review), so the scenario no longer proves the row is the only way in by
// the tray's absence. It proves it directly instead: the tray is never
// opened, and the round trip completes from the row.
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

	var trayOpen bool
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelector(".tray") !== null`, &trayOpen),
	)
	require.False(t, trayOpen, "nothing opens the tray here - the row is the only way in")

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
		pollUntil(betaLockBadge+` !== null`),
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
		pollUntil(betaLockBadge+` === null`),
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

// TestE2E_ReorderModal_DragShowsAGhostThatDisappearsOnDrop is issue 338
// (owner Demo 3): the reorder modal's drag is built on plain mouse events
// rather than the HTML5 Drag and Drop API (this file's own dragRowTo doc
// comment explains why - chromedp, like any other input-automation tool,
// only ever dispatches synthetic mousedown/mousemove/mouseup, which never
// raises the browser's own dragstart/dragover), which means no
// browser-provided drag image either: nothing visibly followed the cursor,
// so it was not obvious a drag was even in progress. dragRowToChecking
// (e2e_harness_test.go) splits dragRowTo's own press/move/release sequence
// so this scenario can assert something true only WHILE the drag is
// in-flight - the one thing an uninterruptible drag-and-drop helper cannot
// prove.
func TestE2E_ReorderModal_DragShowsAGhostThatDisappearsOnDrop(t *testing.T) {
	f := newE2EFixtureWithReorderableConflict(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--conflicts`, chromedp.ByQuery),
		chromedp.Click(`.card--conflicts [data-action="resolve"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="reorder-list"]`, chromedp.ByQuery),
	)

	var midDragGhost bool
	f.runInBrowser(t,
		dragRowToChecking("Mod X", "Mod Y", chromedp.Evaluate(
			`document.querySelector('[data-testid="reorder-ghost"]') !== null`,
			&midDragGhost,
		)),
	)
	assert.True(t, midDragGhost, "a drag ghost must be present in the DOM while a drag is in progress")

	var ghostAfterDrop bool
	f.runInBrowser(t, chromedp.Evaluate(
		`document.querySelector('[data-testid="reorder-ghost"]') !== null`, &ghostAfterDrop,
	))
	assert.False(t, ghostAfterDrop, "the drag ghost must be gone once the drop completes")

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
		pollUntil(profilesListContains("survival")),
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
		pollUntil(profilesListContains("outpost")),
	)
	_, err = f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "outpost")
	require.NoError(t, err, "the rename must actually rename the profile")
	settle()

	// Set outpost as default.
	f.runInBrowser(t,
		clickInRow(".profiles-row", "outpost", "Set default"),
		pollUntil(`Array.from(document.querySelectorAll(".profiles-row")).find((r) => r.textContent.includes("outpost")).textContent.includes("default")`),
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
		pollUntil(profilesListNotContains("outpost")),
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

// TestE2E_ProfileImportNoInstallOverridesTheInstallCheckbox is N-14 of the
// epic re-review: `profile import --no-install` is a hard override the wire
// has always carried (profileImportApplyRequest.NoInstall), but only the
// positive "Download and install" opt-in had a control - the outcome
// matched whenever it was left unchecked, but there was no way to check it
// AND still guarantee nothing is installed.
//
// The distinguishing evidence is "skipped" rather than "failed": these two
// mod ids are not registered in the fixture's fake source at all, so if the
// hard override did NOT win over a checked "Download and install", Apply
// would attempt real downloads that error out - a "failed" tally, not a
// "skipped" one. Seeing "skipped" with Install visibly checked is what
// proves the override, not merely the unchecked default, produced this.
func TestE2E_ProfileImportNoInstallOverridesTheInstallCheckbox(t *testing.T) {
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

	var installChecked, noInstallVisible bool
	f.runInBrowser(t,
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('.modal[data-kind="profile_import"] input[type="checkbox"]'))
				.find((el) => el.closest("label")?.textContent.includes("Download and install"))
				.click();
		`, nil),
		chromedp.Click(`.modal[data-kind="profile_import"] [data-testid="plan-advanced"] summary`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] input[name="no_install"]`, chromedp.ByQuery),
		chromedp.Click(`.modal[data-kind="profile_import"] input[name="no_install"]`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('.modal[data-kind="profile_import"] input[type="checkbox"]'))
				.find((el) => el.closest("label")?.textContent.includes("Download and install")).checked
		`, &installChecked),
		chromedp.Evaluate(`document.querySelector('.modal[data-kind="profile_import"] input[name="no_install"]').checked`, &noInstallVisible),
	)
	require.True(t, installChecked, "Download and install must be visibly checked - the override must win DESPITE it, not merely stand in for it")
	require.True(t, noInstallVisible, "the hard override checkbox must be checked too")

	f.runInBrowser(t,
		chromedp.Click(`.modal[data-kind="profile_import"] [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal[data-kind="profile_import"]`, chromedp.ByQuery),
	)

	require.Eventually(t, func() bool {
		p, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "imported")
		return err == nil && len(p.Mods) == 2
	}, 5*time.Second, 20*time.Millisecond, "the import job must still succeed and save the profile")

	list, err := f.Svc.ListMods(t.Context(), f.Game, "imported")
	require.NoError(t, err)
	assert.Empty(t, list.Mods, "no_install must have stopped anything from actually installing, even though Install was checked")

	f.runInBrowser(t,
		chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.tray`, chromedp.ByQuery),
	)
	var trayText string
	require.Eventually(t, func() bool {
		f.runInBrowser(t, textContent(`.tray`, &trayText))
		return strings.Contains(trayText, "profile_import") && strings.Contains(trayText, "skipped")
	}, 5*time.Second, 100*time.Millisecond, "the tray must show the pending mods as skipped, not attempted")
	assert.NotContains(t, trayText, "failed",
		"a failed download attempt would mean the checked Install box was not actually overridden")

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
	// M6, unit 8 gate review: the Profile card's title carried no count
	// where its three siblings read "⬆ Updates (1)" / "⚠ Health (2)" /
	// "⇄ Conflicts (1)".
	assert.Contains(t, card, "Profile (1)",
		"the card's title must carry its count, like every other attention card's")
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
		waitGone(`.modal`),
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
		// issue 344: the terminal STATE and the terminal LABEL do not arrive
		// together. jobprogress.js renders the outcome the instant the job
		// summary says "succeeded", while the tally behind "1 applied / 1
		// skipped" is a SECOND read (jobresult.js -> GET
		// /api/v1/jobs/{id}), so the control carries a bare "Done" for the
		// frames in between. Reading the text straight after the state wait
		// therefore sampled whichever label happened to be there, which is
		// what made this test fail once in a full -race suite run.
		//
		// The label is waited out rather than the render deferred: holding
		// the terminal render until the tally settles was tried, and it
		// loses the outcome entirely on any control whose CONTAINER
		// unmounts on completion (Mission Control's profile attention card
		// disappears the moment the profile is applied). Carrying the tally
		// on the job SUMMARY - so the first terminal render already has it -
		// is the regression-free fix, and is filed as #364 (post-v2.0).
		chromedp.Poll(
			`(() => {
				const el = document.querySelector(".card--updates .job-progress__text");
				return Boolean(el) && el.textContent.trim() !== "" && el.textContent.trim() !== "Done";
			})()`, nil,
			chromedp.WithPollingInterval(50*time.Millisecond),
		),
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
	var missionControls, appBars, libraries int
	var missionControlsAfterNav, appBarsAfterNav int
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
		// The indicator is derived from the mod list, which the route change
		// re-fetches - so this waits for the screen to have finished
		// following the machine rather than catching it mid-move. It is
		// itself an assertion: before the fix the indicator sat on
		// "1 change undeployed" forever, describing the profile the user had
		// left, for a profile whose Deploy plan says there is nothing to do.
		chromedp.Poll(
			`document.querySelector(".deploy-indicator")?.textContent.trim() === "Deployed"`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond),
		),
		// The count is the assertion the unit-8 gate re-review's R1 asked
		// for: the earlier selector-based assertions below each resolve
		// against whichever copy comes first in document order, which is
		// the STALE one when the navigate-during-commit bug reintroduces a
		// second, orphaned Mission Control - so they kept passing on the
		// doubled screen. A count does not have that blind spot.
		chromedp.Evaluate(`document.querySelectorAll(".mission-control").length`, &missionControls),
		chromedp.Evaluate(`document.querySelectorAll(".app-bar").length`, &appBars),
		chromedp.Evaluate(`document.querySelectorAll(".library").length`, &libraries),
		chromedp.Location(&location),
		chromedp.Evaluate(`Boolean(document.querySelector(".job-progress .profile-picker"))`, &pickerInsideJob),
		textContent(`.deploy-indicator`, &indicator),
		chromedp.Text(`.library`, &library, chromedp.ByQuery),

		// The orphan does not just sit on this screen - it survives further
		// SPA navigation (the re-review's own drive: it followed onto
		// Setup, and coming back made two AGAIN). A round trip to Setup and
		// back is what proves it is gone for good, not just absent from
		// the one screen the earlier assertions happened to check.
		chromedp.Click(`[data-action="setup"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.setup-page`, chromedp.ByQuery),
		chromedp.Click(`.mod-page__back`, chromedp.ByQuery),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".mission-control").length`, &missionControlsAfterNav),
		chromedp.Evaluate(`document.querySelectorAll(".app-bar").length`, &appBarsAfterNav),
	)

	assert.True(t, strings.HasSuffix(location, "/g/g1/hardcore"),
		"a confirmed switch must take the screen with it, not leave the URL on the profile it left")
	assert.False(t, pickerInsideJob,
		"the profile picker must never be the control a switch job morphs - it is how you navigate")
	assert.Equal(t, "Deployed", strings.TrimSpace(indicator),
		"the indicator must describe the profile now on screen, whose deploy is a no-op after the switch")
	assert.Contains(t, library, "Beta Mod", "the library must list what the profile it moved to holds")
	assert.Equal(t, 1, missionControls,
		"a confirmed switch must leave exactly one Mission Control on screen, not a stale copy and a live one")
	assert.Equal(t, 1, appBars,
		"a confirmed switch must leave exactly one top bar, not the profile left behind stacked above the one moved to")
	assert.Equal(t, 1, libraries,
		"a confirmed switch must leave exactly one library, not the old profile's stacked above the new one")
	assert.Equal(t, 1, missionControlsAfterNav,
		"the orphan must not survive a further SPA navigation away and back")
	assert.Equal(t, 1, appBarsAfterNav,
		"the orphan top bar must not survive a further SPA navigation away and back")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_KeyboardShortcutsHelpOpensAndReturnsFocus is Important 3 of the
// unit-8 gate review: the last entry in the design's own modal inventory
// (§Modals: "keyboard-shortcuts help") with nothing behind it, in an
// application that is now fully keyboard-driven and whose README documents
// every binding.
//
// Both routes in are driven - the `?` key from anywhere and the top bar's
// own control - along with the two things every modal in this application
// owes its user: Escape closes it, and focus comes back to what opened it.
// The rows themselves are shortcuts.js's, pinned to the README by
// TestSPAKeyboardShortcutsMatchTheREADME.
func TestE2E_KeyboardShortcutsHelpOpensAndReturnsFocus(t *testing.T) {
	f := newE2EFixture(t)

	var byKey, byButton, focused string
	var reopenedFromOmnibar bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),

		// The `?` key, from the page rather than from any control.
		chromedp.KeyEvent("?"),
		chromedp.WaitVisible(`.modal[data-kind="shortcuts"]`, chromedp.ByQuery),
		// The shell's Escape handler lives in an effect, and this modal opens
		// from a keystroke rather than a click - so the next key would
		// otherwise land before the listener that answers it exists.
		settleEffects(),
		chromedp.Text(`.modal[data-kind="shortcuts"] .shortcuts`, &byKey, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		waitGone(`.modal`),

		// A "?" typed into a text field is a character, not a command.
		chromedp.Focus(`.omnibar`, chromedp.ByQuery),
		chromedp.KeyEvent("?"),
		settleEffects(),
		chromedp.Evaluate(`Boolean(document.querySelector('.modal[data-kind="shortcuts"]'))`, &reopenedFromOmnibar),
		chromedp.Blur(`.omnibar`, chromedp.ByQuery),

		// The top bar's own control, and the focus it must get back.
		chromedp.Click(`[data-action="shortcuts"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="shortcuts"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Text(`.modal[data-kind="shortcuts"] .shortcuts`, &byButton, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		waitGone(`.modal`),
		chromedp.Evaluate(`document.activeElement?.dataset?.action ?? ""`, &focused),
	)

	assert.Contains(t, byKey, "Tab", "the help must list the traversal binding")
	assert.Contains(t, byKey, "Esc", "and the one every overlay answers to")
	assert.Contains(t, byKey, "step to the previous/next mod",
		"the rows are the README's own words, not a second wording of them")
	assert.Equal(t, byKey, byButton, "both ways in must open the same help")
	assert.False(t, reopenedFromOmnibar,
		"`?` typed into the omnibar is a character someone meant to search for")
	assert.Equal(t, "shortcuts", focused,
		"Escape must give focus back to the control that opened the modal")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ShortcutsHelpNamesTheRunningVersion is N-5 of the epic re-review:
// nothing in the browser said what version of lmm was running - the
// shortcuts help is where TestE2E_KeyboardShortcutsHelpOpensAndReturnsFocus
// already proves a user can reliably land, so it is where the version now
// reads too, sourced from the shell's own <meta name="lmm-version">
// (spa.go) rather than any /api/v1 document.
func TestE2E_ShortcutsHelpNamesTheRunningVersion(t *testing.T) {
	f := newE2EFixture(t)

	var versionText string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="shortcuts"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="lmm-version"]`, chromedp.ByQuery),
		chromedp.Text(`[data-testid="lmm-version"]`, &versionText, chromedp.ByQuery),
	)

	assert.Contains(t, versionText, "lmm "+e2eLmmVersion,
		"the shortcuts help must name the running server's own version")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SlideOverFindingsReadAsProse is M1 of the unit-8 gate review: the
// slide-over printed a finding's raw status slug ("version_mismatch") while
// the Health card two inches to its left rendered the same finding, for the
// same mod, as "version mismatch (recorded 1.0, source reports 2.0)" -
// verify.js#findingLabel, whose own doc comment says it was extracted "so
// the two surfaces can never drift". The slide-over was a third surface
// that never adopted it.
//
// Both surfaces are read in one pass, so the assertion is that they AGREE
// rather than that each matches a string typed twice into this test.
func TestE2E_SlideOverFindingsReadAsProse(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var card, panel string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--health`, chromedp.ByQuery),
		textContent(`.card--health .card__row-detail`, &card),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		clickModRow("Better Boots"),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		chromedp.Text(`.slide-over__panel`, &panel, chromedp.ByQuery),
	)

	assert.Contains(t, panel, "version mismatch (recorded 1.0, source reports 2.0)",
		"the slide-over must speak the same language as the card beside it")
	assert.NotContains(t, panel, "version_mismatch",
		"no raw status slug may reach a user")
	assert.Contains(t, panel, strings.TrimSpace(card),
		"the two surfaces render one finding through one helper, so their words must be identical")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_HealthCardTruncatesTheDetailBeforeTheName is M3 of the unit-8
// gate review. Both halves of a card row shrank at the same rate, so at
// 1280px a Health row read "Better …  version mismatch (recorded 1.0, so…"
// - both cut. The name is the identifier: a truncated detail still explains
// a mod you can name, while a truncated name explains nothing at all. Only
// the not-fixable branch carried a title, so a fixable row's cut text was
// unrecoverable without resizing the window.
//
// Measured in the browser rather than asserted against the stylesheet: a
// truncation is what a layout engine DOES with a rule, and scrollWidth
// versus clientWidth is the only place that fact exists.
func TestE2E_HealthCardTruncatesTheDetailBeforeTheName(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var nameCut, detailPresent, titled bool
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--health .card__row-name`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const n = document.querySelector(".card--health .card__row-name");
			return n.scrollWidth > n.clientWidth;
		})()`, &nameCut),
		chromedp.Evaluate(`Boolean(document.querySelector(".card--health .card__row-detail"))`, &detailPresent),
		chromedp.Evaluate(`(() => {
			const row = document.querySelector(".card--health .card__row");
			return Array.from(row.querySelectorAll(".card__row-name, .card__row-detail"))
				.every((el) => (el.title ?? "") !== "");
		})()`, &titled),
	)

	assert.True(t, detailPresent, "the row must actually carry both halves, or this proves nothing")
	assert.False(t, nameCut,
		"at 1280px the mod name must not be truncated - the detail gives up the room first")
	assert.True(t, titled,
		"both halves must carry their own full text as a title, so nothing cut is unrecoverable")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ASlowStaleHydrationCannotRepaintTheProfileSwitchedTo is C-1 of the
// epic live review, made deterministic.
//
// A confirmed profile switch leaves TWO hydrations in flight at once:
// onJobDone reads the route at the instant the job lands (main.js) - still
// the OLD profile, because the picker's own navigate is deferred to a
// microtask - and the route change that follows hydrates the new one. Both
// write Mission Control's four documents into the same store slots, so the
// screen belongs to whichever settles last. When that is the stale one, the
// application renders the previous profile's mods, updates, health and
// conflicts under the new profile's URL: exactly the wrong-context bug class
// path-based routing was introduced to make impossible, re-entering through
// the store instead of the URL.
//
// TestE2E_ProfileSwitchKeepsThePickerAndFollowsTheProfile catches this, but
// only when the scheduler happens to lose the race (~10% of runs). Here the
// fixture delays every GET scoped to the profile being LEFT, which
// guarantees the stale hydration lands last - so this scenario fails on
// every single run without the fence, and passes on every single run with
// it.
func TestE2E_ASlowStaleHydrationCannotRepaintTheProfileSwitchedTo(t *testing.T) {
	const staleReadDelay = 400 * time.Millisecond
	f := newE2EFixtureWithASwitchTargetAndSlowStaleReads(t, staleReadDelay)

	var location, indicator, library string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		// The profile being left is fully on screen before anything moves:
		// without this the scenario could switch away before the FIRST
		// (equally delayed) hydration had landed, and prove nothing.
		chromedp.Poll(
			`document.querySelector(".library")?.textContent.includes("Alpha Mod")`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond),
		),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.Click(`[data-action="switch"][data-profile="hardcore"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="switch"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__trigger[data-profile="hardcore"]`, chromedp.ByQuery),
		// The precondition: wait until the stale hydration has reached its
		// first write. That write is the scoped status pair, and a resource
		// timing entry is recorded when a response completes - so a second
		// `profile=default` status request (the first was the initial
		// hydration) means the response that would repaint the deploy
		// indicator with the other profile's state is in the page's hands.
		chromedp.Poll(
			`window.performance.getEntriesByType("resource").filter(`+
				`(e) => e.name.includes("/api/v1/status") && e.name.includes("profile=default")).length >= 2`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond),
		),
		// Then a settle window - deliberately a sleep, and the one place in
		// this suite where that is the right instrument. The assertion here
		// is an ABSENCE (no stale document lands), and an absence can only
		// be proved by giving the write that must not happen the time it
		// would have needed. Unfenced, the status commit above is followed
		// immediately by the four Mission Control reads, each of which pays
		// the fixture's delay once more before repainting the library; this
		// window is comfortably longer than that whole sequence. Fenced,
		// the hydration stops at the status pair and never issues them at
		// all, so there is no later request left to wait for instead.
		chromedp.Sleep(3*staleReadDelay),
		chromedp.Location(&location),
		textContent(`.deploy-indicator`, &indicator),
		chromedp.Text(`.library`, &library, chromedp.ByQuery),
	)

	assert.True(t, strings.HasSuffix(location, "/g/g1/hardcore"),
		"the switch must take the URL with it")
	assert.Contains(t, library, "Beta Mod",
		"the library must hold the profile now in the URL, not the one the stale hydration answered for")
	assert.NotContains(t, library, "Alpha Mod",
		"a hydration started under the profile the user left must not repaint the profile they moved to")
	assert.Equal(t, "Deployed", strings.TrimSpace(indicator),
		"the deploy indicator must describe the profile on screen")
	assert.Empty(t, f.BrowserErrors())
}

// succeededOriginReleaseBudget is main.js#succeededOriginReleaseMillis, as
// the two I-2 scenarios below need to reason about it: long enough that a
// pinned failure is provably pinned, short enough that a released success
// does not stretch the suite. Kept as a Go constant rather than read out of
// the module, because a scenario that derived its own budget from the code
// under test could not fail if that code changed.
const succeededOriginReleaseBudget = 4 * time.Second

// searchRefreshedAfterTheJob is the poll every C-2 scenario uses to sample
// the toast decision at a point where it has definitely been made.
//
// onJobDone runs to its toast decision in a microtask or two: it starts the
// re-hydrate, starts the search refresh, waits for any in-flight job start
// to bind, and then decides. The search refresh's own RESPONSE lands well
// after that, so a second /api/v1/search resource entry is a marker that is
// strictly later than the decision - which is what makes "no toast" an
// assertion rather than a sampling race.
const searchRefreshedAfterTheJob = `window.performance.getEntriesByType("resource")` +
	`.filter((e) => e.name.includes("/api/v1/search")).length >= 2`

// TestE2E_OmnibarInstallReportsInlineWithoutAlsoToasting is C-2 of the epic
// live review, success half.
//
// The design's toast rule is "completion/failure when its origin isn't
// on-screen; never for things in view" (§Jobs). Installing from the omnibar
// broke it on the application's single most common flow: onJobDone called
// refreshSearchResults() BEFORE reading isOriginMounted, and that call puts
// the omnibar's result list into its loading state, which unmounts the very
// row rendering the outcome. By the time the rule was consulted, the answer
// had been changed by the code asking the question - so every omnibar
// install reported inline AND toasted.
func TestE2E_OmnibarInstallReportsInlineWithoutAlsoToasting(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	row := searchResultRow("fake", e2eSearchInstallModID)
	var toasts int
	var inline string
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
		chromedp.Poll(searchRefreshedAfterTheJob, nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		textContent(row+` .job-progress`, &inline),
		chromedp.Evaluate(`document.querySelectorAll(".toast").length`, &toasts),
	)

	assert.Contains(t, inline, "Done", "the outcome must resurface on the row that started it")
	assert.Zero(t, toasts,
		"the row is on screen and already says so - a toast repeating it is the design's own "+
			"'never for things in view'")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SearchPageInstallConflictReportsInlineWithoutAlsoToasting is C-2's
// failure half, on the route where the toast is most misleading: the search
// page has no tray, so its inline row is the ONLY place the conflict and its
// Overwrite affordance live - and a toast beside it says the same sentence
// with nothing to do about it.
func TestE2E_SearchPageInstallConflictReportsInlineWithoutAlsoToasting(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	row := searchResultRow("fake", e2eSearchConflictModID)
	var toasts int
	var inline string
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("clash")),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="failed"]`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` button[data-action="overwrite"]`, chromedp.ByQuery),
		chromedp.Poll(searchRefreshedAfterTheJob, nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		textContent(row+` .job-progress`, &inline),
		chromedp.Evaluate(`document.querySelectorAll(".toast").length`, &toasts),
	)

	assert.Contains(t, inline, "conflict", "the failure must resurface on the row, with its own next step")
	assert.Zero(t, toasts,
		"the failure is on screen with the affordance that answers it - a toast can only repeat it")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SucceededDeployReleasesTheButtonOnItsOwn is I-2 of the epic live
// review.
//
// A finished job stayed on its control until the user dismissed it by hand.
// That rule reads correctly on a row or a card - the outcome is where you
// left it - but on the top bar's DEPLOY it means the application's primary
// action is unavailable until you close a success message: install
// something else and the way to deploy it is to first dismiss the last
// deploy's "Done ✕". A success has nothing left to say a few seconds later
// (the tray keeps the record), so it releases the control on its own.
func TestE2E_SucceededDeployReleasesTheButtonOnItsOwn(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
		// Nothing is clicked between here and the assertion: the button
		// comes back because the timer ran, not because anything dismissed
		// it.
		chromedp.Poll(`document.querySelector('[data-action="deploy"]') !== null`,
			nil, chromedp.WithPollingInterval(100*time.Millisecond)),
	)

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FailedDeployStaysPinnedUntilDismissed is I-2's other half, and the
// reason the release is not simply "every finished job".
//
// A failure carries the next step - the message, and often an affordance
// that answers it (the conflict round trip's Overwrite) - so it waits for
// the user rather than for a timer. This scenario deliberately waits LONGER
// than the success release before it looks.
func TestE2E_FailedDeployStaysPinnedUntilDismissed(t *testing.T) {
	f := newE2EFixtureWithFailingDeploy(t)

	var stillFailed, deployButtonBack bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="failed"]`, chromedp.ByQuery),
		// Longer than a success would need to release itself, so "still
		// pinned" is a fact about the rule and not about how fast this ran.
		chromedp.Sleep(2*succeededOriginReleaseBudget),
		chromedp.Evaluate(`document.querySelector('.job-progress[data-state="failed"]') !== null`, &stillFailed),
		chromedp.Evaluate(`document.querySelector('[data-action="deploy"]') !== null`, &deployButtonBack),
	)

	assert.True(t, stillFailed, "a failure carries its own next step and must wait for the user, not a timer")
	assert.False(t, deployButtonBack, "the control is still the failure's, until it is dismissed")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ConflictsCardSaysWhatToDoAboutAStaleWinner is I-4 of the epic live
// review.
//
// A conflict is "stale" when the file's CURRENT deployed provider is not the
// one the profile's load order now names - a fact about the deploy, not
// about the conflict, so the sentence has to say what closes the gap. The
// card rendered a bare "(stale)"; `lmm conflicts` renders
// "(stale — redeploy to apply)" (cmd/lmm/conflicts.go) for the identical
// document. The web told the user something was wrong and not what to do,
// on the one card whose whole job is to name a next step.
//
// The scenario makes a conflict stale the way a user does: reorder the
// profile so the other mod wins, without redeploying.
func TestE2E_ConflictsCardSaysWhatToDoAboutAStaleWinner(t *testing.T) {
	f := newE2EFixtureWithReorderableConflict(t)

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--conflicts`, chromedp.ByQuery),
		chromedp.Click(`.card--conflicts [data-action="resolve"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="reorder-list"]`, chromedp.ByQuery),
		chromedp.Poll(`(() => {
			const btn = Array.from(document.querySelectorAll(".reorder-row"))
				.find((r) => r.textContent.includes("Mod X"))
				?.querySelector('[aria-label="Move Mod X to highest priority"]');
			if (!btn || btn.disabled) return false;
			btn.click();
			return true;
		})()`, nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		chromedp.Click(`[data-action="save-order"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`[data-testid="reorder-list"]`, chromedp.ByQuery),
	)

	var card string
	f.runInBrowser(t,
		chromedp.Poll(`document.querySelector(".card--conflicts")?.textContent.includes("stale")`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		textContent(`.card--conflicts`, &card),
	)

	assert.Contains(t, card, "stale — redeploy to apply",
		"the card must carry the CLI's own actionable sentence, not a bare (stale)")
	assert.Empty(t, f.BrowserErrors())
}

// countH1s asks the page how many top-level headings it is rendering.
func countH1s(out *int) chromedp.Action {
	return chromedp.Evaluate(`document.querySelectorAll("h1").length`, out)
}

// TestE2E_EveryRouteRendersExactlyOneH1 is I-3's ratchet.
//
// The epic live review's keyboard/screen-reader pass found the application
// contained no heading elements at all outside the full mod page: every
// section title was a styled <p class="section-header"> or
// <p class="plan__heading">, so heading navigation - the primary way a
// screen-reader user moves around a page - found nothing on Mission
// Control, the chooser, first run, search or Setup. WCAG 1.3.1 and 2.4.6
// are about the structure, not the styling, and the classes are unchanged:
// the tags carry the meaning the CSS was already carrying visually.
//
// One h1 per route is the ratchet because both failure modes are real: zero
// (the state this fixes) leaves a screen reader with no landmark, and two
// leaves it with no idea which one names the page. Asked of a real browser
// because it is a claim about a rendered document, and because there is no
// other place in this repo that executes the SPA.
func TestE2E_EveryRouteRendersExactlyOneH1(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	routes := []struct {
		name  string
		path  string
		ready string
	}{
		{"home", f.HomePath(), `.mission-control[data-hydrated="true"]`},
		{"mod page", f.ModPagePath("fake", "a"), `.mod-page`},
		{"search page", f.BaseURL + "/g/" + f.Game.ID + "/" + f.Profile + "/search?q=a", `.search-page[data-hydrated="true"]`},
		{"setup", f.HomePath() + "/setup", `.setup-page`},
	}
	for _, route := range routes {
		var h1s int
		f.runInBrowser(t,
			chromedp.Navigate(route.path),
			chromedp.WaitVisible(route.ready, chromedp.ByQuery),
			countH1s(&h1s),
		)
		assert.Equal(t, 1, h1s, "%s must render exactly one <h1>", route.name)
	}
	assert.Empty(t, f.BrowserErrors())

	chooser := newE2EMultiGameFixture(t)
	var chooserH1s int
	chooser.runInBrowser(t,
		chromedp.Navigate(chooser.BaseURL+"/"),
		chromedp.WaitVisible(`.game-chooser[data-hydrated="true"]`, chromedp.ByQuery),
		countH1s(&chooserH1s),
	)
	assert.Equal(t, 1, chooserH1s, "the game chooser must render exactly one <h1>")
	assert.Empty(t, chooser.BrowserErrors())

	firstRun := newE2EFixtureNoGames(t)
	var firstRunH1s int
	firstRun.runInBrowser(t,
		chromedp.Navigate(firstRun.BaseURL+"/"),
		chromedp.WaitVisible(`[data-testid="first-run-setup"]`, chromedp.ByQuery),
		countH1s(&firstRunH1s),
	)
	assert.Equal(t, 1, firstRunH1s, "first run must render exactly one <h1>")
	assert.Empty(t, firstRun.BrowserErrors())
}

// headingTexts returns every h1/h2 the document currently renders, tagged
// with its level - so an assertion can say both "these are the sections"
// and "they are the level they claim to be".
func headingTexts(selector string, out *[]string) chromedp.Action {
	return chromedp.Evaluate(
		`Array.from(document.querySelectorAll(`+"`"+selector+"`"+`)).map(h => h.textContent.trim().replace(/\s+/g, " "))`,
		out,
	)
}

// TestE2E_EveryRouteRendersItsSectionsAsHeadings is IMP-2's ratchet, and
// TestE2E_EveryRouteRendersExactlyOneH1's sibling.
//
// The h1 ratchet counts h1s only, which is why it stayed green through the
// half of I-3 that never landed: Mission Control's four attention cards
// were still <p class="card__title"> and Setup's five sections contributed
// no heading at all, so the two screens a user spends the most time on
// answered heading navigation with "h1 → Library" and "h1" respectively.
// The CHANGELOG's a11y paragraph claims "its sections are real headings",
// and this is what makes that sentence checkable.
//
// The expected SET, not merely a count: a count cannot tell a section that
// regressed to a <p> from one that was renamed, and this ratchet's whole
// job is to notice a heading quietly becoming a styled paragraph again.
// The fixture is the one that renders all four cards at once
// (newE2EFixtureWithAttention: an available update, two health findings, a
// real file conflict, and a mod installed but absent from the profile) -
// a fixture where a card happened not to render would ratchet nothing.
func TestE2E_EveryRouteRendersItsSectionsAsHeadings(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var home []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelectorAll("h2").length === 6`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		headingTexts("h2", &home),
	)
	// The Snapshots card (issue 350) is the sixth, and it is LAST because it
	// renders below the library rather than in the attention row - and it
	// is present with a count of zero because, unlike an attention card,
	// it renders whether or not it has anything to show.
	assert.Equal(t, []string{
		"⬆ Updates (1)", "⚠ Health (2)", "◎ Profile (1)", "⇄ Conflicts (1)", "Library (3)",
		"⏱ Snapshots (0)",
	}, home, "Mission Control's four attention cards, its library and its snapshots are its sections")

	var modPage []string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "boots")),
		chromedp.WaitVisible(`.mod-page`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelectorAll(".mod-page h2").length === 5`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		headingTexts(".mod-page h2", &modPage),
	)
	// One level for all of them (MIN-3): Findings/Conflicts used to be h2
	// while Description/Changelog/Files/Versions/Job history were h3 - and
	// the h3s came AFTER the h2s, so they read as children of "Findings".
	// This row is the mix itself: Findings (the old h2) beside four of the
	// old h3s.
	assert.Equal(t, []string{"Findings (2)", "Changelog", "Files", "Versions", "Job history"}, modPage,
		"the full mod page's sections are siblings, not a Findings subtree")

	// Setup renders ONE section at a time (a tablist), so "its five
	// sections are real headings" is a claim about each tab in turn: the
	// panel's own h2 moves with the selection.
	for _, s := range []struct{ key, label string }{
		{"games", "Games"},
		{"auth", "Authentication"},
		{"sources", "Custom sources"},
		{"archive", "Archive import"},
		{"adopt", "Adopt"},
	} {
		var setup []string
		f.runInBrowser(t,
			chromedp.Navigate(f.HomePath()+"/setup"),
			chromedp.WaitVisible(`.setup-page`, chromedp.ByQuery),
			chromedp.Click(`.setup-nav__tab[data-section="`+s.key+`"]`, chromedp.ByQuery),
			chromedp.Poll(`document.querySelector("#setup-panel h2")?.textContent.trim() === `+
				"`"+s.label+"`", nil, chromedp.WithPollingInterval(50*time.Millisecond)),
			headingTexts("#setup-panel h2", &setup),
		)
		assert.Equal(t, []string{s.label}, setup,
			"Setup's %s panel must name itself with a real heading", s.key)
	}

	// The three routes whose whole content IS their h1 - a results
	// summary, a list of game cards, a single add form - have no sections
	// to name, and inventing one would be worse than having none. Pinned
	// so that a section ARRIVING on one of them has to come with a
	// heading.
	var search []string
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/g/"+f.Game.ID+"/"+f.Profile+"/search?q=a"),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"]`, chromedp.ByQuery),
		headingTexts(".search-page h2", &search),
	)
	assert.Empty(t, search, "the search page's own h1 is its only section")
	assert.Empty(t, f.BrowserErrors())

	chooser := newE2EMultiGameFixture(t)
	var chooserH2s []string
	chooser.runInBrowser(t,
		chromedp.Navigate(chooser.BaseURL+"/"),
		chromedp.WaitVisible(`.game-chooser[data-hydrated="true"]`, chromedp.ByQuery),
		headingTexts(".game-chooser h2", &chooserH2s),
	)
	assert.Empty(t, chooserH2s, "the chooser's own h1 is its only section")
	assert.Empty(t, chooser.BrowserErrors())
}

// TestE2E_EveryFramedRouteRendersExactlyOneMainAndOneNav is N-11 of the
// epic re-review, and TestE2E_EveryRouteRendersExactlyOneH1's sibling for
// landmarks rather than headings: before this, the application's only
// landmarks were `header` + `main` - no `nav` around either bar's
// navigation controls - so heading navigation could find a page's title but
// landmark navigation (the OTHER primary way a screen reader user moves
// around a page) had nothing to jump to for "the controls that move me
// somewhere else". Scoped to the four routes that carry a real navigation
// bar (topbar.js or awaybar.js); the chooser and first-run routes have no
// navigation controls at all (just a theme toggle) and are covered
// separately by the h1 ratchet.
func TestE2E_EveryFramedRouteRendersExactlyOneMainAndOneNav(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	routes := []struct {
		name  string
		path  string
		ready string
	}{
		{"home", f.HomePath(), `.mission-control[data-hydrated="true"]`},
		{"mod page", f.ModPagePath("fake", "a"), `.mod-page`},
		{"search page", f.BaseURL + "/g/" + f.Game.ID + "/" + f.Profile + "/search?q=a", `.search-page[data-hydrated="true"]`},
		{"setup", f.HomePath() + "/setup", `.setup-page`},
	}
	for _, route := range routes {
		var mains, navs int
		f.runInBrowser(t,
			chromedp.Navigate(route.path),
			chromedp.WaitVisible(route.ready, chromedp.ByQuery),
			chromedp.Evaluate(`document.querySelectorAll("main").length`, &mains),
			// A role attribute overrides the implicit ARIA role a <nav> would
			// otherwise carry - Setup's own section switcher is a <nav
			// role="tablist">, a real tab widget rather than a second
			// navigation landmark, so it does not count here.
			chromedp.Evaluate(`document.querySelectorAll('nav:not([role="tablist"])').length`, &navs),
		)
		assert.Equal(t, 1, mains, "%s must render exactly one <main>", route.name)
		assert.Equal(t, 1, navs, "%s must render exactly one navigation-landmark <nav>", route.name)
	}
	assert.Empty(t, f.BrowserErrors())
}

// unlabelledFormControls returns every visible input/select/textarea the
// document currently renders that has no accessible name: no aria-label, no
// aria-labelledby pointing at text, and no <label> (wrapping or `for`)
// carrying text either - the three sources a browser's accessibility tree
// actually looks at. Returned as trimmed outerHTML fragments rather than a
// bare count, so a failure names the element instead of leaving the reader
// to go find it.
func unlabelledFormControls(out *[]string) chromedp.Action {
	return chromedp.Evaluate(`
		Array.from(document.querySelectorAll("input,select,textarea"))
			.filter((el) => {
				if ((el.getAttribute("aria-label") || "").trim()) return false;
				const labelledby = el.getAttribute("aria-labelledby");
				if (labelledby && labelledby.split(/\s+/).every(
					(id) => (document.getElementById(id)?.textContent || "").trim(),
				)) return false;
				if (el.labels && Array.from(el.labels).some((l) => l.textContent.trim())) return false;
				return true;
			})
			.map((el) => el.outerHTML.replace(/\s+/g, " ").slice(0, 160))
	`, out)
}

// TestE2E_EveryFormControlHasAnAccessibleName is N-3 of the epic re-review:
// the omnibar (topbar.js) is the one unlabelled input in the whole
// application - a placeholder alone, which is not a reliable accessible
// name and is not exposed by every assistive technology once a value is
// present - and it is the app's primary input. Checked across every route
// that carries the top bar or the away bar, since either could regress a
// control back to placeholder-only labelling without this ratchet noticing.
func TestE2E_EveryFormControlHasAnAccessibleName(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	routes := []struct {
		name  string
		path  string
		ready string
	}{
		{"home", f.HomePath(), `.mission-control[data-hydrated="true"]`},
		{"mod page", f.ModPagePath("fake", "boots"), `.mod-page`},
		{"search page", f.BaseURL + "/g/" + f.Game.ID + "/" + f.Profile + "/search?q=a", `.search-page[data-hydrated="true"]`},
	}
	for _, route := range routes {
		var gaps []string
		f.runInBrowser(t,
			chromedp.Navigate(route.path),
			chromedp.WaitVisible(route.ready, chromedp.ByQuery),
			unlabelledFormControls(&gaps),
		)
		assert.Empty(t, gaps, "%s must have no unlabelled input/select/textarea", route.name)
	}

	for _, section := range []string{"games", "auth", "sources", "archive", "adopt"} {
		var gaps []string
		f.runInBrowser(t,
			chromedp.Navigate(f.HomePath()+"/setup?section="+section),
			chromedp.WaitVisible(`.setup-page`, chromedp.ByQuery),
			unlabelledFormControls(&gaps),
		)
		assert.Empty(t, gaps, "Setup's %s panel must have no unlabelled input/select/textarea", section)
	}
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_TheYAMLEditorReallyHasSpellcheckOff is IMP-3 of the closing
// wave's gate review.
//
// setupsources.js has carried spellcheck="false" since #333 and the epic
// reviewer still saw red squiggles under every YAML key. Reading the source
// says the attribute is there; reading the DOM says what it became. Preact
// assigns spellcheck as a PROPERTY, and the non-empty string "false" is
// truthy, so the element ended up with spellcheck === true and
// getAttribute("spellcheck") === "true" - the exact opposite of what the
// markup appeared to ask for.
//
// So this asserts what a BROWSER computed, not what the file says: an
// earlier fix-wave item was closed as "does not reproduce" on a source read
// alone, and this is the class of bug only the DOM can settle.
func TestE2E_TheYAMLEditorReallyHasSpellcheckOff(t *testing.T) {
	f := newE2EFixture(t)

	var attribute string
	var property bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup?section=sources"),
		chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="new-source"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="source-editor"] textarea`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-testid="source-editor"] textarea').getAttribute("spellcheck") ?? "(absent)"`, &attribute),
		chromedp.Evaluate(`document.querySelector('[data-testid="source-editor"] textarea').spellcheck`, &property),
	)

	assert.Equal(t, "false", attribute,
		"the YAML editor's rendered spellcheck attribute must be \"false\"")
	assert.False(t, property,
		"and the DOM property with it - the string \"false\" is truthy, which is how this got missed")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ASlowStaleModPageHydrationCannotBlankTheOneOnScreen is MIN-2 of
// the closing wave's gate review - C-1's fence, applied to the two writes
// in hydrateModPage that escaped it.
//
// The primary read's write (and the error write beside it) were guarded by
// modPage.key alone. That key is "source/id" and carries NO profile, so it
// cannot tell two hydrations of one mod page apart at all - and onJobDone
// re-hydrates on every completed job, including one another client started.
// Here that leaves a job's re-hydrate for the profile being left in flight
// while the profile moved to has already loaded and written its own
// documents; the older one's filesReport write then resets detail, versions
// and updates to null, and since its own extras arrive AFTER its fence
// check they never replace them. The page keeps its Changelog and Versions
// headings but empties them until something hydrates it again.
//
// The profile is what makes this drivable rather than merely arguable: two
// hydrations under the SAME profile issue the identical URL, and Chrome's
// HTTP cache takes a single-writer lock on an in-flight cacheable GET, so
// the duplicate queues behind it and cannot land out of order at all. A
// different profile is a different URL - and the same modPage.key.
//
// Made deterministic the way C-1's own scenario is: a proxy delays exactly
// the stale profile's second mod-files read, so the stale hydration is
// GUARANTEED to land last rather than winning a scheduler coin-toss.
func TestE2E_ASlowStaleModPageHydrationCannotBlankTheOneOnScreen(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	// A second profile carrying the same mod, so the page the scenario
	// moves to renders the same modPage.key from a different URL.
	_, err := f.Svc.NewProfileManager().Create(t.Context(), f.Game.ID, "other")
	require.NoError(t, err)
	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "boots", SourceID: "fake", Name: "Better Boots", Version: "1.0", GameID: f.Game.ID},
		ProfileName:  "other",
		Enabled:      true,
		FileIDs:      []string{"f1"},
		UpdatePolicy: domain.UpdateNotify,
	}))

	// The SECOND read scoped to "default" is the delayed one: the first is
	// the cold load that puts the page on screen, and the second belongs to
	// the job's re-hydrate - the one this scenario makes stale.
	f.BaseURL = startE2EProxyDelayingTheNthModFilesRead(t, f.BaseURL, "default", 2, 2*time.Second)

	modPage := func(profile string) string {
		return f.BaseURL + "/g/" + f.Game.ID + "/" + profile + "/mod/fake/boots"
	}

	f.runInBrowser(t,
		chromedp.Navigate(modPage("default")),
		chromedp.WaitVisible(`.mod-page`, chromedp.ByQuery),
		// The extras are the subject, so wait until they are really there.
		chromedp.Poll(`Array.from(document.querySelectorAll(".mod-page h2")).some(h => h.textContent.trim() === "Changelog")`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
	)

	// A job finishing anywhere re-hydrates the route on screen, and THAT
	// hydration's files read is the delayed one.
	startEnableFromAnotherClient(t, f, "fake", "x")
	time.Sleep(300 * time.Millisecond)

	// Move to the other profile's copy of this page while that is still
	// fetching - the same two calls router.js's own navigate() makes, so
	// this is an in-app route change and not a reload that would throw the
	// in-flight hydration away with the whole document.
	f.runInBrowser(t,
		chromedp.Evaluate(`window.history.pushState(null, "", "/g/`+f.Game.ID+`/other/mod/fake/boots");
			window.dispatchEvent(new PopStateEvent("popstate"));`, nil),
		chromedp.Poll(`Array.from(document.querySelectorAll(".mod-page h2")).some(h => h.textContent.trim() === "Changelog")`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
	)

	// Long enough for the delayed read to have landed and repainted. The
	// Changelog and Versions SECTIONS are the observable: both are gated on
	// modPage.detail / modPage.versions being non-null, so a write that
	// resets them to null takes the headings off the page entirely.
	var headings []string
	f.runInBrowser(t,
		chromedp.Sleep(3*time.Second),
		headingTexts(".mod-page h2", &headings),
	)

	assert.Contains(t, headings, "Changelog",
		"a hydration older than the one on screen must not blank the mod page's live detail")
	assert.Contains(t, headings, "Versions",
		"nor its versions table")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_EveryRouteKeepsTheActivityBellAndTheShortcutsHelp is I-6 of the
// epic live review.
//
// The three routes that leave home - Setup, the full mod page, the search
// page - each hand-rolled their own header (brand, "← Back to library",
// theme) and between them dropped the whole frame. Start a long archive
// import from Setup and there was no tray to watch it in; a deploy running
// in another tab was invisible there; the `?` key still worked but the
// button naming it was gone. Those are frame concerns, not home concerns.
//
// The bell is opened on each route, not merely counted, because a bell that
// renders and cannot open its tray would pass a presence check and still
// leave the user with nowhere to watch a job.
func TestE2E_EveryRouteKeepsTheActivityBellAndTheShortcutsHelp(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	routes := []struct {
		name  string
		path  string
		ready string
	}{
		{"home", f.HomePath(), `.mission-control[data-hydrated="true"]`},
		{"mod page", f.ModPagePath("fake", "a"), `.mod-page`},
		{"search page", f.BaseURL + "/g/" + f.Game.ID + "/" + f.Profile + "/search?q=a", `.search-page[data-hydrated="true"]`},
		{"setup", f.HomePath() + "/setup", `.setup-page`},
	}
	for _, route := range routes {
		var shortcutsButtons int
		f.runInBrowser(t,
			chromedp.Navigate(route.path),
			chromedp.WaitVisible(route.ready, chromedp.ByQuery),
			chromedp.Click(`.activity-bell__trigger`, chromedp.ByQuery),
			chromedp.WaitVisible(`.tray`, chromedp.ByQuery),
			chromedp.Evaluate(`document.querySelectorAll('[data-action="shortcuts"]').length`, &shortcutsButtons),
			// Escape closes it and hands the keyboard back to the trigger -
			// the same rule the home bar's dropdowns obey, now that both bars
			// share one implementation of it (dismiss.js). settleEffects
			// first: that listener is installed by an effect, and Preact
			// flushes effects after paint, which a headless browser is often
			// not doing.
			settleEffects(),
			chromedp.KeyEvent(kb.Escape),
			waitGone(`.tray`),
		)
		assert.Equal(t, 1, shortcutsButtons,
			"%s must offer the keyboard help, exactly once", route.name)
	}

	var focused string
	f.runInBrowser(t,
		chromedp.Click(`[data-action="shortcuts"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="shortcuts"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.KeyEvent(kb.Escape),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.Evaluate(`document.activeElement?.getAttribute("data-action") ?? ""`, &focused),
	)
	assert.Equal(t, "shortcuts", focused,
		"the help opened from Setup's own bar must return focus to the button that opened it")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FullModPageCarriesEverythingTheSlideOverDoes is I-5 of the epic
// live review.
//
// "More info →" led to a page with FEWER actions than the panel it came
// from: the slide-over offered Update, Enable/Disable, Uninstall, an
// editable lock and update policy, the mod's own findings and its
// conflicts; the full page offered Enable/Disable and the versions table.
// A deep link or a bookmark therefore landed on the LESS capable surface,
// on a page whose own design section opens with "Everything, unlimited
// room".
//
// Driven as a deep link rather than through the slide-over on purpose: the
// three profile-scoped documents this page now needs (mods, health,
// conflicts) are Mission Control's, and arriving here cold is the case
// where nothing has fetched them yet.
func TestE2E_FullModPageCarriesEverythingTheSlideOverDoes(t *testing.T) {
	f := newE2EFixtureWithAttention(t)

	var lockPresent, uninstall bool
	var policy, findings string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "boots")),
		chromedp.WaitVisible(`.mod-page`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="mod-page-findings"]`, chromedp.ByQuery),
		textContent(`[data-testid="mod-page-findings"]`, &findings),
		chromedp.Evaluate(`document.querySelector('.slide-over__settings input[type="checkbox"]') !== null`, &lockPresent),
		chromedp.Evaluate(`document.querySelector('.mod-page [data-action="uninstall"]') !== null`, &uninstall),
		chromedp.Evaluate(`document.querySelector(".slide-over__settings select")?.value ?? ""`, &policy),
	)

	assert.True(t, lockPresent, "the page must carry the editable lock the slide-over has")
	assert.True(t, uninstall, "the page must carry Uninstall")
	assert.Equal(t, "notify", policy, "the page must carry the editable update policy")
	assert.Contains(t, findings, "version mismatch",
		"the page must carry this mod's own verify findings, in the engine's own words")

	assert.Empty(t, f.BrowserErrors())

	// Conflicts, and the lock actually WRITING - a control that renders and
	// does nothing would satisfy every assertion above. Both are driven on
	// Mod Y, which is in the profile's own load order (Better Boots is
	// installed but never added to it, so its lock legitimately 404s), and
	// both run deliberately AFTER the BrowserErrors assertion: Mod X/Y are
	// seeded into the DB and the cache but not into the fake source's
	// catalog, so this page's LIVE reads (ModDetail, versions) 404 by
	// design - the degradation this page has always handled, and a network
	// log entry the plain assert.Empty above would fail on for a reason
	// that is not this scenario's subject.
	var conflicts string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "y")),
		chromedp.WaitVisible(`[data-testid="mod-page-conflicts"]`, chromedp.ByQuery),
		textContent(`[data-testid="mod-page-conflicts"]`, &conflicts),
		chromedp.Click(`.slide-over__settings input[type="checkbox"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.slide-over__settings input[type="checkbox"]').checked === true`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
	)
	assert.Contains(t, conflicts, "shared.esp",
		"the page must carry this mod's own file conflicts")
	assert.Contains(t, conflicts, "(wins)",
		"and say which side of each one it is on")

	list, err := f.Svc.ListMods(t.Context(), f.Game, "default")
	require.NoError(t, err)
	idx := slices.IndexFunc(list.Mods, func(m core.ModListing) bool { return m.ID == "y" })
	require.GreaterOrEqual(t, idx, 0)
	assert.True(t, list.Mods[idx].Locked, "the lock the page rendered must have reached the database")
}

// TestE2E_AdvancedOptionsReachTheFlow is C-3's option half.
//
// Every flag in this scenario has been on the wire since the unit that
// landed its kind; nothing in the SPA ever set one, so `--keep-cache`,
// `--show-archived`, `--no-deps`, `--no-hooks`, `--force` and
// `deploy --method/--purge/<mod-id>` were CLI-only in practice while the
// design's Scope claims full bidirectional parity.
//
// Both halves are driven, because they behave differently by construction:
// a PLAN-time option re-computes the plan (so the preview cannot describe
// one mutation while Confirm submits another), and an APPLY-time one is a
// local patch on a plan already computed. The assertions are on the END
// STATE - what is on disk and in the cache afterwards - not on the request.
func TestE2E_AdvancedOptionsReachTheFlow(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	// --- APPLY-time, and a PLAN-time twin: uninstall --keep-cache. ---
	// keep_cache rides both halves (kind_uninstall.go takes it twice), so
	// the preview's own sentence about the cache has to move with it.
	var noteBefore, noteAfter string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		clickModRow("Alpha Mod"),
		chromedp.WaitVisible(`.slide-over`, chromedp.ByQuery),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".slide-over__actions button")).find((b) => b.textContent.trim() === "Uninstall").click()`, nil),
		chromedp.WaitVisible(`.modal[data-kind="uninstall"] .plan`, chromedp.ByQuery),
		textContent(`[data-testid="uninstall-cache-note"]`, &noteBefore),
		chromedp.Click(`[data-testid="plan-advanced"] summary`, chromedp.ByQuery),
		chromedp.Click(`.modal input[name="keep_cache"]`, chromedp.ByQuery),
		// The re-plan is the assertion: the preview's cache sentence flips
		// because the SERVER recomputed it, not because the client edited
		// a string.
		chromedp.Poll(`document.querySelector('[data-testid="uninstall-cache-note"]')?.textContent.includes("kept")`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		textContent(`[data-testid="uninstall-cache-note"]`, &noteAfter),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)

	assert.Contains(t, noteBefore, "deleted", "the default preview says the cache goes too")
	assert.Contains(t, noteAfter, "kept", "and the re-planned one says it stays")

	require.Eventually(t, func() bool {
		list, err := f.Svc.ListMods(t.Context(), f.Game, "default")
		return err == nil && !slices.ContainsFunc(list.Mods, func(m core.ModListing) bool {
			return m.ID == "a"
		})
	}, 10*time.Second, 100*time.Millisecond, "the uninstall must actually have run")

	assert.True(t, f.Svc.GetGameCache(f.Game).Exists(f.Game.ID, "fake", "a", "1.0"),
		"--keep-cache must have kept the cached download the default would have deleted")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_DeployAllIncludesDisabledMods is IMP-1(a) of the closing wave's
// gate review.
//
// `deploy --all` has been on the wire since the unit that landed the kind
// (kind_deploy.go's deployPlanRequest.All) and 8C-A's own "already on the
// wire" list named it - but no control ever set it, so a disabled mod was
// web-undeployable while the design's Scope claims full bidirectional
// parity. It is a PLAN-time option (core's PlanDeploy selects
// `opts.All || mod.Enabled`), so the assertion is the one every plan-time
// option gets: the SERVER's own re-planned preview grows, and the apply
// that follows lands the disabled mod's bytes.
func TestE2E_DeployAllIncludesDisabledMods(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	// A third mod, in the profile's load order but DISABLED - the exact
	// row the default full-profile deploy skips.
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "c", SourceID: "fake", Name: "Gamma Mod", Version: "1.0", GameID: f.Game.ID},
		false, map[string][]byte{"gamma.pak": []byte("gamma")})
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "c", Version: "1.0"}))

	var before, after int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".modal .plan__mod").length`, &before),
		chromedp.Click(`[data-testid="plan-advanced"] summary`, chromedp.ByQuery),
		chromedp.Click(`.modal input[name="all"]`, chromedp.ByQuery),
		chromedp.Poll(`document.querySelectorAll(".modal .plan__mod").length === 3`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		chromedp.Evaluate(`document.querySelectorAll(".modal .plan__mod").length`, &after),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	assert.Equal(t, 2, before, "the default full-profile deploy skips the disabled mod")
	assert.Equal(t, 3, after, "and --all re-plans to include it")

	assert.FileExists(t, filepath.Join(f.Game.ModPath, "gamma.pak"),
		"the disabled mod's file must actually have been deployed")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_InstallSkipVerifySkipsTheChecksumRecord is IMP-1(b) of the
// closing wave's gate review.
//
// `lmm install --skip-verify` was the one CLI flag with no wire field at
// all: core.InstallOptions.SkipVerify existed and the CLI set it, but
// `grep -ri "skip.verify" internal/serve/` returned nothing. It is an
// APPLY-time option - it changes nothing about what the plan says, only
// whether the download's computed checksum is stored - so the assertion is
// on the DB row the install writes, and the control's absence would leave
// nothing that could clear it.
func TestE2E_InstallSkipVerifySkipsTheChecksumRecord(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	// The control mod first, installed with the DEFAULT options: without
	// it, "no checksum" would be indistinguishable from "this fixture
	// never records one".
	installFromOmnibar(t, f, "multi", searchResultRow("fake", e2eSearchMultiFileModID), false)
	installFromOmnibar(t, f, "boots", searchResultRow("fake", e2eSearchInstallModID), true)

	files, err := f.Svc.GetFilesWithChecksums(t.Context(), f.Game.ID, "default")
	require.NoError(t, err)

	sums := map[string]string{}
	for _, file := range files {
		sums[file.ModID] = file.Checksum
	}
	assert.NotEmpty(t, sums[e2eSearchMultiFileModID],
		"the default install must still record the checksum it computed")
	assert.Empty(t, sums[e2eSearchInstallModID],
		"--skip-verify must have kept the checksum out of the database")
	assert.Empty(t, f.BrowserErrors())
}

// installFromOmnibar fans out for `query`, installs `row` and waits for its
// job, optionally ticking the confirm step's Advanced "Skip checksum
// recording" first.
func installFromOmnibar(t *testing.T, f e2eSearchFixture, query, row string, skipVerify bool) {
	t.Helper()

	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, query, chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(row, chromedp.ByQuery),
		chromedp.Click(row+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
	)
	if skipVerify {
		f.runInBrowser(t,
			chromedp.Click(`[data-testid="plan-advanced"] summary`, chromedp.ByQuery),
			chromedp.Click(`.modal input[name="skip_verify"]`, chromedp.ByQuery),
			chromedp.Poll(`document.querySelector('.modal input[name="skip_verify"]').checked === true`,
				nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		)
	}
	f.runInBrowser(t,
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(row+` .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)
}

// TestE2E_PurgeOfAnEmptyProfileStillOffersItsConfirmGate is MIN-4 of the
// closing wave's gate review.
//
// plan_purge.js returned early on an empty plan WITHOUT rendering the
// type-the-profile-name input, while confirmplan.js's typedNameFor.purge
// still demands that name back for this kind - so Confirm sat permanently
// disabled with nothing on screen explaining why. Nothing was lost (there
// is nothing to purge), but a control that cannot be operated and does not
// say why is a bug report waiting to happen, and it would break outright
// the moment the input moved.
func TestE2E_PurgeOfAnEmptyProfileStillOffersItsConfirmGate(t *testing.T) {
	f := newE2EFixture(t)

	var note string
	var beforeTyping, afterTyping bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil),
		chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="purge-profile"][data-profile="default"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="purge"] .plan`, chromedp.ByQuery),
		textContent(`.modal .plan__note`, &note),
		chromedp.Evaluate(`document.querySelector('.modal [data-action="confirm"]').disabled`, &beforeTyping),
		chromedp.SendKeys(`input[name="purge-confirm"]`, "default", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.modal [data-action="confirm"]').disabled === false`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		chromedp.Evaluate(`document.querySelector('.modal [data-action="confirm"]').disabled`, &afterTyping),
	)

	assert.Contains(t, note, "Nothing to purge",
		"the empty plan still says what it found")
	assert.True(t, beforeTyping, "and still gates Confirm behind the profile name")
	assert.False(t, afterTyping, "which the user can now actually satisfy")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_PurgeFromTheProfilesModalEmptiesTheGameDirectory is C-3's purge
// half.
//
// `lmm purge` had no plan kind, no route and no control at all - one of the
// four whole commands the epic live review found web-unreachable in a UI
// whose design Scope claims full bidirectional parity. It is also the most
// destructive single click in the application, so it is the one that draws
// a type-the-profile-name gate; the scenario proves the gate is real by
// trying Confirm before typing.
func TestE2E_PurgeFromTheProfilesModalEmptiesTheGameDirectory(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)

	deployed := filepath.Join(f.Game.ModPath, "alpha.pak")
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)
	require.FileExists(t, deployed, "the scenario needs something deployed to purge")

	var confirmDisabled, confirmEnabled bool
	f.runInBrowser(t,
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil),
		chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="purge-profile"][data-profile="default"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="purge"] .plan`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.modal [data-action="confirm"]').disabled`, &confirmDisabled),
		chromedp.SendKeys(`input[name="purge-confirm"]`, "default", chromedp.ByQuery),
		chromedp.Poll(`document.querySelector('.modal [data-action="confirm"]').disabled === false`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		chromedp.Evaluate(`document.querySelector('.modal [data-action="confirm"]').disabled`, &confirmEnabled),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)

	assert.True(t, confirmDisabled,
		"a purge must not be one click away - Confirm stays disabled until the profile is typed back")
	assert.False(t, confirmEnabled, "and must become available once it is")

	require.Eventually(t, func() bool {
		_, err := os.Stat(deployed)
		return os.IsNotExist(err)
	}, 10*time.Second, 100*time.Millisecond, "the purge must actually have emptied the game directory")

	// Records preserved: the default (no --uninstall) is exactly what the
	// plan's own sentence promised.
	list, err := f.Svc.ListMods(t.Context(), f.Game, "default")
	require.NoError(t, err)
	assert.NotEmpty(t, list.Mods, "without --uninstall the mod records stay")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ProfileSyncFromTheCardAddsWhatIsInstalled is C-3's profile-sync
// half - the other whole command with no web path at all.
//
// Sync is the mirror image of `profile apply`: apply converges the INSTALL
// SET onto what the profile lists, sync converges the PROFILE onto what is
// actually installed. The fixture is seeded on the sync side of that
// mirror - a mod installed and enabled in the database that the profile's
// own load order does not list - which is what makes the Profile card
// render a Sync… at all.
func TestE2E_ProfileSyncFromTheCardAddsWhatIsInstalled(t *testing.T) {
	f := newE2EFixtureWithAnUnlistedInstall(t)

	var card string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--profile`, chromedp.ByQuery),
		textContent(`.card--profile`, &card),
		chromedp.Click(`.card--profile [data-action="sync-profile"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_sync"] .plan`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="sync-to-add"]`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)

	assert.Contains(t, card, "not in this profile",
		"the card must say which way the drift runs")

	require.Eventually(t, func() bool {
		profile, err := f.Svc.NewProfileManager().Get(t.Context(), f.Game.ID, "default")
		return err == nil && slices.ContainsFunc(profile.Mods, func(r domain.ModReference) bool {
			return r.ModID == "b"
		})
	}, 10*time.Second, 100*time.Millisecond,
		"the sync must have added the installed-but-unlisted mod to the profile's load order")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_RelinkFromTheRowMenuMovesTheModsIdentity is C-3's `lmm mod edit`
// half - the third whole command with no web path at all.
//
// The renderer IS the form here: a re-link needs the user to say WHERE to
// re-link to before there is anything to preview, and those fields are
// plan-time, so each change re-plans rather than opening a second dialog in
// front of the confirm modal. What is on screen is therefore always a
// preview of exactly what Confirm will submit.
func TestE2E_RelinkFromTheRowMenuMovesTheModsIdentity(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	var fromTo string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row"))
				.find((r) => r.textContent.includes("Alpha Mod"))
				.querySelector(".row-menu-cell button").click()
		`, nil),
		chromedp.WaitVisible(`.row-menu [data-action="relink"]`, chromedp.ByQuery),
		chromedp.Click(`.row-menu [data-action="relink"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="mod_relink"] .plan`, chromedp.ByQuery),
		// A blank form is a legal metadata-only edit, and the preview says
		// so rather than showing an empty modal with a live Confirm.
		textContent(`[data-testid="relink-from-to"]`, &fromTo),
		chromedp.SetValue(`input[name="relink-mod-id"]`, "renamed", chromedp.ByQuery),
		// SetValue fires `change`, which is the commit event this input
		// re-plans on - typing every keystroke into a round trip would be a
		// plan per character.
		chromedp.Poll(`document.querySelector('[data-testid="relink-from-to"]')?.textContent.includes("renamed")`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)

	assert.Contains(t, fromTo, "fake:a", "the untouched plan is the mod as it stands")

	require.Eventually(t, func() bool {
		list, err := f.Svc.ListMods(t.Context(), f.Game, "default")
		return err == nil && slices.ContainsFunc(list.Mods, func(m core.ModListing) bool {
			return m.ID == "renamed"
		})
	}, 10*time.Second, 100*time.Millisecond,
		"the re-link must have moved the mod's identity in the database")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ARefusedRelinkPlanDisablesConfirm is N-1 of the epic re-review
// (epic-rereview.md): the row ⋯ menu offers Re-link… on any mod including a
// locked one, and a blank re-link form is a legal metadata-only edit
// (core.RelinkPlan.Refusal only populates once the plan actually proposes a
// relink) - but naming a new mod id on a LOCKED ref turns the plan into one
// core.PlanRelinkMod refuses (RelinkPlan.Refusal, "...unlock it first...").
// The CLI refuses at plan time and never calls Apply
// (cmd/lmm/mod_edit.go:109); before this fix the web's Confirm stayed live,
// so the only way to learn the answer was to start a job that could not
// succeed.
func TestE2E_ARefusedRelinkPlanDisablesConfirm(t *testing.T) {
	f := newE2EFixtureWithDrillInModsAndALockedMod(t)

	var confirmDisabled bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row"))
				.find((r) => r.textContent.includes("Alpha Mod"))
				.querySelector(".row-menu-cell button").click()
		`, nil),
		chromedp.WaitVisible(`.row-menu [data-action="relink"]`, chromedp.ByQuery),
		chromedp.Click(`.row-menu [data-action="relink"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="mod_relink"] .plan`, chromedp.ByQuery),
		// A blank form is a metadata-only edit and refuses nothing yet - the
		// refusal only appears once a real re-link is proposed.
		chromedp.SetValue(`input[name="relink-mod-id"]`, "renamed", chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="relink-refusal"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.modal [data-action="confirm"]').disabled`, &confirmDisabled),
	)

	assert.True(t, confirmDisabled,
		"a re-link plan carrying a refusal must not offer a live Confirm")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_PakConversionTogglesOnlyWhereItApplies is C-3's `lmm mod convert`
// half, and the one the epic live review said it would not defer: it is the
// Icarus pak-conversion toggle on a first-class supported game, and it had
// no route at all.
//
// core.ModListing's convert_paks is a TRI-STATE - null means the question
// does not apply to this mod (not a merge-compile game, or no pak merge
// source), which is distinct from a non-null false meaning "applies, and is
// off". The control renders on the null case's absence as much as on the
// other's presence, so both are asserted.
func TestE2E_PakConversionTogglesOnlyWhereItApplies(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	var menuItems int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row"))
				.find((r) => r.textContent.includes("Alpha Mod"))
				.querySelector(".row-menu-cell button").click()
		`, nil),
		chromedp.WaitVisible(`.row-menu`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('.row-menu [data-action="toggle-convert"]').length`, &menuItems),
	)
	assert.Zero(t, menuItems,
		"a game with no pak merge source must not offer a conversion toggle at all - "+
			"convert_paks is null there, and a control for a question that does not apply is worse than none")
	assert.Empty(t, f.BrowserErrors())

	// And the case it DOES apply to: a DeployCompile game whose mod carries
	// a pak-kind retained file. The toggle is offered, and it writes.
	c := newE2EFixtureWithAConvertibleMod(t)
	var label string
	c.runInBrowser(t,
		chromedp.Navigate(c.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".mod-row"))
				.find((r) => r.textContent.includes("Convertible Mod"))
				.querySelector(".row-menu-cell button").click()
		`, nil),
		chromedp.WaitVisible(`.row-menu [data-action="toggle-convert"]`, chromedp.ByQuery),
		textContent(`.row-menu [data-action="toggle-convert"]`, &label),
		chromedp.Click(`.row-menu [data-action="toggle-convert"]`, chromedp.ByQuery),
	)

	assert.Equal(t, "Disable pak conversion", label,
		"the menu item must name what the click will DO, read off the mod's current state")

	require.Eventually(t, func() bool {
		list, err := c.Svc.ListMods(t.Context(), c.Game, "default")
		if err != nil || len(list.Mods) == 0 {
			return false
		}
		return list.Mods[0].ConvertPaks != nil && !*list.Mods[0].ConvertPaks
	}, 10*time.Second, 100*time.Millisecond,
		"the toggle must have reached the database")
	assert.Empty(t, c.BrowserErrors())
}

// TestE2E_SearchTagFilterAppearsOnlyForASourceThatHonoursIt is C-3's
// `lmm search --tag` half.
//
// Task A put ?tag= on GET /api/v1/search; nothing in the SPA set it. The
// interesting half of wiring it is not the field but the GATE: tag support
// varies by source (NexusMods honours it), nothing on the wire advertises
// the capability per source, and a filter that silently narrows nothing is
// worse than no filter. So both cases are driven - the fake source's game,
// which must not offer it, and a game mapping nexusmods, which must.
func TestE2E_SearchTagFilterAppearsOnlyForASourceThatHonoursIt(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)

	var fields int
	f.runInBrowser(t,
		chromedp.Navigate(f.SearchPagePath("boots")),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('.search-page input[name="tag"]').length`, &fields),
	)
	assert.Zero(t, fields,
		"a game whose sources do not honour tags must not offer a tag filter")
	assert.Empty(t, f.BrowserErrors())

	g := newE2EFixtureWithATagCapableSource(t)
	var tagged string
	g.runInBrowser(t,
		chromedp.Navigate(g.BaseURL+"/g/"+g.Game.ID+"/"+g.Profile+"/search?q=mod"),
		chromedp.WaitVisible(`.search-page[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.search-page input[name="tag"]`, chromedp.ByQuery),
		chromedp.SetValue(`.search-page input[name="tag"]`, "armour", chromedp.ByQuery),
		// The filter is server-side: the row that survives is the one the
		// SOURCE kept, not one this page hid. Polled on BOTH halves - the
		// negative alone is momentarily true of the loading state, which
		// contains neither row.
		chromedp.Poll(`(() => {
			const t = document.querySelector(".search-page")?.textContent ?? "";
			return t.includes("Armoured Mod") && !t.includes("Plain Mod");
		})()`, nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		textContent(`.search-page`, &tagged),
	)

	assert.Contains(t, tagged, "Armoured Mod",
		"the tagged row must survive the filter")
	assert.NotContains(t, tagged, "Plain Mod",
		"the untagged row must not")
	assert.Empty(t, g.BrowserErrors())
}

// TestE2E_DeployPreviewMarksWhichContenderWins is M-9 of the epic live
// review: the deploy plan listed both contenders' copies of a contested
// path with nothing to say which one would actually be there afterwards, so
// a preview over a real conflict read as if both would land. The fact lives
// in the Conflicts document this route has already fetched.
func TestE2E_DeployPreviewMarksWhichContenderWins(t *testing.T) {
	f := newE2EFixtureWithReorderableConflict(t)

	var plan string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--conflicts`, chromedp.ByQuery),
		chromedp.Click(`[data-action="deploy"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] .plan`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal .plan__winner`, chromedp.ByQuery),
		textContent(`.modal .plan`, &plan),
	)

	assert.Contains(t, plan, "(wins)",
		"the winning contender's copy of the contested path must say so")
	assert.Contains(t, plan, "(loses to",
		"and the losing one must name who takes it")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_AuthSurfaceNamesTheEnvironmentVariableAsText is M-1/D-3: the env
// var was a PLACEHOLDER in a ~190px field on a ~900px row - visibly
// truncated to "or set NEXUSMODS_API_KE" and gone entirely the moment the
// user typed. It is the only place the UI names the variable, and the
// README says it is shown beside the field.
func TestE2E_AuthSurfaceNamesTheEnvironmentVariableAsText(t *testing.T) {
	f := newE2EFixtureFromSource(t, newFakeSource("fake"))

	var body string
	var stillThereAfterTyping bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup?section=auth"),
		chromedp.WaitVisible(`.setup-auth__env-var`, chromedp.ByQuery),
		textContent(`.setup-auth__env-var`, &body),
		chromedp.SendKeys(`.setup-auth__login input[type="password"]`, "secret", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector(".setup-auth__env-var") !== null`, &stillThereAfterTyping),
	)

	assert.Contains(t, body, "_API_KEY",
		"the environment variable must be named as text, not as a placeholder")
	assert.True(t, stillThereAfterTyping,
		"and must survive the first keystroke, which a placeholder does not")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ModDescriptionRendersAsProseNotMarkup is #342: domain.Mod's
// Description carries the source's RAW markup by design (#86) and Preact
// renders a text child as the text it is, so the page showed the reader
// "<p>Adds bigger backpacks.</p>" - angle brackets and all - where the CLI
// has always printed clean prose. dangerouslySetInnerHTML is forbidden
// here, so core hands over a cleaned sibling (description_text) and both
// surfaces render that.
func TestE2E_ModDescriptionRendersAsProseNotMarkup(t *testing.T) {
	const rawHTML = "<p>Adds <b>bigger</b> backpacks.</p><p>Requires SKSE &amp; SkyUI.</p>"

	src := newFakeSource("fake")
	src.addMod(fakeSourceMod{
		Mod: domain.Mod{
			ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0",
			Author: "Ada Lovelace", Summary: "A tidy little mod.", Description: rawHTML,
		},
		Files:     []domain.DownloadableFile{{ID: "f1", Version: "1.0"}},
		Changelog: "Fixed a crash on load.",
	})
	f := newE2EFixtureFromSource(t, src)
	seedInstalledMod(t, f.Svc, f.Game,
		domain.Mod{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0",
			Author: "Ada Lovelace", Summary: "A tidy little mod.", Description: rawHTML, GameID: f.Game.ID},
		true, map[string][]byte{"alpha.esp": []byte("alpha")})
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "a", Version: "1.0"}))

	var pageText string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`.mod-page__prose`, chromedp.ByQuery),
		textContent(`.mod-page`, &pageText),
	)
	assert.Contains(t, pageText, "Adds bigger backpacks.")
	assert.Contains(t, pageText, "Requires SKSE & SkyUI.")
	assert.NotContains(t, pageText, "<p>", "the reader must never see the source's markup")
	assert.NotContains(t, pageText, "<b>")
	assert.NotContains(t, pageText, "&amp;")

	// The paragraph break survives as a real paragraph rather than as two
	// runs jammed together.
	var paragraphs []string
	f.runInBrowser(t, chromedp.Evaluate(
		`Array.from(document.querySelectorAll(".mod-page__prose")).map((p) => p.textContent.trim())`,
		&paragraphs))
	assert.Contains(t, paragraphs, "Adds bigger backpacks.")
	assert.Contains(t, paragraphs, "Requires SKSE & SkyUI.")

	// The slide-over reads the same ModDetail document (for its changelog),
	// so it must not surface the raw field either.
	var panelText string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Click(`.mod-row__name`, chromedp.ByQuery),
		chromedp.WaitVisible(`.slide-over__section[data-changelog-status="ready"]`, chromedp.ByQuery),
		textContent(`.slide-over`, &panelText),
	)
	assert.NotContains(t, panelText, "<p>", "the slide-over must not render the source's markup either")
	assert.NotContains(t, panelText, "<b>")
	assert.Empty(t, f.BrowserErrors())
}

// --- Snapshots (issue 350) ---

// newE2EFixtureWithASnapshottableProfile is newE2EFixtureWithLibrarySample
// plus a real deploy, so the game directory genuinely holds something a
// snapshot can record and a restore can put back.
func newE2EFixtureWithASnapshottableProfile(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixtureWithLibrarySample(t)

	// The library sample's mods are installed but not deployed, and a
	// snapshot's deployed-files manifest is one of the four halves the card
	// reports on - so deploy for real, through the plan/apply pair.
	plan, err := f.Svc.PlanDeploy(t.Context(), f.Game, f.Profile, core.DeployOptions{})
	require.NoError(t, err)
	_, err = f.Svc.ApplyDeploy(t.Context(), f.Game, plan, core.DeployOptions{}, nil)
	require.NoError(t, err)
	return f
}

// TestE2E_SnapshotsCard_RendersEmptyAndAlwaysOffersToRecordOne pins the
// card's own premise: unlike an attention card, it renders when there is
// nothing to show, because its value is knowing the safety net is there.
func TestE2E_SnapshotsCard_RendersEmptyAndAlwaysOffersToRecordOne(t *testing.T) {
	f := newE2EFixtureWithASnapshottableProfile(t)

	var card string
	var buttons int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="snapshots-card"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="snapshots-empty"]`, chromedp.ByQuery),
		textContent(`[data-testid="snapshots-card"]`, &card),
		chromedp.Evaluate(`document.querySelectorAll('[data-action="snapshot-now"]').length`, &buttons),
	)

	assert.Contains(t, card, "Snapshots (0)")
	assert.Contains(t, card, "No snapshots yet.")
	assert.Equal(t, 1, buttons, "the primary action is available precisely when there is nothing to show")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SnapshotsCard_SnapshotNowRecordsOneAndTheRowAppears drives the
// create write end to end - the button carries no text input at all,
// because the server applies core's own shared default name.
func TestE2E_SnapshotsCard_SnapshotNowRecordsOneAndTheRowAppears(t *testing.T) {
	f := newE2EFixtureWithASnapshottableProfile(t)

	var card string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="snapshots-empty"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="snapshot-now"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-action="snapshot-restore"]`, chromedp.ByQuery),
		textContent(`[data-testid="snapshots-card"]`, &card),
	)

	assert.Contains(t, card, "Snapshots (1)")
	assert.Contains(t, card, "3 mods", "the row says what the snapshot recorded")

	// And it really landed in the store the CLI reads.
	listing, err := f.Svc.ListSnapshots(t.Context(), f.Game.ID)
	require.NoError(t, err)
	require.Len(t, listing.Snapshots, 1)
	assert.Equal(t, 3, listing.Snapshots[0].Mods)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SnapshotsCard_RestoreGoesThroughTheConfirmPlanModal is the
// point of the card: the destructive half runs through the SAME confirm
// framework every other mutation in this UI uses, and its preview is what
// the user says yes to.
func TestE2E_SnapshotsCard_RestoreGoesThroughTheConfirmPlanModal(t *testing.T) {
	f := newE2EFixtureWithASnapshottableProfile(t)
	_, err := f.Svc.CreateSnapshot(t.Context(), f.Game, f.Profile, "known-good")
	require.NoError(t, err)

	var plan string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-action="snapshot-restore"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="snapshot-restore"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="snapshot_restore"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="snapshot_restore"]`, &plan),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	assert.Contains(t, plan, "Snapshot known-good")
	assert.Contains(t, plan, "undeploys 3 mods", "the preview says what it is about to do")

	// The restore's own safety copy is what proves the default applied
	// through the whole browser -> plan -> job path, not just in core.
	listing, err := f.Svc.ListSnapshots(t.Context(), f.Game.ID)
	require.NoError(t, err)
	require.Len(t, listing.Snapshots, 2, "the restore recorded where we were first")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SnapshotsCard_DeleteConfirmsInlineAndSaysWhatItKeeps pins the
// inline confirm ("modals stack at most one deep") AND the sentence that
// matters most on that control: deleting a snapshot never deletes the
// stored originals, which are the only copy of the files lmm replaced.
func TestE2E_SnapshotsCard_DeleteConfirmsInlineAndSaysWhatItKeeps(t *testing.T) {
	f := newE2EFixtureWithASnapshottableProfile(t)
	_, err := f.Svc.CreateSnapshot(t.Context(), f.Game, f.Profile, "doomed")
	require.NoError(t, err)

	var confirmRow string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-action="snapshot-delete"]`, chromedp.ByQuery),
		chromedp.Click(`[data-action="snapshot-delete"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-action="snapshot-delete-confirm"]`, chromedp.ByQuery),
		textContent(`[data-testid="snapshots-card"]`, &confirmRow),
		chromedp.Click(`[data-action="snapshot-delete-confirm"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="snapshots-empty"]`, chromedp.ByQuery),
	)

	assert.Contains(t, confirmRow, "Delete doomed?")
	assert.Contains(t, confirmRow, "stored originals are kept")

	listing, err := f.Svc.ListSnapshots(t.Context(), f.Game.ID)
	require.NoError(t, err)
	assert.Empty(t, listing.Snapshots)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FetchPhasesReachTheScreenHumanized is issue 269 Tier 3's progress
// claim, and it can only be checked in a browser: a source that fetches its
// own files (steamcmd, in production) reports phases core carries as
// workshop_fetch_started/progress/done, and the SPA's job readout must show
// them as readable text rather than as the wire names - without any
// SPA-side table of phases, which is what makes it safe for core to add
// one (progress.js's humanizePhase).
func TestE2E_FetchPhasesReachTheScreenHumanized(t *testing.T) {
	f := newE2EFixtureWithAFetchingSource(t, nil)

	var running string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "Fetched", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(searchResultRow("fake", e2eFetchModID), chromedp.ByQuery),
		chromedp.Click(searchResultRow("fake", e2eFetchModID)+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		// Wait for a FETCH frame specifically, not merely for the job: the
		// phase under test is one core only emits while the fetch runs.
		chromedp.Poll(
			`(document.querySelector('.job-progress__text')?.textContent ?? '').includes('Workshop fetch')`,
			nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		textContent(`.job-progress__text`, &running),
		chromedp.WaitVisible(searchResultRow("fake", e2eFetchModID)+` .job-progress[data-state="succeeded"]`, chromedp.ByQuery),
	)

	assert.Contains(t, running, "Workshop fetch", "the humanized phase reaches the screen")
	assert.NotContains(t, running, "workshop_fetch", "the wire phase name must not")

	_, err := os.Lstat(filepath.Join(f.Game.ModPath, "Mods", "fetched.pak"))
	assert.NoError(t, err, "the fetched item deploys like any other mod")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FetchRefusalRendersItsExplainerAndKeepsTheInstallOffered is issue
// 269 Tier 3's error surface. A publisher that refuses anonymous downloads
// is not a bug and not a dead end - there IS a way to get the item - so the
// mod surface renders the reason from the failure's TYPED details, and
// dismissing it puts the Install action straight back.
func TestE2E_FetchRefusalRendersItsExplainerAndKeepsTheInstallOffered(t *testing.T) {
	f := newE2EFixtureWithAFetchingSource(t, &domain.WorkshopFetchFailure{
		AppID: "431960", PublishedFileID: e2eFetchModID, Tool: "steamcmd",
		Reason: "This game's publisher does not allow anonymous Workshop downloads. " +
			"Subscribe to the item in the Steam client, then run `lmm import --workshop`.",
		Err: domain.ErrWorkshopAnonymousRefused,
	})

	var explainer string
	var installBackAfterDismiss bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.SendKeys(`.omnibar`, "Fetched", chromedp.ByQuery),
		chromedp.Click(`.omnibar__fanout`, chromedp.ByQuery),
		chromedp.WaitVisible(searchResultRow("fake", e2eFetchModID), chromedp.ByQuery),
		chromedp.Click(searchResultRow("fake", e2eFetchModID)+" .search-result__install", chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="install"] .plan`, chromedp.ByQuery),
		chromedp.Click(`.modal [data-action="confirm"]`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.modal`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress[data-state="failed"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.job-progress__explainer[data-explainer="steamcmd"]`, chromedp.ByQuery),
		textContent(`.job-progress__explainer`, &explainer),
		chromedp.Click(`.job-progress__dismiss`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`.job-progress`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(
			`!!document.querySelector(`+"`"+searchResultRow("fake", e2eFetchModID)+` .search-result__install`+"`"+`)`,
			&installBackAfterDismiss),
	)

	assert.Contains(t, explainer, "does not allow anonymous Workshop downloads")
	assert.Contains(t, explainer, "lmm import --workshop",
		"the refusal must name the route that DOES work")
	assert.True(t, installBackAfterDismiss, "dismissing the explainer puts the Install action back")
	assert.Empty(t, f.BrowserErrors())
}

// TestNotListedCount_IgnoresDisabledRows is issue 378. The Profile card is
// a subtraction of two documents rather than a plan: GET /api/v1/mods
// lists EVERY installed row, disabled ones included, while
// ProfileSummary.mod_count is the profile YAML's load order, which carries
// only enabled mods (core's PlanProfileSync builds ToAdd from mods
// "enabled in the DB but absent from the profile"). So one disabled mod
// produced one phantom "not in this profile's load order" - the card
// claimed drift, offered a Sync, and the Sync's own plan came back
// no_changes, which is also what `lmm profile sync --dry-run` said at the
// same moment.
//
// cards.js has no DOM in this function, so it is exercised directly here
// through a dynamic import in a real browser, the same module Mission
// Control runs - the pattern TestSortRows_RecentToleratesMissingInstalledAt
// established.
func TestNotListedCount_IgnoresDisabledRows(t *testing.T) {
	f := newE2EFixture(t)

	var counts []float64
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { notListedCount } = await import("/static/app/components/cards.js");
			const state = {
				route: { profile: "default" },
				status: { profiles: [{ name: "default", mod_count: 1 }] },
			};
			const oneEnabledOneDisabled = { mods: [
				{ key: "fake:a", enabled: true },
				{ key: "fake:b", enabled: false },
			] };
			const twoEnabled = { mods: [
				{ key: "fake:a", enabled: true },
				{ key: "fake:b", enabled: true },
			] };
			const onlyDisabled = { mods: [{ key: "fake:b", enabled: false }] };
			return [
				notListedCount(state, oneEnabledOneDisabled),
				notListedCount(state, twoEnabled),
				notListedCount(state, onlyDisabled),
			];
		})()`, &counts, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)

	require.Len(t, counts, 3)
	assert.Equal(t, float64(0), counts[0],
		"a disabled installed mod is not 'missing from the load order' - the profile YAML never carries one")
	assert.Equal(t, float64(1), counts[1],
		"a genuinely unlisted ENABLED mod is still counted, which is what the card exists to say")
	assert.Equal(t, float64(0), counts[2],
		"and the count never goes negative when the profile lists more than is enabled")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FullModPageRefusesRelinkOnALockedMod is issue 394. The page
// disabled its rollback button for a locked mod with a title explaining
// why, but rendered Re-link… unconditionally - and `lmm mod edit
// --source/--source-id` on a locked mod is refused ("mod is locked: …
// unlock with 'lmm mod unlock …' first"). #365 had already removed Re-link
// from the ROW menu for external mods; the locked case never got the same
// treatment on this page.
//
// The issue's own aside is answered too: a title on a disabled button is
// invisible to keyboard users, because a disabled button is not focusable.
// Both refused actions therefore also carry a VISIBLE line naming the
// unlock remedy, which is the only form of it a keyboard user can reach.
func TestE2E_FullModPageRefusesRelinkOnALockedMod(t *testing.T) {
	f := newE2EFixtureWithDrillInModsAndALockedMod(t)

	var relinkDisabled bool
	var relinkTitle, hint string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`[data-action="relink"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`document.querySelector('[data-action="relink"]').disabled`, &relinkDisabled),
		chromedp.Evaluate(`document.querySelector('[data-action="relink"]').title`, &relinkTitle),
		chromedp.Evaluate(`(document.querySelector('[data-testid="mod-page-locked-actions"]')?.textContent ?? "")`, &hint),
	)

	assert.True(t, relinkDisabled,
		"core refuses a re-link on a locked mod, so the page must not offer it live")
	assert.Contains(t, relinkTitle, "Unlock",
		"and must name the remedy, matching the rollback button four lines below")
	assert.Contains(t, hint, "Unlock",
		"the remedy must be readable without a hover, which a disabled button never gets")
	assert.Contains(t, hint, "lmm mod unlock fake:a",
		"and must name the command that lifts it, not merely the word")

	// The unlocked sibling on the same fixture proves the gate is the lock
	// and not the page.
	var otherDisabled bool
	var otherHints int
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "b")),
		chromedp.WaitVisible(`[data-action="relink"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`document.querySelector('[data-action="relink"]').disabled`, &otherDisabled),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="mod-page-locked-actions"]').length`, &otherHints),
	)
	assert.False(t, otherDisabled, "an unlocked mod still offers Re-link…")
	assert.Equal(t, 0, otherHints, "and says nothing about a lock it does not have")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_EveryRouteNamesItselfInTheTitleAndAnnouncesTheChange is issue
// 399. `<title>lmm</title>` was static and nothing under spa/ ever assigned
// document.title, so Mission Control, a mod page, search and Setup were all
// "lmm" — in the tab, in browser history and in the window switcher — and a
// screen-reader user got nothing at all on a pushState navigation, since
// the only role="status" regions in the application are job progress.
//
// Both halves are asserted here because both are claims about what a
// BROWSER does: the title after a real history navigation, and a live
// region that is in the accessibility tree while out of the visual layout.
func TestE2E_EveryRouteNamesItselfInTheTitleAndAnnouncesTheChange(t *testing.T) {
	f := newE2EFixtureWithDrillInMods(t)

	// The router's own navigation, driven the way router.js#navigate does
	// it, so this exercises a pushState route change rather than a fresh
	// document load - the case that had no announcement at all.
	pushState := func(path string) chromedp.Action {
		return chromedp.Evaluate(`(() => {
			window.history.pushState(null, "", `+"`"+path+"`"+`);
			window.dispatchEvent(new PopStateEvent("popstate"));
			return true;
		})()`, nil)
	}
	const regionJS = `(() => {
		const el = document.querySelector('[data-testid="route-announcer"]');
		if (!el) return null;
		return {
			text: el.textContent.trim(),
			live: el.getAttribute("aria-live"),
			visible: el.getBoundingClientRect().width > 2,
		};
	})()`
	type announcer struct {
		Text    string `json:"text"`
		Live    string `json:"live"`
		Visible bool   `json:"visible"`
	}

	var homeTitle, modTitle, searchTitle, setupTitle string
	var homeRegion, modRegion announcer
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Title(&homeTitle),
		chromedp.Evaluate(regionJS, &homeRegion),

		pushState(f.ContextPath()+"/mod/fake/a"),
		pollUntil(`document.title.includes("fake:a")`),
		settleEffects(),
		chromedp.Title(&modTitle),
		chromedp.Evaluate(regionJS, &modRegion),

		pushState(f.ContextPath()+"/search?q=alpha"),
		pollUntil(`document.title.toLowerCase().includes("search")`),
		chromedp.Title(&searchTitle),

		pushState(f.ContextPath()+"/setup?section=auth"),
		pollUntil(`document.title.toLowerCase().includes("setup")`),
		chromedp.Title(&setupTitle),
	)

	assert.Contains(t, homeTitle, "Mission Control", "the tab must name the view")
	assert.Contains(t, homeTitle, f.Game.ID, "and the context it is showing")
	assert.Contains(t, homeTitle, "lmm", "and still say which application it is")

	assert.Contains(t, modTitle, "fake:a", "a mod page names its mod")
	assert.NotEqual(t, homeTitle, modTitle,
		"two routes must not share one history entry title")
	assert.Contains(t, searchTitle, "alpha", "search names what was searched for")
	assert.Contains(t, setupTitle, "Setup")

	require.NotNil(t, homeRegion.Live, "a route announcer must exist on every route")
	assert.Equal(t, "polite", homeRegion.Live,
		"a route change interrupts nothing - it is announced politely")
	assert.False(t, homeRegion.Visible,
		"it is for the accessibility tree, not the visual layout")
	assert.Contains(t, homeRegion.Text, "Mission Control")
	assert.Contains(t, modRegion.Text, "fake:a",
		"and its text must actually change on a pushState navigation, or nothing is announced")

	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_TheRouteAnnouncerSurvivesTheChooserBoundary is P2 review Minor 1,
// on top of issue 399.
//
// The announcer only announces if its NODE survives the route change: an
// aria-live region that is unmounted and re-created with its new text
// already in it fires nothing. It used to ride inside the overlays
// fragment, which four of the five branches in App render at child index 1
// — but the chooser branch renders <header>, <main>, overlays, putting the
// fragment at index 2. Preact diffs children positionally, so leaving or
// entering the chooser met the old overlays Fragment with a <main>, a type
// change, and tore the live region down.
//
// Those are exactly the two transitions behind Mission Control's "Choose a
// different game" link, so this is reachable in the app rather than only on
// a cold load. Node identity is the assertion, because it is the thing the
// screen reader's behaviour actually depends on, and only a browser can
// answer it.
func TestE2E_TheRouteAnnouncerSurvivesTheChooserBoundary(t *testing.T) {
	// Two games and no default, so the chooser STAYS on screen rather than
	// redirecting to the single game (maybeRedirectFromChooser).
	f := newE2EMultiGameFixture(t)

	// A property set on the DOM node itself: it can only still be there if
	// this is the same node, which no attribute or text assertion can tell.
	const tagJS = `(() => {
		document.querySelector('[data-testid="route-announcer"]').__lmmSameNode = "yes";
		return true;
	})()`
	const readTagJS = `(() => {
		const el = document.querySelector('[data-testid="route-announcer"]');
		if (!el) return { present: false, tag: "", text: "" };
		return {
			present: true,
			tag: el.__lmmSameNode ?? "",
			text: el.textContent.trim(),
		};
	})()`
	type announcerNode struct {
		Present bool   `json:"present"`
		Tag     string `json:"tag"`
		Text    string `json:"text"`
	}

	pushState := func(path string) chromedp.Action {
		return chromedp.Evaluate(`(() => {
			window.history.pushState(null, "", `+"`"+path+"`"+`);
			window.dispatchEvent(new PopStateEvent("popstate"));
			return true;
		})()`, nil)
	}

	var intoGame, backToChooser announcerNode
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/"),
		chromedp.WaitVisible(`.game-chooser[data-hydrated="true"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(tagJS, nil),

		// chooser -> home: picking a game.
		pushState("/g/"+f.GameA.ID+"/default"),
		pollUntil(`document.title.includes("Mission Control")`),
		settleEffects(),
		chromedp.Evaluate(readTagJS, &intoGame),

		// home -> chooser: Mission Control's "Choose a different game".
		pushState("/"),
		chromedp.WaitVisible(`.game-chooser[data-hydrated="true"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(readTagJS, &backToChooser),
	)

	require.True(t, intoGame.Present, "the announcer must exist on Mission Control")
	assert.Equal(t, "yes", intoGame.Tag,
		"picking a game must keep the SAME live-region node - a rebuilt one announces nothing")
	assert.Contains(t, intoGame.Text, "Mission Control",
		"and the surviving node must carry the new route's name")

	require.True(t, backToChooser.Present, "the announcer must exist on the chooser")
	assert.Equal(t, "yes", backToChooser.Tag,
		"and leaving a game must keep it too")
	assert.NotContains(t, backToChooser.Text, "Mission Control",
		"with the chooser's own name in it")

	assert.Empty(t, f.BrowserErrors())
}
