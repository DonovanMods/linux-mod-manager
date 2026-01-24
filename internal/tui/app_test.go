package tui_test

import (
	"testing"

	"lmm/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewApp_InitialState(t *testing.T) {
	app := tui.NewApp(nil)

	assert.Equal(t, tui.ViewGameSelect, app.CurrentView())
	assert.NotEmpty(t, app.View())
}

func TestApp_NavigateToView(t *testing.T) {
	app := tui.NewApp(nil)

	// Navigate to mod browser
	newApp, _ := app.Update(tui.NavigateMsg{View: tui.ViewModBrowser})
	updatedApp := newApp.(tui.App)

	assert.Equal(t, tui.ViewModBrowser, updatedApp.CurrentView())
}

func TestApp_QuitOnQ(t *testing.T) {
	app := tui.NewApp(nil)

	newModel, cmd := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	require.NotNil(t, newModel)

	// Check that quit command was returned
	if cmd != nil {
		msg := cmd()
		_, isQuit := msg.(tea.QuitMsg)
		assert.True(t, isQuit)
	}
}

func TestApp_ViewRendersWithoutPanic(t *testing.T) {
	app := tui.NewApp(nil)

	// Should not panic
	view := app.View()
	assert.NotEmpty(t, view)
}
