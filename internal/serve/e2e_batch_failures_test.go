package serve_test

// #533: a batch readout's failed items name themselves, their buttons act on
// (and are named for) their own mod, and the readout takes its own space in
// the card instead of lying over the card's controls. Driven in a real
// browser because every claim is about what is rendered, where, and what a
// click at a pixel actually reaches.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

const (
	e2eTrainerName = "TrainerSpells (What's Training Alternative)"
	e2eTrainerPage = "https://example.test/mods/trainer"
	e2eTrainerFile = "Mods/trainer.pak"
)

// newE2EFixtureWithTwoManualUpdates is the owner's #533 scenario: three
// updates, one the engine can apply (Okup) and two whose source refuses the
// download (Manual Up 1.0 -> 2.0 and TrainerSpells 1.2 -> 1.3).
func newE2EFixtureWithTwoManualUpdates(t *testing.T) e2eSearchFixture {
	t.Helper()
	f := newE2EFixtureWithAPartialUpdate(t)
	f.Src.addMod(e2eSearchSourceMod{
		mod: domain.Mod{ID: "trainer", SourceID: "fake", Name: e2eTrainerName, Version: "1.3", SourceURL: e2eTrainerPage},
		files: []domain.DownloadableFile{
			{ID: "t1", Name: "Main 1.2", FileName: "Trainer-1.2.zip", Version: "1.2", Category: "MAIN", Size: 16},
			{ID: "t2", Name: "Main 1.3", FileName: "Trainer-1.3.zip", Version: "1.3", Category: "MAIN", IsPrimary: true, Size: 16},
		},
	})
	f.Src.urlErrs["trainer"] = &source.ManualDownloadError{Reason: "the author has turned off API downloads"}

	mod := domain.Mod{ID: "trainer", SourceID: "fake", Name: e2eTrainerName, Version: "1.2", GameID: f.Game.ID, SourceURL: e2eTrainerPage}
	seedInstalledMod(t, f.Svc, f.Game, mod, true, map[string][]byte{e2eTrainerFile: []byte("v1.2 bytes")})
	row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "trainer", f.Game.ID, "default")
	require.NoError(t, err)
	row.FileIDs = []string{"t1"}
	require.NoError(t, f.Svc.SaveInstalledMod(t.Context(), row))
	require.NoError(t, f.Svc.NewProfileManager().AddMod(t.Context(), f.Game.ID, "default",
		domain.ModReference{SourceID: "fake", ModID: "trainer", Version: "1.2", FileIDs: []string{"t1"}}))
	_, err = f.Svc.DeployProfile(t.Context(), f.Game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	return f
}

// readoutLayout is how a card's finished readout sits among the card's own
// controls, as the page lays it out.
type readoutLayout struct {
	Controls []struct {
		Name    string  `json:"name"`
		Visible bool    `json:"visible"`
		Reaches bool    `json:"reaches"`
		Overlap bool    `json:"overlap"`
		Bottom  float64 `json:"bottom"`
	} `json:"controls"`
	InCard       bool `json:"inCard"`
	OwnRow       bool `json:"ownRow"`
	CardClipped  bool `json:"cardClipped"`
	DismissReach bool `json:"dismissReaches"`
	ButtonsReach bool `json:"buttonsReach"`
}

// readoutLayoutJS measures the finished readout inside card: whether the
// card's own controls (controlSel) are visible, unobscured (elementFromPoint
// at their centre returns them) and geometrically clear of it, whether the
// readout starts below all of them, and whether it sits inside the card.
func readoutLayoutJS(card, controlSel string) string {
	return `(() => {
	const card = document.querySelector(` + jsString(card) + `);
	const ro = card.querySelector(".job-progress");
	if (!ro) return null;
	const rect = (el) => el.getBoundingClientRect();
	const reaches = (el) => {
		const r = rect(el);
		const top = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
		return top === el || el.contains(top);
	};
	const hits = (a, b) => a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom;
	const roR = rect(ro);
	const controls = [...card.querySelectorAll(` + jsString(controlSel) + `)].map((el) => {
		const r = rect(el);
		return {
			name: el.getAttribute("data-action") ?? el.textContent.trim(),
			visible: r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== "hidden",
			reaches: reaches(el),
			overlap: hits(r, roR),
			bottom: r.bottom,
		};
	});
	const cardR = rect(card);
	const buttons = [...ro.querySelectorAll("button, a")];
	return {
		controls,
		// The readout has a line of its own: it starts below every control's
		// bottom edge, so it is not sharing the footer row with them.
		ownRow: controls.every((c) => c.bottom <= roR.top + 0.5),
		inCard: roR.left >= cardR.left - 0.5 && roR.right <= cardR.right + 0.5 && roR.top >= cardR.top - 0.5 && roR.bottom <= cardR.bottom + 0.5,
		cardClipped: card.scrollHeight > card.clientHeight + 1,
		dismissReaches: reaches(ro.querySelector(".job-progress__dismiss")),
		buttonsReach: buttons.every(reaches),
	};
})()`
}

