package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenModDetails_PushesImmediatelyWithLocalData is the local-first
// contract: the view must be on screen before the fetch resolves, seeded from
// the row, so an offline user still sees what the list already knew.
func TestOpenModDetails_PushesImmediatelyWithLocalData(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)

	m, cmd := m.openSelectedModDetails()

	require.NotNil(t, m.contextContent, "the view must be pushed before the fetch resolves")
	require.NotNil(t, cmd, "a fetch must be dispatched")
	assert.Equal(t, ScreenInstalledMods, m.screen)
	assert.True(t, m.action.running)
	body := m.View()
	item, _ := m.selectedMod()
	assert.Contains(t, body, item.Name)
}

// TestOpenModDetails_EnterBindingOnBothScreens: enter is the binding, and it
// works from Installed Mods and Search - opening the row actually
// highlighted on EACH screen's own selection state, not whichever screen's
// selection happened to be reachable. This is the regression test for a bug
// found in task-7-brief.md's own listed code: selectedModForDetails
// (mutations.go) must NOT fall back to m.selectedMod() (which only ever
// reads m.mods/m.selected[ScreenInstalledMods]) on the Search screen - doing
// so would open details for the Installed Mods row instead of the search
// result under the cursor. Asserting the exact NAME shown, not just that
// SOMETHING opened, is what makes this test catch that class of bug.
func TestOpenModDetails_EnterBindingOnBothScreens(t *testing.T) {
	for _, screen := range []Screen{ScreenInstalledMods, ScreenSearch} {
		m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
		m, _ = m.gotoScreen(screen)
		var wantName string
		if screen == ScreenSearch {
			// Same idiom search_test.go uses to reach a populated results
			// list (e.g. :219) - populatedSearchPage() at search_test.go:657.
			m.search.page = populatedSearchPage()
			m.search.state = searchReady
			wantName = m.search.page.Results[m.selected[ScreenSearch]].Name
		} else {
			item, ok := m.selectedMod()
			require.True(t, ok)
			wantName = item.Name
		}
		m = updateWithMsg(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		require.NotNil(t, m.contextContent, "enter must open details on %v", screen)
		assert.Contains(t, m.View(), wantName, "must show the row actually selected on %v, not a different screen's selection", screen)
	}
}

// TestOpenModDetails_SearchUsesSearchSelectionNotInstalledModsSelection is a
// tighter regression test for the same bug, isolating the failure mode: an
// Installed Mods selection sitting on a DIFFERENT mod than the Search
// selection. If selectedModForDetails ever regresses back to
// m.selectedMod() unconditionally, this fails by showing the wrong mod's
// name (the Installed Mods row) instead of the search result's.
func TestOpenModDetails_SearchUsesSearchSelectionNotInstalledModsSelection(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	require.NotEmpty(t, m.mods, "prototype data must seed at least one installed mod")
	installedItem, ok := m.selectedMod()
	require.True(t, ok)

	m, _ = m.gotoScreen(ScreenSearch)
	m.search.page = populatedSearchPage()
	m.search.state = searchReady
	m.selected[ScreenSearch] = 1
	searchItem := m.search.page.Results[1]
	require.NotEqual(t, installedItem.Name, searchItem.Name, "fixture must select genuinely different mods")

	m, cmd := m.openSelectedModDetails()
	require.NotNil(t, cmd)
	view := m.View()
	assert.Contains(t, view, searchItem.Name, "must open the SEARCH selection")
	assert.NotContains(t, view, installedItem.Name, "must not open the Installed Mods selection instead")
}

// TestOpenModDetails_EnterStillOpensDashboardMenu: the existing meaning of
// enter on the dashboard must not regress.
func TestOpenModDetails_EnterStillOpensDashboardMenu(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenDashboard)
	m = updateWithMsg(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, m.contextContent, "enter on the dashboard opens a menu entry, not details")
}

// TestOpenModDetails_SingleFlight: a second open while one is in flight is
// refused, matching every other async fetch.
func TestOpenModDetails_SingleFlight(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, _ = m.openSelectedModDetails()
	gen := m.action.gen

	m, cmd := m.openSelectedModDetails()
	assert.Nil(t, cmd, "a second open must be refused while one is running")
	assert.Equal(t, gen, m.action.gen)
}

// TestOpenModDetails_StaleResultDropped
func TestOpenModDetails_StaleResultDropped(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, _ = m.openSelectedModDetails()

	stale := modDetailsFetchedMsg{gen: m.action.gen - 1, details: ModDetails{Description: "STALE"}}
	updated, _ := m.Update(stale)
	m = updated.(Model)

	assert.NotContains(t, m.View(), "STALE")
	assert.True(t, m.action.running, "a stale result must not clear the running flag")
}

