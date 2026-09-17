package serve_test

// #460, the web half of #427: a game whose mod_path lmm deployed into has
// gone is flagged everywhere the game is shown, with the action that
// repairs it; the Games table edits the mod path; and a move refused for
// files still deployed under the old directory reads as the ordered steps
// that clear it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// newE2EFixtureWithAMissingModPath is newE2EFixtureWithDeployableMods with
// the game at the detect fixture's Steam install path, both mods deployed
// into <install>/Data, and that directory then removed - the state
// ModPathProblem flags: files recorded under a mod_path that is gone.
func newE2EFixtureWithAMissingModPath(t *testing.T) e2eFixture {
	t.Helper()
	f := newE2EFixtureWithDeployableMods(t)
	steam := writeE2ESteamDetectFixture(t, f.Svc.ConfigDir())
	f.Game.InstallPath = filepath.Join(steam.SteamRoot, "steamapps", "common", "E2EDetectGame")
	f.Game.ModPath = filepath.Join(f.Game.InstallPath, "Data")
	require.NoError(t, os.MkdirAll(f.Game.ModPath, 0o755))
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	_, err := f.Svc.DeployProfile(t.Context(), f.Game, f.Profile, core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(f.Game.ModPath))
	return f
}

// TestE2E_MissingModPath_FlaggedWithTheRepairEverywhere walks Mission
// Control (the warning and the Health row) to Setup > Games through the
// warning's own action, which opens that game's mod-path editor prefilled.
func TestE2E_MissingModPath_FlaggedWithTheRepairEverywhere(t *testing.T) {
	f := newE2EFixtureWithAMissingModPath(t)

	var banner, healthRow, value, rowWarning string
	var focused bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.mission-control__body > [data-testid="mod-path-error"]') !== null`),
		textContent(`.mission-control__body > [data-testid="mod-path-error"]`, &banner),
		pollUntil(`document.querySelector('.card--health [data-status="mod_path_missing"]') !== null`),
		textContent(`.card--health [data-status="mod_path_missing"]`, &healthRow),
		clickWhenSettled(`.mission-control__body > [data-testid="mod-path-error"] [data-action="set-mod-path"]`),
		pollUntil(`document.querySelector('[data-testid="mod-path-editor"] input[name="mod-path"]')?.value !== undefined &&
			document.querySelector('[data-testid="mod-path-editor"] input[name="mod-path"]').value !== ""`),
		chromedp.Value(`[data-testid="mod-path-editor"] input[name="mod-path"]`, &value, chromedp.ByQuery),
		textContent(`[data-testid="setup-games"] td [data-testid="mod-path-error"]`, &rowWarning),
		chromedp.Evaluate(`document.activeElement === document.querySelector('[data-testid="mod-path-editor"] input[name="mod-path"]')`, &focused),
	)
	assert.Contains(t, banner, f.Game.ModPath+" does not exist, but lmm recorded 2 deployed file(s) under it")
	assert.Contains(t, banner, "Set mod path…")
	assert.Contains(t, healthRow, "Mod path")
	assert.NotContains(t, healthRow, "`", "the note's commands are set as code, not wrapped in backticks")
	assert.Contains(t, healthRow, "Set mod path…", "the row's action is the repair, not a dead 'Not fixable'")
	assert.Equal(t, f.Game.ModPath, value, "with no suggestion the editor starts from the current value")
	assert.Contains(t, rowWarning, "does not exist")
	assert.True(t, focused, "opening the editor from the deep link moves keyboard focus into its input")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ModPathEditor_RowActionRefocusesWhenAlreadyOpen is review F4: a
// row's own "Set mod path…" warning action used to no-op when that row's
// editor was already open (setupgames.js gated the call on
// `editingModPath?.id !== g.id`), leaving a keyboard user with no feedback
// at all for the click. The action now moves focus into the input either
// way.
func TestE2E_ModPathEditor_RowActionRefocusesWhenAlreadyOpen(t *testing.T) {
	f := newE2EFixtureWithAMissingModPath(t)

	var reopenedFocus bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup"),
		chromedp.WaitVisible(`.setup-page`, chromedp.ByQuery),
		chromedp.Click(`.setup-nav__tab[data-section="games"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-testid="setup-games"] [data-action="edit-mod-path"]`),
		chromedp.WaitVisible(`[data-testid="mod-path-editor"] input[name="mod-path"]`, chromedp.ByQuery),
		// Move focus elsewhere so the row action's effect is unambiguous.
		chromedp.Evaluate(`document.querySelector('[data-testid="setup-games"] [data-action="edit-mod-path"]').focus()`, nil),
		clickWhenSettled(`[data-testid="setup-games"] td [data-testid="mod-path-error"] [data-action="set-mod-path"]`),
		chromedp.Evaluate(`document.activeElement === document.querySelector('[data-testid="mod-path-editor"] input[name="mod-path"]')`, &reopenedFocus),
	)
	assert.True(t, reopenedFocus, "the row warning's action refocuses the already-open editor's input rather than doing nothing")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ModPathEditor_MarksARejectedValueAndOrdersTheRefusal: a file is
