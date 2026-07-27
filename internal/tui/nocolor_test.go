package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

// TestOptionsNoColorSuppressesEscapes is a differential test: it forces the
// GLOBAL lipgloss color profile to termenv.ANSI (so styles that emit color
// normally WOULD emit escapes even in this non-TTY test process — the repo's
// other styled-bytes tests dodge this by degrading to no color and using a
// Transform marker instead, see search_test.go's TestSearchDetailPaneStylesInstalledStatus),
// then proves that Options.NoColor overrides that pinned profile back down to
// termenv.Ascii for its own Model, regardless of what the process-global
// profile was set to.
//
// Deliberately NOT t.Parallel(): lipgloss's color profile is process-global
// renderer state (lipgloss.SetColorProfile mutates the single shared
// *Renderer every Style.Render call reads from), so running this alongside
// other tests that render styled output would race the profile out from
// under them (or from under this test). t.Cleanup restores Ascii — the
// package's ambient default in this non-TTY environment — so later tests in
// the same process see the same baseline they'd get without this test
// existing.
func TestOptionsNoColorSuppressesEscapes(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	baseline, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	loaded, _ := baseline.Update(baseline.Init()())
	sized, _ := loaded.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	baselineModel := sized.(Model)
	require.Contains(t, baselineModel.View(), "\x1b[",
		"sanity: with the profile pinned to ANSI, the baseline view must contain escape sequences")

	noColorModel, err := NewPrototypeModel(Options{Theme: "wizardry", NoColor: true})
	require.NoError(t, err)
	ncLoaded, _ := noColorModel.Update(noColorModel.Init()())
	ncSized, _ := ncLoaded.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	ncModel := ncSized.(Model)
	require.NotContains(t, ncModel.View(), "\x1b[",
		"Options.NoColor must suppress escape sequences even though the process-global profile is pinned to ANSI")
}