// TestOpenModDetails_SuccessEnrichesInPlace
func TestOpenModDetails_SuccessEnrichesInPlace(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, _ = m.openSelectedModDetails()

	enriched := ModDetails{Name: "Mod A", ID: "a", Version: "1.0", Author: "X",
		Description: "ENRICHED DESCRIPTION"}
	updated, _ := m.Update(modDetailsFetchedMsg{gen: m.action.gen, details: enriched})
	m = updated.(Model)

	assert.False(t, m.action.running)
	assert.Contains(t, m.View(), "ENRICHED DESCRIPTION")
}

// TestOpenModDetails_FailureKeepsLocalView is the degradation contract: a
// failed fetch must never close the view or discard what was already shown.
func TestOpenModDetails_FailureKeepsLocalView(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	item, _ := m.selectedMod()
	m, _ = m.openSelectedModDetails()

	updated, _ := m.Update(modDetailsFailedMsg{gen: m.action.gen, err: errors.New("source unreachable")})
	m = updated.(Model)

	require.NotNil(t, m.contextContent, "a failed fetch must not close the view")
	view := m.View()
	assert.Contains(t, view, item.Name, "local data must survive the failure")
	assert.Contains(t, view, "unavailable")
	assert.False(t, m.action.running)
	assert.True(t, m.action.statusIsError)
}

// TestOpenModDetails_FailurePreservesSeededFields is the explicit assertion
// that resolveModDetailsFailed mutates ONLY Fetching/FetchErr on the pushed
// content, never assigning msg.details (which is always the zero value on
// this path - GetModDetails' documented failure contract) over the
// locally-seeded fields. Distinct from TestOpenModDetails_FailureKeepsLocalView
// above (which only checks the rendered view): this reaches into the pushed
// content directly so a regression that narrows what's checked at render time
// can't hide a wipe of fields the current body() happens not to show yet.
func TestOpenModDetails_FailurePreservesSeededFields(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	item, _ := m.selectedMod()
	m, _ = m.openSelectedModDetails()

	c, ok := m.contextContent.(*modDetailsContent)
	require.True(t, ok)
	seededName, seededVersion, seededAuthor := c.details.Name, c.details.Version, c.details.Author

	// GetModDetails' documented contract: a failure is accompanied by a
	// zero-value ModDetails{} - the resolver must never assign that over the
	// seeded fields.
	updated, _ := m.Update(modDetailsFailedMsg{gen: m.action.gen, err: errors.New("source unreachable")})
	m = updated.(Model)

	c, ok = m.contextContent.(*modDetailsContent)
	require.True(t, ok)
	assert.Equal(t, seededName, c.details.Name, "name must survive a failed fetch")
	assert.Equal(t, seededVersion, c.details.Version, "version must survive a failed fetch")
	assert.Equal(t, seededAuthor, c.details.Author, "author must survive a failed fetch")
	assert.Equal(t, item.Name, c.details.Name)
	assert.False(t, c.details.Fetching)
	assert.Equal(t, "source unreachable", c.details.FetchErr)
}

// TestOpenModDetails_NoProviderNoOp
func TestOpenModDetails_NoProviderNoOp(t *testing.T) {
	m := sizedModelWithActions(t, nil, 100, 30)
	m.provider = nil
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, cmd := m.openSelectedModDetails()
	assert.Nil(t, cmd)
	assert.Nil(t, m.contextContent)
}

// TestOpenModDetails_QuitDrainCancels: quitting mid-fetch drains cleanly.
func TestOpenModDetails_QuitDrainCancels(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, _ = m.openSelectedModDetails()
	m.action.draining = true

	updated, cmd := m.Update(modDetailsFetchedMsg{gen: m.action.gen, details: ModDetails{}})
	_ = updated
	assert.NotNil(t, cmd, "a drained quit must produce the quit command")
}

