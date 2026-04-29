package theme

import (
	"strconv"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func TestByNameReturnsPresets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		caseName string
		name     string
		want     string
	}{
		{caseName: "default", name: "", want: "wizardry"},
		{caseName: "wizardry", name: "wizardry", want: "wizardry"},
		{caseName: "amber", name: "amber", want: "amber"},
		{caseName: "dos", name: "dos", want: "dos"},
		{caseName: "green", name: "green", want: "green"},
		{caseName: "phosphor alias", name: "phosphor", want: "green"},
	}

	for _, tt := range tests {
		t.Run(tt.caseName, func(t *testing.T) {
			theme, err := ByName(tt.name)
			require.NoError(t, err)
			require.Equal(t, tt.want, theme.Name)
		})
	}
}

func TestByNameRejectsUnknownTheme(t *testing.T) {
	t.Parallel()

	_, err := ByName("cursed-rainbow")
	require.Error(t, err)
}

func TestDarkTerminalThemesKeepMutedTextReadable(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"wizardry", "amber", "green"} {
		t.Run(name, func(t *testing.T) {
			theme, err := ByName(name)
			require.NoError(t, err)
			require.NotEqual(t, theme.Background, theme.Muted)
			require.GreaterOrEqual(t, colorIndex(t, theme.Muted), 60)
		})
	}
}

func colorIndex(t *testing.T, color lipgloss.Color) int {
	t.Helper()

	index, err := strconv.Atoi(string(color))
	require.NoError(t, err)
	return index
}
