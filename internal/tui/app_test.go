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

	updated := updateWithKey(t, model, "2")
	require.Equal(t, ScreenInstalledMods, updated.CurrentScreen())

	updated = updateWithKey(t, updated, "3")
	require.Equal(t, ScreenSearch, updated.CurrentScreen())

	updated = updateWithKey(t, updated, "4")
	require.Equal(t, ScreenProfiles, updated.CurrentScreen())

	updated = updateWithKey(t, updated, "1")
	require.Equal(t, ScreenDashboard, updated.CurrentScreen())
}

func TestTabCyclesScreens(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry", Prototype: true})
	require.NoError(t, err)

	updated := updateWithKey(t, model, "tab")
	require.Equal(t, ScreenInstalledMods, updated.CurrentScreen())

	updated = updateWithKey(t, updated, "shift+tab")
	require.Equal(t, ScreenDashboard, updated.CurrentScreen())
}

func TestSelectionMovementIsClamped(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry", Prototype: true})
	require.NoError(t, err)
	model = updateWithKey(t, model, "2")

	model = updateWithKey(t, model, "j")
	model = updateWithKey(t, model, "j")
	require.Equal(t, 2, model.SelectedIndex(ScreenInstalledMods))

	for i := 0; i < 20; i++ {
		model = updateWithKey(t, model, "j")
	}
	require.Equal(t, 4, model.SelectedIndex(ScreenInstalledMods))

	for i := 0; i < 20; i++ {
		model = updateWithKey(t, model, "k")
	}
	require.Equal(t, 0, model.SelectedIndex(ScreenInstalledMods))
}

func TestHelpToggle(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry", Prototype: true})
	require.NoError(t, err)

	model = updateWithKey(t, model, "?")
	require.True(t, model.HelpVisible())

	model = updateWithKey(t, model, "?")
	require.False(t, model.HelpVisible())
}

func updateWithKey(t *testing.T, model Model, key string) Model {
	t.Helper()

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	updatedModel, ok := updated.(Model)
	require.True(t, ok)
	return updatedModel
}
