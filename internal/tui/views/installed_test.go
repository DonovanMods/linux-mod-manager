package views_test

import (
	"testing"
	"time"

	"lmm/internal/domain"
	"lmm/internal/tui/views"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestInstalled_InitialState(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profile := &domain.Profile{Name: "default", GameID: "skyrim-se"}
	model := views.NewInstalled(game, profile, nil)

	assert.Equal(t, 0, model.Selected())
	assert.NotEmpty(t, model.View())
}

func TestInstalled_WithMods(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profile := &domain.Profile{Name: "default", GameID: "skyrim-se"}
	mods := []domain.InstalledMod{
		{
			Mod:         domain.Mod{ID: "1", Name: "SkyUI", Version: "5.2"},
			ProfileName: "default",
			Enabled:     true,
			InstalledAt: time.Now(),
		},
		{
			Mod:         domain.Mod{ID: "2", Name: "SKSE", Version: "2.0"},
			ProfileName: "default",
			Enabled:     true,
			InstalledAt: time.Now(),
		},
	}

	model := views.NewInstalled(game, profile, mods)

	assert.Equal(t, 2, model.ModCount())
	view := model.View()
	assert.Contains(t, view, "SkyUI")
	assert.Contains(t, view, "SKSE")
}

func TestInstalled_Navigate(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profile := &domain.Profile{Name: "default", GameID: "skyrim-se"}
	mods := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "1", Name: "SkyUI"}, Enabled: true},
		{Mod: domain.Mod{ID: "2", Name: "SKSE"}, Enabled: true},
	}

	model := views.NewInstalled(game, profile, mods)

	// Navigate down
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated := newModel.(views.Installed)

	assert.Equal(t, 1, updated.Selected())
}

func TestInstalled_ToggleEnabled(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profile := &domain.Profile{Name: "default", GameID: "skyrim-se"}
	mods := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "1", Name: "SkyUI"}, Enabled: true},
	}

	model := views.NewInstalled(game, profile, mods)

	// Press space to toggle
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeySpace})

	if cmd != nil {
		msg := cmd()
		toggleMsg, ok := msg.(views.ToggleModMsg)
		assert.True(t, ok)
		assert.Equal(t, "1", toggleMsg.Mod.ID)
	}
}

func TestInstalled_Uninstall(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profile := &domain.Profile{Name: "default", GameID: "skyrim-se"}
	mods := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "1", Name: "SkyUI"}, Enabled: true},
	}

	model := views.NewInstalled(game, profile, mods)

	// Press d to uninstall
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})

	if cmd != nil {
		msg := cmd()
		uninstallMsg, ok := msg.(views.UninstallModMsg)
		assert.True(t, ok)
		assert.Equal(t, "1", uninstallMsg.Mod.ID)
	}
}

func TestInstalled_MoveUp(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profile := &domain.Profile{Name: "default", GameID: "skyrim-se"}
	mods := []domain.InstalledMod{
		{Mod: domain.Mod{ID: "1", Name: "SkyUI"}, Enabled: true},
		{Mod: domain.Mod{ID: "2", Name: "SKSE"}, Enabled: true},
	}

	model := views.NewInstalled(game, profile, mods)

	// Select second item
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Press K to move up in load order
	_, cmd := newModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'K'}})

	if cmd != nil {
		msg := cmd()
		reorderMsg, ok := msg.(views.ReorderModMsg)
		assert.True(t, ok)
		assert.Equal(t, 1, reorderMsg.FromIndex)
		assert.Equal(t, 0, reorderMsg.ToIndex)
	}
}

func TestInstalled_EmptyList(t *testing.T) {
	game := &domain.Game{ID: "skyrim-se", Name: "Skyrim SE"}
	profile := &domain.Profile{Name: "default", GameID: "skyrim-se"}
	model := views.NewInstalled(game, profile, nil)

	view := model.View()
	assert.Contains(t, view, "No mods installed")
}
