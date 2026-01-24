package tui_test

import (
	"testing"

	"lmm/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestKeyMap_VimMode(t *testing.T) {
	km := tui.NewKeyMap("vim")

	assert.True(t, km.IsUp(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}))
	assert.True(t, km.IsDown(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}))
	assert.True(t, km.IsLeft(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}}))
	assert.True(t, km.IsRight(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}}))
	assert.True(t, km.IsConfirm(tea.KeyMsg{Type: tea.KeyEnter}))
	assert.True(t, km.IsCancel(tea.KeyMsg{Type: tea.KeyEsc}))
	assert.True(t, km.IsQuit(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}))
}

func TestKeyMap_StandardMode(t *testing.T) {
	km := tui.NewKeyMap("standard")

	// Standard mode should still support arrow keys
	assert.True(t, km.IsUp(tea.KeyMsg{Type: tea.KeyUp}))
	assert.True(t, km.IsDown(tea.KeyMsg{Type: tea.KeyDown}))
	assert.True(t, km.IsLeft(tea.KeyMsg{Type: tea.KeyLeft}))
	assert.True(t, km.IsRight(tea.KeyMsg{Type: tea.KeyRight}))

	// But not vim keys for navigation
	assert.False(t, km.IsUp(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}))
	assert.False(t, km.IsDown(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}))
}

func TestKeyMap_ArrowKeysWorkInBothModes(t *testing.T) {
	vimKm := tui.NewKeyMap("vim")
	stdKm := tui.NewKeyMap("standard")

	// Arrow keys should work in both modes
	upKey := tea.KeyMsg{Type: tea.KeyUp}
	downKey := tea.KeyMsg{Type: tea.KeyDown}

	assert.True(t, vimKm.IsUp(upKey))
	assert.True(t, stdKm.IsUp(upKey))
	assert.True(t, vimKm.IsDown(downKey))
	assert.True(t, stdKm.IsDown(downKey))
}

func TestKeyMap_Help(t *testing.T) {
	vimKm := tui.NewKeyMap("vim")
	stdKm := tui.NewKeyMap("standard")

	vimHelp := vimKm.NavigationHelp()
	stdHelp := stdKm.NavigationHelp()

	assert.Contains(t, vimHelp, "j/k")
	assert.Contains(t, stdHelp, "↑/↓")
}

func TestKeyMap_DefaultsToVim(t *testing.T) {
	km := tui.NewKeyMap("")

	// Should default to vim mode
	assert.True(t, km.IsUp(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}}))
}
