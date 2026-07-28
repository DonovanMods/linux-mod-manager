package tui

import (
	"context"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

// TestConflictsScreenNavigation proves ScreenConflicts is a fully wired
// screen (task-3-brief.md): the "6" direct-jump binding reaches it, tab
// cycling from the last existing screen (Sources) rotates onto it, and its
// String() renders a real name rather than navigation.go's Screen(N)
// fallback.
func TestConflictsScreenNavigation(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	updated := updateWithRunes(t, model, "6")
	require.Equal(t, ScreenConflicts, updated.CurrentScreen())

	// Tab-rotation: Sources (5) -> Conflicts (6) -> back to Dashboard (1).
	onSources := updateWithRunes(t, model, "5")
	require.Equal(t, ScreenSources, onSources.CurrentScreen())
	onConflicts := updateWithKeyType(t, onSources, tea.KeyTab)
	require.Equal(t, ScreenConflicts, onConflicts.CurrentScreen())
	onDashboard := updateWithKeyType(t, onConflicts, tea.KeyTab)
	require.Equal(t, ScreenDashboard, onDashboard.CurrentScreen())

	require.Equal(t, "Conflicts", ScreenConflicts.String())
}

// TestConflictsKeySwallowedByFocusedSearchInput proves "6" is dispatched
// AFTER updateKey's focused-input branch (mirroring every other direct
// screen-jump key, e.g. TestNumberKeysJumpToScreens): while the search input
// is focused, "6" must be typed into the query, not jump screens.
func TestConflictsKeySwallowedByFocusedSearchInput(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	model = updateWithRunes(t, model, "/")
	require.True(t, model.search.input.Focused())

	model = updateWithRunes(t, model, "6")
	require.Equal(t, ScreenSearch, model.CurrentScreen(), "a focused input must swallow '6', not navigate")
	require.Equal(t, "6", model.search.input.Value())
}

// conflictsFakeProvider is a minimal DataProvider double letting these tests
// supply canned Conflicts() rows without a real core.Service - mirrors
// recordingProvider's DeployedFilesResult/-Err shape (app_test.go), narrowed
// to just this one method plus the boilerplate every DataProvider fake
// needs.
type conflictsFakeProvider struct {
	summary   Summary
	conflicts []ConflictItem
}

func (f conflictsFakeProvider) Overview(context.Context) (Summary, []ModItem, error) {
	return f.summary, nil, nil
}
func (f conflictsFakeProvider) Profiles(context.Context) ([]ProfileItem, error) { return nil, nil }
func (f conflictsFakeProvider) Sources() []string                               { return nil }
func (f conflictsFakeProvider) SourceInfos(bool) []SourceInfo                   { return nil }
func (f conflictsFakeProvider) Search(context.Context, string, string, int, int) (SearchPage, error) {
	return SearchPage{}, nil
}
func (f conflictsFakeProvider) DeployedFiles(string, string) ([]string, error) { return nil, nil }
func (f conflictsFakeProvider) ListGames() ([]GameInfo, error)                 { return nil, nil }
func (f conflictsFakeProvider) Conflicts(context.Context) ([]ConflictItem, error) {
	return f.conflicts, nil
}

// TestConflictsScreenRendersRows covers the non-empty path: one stale and
// one in-sync conflict. The list pane must show FILE/OWNER/WINNER columns
// with a stale marker distinguishing the two rows; the detail pane for
// whichever row is selected must show AlsoIn plus the row's hint copy -
// task-3-brief.md's copy is exact and must match verbatim.
func TestConflictsScreenRendersRows(t *testing.T) {
	t.Parallel()

	provider := conflictsFakeProvider{
		conflicts: []ConflictItem{
			{
				Path:   "meshes/armor/steel_helmet.nif",
				Owner:  "Immersive Armors",
				Winner: "USSEP",
				AlsoIn: []string{"USSEP"},
				Stale:  true,
			},
			{
				Path:   "textures/frost.dds",
				Owner:  "USSEP",
				Winner: "USSEP",
				AlsoIn: []string{"Frostfall"},
				Stale:  false,
			},
		},
	}
	model := modelWithProvider(t, provider)
	model.screen = ScreenConflicts
	// Wide enough that the list pane's FILE column doesn't truncate the
	// canned paths below - the default unsized 76-column width leaves too
	// little room for FILE+OWNER+WINNER side by side with a real path.
	resized, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	model = resized.(Model)

	view := model.View()
	require.Contains(t, view, "FILE")
	require.Contains(t, view, "OWNER")
	require.Contains(t, view, "WINNER")
	require.Contains(t, view, "meshes/armor/steel_helmet.nif")
	require.Contains(t, view, "textures/frost.dds")

	// Row 0 (stale) is selected by default: detail pane shows AlsoIn and the
	// stale hint naming the winner.
	require.Contains(t, view, "USSEP")
	require.Contains(t, view, "load order says USSEP should win — deploy (D) to apply")

	// Selecting row 1 (in-sync) must switch the detail pane's hint copy.
	model.selected[ScreenConflicts] = 1
	view = model.View()
	require.Contains(t, view, "Frostfall")
	require.Contains(t, view, "reorder mods (J/K on installed) to change the winner")
}

// TestConflictsScreenEmptyState covers a profile with no conflicts (e.g.
// never deployed - GetProfileConflicts' own doc comment: ownership only
// comes from deployed_files, so a never-deployed profile reports none). The
// copy deliberately does not promise pre-deploy detection.
func TestConflictsScreenEmptyState(t *testing.T) {
	t.Parallel()

	model := modelWithProvider(t, conflictsFakeProvider{})
	model.screen = ScreenConflicts

	require.Contains(t, model.View(), "No conflicts detected.")
}

// TestDeployKeyFiresFromConflictsScreen covers the review fix wave: the
// stale-conflict hint copy names "deploy (D) to apply", so the deploy key
// must actually fire from this screen - same confirmation modal and
// machinery as Dashboard/Installed Mods (mirrors
// TestDeployKeyFromInstalledModsPrompts' own shape).
func TestDeployKeyFiresFromConflictsScreen(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenConflicts

	updated, _ := model.Update(keyRunes("D"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending, "D on the Conflicts screen must open the deploy confirmation modal")
	require.Equal(t, actionDeploy, model.action.pending.kind)
}

// TestDashboardConflictCountWired proves Summary.Conflicts - populated from
// the same Conflicts() fetch the Conflicts screen itself renders, via
// loadData's refresh cycle - reaches the dashboard's conflict count, which
// used to be permanently stuck at the "?" sentinel (conflict detection had
// no query to back it before Task 2/3).
func TestDashboardConflictCountWired(t *testing.T) {
	t.Parallel()

	provider := conflictsFakeProvider{
		summary: Summary{GameName: "Game", ProfileName: "default", Updates: -1, Conflicts: -1},
		conflicts: []ConflictItem{
			{Path: "a.esp", Owner: "A", Winner: "A"},
			{Path: "b.esp", Owner: "B", Winner: "B"},
		},
	}
	model := modelWithProvider(t, provider)

	require.Equal(t, 2, model.summary.Conflicts, "the dashboard count must come from the real Conflicts() fetch, not the provider's own Overview sentinel")
	require.Contains(t, model.dashboardView(), "2 file conflict")
	require.NotContains(t, model.dashboardView(), "? file conflict", "the '?' placeholder must be replaced once a real count is known")
}

// longConflictsProvider returns conflicts with enough entries to overflow
// the 80x12 terminal budget when displayed in the conflicts list pane.
type longConflictsProvider struct{}

func (longConflictsProvider) Overview(context.Context) (Summary, []ModItem, error) {
	return Summary{}, nil, nil
}
func (longConflictsProvider) Profiles(context.Context) ([]ProfileItem, error) { return nil, nil }
func (longConflictsProvider) Sources() []string                               { return nil }
func (longConflictsProvider) SourceInfos(bool) []SourceInfo                   { return nil }
func (longConflictsProvider) Search(context.Context, string, string, int, int) (SearchPage, error) {
	return SearchPage{}, nil
}
func (longConflictsProvider) DeployedFiles(string, string) ([]string, error) { return nil, nil }
func (longConflictsProvider) ListGames() ([]GameInfo, error)                 { return nil, nil }
func (longConflictsProvider) Conflicts(context.Context) ([]ConflictItem, error) {
	// Generate 20 conflicts to overflow the 80x12 budget (12 lines total,
	// minus title + header = 10 lines available, easily exceeded by 20 items).
	conflicts := make([]ConflictItem, 20)
	for i := 0; i < 20; i++ {
		conflicts[i] = ConflictItem{
			Path:   fmt.Sprintf("file_%02d.nif", i),
			Owner:  fmt.Sprintf("Mod%d", i%5),
			Winner: fmt.Sprintf("Winner%d", (i+1)%5),
			AlsoIn: []string{"OtherMod"},
			Stale:  i%2 == 0,
		}
	}
	return conflicts, nil
}

// TestConflictsListFollowsSelectionOnShortTerminals proves conflictsListPane
// uses scroll-follow-selection windowing: the selected row stays visible when
// navigating past the fold, instead of the old first-N slice behavior where
// the highlight simply vanished off-screen (#42).
func TestConflictsListFollowsSelectionOnShortTerminals(t *testing.T) {
	t.Parallel()
	model, err := NewModel(Options{Theme: "wizardry", Provider: longConflictsProvider{}})
	require.NoError(t, err)

	loaded, _ := model.Update(model.Init()())
	updated, _ := loaded.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	model = updated.(Model)

	model = updateWithRunes(t, model, "6") // jump to conflicts screen

	last := len(model.conflicts) - 1
	for i := 0; i < last; i++ {
		model = updateWithMsg(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}
	require.Equal(t, last, model.selected[ScreenConflicts])

	view := model.screenView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())
	require.Contains(t, view, "> ", "selected conflict must be visible")
}
