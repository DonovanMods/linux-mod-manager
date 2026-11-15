package serve_test

// #655 (and #660's ruling that a collection import is never all or
// nothing): a collection import started from the "Import collection…" modal
// gave no feedback while it ran, and its failure toast read
// "profile_import failed", with the engine's joined text for a body and two
// actions run together ("Open profilesShow in activity"), one of which
// opened an empty form. The scenarios here start a long import - hundreds
// of items, over a thousand events, more than the job's 1024-event ring -
// against a NexusMods double whose downloads are held until the test lets
// them go, and two of whose items' files are refused.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eLongImportItems is how many items the long collection carries for a
// scenario that fails: each emits at least two events, so a failed import
// emits more than the job ring's 1024. A refused import writes nothing, so
// this is fast however long the list.
const e2eLongImportItems = 600

// e2eShortImportItems is the collection's size for a scenario that
// SUCCEEDS: every item is downloaded, installed and deployed, which under
// -race runs at a few items a second - 600 took about 96 seconds, past the
// harness's budget (the #655 race-suite failure).
const e2eShortImportItems = 40

// e2eLongImportURL is the long collection's page address.
const e2eLongImportURL = "https://www.nexusmods.com/games/skyrimspecialedition/collections/longcoll"

// The two refused items, and why.
const (
	e2eLongImportFirstFailed = "1010"
	e2eLongImportLastFailed  = "1599"
)

var (
	errE2ELongImportGone   = errors.New("stored file no longer available upstream")
	errE2ELongImportBadZip = errors.New("zip: unsupported compression algorithm")
)

// e2eLongImportSource is the Premium double reading a slug out of a page
// address, as the real source does.
type e2eLongImportSource struct{ e2ePremiumNexusSource }

func (s e2eLongImportSource) ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (source.Collection, error) {
	return s.e2ePremiumNexusSource.ResolveGameCollection(ctx, gameSourceID, ref[strings.LastIndex(ref, "/")+1:])
}

// e2eImportFailure is which of the long collection's downloads are refused.
type e2eImportFailure int

const (
	e2eFailNone e2eImportFailure = iota
	e2eFailTwo
	e2eFailAll
)

// newE2ELongImportFixture is a Premium NexusMods double over "longcoll", a
// collection of e2eLongImportItems real downloads, every one held until
// release is called. fail says which downloads are refused: none, two, or
// every one.
func newE2ELongImportFixture(t *testing.T, fail e2eImportFailure) (f e2eFixture, release func()) {
	t.Helper()
	return newE2EImportFixtureOf(t, fail, e2eLongImportItems)
}

// newE2EImportFixtureOf is newE2ELongImportFixture with items items.
func newE2EImportFixtureOf(t *testing.T, fail e2eImportFailure, items int) (f e2eFixture, release func()) {
	t.Helper()
	sandboxE2EEnv(t)
	src := newE2ESearchSource(t, "nexusmods")
	gate := make(chan struct{})
	src.urlGate = gate
	src.urlErrs = map[string]error{}
	mods := make([]string, 0, items)
	for i := range items {
		id := fmt.Sprintf("%d", 1000+i)
		fileID := id + "0"
		src.addMod(e2eSearchSourceMod{
			mod:     domain.Mod{ID: id, SourceID: "nexusmods", Name: "Item " + id, Version: "1.0"},
			files:   []domain.DownloadableFile{{ID: fileID, Name: "Main", FileName: "item" + id + ".zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 16}},
			members: map[string]string{fileID: "item" + id + ".esp"},
		})
		mods = append(mods, fmt.Sprintf(`{"Name": "Item %s", "Version": "1.0", "SourceType": "nexus", "ModID": "%s", "FileID": "%s", "UpdatePolicy": "exact"}`, id, id, fileID))
		if fail == e2eFailAll {
			src.urlErrs[id] = errE2ELongImportGone
		}
	}
	if fail == e2eFailTwo {
		src.urlErrs[e2eLongImportFirstFailed] = errE2ELongImportBadZip
		src.urlErrs[e2eLongImportLastFailed] = errE2ELongImportGone
	}
	f = newE2EFixture(t)
	f.Svc.RegisterSource(e2eLongImportSource{e2ePremiumNexusSource{src}})
	f.Game.SourceIDs["nexusmods"] = "skyrimspecialedition"
	require.NoError(t, f.Svc.SaveGame(t.Context(), f.Game))
	seedE2ENexusRecipe(t, f, "longcoll", `{"Name": "Long Collection", "Mods": [`+strings.Join(mods, ",")+`]}`)
	released := false
	release = func() {
		if !released {
			released = true
			close(gate)
		}
	}
	t.Cleanup(release)
	return f, release
}

// confirmImportPlan opts the open import plan in to downloading its mods
// and confirms it.
func confirmImportPlan() chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`[data-testid="source-items"]`, chromedp.ByQuery),
		clickWhenSettled(`.modal .form-check input[type="checkbox"]`),
		clickWhenSettled(`.modal [data-action="confirm"]`),
	}
}

