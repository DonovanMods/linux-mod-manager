package serve_test

// The browser scenarios for select-all on the multi-select surfaces (#434).
//
// A browser test rather than a Go one for a specific reason: the header
// box's third state is `indeterminate`, a DOM PROPERTY a renderer sets and
// markup cannot express at all, so nothing short of running the application
// can show whether a partial selection reads as one.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	for _, label := range []string{empty.Label, all.Label} {
		assert.True(t, strings.HasPrefix(label, "Select"),
			"the accessible name starts with the word the box shows, in either state (WCAG 2.5.3): %q", label)
	}
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

	// Nor does it reach past the slide-over. That panel is a route
	// annotation rather than a modal - it is not in state.modal, so it needs
	// its own guard - and a selection quietly changing behind a panel you are
	// reading is a surprise, not a shortcut.
	var behindPanel int
	f.runInBrowser(t,
		chromedp.Blur(`.omnibar`, chromedp.ByQuery),
		chromedp.Navigate(f.SlideOverPath("fake", "a")),
		chromedp.WaitVisible(`.slide-over__nav`, chromedp.ByQuery),
		settleEffects(),
		chromedp.KeyEvent("a"),
		settleEffects(),
		chromedp.Evaluate(`document.querySelectorAll(".mod-row--selected").length`, &behindPanel),
	)
	assert.Zero(t, behindPanel, "the binding is off while the slide-over covers the table")
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
	assert.True(t, strings.HasPrefix(state.Label, "Select all"),
		"with every applicable row taken, the name still starts with the visible \"Select all\" (WCAG 2.5.3): %q", state.Label)
	assert.Contains(t, state.Button, "1",
		"the button states how many mods it is about to update")
	assert.Empty(t, f.BrowserErrors())
}

// typeIntoOmnibar narrows the library by text the way a user does: focus the
// top bar's field and type. It does not press Enter, which would fan the
// query out to the sources instead.
func typeIntoOmnibar(text string) chromedp.Action {
	return chromedp.SendKeys(`.omnibar`, text, chromedp.ByQuery)
}

