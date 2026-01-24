package views_test

import (
	"testing"

	"lmm/internal/domain"
	"lmm/internal/tui/views"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestProfiles_InitialState(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profiles := []*domain.Profile{
		{Name: "default", GameID: "skyrim-se", IsDefault: true},
		{Name: "survival", GameID: "skyrim-se"},
	}

	model := views.NewProfiles(game, profiles, "default")

	assert.Equal(t, 0, model.Selected())
	assert.Equal(t, 2, model.ProfileCount())
	assert.NotEmpty(t, model.View())
}

func TestProfiles_Navigate(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profiles := []*domain.Profile{
		{Name: "default", GameID: "skyrim-se"},
		{Name: "survival", GameID: "skyrim-se"},
	}

	model := views.NewProfiles(game, profiles, "default")

	// Navigate down
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated := newModel.(views.Profiles)

	assert.Equal(t, 1, updated.Selected())
}

func TestProfiles_SwitchProfile(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profiles := []*domain.Profile{
		{Name: "default", GameID: "skyrim-se"},
		{Name: "survival", GameID: "skyrim-se"},
	}

	model := views.NewProfiles(game, profiles, "default")

	// Select second profile
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Press enter to switch
	_, cmd := newModel.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd != nil {
		msg := cmd()
		switchMsg, ok := msg.(views.SwitchProfileMsg)
		assert.True(t, ok)
		assert.Equal(t, "survival", switchMsg.Profile.Name)
	}
}

func TestProfiles_CreateNew(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profiles := []*domain.Profile{
		{Name: "default", GameID: "skyrim-se"},
	}

	model := views.NewProfiles(game, profiles, "default")

	// Press 'n' to create new
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	updated := newModel.(views.Profiles)

	assert.True(t, updated.IsCreating())
}

func TestProfiles_Delete(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profiles := []*domain.Profile{
		{Name: "default", GameID: "skyrim-se"},
		{Name: "survival", GameID: "skyrim-se"},
	}

	model := views.NewProfiles(game, profiles, "default")

	// Select second profile and delete
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd := newModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})

	if cmd != nil {
		msg := cmd()
		deleteMsg, ok := msg.(views.DeleteProfileMsg)
		assert.True(t, ok)
		assert.Equal(t, "survival", deleteMsg.Profile.Name)
	}
}

func TestProfiles_SetDefault(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profiles := []*domain.Profile{
		{Name: "default", GameID: "skyrim-se", IsDefault: true},
		{Name: "survival", GameID: "skyrim-se"},
	}

	model := views.NewProfiles(game, profiles, "default")

	// Select second profile and set as default
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd := newModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})

	if cmd != nil {
		msg := cmd()
		defaultMsg, ok := msg.(views.SetDefaultProfileMsg)
		assert.True(t, ok)
		assert.Equal(t, "survival", defaultMsg.Profile.Name)
	}
}

func TestProfiles_Export(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profiles := []*domain.Profile{
		{Name: "default", GameID: "skyrim-se"},
	}

	model := views.NewProfiles(game, profiles, "default")

	// Press 'e' to export
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if cmd != nil {
		msg := cmd()
		exportMsg, ok := msg.(views.ExportProfileMsg)
		assert.True(t, ok)
		assert.Equal(t, "default", exportMsg.Profile.Name)
	}
}

func TestProfiles_EmptyList(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	model := views.NewProfiles(game, nil, "")

	view := model.View()
	assert.Contains(t, view, "No profiles")
}
