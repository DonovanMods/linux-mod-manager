package views_test

import (
	"testing"

	"lmm/internal/domain"
	"lmm/internal/tui/views"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestGameSelect_InitialState(t *testing.T) {
	games := []*domain.Game{
		{ID: "skyrim-se", Name: "Skyrim Special Edition"},
		{ID: "fallout4", Name: "Fallout 4"},
	}

	model := views.NewGameSelect(games)

	assert.Equal(t, 0, model.Selected())
	assert.NotEmpty(t, model.View())
}

func TestGameSelect_NavigateDown(t *testing.T) {
	games := []*domain.Game{
		{ID: "skyrim-se", Name: "Skyrim Special Edition"},
		{ID: "fallout4", Name: "Fallout 4"},
	}

	model := views.NewGameSelect(games)

	// Move down
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated := newModel.(views.GameSelect)

	assert.Equal(t, 1, updated.Selected())
}

func TestGameSelect_NavigateUp(t *testing.T) {
	games := []*domain.Game{
		{ID: "skyrim-se", Name: "Skyrim Special Edition"},
		{ID: "fallout4", Name: "Fallout 4"},
	}

	model := views.NewGameSelect(games)

	// Move down first, then up
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	newModel, _ = newModel.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := newModel.(views.GameSelect)

	assert.Equal(t, 0, updated.Selected())
}

func TestGameSelect_WrapAround(t *testing.T) {
	games := []*domain.Game{
		{ID: "skyrim-se", Name: "Skyrim Special Edition"},
		{ID: "fallout4", Name: "Fallout 4"},
	}

	model := views.NewGameSelect(games)

	// Move up from first item should wrap to last
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyUp})
	updated := newModel.(views.GameSelect)

	assert.Equal(t, 1, updated.Selected())
}

func TestGameSelect_EnterSelectsGame(t *testing.T) {
	games := []*domain.Game{
		{ID: "skyrim-se", Name: "Skyrim Special Edition"},
		{ID: "fallout4", Name: "Fallout 4"},
	}

	model := views.NewGameSelect(games)

	// Press enter
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Should return a GameSelectedMsg command
	if cmd != nil {
		msg := cmd()
		selectedMsg, ok := msg.(views.GameSelectedMsg)
		assert.True(t, ok)
		assert.Equal(t, "skyrim-se", selectedMsg.Game.ID)
	}
}

func TestGameSelect_EmptyList(t *testing.T) {
	model := views.NewGameSelect(nil)

	view := model.View()
	assert.Contains(t, view, "No games configured")
}
