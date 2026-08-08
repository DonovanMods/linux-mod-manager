package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

// TestHelpViewListsPerScreenGroups proves Task 9's restructure: the help
// panel documents every binding added across Tasks 4-8 (files, policy,
// purge, game switch, profile create/delete) grouped by the screen each
// applies to, with "global" first. Zero-size model (no WindowSizeMsg) keeps
// helpBodyBudget() at its generous unsized default so the full group list
// renders uncapped - this test is about content, not the height invariant
// (see TestViewFitsTerminalBoundsWithHelpVisible for that).
func TestHelpViewListsPerScreenGroups(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.showHelp = true
	view := model.helpView()

	for _, want := range []string{
		"global", "dashboard", "installed mods", "search", "profiles", "health",
		"files", "policy", "purge", "game", "new profile", "delete profile",
		// changelog is fix-wave-2 finding #1's list-scoped changelog viewer
		// ('v' on Installed Mods, outside any modal - viewSelectedModChangelog,
		// mutations.go), added to the installed-mods group.
		"changelog",
	} {
		require.Contains(t, view, want, "missing %q", want)
	}
}

// TestHelpViewCurrentScreenGroupFirst proves the current screen's group is
// promoted to immediately after "global", while the rest keep their fixed
// relative order - so a Profiles-screen user sees their own bindings first,
// not buried after Installed Mods' longer list.
func TestHelpViewCurrentScreenGroupFirst(t *testing.T) {
	t.Parallel()

	onProfiles, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	onProfiles.screen = ScreenProfiles
	onProfiles.showHelp = true
	profilesView := onProfiles.helpView()
	profilesIdx := strings.Index(profilesView, "profiles")
	installedIdx := strings.Index(profilesView, "installed mods")
	require.NotEqual(t, -1, profilesIdx, "profiles header missing")
	require.NotEqual(t, -1, installedIdx, "installed mods header missing")
	require.Less(t, profilesIdx, installedIdx, "profiles group should render before installed mods when on Profiles")

	onInstalled, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	onInstalled.screen = ScreenInstalledMods
	onInstalled.showHelp = true
	installedView := onInstalled.helpView()
	installedIdx = strings.Index(installedView, "installed mods")
	profilesIdx = strings.Index(installedView, "profiles")
	require.NotEqual(t, -1, installedIdx, "installed mods header missing")
	require.NotEqual(t, -1, profilesIdx, "profiles header missing")
	require.Less(t, installedIdx, profilesIdx, "installed mods group should render before profiles when on Installed Mods")
}

// TestHelpViewHealthGroupPromotedOnHealthScreen mirrors
// TestHelpViewCurrentScreenGroupFirst for the "health" group: a
// Health-screen user (nothing pushed) sees it promoted to immediately
// follow "global", ahead of the fixed dashboard/installed mods/search/
// profiles order. Formerly TestHelpViewConflictsGroupPromotedOnConflictsScreen,
// which proved the same promotion for the standalone Conflicts screen's own
// "conflicts" group before #224 Task 15 retired that screen and folded its
// group's still-relevant content (Up/Down/Deploy) into "health" - see
// helpGroups' own doc comment on the health group.
func TestHelpViewHealthGroupPromotedOnHealthScreen(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.screen = ScreenHealth
	model.showHelp = true

	view := model.helpView()
	healthIdx := strings.Index(view, "health")
	dashboardIdx := strings.Index(view, "dashboard")
	require.NotEqual(t, -1, healthIdx, "health header missing")
	require.NotEqual(t, -1, dashboardIdx, "dashboard header missing")
	require.Less(t, healthIdx, dashboardIdx, "health group should render before dashboard when on Health")
}

// TestHelpViewCapsWithMoreTailAtSmallHeight exercises the height-capped
// path the other help tests never reach (their budgets exceed the full
// grouped list by construction): at 80x24 helpBodyBudget is far below the
// body's natural size, so helpView must cap the list with a dimmed
// "+N more" tail AND the whole View() must still occupy the terminal
// bounds exactly - the same invariant TestViewFitsTerminalBoundsWithHelpVisible
// pins at its uncapped zero-slack height. ScreenProfiles is used because
// its screen view genuinely shrinks to availableContentHeight's 8-row
// floor (the floor helpBodyBudget reserves); the party-sheet Dashboard and
// a populated Installed Mods list have natural minimums above that floor
// and overflow at this size with or without the help panel open - a
// pre-existing limitation, not the cap's.
//
// Negative control (recorded in task-9-report.md): temporarily inflating
// helpBodyBudget by +10 makes this test FAIL (view grows past 24 rows),
// proving it guards the cap arithmetic, not just the tail copy.
func TestHelpViewCapsWithMoreTailAtSmallHeight(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 80, 24)
	model.screen = ScreenProfiles
	model = updateWithRunes(t, model, "?")

	view := model.View()
	require.Regexp(t, `\+\d+ more`, view, "capped help must end in a '+N more' tail")
	require.Equal(t, 80, lipgloss.Width(view))
	require.Equal(t, 24, lipgloss.Height(view))
}