// TestE2E_LibrarySelectAll_HonoursTheSearch is the search half of issue
// 434's "everything currently visible": the omnibar narrows the table just
// as the Filter does, and select-all takes what that leaves - then, with the
// search cleared, the rows it did not take read the box as partial.
func TestE2E_LibrarySelectAll_HonoursTheSearch(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var searched e2eSelectAllState
	var names []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 3`),
		typeIntoOmnibar("alpha"),
		pollUntil(`document.querySelectorAll(".mod-row").length === 1`),
		chromedp.Evaluate(`document.querySelector('[data-testid="select-all"]').click()`, nil),
		settleEffects(),
		chromedp.Evaluate(selectAllStateJS, &searched),
		chromedp.Evaluate(`Array.from(document.querySelectorAll(".mod-row--selected"))
			.map((r) => r.querySelector(".mod-row__name").textContent.trim())`, &names),
	)
	assert.Equal(t, []string{"Alpha Mod"}, names, "select-all takes only what the search left in view")
	assert.True(t, searched.Checked)
	assert.Contains(t, searched.BatchText, "1 of 1 selected")

	var cleared e2eSelectAllState
	f.runInBrowser(t,
		chromedp.Focus(`.omnibar`, chromedp.ByQuery),
		chromedp.KeyEvent(kb.Escape),
		pollUntil(`document.querySelectorAll(".mod-row").length === 3`),
		settleEffects(),
		chromedp.Evaluate(selectAllStateJS, &cleared),
	)
	assert.Equal(t, 1, cleared.Selected, "clearing the search does not widen the selection")
	assert.True(t, cleared.Indeterminate, "and the full table reads it as partial")
	assert.Contains(t, cleared.BatchText, "1 of 3 selected")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibrarySelectAll_IsRefusedWithNothingItCouldTake is issue 434's
// empty case. With only Steam-managed rows in view the box used to be live,
// unchecked and named "Select all 0 mods in view" - a checkbox that would
// not tick, saying so only in a tooltip. The Updates card's box has always
// been disabled in its equivalent state; the library's now agrees.
func TestE2E_LibrarySelectAll_IsRefusedWithNothingItCouldTake(t *testing.T) {
	f := newE2EWorkshopFixture(t)
	seedWorkshopManagedMod(t, f)

	var box struct {
		Disabled bool   `json:"disabled"`
		Title    string `json:"title"`
	}
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 2`),
		typeIntoOmnibar("Sample Workshop"),
		pollUntil(`document.querySelectorAll(".mod-row").length === 1`),
		chromedp.Evaluate(`(() => {
			const b = document.querySelector('[data-testid="select-all"]');
			return {disabled: b.disabled, title: b.getAttribute("title") ?? ""};
		})()`, &box),
	)
	assert.True(t, box.Disabled, "with nothing in view it could take, the box is refused, not silently inert")
	assert.Contains(t, box.Title, "Steam", "and it still says why")

	// The keyboard binding has nothing to take either.
	var selected int
	f.runInBrowser(t,
		chromedp.Blur(`.omnibar`, chromedp.ByQuery),
		chromedp.KeyEvent("a"),
		settleEffects(),
		chromedp.Evaluate(`document.querySelectorAll(".mod-row--selected").length`, &selected),
	)
	assert.Zero(t, selected)
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibrarySelection_IsAnnounced is the other half of issue 418/434's
// a11y review: pressing "a" changed the selection with nothing announced.
//
// The region is asserted to be the SAME element before and after, because
// that is what makes the announcement happen at all: a live region that is
// inserted together with its text - the batch bar, which mounts on the first
// selection - is not reliably read out.
func TestE2E_LibrarySelection_IsAnnounced(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var before, taken, clearedText string
	var role string
	var sameElement bool
	regionJS := `document.querySelector('[data-testid="selection-status"]').textContent.trim()`
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 3`),
		settleEffects(),
		chromedp.Evaluate(`(() => {
			window.__selectionRegion = document.querySelector('[data-testid="selection-status"]');
			return window.__selectionRegion.getAttribute("role");
		})()`, &role),
		chromedp.Evaluate(regionJS, &before),
		chromedp.KeyEvent("a"),
		pollUntil(`document.querySelectorAll(".mod-row--selected").length === 3`),
		chromedp.Evaluate(regionJS, &taken),
		chromedp.KeyEvent("a"),
		pollUntil(`document.querySelectorAll(".mod-row--selected").length === 0`),
		chromedp.Evaluate(regionJS, &clearedText),
		chromedp.Evaluate(`window.__selectionRegion === document.querySelector('[data-testid="selection-status"]')`, &sameElement),
	)

	require.Equal(t, "status", role, "a polite live region")
	assert.Equal(t, "No mods selected", before)
	assert.Equal(t, "3 of 3 selected", taken, "taking the selection is announced with its size")
	assert.Equal(t, "No mods selected", clearedText, "and so is clearing it")
	assert.True(t, sameElement, "one persistent region, not one mounted with the batch bar")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_LibrarySelection_StatusSpeaksOnlyWhenTheSelectionChanges is the
// re-review's F4: the always-present status region (issue 434) announced
// "0 of 1 selected" on every keystroke of a search, with the selection
// itself untouched. It speaks for a selection change and nothing else.
func TestE2E_LibrarySelection_StatusSpeaksOnlyWhenTheSelectionChanges(t *testing.T) {
	f := newE2EFixtureWithLibrarySample(t)

	var whileTyping, afterSelecting []string
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.library__table`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".mod-row").length === 3`),
		chromedp.Evaluate(selectLibraryRowJS("Alpha Mod"), nil),
		settleEffects(),
		chromedp.Evaluate(`(() => {
			window.__statusLog = [];
			const el = document.querySelector('[data-testid="selection-status"]');
			new MutationObserver(() => window.__statusLog.push(el.textContent.trim()))
				.observe(el, {subtree: true, childList: true, characterData: true});
		})()`, nil),
		typeIntoOmnibar("m"), settleEffects(),
		typeIntoOmnibar("i"), settleEffects(),
		typeIntoOmnibar("d"), settleEffects(),
		pollUntil(`document.querySelectorAll(".mod-row").length === 1`),
		chromedp.Evaluate(`window.__statusLog.slice()`, &whileTyping),
		chromedp.Evaluate(selectLibraryRowJS("Middle Mod"), nil),
		settleEffects(),
		chromedp.Evaluate(`window.__statusLog.slice()`, &afterSelecting),
	)
	assert.Empty(t, whileTyping, "narrowing the table is not a selection change, so the region stays quiet")
	require.NotEmpty(t, afterSelecting, "a selection change is still announced")
	assert.Contains(t, afterSelecting[len(afterSelecting)-1], "selected")
	assert.Empty(t, f.BrowserErrors())
}

// selectLibraryRowJS ticks the batch-selection box of the row named.
func selectLibraryRowJS(name string) string {
	return fmt.Sprintf(`Array.from(document.querySelectorAll(".mod-row"))
		.find((r) => r.textContent.includes(%q))
		.querySelector("td.col--select input").click();`, name)
}

// updatesCardStateJS reads the Updates card's selection surfaces together:
// which rows are ticked, and what the batch button says and allows.
const updatesCardStateJS = `(() => {
	const card = document.querySelector(".card--updates");
	if (!card) return null;
	const button = card.querySelector('[data-action="update-selected"]');
	return {
		rows: card.querySelectorAll(".card__row").length,
		ticked: Array.from(card.querySelectorAll(".card__row input[type=checkbox]:checked"))
			.map((b) => b.closest(".card__row").querySelector(".card__row-name").textContent.trim()),
		button: button ? button.textContent.trim() : "",
		disabled: button ? button.disabled : null,
	};
})()`

// e2eUpdatesCardState is updatesCardStateJS's shape.
type e2eUpdatesCardState struct {
	Rows     int      `json:"rows"`
	Ticked   []string `json:"ticked"`
	Button   string   `json:"button"`
	Disabled *bool    `json:"disabled"`
}

// TestE2E_UpdatesCardSelection_FollowsTheRowsItWasMadeFrom is issue 434's
// count held to the rows it counts. The card's selection is kept as mod
// keys, and a key outlives its row: update a ticked mod from anywhere else
// and the card loses the row at its next refresh, but the key stayed - so the
// button kept counting it ("Update 2 mods" over one row) and the batch
// planned a mod with nothing left to update.
func TestE2E_UpdatesCardSelection_FollowsTheRowsItWasMadeFrom(t *testing.T) {
	f := newE2EFixtureWithALockedAndAnUnlockedUpdate(t)

	var both e2eUpdatesCardState
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".card--updates .card__row").length === 2`),
		chromedp.Evaluate(`document.querySelector('.card--updates [data-testid="select-all"]').click()`, nil),
		settleEffects(),
		chromedp.Evaluate(updatesCardStateJS, &both),
	)
	require.Equal(t, "Update 2 mods", both.Button)

	// Great Gloves is updated from its LIBRARY row, not from the card.
	var after e2eUpdatesCardState
	f.runInBrowser(t,
		chromedp.Evaluate(`document.querySelector('.mod-row[data-mod="fake:gloves"] [data-action="row-update"]').click()`, nil),
		chromedp.WaitVisible(`.modal[data-kind="updates"] [data-action="confirm"]:not([disabled])`, chromedp.ByQuery),
		chromedp.Click(`.modal[data-kind="updates"] [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		pollUntil(`document.querySelectorAll(".card--updates .card__row").length === 1`),
		settleEffects(),
		chromedp.Evaluate(updatesCardStateJS, &after),
	)
	assert.Equal(t, []string{"Better Boots"}, after.Ticked, "the row that is left keeps its tick")
	assert.Equal(t, "Update 1 mod", after.Button,
		"the button counts the ticked rows on the card, not a key whose row has gone")

	// And the plan it opens is for that one mod only.
	var title, body string
	f.runInBrowser(t,
		chromedp.Click(`.card--updates [data-action="update-selected"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="updates"] .plan`, chromedp.ByQuery),
		textContent(`.modal[data-kind="updates"] .modal__title`, &title),
		textContent(`.modal[data-kind="updates"]`, &body),
		chromedp.Click(`.modal [data-action="cancel"]`, chromedp.ByQuery),
		waitGone(`.modal`),
	)
	assert.Equal(t, "Update 1 mod", strings.TrimSpace(title))
	assert.NotContains(t, body, "Great Gloves", "the updated mod is not planned again")
	assert.Empty(t, f.BrowserErrors())
}

// TestE2E_UpdatesCardSelection_ClearsOnceTheBatchIsConfirmed mirrors the
// library's batch bar: a confirmed batch has used its selection. The locked
// row is what makes this observable - the batch skips it, so it is still on
// the card afterwards, and it must not still be ticked with the button
// offering to update it again.
func TestE2E_UpdatesCardSelection_ClearsOnceTheBatchIsConfirmed(t *testing.T) {
	f := newE2EFixtureWithALockedAndAnUnlockedUpdate(t)

	var after e2eUpdatesCardState
	f.runInBrowser(t,
		chromedp.Navigate(f.HomePath()),
		chromedp.WaitVisible(`.card--updates`, chromedp.ByQuery),
		pollUntil(`document.querySelectorAll(".card--updates .card__row").length === 2`),
		chromedp.Evaluate(`document.querySelector('.card--updates [data-testid="select-all"]').click()`, nil),
		settleEffects(),
		chromedp.Click(`.card--updates [data-action="update-selected"]`, chromedp.ByQuery),
		chromedp.WaitVisible(`.modal[data-kind="updates"] [data-action="confirm"]:not([disabled])`, chromedp.ByQuery),
		chromedp.Click(`.modal[data-kind="updates"] [data-action="confirm"]`, chromedp.ByQuery),
		waitGone(`.modal`),
		// The batch's own outcome holds the button's place for a few seconds
		// after it succeeds (main.js#releaseSucceededOrigin), then hands it
		// back.
		pollUntil(`document.querySelectorAll(".card--updates .card__row").length === 1`),
		pollUntil(`document.querySelector('.card--updates [data-action="update-selected"]') !== null`),
		chromedp.Evaluate(updatesCardStateJS, &after),
	)
	assert.Empty(t, after.Ticked, "the skipped row is still listed, but no longer selected")
	assert.Equal(t, "Update selected", after.Button)
	require.NotNil(t, after.Disabled)
	assert.True(t, *after.Disabled, "with nothing selected there is nothing to update")
	assert.Empty(t, f.BrowserErrors())
}
