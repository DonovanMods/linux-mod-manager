package serve_test

// #527: a Games row's pencil opens ONE editor panel holding every field the
// row edits, with one Save - one request, which core checks as a whole and
// writes once or not at all - and one Cancel. #528: the install path is one
// of those fields, and a move of the game folder saves end to end.
//
// Every scenario asserts the END STATE on disk (games.yaml, through the
// Service and through config.LoadGames), not just the DOM.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// editorSel is a selector inside the open game editor.
func editorSel(sel string) string { return `[data-testid="game-editor"] ` + sel }

// typeInto replaces an input's value the way a user does: select what is
// there and type over it, so the browser fires the input events the panel
// listens for.
func typeInto(sel, value string) chromedp.Action {
	return chromedp.Tasks{
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).select()`, sel), nil),
		chromedp.SendKeys(sel, value, chromedp.ByQuery),
	}
}

// countPUTs wraps the page's fetch so a scenario can say how many PUTs the
// panel sent (window.__puts).
const countPUTs = `(() => {
	window.__puts = 0;
	const real = window.fetch;
	window.fetch = (url, init) => {
		if (init?.method === "PUT") window.__puts++;
		return real(url, init);
	};
})()`

// openEditor opens game's editor from its row's pencil.
func openEditor(game string) chromedp.Action {
	return chromedp.Tasks{
		clickWhenSettled(pencilSel("edit-game", game)),
		chromedp.WaitVisible(fmt.Sprintf(`[data-testid="game-editor"][data-game=%q]`, game), chromedp.ByQuery),
		// The panel takes focus on mount; a test that types before it has
		// would see the first key land and the rest go to wherever focus
		// moved (#532). Nothing may type into the panel until it holds focus.
		pollUntil(`!!document.activeElement?.closest('[data-testid="game-editor"]')`),
	}
}

// TestE2E_GameEditor_FocusIsPlacedWhenThePanelMounts (#532): the editor's
// focus lands in the same commit that mounts it, not after paint. A plain
// effect runs after paint, so a user (or a test) that clicked into another
// field and typed straight away had focus pulled back to Name after the first
// keystroke - the rest of the text went nowhere.
//
// A MutationObserver callback is a microtask that runs once the render task
// that inserted the panel has finished, which is before any after-paint
// effect and after every layout effect - so it samples focus at exactly the
// boundary that tells the two apart, deterministically.
func TestE2E_GameEditor_FocusIsPlacedWhenThePanelMounts(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)

	var atMount string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		chromedp.Evaluate(`(() => {
			window.__focusAtMount = null;
			new MutationObserver((_, obs) => {
				if (!document.querySelector('[data-testid="game-editor"]')) return;
				window.__focusAtMount = document.activeElement?.name ?? "";
				obs.disconnect();
			}).observe(document.body, { childList: true, subtree: true });
		})()`, nil),
		openEditor("valheim"),
		chromedp.Evaluate(`window.__focusAtMount`, &atMount),
	)
	assert.Equal(t, "game-name", atMount, "the Name field holds focus by the time the panel is on screen")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_GameEditor_SavesSeveralFieldsInOneSave: the name, the install
// path, a source and the loader's version, changed together, reach
// games.yaml from ONE Save - one PUT - and the mod path inside the install
// path moves with it.
func TestE2E_GameEditor_SavesSeveralFieldsInOneSave(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	moved := t.TempDir()

	var puts int
	var row string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		chromedp.Evaluate(countPUTs, nil),
		openEditor("valheim"),
		typeInto(editorSel(`input[name="game-name"]`), "Valheim (modded)"),
		typeInto(editorSel(`input[name="install-path"]`), moved),
		clickWhenSettled(editorSel(`input[name="source-nexusmods"]`)),
		typeInto(editorSel(`input[name="identifier-nexusmods"]`), "valheim"),
		typeInto(editorSel(`input[name="loader-version"]`), "5.4.23.5"),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
		chromedp.Evaluate(`window.__puts`, &puts),
		textContent(`[data-testid="setup-games"] tbody tr:has([data-game="valheim"]) .setup-table__name`, &row),
	)
	assert.Equal(t, 1, puts, "one Save is one request")
	assert.Contains(t, row, "Valheim (modded)", "the row re-reads")

	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	got := onDisk["valheim"]
	require.NotNil(t, got)
	assert.Equal(t, "Valheim (modded)", got.Name)
	assert.Equal(t, moved, got.InstallPath)
	assert.Equal(t, filepath.Join(moved, "Data", "Mods"), got.ModPath, "the mod path inside the install path moved with it")
	assert.Equal(t, map[string]string{"steamworkshop": "valheim", "nexusmods": "valheim"}, got.SourceIDs)
	require.NotNil(t, got.Loader)
	assert.Equal(t, "bepinex", got.Loader.Kind)
	assert.Equal(t, "5.4.23.5", got.Loader.Version)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_GameEditor_AFieldErrorSavesNothing: a refusal of one field - an
// install path that does not exist - marks THAT field, keeps the panel and
// its draft, and writes none of the other changes either.
func TestE2E_GameEditor_AFieldErrorSavesNothing(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	before, err := os.ReadFile(filepath.Join(f.Svc.ConfigDir(), "games.yaml"))
	require.NoError(t, err)

	var state struct {
		InstallInvalid string `json:"installInvalid"`
		NameInvalid    string `json:"nameInvalid"`
		FieldError     string `json:"fieldError"`
		Status         string `json:"status"`
		Name           string `json:"name"`
		Saved          bool   `json:"saved"`
	}
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("valheim"),
		typeInto(editorSel(`input[name="game-name"]`), "Renamed"),
		typeInto(editorSel(`input[name="install-path"]`), filepath.Join(t.TempDir(), "nope")),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-field-error"]`), chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const p = document.querySelector('[data-testid="game-editor"]');
			const install = p.querySelector('input[name="install-path"]');
			return {
				installInvalid: install.getAttribute("aria-invalid") ?? "",
				nameInvalid: p.querySelector('input[name="game-name"]').getAttribute("aria-invalid") ?? "",
				fieldError: install.closest(".game-editor__field").querySelector('[data-testid="game-field-error"]')?.textContent.trim() ?? "",
				status: p.querySelector(".game-editor__status").textContent.trim(),
				name: p.querySelector('input[name="game-name"]').value,
				saved: p.querySelector('[data-testid="game-saved"]') !== null,
			};
		})()`, &state),
	)
	assert.Equal(t, "true", state.InstallInvalid, "the install path is marked")
	assert.Empty(t, state.NameInvalid, "and only the install path")
	assert.Contains(t, state.FieldError, "does not exist", "core's reason, under the field")
	assert.Contains(t, state.Status, "Nothing was saved")
	assert.Equal(t, "Renamed", state.Name, "the draft is kept, to correct and save again")
	assert.False(t, state.Saved)

	after, err := os.ReadFile(filepath.Join(f.Svc.ConfigDir(), "games.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "games.yaml is untouched - the valid name was not written either")
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/games/valheim")
}

// TestE2E_GameEditor_CancelDiscards: Cancel closes the panel without a
// request, hands focus back to the pencil, and the next opening starts from
// the game as it is.
func TestE2E_GameEditor_CancelDiscards(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	before, err := os.ReadFile(filepath.Join(f.Svc.ConfigDir(), "games.yaml"))
	require.NoError(t, err)

	var puts int
	var label, reopenedName string
	var focused, reopenedSource bool
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		chromedp.Evaluate(countPUTs, nil),
		openEditor("valheim"),
		typeInto(editorSel(`input[name="game-name"]`), "Discard me"),
		clickWhenSettled(editorSel(`input[name="source-steamworkshop"]`)),
		textContent(editorSel(`[data-action="cancel-edit-game"]`), &label),
		clickWhenSettled(editorSel(`[data-action="cancel-edit-game"]`)),
		waitGone(`[data-testid="game-editor"]`),
		chromedp.Evaluate(fmt.Sprintf(`document.activeElement === document.querySelector(%q)`, pencilSel("edit-game", "valheim")), &focused),
		chromedp.Evaluate(`window.__puts`, &puts),
		openEditor("valheim"),
		chromedp.Value(editorSel(`input[name="game-name"]`), &reopenedName, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-testid="game-editor"] input[name="source-steamworkshop"]').checked`, &reopenedSource),
	)
	assert.Equal(t, "Cancel", label, "with changes made, the panel's second action is Cancel")
	assert.Zero(t, puts, "Cancel sends nothing")
	assert.True(t, focused, "focus returns to the row's pencil")
	assert.Equal(t, "Valheim", reopenedName, "the draft was discarded")
	assert.True(t, reopenedSource)

	after, err := os.ReadFile(filepath.Join(f.Svc.ConfigDir(), "games.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_GameEditor_MovesAnInstallPathEndToEnd (#528): the game folder,
// with mods deployed in it, moved; the panel's install path follows it, and
// lmm records the deployed files under the moved mod path.
func TestE2E_GameEditor_MovesAnInstallPathEndToEnd(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)
	root := t.TempDir()
	f.Game.InstallPath = filepath.Join(root, "Library A", "Game")
	f.Game.ModPath = filepath.Join(f.Game.InstallPath, "Data")
	require.NoError(t, os.MkdirAll(f.Game.ModPath, 0o755))
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	_, err := f.Svc.DeployProfile(t.Context(), f.Game, f.Profile, core.DeployOptions{}, nil)
	require.NoError(t, err)
	moved := filepath.Join(root, "Library B", "Game")
	require.NoError(t, os.MkdirAll(filepath.Dir(moved), 0o755))
	require.NoError(t, os.Rename(f.Game.InstallPath, moved))

	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
		pollUntil(fmt.Sprintf(`document.querySelector(%q)?.disabled === false`, pencilSel("edit-game", f.Game.ID))),
		openEditor(f.Game.ID),
		typeInto(editorSel(`input[name="install-path"]`), moved),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="setup-games"] td [data-testid="mod-path-error"]') === null`),
	)

	game, err := f.Svc.GetGame(f.Game.ID)
	require.NoError(t, err)
	assert.Equal(t, moved, game.InstallPath)
	assert.Equal(t, filepath.Join(moved, "Data"), game.ModPath)
	problem, err := f.Svc.ModPathProblem(context.Background(), game)
	require.NoError(t, err)
	assert.Nil(t, problem, "the deployed files are recorded where they now are")
	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	assert.Equal(t, moved, onDisk[f.Game.ID].InstallPath)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_GameEditor_HoldsInEveryFace is #521's stress pass over the panel:
// at 1280px, in every face, its Save and Cancel are centred one-line labels,
// every field label is one line, every input and the loader panel stay
// inside the panel, the panel stays inside the table, and the page does not
// scroll sideways.
func TestE2E_GameEditor_HoldsInEveryFace(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		var got map[string][]labelBox
		var layout struct {
			LabelLines []int     `json:"labelLines"`
			Overruns   []string  `json:"overruns"`
			Panel      []float64 `json:"panel"`
			Table      []float64 `json:"table"`
			Overflows  bool      `json:"overflows"`
		}
		f.runInBrowser(t,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(f.SetupPath("games")),
			setupGamesReady(),
			faceInEffect(face),
			openEditor("valheim"),
			chromedp.WaitVisible(editorSel(`[data-testid="loader-panel"]`), chromedp.ByQuery),
			measureLabels(`{"game editor": "[data-testid=\"game-editor\"]"}`, &got),
			chromedp.Evaluate(`(() => { `+textLinesJS+`
				const p = document.querySelector('[data-testid="game-editor"]');
				const box = p.getBoundingClientRect();
				const t = p.closest("table").getBoundingClientRect();
				const overruns = [...p.querySelectorAll("input, select, .loader-panel, .sources-map__row")]
					.filter((el) => el.getBoundingClientRect().width > 0)
					.filter((el) => { const r = el.getBoundingClientRect(); return r.left < box.left - 0.5 || r.right > box.right + 0.5; })
					.map((el) => el.name || el.className);
				return {
					labelLines: [...p.querySelectorAll("label.plan__control, legend.plan__control")].map((l) => textLines(l)),
					overruns,
					panel: [box.left, box.right],
					table: [t.left, t.right],
					overflows: document.documentElement.scrollWidth > window.innerWidth,
				};
			})()`, &layout),
		)
		assertLabelsCentred(t, "game editor", got["game editor"])
		require.NotEmpty(t, layout.LabelLines)
		for i, n := range layout.LabelLines {
			assert.Equal(t, 1, n, "field label %d is laid out on one line", i)
		}
		assert.Empty(t, layout.Overruns, "every control stays inside the panel")
		assert.GreaterOrEqual(t, layout.Panel[0], layout.Table[0]-0.5, "the panel is inside the table")
		assert.LessOrEqual(t, layout.Panel[1], layout.Table[1]+0.5, "the panel is inside the table")
		assert.False(t, layout.Overflows, "the page does not scroll sideways")
	})
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