// TestFooterMentionsHelpKey pins "?" as the discovery point for help: the
// footer must always name it, even as the panel it opens grows with new
// bindings.
func TestFooterMentionsHelpKey(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	require.Contains(t, model.footerLine(), "?: help")
}

// TestHelpViewEnterRowsDisambiguated is smoke round 2's finding 2, checked
// against the CONTENT (an unsized model, helpBodyBudget's generous unsized
// default - see TestHelpViewListsPerScreenGroups' own doc comment for why
// that's the right size to assert content against): the "installed mods"
// group used to render Select via helpEntry, which falls back to keys.go's
// generic "enter"/"open" and never said WHAT opened; the "search" group
// listed Submit ("enter: search") and Select ("enter: open") side by side
// with the same key and no indication of which state each applies to.
// Installed Mods now hand-writes Select's row like the dashboard's ("open
// menu entry") and profiles' ("switch profile") groups already do for this
// exact problem; Search hand-writes BOTH Submit's and Select's rows, each
// naming its own state (Submit fires only while the query input is
// focused; Select only once it's blurred with a result selected - see
// openSelectedModDetails).
func TestHelpViewEnterRowsDisambiguated(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.showHelp = true
	view := model.helpView()

	require.Contains(t, view, "open mod details",
		"installed mods and search help groups must say what enter opens")
	require.Contains(t, view, "query input focused",
		"search help group's submit row must say it only applies while the input is focused")
}

// TestHelpViewInstalledModsEnterRowSurvivesAtNormalSize proves the Installed
// Mods "enter: open mod details" row actually reaches the rendered panel at
// a normal terminal size (100x30), not just in helpGroups' unrendered data -
// it's the group's first entry, so helpBodyBudget's "+N more" tail-collapse
// (which starts biting well before the full grouped list at this size, per
// TestHelpViewSearchEnterRowSurvivesCollapseAtNormalSize below) never
// reaches it - and since #234 it is also marked kept(), so it would survive
// even if later entries were reordered above it.
func TestHelpViewInstalledModsEnterRowSurvivesAtNormalSize(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 30)
	model.screen = ScreenInstalledMods
	model = updateWithRunes(t, model, "?")

	view := model.View()
	require.Contains(t, view, "open mod details",
		"installed mods help group must say what enter opens, at a normal terminal size")
}

// TestHelpViewSearchEnterRowSurvivesCollapseAtNormalSize is #234's
// regression test, deliberately inverted from the characterization test
// (TestHelpViewSearchEnterRowCollapsedAtNormalSize) that used to pin the
// bug it fixes: on the Search screen, Select's disambiguated row ("open mod
// details...") is the group's LAST entry, and at 100x30 helpBodyBudget's
// "+N more" tail-collapse used to swallow it before rendering reached it -
// the screen's headline action was undiscoverable from its own help panel.
// The collapse now drops by priority, not position: rows marked kept() in
// the promoted (current-screen) group survive wherever they sit, so BOTH of
// the search group's hand-written headline rows - Submit's ("query input
// focused") and Select's ("open mod details...") - must render. The
// "+N more" tail must ALSO still be present: the rows survive because the
// collapse prefers them, not because the budget grew - a raised budget
// would regress the terminal-bounds invariant
// (TestHelpViewCapsWithMoreTailAtSmallHeight) instead.
func TestHelpViewSearchEnterRowSurvivesCollapseAtNormalSize(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 30)
	model.screen = ScreenSearch
	model = updateWithRunes(t, model, "?")

	view := model.View()
	require.Contains(t, view, "query input focused",
		"submit's disambiguated row must survive the collapse at 100x30")
	require.Contains(t, view, "open mod details",
		"select's disambiguated row (the screen's headline action) must survive the collapse at 100x30 (#234)")
	require.Regexp(t, `\+\d+ more`, view,
		"the search group's tail must still collapse at this size - survival comes from priority, not a raised budget")
}
