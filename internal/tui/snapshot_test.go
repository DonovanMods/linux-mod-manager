package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// TestGenerateThemeSnapshots regenerates the committed screen captures under
// docs/assets/tui. It only runs when UPDATE_TUI_SNAPSHOTS=1 so normal test
// runs never write into the repo.
//
// Coverage: all 4 themes x the dashboard (preserving the theme-comparison
// purpose the harness started with), plus the wizardry theme across every
// other screen, each at 80x24 and 120x36. The search screen is captured
// populated (search-ready) rather than in its initial idle state, mirroring
// the Task 4/6 test pattern (see populatedSearchPage in search_test.go).
//
// Deliberately not t.Parallel(): this test writes real files under
// docs/assets/tui and should run in isolation.
//
//	UPDATE_TUI_SNAPSHOTS=1 go test ./internal/tui -run TestGenerateThemeSnapshots
func TestGenerateThemeSnapshots(t *testing.T) {
	if os.Getenv("UPDATE_TUI_SNAPSHOTS") != "1" {
		t.Skip("set UPDATE_TUI_SNAPSHOTS=1 to regenerate theme snapshots")
	}

	outDir := filepath.Join("..", "..", "docs", "assets", "tui")
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	sizes := []struct{ width, height int }{{80, 24}, {120, 36}}

	// #224 Task 15 retired ScreenConflicts (its file-conflict reporting
	// folded into ScreenHealth's own table) and moved ScreenHealth into its
	// former slot 6 - "health" replaces the old "conflicts" slug entry
	// rather than being appended. This is a Go map (iteration order
	// undefined, and capture below looks entries up by key rather than
	// ranging over them), so what matters is that its KEY SET keeps tracking
	// navigation.go's screens slice - not the order these entries are
	// written in.
	slugs := map[Screen]string{
		ScreenDashboard:     "dashboard",
		ScreenInstalledMods: "installed-mods",
		ScreenSearch:        "search",
		ScreenProfiles:      "profiles",
		ScreenSources:       "sources",
		ScreenHealth:        "health",
	}

	// capture builds a model for themeName, sizes it, navigates to screen via
	// its number-key binding (navIndex+1, matching the sweep test in
	// app_test.go's TestScreenViewsFitHeightBudgetAtAllSizes — deriving the
	// key from the screens slice's own index rather than int(screen)+1 keeps
	// both tests correct even if Screen's iota values ever stop lining up
	// with the number-key bindings), and (for the search screen) populates it
	// with ready results before rendering the view.
	capture := func(themeName string, screen Screen, navIndex, width, height int) string {
		model, err := NewPrototypeModel(Options{Theme: themeName})
		require.NoError(t, err)

		// Run the init command if the model has one, so snapshots keep
		// capturing loaded data once async loading lands (Phase 3).
		if cmd := model.Init(); cmd != nil {
			loaded, _ := model.Update(cmd())
			model = loaded.(Model)
		}

		updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
		model = updated.(Model)

		if screen != ScreenDashboard {
			key := fmt.Sprintf("%d", navIndex+1)
			navigated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
			model = navigated.(Model)
		}

		if screen == ScreenSearch {
			model.search.state = searchReady
			model.search.page = populatedSearchPage()
		}

		return model.View()
	}

	write := func(name, view string) {
		require.NoError(t, os.WriteFile(filepath.Join(outDir, name), []byte(view+"\n"), 0o644))
	}

	for _, themeName := range []string{"wizardry", "amber", "dos", "green"} {
		for _, size := range sizes {
			view := capture(themeName, ScreenDashboard, 0, size.width, size.height)
			name := fmt.Sprintf("%s-%s-%dx%d.ansi", themeName, slugs[ScreenDashboard], size.width, size.height)
			write(name, view)
		}
	}

	for i, screen := range screens {
		if screen == ScreenDashboard {
			continue
		}
		for _, size := range sizes {
			view := capture("wizardry", screen, i, size.width, size.height)
			name := fmt.Sprintf("wizardry-%s-%dx%d.ansi", slugs[screen], size.width, size.height)
			write(name, view)
		}
	}
}
