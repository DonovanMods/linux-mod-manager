package tui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// #221: pak-conversion toggle ('m' on Installed Mods) - mutations.go's
// toggleSelectedModConvert, wired onto ActionProvider.SetConvertPaks.
// Mirrors rollback_test.go/mutations_test.go's own guard-test shape:
// synchronous, no confirm modal - the keypress itself is the confirmation
// (resolvePolicyChoice's "the choice IS the confirmation" precedent).

// convertReadyModIndex names a prototype fixture mod ("skyui", see
// rollback_test.go's identical index 0 comment) this file's success-path
// test forces CompileGame/ConvertPaks on directly. Unlike
// rollbackReadyModIndex/noPreviousVersionModIndex (which rely entirely on
// pre-seeded canned fields), prototype/data.go carries NO ConvertPaks/
// CompileGame state at all (task-12-brief.md scopes prototype/data.go OUT -
// #221's flag only matters for a real DeployCompile game, which
// --prototype mode has no need to model) - so every canned mod's
// CompileGame reads false until a test sets it directly, the only way to
// exercise the success path against this fixture.
const convertReadyModIndex = 0 // "skyui"

// TestConvertToggleKeyNonCompileGameRefusesSynchronously proves the
// CompileGame gate: every prototype fixture mod is non-compile by default,
// so 'm' refuses on the status line with no provider call - mirroring
// TestRollbackKeyNoPreviousVersion's "benign, not an error" shape
// (statusIsError false: the row itself isn't wrong, the game's deploy mode
// just makes the flag inert).
func TestConvertToggleKeyNonCompileGameRefusesSynchronously(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = convertReadyModIndex
	require.False(t, model.mods[convertReadyModIndex].CompileGame, "sanity: prototype fixture never sets CompileGame")

	updated, cmd := model.Update(keyRunes("m"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Equal(t, "pak conversion applies only to merge-compile games", model.action.status)
	require.False(t, model.action.statusIsError, "a non-compile game's toggle is benign, not a refusal")
	require.Empty(t, rec.SetConvertPaksCalls)
}

// TestConvertToggleKeyDispatchesAndRefreshes proves the success path: on a
// mod with CompileGame true, 'm' calls SetConvertPaks with the FLIPPED
// ConvertPaks value, shows the outcome's message on the status line, and
// dispatches a refresh (m.loadData) so the "raw" flag column picks up the
// new state - mirroring TestMoveSelectedModDownPersistsOrder's own
// success-path shape (moveSelectedMod, mutations_test.go).
func TestConvertToggleKeyDispatchesAndRefreshes(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{SetConvertPaksOutcome: ActionOutcome{Message: `"SkyUI" pak conversion: off (deploy to apply)`}}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.selected[ScreenInstalledMods] = convertReadyModIndex
	model.mods[convertReadyModIndex].CompileGame = true
	model.mods[convertReadyModIndex].ConvertPaks = true

	updated, cmd := model.Update(keyRunes("m"))
	model = updated.(Model)
	require.NotNil(t, cmd, "a successful toggle must refresh so the raw flag column updates")
	require.IsType(t, dataLoadedMsg{}, cmd())

	require.Len(t, rec.SetConvertPaksCalls, 1)
	require.Equal(t, "skyui", rec.SetConvertPaksCalls[0].ModID)
	require.False(t, rec.SetConvertPaksCalls[0].Enabled, "toggling an ON mod must request OFF")
	require.Equal(t, `"SkyUI" pak conversion: off (deploy to apply)`, model.action.status)
	require.False(t, model.action.statusIsError)
}

// TestConvertToggleKeyEmptyListNoop mirrors TestMoveAtListEdgeNoop's own
// empty-list guard shape: no selection, no provider call, no status change.
func TestConvertToggleKeyEmptyListNoop(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenInstalledMods
	model.mods = nil

	updated, cmd := model.Update(keyRunes("m"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Empty(t, rec.SetConvertPaksCalls)
}

// TestConvertToggleKeyInertOffInstalledMods mirrors this file's sibling
// guard tests (and TestPolicyKeyIgnoredOffInstalledMods' identical shape):
// 'm' on any other screen is a silent no-op.
func TestConvertToggleKeyInertOffInstalledMods(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenDashboard

	updated, cmd := model.Update(keyRunes("m"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Empty(t, rec.SetConvertPaksCalls)
}
