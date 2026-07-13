package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func TestDefaultKeyMapDocumentsPrototypeBindings(t *testing.T) {
	t.Parallel()

	keyMap := DefaultKeyMap()
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}, keyMap.Help))
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyCtrlC}, keyMap.Quit))
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyTab}, keyMap.NextScreen))
	require.True(t, key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}, keyMap.Search))
}
