package serve_test

// #660 Part 2: the web half of the resolvable collection import. A
// collection import installs every item it can and leaves each one it
// could not on the profile's list to finish; the web UI shows such an
// import as needing attention, leads with Finish collection, and finishes
// each item from the Finish collection panel - answering an installer
// through the FOMOD wizard, retrying a download - while a deploy of the
// still-unfinished profile warns, naming what is left.

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eFinishPack is the collection (and the profile it imports as) of the
// Finish collection scenarios.
const e2eFinishPack = "FinishPack"

// finishPanel is the Finish collection panel's root.
const finishPanel = `.modal[data-kind="finish-collection"]`

// newE2EFinishFixture is the searchable-mods fixture with a FOMOD mod and
// a mod whose download fails until the source recovers, and FinishPack: a
// collection of Better Boots, the FOMOD mod with recorded choices that no
// longer fit its installer, and the failing mod.
func newE2EFinishFixture(t *testing.T) (e2eSearchFixture, string) {
	t.Helper()
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod:      domain.Mod{ID: "wizard", SourceID: "fake", Name: "Wizard Mod", Version: "1.0"},
		files:    []domain.DownloadableFile{{ID: "w1", Name: "Main", FileName: "wizard.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512}},
		archives: map[string][]byte{"w1": e2eFomodArchive(t)},
	})
	f.Src.addMod(e2eSearchSourceMod{
		mod:     domain.Mod{ID: "flaky", SourceID: "fake", Name: "Flaky Mod", Version: "1.0"},
		files:   []domain.DownloadableFile{{ID: "fl1", Name: "Main", FileName: "flaky.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 32}},
		members: map[string]string{"fl1": "Mods/flaky.pak"},
	})
	if f.Src.urlErrs == nil {
		f.Src.urlErrs = map[string]error{}
	}
	f.Src.urlErrs["flaky"] = errors.New("the download server is down")
	doc := "lmm_collection: 1\nname: " + e2eFinishPack + "\nrequires: [fomod]\nmods:\n" +
		"  - {source_id: fake, mod_id: " + e2eSearchInstallModID + ", version: \"2.0\"}\n" +
		"  - {source_id: fake, mod_id: wizard, version: \"1.0\", fomod: {config_sha256: " + strings.Repeat("ab", 32) +
		", steps: [{name: Size, groups: [{name: Texture, choices: [{name: Gone, idx: 0}]}]}]}}\n" +
		"  - {source_id: fake, mod_id: flaky, version: \"1.0\"}\n"
	path := filepath.Join(t.TempDir(), "finishpack.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o644))
	return f, path
}

// openImportCollectionDialog opens the "Import collection…" modal from the
// profile picker's menu, for a game NexusMods is not mapped for (its file
// and URL forms only).
func openImportCollectionDialog() chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		clickWhenSettled(`.profile-picker__menu [data-action="import-collection"]`),
		chromedp.WaitVisible(`.modal[data-kind="import-collection"] [data-testid="import-url"]`, chromedp.ByQuery),
		settleEffects(),
	}
}

// importFinishPack imports the collection at path from the Import
// collection… dialog, downloading and installing its mods.
func importFinishPack(path string) chromedp.Tasks {
	return chromedp.Tasks{
		openImportCollectionDialog(),
		chromedp.SetUploadFiles(`.modal[data-kind="import-collection"] input[type="file"]`, []string{path}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] [data-testid="collection-installer-choices"]`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('.modal[data-kind="profile_import"] label.form-check'))
				.find((l) => l.textContent.includes("Download and install"))?.querySelector("input").click();
		`, nil),
		settleEffects(),
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
	}
}

// finishRowsJS lists the panel's rows: each item's key and reason.
const finishRowsJS = `[...document.querySelectorAll('` + finishPanel + ` [data-todo]')]
	.map((row) => row.dataset.todo + "=" + row.dataset.reason)`

// confirmFinishPlan confirms the open collection_finish plan.
func confirmFinishPlan() chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`.modal[data-kind="collection_finish"] [data-testid="finish-plan"]`, chromedp.ByQuery),
		clickWhenSettled(`.modal[data-kind="collection_finish"] [data-action="confirm"]`),
	}
}