// TestOpenModDetails_EnterNoOpWhileSearchInputFocused is the (b) finding from
// task-7-brief.md's review: with the search input focused, enter is bound to
// Submit (also "enter" - see keys.go), which is matched BEFORE the default
// case in updateKey's focused-input branch (app.go, ahead of the outer
// switch's Select case) - so it blurs the input and starts a search instead
// of ever reaching openSelectedModDetails. This proves that path stays inert:
// no details view opens, and the input ends up blurred (Submit's own
// behavior), not left focused with content pushed underneath.
func TestOpenModDetails_EnterNoOpWhileSearchInputFocused(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreenFocused(ScreenSearch)
	require.True(t, m.search.input.Focused())

	m = updateWithMsg(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	assert.Nil(t, m.contextContent, "enter with the search input focused must submit, not open details")
	assert.False(t, m.search.input.Focused(), "Submit blurs the input, matching every other submit")
}

// TestOpenModDetails_FailureStatusLineStaysSingleLine is the regression test
// for a Task 7 review finding: resolveModDetailsFailed used to write
// msg.err.Error() straight onto the status line, unlike every other failure
// resolver in this file (resolvePlanFailure, resolveVersionsFetchFailed,
// resolveInstallPlanFailure, ...), all of which wrap with singleLine.
// statusLine's own truncate() bounds character WIDTH only, not line count,
// so an unwrapped multi-line error adds genuine extra rows nothing budgets
// for - empirically, a 5-line error broke
// TestWindowSizeExpandsViewToTerminalBounds' invariant (view height == window
// height) by rendering 34 lines in a 30-row terminal. Asserted against the
// rendered view's HEIGHT, not the status string, since the height is what
// actually broke - a string-only assertion would pass even with the bug
// present.
func TestOpenModDetails_FailureStatusLineStaysSingleLine(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, _ = m.openSelectedModDetails()

	multiLine := "line one\nline two\nline three\nline four\nline five"
	updated, _ := m.Update(modDetailsFailedMsg{gen: m.action.gen, err: errors.New(multiLine)})
	m = updated.(Model)

	require.True(t, m.action.statusIsError)
	assert.NotContains(t, m.action.status, "\n", "the status line must never carry an embedded newline")
	view := m.View()
	assert.LessOrEqual(t, lipgloss.Height(view), 30,
		"a multi-line error must not push the view past the terminal's row budget")
}

// TestOpenModDetails_EscThenEnterReopensImmediately is the regression test
// for the second Task 7 review finding: esc used to pop the details view
// without cancelling its in-flight fetch or clearing m.action.running, so
// pressing enter again (on the same or a different mod) was a silent no-op -
// dead until the abandoned fetch eventually settled - because
// openSelectedModDetails' own single-flight guard (`m.action.running ||
// m.action.pending != nil`) refused to start a new one.
// cancelPushedContentFetch (mutations.go), called from updateKey's Blur
// case, fixes this by cancelling and clearing running at the moment the
// view is dismissed, not when the abandoned fetch eventually reports back.
func TestOpenModDetails_EscThenEnterReopensImmediately(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, _ = m.openSelectedModDetails()
	require.True(t, m.action.running, "the fetch must still be in flight")
	firstGen := m.action.gen

	// esc (Blur) pops the view while the fetch is still running.
	m = updateWithMsg(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	require.Nil(t, m.contextContent, "esc must pop the view")
	require.False(t, m.action.running, "esc must cancel the abandoned fetch, not leave running stuck true")

	// enter must work immediately - not a dead key until the abandoned fetch
	// (which was never actually stopped mid-goroutine, only its result
	// discarded) settles.
	m = updateWithMsg(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, m.contextContent, "enter must reopen details immediately after esc")
	assert.True(t, m.action.running, "the new fetch must actually be running")
	assert.NotEqual(t, firstGen, m.action.gen, "the reopened fetch must get a fresh generation")
}

// TestOpenModDetails_DismissedFetchNoStatusError proves the abandoned
// fetch's eventual result - which cancelPushedContentFetch cannot stop
// mid-goroutine, only discard - never surfaces on the status line once the
// view it belonged to has been dismissed: a stale error about a mod the
// user already dismissed would be confusing, and clearing/overwriting
// whatever status the user's NEXT action set would be just as wrong.
// resolveModDetailsFailed/resolveModDetailsFetched both guard on the pushed
// content still being their own *modDetailsContent (see their doc
// comments); this exercises that guard from the OTHER side - the message
// arriving after the content is already gone, not stale-gen dropped (esc
// doesn't bump m.action.gen, so the ordinary gen check alone would NOT have
// caught this).
func TestOpenModDetails_DismissedFetchNoStatusError(t *testing.T) {
	m := sizedModelWithActions(t, &recordingActions{}, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m, _ = m.openSelectedModDetails()
	gen := m.action.gen

	m = updateWithMsg(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	require.Nil(t, m.contextContent)
	require.Equal(t, gen, m.action.gen, "esc must not bump gen - the ordinary stale-gen check must not be what saves this")

	// Something else now legitimately owns the status line.
	m.action.status = "unrelated status from a later action"
	m.action.statusIsError = false

	updated, _ := m.Update(modDetailsFailedMsg{gen: gen, err: errors.New("source unreachable")})
	m = updated.(Model)

	assert.Equal(t, "unrelated status from a later action", m.action.status,
		"a dismissed fetch's error must not overwrite whatever the status line is showing now")
	assert.False(t, m.action.statusIsError)
}
