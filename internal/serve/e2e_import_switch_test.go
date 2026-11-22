package serve_test

// #666 in the browser: importing a collection into a game whose active
// profile is another offers "Switch to this profile when the import
// finishes", checked by default, saying that the active profile's deployed
// mods come down. Confirmed, the job imports the collection, switches to
// its profile, and the page follows it: the library lists the
// collection's mods.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eSwitchPack is the collection (and the profile it imports as) of the
// switch scenario: Better Boots, which the source serves.
const e2eSwitchPack = "SwitchPack"

func TestE2E_ImportCollection_SwitchesToTheImportedProfile(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	doc := "lmm_collection: 1\nname: " + e2eSwitchPack + "\nmods:\n" +
		"  - {source_id: fake, mod_id: " + e2eSearchInstallModID + ", version: \"2.0\"}\n"
	path := filepath.Join(t.TempDir(), "switchpack.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o644))

	const switchBox = `.modal[data-kind="profile_import"] input[name="switch"]`
	var blockedBeforeInstall, checked, disabled bool
	var note string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openImportCollectionDialog(),
		chromedp.SetUploadFiles(`.modal[data-kind="import-collection"] input[type="file"]`, []string{path}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] [data-testid="import-switch"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('`+switchBox+`').disabled`, &blockedBeforeInstall),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('.modal[data-kind="profile_import"] label.form-check'))
				.find((l) => l.textContent.includes("Download and install"))?.querySelector("input").click();
		`, nil),
		settleEffects(),
		chromedp.Evaluate(`document.querySelector('`+switchBox+`').checked`, &checked),
		chromedp.Evaluate(`document.querySelector('`+switchBox+`').disabled`, &disabled),
		textContent(`[data-testid="import-switch-note"]`, &note),
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		awaitJobsSettled(),
		pollUntil(`location.pathname === "/g/`+f.Game.ID+`/`+e2eSwitchPack+`"`),
		pollUntil(`[...document.querySelectorAll(".mod-row")].some((r) => r.textContent.includes("Better Boots"))`),
	)

	assert.True(t, blockedBeforeInstall, "a switch would install the mods the import was not told to")
	assert.True(t, checked, "the switch is checked by default")
	assert.False(t, disabled)
	assert.Contains(t, note, "Switching takes down the mods default has deployed", "the plan says what the switch takes down")

	var rows []string
	f.runInBrowser(t, chromedp.Evaluate(`[...document.querySelectorAll(".mod-row")].map((r) => r.textContent)`, &rows))
	for _, row := range rows {
		assert.NotContains(t, row, "Alpha Mod", "the library is the imported profile's, not the old one's")
	}

	live, err := f.Svc.NewProfileManager().GetDefault(t.Context(), f.Game.ID)
	require.NoError(t, err)
	assert.Equal(t, e2eSwitchPack, live.Name, "the imported profile is the active one")
	row, err := f.Svc.GetInstalledMod(t.Context(), "fake", e2eSearchInstallModID, f.Game.ID, e2eSwitchPack)
	require.NoError(t, err)
	assert.True(t, row.Deployed, "and deployed")
	assert.Empty(t, f.BrowserErrors())
}