// TestE2E_FinishCollection_ImportAnswerDeployRetry is #660's web path end
// to end: an import with an installer whose recorded choices no longer fit
// and a download that fails installs the rest and toasts as needing
// attention, leading with Finish collection; the panel lists both; the
// installer is answered in the wizard; a deploy of the profile with one
// item still left warns, naming it; and a retry, once the source serves
// the file again, finishes the collection.
func TestE2E_FinishCollection_ImportAnswerDeployRetry(t *testing.T) {
	f, path := newE2EFinishFixture(t)
	(&e2eLauncher{appID: "892970"}).install(f.Svc)

	// 1. The import installs the rest, and its toast needs attention.
	var toast e2eOutcomeToast
	var rows []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		importFinishPack(path),
		awaitJobsSettled(),
		chromedp.WaitVisible(`.toast--attention`, chromedp.ByQuery),
		chromedp.Evaluate(outcomeToastJS, &toast),
	)
	assert.Equal(t, "Collection import finished", toast.Title)
	assert.Equal(t, "Recorded 1 of 3 mods; 2 need you", toast.Detail, "the profile is not the active one: recorded, as the CLI says it")
	assert.Equal(t, []string{"Finish collection", "Show in activity"}, toast.Buttons, "Finish collection leads")

	// 2. Finish collection lists both items, each with its reason.
	var summary string
	f.runInBrowser(t,
		clickWhenSettled(`.toast--attention [data-action="toast-action"]`),
		chromedp.WaitVisible(finishPanel+` [data-todo]`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll('`+finishPanel+` [data-todo]').length === 2`),
		chromedp.Evaluate(finishRowsJS, &rows),
		textContent(finishPanel+` [data-testid="finish-summary"]`, &summary),
	)
	assert.Equal(t, []string{"fake:wizard=installer_questions", "fake:flaky=download_failed"}, rows)
	assert.Contains(t, summary, "2 of 3 need you.")

	// 3. Answering the installer resolves one: the finish plan, then the
	// wizard over the panel, which comes back with one item left.
	var plan string
	f.runInBrowser(t,
		clickWhenSettled(finishPanel+` [data-todo="fake:wizard"] [data-action="finish-answer"]`),
		chromedp.WaitVisible(`.modal[data-kind="collection_finish"] [data-testid="finish-plan"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="collection_finish"]`, &plan),
		clickWhenSettled(`.modal[data-kind="collection_finish"] [data-action="confirm"]`),
		chromedp.WaitVisible(fomodModal+` [data-testid="fomod-step"][data-step="Size"]`, chromedp.ByQuery),
		clickWhenSettled(fomodModal+` [data-option="2K"] input`),
		clickWhenSettled(fomodModal+` [data-action="fomod-finish"]`),
		chromedp.WaitVisible(finishPanel, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll('`+finishPanel+` [data-todo]').length === 1`),
		chromedp.Evaluate(finishRowsJS, &rows),
	)
	assert.Contains(t, plan, "Wizard Mod")
	assert.Contains(t, plan, "its installer asks")
	assert.Equal(t, []string{"fake:flaky=download_failed"}, rows)

	// 4. Switched to, with one item still left: the switch and the deploy
	// both warn, naming it, the Profile card counts it, and Play launches
	// and says the collection is incomplete.
	var switchWarning, deployWarning, unresolved, finishButton, card string
	var trayFinish []string
	var applyOffered bool
	var played e2eOutcomeToast
	f.runInBrowser(t,
		clickWhenSettled(finishPanel+` [data-action="close-finish"]`),
		waitGone(finishPanel),
		// The import's tray entry offers Finish collection with the count
		// left now, not the import's own.
		clickWhenSettled(`.activity-bell__trigger`),
		pollUntil(`[...document.querySelectorAll('.tray [data-action="finish-collection"]')].some((b) => b.textContent.includes("(1)"))`),
		chromedp.Evaluate(`[...document.querySelectorAll('.tray [data-action="finish-collection"]')].map((b) => b.textContent.trim())`, &trayFinish),
		clickWhenSettled(`.activity-bell__trigger`),
		// Opening the tray acknowledged everything; forget that, so step 5
		// sees what the bell counts on its own.
		chromedp.Evaluate(`localStorage.clear()`, nil),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		clickWhenSettled(`[data-action="switch"][data-profile="`+e2eFinishPack+`"]`),
		chromedp.WaitVisible(`.modal[data-kind="switch"] [data-testid="plan-unresolved"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="switch"] [data-testid="plan-unresolved"]`, &switchWarning),
		clickWhenSettled(`.modal[data-kind="switch"] [data-action="confirm"]`),
		awaitJobsSettled(),
		chromedp.Navigate(f.BaseURL+"/g/"+f.Game.ID+"/"+url.PathEscape(e2eFinishPack)),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--profile [data-testid="profile-unresolved"]`, chromedp.ByQuery),
		textContent(`.card--profile [data-testid="profile-unresolved"]`, &unresolved),
		textContent(`.card--profile [data-action="finish-collection"]`, &finishButton),
		textContent(`.card--profile`, &card),
		chromedp.Evaluate(`document.querySelector('.card--profile [data-action="apply-profile"]') !== null`, &applyOffered),
		clickWhenSettled(`[data-action="deploy"]`),
		chromedp.WaitVisible(`.modal[data-kind="deploy"] [data-testid="plan-unresolved"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="deploy"] [data-testid="plan-unresolved"]`, &deployWarning),
		clickWhenSettled(`.modal[data-kind="deploy"] [data-action="cancel"]`),
		waitGone(`.modal`),
		pollUntil(`document.querySelector('[data-action="play"]')?.getAttribute("aria-disabled") === null`),
		clickWhenSettled(`[data-action="play"]`),
		chromedp.WaitVisible(`.toast--attention`, chromedp.ByQuery),
		chromedp.Evaluate(strings.Replace(outcomeToastJS, `".toast--attention, .toast--failure, .toast--success"`, `".toast--attention"`, 1), &played),
		clickWhenSettled(`.toast--attention .toast__dismiss`),
	)
	assert.Contains(t, played.Detail, "Collection incomplete: 1 item left to finish")
	assert.Equal(t, []string{"Finish collection"}, played.Buttons, "Play offers Finish collection")
	for _, warning := range []string{switchWarning, deployWarning} {
		assert.Contains(t, warning, "Collection incomplete: 1 item left to finish")
		assert.Contains(t, warning, "Flaky Mod")
		assert.NotContains(t, warning, "Wizard Mod", "the answered installer is no longer left")
	}
	assert.Equal(t, "Collection incomplete: 1 item left to finish", unresolved)
	assert.Equal(t, "Finish collection: 1 of 3 need you", finishButton)
	assert.NotContains(t, card, "not installed", "the item left to finish is said once, as one")
	assert.False(t, applyOffered, "an item left to finish is not Apply's to install")
	assert.NotEmpty(t, trayFinish)
	for _, label := range trayFinish {
		assert.Equal(t, "Finish collection (1)…", label)
	}

	// 5. The source serves the file again: a retry from the Profile card's
	// Finish collection resolves the last item.
	f.Src.recovered.Store(true)
	var todos []e2eTodo
	var cards, trayFinishLeft int
	var installed, trayStates []string
	f.runInBrowser(t,
		clickWhenSettled(`.card--profile [data-action="finish-collection"]`),
		chromedp.WaitVisible(finishPanel+` [data-todo="fake:flaky"]`, chromedp.ByQuery),
		clickWhenSettled(finishPanel+` [data-todo="fake:flaky"] [data-action="finish-retry"]`),
		confirmFinishPlan(),
		chromedp.WaitVisible(finishPanel, chromedp.ByQuery),
		pollUntil(`document.querySelector('`+finishPanel+` [data-testid="finish-summary"]')?.textContent.includes("Nothing is left to finish") === true`),
		clickWhenSettled(finishPanel+` [data-action="close-finish"]`),
		waitGone(finishPanel),
		pollUntil(`document.querySelector('.card--profile [data-action="finish-collection"]') === null`),
		chromedp.Evaluate(`document.querySelectorAll('[data-testid="profile-unresolved"]').length`, &cards),
		fetchTodos(f.Game.ID, e2eFinishPack, &todos),
		// The bell no longer counts the jobs that left the collection
		// unfinished, and their tray entries no longer offer to finish it.
		pollUntil(`document.querySelector('.activity-bell__count') === null`),
		clickWhenSettled(`.activity-bell__trigger`),
		chromedp.WaitVisible(`.tray__row`, chromedp.ByQuery),
		quietWindow(),
		chromedp.Evaluate(`document.querySelectorAll('.tray [data-action="finish-collection"]').length`, &trayFinishLeft),
		chromedp.Evaluate(`[...document.querySelectorAll('.tray__state')].map((el) => el.textContent.trim())`, &trayStates),
		chromedp.Evaluate(`fetch("/api/v1/mods?game=`+f.Game.ID+`&profile=`+e2eFinishPack+`").then((r) => r.json()).then((d) => d.mods.map((m) => m.name).sort())`, &installed, awaitPromise),
	)
	assert.Empty(t, todos)
	assert.Zero(t, cards)
	assert.Zero(t, trayFinishLeft, "a finished collection has nothing left to offer")
	assert.Contains(t, trayStates, "Recorded 3 mods", "the import's entry reads as a clean one once its collection is finished")
	for _, state := range trayStates {
		assert.NotContains(t, state, "need you", "no entry still says items need the user")
	}
	assert.Equal(t, []string{"Better Boots", "Flaky Mod", "Wizard Mod"}, installed, "every mod of the collection is installed")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FinishCollection_ACleanImportIsAPlainSuccess: an import with
// nothing left to finish is a plain success, counted as the CLI counts it:
// "Recorded 2 mods" into a profile that is not the active one.
func TestE2E_FinishCollection_ACleanImportIsAPlainSuccess(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	doc := "lmm_collection: 1\nname: Clean Pack\nmods:\n" +
		"  - {source_id: fake, mod_id: " + e2eSearchInstallModID + ", version: \"2.0\"}\n" +
		"  - {source_id: fake, mod_id: " + e2eSearchConflictModID + ", version: \"1.0\"}\n"
	path := filepath.Join(t.TempDir(), "clean.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o644))

	var toast e2eOutcomeToast
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		openImportCollectionDialog(),
		chromedp.SetUploadFiles(`.modal[data-kind="import-collection"] input[type="file"]`, []string{path}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="profile_import"] .plan`, chromedp.ByQuery),
		chromedp.Evaluate(`
			Array.from(document.querySelectorAll('.modal[data-kind="profile_import"] label.form-check'))
				.find((l) => l.textContent.includes("Download and install"))?.querySelector("input").click();
		`, nil),
		settleEffects(),
		clickWhenSettled(`.modal[data-kind="profile_import"] [data-action="confirm"]`),
		awaitJobsSettled(),
		chromedp.WaitVisible(`.toast--success`, chromedp.ByQuery),
		chromedp.Evaluate(outcomeToastJS, &toast),
	)
	assert.Equal(t, "success", toast.Tone)
	assert.Equal(t, "Collection import finished", toast.Title)
	assert.Equal(t, "Recorded 2 mods", toast.Detail, "into a profile that is not the active one")
	assert.NotContains(t, toast.Buttons, "Finish collection")
	assert.Empty(t, f.BrowserErrors())
}

