package serve_test

// #666 in the browser: importing a collection into a game whose active
// profile is another offers "Switch to this profile when the import
// finishes", checked by default, saying that the active profile's deployed
// mods come down. Confirmed, the job imports the collection, switches to
// its profile, and the page follows it: the library lists the
// collection's mods.

import (
	"context"
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
	var note, describedBy string
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
		chromedp.Evaluate(`document.getElementById(document.querySelector('`+switchBox+`').getAttribute("aria-describedby"))?.dataset.testid ?? ""`, &describedBy),
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		awaitJobsSettled(),
		pollUntil(`location.pathname === "/g/`+f.Game.ID+`/`+e2eSwitchPack+`"`),
		pollUntil(`[...document.querySelectorAll(".mod-row")].some((r) => r.textContent.includes("Better Boots"))`),
	)

	assert.True(t, blockedBeforeInstall, "a switch would install the mods the import was not told to")
	assert.True(t, checked, "the switch is checked by default")
	assert.False(t, disabled)
	assert.Contains(t, note, "Switching takes down the mods default has deployed", "the plan says what the switch takes down")
	assert.Equal(t, "import-switch-note", describedBy, "the checkbox is described by its note")

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

// writeSwitchPack writes the switch scenario's collection to a file.
func writeSwitchPack(t *testing.T) string {
	t.Helper()
	doc := "lmm_collection: 1\nname: " + e2eSwitchPack + "\nmods:\n" +
		"  - {source_id: fake, mod_id: " + e2eSearchInstallModID + ", version: \"2.0\"}\n"
	path := filepath.Join(t.TempDir(), "switchpack.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o644))
	return path
}

// TestE2E_ImportCollection_ASwitchWhileTheUserMovedOnOffersGoTo is the
// #666 review's case: a user who left the page they confirmed the import
// on - here for the search page, while the job waited to start - is not
// pulled back when the switch lands. A toast offers "Go to SwitchPack"
// instead, and that goes there.
func TestE2E_ImportCollection_ASwitchWhileTheUserMovedOnOffersGoTo(t *testing.T) {
	f, jobs, _ := newE2EFixtureWithSearchableModsAndGatedJobStart(t)
	path := writeSwitchPack(t)
	searchPath := "/g/" + f.Game.ID + "/default/search"

	var pathAfter string
	var toast e2eOutcomeToast
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openImportCollectionDialog(),
		chromedp.SetUploadFiles(`.modal[data-kind="import-collection"] input[type="file"]`, []string{path}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] [data-testid="import-switch"]`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('.modal[data-kind="profile_import"] label.form-check'))
				.find((l) => l.textContent.includes("Download and install"))?.querySelector("input").click();
		`, nil),
		settleEffects(),
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		jobs.awaitArrival("the import's job start"),
		chromedp.Evaluate(`import("/static/app/router.js").then((r) => r.navigate("`+searchPath+`"))`, nil, awaitPromise),
		pollUntil(`location.pathname === "`+searchPath+`"`),
		chromedp.ActionFunc(func(context.Context) error { jobs.Release(); return nil }),
		awaitJobsSettled(),
		pollUntil(`[...document.querySelectorAll(".toast__actions button")].some((b) => b.textContent.includes("Go to `+e2eSwitchPack+`"))`),
		chromedp.Evaluate(`location.pathname`, &pathAfter),
		chromedp.Evaluate(`(() => {
			const toast = [...document.querySelectorAll(".toast")].find((t) => t.textContent.includes("Go to `+e2eSwitchPack+`"));
			return {
				title: toast.querySelector(".toast__title").textContent,
				detail: toast.querySelector(".toast__detail")?.textContent ?? "",
				buttons: [...toast.querySelectorAll(".toast__actions button")].map((el) => el.textContent.trim()),
			};
		})()`, &toast),
	)
	assert.Equal(t, searchPath, pathAfter, "the page stays where the user went")
	assert.Equal(t, "Switched to "+e2eSwitchPack, toast.Title)
	assert.Contains(t, toast.Buttons, "Go to "+e2eSwitchPack)

	f.runInBrowser(t,
		chromedp.Evaluate(`[...document.querySelectorAll(".toast__actions button")].find((b) => b.textContent.includes("Go to `+e2eSwitchPack+`")).click()`, nil),
		pollUntil(`location.pathname === "/g/`+f.Game.ID+`/`+e2eSwitchPack+`"`),
		pollUntil(`[...document.querySelectorAll(".mod-row")].some((r) => r.textContent.includes("Better Boots"))`),
	)
	live, err := f.Svc.NewProfileManager().GetDefault(t.Context(), f.Game.ID)
	require.NoError(t, err)
	assert.Equal(t, e2eSwitchPack, live.Name)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportSwitchProgress_NeverGoesBack holds the #664 rule for an
// import that switches (#666 review): once the import has run its mods,
// its switch - which counts its own disables, enables and downloads from
// their start - is a fresh, labelled step with an indeterminate bar, never
// a bar sent back to the switch's first item. Another job's switch frames
// keep their own measure.
func TestE2E_ImportSwitchProgress_NeverGoesBack(t *testing.T) {
	f := newE2EFixture(t)

	var got struct {
		Import []*float64 `json:"import"`
		Switch []*float64 `json:"switch"`
		Plain  []*float64 `json:"plain"`
		Label  string     `json:"label"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { progressFraction, humanizePhase } = await import("/static/app/progress.js");
			const job = "j-import-switch";
			const imp = (phase, index, total) => ({ job_id: job, op: "import", type: "step", phase, index, total });
			const sw = (phase, index, total) => ({ job_id: job, op: "switch", type: "mod", phase, index, total });
			const run = (frames) => frames.map((fr) => progressFraction(fr));
			return {
				import: run([imp("import_mod_installing", 1, 2), imp("import_mod_installing", 2, 2)]),
				switch: run([
					imp("import_switching"),
					sw("switch_disabled", 1, 3), sw("switch_enabled", 1, 2),
					{ job_id: job, op: "switch", type: "download", phase: "switch_downloading", percent: 10, total_bytes: 100 },
				]),
				plain: run([{ job_id: "j-plain-switch", op: "switch", type: "mod", phase: "switch_disabled", index: 1, total: 4 }]),
				label: humanizePhase("import_switching"),
			};
		})()`, &got, awaitPromise),
	)

	require.Len(t, got.Import, 2)
	require.NotNil(t, got.Import[1])
	assert.InDelta(t, 1.0, *got.Import[1], 0.001, "the import runs its bar to the end")
	for i, v := range got.Switch {
		assert.Nil(t, v, "switch frame %d is the fresh step's indeterminate bar, not a bar sent back", i)
	}
	require.Len(t, got.Plain, 1)
	require.NotNil(t, got.Plain[0], "a switch job of its own is measured as before")
	assert.InDelta(t, 0.25, *got.Plain[0], 0.001)
	assert.Equal(t, "Switching to profile", got.Label)
	assert.Empty(t, f.BrowserErrors())
}
