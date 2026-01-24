package views_test

import (
	"testing"

	"lmm/internal/domain"
	"lmm/internal/tui/views"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestSettings_InitialState(t *testing.T) {
	settings := views.SettingsData{
		LinkMethod:  domain.LinkSymlink,
		Keybindings: "vim",
	}

	model := views.NewSettings(settings)

	assert.Equal(t, 0, model.Selected())
	assert.NotEmpty(t, model.View())
}

func TestSettings_Navigate(t *testing.T) {
	settings := views.SettingsData{
		LinkMethod:  domain.LinkSymlink,
		Keybindings: "vim",
	}

	model := views.NewSettings(settings)

	// Navigate down
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated := newModel.(views.Settings)

	assert.Equal(t, 1, updated.Selected())
}

func TestSettings_CycleLinkMethod(t *testing.T) {
	settings := views.SettingsData{
		LinkMethod:  domain.LinkSymlink,
		Keybindings: "vim",
	}

	model := views.NewSettings(settings)

	// Press enter to cycle link method
	newModel, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := newModel.(views.Settings)

	// Should have cycled to hardlink
	assert.Equal(t, domain.LinkHardlink, updated.CurrentSettings().LinkMethod)

	// Should emit a change message
	if cmd != nil {
		msg := cmd()
		changeMsg, ok := msg.(views.SettingsChangedMsg)
		assert.True(t, ok)
		assert.Equal(t, domain.LinkHardlink, changeMsg.Settings.LinkMethod)
	}
}

func TestSettings_CycleKeybindings(t *testing.T) {
	settings := views.SettingsData{
		LinkMethod:  domain.LinkSymlink,
		Keybindings: "vim",
	}

	model := views.NewSettings(settings)

	// Navigate to keybindings
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Press enter to cycle
	newModel, _ = newModel.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := newModel.(views.Settings)

	// Should have cycled to standard
	assert.Equal(t, "standard", updated.CurrentSettings().Keybindings)
}

func TestSettings_LeftRightCycle(t *testing.T) {
	settings := views.SettingsData{
		LinkMethod:  domain.LinkSymlink,
		Keybindings: "vim",
	}

	model := views.NewSettings(settings)

	// Press right to cycle forward
	newModel, _ := model.Update(tea.KeyMsg{Type: tea.KeyRight})
	updated := newModel.(views.Settings)

	assert.Equal(t, domain.LinkHardlink, updated.CurrentSettings().LinkMethod)

	// Press left to cycle back
	newModel, _ = updated.Update(tea.KeyMsg{Type: tea.KeyLeft})
	updated = newModel.(views.Settings)

	assert.Equal(t, domain.LinkSymlink, updated.CurrentSettings().LinkMethod)
}

func TestSettings_ViewContainsCurrentValues(t *testing.T) {
	settings := views.SettingsData{
		LinkMethod:  domain.LinkHardlink,
		Keybindings: "standard",
	}

	model := views.NewSettings(settings)

	view := model.View()
	assert.Contains(t, view, "hardlink")
	assert.Contains(t, view, "standard")
}