// seedFinishProfile writes "Fin": a collection profile with an item of
// each kind of action left to finish - a manual download, a failed
// download, an installer's questions, a gone secondary file - for the
// dialog scenarios and the narrow-width pass - and a mod gone from its
// source.
func seedFinishProfile(t *testing.T, f e2eFixture) {
	t.Helper()
	require.NoError(t, config.SaveProfile(f.Svc.ConfigDir(), &domain.Profile{
		Name: "Fin", GameID: f.Game.ID,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "10", Version: "1.0", FileIDs: []string{"100"},
				Todo: &domain.CollectionTodo{Reason: domain.TodoManualDownload, Name: "Ten"}},
			{SourceID: "nexusmods", ModID: "11", Version: "1.1", FileIDs: []string{"110"},
				Todo: &domain.CollectionTodo{Reason: domain.TodoDownloadFailed, Name: "Eleven With A Rather Long Name For A Narrow Screen", Detail: "the download server is down"}},
			{SourceID: "nexusmods", ModID: "12", Version: "1.2", FileIDs: []string{"120"},
				Todo: &domain.CollectionTodo{Reason: domain.TodoInstallerQuestions, Name: "Twelve", FomodReason: "stale"}},
			{SourceID: "nexusmods", ModID: "13", Version: "1.3", FileIDs: []string{"130", "131"},
				Todo: &domain.CollectionTodo{Reason: domain.TodoGonePin, Name: "Thirteen", FileIDs: []string{"131"}, CanSkipFile: true}},
			{SourceID: "nexusmods", ModID: "14", Version: "1.4", FileIDs: []string{"140"},
				Todo: &domain.CollectionTodo{Reason: domain.TodoModGone, Name: "Fourteen"}},
		},
		Collection: &domain.CollectionInfo{ID: "nexusmods:fin", Origin: &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "fin", Revision: 1}},
	}))
}

