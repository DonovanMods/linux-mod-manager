package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func TestNewPrototypeModelDefaultsToDashboard(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry", Prototype: true})
	require.NoError(t, err)
	require.Equal(t, ScreenDashboard, model.CurrentScreen())
}

func TestNewPrototypeModelRejectsInvalidTheme(t *testing.T) {
	t.Parallel()

	_, err := NewPrototypeModel(Options{Theme: "bogus", Prototype: true})
	require.Error(t, err)
}

func TestNumberKeysNavigateScreens(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry", Prototype: true})
	require.NoError(t, err)

	updated := updateWithRunes(t, model, "2")
	require.Equal(t, ScreenInstalledMods, updated.CurrentScreen())

	updated = updateWithRunes(t, updated, "3")
	require.Equal(t, ScreenSearch, updated.CurrentScreen())

	updated = updateWithRunes(t, updated, "4")
	require.Equal(t, ScreenProfiles, updated.CurrentScreen())

	updated = updateWithRunes(t, updated, "1")
	require.Equal(t, ScreenDashboard, updated.CurrentScreen())
}

func TestTabCyclesScreens(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry", Prototype: true})
	require.NoError(t, err)

	updated := updateWithKeyType(t, model, tea.KeyTab)
	require.Equal(t, ScreenInstalledMods, updated.CurrentScreen())

	updated = updateWithKeyType(t, updated, tea.KeyShiftTab)
	require.Equal(t, ScreenDashboard, updated.CurrentScreen())
}

func TestArrowAndVimKeysNavigateScreens(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry", Prototype: true})
	require.NoError(t, err)

	model = updateWithKeyType(t, model, tea.KeyRight)
	require.Equal(t, ScreenInstalledMods, model.CurrentScreen())

	model = updateWithRunes(t, model, "l")
	require.Equal(t, ScreenSearch, model.CurrentScreen())

	model = updateWithKeyType(t, model, tea.KeyLeft)
	require.Equal(t, ScreenInstalledMods, model.CurrentScreen())

	model = updateWithRunes(t, model, "h")
	require.Equal(t, ScreenDashboard, model.CurrentScreen())
}

func TestSelectionMovementIsClamped(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry", Prototype: true})
	require.NoError(t, err)
	model = updateWithRunes(t, model, "2")

	model = updateWithRunes(t, model, "j")
	model = updateWithKeyType(t, model, tea.KeyDown)
	require.Equal(t, 2, model.SelectedIndex(ScreenInstalledMods))

	for i := 0; i < 20; i++ {
		model = updateWithKeyType(t, model, tea.KeyDown)
	}
	require.Equal(t, 4, model.SelectedIndex(ScreenInstalledMods))

	model = updateWithRunes(t, model, "k")
	require.Equal(t, 3, model.SelectedIndex(ScreenInstalledMods))

	for i := 0; i < 20; i++ {
		model = updateWithKeyType(t, model, tea.KeyUp)
	}
	require.Equal(t, 0, model.SelectedIndex(ScreenInstalledMods))
}

func TestSearchAndQuitBindings(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry", Prototype: true})
	require.NoError(t, err)

	model = updateWithRunes(t, model, "/")
	require.Equal(t, ScreenSearch, model.CurrentScreen())

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
}

func TestHelpToggle(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry", Prototype: true})
	require.NoError(t, err)

	model = updateWithRunes(t, model, "?")
	require.True(t, model.HelpVisible())

	model = updateWithRunes(t, model, "?")
	require.False(t, model.HelpVisible())
}

func updateWithRunes(t *testing.T, model Model, key string) Model {
	t.Helper()

	return updateWithMsg(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

func updateWithKeyType(t *testing.T, model Model, keyType tea.KeyType) Model {
	t.Helper()

	return updateWithMsg(t, model, tea.KeyMsg{Type: keyType})
}

func updateWithMsg(t *testing.T, model Model, msg tea.KeyMsg) Model {
	t.Helper()

	updated, _ := model.Update(msg)
	updatedModel, ok := updated.(Model)
	require.True(t, ok)
	return updatedModel
}
