package serve_test

// #544: the game editor sets and clears a CurseForge game's accepted game
// version types, and only a game that maps CurseForge shows the field.
// Asserted against games.yaml, the end state.

import (
	"context"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

func TestE2E_GameEditor_SetsAndClearsCurseForgeVersionTypes(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	field := editorSel(`input[name="curseforge-types"]`)

	var valheimHasField bool
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("valheim"),
		chromedp.Evaluate(`document.querySelector('[data-testid="game-editor"] input[name="curseforge-types"]') !== null`, &valheimHasField),
		clickWhenSettled(editorSel(`[data-action="cancel-edit-game"]`)),
		pollUntil(`!document.querySelector('[data-testid="game-editor"]')`),
		openEditor("drive"),
		// The id field once the version-type list has failed to load.
		chromedp.WaitVisible(field, chromedp.ByQuery),
		typeInto(field, "67408, 77522"),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
	)
	assert.False(t, valheimHasField, "a game without CurseForge has no CurseForge flavor to pick")

	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	require.NotNil(t, onDisk["drive"].CurseForge)
	assert.Equal(t, []int{67408, 77522}, onDisk["drive"].CurseForge.GameVersionTypes)

	f.runInBrowser(t,
		chromedp.Evaluate(countPUTs, nil),
		chromedp.Evaluate(`(() => {
			const i = document.querySelector('[data-testid="game-editor"] input[name="curseforge-types"]');
			i.value = "";
			i.dispatchEvent(new Event("input", { bubbles: true }));
		})()`, nil),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		pollUntil(`window.__puts === 1 &&
			document.querySelector('[data-testid="game-editor"] [data-action="save-game"]').textContent.trim() === "Save" &&
			document.querySelector('[data-testid="game-editor"] [data-testid="game-saved"]') !== null`),
	)
	onDisk, err = config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	assert.Nil(t, onDisk["drive"].CurseForge, "a blank field clears the setting")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// A value that is not an id is refused on the field, and nothing is written.
func TestE2E_GameEditor_ACurseForgeTypoMarksTheField(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	field := editorSel(`input[name="curseforge-types"]`)

	var invalid string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("drive"),
		chromedp.WaitVisible(field, chromedp.ByQuery),
		typeInto(field, "classic"),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-field-error"]`), chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-testid="game-editor"] input[name="curseforge-types"]').getAttribute("aria-invalid") ?? ""`, &invalid),
	)
	assert.Equal(t, "true", invalid)

	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	assert.Nil(t, onDisk["drive"].CurseForge)
	// The fixture's curseforge publishes no version types (a 404), which
	// is what leaves the id field in place (#563).
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/games/drive", "/api/v1/sources/curseforge/version-types")
}

// e2eVersionTypesSource is a curseforge that lists its game version types
// (#563), or fails to with listErr.
type e2eVersionTypesSource struct {
	*fakeSource
	listErr error
}

func (s *e2eVersionTypesSource) ListGameVersionTypes(context.Context, string) ([]source.GameVersionType, error) {
	return []source.GameVersionType{
		{ID: 517, Name: "WoW Retail", Slug: "game"},
		{ID: 67408, Name: "WoW Classic", Slug: "wow-classic"},
		{ID: 79434, Name: "WoW Mists of Pandaria Classic", Slug: "wow-mists-of-pandaria-classic"},
	}, s.listErr
}

var _ source.GameVersionTypeLister = (*e2eVersionTypesSource)(nil)

// typeBox is the picker's checkbox for one version type id.
func typeBox(id string) string {
	return editorSel(`[data-testid="curseforge-types-picker"] input[name="curseforge-type"][value="` + id + `"]`)
}