// openFinishFromProfiles opens Finish collection on "Fin" from the
// profiles modal.
func openFinishFromProfiles(f dialogFixture) chromedp.Tasks {
	return inProfilesModal(f,
		chromedp.WaitVisible(`[data-action="finish-collection"][data-profile="Fin"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="finish-collection"][data-profile="Fin"]`),
		chromedp.WaitVisible(finishPanel+` [data-todo="nexusmods:13"]`, chromedp.ByQuery),
	)
}

// TestE2E_FinishCollection_EachItemHasItsAction: the panel offers each
// item its one action and Skip, opened from the profiles modal's row.
func TestE2E_FinishCollection_EachItemHasItsAction(t *testing.T) {
	f := newE2EFillFixture(t)
	seedFinishProfile(t, f.e2eFixture)

	var actions map[string][]string
	var label string
	f.runInBrowser(t,
		openFinishFromProfiles(dialogFixture{e2eFixture: f.e2eFixture}),
		textContent(`[data-testid="finish-summary"]`, &label),
		chromedp.Evaluate(`Object.fromEntries([...document.querySelectorAll('`+finishPanel+` [data-todo]')]
			.map((row) => [row.dataset.todo, [...row.querySelectorAll("button")].map((b) => b.dataset.action)]))`, &actions),
	)
	assert.Contains(t, label, "5 of 5 need you.")
	assert.Equal(t, map[string][]string{
		"nexusmods:10": {"finish-download", "finish-skip"},
		"nexusmods:11": {"finish-retry", "finish-skip"},
		"nexusmods:12": {"finish-answer", "finish-skip"},
		"nexusmods:13": {"finish-skip-file", "finish-skip"},
		"nexusmods:14": {"finish-skip"},
	}, actions, "a mod gone from its source offers Skip only")

	// Download by hand opens the fill drop zone under its row.
	var dropZone bool
	f.runInBrowser(t,
		clickWhenSettled(finishPanel+` [data-todo="nexusmods:10"] [data-action="finish-download"]`),
		chromedp.WaitVisible(finishPanel+` [data-todo="nexusmods:10"] [data-testid="fill-drop"]`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('`+finishPanel+` [data-todo="nexusmods:10"] [data-action="fill-folder"]') !== null`, &dropZone),
	)
	assert.True(t, dropZone, "the drop zone offers the folder too")

	// Skip plans the skip, and Cancel goes back to the panel.
	var skipPlan string
	f.runInBrowser(t,
		clickWhenSettled(finishPanel+` [data-todo="nexusmods:11"] [data-action="finish-skip"]`),
		chromedp.WaitVisible(`.modal[data-kind="collection_finish"] [data-testid="finish-skip"]`, chromedp.ByQuery),
		textContent(`.modal[data-kind="collection_finish"] [data-testid="finish-skip"]`, &skipPlan),
		clickWhenSettled(`.modal[data-kind="collection_finish"] [data-action="cancel"]`),
		chromedp.WaitVisible(finishPanel+` [data-todo="nexusmods:11"]`, chromedp.ByQuery),
	)
	assert.Contains(t, skipPlan, "Eleven With A Rather Long Name")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FinishCollection_TallyWordsAndTrayLogCollapse pins, in the
// browser, a collection tally's words and tone - "Imported N of M mods; K
// need you" needing attention, "Imported M mods" a success, "Recorded" for
// a profile that is not the active one - and the activity entry's event
// log keeping only the latest plain progress line per mod, by the event's
// exact mod (two mods may share a name), while every other line - a
// failure, a refusal, a note, an unavailable item, a warning - is kept.
func TestE2E_FinishCollection_TallyWordsAndTrayLogCollapse(t *testing.T) {
	f := newE2EFixture(t)

	var got struct {
		Partial     string   `json:"partial"`
		PartialTone string   `json:"partialTone"`
		Clean       string   `json:"clean"`
		CleanTone   string   `json:"cleanTone"`
		Pending     string   `json:"pending"`
		Recorded    string   `json:"recorded"`
		Log         []string `json:"log"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const p = await import("/static/app/progress.js");
			const partial = { kind: "profile_import", installed: 38, failed: 2, skipped: 0, todos: 2, total: 40 };
			const clean = { kind: "profile_import", installed: 40, failed: 0, skipped: 0, total: 40 };
			const pending = { kind: "profile_import", installed: 0, failed: 0, skipped: 40, total: 40 };
			const mod = (id) => ({ source_id: "fake", mod_id: id });
			const ev = (type, id, phase, extra) => ({ type, data: { op: "switch", mod: mod(id), mod_name: "Same Name", phase, ...extra } });
			const events = [
				ev("step", "1", "switch_downloading"),
				ev("download", "1", "switch_downloading", { percent: 50 }),
				ev("step", "1", "switch_install_error", { detail: "boom" }),
				ev("download", "1", "switch_downloading", { percent: 100 }),
				ev("step", "2", "install_lock_refusal", { detail: "locked" }),
				ev("step", "2", "switch_downloading"),
				ev("step", "3", "workshop_unavailable", { detail: "gone" }),
				ev("step", "2", "install_note", { detail: "a note" }),
				ev("step", "2", "switch_installing"),
				ev("step", "1", "switch_install_note", { detail: "another note" }),
			];
			let log = [];
			events.forEach((event, i) => { log = p.appendCollapsed(log, { key: i, frame: p.frameFromEvent(event) }); });
			return {
				partial: p.resultTallyLabel(partial), partialTone: p.resultTallyTone(partial),
				clean: p.resultTallyLabel(clean), cleanTone: p.resultTallyTone(clean),
				pending: p.resultTallyLabel(pending),
				recorded: p.collectionTallyLabel({ total: 3, todos: 2, recorded: true }),
				log: log.map((e) => (e.frame.type === "download" ? "download" : e.frame.phase) + ":" + e.frame.mod_key),
			};
		})()`, &got, awaitPromise),
	)
	assert.Equal(t, "Imported 38 of 40 mods; 2 need you", got.Partial)
	assert.Equal(t, "attention", got.PartialTone)
	assert.Equal(t, "Imported 40 mods", got.Clean)
	assert.Equal(t, "succeeded", got.CleanTone)
	assert.Equal(t, "0 installed · 40 skipped", got.Pending, "an import that saved its mods pending keeps its words")
	assert.Equal(t, "Recorded 1 of 3 mods; 2 need you", got.Recorded)
	assert.Equal(t, []string{
		"switch_install_error:fake:1",
		"download:fake:1",
		"install_lock_refusal:fake:2",
		"workshop_unavailable:fake:3",
		"install_note:fake:2",
		"switch_installing:fake:2",
		"switch_install_note:fake:1",
	}, got.Log, "only plain progress collapses, per exact mod")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FinishCollection_TheProfileCardCountsItemsOnce: a profile whose
// only difference is its items left to finish says them once - as items
// left to finish, never also as "mods not installed" - and offers Finish
// collection, not Apply profile.
func TestE2E_FinishCollection_TheProfileCardCountsItemsOnce(t *testing.T) {
	f := newE2EFillFixture(t)
	seedFinishProfile(t, f.e2eFixture)

	var card, title, button string
	var applyOffered bool
	f.runInBrowser(t,
		chromedp.Navigate(f.BaseURL+"/g/"+f.Game.ID+"/Fin"),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.card--profile [data-testid="profile-unresolved"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.card--profile [data-action="finish-collection"]') !== null`),
		textContent(`.card--profile`, &card),
		textContent(`.card--profile .card__title`, &title),
		textContent(`.card--profile [data-action="finish-collection"]`, &button),
		chromedp.Evaluate(`document.querySelector('.card--profile [data-action="apply-profile"]') !== null`, &applyOffered),
	)
	assert.Contains(t, card, "Collection incomplete: 5 items left to finish")
	assert.NotContains(t, card, "not installed", "an item left to finish is not also a mod not installed")
	assert.False(t, applyOffered, "items left to finish alone offer no Apply profile")
	assert.Equal(t, "◎ Profile (5)", title)
	assert.Equal(t, "Finish collection: 5 of 5 need you", button)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_FinishCollection_ADeletedProfileLeavesNothingToFinish: a
