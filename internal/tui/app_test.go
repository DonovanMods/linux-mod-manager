package tui_test

import (
	"testing"

	"lmm/internal/domain"
	"lmm/internal/tui"
	"lmm/internal/tui/views"

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

func TestApp_GameSelectedNavigatesToModBrowser(t *testing.T) {
	app := tui.NewApp(nil)

	// When GameSelectedMsg is received from the game select view,
	// the app should navigate to the mod browser view
	testGame := &domain.Game{
		ID:   "test-game",
		Name: "Test Game",
	}

	newApp, _ := app.Update(views.GameSelectedMsg{Game: testGame})
	updatedApp := newApp.(tui.App)

	assert.Equal(t, tui.ViewModBrowser, updatedApp.CurrentView(),
		"app should navigate to mod browser when game is selected")
}

func TestApp_GameSelectInitialized(t *testing.T) {
	// When app is created, the gameSelect view should be initialized
	// so that pressing enter on a game works
	app := tui.NewApp(nil)

	// The gameSelect should be initialized (not nil)
	// We verify this by checking the view output shows content from GameSelect view
	view := app.View()

	// With no games, GameSelect shows its empty state message
	// This confirms gameSelect is initialized and rendering
	assert.Contains(t, view, "No games configured",
		"app should initialize gameSelect view (showing empty state)")
	assert.Contains(t, view, "lmm config add-game",
		"app should show gameSelect help text")
}
