package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// TestGenerateThemeSnapshots regenerates the committed theme captures under
// docs/assets/tui. It only runs when UPDATE_TUI_SNAPSHOTS=1 so normal test
// runs never write into the repo.
//
//	UPDATE_TUI_SNAPSHOTS=1 go test ./internal/tui -run TestGenerateThemeSnapshots
func TestGenerateThemeSnapshots(t *testing.T) {
	if os.Getenv("UPDATE_TUI_SNAPSHOTS") != "1" {
		t.Skip("set UPDATE_TUI_SNAPSHOTS=1 to regenerate theme snapshots")
	}

	outDir := filepath.Join("..", "..", "docs", "assets", "tui")
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	sizes := []struct{ width, height int }{{80, 24}, {120, 36}}
	for _, themeName := range []string{"wizardry", "amber", "dos", "green"} {
		for _, size := range sizes {
			model, err := NewPrototypeModel(Options{Theme: themeName})
			require.NoError(t, err)

			// Run the init command if the model has one, so snapshots keep
			// capturing loaded data once async loading lands (Phase 3).
			if cmd := model.Init(); cmd != nil {
				loaded, _ := model.Update(cmd())
				model = loaded.(Model)
			}

			updated, _ := model.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			view := updated.(Model).View()

			name := fmt.Sprintf("%s-%dx%d.ansi", themeName, size.width, size.height)
			require.NoError(t, os.WriteFile(filepath.Join(outDir, name), []byte(view+"\n"), 0o644))
		}
	}
}
