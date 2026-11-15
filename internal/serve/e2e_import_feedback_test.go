package serve_test

// #655: a collection import started from the "Import collection…" modal
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
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eLongImportItems is how many items the long collection carries: each
// emits at least two events, so a failed import emits more than the job
// ring's 1024.
const e2eLongImportItems = 600

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

// newE2ELongImportFixture is a Premium NexusMods double over "longcoll", a
// collection of e2eLongImportItems real downloads, every one held until
// release is called. With fail, two items' downloads are refused.
func newE2ELongImportFixture(t *testing.T, fail bool) (f e2eFixture, release func()) {
	t.Helper()
	sandboxE2EEnv(t)
	src := newE2ESearchSource(t, "nexusmods")
	gate := make(chan struct{})
	src.urlGate = gate
	src.urlErrs = map[string]error{}
	mods := make([]string, 0, e2eLongImportItems)
	for i := range e2eLongImportItems {
		id := fmt.Sprintf("%d", 1000+i)
		fileID := id + "0"
		src.addMod(e2eSearchSourceMod{
			mod:     domain.Mod{ID: id, SourceID: "nexusmods", Name: "Item " + id, Version: "1.0"},
			files:   []domain.DownloadableFile{{ID: fileID, Name: "Main", FileName: "item" + id + ".zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 16}},
			members: map[string]string{fileID: "item" + id + ".esp"},
		})
		mods = append(mods, fmt.Sprintf(`{"Name": "Item %s", "Version": "1.0", "SourceType": "nexus", "ModID": "%s", "FileID": "%s", "UpdatePolicy": "exact"}`, id, id, fileID))
	}
	if fail {
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
// first item count.
func awaitRunningToast(title, progress *string) chromedp.Tasks {
	return chromedp.Tasks{
		chromedp.WaitVisible(`.toast--running`, chromedp.ByQuery),
		pollUntil(`document.querySelector('.toast--running [data-testid="toast-progress"]')?.textContent.includes("of 600") === true`),
		textContent(`.toast--running .toast__title`, title),
		textContent(`.toast--running [data-testid="toast-progress"]`, progress),
	}
}

// failureToastJS reads the failure toast: its title, its per-mod lines and
// its action buttons, with the gap between the first two.
const failureToastJS = `(() => {
	const toast = document.querySelector(".toast--failure");
	const buttons = [...toast.querySelectorAll(".toast__actions button")];
	const a = buttons[0]?.getBoundingClientRect();
	const b = buttons[1]?.getBoundingClientRect();
	const sameRow = a && b && Math.abs(a.top - b.top) < 2;
	return {
		title: toast.querySelector(".toast__title").textContent,
		items: [...toast.querySelectorAll(".item-failures__item")].map((li) => li.textContent.replace(/\s+/g, " ").trim()),
		detail: toast.querySelector(".toast__detail")?.textContent ?? "",
		buttons: buttons.map((el) => el.textContent.trim()),
		gap: !a || !b ? -1 : sameRow ? b.left - a.right : b.top - a.bottom,
	};
})()`

type e2eFailureToast struct {
	Title   string   `json:"title"`
	Items   []string `json:"items"`
	Detail  string   `json:"detail"`
	Buttons []string `json:"buttons"`
	Gap     float64  `json:"gap"`
}

// assertCollectionFailureToast holds the failure toast to issue 655's four
// points: a human title, one line per failed mod, and two spaced actions.
func assertCollectionFailureToast(t *testing.T, got e2eFailureToast) {
	t.Helper()
	assert.Equal(t, "Collection import failed", got.Title)
	assert.Equal(t, []string{
		"Item " + e2eLongImportFirstFailed + ": getting download URL: " + errE2ELongImportBadZip.Error(),
		"Item " + e2eLongImportLastFailed + ": getting download URL: " + errE2ELongImportGone.Error(),
	}, got.Items, "one line per failed mod: its name, then why")
	assert.Empty(t, got.Detail, "the joined engine text is not the toast's body")
	assert.Equal(t, []string{"Back to import", "Show in activity"}, got.Buttons)
	assert.GreaterOrEqual(t, got.Gap, 4.0, "the two actions are two spaced controls")
}

// TestE2E_ImportFeedback_ProgressThenAStickyFailure is the owner's report:
// the import shows its progress the moment it is confirmed and while it
// runs, and its failure - after more events than the job ring holds - stays
// on screen, readable, until dismissed.
func TestE2E_ImportFeedback_ProgressThenAStickyFailure(t *testing.T) {
	f, release := newE2ELongImportFixture(t, true)

	var title, progress string
	var toast e2eFailureToast
	var running, toastsLeft int
	var jobs string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		startLongImportFromModal(),
		awaitRunningToast(&title, &progress),
		chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
		chromedp.WaitVisible(`.toast--failure`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll(".toast--running").length`, &running),
		chromedp.Evaluate(failureToastJS, &toast),
		quietWindow(),
		chromedp.Evaluate(`document.querySelectorAll(".toast--failure").length`, &toastsLeft),
		chromedp.Evaluate(`fetch("/api/v1/jobs").then((r) => r.text())`, &jobs, awaitPromise),
	)

	assert.Equal(t, "Importing collection…", title)
	assert.Contains(t, progress, "of 600", "the import counts its items")
	assert.Zero(t, running, "the outcome replaces the progress")
	assertCollectionFailureToast(t, toast)
	assert.Equal(t, 1, toastsLeft, "a failure stays until dismissed")
	assert.Contains(t, jobs, `"dropped_events"`, "the job outran its event ring, and its end still arrived")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_ShowInActivityOpensTheJob: the toast's "Show in
// activity" opens the tray on THIS job, named in words, with one line per
// failed mod and the engine's whole text under Details.
func TestE2E_ImportFeedback_ShowInActivityOpensTheJob(t *testing.T) {
	f, release := newE2ELongImportFixture(t, true)
	release()

	var kind, failure, raw string
	var expanded bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		startLongImportFromModal(),
		chromedp.WaitVisible(`.toast--failure`, chromedp.ByQuery),
		clickWhenSettled(`.toast--failure [data-action="toast-activity"]`),
		chromedp.WaitVisible(`.tray__row[data-state="failed"] .item-failures`, chromedp.ByQuery),
		textContent(`.tray__row[data-state="failed"] .tray__kind`, &kind),
		textContent(`.tray__row[data-state="failed"] .tray__failure`, &failure),
		chromedp.Evaluate(`document.querySelector('.tray__row[data-state="failed"] .tray__summary').getAttribute("aria-expanded") === "true"`, &expanded),
		clickWhenSettled(`.tray__row[data-state="failed"] .raw-error__summary`),
		textContent(`.tray__row[data-state="failed"] .raw-error__text`, &raw),
	)

	assert.Equal(t, "Collection import", kind)
	assert.True(t, expanded, "the tray opens on this job")
	assert.Contains(t, failure, "2 mods failed")
	assert.Contains(t, failure, "Item "+e2eLongImportFirstFailed+": getting download URL: "+errE2ELongImportBadZip.Error())
	assert.Contains(t, raw, "nexusmods:"+e2eLongImportLastFailed+": getting download URL", "the engine's own text is kept in the details")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_BackToImportShowsTheFailure: "Back to import"
// opens the import dialog with the failure above its forms, not an empty
// form.
func TestE2E_ImportFeedback_BackToImportShowsTheFailure(t *testing.T) {
	f, release := newE2ELongImportFixture(t, true)
	release()

	var readout string
	var refInput bool
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		startLongImportFromModal(),
		chromedp.WaitVisible(`.toast--failure`, chromedp.ByQuery),
		clickWhenSettled(`.toast--failure [data-action="toast-action"]`),
		chromedp.WaitVisible(`.modal[data-kind="import-collection"] [data-testid="import-failure"]`, chromedp.ByQuery),
		textContent(`[data-testid="import-failure"]`, &readout),
		chromedp.Evaluate(`document.querySelector('.modal[data-kind="import-collection"] [data-testid="nexus-collection-ref"]') !== null`, &refInput),
	)

	assert.Contains(t, readout, "The last import failed")
	assert.Contains(t, readout, "Item "+e2eLongImportFirstFailed)
	assert.Contains(t, readout, "Item "+e2eLongImportLastFailed)
	assert.True(t, refInput, "the import's own forms are there to try again")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_ImportFeedback_SuccessShowsProgressThenFinished: a long import
// that succeeds shows its progress too, then says it finished, in words.
func TestE2E_ImportFeedback_SuccessShowsProgressThenFinished(t *testing.T) {
	f, release := newE2ELongImportFixture(t, false)

	var title, progress, done string
	var buttons []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		startLongImportFromModal(),
		awaitRunningToast(&title, &progress),
		chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
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
			f, release := newE2ELongImportFixture(t, true)
			var title, progress string
			var toast e2eFailureToast
			f.runInBrowser(t,
				door(f),
				confirmImportPlan(),
				awaitRunningToast(&title, &progress),
				chromedp.ActionFunc(func(context.Context) error { release(); return nil }),
				chromedp.WaitVisible(`.toast--failure`, chromedp.ByQuery),
				chromedp.Evaluate(failureToastJS, &toast),
			)
			assert.Equal(t, "Importing collection…", title)
			assertCollectionFailureToast(t, toast)
			assert.Empty(t, f.BrowserErrors())
		})
	}
}