// startLongImportFromModal plans "longcoll" from the Import collection…
// modal and confirms it.
func startLongImportFromModal() chromedp.Tasks {
	return chromedp.Tasks{
		openImportCollectionFromPicker(),
		chromedp.SetValue(`[data-testid="nexus-collection-ref"]`, "longcoll", chromedp.ByQuery),
		clickWhenSettled(`[data-testid="nexus-collection-import"]`),
		confirmImportPlan(),
	}
}

// awaitRunningToast waits for the running import's progress toast and its
// first count of the collection's items.
func awaitRunningToast(items int, title, progress *string) chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`.toast--running`, chromedp.ByQuery),
		pollUntil(fmt.Sprintf(`document.querySelector('.toast--running [data-testid="toast-progress"]')?.textContent.includes("of %d") === true`, items)),
		textContent(`.toast--running .toast__title`, title),
		textContent(`.toast--running [data-testid="toast-progress"]`, progress),
	}
}

// awaitJobsSettled waits until the server has no running job: the end
// state the outcome toast follows, waited for on its own so a slow run
// fails on the job, not on the toast.
func awaitJobsSettled() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		for {
			var settled bool
			if err := chromedp.Evaluate(`fetch("/api/v1/jobs").then((r) => r.json()).then((d) =>
				d.jobs.length > 0 && d.jobs.every((j) => j.state !== "running"))`, &settled, awaitPromise).Do(ctx); err != nil {
				return err
			}
			if settled {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(e2ePollInterval):
			}
		}
	})
}

// outcomeToastJS reads the outcome toast: its title, its detail and its
// action buttons, with the gap between the first two.
const outcomeToastJS = `(() => {
	const toast = document.querySelector(".toast--failure, .toast--success");
	const buttons = [...toast.querySelectorAll(".toast__actions button")];
	const a = buttons[0]?.getBoundingClientRect();
	const b = buttons[1]?.getBoundingClientRect();
	const sameRow = a && b && Math.abs(a.top - b.top) < 2;
	return {
		tone: toast.classList.contains("toast--failure") ? "attention" : "success",
		title: toast.querySelector(".toast__title").textContent,
		detail: toast.querySelector(".toast__detail")?.textContent ?? "",
		buttons: buttons.map((el) => el.textContent.trim()),
		gap: !a || !b ? -1 : sameRow ? b.left - a.right : b.top - a.bottom,
	};
})()`

type e2eOutcomeToast struct {
	Tone    string   `json:"tone"`
	Title   string   `json:"title"`
	Detail  string   `json:"detail"`
	Buttons []string `json:"buttons"`
	Gap     float64  `json:"gap"`
}

