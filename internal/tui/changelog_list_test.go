package tui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Fix-wave-2 smoke finding #1: 'v' (Changelog) was reachable ONLY while the
// apply-updates confirmation modal was pending (updatePendingActionKey/
// openChangelogFromUpdateModal, actions.go) - once that modal closed
// (confirm or cancel), or a check found zero updates and never opened one at
// all, changelogs became completely unreachable even though the user was
// still looking at the very same Installed Mods list. This file covers 'v'
// on ScreenInstalledMods OUTSIDE any modal (viewSelectedModChangelog,
// mutations.go), backed by m.lastUpdates - the retained CheckUpdates result
// that outlives the modal (see Model.lastUpdates' own doc comment, app.go),
// distinct from m.pendingUpdates which dies with it.
//
// The prototype fixture's InstalledMods rows (prototype/data.go's Load) are
// [0]=skyui, [1]=ussep, [2]=skse-address-library, [3]=immersive-armors,
// [4]=alternate-start - selected[ScreenInstalledMods] starts at 0 (skyui).

// TestViewChangelogFromListAfterModalCloses is the happy path: check finds 2
// updates, the batch modal is dismissed with esc (NOT confirmed), and 'v' on
// each of the two matching rows still opens that row's own changelog
// overlay - proving lastUpdates survives the modal's cancel path.
func TestViewChangelogFromListAfterModalCloses(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{Updates: []UpdateItem{
		{Source: "nexusmods", ID: "skyui", Name: "SkyUI", FromVersion: "5.2", ToVersion: "5.3", Changelog: "Fixed bugs."},
		{Source: "nexusmods", ID: "ussep", Name: "USSEP", FromVersion: "4.3", ToVersion: "4.4", Changelog: "More fixes."},
	}}}
	model := openUpdatesModal(t, rec)
	require.NotNil(t, model.lastUpdates)

	// Cancel (not confirm) the batch modal.
	updated, cmd := model.Update(keyRunes("n"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Nil(t, model.pendingUpdates, "cancel still clears the modal-scoped view")
	require.NotNil(t, model.lastUpdates, "cancel must NOT clear the list-scoped retained view")

	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // skyui

	updated, cmd = model.Update(keyRunes("v"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.overlay)
	require.Equal(t, "SkyUI 5.2 → 5.3", model.overlay.title)
	require.Equal(t, []string{"Fixed bugs."}, model.overlay.lines)

	// Close the overlay, select the OTHER updated mod, and check its own
	// changelog too.
	model.overlay = nil
	model.selected[ScreenInstalledMods] = 1 // ussep

	updated, cmd = model.Update(keyRunes("v"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.NotNil(t, model.overlay)
	require.Equal(t, "USSEP 4.3 → 4.4", model.overlay.title)
	require.Equal(t, []string{"More fixes."}, model.overlay.lines)
}

// TestViewChangelogFromListAfterZeroUpdates covers the OTHER path that never
// even opens a modal: CheckUpdates finding zero updates must still populate
// m.lastUpdates (empty), so 'v' resolves to the "checked, no entry" status
// line rather than the "never checked" one.
func TestViewChangelogFromListAfterZeroUpdates(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{}}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.Nil(t, model.action.pending, "zero updates never opens a modal")
	require.NotNil(t, model.lastUpdates, "the zero-updates path must still retain the (empty) result")

	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0 // skyui

	updated, cmd = model.Update(keyRunes("v"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.overlay)
	require.Contains(t, model.action.status, `no update changelog for "SkyUI"`)
	require.False(t, model.action.statusIsError)
}

// TestViewChangelogFromListNoEntryForSelectedMod covers a check that DID find
// updates, but not for the currently-selected row.
func TestViewChangelogFromListNoEntryForSelectedMod(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{Updates: []UpdateItem{
		{Source: "nexusmods", ID: "skyui", Name: "SkyUI", FromVersion: "5.2", ToVersion: "5.3", Changelog: "Fixed bugs."},
	}}}
	model := openUpdatesModal(t, rec)
	// Cancel so we're outside the modal.
	updated, _ := model.Update(keyRunes("n"))
	model = updated.(Model)

	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 2 // skse-address-library - not in the update batch
	item := model.mods[2]
	require.Equal(t, "skse-address-library", item.ID)

	updated, cmd := model.Update(keyRunes("v"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.overlay)
	require.Equal(t, `no update changelog for "SKSE Address Library"`, model.action.status)
	require.False(t, model.action.statusIsError)
}

// TestViewChangelogFromListNeverChecked covers a fresh model, no CheckUpdates
// ever run this session: m.lastUpdates is nil, so 'v' must give the distinct
// "go check first" copy, not the "no entry" one.
func TestViewChangelogFromListNeverChecked(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = 0
	require.Nil(t, model.lastUpdates)

	updated, cmd := model.Update(keyRunes("v"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.overlay)
	require.Equal(t, "no update info — check updates (u) first", model.action.status)
	require.False(t, model.action.statusIsError)
}

// TestViewChangelogFromListWrongScreenIsNoop mirrors every other list key's
// wrong-screen guard (e.g. TestRollbackKeyWrongScreenIsNoop).
func TestViewChangelogFromListWrongScreenIsNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenDashboard, ScreenSearch, ScreenProfiles, ScreenSources} {
		model := modelWithActions(t, &recordingActions{})
		model.screen = screen

		updated, cmd := model.Update(keyRunes("v"))
		model = updated.(Model)
		require.Nil(t, cmd)
		require.Nil(t, model.overlay, "screen %v", screen)
		require.Empty(t, model.action.status, "screen %v", screen)
	}
}

// TestViewChangelogFromListKeySwallowedByFocusedSearchInput mirrors
// TestRollbackKeySwallowedByFocusedSearchInput's identical shape: the
// focused-input branch in updateKey runs before the mutation-key switch, so
// this is inertness by construction, not a special case viewSelectedModChangelog
// needs to implement itself.
func TestViewChangelogFromListKeySwallowedByFocusedSearchInput(t *testing.T) {
	t.Parallel()

	model := modelWithActions(t, &recordingActions{})
	updated := updateWithRunes(t, model, "3") // jump to search, focused
	updated = updateWithRunes(t, updated, "v")

	require.True(t, updated.search.input.Focused())
	require.Contains(t, updated.search.input.Value(), "v")
	require.Nil(t, updated.overlay)
}

// TestGameSwitchClearsLastUpdates guards the leak this must never allow: a
// different game's retained CheckUpdates result must not answer 'v' after
// switching games (resolveGameSwitch's defense-in-depth reset, mutations.go -
// alongside its existing pendingUpdates clear).
func TestGameSwitchClearsLastUpdates(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{Updates: []UpdateItem{
		{Source: "nexusmods", ID: "skyui", Name: "SkyUI", FromVersion: "5.2", ToVersion: "5.3", Changelog: "Fixed bugs."},
	}}}
	model := openUpdatesModal(t, rec)
	updated, _ := model.Update(keyRunes("n")) // cancel, outside the modal
	model = updated.(Model)
	require.NotNil(t, model.lastUpdates)

	// modelWithActions binds the prototype provider (skyrim-se active,
	// fallout4 the alternate - prototype/data.go's Load) as BOTH m.provider
	// and m.actions (recordingActions also implements gameRebinder), so
	// switching to "fallout4" is a real, non-same-game switch that exercises
	// resolveGameSwitch's full reset.
	updated, cmd := model.Update(gameChosenMsg{id: "fallout4"})
	model = updated.(Model)
	_ = cmd
	require.Nil(t, model.lastUpdates, "a game switch must clear the retained list-scoped changelog view")
}