// assertReadoutLayout fails unless the readout is inside the card, on a row
// of its own, clear of every control and every control is reachable.
func assertReadoutLayout(t *testing.T, where string, l *readoutLayout, wantControls ...string) {
	t.Helper()
	require.NotNil(t, l, "%s: no readout", where)
	assert.True(t, l.InCard, "%s: the readout sits inside the card", where)
	assert.True(t, l.OwnRow, "%s: the readout takes its own row below the card's controls", where)
	assert.False(t, l.CardClipped, "%s: the card grows to hold it", where)
	assert.True(t, l.ButtonsReach, "%s: every control inside the readout is reachable", where)
	assert.True(t, l.DismissReach, "%s: the dismiss button is reachable", where)
	seen := map[string]bool{}
	for _, c := range l.Controls {
		seen[c.Name] = true
		assert.True(t, c.Visible, "%s: %s is visible", where, c.Name)
		assert.True(t, c.Reaches, "%s: %s is the element at its own centre (nothing over it)", where, c.Name)
		assert.False(t, c.Overlap, "%s: %s does not intersect the readout", where, c.Name)
	}
	for _, name := range wantControls {
		assert.True(t, seen[name], "%s: %s was measured: %v", where, name, seen)
	}
}

// batchFailureView is the Updates card's readout as the page renders it.
type batchFailureView struct {
	Entries []struct {
		Mod      string `json:"mod"`
		Head     string `json:"head"`
		Reason   string `json:"reason"`
		Link     string `json:"link"`
		FromFile string `json:"fromFile"`
		FromText string `json:"fromText"`
	} `json:"entries"`
	Layout *readoutLayout `json:"layout"`
}

const updatesCardControls = `[data-action="check-updates"], [data-action="update-selected"], [data-action="update-all"]`

// batchFailureJS reads the Updates card's entries (text and accessible
// attributes) and its readout's layout.
var batchFailureJS = `(() => {
	const card = document.querySelector(".card--updates");
	const ro = card.querySelector(".job-progress");
	if (!ro) return null;
	const text = (el) => (el ? el.textContent.replace(/\s+/g, " ").trim() : "");
	const entries = [...ro.querySelectorAll('[data-testid="batch-failure"]')].map((e) => ({
		mod: e.getAttribute("data-mod"),
		head: text(e.querySelector(".batch-failure__head")),
		reason: text(e.querySelector(".batch-failure__reason")),
		link: e.querySelector("a.mod-page-link")?.getAttribute("aria-label") ?? "",
		fromFile: e.querySelector('[data-action="update-from-file"]')?.getAttribute("aria-label") ?? "",
		fromText: text(e.querySelector('[data-action="update-from-file"]')),
	}));
	return { entries, layout: ` + readoutLayoutJS(".card--updates", updatesCardControls) + ` };
})()`