// assertCollectionTodoToast holds the outcome toast of a collection import
// that left items to finish (#660): the import is no longer refused - it
// finishes, and the toast stays, needing attention, counting what failed.
// (Part 2 of #660 gives it Finish collection.)
func assertCollectionTodoToast(t *testing.T, got e2eOutcomeToast, failed int) {
	t.Helper()
	assert.Equal(t, "Collection import finished", got.Title)
	assert.Equal(t, "attention", got.Tone, "an import that left items to finish is not a plain success")
	assert.Contains(t, got.Detail, fmt.Sprintf("%d failed", failed))
	assert.Equal(t, []string{"Open profiles", "Show in activity"}, got.Buttons)
	assert.GreaterOrEqual(t, got.Gap, 4.0, "the two actions are two spaced controls")
}

// TestE2E_ImportFeedback_ProgressThenAStickyOutcome is the owner's report
// (#655), under #660's ruling: the import shows its progress the moment it
// is confirmed and while it runs, installs every mod it can, and its
// outcome - after more events than the job ring holds - stays on screen,
// needing attention, until dismissed.
func TestE2E_ImportFeedback_ProgressThenAStickyOutcome(t *testing.T) {
	f, release := newE2ELongImportFixture(t, e2eFailTwo)

	var title, progress string
	var toast e2eOutcomeToast
	var running, toastsLeft int
	var jobs string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		startLongImportFromModal(),
		awaitRunningToast(e2eLongImportItems, &title, &progress),
		chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
		awaitJobsSettled(),
		chromedp.WaitVisible(`.toast--failure`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".toast--running").length`, &running),
		chromedp.Evaluate(outcomeToastJS, &toast),
		quietWindow(),
		chromedp.Evaluate(`document.querySelectorAll(".toast--failure").length`, &toastsLeft),
		chromedp.Evaluate(`fetch("/api/v1/jobs").then((r) => r.text())`, &jobs, awaitPromise),
	)

	assert.Equal(t, "Importing collection…", title)
	assert.Contains(t, progress, "of 600", "the import counts its items")
	assert.Zero(t, running, "the outcome replaces the progress")
	assertCollectionTodoToast(t, toast, 2)
	assert.Equal(t, 1, toastsLeft, "it stays until dismissed")
	assert.Contains(t, jobs, `"dropped_events"`, "the job outran its event ring, and its end still arrived")
	assert.Contains(t, jobs, `"todos": 2`, "the job's tally carries the items left to finish")
	assert.Contains(t, jobs, `"state": "succeeded"`, "#660: the import is not refused")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_ShowInActivityOpensTheJob: the toast's "Show in
// activity" opens the tray on THIS job, named in words.
func TestE2E_ImportFeedback_ShowInActivityOpensTheJob(t *testing.T) {
	f, release := newE2ELongImportFixture(t, e2eFailTwo)
	release()

	var kind string
	var expanded bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		startLongImportFromModal(),
		awaitJobsSettled(),
		chromedp.WaitVisible(`.toast--failure`, chromedp.ByQuery),
		clickWhenSettled(`.toast--failure [data-action="toast-activity"]`),
		chromedp.WaitVisible(`.tray__row[data-state="succeeded"] .tray__summary[aria-expanded="true"]`, chromedp.ByQuery),
		textContent(`.tray__row[data-state="succeeded"] .tray__kind`, &kind),
		chromedp.Evaluate(`document.querySelector('.tray__row[data-state="succeeded"] .tray__summary').getAttribute("aria-expanded") === "true"`, &expanded),
	)

	assert.Equal(t, "Collection import", kind)
	assert.True(t, expanded, "the tray opens on this job")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_ItemsLeftToFinishAreListed (#660): the import
// leaves the mods it could not install on the profile's list of items to
// finish, each with its reason and action - what Part 2's Finish
// collection panel shows.
func TestE2E_ImportFeedback_ItemsLeftToFinishAreListed(t *testing.T) {
	f, release := newE2ELongImportFixture(t, e2eFailTwo)
	release()

	var report struct {
		Total int `json:"total"`
		Todos []struct {
			ModID  string `json:"mod_id"`
			Reason string `json:"reason"`
			Action string `json:"action"`
		} `json:"todos"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		startLongImportFromModal(),
		awaitJobsSettled(),
		chromedp.Evaluate(`fetch("/api/v1/profiles/"+encodeURIComponent("Long Collection")+"/todos?game=`+f.Game.ID+`").then((r) => r.json())`, &report, awaitPromise),
	)
	assert.Equal(t, e2eLongImportItems, report.Total)
	require.Len(t, report.Todos, 2)
	assert.Equal(t, e2eLongImportFirstFailed, report.Todos[0].ModID)
	assert.Equal(t, e2eLongImportLastFailed, report.Todos[1].ModID)
	for _, todo := range report.Todos {
		assert.NotEmpty(t, todo.Reason)
		assert.Equal(t, "retry", todo.Action)
	}
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_SuccessShowsProgressThenFinished: a long import
// that succeeds shows its progress too, then says it finished, in words.
func TestE2E_ImportFeedback_SuccessShowsProgressThenFinished(t *testing.T) {
	f, release := newE2EImportFixtureOf(t, e2eFailNone, e2eShortImportItems)

	var title, progress, done string
	var buttons []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		startLongImportFromModal(),
		awaitRunningToast(e2eShortImportItems, &title, &progress),
		chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
		awaitJobsSettled(),
		chromedp.WaitVisible(`.toast--success`, chromedp.ByQuery),
		textContent(`.toast--success .toast__title`, &done),
		chromedp.Evaluate(`[...document.querySelectorAll(".toast--success .toast__actions button")].map((b) => b.textContent.trim())`, &buttons),
	)

	assert.Equal(t, "Importing collection…", title)
	assert.Equal(t, "Collection import finished", done)
	assert.Equal(t, []string{"Open profiles", "Show in activity"}, buttons)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_EveryDoor: the search page's offer, the omnibar's
