package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

// overlayTestModel builds a fully-loaded, sized prototype Model with no
// pending action/picker/input modal/overlay - the common starting point for
// every test below.
func overlayTestModel(t *testing.T) Model {
	t.Helper()
	return sizedPrototypeModel(t, "wizardry", 100, 30)
}

func TestOverlayEscCloses(t *testing.T) {
	t.Parallel()

	model := overlayTestModel(t).promptOverlay(infoOverlay{
		title: "Deployed Files",
		lines: []string{"Data/SkyUI.esp"},
	})
	require.NotNil(t, model.overlay)

	model = updateWithKeyType(t, model, tea.KeyEsc)

	require.Nil(t, model.overlay)
}

// TestOverlayFKeyCloses locks in the second documented close key: unlike
// esc (Blur), "f" is overlay-specific - it must also dismiss the overlay,
// not just open it (Task 4 wires the open side).
func TestOverlayFKeyCloses(t *testing.T) {
	t.Parallel()

	model := overlayTestModel(t).promptOverlay(infoOverlay{
		title: "Deployed Files",
		lines: []string{"Data/SkyUI.esp"},
	})
	require.NotNil(t, model.overlay)

	model = updateWithRunes(t, model, "f")

	require.Nil(t, model.overlay)
}

// TestOverlayClosesOnRemappedFilesKey guards Copilot PR #69's finding on
// updateOverlayKey: the close side used to hard-code msg.String() == "f"
// while the OPEN side matches m.keys.Files - a custom KeyMap remapping
// Files would desync the toggle (the new key opens, only the old literal
// closes). Both sides must consult the same binding.
func TestOverlayClosesOnRemappedFilesKey(t *testing.T) {
	t.Parallel()

	model := overlayTestModel(t)
	model.keys.Files = key.NewBinding(key.WithKeys("F"), key.WithHelp("F", "files"))
	model = model.promptOverlay(infoOverlay{
		title: "Deployed Files",
		lines: []string{"Data/SkyUI.esp"},
	})
	require.NotNil(t, model.overlay)

	// The now-unbound old literal must NOT close it...
	model = updateWithRunes(t, model, "f")
	require.NotNil(t, model.overlay, "an unbound key must be swallowed, not treated as the Files toggle")

	// ...and the remapped binding must.
	model = updateWithRunes(t, model, "F")
	require.Nil(t, model.overlay, "the remapped Files binding must close the overlay it opens")
}

// TestOverlayQuitKeyQuits locks in that, unlike the input modal (which must
// let a plain "q" be typed), the overlay has no text entry, so a plain "q"
// still quits - matching list-screen behavior.
func TestOverlayQuitKeyQuits(t *testing.T) {
	t.Parallel()

	model := overlayTestModel(t).promptOverlay(infoOverlay{
		title: "Deployed Files",
		lines: []string{"Data/SkyUI.esp"},
	})
	require.NotNil(t, model.overlay)

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	require.NotNil(t, cmd)
	require.Equal(t, tea.Quit(), cmd(), "plain q must quit while the overlay is open")
}

// TestOverlayBlockedWhileAnotherModalOpen locks in the single-flight guard
// promptOverlay's doc comment describes: a picker/input modal/action modal
// already up refuses a second overlay.
func TestOverlayBlockedWhileAnotherModalOpen(t *testing.T) {
	t.Parallel()

	model := overlayTestModel(t)
	model.action.pending = &pendingAction{title: "Some action"}

	model = model.promptOverlay(infoOverlay{title: "Deployed Files", lines: []string{"a"}})

	require.Nil(t, model.overlay)
}

func TestOverlayRendersTitleAndLines(t *testing.T) {
	t.Parallel()

	longLine := strings.Repeat("x", 200)
	model := sizedPrototypeModel(t, "wizardry", 40, 30).promptOverlay(infoOverlay{
		title: "Deployed Files",
		lines: []string{"Data/SkyUI.esp", longLine},
	})

	view := model.overlayView()

	require.Contains(t, view, "Deployed Files")
	require.Contains(t, view, "Data/SkyUI.esp")
	require.Contains(t, view, "esc close")
	require.NotContains(t, view, longLine, "a line wider than the panel must be truncated, not wrapped")
}

// TestOverlayCapsLines pins the exact-height render invariant for an
// overlay taller than the panel: it is not scrollable (YAGNI), so overflow
// collapses into a single dimmed "+N more" tail line, mirroring
// actionModalView's cap style - never the scroll-follow-selection window
// pickerView uses (there's no selection to follow).
func TestOverlayCapsLines(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 12) // forces the 8-line content floor
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("file-%02d.esp", i+1)
	}
	model = model.promptOverlay(infoOverlay{title: "Deployed Files", lines: lines})
	require.NotNil(t, model.overlay)

	view := model.overlayView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight(),
		"overlay must never render taller than the content budget")
	require.Contains(t, view, "more", "clipped lines are named by an indicator line")
}