// #563: with CurseForge's list in, the editor is a checklist by name:
// ticking a name stores its id, the stored ones arrive ticked, and an id
// the list does not carry is still shown - by id - and kept.
func TestE2E_GameEditor_PicksCurseForgeVersionTypesByName(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	f.Svc.RegisterSource(&e2eVersionTypesSource{fakeSource: newFakeSource("curseforge")})
	game, err := f.Svc.GetGame("drive")
	require.NoError(t, err)
	game.CurseForge = &domain.GameCurseForge{GameVersionTypes: []int{12345}}
	require.NoError(t, f.Svc.SaveGame(t.Context(), game))

	var names string
	var classicTicked, unlistedTicked, hasIDField bool
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("drive"),
		chromedp.WaitVisible(typeBox("67408"), chromedp.ByQuery),
		chromedp.Evaluate(`[...document.querySelectorAll('[data-testid="curseforge-types-picker"] label')].map((l) => l.textContent.replace(/\s+/g, " ").trim()).join("|")`, &names),
		chromedp.Evaluate(`document.querySelector('[data-testid="game-editor"] input[name="curseforge-types"]') !== null`, &hasIDField),
		chromedp.Evaluate(`document.querySelector('`+typeBox("12345")+`').checked`, &unlistedTicked),
		clickWhenSettled(typeBox("67408")),
		chromedp.Evaluate(`document.querySelector('`+typeBox("67408")+`').checked`, &classicTicked),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
	)
	assert.Equal(t, "WoW Classic|WoW Mists of Pandaria Classic|WoW Retail|12345 (not in CurseForge's list)", names)
	assert.False(t, hasIDField, "with the list in, there is no id field")
	assert.True(t, unlistedTicked)
	assert.True(t, classicTicked)

	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	require.NotNil(t, onDisk["drive"].CurseForge)
	assert.Equal(t, []int{12345, 67408}, onDisk["drive"].CurseForge.GameVersionTypes, "ids, as before; the unlisted one is kept")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// #563: when the list cannot be had - here, no CurseForge key - the editor
// is the id field again, saying why and linking to the guide's section.
func TestE2E_GameEditor_CurseForgePickerFallsBackToTheIDField(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	f.Svc.RegisterSource(&e2eVersionTypesSource{fakeSource: newFakeSource("curseforge"), listErr: domain.ErrAuthRequired})
	field := editorSel(`input[name="curseforge-types"]`)

	var why, guide string
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("drive"),
		chromedp.WaitVisible(field, chromedp.ByQuery),
		chromedp.Text(editorSel(`[data-testid="curseforge-types-unavailable"]`), &why, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('[data-testid="curseforge-types-editor"] a').href`, &guide),
		typeInto(field, "67408"),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
	)
	assert.Contains(t, why, "Couldn't load CurseForge's list")
	assert.Contains(t, guide, "#pick-a-curseforge-game-flavor")

	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	require.NotNil(t, onDisk["drive"].CurseForge)
	assert.Equal(t, []int{67408}, onDisk["drive"].CurseForge.GameVersionTypes)
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/sources/curseforge/version-types")
}

// e2ePerGameVersionTypesSource lists different version types for each
// CurseForge game identifier, as CurseForge does.
type e2ePerGameVersionTypesSource struct {
	*fakeSource
}

func (s *e2ePerGameVersionTypesSource) ListGameVersionTypes(_ context.Context, sourceGameID string) ([]source.GameVersionType, error) {
	if sourceGameID == "432" {
		return []source.GameVersionType{{ID: 68441, Name: "Forge", Slug: "forge"}}, nil
	}
	return []source.GameVersionType{{ID: 67408, Name: "WoW Classic", Slug: "wow-classic"}}, nil
}

var _ source.GameVersionTypeLister = (*e2ePerGameVersionTypesSource)(nil)

// #563 review: a save that re-points the game's curseforge identifier
// re-reads the list for the game it now names, so a tick saves an id of
// THAT game rather than one from the list the panel opened with.
func TestE2E_GameEditor_CurseForgePickerFollowsTheSavedIdentifier(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	f.Svc.RegisterSource(&e2ePerGameVersionTypesSource{fakeSource: newFakeSource("curseforge")})

	var stale bool
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("drive"),
		chromedp.WaitVisible(typeBox("67408"), chromedp.ByQuery),
		typeInto(editorSel(`input[name="identifier-curseforge"]`), "432"),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
		chromedp.WaitVisible(typeBox("68441"), chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('`+typeBox("67408")+`') !== null`, &stale),
		clickWhenSettled(typeBox("68441")),
		clickWhenSettled(editorSel(`[data-action="save-game"]`)),
		chromedp.WaitVisible(editorSel(`[data-testid="game-saved"]`), chromedp.ByQuery),
	)
	assert.False(t, stale, "the old CurseForge game's list is gone")

	onDisk, err := config.LoadGames(f.Svc.ConfigDir())
	require.NoError(t, err)
	assert.Equal(t, "432", onDisk["drive"].SourceIDs["curseforge"])
	require.NotNil(t, onDisk["drive"].CurseForge)
	assert.Equal(t, []int{68441}, onDisk["drive"].CurseForge.GameVersionTypes)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// #563 review: unticking a stored type and ticking it again is no change,
