package serve_test

// The browser scenarios for select-all on the multi-select surfaces (#434).
//
// A browser test rather than a Go one for a specific reason: the header
// box's third state is `indeterminate`, a DOM PROPERTY a renderer sets and
// markup cannot express at all, so nothing short of running the application
// can show whether a partial selection reads as one.

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
)

// e2eSelectAllState is what selectAllStateJS reads back.
type e2eSelectAllState struct {
	Checked       bool   `json:"checked"`
	Indeterminate bool   `json:"indeterminate"`
	Label         string `json:"label"`
	Selected      int    `json:"selected"`
	BatchText     string `json:"batchText"`
}

// selectAllStateJS reads the library header's select-all box and the batch
// bar it drives in one evaluation.
const selectAllStateJS = `(() => {
	const box = document.querySelector('[data-testid="select-all"]');
	const bar = document.querySelector(".batch-bar");
	return {
		checked: box.checked,
		indeterminate: box.indeterminate,
		label: box.getAttribute("aria-label"),
		selected: document.querySelectorAll(".mod-row--selected").length,
		batchText: bar ? bar.querySelector(".batch-bar__count").textContent.trim() : "",
	};
})()`

// TestE2E_LibrarySelectAll_TakesTheWholeTableAndReadsPartialSelections is
// issue 434.
//
// Every multi-select surface in this UI made the user tick each row by hand;
// there was no select-all anywhere. The header box takes everything
// currently visible, and - the part only a browser can show - reads back as
// checked, unchecked, or INDETERMINATE, which is a DOM property with no
// markup spelling at all.
func TestE2E_LibrarySelectAll_TakesTheWholeTableAndReadsPartialSelections(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var empty, all, partial e2eSelectAllState
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`[data-testid="select-all"]`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 3`),
		chromedp.Evaluate(selectAllStateJS, &empty),
		chromedp.Click(`[data-testid="select-all"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(selectAllStateJS, &all),
	)

	assert.False(t, empty.Checked)
	assert.False(t, empty.Indeterminate, "nothing selected is not a partial selection")
	assert.Contains(t, empty.Label, "3", "the accessible name counts what it would actually select")

	assert.Equal(t, 3, all.Selected)
	assert.True(t, all.Checked)
	assert.False(t, all.Indeterminate)
	assert.Contains(t, all.BatchText, "3 of 3 selected",
		"the batch bar states the size of what is about to happen, so \"Update 40 mods\" is never a surprise")

	// Untick one row: the header box must read as PARTIAL, not as off.
	f.runInBrowser(t,
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".mod-row"))
			.find((r) => r.textContent.includes("Alpha Mod"))
			.querySelector("td.col--select input").click();`, nil),
		settleEffects(),
		chromedp.Evaluate(selectAllStateJS, &partial),
	)
	assert.Equal(t, 2, partial.Selected)
	assert.True(t, partial.Indeterminate, "a partial selection is neither checked nor unchecked")

	// From a partial selection the box completes it, and from a full one it
	// clears - the ordinary two-press cycle.
	var completed, cleared e2eSelectAllState
	f.runInBrowser(t,
		chromedp.Click(`[data-testid="select-all"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(selectAllStateJS, &completed),
		chromedp.Click(`[data-testid="select-all"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(selectAllStateJS, &cleared),
	)
	assert.Equal(t, 3, completed.Selected)
	assert.Zero(t, cleared.Selected)
	assert.False(t, cleared.Checked)
	assert.False(t, cleared.Indeterminate)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibrarySelectAll_SkipsTheSteamManagedRow is issue 434's exclusion
// rule: a row the batch actions cannot apply to is skipped rather than
// selected-and-then-refused. lmm cannot enable or disable a Steam Workshop
// item at all (#379), and the row's own enabled checkbox has been disabled
// with that reason since then - a select-all that swept it in would walk
// straight back into the defect that gate exists to prevent.
func TestE2E_LibrarySelectAll_SkipsTheSteamManagedRow(t *testing.T) {
	f := newE2EWorkshopFixture(t)
	seedWorkshopManagedMod(t, f)

	var state e2eSelectAllState
	var selectedNames []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		chromedp.Click(`[data-testid="select-all"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(selectAllStateJS, &state),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".mod-row--selected"))
			.map((r) => r.querySelector(".mod-row__name").textContent.trim())`, &selectedNames),
	)

	assert.Equal(t, []string{"Managed Mod"}, selectedNames,
		"the Steam-managed row is skipped, not selected then refused")
	assert.True(t, state.Checked,
		"and with every SELECTABLE row taken the box is checked, not stuck half-way forever")
	assert.False(t, state.Indeterminate)
	assert.Contains(t, state.BatchText, "1 of 2 selected",
		"the count is honest about the row it left behind")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibrarySelectAll_HonoursTheFilterAndTheKeyboard is the other half
// of issue 434: "everything currently VISIBLE" means after the filter and
// the search, not the whole library - and the binding shortcuts.js documents
// has to actually do the same thing the box does.
func TestE2E_LibrarySelectAll_HonoursTheFilterAndTheKeyboard(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	// The sample seeds Zebra and Alpha enabled, Middle disabled.
	var filtered e2eSelectAllState
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 3`),
		chromedp.SetValue(`.library__toolbar select[name="filter"]`, "enabled", chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		chromedp.Click(`[data-testid="select-all"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(selectAllStateJS, &filtered),
	)
	assert.Equal(t, 2, filtered.Selected,
		"select-all selects what is in view, never the rows a filter is hiding")

	// The keyboard binding, from the page rather than from any control,
	// clears it again - the same toggle the box is. The blur matters: the
	// filter <select> above still holds focus, and a keystroke inside a form
	// control is deliberately not a command.
	var afterKey e2eSelectAllState
	f.runInBrowser(t,
		chromedp.Blur(`.library__toolbar select[name="filter"]`, chromedp.ByQuery),
		chromedp.KeyEvent("a"),
		settleEffects(),
		chromedp.Evaluate(selectAllStateJS, &afterKey),
	)
	assert.Zero(t, afterKey.Selected, "the binding toggles the same selection the header box does")

	// And it stays out of the omnibar: "a" is a character someone is
	// entitled to type into a search field.
	var afterTyping struct {
		Selected int    `json:"selected"`
		Query    string `json:"query"`
	}
	f.runInBrowser(t,
		chromedp.Focus(`.omnibar`, chromedp.ByQuery),
		chromedp.KeyEvent("a"),
		settleEffects(),
		chromedp.Evaluate(`({
			selected: document.querySelectorAll(".mod-row--selected").length,
			query: document.querySelector(".omnibar").value,
		})`, &afterTyping),
	)
	assert.Equal(t, "a", afterTyping.Query, "the character reaches the field")
	assert.Zero(t, afterTyping.Selected, "and selects nothing")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_UpdatesCardSelectAll_SkipsTheRowsWithNoCheckbox is issue 434 on
// the second multi-select surface. The Updates card gives an EXTERNAL row no
// checkbox at all (#269: ApplyUpdateBatch declines it outright), so its own
// select-all must not reach one either.
func TestE2E_UpdatesCardSelectAll_SkipsTheRowsWithNoCheckbox(t *testing.T) {
	f := newE2EWorkshopFixture(t)
	seedWorkshopManagedMod(t, f)
	setWorkshopCatalogVersion(t, f, e2eWorkshopFileID, e2eWorkshopNewManifest)
	setWorkshopCatalogVersion(t, f, "managed-1", "2.0")

	var state struct {
		Rows     int    `json:"rows"`
		Boxes    int    `json:"boxes"`
		Selected int    `json:"selected"`
		Label    string `json:"label"`
		Button   string `json:"button"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".card--updates .card__row").length === 2`),
		chromedp.Click(`.card--updates [data-testid="select-all"]`, chromedp.ByQuery),
		settleEffects(),
		chromedp.Evaluate(`({
			rows: document.querySelectorAll(".card--updates .card__row").length,
			boxes: document.querySelectorAll('.card--updates .card__row input[type="checkbox"]').length,
			selected: document.querySelectorAll('.card--updates .card__row input[type="checkbox"]:checked').length,
			label: document.querySelector('.card--updates [data-testid="select-all"]').getAttribute("aria-label"),
			button: document.querySelector('.card--updates [data-action="update-selected"]').textContent.trim(),
		})`, &state),
	)

	assert.Equal(t, 2, state.Rows, "both updates are reported")
	assert.Equal(t, 1, state.Boxes, "only one of them is applicable, so only one has a checkbox")
	assert.Equal(t, 1, state.Selected, "and select-all takes exactly that one")
	assert.Contains(t, state.Label, "1")
	assert.Contains(t, state.Button, "1",
		"the button states how many mods it is about to update")
	assert.Empty(t, f.BrowserErrors())
}