// offer and the profiles modal start the same import, and each shows its
// progress and then its failure.
func TestE2E_ImportFeedback_EveryDoor(t *testing.T) {
	doors := map[string]func(f e2eFixture) chromedp.Tasks{
		"search page offer": func(f e2eFixture) chromedp.Tasks {
			return chromedp.Tasks{
				chromedp.Navigate(workshopSearchPath(f, e2eLongImportURL)),
				chromedp.WaitVisible(`[data-testid="collection-offer"][data-source="nexusmods"]`, chromedp.ByQuery),
				clickWhenSettled(`[data-testid="collection-offer"] button`),
			}
		},
		"omnibar offer": func(f e2eFixture) chromedp.Tasks {
			return chromedp.Tasks{
				chromedp.Navigate(f.HomePath()),
				chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
				chromedp.SendKeys(`.omnibar`, e2eLongImportURL+"\r", chromedp.ByQuery),
				chromedp.WaitVisible(`.omnibar-results [data-testid="collection-offer"][data-source="nexusmods"]`, chromedp.ByQuery),
				clickWhenSettled(`.omnibar-results [data-testid="collection-offer"] button`),
			}
		},
		"profiles modal": func(f e2eFixture) chromedp.Tasks {
			return chromedp.Tasks{
				chromedp.Navigate(f.HomePath()),
				openNexusImport("longcoll"),
			}
		},
	}
	for name, door := range doors {
		t.Run(name, func(t *testing.T) {
			f, release := newE2ELongImportFixture(t, e2eFailTwo)
			var title, progress string
			var toast e2eOutcomeToast
			f.runInBrowser(t,
				door(f),
				confirmImportPlan(),
				awaitRunningToast(e2eLongImportItems, &title, &progress),
				chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
				awaitJobsSettled(),
				chromedp.WaitVisible(`.toast--failure`, chromedp.ByQuery),
				chromedp.Evaluate(outcomeToastJS, &toast),
			)
			assert.Equal(t, "Importing collection…", title)
			assertCollectionTodoToast(t, toast, 2)
			assert.Empty(t, f.BrowserErrors())
		})
	}
}

