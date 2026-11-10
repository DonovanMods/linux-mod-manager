package serve_test

// The browser scenarios for the library's "Group by category" view and the
// slide-over's category editor (issue 592). Browser tests because what is
// claimed is what the user SEES: group headings in the order the CLI
// prints them, a collapsed group's rows gone from the table, a preference
// that outlives a reload (localStorage, which only a browser has), and a row
// that moves to its new group once its category is saved.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newE2EFixtureWithCategories seeds three mods, each listed in the default
// profile: Zebra Mod's source names its category "Weapons", Middle Mod
// carries the user's own "armour", and Alpha Mod has no category at all.
// Load order is Zebra, Alpha, Middle - so a grouped table that kept load
// order across groups would be visibly wrong.
func newE2EFixtureWithCategories(t *testing.T) e2eFixture {
	t.Helper()
	src := newFakeSource("fake")
	mods := []domain.Mod{
		{ID: "z", SourceID: "fake", Name: "Zebra Mod", Version: "1.0", CategoryName: "Weapons"},
		{ID: "a", SourceID: "fake", Name: "Alpha Mod", Version: "1.0"},
		{ID: "m", SourceID: "fake", Name: "Middle Mod", Version: "1.0"},
	}
	for _, mod := range mods {
		src.addMod(fakeSourceMod{Mod: mod})
	}
	f := newE2EFixtureFromSource(t, src)
	pm := f.Svc.NewProfileManager()
	for _, mod := range mods {
		mod.GameID = f.Game.ID
		seedInstalledMod(t, f.Svc, f.Game, mod, true, map[string][]byte{mod.ID + ".pak": []byte(mod.ID)})
		require.NoError(t, pm.AddMod(t.Context(), f.Game.ID, f.Profile,
			domain.ModReference{SourceID: "fake", ModID: mod.ID, Version: "1.0"}))
	}
	require.NoError(t, pm.SetModCategory(t.Context(), f.Game.ID, f.Profile, "fake", "m", "armour"))
	return f
}

// e2eLibraryGroups is what libraryGroupsJS reads back: each group's
// heading, whether it is expanded, and the mod keys drawn under it.
type e2eLibraryGroup struct {
	Name     string   `json:"name"`
	Expanded bool     `json:"expanded"`
	Mods     []string `json:"mods"`
}

const libraryGroupsJS = `[...document.querySelectorAll(".library__group")].map((g) => {
	const toggle = g.querySelector('[data-action="toggle-group"]');
	return {
		name: toggle.querySelector(".library__group-name").textContent.trim(),
		expanded: toggle.getAttribute("aria-expanded") === "true",
		mods: [...g.querySelectorAll(".mod-row")].map((r) => r.dataset.mod),
	};
})`

const groupToggleSel = `input[data-action="group-by-category"]`

