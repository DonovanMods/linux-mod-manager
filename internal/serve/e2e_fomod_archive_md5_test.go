package serve_test

// #609 in the browser: a collection whose recorded installer choices were
// made against another archive than the one downloaded is not replayed.
// The import comes back with the installer's question; the wizard says
// why, with both md5s, and has the collection's choices selected; finishing
// it imports the profile with them.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func TestE2E_FomodWizard_ACollectionForAnotherArchiveIsLeftToFinish(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod:      domain.Mod{ID: "wizard", SourceID: "fake", Name: "Wizard Mod", Version: "1.0"},
		files:    []domain.DownloadableFile{{ID: "w1", Name: "Main", FileName: "wizard.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512}},
		archives: map[string][]byte{"w1": e2eFomodArchive(t)},
	})
	recorded := strings.Repeat("0", 32)
	doc := "lmm_collection: 1\nname: Pinned Pack\nrequires: [fomod]\nmods:\n" +
		"  - {source_id: fake, mod_id: wizard, version: \"1.0\", file_ids: [w1], files: [{id: w1, md5: \"" + recorded + "\"}], " +
		"fomod: {steps: [{name: Size, groups: [{name: Texture, choices: [{name: 2K}]}]}]}}\n"
	importPath := filepath.Join(t.TempDir(), "pack.yaml")
	require.NoError(t, os.WriteFile(importPath, []byte(doc), 0o644))

	// #660: the import no longer stops to ask - the installer is left to
	// finish, its reason saying the archive is another (Part 2's Finish
	// collection asks it).
	var wizard bool
	var todos []e2eTodo
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		chromedp.Click(`.profile-picker__trigger`, chromedp.ByQuery),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll(".profile-picker__menu button"))
				.find((b) => b.textContent.includes("Manage profiles"))?.click();
		`, nil),
		chromedp.WaitVisible(`[data-testid="profiles-list"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.SetUploadFiles(`.profiles-import input[type="file"]`, []string{importPath}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] [data-testid="collection-installer-choices"]`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('.modal[data-kind="profile_import"] label.form-check'))
				.find((l) => l.textContent.includes("Download and install"))?.querySelector("input").click();
		`, nil),
		settleEffects(),
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		awaitJobsSettled(),
		quietWindow(),
		chromedp.Evaluate(`document.querySelector(`+"`"+fomodModal+"`"+`) !== null`, &wizard),
		fetchTodos(f.Game.ID, "Pinned Pack", &todos),
	)
	assert.False(t, wizard, "the import never stops to ask")
	require.Len(t, todos, 1)
	assert.Equal(t, "installer_questions", todos[0].Reason)
	assert.Equal(t, "archive_mismatch", todos[0].FomodReason)
	assert.Contains(t, todos[0].Remedy, "not the archive the curator recorded their choices against")
	assert.Empty(t, f.BrowserErrors())
}