// runOwnersBatch drives the owner's path to the finished readout and
// returns what the page shows.
func runOwnersBatch(t *testing.T, f e2eSearchFixture, width int64, out *batchFailureView) {
	t.Helper()
	f.runInBrowser(t,
		chromedp.EmulateViewport(width, 900),
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates [data-action="update-all"]`, chromedp.ByQuery),
		pollUntil(`/\(\d\)/.test(document.querySelector('.card--updates [data-action="update-all"]').textContent)`),
		clickWhenSettled(`.card--updates [data-action="update-all"]`),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
		pollUntil(`document.querySelectorAll('.card--updates [data-testid="batch-failure"] [data-action="update-from-file"]').length === 2`),
		chromedp.Evaluate(batchFailureJS, out),
	)
}

func entryFor(t *testing.T, v *batchFailureView, mod string) int {
	t.Helper()
	for i, e := range v.Entries {
		if e.Mod == mod {
			return i
		}
	}
	require.Failf(t, "no entry", "no failed-item entry for %s in %+v", mod, v.Entries)
	return -1
}

// TestE2E_BatchFailures_EachFailedItemNamesItselfAndItsButtonsActOnIt is the
// owner's exact case: "Update all (3)" with one applicable and two
// manual-download mods. Each failed item has a header naming it and the
// change that was attempted, an explanation naming the mod, and buttons whose
// accessible names carry the mod - and the second item's "Update from file…"
// completes THAT mod's update, leaving the first untouched.
func TestE2E_BatchFailures_EachFailedItemNamesItselfAndItsButtonsActOnIt(t *testing.T) {
	f := newE2EFixtureWithTwoManualUpdates(t)
	var view batchFailureView
	runOwnersBatch(t, f, 1280, &view)

	require.Len(t, view.Entries, 2, "one entry per failed item")
	manual := view.Entries[entryFor(t, &view, "fake:manualup")]
	trainer := view.Entries[entryFor(t, &view, "fake:trainer")]

	assert.Contains(t, manual.Head, "Manual Up")
	assert.Contains(t, manual.Head, "1.0 → 2.0", "the header carries the attempted change")
	assert.Contains(t, trainer.Head, e2eTrainerName)
	assert.Contains(t, trainer.Head, "1.2 → 1.3")
	assert.Contains(t, manual.Reason, "Manual Up", "the explanation names the mod, not 'this file'")
	assert.Contains(t, trainer.Reason, e2eTrainerName)
	assert.Contains(t, trainer.Reason, e2eSourceName, "and the source that refused")
	assert.NotContains(t, trainer.Reason, "this file")

	// Visible labels stay short; accessible names carry the mod and begin
	// with the visible text (WCAG 2.5.3).
	for _, e := range []struct{ entry, mod string }{{manual.Mod, "Manual Up"}, {trainer.Mod, e2eTrainerName}} {
		var fromFile, link e2eAXNode
		sel := fmt.Sprintf(`.card--updates [data-testid="batch-failure"][data-mod=%q] `, e.entry)
		f.runInBrowser(t,
			awaitSourceNamed(fmt.Sprintf(`.card--updates [data-testid="batch-failure"][data-mod=%q]`, e.entry)),
			accessibleNodeOf(sel+`[data-action="update-from-file"]`, &fromFile),
			accessibleNodeOf(sel+`a.mod-page-link`, &link),
		)
		assert.Equal(t, "button", fromFile.Role)
		assert.Contains(t, fromFile.Name, e.mod, "the button's accessible name says which mod")
		assert.Contains(t, fromFile.Name, "Update from file…", "and starts with its visible text")
		assert.Contains(t, link.Name, e.mod)
		assert.Contains(t, link.Name, "Open on "+e2eSourceName)
	}
	assert.Equal(t, "Update from file…", trainer.FromText, "the visible label stays short")

	// Layout: the readout is in the card's flow and clear of every control.
	assertReadoutLayout(t, "owner's case", view.Layout, "check-updates", "update-all", "update-selected")

	// The SECOND item's button completes the second mod.
	archive := filepath.Join(t.TempDir(), "Trainer-1.3.zip")
	require.NoError(t, os.WriteFile(archive, e2eZipWith(e2eTrainerFile, "v1.3 bytes"), 0o644))
	f.runInBrowser(t,
		clickWhenSettled(fmt.Sprintf(`.card--updates [data-testid="batch-failure"][data-mod=%q] [data-action="update-from-file"]`, "fake:trainer")),
		chromedp.SetUploadFiles(e2eFromFileInput, []string{archive}, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="update_from_archive"] .plan[data-match="advertised"]`, chromedp.ByQuery),
		clickWhenSettled(`.modal [data-action="confirm"]`),
		waitGone(`.modal`),
	)
	require.Eventually(t, func() bool {
		row, err := f.Svc.GetInstalledMod(t.Context(), "fake", "trainer", f.Game.ID, "default")
		return err == nil && row.Version == "1.3"
	}, e2eTimeout, e2ePollInterval, "the second item's button updated the second mod")
	assert.Equal(t, "1.0", manualUpRow(t, f).Version, "the first mod was not touched")
	assertNoUncaughtErrors(t, f.BrowserErrors())
}

// TestE2E_BatchFailures_LayoutHoldsInEveryFace is #521's stress pass over the
// owner's readout, at 1280 and 720: in every face the readout is inside the
// card, no control intersects it, and a click at each control's centre
// reaches that control.
func TestE2E_BatchFailures_LayoutHoldsInEveryFace(t *testing.T) {
	f := newE2EFixtureWithTwoManualUpdates(t)

	forEachFace(t, f.Ctx, func(t *testing.T, face e2eFace) {
		for _, width := range []int64{1280, 720} {
			var view batchFailureView
			runOwnersBatch(t, f, width, &view)
			f.runInBrowser(t, faceInEffect(face))

			require.Len(t, view.Entries, 2, "%dpx", width)
			assertReadoutLayout(t, fmt.Sprintf("%dpx", width), view.Layout, "check-updates", "update-all", "update-selected")
		}
		assertNoUncaughtErrors(t, f.BrowserErrors())
	})
}