// collection profile deleted with items left to finish has nothing left
// to finish - its to-do list answers 404 - so its import's tray entry
// stops asking for attention and offering Finish collection.
func TestE2E_FinishCollection_ADeletedProfileLeavesNothingToFinish(t *testing.T) {
	f, path := newE2EFinishFixture(t)

	var counted bool
	var trayFinish int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		importFinishPack(path),
		awaitJobsSettled(),
		chromedp.WaitVisible(`.toast--attention`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.activity-bell__count') !== null`),
		chromedp.Evaluate(`document.querySelector('.activity-bell__count') !== null`, &counted),
		chromedp.Evaluate(`import("/static/app/api.js").then((api) => api.deleteProfile(`+"`"+e2eFinishPack+"`"+`, { game: `+"`"+f.Game.ID+"`"+` })).then(() => true)`, nil, awaitPromise),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.activity-bell__count') === null`),
		clickWhenSettled(`.activity-bell__trigger`),
		chromedp.WaitVisible(`.tray__row`, chromedp.ByQuery),
		quietWindow(),
		chromedp.Evaluate(`document.querySelectorAll('.tray [data-action="finish-collection"]').length`, &trayFinish),
	)
	assert.True(t, counted, "before the delete, the import needs attention")
	assert.Zero(t, trayFinish, "a deleted profile has nothing left to finish")
	// The deleted profile's list answers 404, which the browser logs.
	assertOnlyExpectedErrors(t, f.BrowserErrors(), "/api/v1/profiles/"+e2eFinishPack+"/todos")
}

