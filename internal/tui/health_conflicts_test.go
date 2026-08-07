package tui

import (
	"context"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

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
func (f conflictsFakeProvider) Health(context.Context) (HealthView, error) { return HealthView{}, nil }

// TestDashboardConflictCountWired proves Summary.Conflicts - populated from
// the same Conflicts() fetch the Health screen's own table now renders, via
// loadData's refresh cycle - reaches the dashboard's conflict count, which
// used to be permanently stuck at the "?" sentinel (conflict detection had
// no query to back it before Task 2/3). Migrated unchanged from the retired
// conflicts_view_test.go (#224 Task 15): this test's premise (the dashboard
// count is real data, independent of which screen renders it) is untouched
// by the conflicts fold.
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
// the 80x12 terminal budget when displayed in the Health table.
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
func (longConflictsProvider) Health(context.Context) (HealthView, error) { return HealthView{}, nil }

// TestHealthConflictRowsFollowSelectionOnShortTerminals migrates the retired
// conflicts_view_test.go's TestConflictsListFollowsSelectionOnShortTerminals
// (#224 Task 15): conflict-only rows (no findings) still use
// healthTableRows' scroll-follow-selection windowing - the selected row
// stays visible when navigating past the fold, instead of vanishing
// off-screen (#42). "6" is now the Health screen's own jump key (moved from
// "7", now that ScreenConflicts no longer occupies slot 6 - see
// navigation.go).
//
// Unlike the original 80x12 size (fine for the old dedicated two-pane
// Conflicts screen, which gave its list pane nearly the whole panel), this
// uses 100x30: the folded Health layout's fixed header+detail-strip
// overhead (up to 7 lines - healthHomeView's own header/detailCap) leaves
// too little of an 80x12 budget for the table to show anything at all
// (clampLines' own "+N more" safety net swallows it whole) - 100x30 still
// forces windowedRows' scroll-follow path for 20 rows while leaving enough
// budget for the mechanism to actually be observable.
func TestHealthConflictRowsFollowSelectionOnShortTerminals(t *testing.T) {
	t.Parallel()
	model, err := NewModel(Options{Theme: "wizardry", Provider: longConflictsProvider{}})
	require.NoError(t, err)

	loaded, _ := model.Update(model.Init()())
	updated, _ := loaded.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)

	model = updateWithRunes(t, model, "6") // jump to the Health screen

	last := model.healthTotalRows() - 1
	for i := 0; i < last; i++ {
		model = updateWithMsg(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}
	require.Equal(t, last, model.selected[ScreenHealth])

	view := model.screenView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())
	require.Contains(t, view, "> ", "the selected conflict row must be visible")
}

// TestHealthKeySwallowedByFocusedSearchInput migrates the retired
// conflicts_view_test.go's TestConflictsKeySwallowedByFocusedSearchInput
// (#224 Task 15): "6" is dispatched AFTER updateKey's focused-input branch
// (mirroring every other direct screen-jump key, e.g.
// TestNumberKeysJumpToScreens) - while the search input is focused, "6" must
// be typed into the query, not jump screens. "6" is now HealthScreen's own
// binding (moved from "7").
func TestHealthKeySwallowedByFocusedSearchInput(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	model = updateWithRunes(t, model, "/")
	require.True(t, model.search.input.Focused())

	model = updateWithRunes(t, model, "6")
	require.Equal(t, ScreenSearch, model.CurrentScreen(), "a focused input must swallow '6', not navigate")
	require.Equal(t, "6", model.search.input.Value())
}

// conflictsHealthFixture is the two conflicts used across this file's
// table/detail-strip tests: one stale (deployed owner disagrees with the
// load-order winner), one in-sync.
func conflictsHealthFixture() []ConflictItem {
	return []ConflictItem{
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
	}
}