// TestE2E_LibraryGroupsByCategory_OrdersCollapsesAndRemembers is issue 592's
// grouped view: headings sorted case-insensitively with Uncategorized last,
// each group's mods under it, a collapsed group's rows gone (and the
// library's count unchanged), and both the grouping and the collapse still
// in force after a reload.
func TestE2E_LibraryGroupsByCategory_OrdersCollapsesAndRemembers(t *testing.T) {
	f := newE2EFixtureWithCategories(t)

	var flat int
	var groups, collapsed, reloaded []e2eLibraryGroup
	var heading string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		pollUntil(`document.querySelectorAll(".mod-row").length === 3`),
		chromedp.Evaluate(`document.querySelectorAll(".library__group").length`, &flat),
		chromedp.Click(groupToggleSel, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".library__group").length === 3`),
		chromedp.Evaluate(libraryGroupsJS, &groups),
		chromedp.Click(`.library__group[data-group="Weapons"] [data-action="toggle-group"]`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		chromedp.Evaluate(libraryGroupsJS, &collapsed),
		textContent(".library__toolbar .section-header", &heading),
		chromedp.Reload(),
		pollUntil(`document.querySelectorAll(".library__group").length === 3`),
		settleEffects(),
		chromedp.Evaluate(libraryGroupsJS, &reloaded),
	)

	assert.Zero(t, flat, "the library starts ungrouped")
	assert.Equal(t, []e2eLibraryGroup{
		{Name: "armour", Expanded: true, Mods: []string{"fake:m"}},
		{Name: "Weapons", Expanded: true, Mods: []string{"fake:z"}},
		{Name: "Uncategorized", Expanded: true, Mods: []string{"fake:a"}},
	}, groups, "case-insensitive name order, the uncategorised group last")

	require.Len(t, collapsed, 3, "a collapsed group keeps its heading")
	assert.False(t, collapsed[1].Expanded)
	assert.Empty(t, collapsed[1].Mods, "a collapsed group's rows are not drawn")
	assert.Contains(t, heading, "(3)", "the library still counts the collapsed group's mods")

	assert.Equal(t, collapsed, reloaded, "grouping and the collapse are remembered across a reload")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SetCategory_FromTheRowMenuMovesTheRow is issue 592's editor: the
// row menu's "Set category…" opens the slide-over with the category field
// focused, a saved category lands in the profile and moves the row to its
// group, and Clear sends it back to Uncategorized.
func TestE2E_SetCategory_FromTheRowMenuMovesTheRow(t *testing.T) {
	f := newE2EFixtureWithCategories(t)
	profilePath := filepath.Join(f.Svc.ConfigDir(), "games", f.Game.ID, "profiles", f.Profile+".yaml")

	var focused bool
	var origin string
	var moved, cleared []e2eLibraryGroup
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		pollUntil(`document.querySelectorAll(".mod-row").length === 3`),
		chromedp.Click(groupToggleSel, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".library__group").length === 3`),
		chromedp.Click(`tr[data-mod="fake:a"] [data-action="row-menu"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.row-menu [data-action="set-category"]`, chromedp.ByQuery),
		chromedp.Click(`.row-menu [data-action="set-category"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="category-input"]`, chromedp.ByQuery),
		pollUntil(`document.activeElement?.dataset.testid === "category-input"`),
		chromedp.Evaluate(`document.activeElement?.dataset.testid === "category-input"`, &focused),
		chromedp.SendKeys(`[data-testid="category-input"]`, "Weapons", chromedp.ByQuery),
		chromedp.Click(`[data-action="save-category"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.library__group[data-group="Weapons"] tr[data-mod="fake:a"]') !== null`),
		pollUntil(`document.querySelector('[data-testid="category-origin"]')?.textContent.includes("Custom")`),
		textContent(`[data-testid="category-origin"]`, &origin),
		chromedp.Evaluate(libraryGroupsJS, &moved),
		chromedp.Click(`[data-action="clear-category"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.library__group[data-group=""] tr[data-mod="fake:a"]') !== null`),
		chromedp.Evaluate(libraryGroupsJS, &cleared),
	)

	assert.True(t, focused, "Set category… opens the slide-over on the category field")
	assert.Contains(t, origin, "Custom category")
	assert.Equal(t, []e2eLibraryGroup{
		{Name: "armour", Expanded: true, Mods: []string{"fake:m"}},
		{Name: "Weapons", Expanded: true, Mods: []string{"fake:z", "fake:a"}},
	}, moved, "the row moves to its new group, in load order within it, and the emptied group goes")

	require.Len(t, cleared, 3)
	assert.Equal(t, "Uncategorized", cleared[2].Name)
	assert.Equal(t, []string{"fake:a"}, cleared[2].Mods, "Clear sends the mod back to Uncategorized")

	raw, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "category: armour", "the other mod's category is untouched")
	assert.NotContains(t, string(raw), "category: Weapons", "the cleared category is gone from the profile")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_SetCategory_SourceCategoryIsNotOneKeypressFromPinned: a mod whose
// category is the SITE's opens the editor empty, with the site's name as the
// placeholder and Save disabled - so the Enter that "Set category…" puts
// right under the user's finger cannot pin the site's name as their own
// (it would stop following the site's recategorisation). Typing the name is
// still a deliberate way to pin it.
func TestE2E_SetCategory_SourceCategoryIsNotOneKeypressFromPinned(t *testing.T) {
	f := newE2EFixtureWithCategories(t)
	profilePath := filepath.Join(f.Svc.ConfigDir(), "games", f.Game.ID, "profiles", f.Profile+".yaml")

	var value, placeholder string
	var saveDisabled bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		pollUntil(`document.querySelectorAll(".mod-row").length === 3`),
		chromedp.Click(`tr[data-mod="fake:z"] [data-action="row-menu"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.row-menu [data-action="set-category"]`, chromedp.ByQuery),
		chromedp.Click(`.row-menu [data-action="set-category"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`[data-testid="category-input"]`, chromedp.ByQuery),
		pollUntil(`document.activeElement?.dataset.testid === "category-input"`),
		chromedp.Evaluate(`document.querySelector('[data-testid="category-input"]').value`, &value),
		chromedp.Evaluate(`document.querySelector('[data-testid="category-input"]').placeholder`, &placeholder),
		chromedp.Evaluate(`document.querySelector('[data-action="save-category"]').disabled`, &saveDisabled),
		chromedp.SendKeys(`[data-testid="category-input"]`, "\r", chromedp.ByQuery),
	)

	assert.Empty(t, value, "the site's category is not prefilled as an edit")
	assert.Equal(t, "Weapons", placeholder, "the site's category shows as the placeholder")
	assert.True(t, saveDisabled, "nothing to save until the user types")

	raw, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "category: Weapons", "Enter on the untouched field pins nothing")
	assert.Empty(t, f.BrowserErrors())
}