// so there is nothing to save.
func TestE2E_GameEditor_CurseForgeRetickIsNotAChange(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	f.Svc.RegisterSource(&e2eVersionTypesSource{fakeSource: newFakeSource("curseforge")})
	game, err := f.Svc.GetGame("drive")
	require.NoError(t, err)
	game.CurseForge = &domain.GameCurseForge{GameVersionTypes: []int{67408, 517}}
	require.NoError(t, f.Svc.SaveGame(t.Context(), game))

	var saveDisabled bool
	f.runInBrowser(t,
		chromedp.EmulateViewport(1280, 900),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("drive"),
		chromedp.WaitVisible(typeBox("67408"), chromedp.ByQuery),
		clickWhenSettled(typeBox("67408")),
		clickWhenSettled(typeBox("67408")),
		pollUntil(`document.querySelector('`+typeBox("67408")+`').checked`),
		chromedp.Evaluate(`document.querySelector('[data-testid="game-editor"] [data-action="save-game"]').disabled`, &saveDisabled),
	)
	assert.True(t, saveDisabled, "the same set in another order is not dirty")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// e2eWoWVersionTypesSource lists World of Warcraft's real version types,
// as CurseForge answered on 2026-10-28: enough of them, with long enough
// names, to show how the checklist lays out.
type e2eWoWVersionTypesSource struct{ *fakeSource }

func (s *e2eWoWVersionTypesSource) ListGameVersionTypes(context.Context, string) ([]source.GameVersionType, error) {
	return []source.GameVersionType{
		{ID: 79434, Name: "WoW Mists of Pandaria Classic", Slug: "wow-mists-of-pandaria-classic"},
		{ID: 67408, Name: "WoW Classic", Slug: "wow-classic"},
		{ID: 517, Name: "WoW Retail", Slug: "game"},
		{ID: 77522, Name: "WoW Cataclysm Classic", Slug: "wow-cataclysm-classic"},
		{ID: 73713, Name: "WoW Wrath of the Lich King Classic", Slug: "wow-wrath-classic"},
		{ID: 81212, Name: "WoW Wrath Titan Reforged", Slug: "wow-wrath-titan"},
		{ID: 73246, Name: "WoW Burning Crusade Classic", Slug: "wow-burning-crusade-classic"},
		{ID: 88568, Name: "WoW Forever", Slug: "wow-forever"},
	}, nil
}

var _ source.GameVersionTypeLister = (*e2eWoWVersionTypesSource)(nil)

// #563 follow-up: the checklist reads as a list, however wide the editor.
// It names each type and nothing else (a slug is the CLI's spelling), it
// never spreads past three columns, and each checkbox sits on the first
// line of its label rather than floating between two.
func TestE2E_GameEditor_CurseForgePickerReadsAsAList(t *testing.T) {
	f := newE2EFixtureWithSetupGames(t)
	f.Svc.RegisterSource(&e2eWoWVersionTypesSource{fakeSource: newFakeSource("curseforge")})

	var layout struct {
		Labels  []string `json:"labels"`
		Columns int      `json:"columns"`
		Floats  []string `json:"floats"`
	}
	f.runInBrowser(t,
		chromedp.EmulateViewport(1920, 1000),
		chromedp.Navigate(f.SetupPath("games")),
		setupGamesReady(),
		openEditor("drive"),
		chromedp.WaitVisible(typeBox("67408"), chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const labels = [...document.querySelectorAll('[data-testid="curseforge-types-picker"] label')];
			const floats = [];
			for (const l of labels) {
				const box = l.querySelector('input').getBoundingClientRect();
				const line = parseFloat(getComputedStyle(l).lineHeight);
				const middle = box.top + box.height / 2 - l.getBoundingClientRect().top;
				if (middle > line * 0.75) floats.push(l.textContent.trim());
			}
			return {
				labels: labels.map((l) => l.textContent.replace(/\s+/g, " ").trim()),
				columns: new Set(labels.map((l) => Math.round(l.getBoundingClientRect().left))).size,
				floats,
			};
		})()`, &layout),
	)
	assert.Equal(t, []string{
		"WoW Burning Crusade Classic", "WoW Cataclysm Classic", "WoW Classic", "WoW Forever",
		"WoW Mists of Pandaria Classic", "WoW Retail", "WoW Wrath of the Lich King Classic", "WoW Wrath Titan Reforged",
	}, layout.Labels, "names only, sorted")
	assert.LessOrEqual(t, layout.Columns, 3, "the checklist spreads no wider than three columns")
	assert.Empty(t, layout.Floats, "every checkbox sits on its label's first line")
}