// TestE2E_ImportFeedback_EveryItemFailing (#660): an import whose every
// item failed still saves the profile, every item left to finish, and its
// toast counts them.
func TestE2E_ImportFeedback_EveryItemFailing(t *testing.T) {
	f, release := newE2ELongImportFixture(t, e2eFailAll)
	release()

	var toast e2eOutcomeToast
	var todos int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		startLongImportFromModal(),
		awaitJobsSettled(),
		chromedp.WaitVisible(`.toast--failure`, chromedp.ByQuery),
		chromedp.Evaluate(outcomeToastJS, &toast),
		chromedp.Evaluate(`fetch("/api/v1/profiles/"+encodeURIComponent("Long Collection")+"/todos?game=`+f.Game.ID+`").then((r) => r.json()).then((d) => d.todos.length)`, &todos, awaitPromise),
	)
	assertCollectionTodoToast(t, toast, e2eLongImportItems)
	assert.Equal(t, e2eLongImportItems, todos)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_HeldBackItemsAreCounted (#655 review S1): an
// import refused with nothing written lists the item that refused first,
// and counts the items only held back behind it on one line.
func TestE2E_ImportFeedback_HeldBackItemsAreCounted(t *testing.T) {
	f := newE2EFixture(t)

	var got map[string]any
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(async () => {
			const { failureSummary, failuresOf } = await import("/static/app/itemfailures.js");
			const { ItemFailureList } = await import("/static/app/components/errordetails.js");
			const { html, render } = await import("/static/app/render.js");
			const failures = [];
			for (let i = 0; i < 299; i++) {
				failures.push({ source_id: "s", mod_id: String(i), name: "Held " + i, refusal: "held_back",
					reason: "held back: another mod refused, so nothing was written" });
			}
			failures.splice(150, 0, { source_id: "s", mod_id: "x", name: "Thing", refusal: "loader_required", reason: "needs BepInEx" });
			const summary = { kind: "profile_import", error: { error: "...", details: { refused: true, failures } } };
			const items = failuresOf(summary);
			const box = document.createElement("div");
			render(html`+"`<${ItemFailureList} items=${items} limit=${5} summary=${true} />`"+`, box);
			return {
				read: items.length,
				summary: failureSummary(items),
				lines: [...box.querySelectorAll(".item-failures__item")].map((li) => li.textContent.replace(/\s+/g, " ").trim()),
				held: box.querySelector(".item-failures__held")?.textContent.replace(/\s+/g, " ").trim() ?? "",
				noItems: failuresOf({ kind: "deploy", error: { error: "x", details: { failures } } }).length,
			};
		})()`, &got, awaitPromise),
	)

	assert.EqualValues(t, 300, got["read"], "a profile import's details.failures are its per-mod failures")
	assert.Equal(t, "1 mod refused · 299 held back", got["summary"])
	assert.Equal(t, []any{"Thing: needs BepInEx"}, got["lines"], "the refusal leads; held-back items are not listed one by one")
	assert.Equal(t, "299 mods held back: not written because another mod refused", got["held"])
	assert.EqualValues(t, 0, got["noItems"], "another kind's details are not read as failures")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_ReloadKeepsTheProgress (#655 review S3): a page
// reloaded mid-import has no origin for the job, and still shows its
// progress, named as a collection import.
func TestE2E_ImportFeedback_ReloadKeepsTheProgress(t *testing.T) {
	f, release := newE2EImportFixtureOf(t, e2eFailNone, e2eShortImportItems)

	var title, again, progress string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		startLongImportFromModal(),
		awaitRunningToast(e2eShortImportItems, &title, &progress),
		chromedp.Reload(),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.toast--running`, chromedp.ByQuery),
		textContent(`.toast--running .toast__title`, &again),
		chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
		chromedp.WaitVisible(`.toast--success`, chromedp.ByQuery),
		textContent(`.toast--success .toast__title`, &title),
	)
	assert.Equal(t, "Importing collection…", again)
	assert.Equal(t, "Collection import finished", title, "the outcome is named from the summary too")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_RunningToastAnnouncesItsTitleOnly (#655 review
// S6): the running toast's live region is its title, not its count.
func TestE2E_ImportFeedback_RunningToastAnnouncesItsTitleOnly(t *testing.T) {
	f, _ := newE2EImportFixtureOf(t, e2eFailNone, e2eShortImportItems)

	var got map[string]any
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		startLongImportFromModal(),
		chromedp.WaitVisible(`.toast--running`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const toast = document.querySelector(".toast--running");
			return {
				toastRole: toast.getAttribute("role") ?? "",
				live: [...toast.querySelectorAll("[role=status], [role=alert], [aria-live]")].map((el) => el.textContent.trim()),
			};
		})()`, &got),
	)
	assert.Equal(t, "", got["toastRole"])
	assert.Equal(t, []any{"Importing collection…"}, got["live"])
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ToastActivity_FromTheModPageNavigates and _NotFromSetup (#655
// review S5): "Show in activity" takes a mod page's user to Mission
// Control's tray, open on the job; on Setup, whose editors hold unsaved
// drafts, it is not offered, and the draft stays.
func TestE2E_ToastActivity_FromTheModPageNavigates(t *testing.T) {
	f := newE2EFixtureWithRollbackReadyMod(t)

	var jobID string
	var url string
	f.runInBrowser(t,
		chromedp.Navigate(f.ModPagePath("fake", "a")),
		chromedp.WaitVisible(`.mod-page`, chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error {
			jobID = startDeployFromAnotherClient(t, f)
			return nil
		}),
		chromedp.WaitVisible(`.toast--success [data-action="toast-activity"]`, chromedp.ByQuery),
		clickWhenSettled(`.toast--success [data-action="toast-activity"]`),
		chromedp.WaitVisible(`.tray`, chromedp.ByQuery),
		chromedp.Location(&url),
	)
	assert.Contains(t, url, "?job="+jobID)
	var expanded bool
	f.runInBrowser(t, chromedp.Evaluate(`document.querySelector('.tray__row[data-job="`+jobID+`"] .tray__summary')?.getAttribute("aria-expanded") === "true"`, &expanded))
	assert.True(t, expanded, "the tray opens on the job")
	assert.Empty(t, f.BrowserErrors())
}

func TestE2E_ToastActivity_NotFromSetup(t *testing.T) {
	f := newE2EFixtureWithDeployableMods(t)
	const draft = "id: my-draft\nname: Draft\n"

	var activity bool
	var value, url string
	f.runInBrowser(t,
		chromedp.Navigate(f.SetupPath("sources")),
		chromedp.WaitVisible(`[data-testid="setup-sources"]`, chromedp.ByQuery),
		clickWhenSettled(`[data-action="new-source"]`),
		chromedp.WaitVisible(`.source-editor__textarea`, chromedp.ByQuery),
		chromedp.SetValue(`.source-editor__textarea`, draft, chromedp.ByQuery),
		chromedp.ActionFunc(func(context.Context) error {
			startDeployFromAnotherClient(t, f)
			return nil
		}),
		chromedp.WaitVisible(`.toast--success`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector('.toast--success [data-action="toast-activity"]') !== null`, &activity),
		chromedp.Value(`.source-editor__textarea`, &value, chromedp.ByQuery),
		chromedp.Location(&url),
	)
	assert.False(t, activity, "Setup offers no way off the page that would drop the draft")
	assert.Equal(t, strings.ReplaceAll(draft, "\\n", "\n"), value)
	assert.Contains(t, url, "/setup")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_AnInstallerIsLeftToFinish (#660): a collection
// whose recorded installer choices no longer fit no longer stops to ask:
// the other mod installs, the job succeeds, and the installer is left to
// finish, with "answer it to continue".
func TestE2E_ImportFeedback_AnInstallerIsLeftToFinish(t *testing.T) {
	f := newE2EFixtureWithSearchableMods(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod:      domain.Mod{ID: "wizard", SourceID: "fake", Name: "Wizard Mod", Version: "1.0"},
		files:    []domain.DownloadableFile{{ID: "w1", Name: "Main", FileName: "wizard.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 512}},
		archives: map[string][]byte{"w1": e2eFomodArchive(t)},
	})
	doc := "lmm_collection: 1\nname: Wizard Pack\nrequires: [fomod]\nmods:\n" +
		"  - {source_id: fake, mod_id: " + e2eSearchInstallModID + ", version: \"2.0\"}\n" +
		"  - {source_id: fake, mod_id: wizard, version: \"1.0\", fomod: {config_sha256: " + strings.Repeat("ab", 32) +
		", steps: [{name: Size, groups: [{name: Texture, choices: [{name: Gone, idx: 0}]}]}]}}\n"
	importPath := filepath.Join(t.TempDir(), "pack.yaml")
	require.NoError(t, os.WriteFile(importPath, []byte(doc), 0o644))

	var report struct {
		Todos []struct {
			ModID       string `json:"mod_id"`
			Reason      string `json:"reason"`
			FomodReason string `json:"fomod_reason"`
			Action      string `json:"action"`
			Remedy      string `json:"remedy"`
		} `json:"todos"`
	}
	var wizard bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		clickWhenSettled(`.profile-picker__trigger`),
		chromedp.WaitVisible(`.profile-picker__menu`, chromedp.ByQuery),
		settleEffects(),
		clickWhenSettled(`.profile-picker__menu [data-action="import-collection"]`),
		chromedp.WaitVisible(`.modal[data-kind="import-collection"] [data-testid="import-url"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.SetUploadFiles(`.modal[data-kind="import-collection"] input[type="file"]`, []string{importPath}, chromedp.ByQuery),
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
		chromedp.Evaluate(`fetch("/api/v1/profiles/"+encodeURIComponent("Wizard Pack")+"/todos?game=`+f.Game.ID+`").then((r) => r.json())`, &report, awaitPromise),
	)

	assert.False(t, wizard, "the import never stops to ask")
	require.Len(t, report.Todos, 1)
	assert.Equal(t, "wizard", report.Todos[0].ModID)
	assert.Equal(t, "installer_questions", report.Todos[0].Reason)
	assert.Equal(t, "stale", report.Todos[0].FomodReason)
	assert.Equal(t, "answer_installer", report.Todos[0].Action)
	assert.Contains(t, report.Todos[0].Remedy, "installer changed since the curator recorded their choices; answer it to continue")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_AJobThatEndsAtOnceStillToasts: a job that ends
// before its running toast could appear (the toast waits a moment, in case
// its control comes back) still gets its outcome toast, and never a
// running one.
func TestE2E_ImportFeedback_AJobThatEndsAtOnceStillToasts(t *testing.T) {
	f, release := newE2EImportFixtureOf(t, e2eFailNone, 2)
	release()

	var done string
	var running int
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		openImportCollectionFromPicker(),
		chromedp.SetValue(`[data-testid="nexus-collection-ref"]`, "longcoll", chromedp.ByQuery),
		clickWhenSettled(`[data-testid="nexus-collection-import"]`),
		confirmImportPlan(),
		awaitJobsSettled(),
		chromedp.WaitVisible(`.toast--success`, chromedp.ByQuery),
		textContent(`.toast--success .toast__title`, &done),
		quietWindow(),
		chromedp.Evaluate(`document.querySelectorAll(".toast--running").length`, &running),
	)
	assert.Equal(t, "Collection import finished", done)
	assert.Zero(t, running, "no progress toast is left behind")
	assert.Empty(t, f.BrowserErrors())
}