// TestE2E_FinishCollection_LiveTodosHelpers pins livetodos.js: a hydrate's
// to-do list counts for its game only while the page is still on that
// game and profile (a newer hydrate of another game with a profile of the
// same name must not be written under the old game), and a 404 - the
// profile is gone - counts as nothing left, while any other failure keeps
// the last count.
func TestE2E_FinishCollection_LiveTodosHelpers(t *testing.T) {
	f := newE2EFixture(t)

	var got map[string]any
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const lt = await import("/static/app/livetodos.js");
			const { ApiError } = await import("/static/app/api.js");
			const report = { profile: "default", total: 3, todos: [{}] };
			const same = lt.hydratedTodos({ game: "g1", profile: "default" }, { game: "g1", profile: "default" }, report);
			const moved = lt.hydratedTodos({ game: "g1", profile: "default" }, { game: "g2", profile: "default" }, report);
			const other = lt.hydratedTodos({ game: "g1", profile: "default" }, { game: "g1", profile: "default" }, { ...report, profile: "x" });
			const notes = [];
			const note = (game, doc) => notes.push(game + ":" + doc.profile + "=" + doc.todos.length);
			await lt.refreshLiveTodos("g1", "gone", note, () => Promise.reject(new ApiError(404, "profile not found")));
			await lt.refreshLiveTodos("g1", "flaky", note, () => Promise.reject(new ApiError(500, "boom")));
			await lt.refreshLiveTodos("g1", "here", note, () => Promise.resolve({ profile: "here", todos: [{}, {}] }));
			let calls = 0;
			let release;
			const slow = () => { calls++; return new Promise((r) => { release = () => r({ profile: "dup", todos: [] }); }); };
			const a = lt.refreshLiveTodos("g1", "dup", note, slow);
			const b = lt.refreshLiveTodos("g1", "dup", note, slow);
			await new Promise((r) => setTimeout(r, 0));
			release();
			await Promise.all([a, b]);
			return { same: same !== null, moved: moved !== null, other: other !== null, notes, calls };
		})()`, &got, awaitPromise),
	)
	assert.Equal(t, true, got["same"])
	assert.Equal(t, false, got["moved"], "another game's hydrate has replaced the page")
	assert.Equal(t, false, got["other"], "the list is another profile's")
	assert.Equal(t, []any{"g1:gone=0", "g1:here=2", "g1:dup=0"}, got["notes"], "404 is nothing left; another failure notes nothing")
	assert.EqualValues(t, 1, got["calls"], "one read of a list at a time")
	assert.Empty(t, f.BrowserErrors())
}
