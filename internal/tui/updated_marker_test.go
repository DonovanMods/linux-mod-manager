package tui

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// --- Task 2 (#77): "updated this session" marker ---
//
// Design (task-2-brief.md, decided): a mod's row shows "*" only when it
// matches an m.lastUpdates entry by Source+ID AND mod.Version already
// equals that entry's ToVersion - a check that found an update but never
// applied it (modal cancelled, or apply failed) must NOT show the marker.
// Marker lifetime rides m.lastUpdates' own lifetime (cleared on game
// switch) - no new lifecycle code.

// TestModRow_ShowsUpdatedMarker_AfterActualApply drives the FULL
// check-then-apply flow through the real prototypeProvider (mirrors
// TestPrototypeUpdatesEndToEndKeyFlow, mutations_test.go) to prove
// prototype-mode parity: once skyui's apply completes and the refresh
// lands, m.lastUpdates still holds the ToVersion ("5.3") and skyui.Version
// has ALSO become "5.3" (ApplyUpdate's own bump), so the row must carry
// "*". skse-address-library, never part of the batch, must show none.
func TestModRow_ShowsUpdatedMarker_AfterActualApply(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("u"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(Model)
	require.NotNil(t, model.action.pending, "sanity: canned prototype data has updates")

	confirmed, confirmCmd := model.Update(keyRunes("y"))
	model = confirmed.(Model)
	require.NotNil(t, confirmCmd)
	doneMsg := runActionCmd(t, confirmCmd)

	updated, refreshCmd := model.Update(doneMsg)
	model = updated.(Model)
	require.NotNil(t, refreshCmd)
	loadedMsg := refreshCmd()
	updated, _ = model.Update(loadedMsg)
	model = updated.(Model)

	skyui := requireModByID(t, model.mods, "skyui")
	require.Equal(t, "5.3", skyui.Version, "sanity: apply actually bumped the version")
	require.Contains(t, model.modRow(0, 100, skyui), "*", "an actually-updated mod must carry the marker")

	other := requireModByID(t, model.mods, "skse-address-library")
	require.NotContains(t, model.modRow(1, 100, other), "*", "a mod never in the update batch must not be marked")
}

// TestModRow_NoMarker_WhenCheckedButNotApplied covers the load-bearing
// honesty rule: cancelling the apply-updates modal (not confirming it)
// leaves an entry in m.lastUpdates.Updates for skyui, but skyui.Version
// never changed, so no "*" must appear. This reuses
// TestViewChangelogFromListAfterModalCloses' cancel-path setup
// (changelog_list_test.go) for modRow instead of 'v'.
func TestModRow_NoMarker_WhenCheckedButNotApplied(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{UpdatesViewOut: UpdatesView{Updates: []UpdateItem{
		{Source: "nexusmods", ID: "skyui", Name: "SkyUI", FromVersion: "5.2", ToVersion: "5.3"},
	}}}
	model := openUpdatesModal(t, rec)

	updated, _ := model.Update(keyRunes("n")) // cancel, not confirm
	model = updated.(Model)
	require.NotNil(t, model.lastUpdates, "sanity: cancel retains the list-scoped lastUpdates")

	skyui := requireModByID(t, model.mods, "skyui")
	require.Equal(t, "5.2", skyui.Version, "sanity: cancelling never applied anything")
	require.NotContains(t, model.modRow(0, 100, skyui), "*", "checked-but-unapplied must not be marked")
}

// TestModRow_ShowsPinAndMarkerTogether: flagsWidth is 5, not 3, specifically
// so a pinned mod that was ALSO updated this session (a manual apply
// overrides the policy for one run) can show both "pin" and "*" without
// collision.
func TestModRow_ShowsPinAndMarkerTogether(t *testing.T) {
	t.Parallel()

	m, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	mod := ModItem{Name: "Pinned Mod", Author: "Auth", Version: "2.0", Status: "installed", Source: "nexusmods", ID: "pinned-mod", UpdatePolicy: "pin"}
	m.lastUpdates = &UpdatesView{Updates: []UpdateItem{
		{Source: "nexusmods", ID: "pinned-mod", Name: "Pinned Mod", FromVersion: "1.0", ToVersion: "2.0"},
	}}

	row := m.modRow(0, 100, mod)
	require.Contains(t, row, "pin", "pin flag must still show")
	require.Contains(t, row, "*", "marker must still show alongside pin")
}

// TestModRow_MarkerDoesNotShiftColumns mirrors
// TestModRow_NoTrailingColumnDrift (pin_visibility_test.go): the marker,
// like "pin", occupies a fixed column, so its presence or absence must
// never shift the author/version columns that follow. Both mods render the
// identical "1.0" Version so the version column's start offset is directly
// comparable between rows.
func TestModRow_MarkerDoesNotShiftColumns(t *testing.T) {
	t.Parallel()

	m, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	updatedMod := ModItem{Name: "Mod", Author: "Auth", Version: "1.0", Status: "installed", Source: "nexusmods", ID: "a"}
	notUpdatedMod := ModItem{Name: "Mod", Author: "Auth", Version: "1.0", Status: "installed", Source: "nexusmods", ID: "b"}
	m.lastUpdates = &UpdatesView{Updates: []UpdateItem{
		{Source: "nexusmods", ID: "a", Name: "Mod", FromVersion: "0.9", ToVersion: "1.0"},
	}}

	a := ansi.Strip(m.modRow(0, 80, updatedMod))
	b := ansi.Strip(m.modRow(0, 80, notUpdatedMod))
	require.Contains(t, a, "*")
	require.NotContains(t, b, "*")
	require.Equal(t, len([]rune(a)), len([]rune(b)), "rows must be the same width whether or not the marker is set")
}