// TestHealthTableIncludesConflictRowsAfterFindings is the RED-then-GREEN
// proof for requirement 1 of #224 Task 15's fold: conflict rows join the
// Health table AFTER the verify findings, one row per conflicted file,
// STATUS "CONFLICT" (warning tint) or "STALE CONFLICT" (danger tint), MOD
// the load-order winner, FILE the contested path, VERSION blank.
func TestHealthTableIncludesConflictRowsAfterFindings(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.health = HealthView{Findings: []HealthFinding{
		{ModID: "a", ModName: "A", FileID: "f1", Status: "missing"},
	}}
	model.conflicts = conflictsHealthFixture()

	rows := model.healthTableRows(160, 10)
	require.Len(t, rows, 3, "1 finding + 2 conflicts")

	require.Contains(t, rows[0], "MISSING", "the finding row still renders first")

	// Assert on a path prefix rather than the exact full string - FILE now
	// flexes proportionally (#224 smoke feedback fix #3, healthColumnWidths'
	// own doc comment) instead of a fixed 18-rune cap, so whether either
	// fixture path below truncates at width 160 is an implementation detail
	// this test shouldn't pin down.
	require.Contains(t, rows[1], "STALE CONFLICT")
	require.Contains(t, rows[1], "USSEP", "MOD column shows the load-order winner")
	require.Contains(t, rows[1], "meshes/armor", "FILE column shows the contested path")
	require.NotContains(t, rows[1], "Immersive Armors", "MOD column shows the winner, not the owner")

	require.Contains(t, rows[2], "CONFLICT")
	require.NotContains(t, rows[2], "STALE CONFLICT")
	require.Contains(t, rows[2], "USSEP")
	require.Contains(t, rows[2], "textures/frost")

	// VERSION column is always blank for a conflict row - conflicts carry no
	// version data.
	_, _, _, _, _, showVersion, _ := model.healthColumnWidths(160)
	require.True(t, showVersion, "sanity: this width must show the VERSION column")
	require.NotContains(t, rows[1], "→", "a conflict row's VERSION column has no recorded/effective arrow")

	// Tints: STALE CONFLICT renders with DangerText, plain CONFLICT with
	// WarningText - mirroring TestHealthFixedStatusesRenderAsResolved's own
	// direct-function-call technique, since ANSI color doesn't survive this
	// package's non-TTY test renders as an observable substring.
	require.Equal(t, model.theme.DangerText, model.conflictRowStyle(conflictsHealthFixture()[0]), "a stale conflict must render with the danger tint")
	require.Equal(t, model.theme.WarningText, model.conflictRowStyle(conflictsHealthFixture()[1]), "an in-sync conflict must render with the warning tint")
}

// TestHealthTableConflictNoteColumn proves the NOTE column's copy for both
// conflict variants (requirement 1's literal wording), truncated to
// healthColumnWidths' bounded NOTE width (#224 smoke feedback fix #5 - NOTE
// is now bounded near its own typical content rather than left to grow
// with the panel, so a full conflictNoteText sentence clips in the table;
// TestHealthConflictDetailStripShowsOwnerAlsoInAndHint proves the detail
// strip below still carries the full, untruncated text).
func TestHealthTableConflictNoteColumn(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.conflicts = conflictsHealthFixture()

	rows := model.healthTableRows(160, 10)
	require.Len(t, rows, 2)
	require.Contains(t, rows[0], "load order says USSEP")
	require.Contains(t, rows[0], "…", "the full remedy sentence must truncate in the bounded NOTE column")
	require.Contains(t, rows[1], "owned by USSEP")
	require.Contains(t, rows[1], "…", "the full remedy sentence must truncate in the bounded NOTE column")
}

// TestHealthConflictDetailStripShowsOwnerAlsoInAndHint proves requirement
// 1's detail-strip contract: selecting a conflict row shows the full data
// including the "Also in: …" alternates list, and the stale-aware hint -
// migrated from the retired conflicts_view_test.go's
// TestConflictsScreenRendersRows (its detail-pane assertions), now exercised
// through the Health screen's shared detail strip.
func TestHealthConflictDetailStripShowsOwnerAlsoInAndHint(t *testing.T) {
	t.Parallel()

	provider := conflictsFakeProvider{conflicts: conflictsHealthFixture()}
	model := modelWithProvider(t, provider)
	model.screen = ScreenHealth
	resized, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	model = resized.(Model)

	view := model.View()
	require.Contains(t, view, "meshes/armor/steel_helmet.nif")
	require.Contains(t, view, "textures/frost.dds")

	// Row 0 (stale) is selected by default: detail strip shows the owner,
	// the AlsoIn alternates, and the stale hint naming the winner.
	require.Contains(t, view, "Immersive Armors", "the detail strip must show the deployed owner")
	require.Contains(t, view, "Also in: USSEP")
	require.Contains(t, view, "load order says USSEP should win — deploy (D) to apply")

	// Selecting row 1 (in-sync) must switch the detail strip's content.
	model.selected[ScreenHealth] = 1
	view = model.View()
	require.Contains(t, view, "Also in: Frostfall")
	require.Contains(t, view, "reorder mods (J/K on installed) to change the winner")
}