// TestE2E_VerifyFixResult_NamesItsModsAndKeepsTheHealthCardUsable is #533 for
// the other batch kind that reports per-item outcomes: a finished verify
// --fix on the Health card. Each row names its mod and its link's accessible
// name carries the mod, the readout takes its own row in the card, and the
// card's footer (Re-verify, Repair all) stays visible and unobscured - at
// 1280 and 720 in every layout face.
func TestE2E_VerifyFixResult_NamesItsModsAndKeepsTheHealthCardUsable(t *testing.T) {
	// A repair is spent once it has run, so every (face, width) gets its own
	// fixture rather than re-running one that has nothing left to repair.
	for _, face := range e2eLayoutFaces {
		for _, width := range []int64{1280, 720} {
			t.Run(fmt.Sprintf("%s/%d", face.name, width), func(t *testing.T) {
				f := newE2EFixtureWithRepairableMods(t)
				useFace(t, f.Ctx, face)
				var view *e2eVerifyResultView
				var layout *readoutLayout
				var link e2eAXNode
				f.runInBrowser(t, chromedp.EmulateViewport(width, 900))
				f.runInBrowser(t, repairAllFromHealthCard(f)...)
				f.runInBrowser(t,
					faceInEffect(face),
					pollUntil(`document.querySelector('.card--health [data-testid="verify-fix-result"] a.mod-page-link')?.textContent.includes("E2E Search Source")`),
					chromedp.Evaluate(resultRowsJS(".card--health"), &view),
					chromedp.Evaluate(readoutLayoutJS(".card--health", `.card__actions > button`), &layout),
					accessibleNodeOf(`.card--health [data-mod="stuck"] a.mod-page-link`, &link),
				)
				require.NotNil(t, view, "%dpx", width)
				byMod := map[string]e2eVerifyResultRow{}
				for _, r := range view.Rows {
					byMod[r.Mod] = r
				}
				assert.Contains(t, byMod["stuck"].Text, "Stuck Mod", "%dpx: the entry names its mod", width)
				assert.Contains(t, byMod["cached"].Text, "Cached Mod", "%dpx", width)
				assert.Contains(t, link.Name, "Stuck Mod", "%dpx: the link's accessible name says which mod", width)
				assert.Contains(t, link.Name, "Open on "+e2eSourceName, "%dpx", width)
				assertReadoutLayout(t, fmt.Sprintf("%dpx", width), layout, "Re-verify", "repair-all")
				assertNoUncaughtErrors(t, f.BrowserErrors())
			})
		}
	}
}

// TestE2E_BatchFailures_AnItemThatFailedForAnyReasonNamesItself: a failed
// item with no download way out - a plain source error - still gets its
// header (name and attempted change) and the engine's own text, rather than
// vanishing into the tally.
func TestE2E_BatchFailures_AnItemThatFailedForAnyReasonNamesItself(t *testing.T) {
	f := newE2EFixture(t)

	var entries []struct {
		Head   string `json:"head"`
		Reason string `json:"reason"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.mission-control[data-hydrated="true"]`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			const real = window.fetch.bind(window);
			window.fetch = (input, init) => {
				const url = typeof input === "string" ? input : (input && input.url) || "";
				if (url.includes("/api/v1/jobs/fake-job-533")) {
					return Promise.resolve(new Response(JSON.stringify({
						id: "fake-job-533", kind: "updates", state: "succeeded",
						result: { applied: [], failed: [
							{ mod: "fake:plain", name: "Plain Failure", from_version: "1.0", to_version: "1.1", error: "fetching mod: source unavailable" },
							{ mod: "fake:nover", name: "No Versions", error: "boom" },
						] },
					}), { status: 200, headers: { "Content-Type": "application/json" } }));
				}
				return real(input, init);
			};
		})()`, nil),
		chromedp.Evaluate(`(async () => {
			const { render, h } = await import("/static/app/render.js");
			const { JobProgress } = await import("/static/app/components/jobprogress.js");
			const container = document.createElement("div");
			document.body.appendChild(container);
			window.__c533 = container;
			render(h(JobProgress, {
				jobID: "fake-job-533",
				summary: { id: "fake-job-533", kind: "updates", state: "succeeded" },
				frame: {}, actions: null, onDismiss: () => {},
			}), container);
		})()`, nil, awaitPromise),
		pollUntil(`window.__c533.querySelectorAll('[data-testid="batch-failure"]').length === 2`),
		chromedp.Evaluate(`[...window.__c533.querySelectorAll('[data-testid="batch-failure"]')].map((e) => ({
			head: e.querySelector(".batch-failure__head").textContent.replace(/\s+/g, " ").trim(),
			reason: e.querySelector(".batch-failure__reason")?.textContent.trim() ?? "",
		}))`, &entries),
	)
	require.Len(t, entries, 2)
	assert.Equal(t, "Plain Failure, 1.0 → 1.1", entries[0].Head)
	assert.Equal(t, "fetching mod: source unavailable", entries[0].Reason)
	assert.Equal(t, "No Versions", entries[1].Head, "no change is invented when the check carried none")
	assert.Equal(t, "boom", entries[1].Reason)
	assertNoUncaughtErrors(t, f.BrowserErrors())
}
