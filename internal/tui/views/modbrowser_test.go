package views_test

import (
	"testing"

	"lmm/internal/domain"
	"lmm/internal/tui/views"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestModBrowser_InitialState(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	model := views.NewModBrowser(game)

	assert.Equal(t, "", model.SearchQuery())
	assert.True(t, model.IsSearchFocused())
	assert.NotEmpty(t, model.View())
}

func TestModBrowser_TypeInSearch(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	model := views.NewModBrowser(game)

	// Type some characters
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	newModel, _ = newModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	newModel, _ = newModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	updated := newModel.(views.ModBrowser)
	assert.Equal(t, "sky", updated.SearchQuery())
}

func TestModBrowser_SetResults(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	model := views.NewModBrowser(game)

	mods := []domain.Mod{
		{ID: "1", Name: "SkyUI", Author: "schlangster"},
		{ID: "2", Name: "SKSE", Author: "ianpatt"},
	}

	newModel, _ := model.Update(views.SearchResultsMsg{Mods: mods})
	updated := newModel.(views.ModBrowser)

	assert.Equal(t, 2, updated.ResultCount())
}

func TestModBrowser_NavigateResults(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	model := views.NewModBrowser(game)

	mods := []domain.Mod{
		{ID: "1", Name: "SkyUI"},
		{ID: "2", Name: "SKSE"},
	}

	// Set results and exit search mode
	newModel, _ := model.Update(views.SearchResultsMsg{Mods: mods})
	newModel, _ = newModel.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := newModel.(views.ModBrowser)

	assert.False(t, updated.IsSearchFocused())

	// Navigate down
	newModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated = newModel.(views.ModBrowser)

	assert.Equal(t, 1, updated.Selected())
}

func TestModBrowser_EnterToInstall(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	model := views.NewModBrowser(game)

	mods := []domain.Mod{
		{ID: "1", Name: "SkyUI"},
	}

	// Set results and exit search mode
	newModel, _ := model.Update(views.SearchResultsMsg{Mods: mods})
	newModel, _ = newModel.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Press enter to install
	_, cmd := newModel.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd != nil {
		msg := cmd()
		installMsg, ok := msg.(views.InstallModMsg)
		assert.True(t, ok)
		assert.Equal(t, "1", installMsg.Mod.ID)
	}
}

func TestModBrowser_SlashFocusesSearch(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	model := views.NewModBrowser(game)

	// Exit search mode first
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := newModel.(views.ModBrowser)
	assert.False(t, updated.IsSearchFocused())

	// Press / to focus search
	newModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	updated = newModel.(views.ModBrowser)
	assert.True(t, updated.IsSearchFocused())
}