// a 400 marked on the input; a move with files still deployed is the 409,
// rendered as the steps in order - purge first, then the save, then the
// deploy - and games.yaml is untouched by both. Then the purge is done and
// the same save succeeds.
func TestE2E_ModPathEditor_MarksARejectedValueAndOrdersTheRefusal(t *testing.T) {
	f := newE2EFixtureWithAMissingModPath(t)
	file := filepath.Join(f.Game.InstallPath, "a-file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	setValue := func(v string) chromedp.Action {
		// Select what is there and type over it, as a user does.
		return chromedp.Tasks{
			chromedp.Evaluate(`document.querySelector('input[name="mod-path"]').select()`, nil),
			chromedp.SendKeys(`input[name="mod-path"]`, v, chromedp.ByQuery),
		}
	}
	var invalid, title string
	var ok bool
	var steps []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup?section=games"),
		chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
		chromedp.Click(`button[data-action="edit-mod-path"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="mod-path-editor"]`, chromedp.ByQuery),
		// The answer region exists before any answer does (issue 442).
		chromedp.Evaluate(`document.querySelector('.mod-path-editor__answer[role="status"]') !== null`, &ok),
		setValue(file),
		chromedp.Click(`button[data-action="save-mod-path"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="mod-path-field-error"]') !== null`),
		chromedp.AttributeValue(`input[name="mod-path"]`, "aria-invalid", &invalid, nil, chromedp.ByQuery),

		setValue(f.Game.InstallPath),
		chromedp.Click(`button[data-action="save-mod-path"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="mod-path-in-use"]') !== null`),
		chromedp.Evaluate(`[...document.querySelectorAll('.mod-path-in-use__step')].map(li => li.dataset.command)`, &steps),
		textContent(`.mod-path-in-use__title`, &title),
	)
	assert.Contains(t, title, "2 file(s) are deployed under "+f.Game.ModPath+", so")
	assert.True(t, ok)
	assert.Equal(t, "true", invalid)
	assert.Equal(t, []string{
		"lmm purge --game g1 --profile default",
		"lmm game edit g1 --mod-path " + f.Game.InstallPath,
		"lmm deploy --game g1",
	}, steps, "purge first, then the move, then the deploy")
	game, err := f.Svc.GetGame(f.Game.ID)
	require.NoError(t, err)
	assert.Equal(t, f.Game.ModPath, game.ModPath, "neither refusal wrote games.yaml")

	// The purge the first step names, then the same save.
	mods, err := f.Svc.GetInstalledMods(t.Context(), f.Game.ID, f.Profile)
	require.NoError(t, err)
	_, err = f.Svc.PurgeProfile(t.Context(), f.Game, f.Profile, mods, core.PurgeOptions{}, nil)
	require.NoError(t, err)
	f.runInBrowser(t,
		chromedp.Click(`button[data-action="save-mod-path"]`, chromedp.ByQuery),
		waitGone(`[data-testid="mod-path-editor"]`),
		pollUntil(`[...document.querySelectorAll('[data-testid="setup-games"] td.col--path .mono')].some(e => e.textContent.trim() === `+jsString(f.Game.InstallPath)+`)`),
	)
	game, err = f.Svc.GetGame(f.Game.ID)
	require.NoError(t, err)
	assert.Equal(t, f.Game.InstallPath, game.ModPath)
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/games/"+f.Game.ID)
}

// TestE2E_ModPathInUseSteps_FollowCoresOrder pins the whole ordering the
// refusal can carry - release another game's copy, apply, deploy, purge
// (the active profile too), move, deploy, apply after the move - against
// a synthetic document, since the state that produces every member at once
// takes three games to build.
func TestE2E_ModPathInUseSteps_FollowCoresOrder(t *testing.T) {
	f := newE2EFixture(t)
	var got []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { modPathInUseSteps, modPathInUseFor } = await import("/static/app/components/modpath.js");
			const d = modPathInUseFor({
				game_id: "g", mod_path: "/old", new_mod_path: "/new", deployed_files: 3,
				profiles: [{profile: "b", deployed_files: 3}], active_profile: "a",
				listed_unrecorded: 2, needs_apply: true, needs_deploy: true,
				release_first: [{game_id: "h", profile: "x"}], apply_after_move: true,
			});
			return modPathInUseSteps(d).map(s => s.command);
		})()`, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
	)
	assert.Equal(t, []string{
		"lmm purge --game h --profile x",
		"lmm profile apply a --game g",
		"lmm deploy --game g",
		"lmm purge --game g --profile a",
		"lmm purge --game g --profile b",
		"lmm game edit g --mod-path /new",
		"lmm deploy --game g",
		"lmm profile apply a --game g",
	}, got)

	var phase string
	f.runInBrowser(t, chromedp.Evaluate(`(async () => {
		const { humanizePhase } = await import("/static/app/progress.js");
		return humanizePhase("download_warning");
	})()`, &phase, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	assert.Equal(t, "Download warning", phase, "the download_warning phase reads as words in the job activity")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_DetectListing_MarksAGameThatNeedsRepair: the detect listing says
// "needs repair" for a configured game whose mod_path is missing, and the
// row stays unselectable - adding it again resets its profile.
func TestE2E_DetectListing_MarksAGameThatNeedsRepair(t *testing.T) {
	f := newE2EFixtureWithAMissingModPath(t)
	var badge, hint string
	var disabled bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()+"/setup?section=games"),
		chromedp.WaitVisible(`[data-testid="setup-games"]`, chromedp.ByQuery),
		chromedp.Click(`.setup-section__actions button`, chromedp.ByQuery),
		pollUntil(`document.querySelector('[data-testid="needs-repair"]') !== null`),
		textContent(`[data-testid="needs-repair"]`, &badge),
		textContent(`[data-testid="detect-needs-repair"]`, &hint),
		chromedp.Evaluate(`document.querySelector('[data-testid="needs-repair"]').closest('.setup-detect__row').querySelector('input[type="checkbox"]').disabled`, &disabled),
	)
	assert.Equal(t, "needs repair", strings.TrimSpace(badge))
	assert.Contains(t, hint, "Set mod path…")
	assert.True(t, disabled, "a configured game is never offered for re-selection")
	assert.Empty(t, f.BrowserErrors())
}

// jsString quotes s as a JavaScript string literal.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