// TestDeployKeyFiresFromHealthScreen migrates the retired
// conflicts_view_test.go's TestDeployKeyFiresFromConflictsScreen (#224 Task
// 15, requirement 2): the stale-conflict remedy copy names "deploy (D) to
// apply", so the deploy key must actually fire from the Health screen (with
// nothing pushed) - same confirmation modal and machinery as Dashboard/
// Installed Mods.
func TestDeployKeyFiresFromHealthScreen(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth

	updated, _ := model.Update(keyRunes("D"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending, "D on the Health screen must open the deploy confirmation modal")
	require.Equal(t, actionDeploy, model.action.pending.kind)
}

// TestDeployKeyDeclinedWithPushedContentOnHealth proves the Health-screen
// deploy guard's "no pushed context" half (mirroring FullCheck/FixHealth's
// own compound guards): 'D' while ScreenHealth has pushed context content
// must not open the deploy confirmation.
func TestDeployKeyDeclinedWithPushedContentOnHealth(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	// #86: pushContext no longer forces the screen to ScreenHealth, so this
	// starts there directly - deployActiveProfile's own guard checks
	// m.contextContent == nil only for ScreenHealth (mutations.go), and
	// starting anywhere else wouldn't exercise that guard clause at all.
	model.screen = ScreenHealth

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line"}}
	model.pushContext(fake)
	require.Equal(t, ScreenHealth, model.CurrentScreen())

	updated, _ := model.Update(keyRunes("D"))
	model = updated.(Model)
	require.Nil(t, model.action.pending, "'D' must not open the deploy modal while context content is pushed")
}

// TestHealthSelectionSpansFindingsAndConflicts proves requirement 1's
// selection contract: itemCount/moveSelection walk findings first, then
// conflicts, as one combined list.
func TestHealthSelectionSpansFindingsAndConflicts(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.health = HealthView{Findings: []HealthFinding{
		{ModID: "a", ModName: "A", Status: "missing"},
		{ModID: "b", ModName: "B", Status: "missing"},
	}}
	model.conflicts = conflictsHealthFixture()
	model.screen = ScreenHealth

	require.Equal(t, 4, model.itemCount(ScreenHealth), "2 findings + 2 conflicts")
	require.Equal(t, 4, model.healthTotalRows())

	for i := 0; i < 3; i++ {
		model = updateWithMsg(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}
	require.Equal(t, 3, model.selected[ScreenHealth], "selection must walk past the findings into the conflicts")

	detail := model.healthDetailPane(80, 20)
	require.Contains(t, detail, "textures/frost.dds", "the selection must land on the second conflict row's own detail")

	// Selection must not walk past the combined end.
	model = updateWithMsg(t, model, tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, 3, model.selected[ScreenHealth], "selection must clamp at the combined list's end")
}

// TestHealthHeaderAppendsConflictCount proves requirement 4: the header
// appends ", M conflict(s)" after "— N checked" whenever M > 0, and omits
// it entirely when there are no conflicts.
func TestHealthHeaderAppendsConflictCount(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	sized, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	model = sized.(Model)
	model.state = stateReady
	model.health = HealthView{Checked: 3, Findings: []HealthFinding{
		{ModID: "a", ModName: "A", Status: "ok"},
	}}
	model.screen = ScreenHealth

	require.NotContains(t, model.View(), "conflict(s)", "no conflicts means no conflict-count suffix")

	model.conflicts = conflictsHealthFixture()
	require.Contains(t, model.View(), "2 conflict(s)")
}

// TestHealthEmptyStateRequiresBothFindingsAndConflictsEmpty proves
// requirement 5: the empty state only fires when BOTH findings and
// conflicts are empty - a profile with conflicts but no findings (or vice
// versa) must still render the table.
func TestHealthEmptyStateRequiresBothFindingsAndConflictsEmpty(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	sized, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	model = sized.(Model)
	model.state = stateReady
	model.screen = ScreenHealth
	model.health = HealthView{} // no findings

	require.Contains(t, model.View(), "no findings", "both empty: the empty state must fire")

	model.conflicts = conflictsHealthFixture()
	view := model.View()
	require.NotContains(t, view, "no findings", "conflicts alone must be enough to skip the empty state")
	require.Contains(t, view, "STALE CONFLICT")
}
