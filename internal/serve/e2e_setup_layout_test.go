package serve_test

// #421: Setup -> Add Game is as wide as its panel, the installed-games list
// lines its names up and does not truncate them, and a game that is already
// configured is changed with "Edit…" on its Games-table row rather than
// offered again by either add flow.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pickerLayoutJS measures the open installed-games list: whether any name
// or path is clipped, how many distinct left edges the names and the paths
// have (one each when they form columns), and whether the form reaches past
// the viewport.
const pickerLayoutJS = `(() => {
	const list = document.querySelector('[data-testid="setup-add-picker"] .setup-detect__list');
	const clipped = (el) => el.scrollWidth > el.clientWidth + 1;
	const names = [...list.querySelectorAll(".setup-detect__name")];
	const paths = [...list.querySelectorAll(".setup-detect__path")];
	const lefts = (els) => new Set(els.map((e) => Math.round(e.getBoundingClientRect().left))).size;
	const form = document.querySelector('[data-testid="setup-add-game"]').getBoundingClientRect();
	return {
		rows: names.length,
		clippedNames: names.filter(clipped).length,
		clippedPaths: paths.filter(clipped).length,
		nameColumns: lefts(names),
		pathColumns: lefts(paths),
		formWidth: Math.round(form.width),
		overflows: form.right > window.innerWidth + 1,
	};
})()`

type pickerLayout struct {
	Rows         int  `json:"rows"`
	ClippedNames int  `json:"clippedNames"`
	ClippedPaths int  `json:"clippedPaths"`
	NameColumns  int  `json:"nameColumns"`
	PathColumns  int  `json:"pathColumns"`
	FormWidth    int  `json:"formWidth"`
	Overflows    bool `json:"overflows"`
}

// TestE2E_AddGamePicker_IsFluidAlignedAndUntruncated opens the first-run
// picker (two unconfigured games, different name lengths) at a narrow and a
// wide viewport.
func TestE2E_AddGamePicker_IsFluidAlignedAndUntruncated(t *testing.T) {
	f := newE2EFixtureNoGames(t)
	writeE2ESteamDetectFixture(t, f.Svc.ConfigDir())

	measure := func(width int) pickerLayout {
		var got pickerLayout
		f.runInBrowser(t,
			chromedp.EmulateViewport(int64(width), 1000),
			chromedp.Navigate(f.BaseURL+"/"),
			chromedp.WaitVisible(`[data-testid="setup-add-game"]`, chromedp.ByQuery),
			clickWhenSettled(`[data-action="pick-installed"]`),
			pollUntil(`document.querySelectorAll('[data-action="pick-installed-row"]').length === 2`),
			chromedp.Evaluate(pickerLayoutJS, &got),
		)
		return got
	}

	narrow := measure(800)
	wide := measure(1600)

	for name, got := range map[string]pickerLayout{"800px": narrow, "1600px": wide} {
		assert.Equal(t, 2, got.Rows, name)
		assert.Zero(t, got.ClippedNames, "%s: a game name is never cut off", name)
		assert.Zero(t, got.ClippedPaths, "%s: an install path wraps rather than being cut off", name)
		assert.Equal(t, 1, got.NameColumns, "%s: the names start on one line", name)
		assert.Equal(t, 1, got.PathColumns, "%s: the paths form a column beside them", name)
		assert.False(t, got.Overflows, "%s: the form stays inside the viewport", name)
	}
	assert.Greater(t, narrow.FormWidth, 480,
		"the form follows its panel's width rather than a fixed 30rem")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_AddGame_ConfiguredGameIsEditedNotOfferedAgain configures the
// detect fixture's curated game, then checks both add flows list only the
// other one (and say why), and that the row's "Edit…" opens every editor.
func TestE2E_AddGame_ConfiguredGameIsEditedNotOfferedAgain(t *testing.T) {
	f := newE2EFixture(t)
	steam := writeE2ESteamDetectFixture(t, f.Svc.ConfigDir())
	f.Game.InstallPath = filepath.Join(steam.SteamRoot, "steamapps", "common", "E2EDetectGame")
	f.Game.ModPath = filepath.Join(f.Game.InstallPath, "Data")
	require.NoError(t, os.MkdirAll(f.Game.ModPath, 0o755))
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))

	for _, width := range []int{800, 1600} {
		t.Run(fmt.Sprintf("%dpx", width), func(t *testing.T) {
			var pickRows, detectRows int
			var pickNames, detectNames []string
			var pickNote, detectNote string
			f.runInBrowser(t,
				chromedp.EmulateViewport(int64(width), 1000),
				chromedp.Navigate(f.SetupPath("games")),
				chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
				clickWhenSettled(`.setup-section__actions button:nth-child(2)`),
				chromedp.WaitVisible(`[data-testid="setup-add-game"]`, chromedp.ByQuery),
				clickWhenSettled(`[data-action="pick-installed"]`),
				pollUntil(`document.querySelector('[data-testid="picker-configured-hidden"]') !== null`),
				chromedp.Evaluate(`document.querySelectorAll('[data-action="pick-installed-row"]').length`, &pickRows),
				chromedp.Evaluate(`[...document.querySelectorAll('[data-action="pick-installed-row"]')].map((b) => b.textContent.trim())`, &pickNames),
				textContent(`[data-testid="picker-configured-hidden"]`, &pickNote),
				clickWhenSettled(`.setup-section__actions button:nth-child(1)`),
				pollUntil(`document.querySelector('[data-testid="detect-configured-hidden"]') !== null`),
				chromedp.Evaluate(`document.querySelectorAll('[data-testid="setup-detect"] .setup-detect__row').length`, &detectRows),
				chromedp.Evaluate(`[...document.querySelectorAll('[data-testid="setup-detect"] .setup-detect__name')].map((n) => n.textContent.trim())`, &detectNames),
				textContent(`[data-testid="detect-configured-hidden"]`, &detectNote),
			)
			assert.Equal(t, 1, pickRows, "the configured game is not offered by the picker")
			assert.Equal(t, []string{steam.UnknownName}, pickNames)
			assert.Contains(t, pickNote, "1 already-configured game is not listed")
			assert.Contains(t, pickNote, "Edit…")
			assert.Equal(t, 1, detectRows, "nor by the detect list")
			require.Len(t, detectNames, 1)
			assert.Contains(t, detectNames[0], steam.UnknownName)
			assert.Contains(t, detectNote, "Edit…")

			var label, expanded string
			var editors struct {
				Sources bool `json:"sources"`
				ModPath bool `json:"modPath"`
				Loader  bool `json:"loader"`
			}
			editSel := fmt.Sprintf(`[data-action="edit-game"][data-game=%q]`, f.Game.ID)
			f.runInBrowser(t,
				clickWhenSettled(editSel),
				pollUntil(`document.querySelector('[data-action="save-loader"]') !== null`),
				chromedp.Evaluate(`({
					sources: document.querySelector('[data-action="save-sources"]') !== null,
					modPath: document.querySelector('.setup-table__editor input') !== null,
					loader: document.querySelector('[data-action="save-loader"]') !== null,
				})`, &editors),
				textContent(editSel, &label),
				chromedp.AttributeValue(editSel, "aria-expanded", &expanded, nil, chromedp.ByQuery),
				clickWhenSettled(editSel),
				waitGone(`.setup-table__editor`),
			)
			assert.True(t, editors.Sources, "Edit… opens the sources editor")
			assert.True(t, editors.ModPath, "and the mod path editor")
			assert.True(t, editors.Loader, "and the loader editor")
			assert.Equal(t, "Close editors", label)
			assert.Equal(t, "true", expanded)
		})
	}
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
