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

func TestThemeChromeUsesTerminalDefaultBackground(t *testing.T) {
	t.Parallel()

	theme, err := ByName("wizardry")
	require.NoError(t, err)

	assertNoBackground(t, theme.App)
	assertNoBackground(t, theme.Title)
	assertNoBackground(t, theme.Panel)
	assertNoBackground(t, theme.PanelTitle)
	assertNoBackground(t, theme.MutedText)
	assertNoBackground(t, theme.Help)

	require.IsType(t, lipgloss.Color(""), theme.Selected.GetBackground())
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

func assertNoBackground(t *testing.T, style lipgloss.Style) {
	t.Helper()

	require.IsType(t, lipgloss.NoColor{}, style.GetBackground())
}

func colorIndex(t *testing.T, color lipgloss.Color) int {
	t.Helper()

	index, err := strconv.Atoi(string(color))
	require.NoError(t, err)
	return index
}

func TestStatusTextStylesMatchStatusColors(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"wizardry", "amber", "dos", "green"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			th, err := ByName(name)
			require.NoError(t, err)

			require.Equal(t, th.Warning, th.WarningText.GetForeground(), "WarningText foreground")
			require.Equal(t, th.Danger, th.DangerText.GetForeground(), "DangerText foreground")
			require.Equal(t, th.Background, th.WarningText.GetBackground(), "WarningText background")
			require.Equal(t, th.Background, th.DangerText.GetBackground(), "DangerText background")
			require.True(t, th.WarningText.GetBold())
			require.True(t, th.DangerText.GetBold())
		})
	}
}
