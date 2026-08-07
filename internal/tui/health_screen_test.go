package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHealthScreenNavigation proves ScreenHealth is a fully wired 6th
// screen (task-9-brief.md, moved from 7th by #224 Task 15's conflicts fold,
// which retired the standalone Conflicts screen and shifted Health into its
// slot 6): the "6" direct-jump binding reaches it, tab cycling from Sources
// (the new next-to-last screen) rotates onto it and then wraps back to
// Dashboard, and its String() renders a real name rather than
// navigation.go's Screen(N) fallback.
func TestHealthScreenNavigation(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	updated := updateWithRunes(t, model, "6")
	require.Equal(t, ScreenHealth, updated.CurrentScreen())
	require.Equal(t, "Health", ScreenHealth.String())

	onSources := updateWithRunes(t, model, "5")
	require.Equal(t, ScreenSources, onSources.CurrentScreen())
	onHealth := updateWithKeyType(t, onSources, tea.KeyTab)
	require.Equal(t, ScreenHealth, onHealth.CurrentScreen())
	onDashboard := updateWithKeyType(t, onHealth, tea.KeyTab)
	require.Equal(t, ScreenDashboard, onDashboard.CurrentScreen())
}

// TestHealthHomeViewRendersFindingsAndHeaderAge covers the non-empty path:
// a "missing" finding and a lock-pending "ok" finding. The header must show
// the relative scan age via lastDeployLabel's own computation, the list
// must show both rows, and the detail pane for whichever row is selected
// must show that status's remedy copy.
func TestHealthHomeViewRendersFindingsAndHeaderAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	scannedAt := now.Add(-3 * time.Minute)

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	model.now = func() time.Time { return now }
	model.healthAt = &scannedAt
	model.health = HealthView{
		Findings: []HealthFinding{
			{ModID: "101", ModName: "SkyUI", FileID: "main", Status: "missing"},
			{ModID: "bear-mount", ModName: "Bear Mount", Status: "ok", Note: "lock pending convergence at v2.0"},
		},
	}
	model.screen = ScreenHealth

	view := model.View()
	require.Contains(t, view, "last scan: local, 3m ago")
	require.Contains(t, view, "SkyUI")
	require.Contains(t, view, "MISSING")
	require.Contains(t, view, "Bear Mount")

	// Row 0 (missing) is selected by default.
	require.Contains(t, view, "run a fix (F) to redownload")

	model.selected[ScreenHealth] = 1
	view = model.View()
	require.Contains(t, view, "lock pending convergence at v2.0 — run 'lmm profile apply'")
}

// TestHealthHomeViewRendersOkRowWithCheckedHeader is the 2026-08-07 smoke
// feedback fix's (#224) core regression guard: a healthy profile must show
// an OK row per checked mod (CLI parity - `lmm verify` prints a `+ <name> -
// OK` row per file, not just a summary), and the header must name how many
// rows were checked so the screen has content even when nothing is wrong.
func TestHealthHomeViewRendersOkRowWithCheckedHeader(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	scannedAt := now.Add(-1 * time.Minute)

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	model.now = func() time.Time { return now }
	model.healthAt = &scannedAt
	model.health = HealthView{
		Checked: 3,
		Findings: []HealthFinding{
			{ModID: "101", ModName: "SkyUI", FileID: "main", Status: "ok"},
		},
	}
	model.screen = ScreenHealth

	view := model.View()
	require.Contains(t, view, "last scan: local, 1m ago — 3 checked")
	require.Contains(t, view, "SkyUI")
	require.Contains(t, view, "OK")

	// Row 0 (the ok row) is selected by default - the detail pane must show
	// a remedy line even for a quiet-ok row, in healthRemedy's own voice.
	require.Contains(t, view, "OK — no action needed")
}

// TestHealthHomeViewGenuinelyEmptyProfileOmitsCheckedSuffix covers the
// OTHER half of the header contract (#224 smoke feedback): a real scan
// (healthAt set) that checked zero rows - a genuinely empty profile, no
// mods installed at all - must NOT claim "0 checked" in the header; the
// suffix is reserved for a scan that actually looked at something.
func TestHealthHomeViewGenuinelyEmptyProfileOmitsCheckedSuffix(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	scannedAt := now.Add(-2 * time.Minute)

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	model.now = func() time.Time { return now }
	model.healthAt = &scannedAt
	model.health = HealthView{} // Checked 0, Findings empty: nothing installed
	// A genuinely empty profile means no conflicts either (#224 Task 15's
	// empty state requires BOTH empty) - cleared here since
	// sizedPrototypeModel's loadData round trip populates the prototype's
	// own unrelated canned conflicts.
	model.conflicts = nil
	model.screen = ScreenHealth

	view := model.View()
	require.Contains(t, view, "last scan: local, 2m ago")
	require.NotContains(t, view, "checked", "a genuinely empty profile must not claim anything was checked")
	require.Contains(t, view, "no findings (local) — run a full check (c)")
}

// TestHealthViewRendersSubjectFallbackForModlessFinding covers a real case
// (#224 Copilot round 1): a stale_deployment finding from a dangling cache
// link carries no owning mod at all, only a FileID - ModName and ModID are
// both empty. Both the list pane row and the detail pane's "Mod:" line must
// fall back to the FileID as a meaningful subject rather than rendering a
// blank/stray label (e.g. list pane's old "%s (%s)" shape produced a bare
// " (stray.pak)"). The list row must also avoid the redundant "X (X)" shape
// since the fallback subject IS the FileID here.
func TestHealthViewRendersSubjectFallbackForModlessFinding(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	model.health = HealthView{
		Findings: []HealthFinding{
			{FileID: "stray.pak", Status: "stale_deployment"},
		},
	}
	model.screen = ScreenHealth
	model.selected[ScreenHealth] = 0

	view := model.View()
	require.Contains(t, view, "stray.pak")
	require.NotContains(t, view, "stray.pak (stray.pak)", "list row must not repeat the fallback subject as its own parenthetical")
	require.NotContains(t, view, " (stray.pak)", "list row must not render a blank-subject label like \" (stray.pak)\"")
	require.Contains(t, view, "Mod:    stray.pak", "detail pane's Mod: line must fall back to the FileID")
}

// TestHealthFixedStatusesRenderAsResolved covers two Copilot round-2
// findings together: (1) healthRemedy had no case for fixed_needs_reingest
// (the engine emits it after a --fix re-ingests a pak - verify.go's
// resolveLast("fixed_needs_reingest", ...) call) so the detail pane fell to
// the default empty remedy with no explanation, and (3) healthStatusClass
// bucketed every fixed_* status as "warning", so a resolved row tinted like
// an outstanding problem in the post-fix table. Both fixed_stale_deployment
// and fixed_needs_reingest must read "resolved" in the detail strip and
// carry the "fine" class (healthStatusStyle's untinted case), never
// "warning" - checked directly against healthStatusClass/healthStatusStyle
// rather than a glyph substring: #224's layout rework replaced the old
// two-pane list's leading "[glyph] LABEL" row with a columnar STATUS field
// tinted by color alone (see healthStatusStyle's own doc comment), and a
// plain-text view render can't observe ANSI color in this package's non-TTY
// test environment (lipgloss only emits escape codes for a TTY output,
// mirrored by search_test.go's own WarningText-Transform workaround for the
// same reason).
func TestHealthFixedStatusesRenderAsResolved(t *testing.T) {
	t.Parallel()

	require.Equal(t, "fine", healthStatusClass("fixed_stale_deployment"))
	require.Equal(t, "fine", healthStatusClass("fixed_needs_reingest"))

	model := sizedPrototypeModel(t, "wizardry", 160, 40)
	require.Equal(t, lipgloss.NewStyle(), model.healthStatusStyle("fixed_stale_deployment"), "the 'fine' class must render untinted, not the 'warning' class's style")

	model.health = HealthView{
		Findings: []HealthFinding{
			{ModID: "a", ModName: "Mod A", FileID: "f1", Status: "fixed_stale_deployment"},
			{ModID: "b", ModName: "Mod B", FileID: "f2", Status: "fixed_needs_reingest"},
		},
	}
	model.screen = ScreenHealth

	model.selected[ScreenHealth] = 0
	view := model.View()
	require.Contains(t, view, "resolved", "fixed_stale_deployment must show the resolved remedy")
	require.Contains(t, view, "FIXED STALE DEPLOYMENT", "the table's STATUS column must show the uppercase label")

	model.selected[ScreenHealth] = 1
	view = model.View()
	require.Contains(t, view, "resolved", "fixed_needs_reingest must show the resolved remedy")
	require.Contains(t, view, "FIXED NEEDS REINGEST", "the table's STATUS column must show the uppercase label")
}

// TestHealthHomeViewEmptyState covers a fresh session that hasn't scanned
// yet: healthAt is nil (its zero value) and m.health carries no findings.
// Deliberately skips sizedPrototypeModel's Init()/loadData round trip (#224
// Task 10 wired DataProvider.Health into loadData, and the prototype
// provider's own Health() returns a canned non-empty view - see
// prototypeHealthFindings - so running a real load here would defeat the
// "hasn't scanned yet" premise); sizing the model directly via
// WindowSizeMsg leaves m.health/m.healthAt/m.healthErr at their zero
// values instead.
//
// Also covers the tier-aware variant (#224 Copilot round 2): after a
// successful FULL check (m.health.Full true) the "run a full check (c)"
// hint is misleading - the user just ran one - so the full-tier empty state
// must drop the hint entirely rather than repeat the local-tier copy.
func TestHealthHomeViewEmptyState(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	sized, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	model = sized.(Model)
	// Skips Init()/loadData (see doc comment above), so state must be
	// stamped ready by hand - otherwise View() renders the "Consulting the
	// archives..." loading screen instead of ScreenHealth's content.
	model.state = stateReady
	model.screen = ScreenHealth

	view := model.View()
	require.Contains(t, view, "no scan yet")
	require.Contains(t, view, "no findings (local) — run a full check (c)")

	model.health.Full = true
	view = model.View()
	require.Contains(t, view, "no findings (full)")
	require.NotContains(t, view, "run a full check (c)", "a full-tier empty state must not hint at running the full check it just ran")
}

// --- #224 layout rework: full-width columnar table + detail strip ---

// TestHealthTableRow_VersionMismatchShowsRecordedArrowEffective is the
// VERSION column's core proof for version_mismatch: core.VerifyFinding's
// Recorded/Effective (plumbed through HealthFinding, #224 follow-up) render
// as "recorded→effective" in the table row, alongside the ordinary STATUS/
// MOD/FILE columns the old two-pane list never showed side by side.
func TestHealthTableRow_VersionMismatchShowsRecordedArrowEffective(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.health = HealthView{Findings: []HealthFinding{
		{ModID: "mismatch-mod", ModName: "Mismatch Mod", FileID: "f1", Status: "version_mismatch", Recorded: "1.2", Effective: "1.4"},
	}}

	rows := model.healthTableRows(160, 5)
	require.Len(t, rows, 1)
	require.Contains(t, rows[0], "Mismatch Mod", "MOD column")
	require.Contains(t, rows[0], "f1", "FILE column")
	require.Contains(t, rows[0], "VERSION MISMATCH", "STATUS column, uppercase")
	require.Contains(t, rows[0], "1.2→1.4", "VERSION column: recorded→effective")
}

// TestHealthTableRow_MissingShowsVersionWithNoArrow covers the VERSION
// column's other source: a "missing" row shows VerifyFinding.Version
// verbatim (no arrow - there's no "effective" side to a missing file).
func TestHealthTableRow_MissingShowsVersionWithNoArrow(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.health = HealthView{Findings: []HealthFinding{
		{ModID: "a", ModName: "A", FileID: "f1", Status: "missing", Version: "1.5"},
	}}

	rows := model.healthTableRows(160, 5)
	require.Len(t, rows, 1)
	require.Contains(t, rows[0], "1.5")
	require.NotContains(t, rows[0], "→", "a missing row's VERSION column has no recorded/effective arrow")
}

// TestHealthTableRow_ModColumnDoesNotFallBackToFileID proves the table's MOD
// column (healthFindingModLabel) deliberately stops at ModID, unlike the
// detail strip's healthFindingSubject - a modless finding (only FileID set)
// must render a blank MOD column, not repeat the FILE column's value there
// too (the old two-pane list combined subject+file into one "X (Y)" label;
// the table has separate columns for each, so no combining is needed and
// would just be redundant).
func TestHealthTableRow_ModColumnDoesNotFallBackToFileID(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.health = HealthView{Findings: []HealthFinding{
		{FileID: "stray.pak", Status: "stale_deployment"},
	}}

	rows := model.healthTableRows(160, 5)
	require.Len(t, rows, 1)
	require.Contains(t, rows[0], "stray.pak")
	require.NotContains(t, rows[0], "stray.pak (stray.pak)")
}

// TestHealthTableNoteTruncatesButDetailStripShowsFull proves the table's
// NOTE column truncates an overlong note to its own (narrower) column width
// while the detail strip below shows it in full - "full" meaning not
// clipped to the table's column, still safety-truncated to the panel's
// overall content width like every other line in a Width()-constrained
// panel (#42) - see healthDetailPane's own doc comment.
func TestHealthTableNoteTruncatesButDetailStripShowsFull(t *testing.T) {
	t.Parallel()

	longNote := strings.Repeat("x", 60)
	model := sizedPrototypeModel(t, "wizardry", 100, 30)
	model.health = HealthView{Findings: []HealthFinding{
		{ModID: "a", ModName: "A", FileID: "f1", Status: "no_checksum", Note: longNote},
	}}
	// sizedPrototypeModel's canned data (via loadData) populates m.conflicts
	// too (#224 Task 15 - the Health table now includes them); cleared here
	// so this test's row-count/content assertions describe the one finding
	// only, undisturbed by the prototype's own unrelated demo conflicts.
	model.conflicts = nil
	model.screen = ScreenHealth
	model.selected[ScreenHealth] = 0

	rows := model.healthTableRows(model.availableWidth(), 5)
	require.Len(t, rows, 1)
	require.NotContains(t, rows[0], longNote, "an overlong note must not survive uncut in the table's NOTE column")

	contentWidth := model.availableWidth() - model.theme.Panel.GetHorizontalFrameSize()
	detail := model.healthDetailPane(contentWidth, 4)
	require.Contains(t, detail, "Note:   "+longNote, "the detail strip must show the note in full, not clipped to the table's NOTE column width")
	require.Contains(t, detail, "no checksum recorded — run a fix (F) to backfill", "the detail strip must also show the remedy copy")
}

// TestHealthColumnWidths_DropsNoteThenVersionOnNarrowTerminal is the #224
// layout rework's narrow-terminal degradation proof: NOTE is the first
// column dropped as the panel narrows, VERSION the second - never MOD/FILE/
// STATUS, which carry a row's primary identity (healthColumnWidths' own doc
// comment).
func TestHealthColumnWidths_DropsNoteThenVersionOnNarrowTerminal(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	_, _, _, _, _, showVersion, showNote := model.healthColumnWidths(160)
	require.True(t, showVersion, "a wide terminal shows every column")
	require.True(t, showNote, "a wide terminal shows every column")

	_, _, _, _, _, showVersion, showNote = model.healthColumnWidths(50)
	require.True(t, showVersion, "VERSION must still show once NOTE alone has been dropped")
	require.False(t, showNote, "NOTE must be the first column dropped on a narrow terminal")

	_, _, _, _, _, showVersion, showNote = model.healthColumnWidths(40)
	require.False(t, showVersion, "VERSION must be dropped next once dropping NOTE alone isn't enough")
	require.False(t, showNote)
}

// TestHealthTableRowsFlexToFullWidthAtWideTerminal is the #224 smoke-feedback
// fix #3's RED-then-GREEN proof, updated by fix #5 (#224) for the
// single-absorber model: on a wide terminal the Health table must behave
// like Installed Mods (modRow/TestModRow_NoTrailingColumnDrift's own
// fixed-row-width convention) - the table must consume the full panel
// content width, and MOD - the row's primary identity, like modRow's own
// NAME column - must absorb the surplus uncapped, rather than staying
// pinned to healthColumnWidths' old small literal cap while a
// short-content, left-aligned NOTE column silently ate most of the surplus
// as invisible padding (the exact bug fix #5 diagnosed: a 210-col
// screenshot showing real mod names truncated while NOTE trailed off in
// blank space).
func TestHealthTableRowsFlexToFullWidthAtWideTerminal(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	longMod := strings.Repeat("m", 60) // longer than any bounded FILE/NOTE cap - only MOD may absorb this much
	model.health = HealthView{Findings: []HealthFinding{
		{ModID: "a", ModName: longMod, FileID: "f1", Status: "missing", Version: "1.5", Note: "short note"},
	}}
	model.conflicts = conflictsHealthFixture()

	const width = 200
	rows := model.healthTableRows(width, 10)
	require.Len(t, rows, 3, "1 finding + 2 conflicts")

	// A 60-rune mod name must render UNtruncated at 200 cols - MOD is the
	// sole surplus absorber (healthColumnWidths' new model), so it must
	// never be the column that clips a real mod name the way NOTE's
	// invisible padding used to force it to.
	require.NotContains(t, rows[0], "…", "a long mod name must not truncate at 200 cols now that MOD absorbs the surplus")
	require.Contains(t, ansi.Strip(rows[0]), longMod, "the full 60-rune mod name must survive uncut")

	// Every row must render at the same width regardless of content length -
	// modRow/TestModRow_NoTrailingColumnDrift's own fixed-row-width
	// convention, extended here to Health's NOTE column (previously the only
	// column left unpadded, so short-note rows fell short of the panel's
	// full width and the selection highlight didn't reach the right edge).
	rowWidth := lipgloss.Width(rows[0])
	for i, r := range rows {
		require.Equal(t, rowWidth, lipgloss.Width(r), "row %d must match row 0's rendered width", i)
	}

	// The table must consume the full panel content width at this terminal
	// size, not just the old capped column sum.
	innerWidth := width - model.theme.Panel.GetHorizontalFrameSize()
	require.Equal(t, innerWidth, rowWidth, "rendered row width must equal the panel's full content width")
}

// TestHealthTableRowsNoteBlockEndsNearRightEdge is fix #5's (#224) second
// RED-then-GREEN proof: a short NOTE value must not leave ~100 columns of
// invisible left-aligned padding trailing off into blank space the way the
// old 3/5-of-the-surplus NOTE share did. NOTE is now bounded near its own
// content (min(32, avail/6)), so once it renders, the row's VISIBLE
// (trimmed) text should reach close to the panel's full content width -
// unlike the old proportional split, where a short note's real text could
// end far short of the right edge even though the cell itself was padded.
func TestHealthTableRowsNoteBlockEndsNearRightEdge(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.health = HealthView{Findings: []HealthFinding{
		{ModID: "a", ModName: "Donovan's Larger Resource Stacks", FileID: "f1", Status: "ok"},
	}}
	model.conflicts = nil

	const width = 210
	rows := model.healthTableRows(width, 10)
	require.Len(t, rows, 1)

	contentWidth := width - model.theme.Panel.GetHorizontalFrameSize()
	trimmed := strings.TrimRight(ansi.Strip(rows[0]), " ")
	require.GreaterOrEqual(t, lipgloss.Width(trimmed), contentWidth*9/10,
		"NOTE's visible text block must reach near the right edge, not leave invisible padding mid-row")
}

// TestHealthTableRowsAllOKRowsCarryVisibleRightSideContent is the 2026-08-07
// smoke feedback round 3's RED-then-GREEN proof: 3ee970c already made
// healthColumnWidths consume the full content width, but that padding is
// invisible whitespace - a healthy profile's OK rows carry no VERSION and no
// NOTE, so the row's VISIBLE (trimmed) text still stopped at FILE and the
// table still read as narrow, exactly like Installed Mods would if
// author/version were ever blank. The fix is content, not geometry: an
// empty NOTE cell must fall back to short per-status text (healthNoteCell)
// and an empty VERSION cell must fall back to an em dash placeholder, so
// every row's visible content reaches as far right as Installed Mods' rows
// do (modRow's status/author/version are always populated).
func TestHealthTableRowsAllOKRowsCarryVisibleRightSideContent(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	model.health = HealthView{Findings: []HealthFinding{
		{ModID: "101", ModName: "SkyUI", FileID: "main", Status: "ok"},
		// Short enough to survive healthColumnWidths' bounded NOTE column
		// uncut at width 160 (#224 smoke feedback fix #5 - NOTE is now
		// capped near its own typical content rather than left to grow, so
		// a longer note would truncate here; that's covered separately by
		// TestHealthTableConflictNoteColumn/TestHealthTableNoteTruncatesButDetailStripShowsFull).
		{ModID: "bear-mount", ModName: "Bear Mount", FileID: "bm.esp", Status: "ok", Note: "lock pending convergence"},
	}}
	model.conflicts = nil

	const width = 160
	statusW, modW, fileW, _, _, showVersion, showNote := model.healthColumnWidths(width)
	require.True(t, showVersion, "a wide terminal shows every column")
	require.True(t, showNote, "a wide terminal shows every column")

	rows := model.healthTableRows(width, 10)
	require.Len(t, rows, 2)

	// oldContentEnd is where a row's visible text used to stop: prefix +
	// STATUS + gap + MOD + gap + FILE, with VERSION/NOTE rendering as blank
	// padding beyond it. Every row's trimmed width must now reach past this
	// point.
	oldContentEnd := 2 /* m.row's "> "/"  " prefix */ + statusW + 1 + modW + 1 + fileW
	for i, r := range rows {
		trimmed := strings.TrimRight(ansi.Strip(r), " ")
		require.Greater(t, lipgloss.Width(trimmed), oldContentEnd, "row %d: visible content must extend past FILE into VERSION/NOTE", i)
	}

	require.Contains(t, ansi.Strip(rows[0]), "no action needed", "a Note-less OK row must get the short default NOTE text")
	require.Contains(t, ansi.Strip(rows[0]), "—", "a Version-less row must get the em-dash VERSION placeholder")

	require.Contains(t, ansi.Strip(rows[1]), "lock pending convergence", "an OK row WITH a Note must keep it")
	require.NotContains(t, ansi.Strip(rows[1]), "no action needed", "a Note-bearing OK row must not also get the default NOTE text")
}

// TestHealthScreenNarrowTerminalDoesNotOverflow guards the #42 contract
// (TestDashboardLayoutsDoNotOverflowNarrowTerminals' own reasoning) for the
// new full-width table + detail strip: neither the rendered width nor the
// rendered height may exceed the screen's own budget, even at a narrow
// width that drops the NOTE/VERSION columns and a long mod name/note that
// would otherwise auto-wrap inside the bordered panel.
func TestHealthScreenNarrowTerminalDoesNotOverflow(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 40, 24)
	model.health = HealthView{Findings: []HealthFinding{
		{ModID: "a", ModName: "A Reasonably Long Mod Name For Testing", FileID: "f1", Status: "version_mismatch", Recorded: "1.2", Effective: "1.4", Note: "some diagnostic note text that runs long"},
	}}
	model.screen = ScreenHealth

	view := model.screenView()
	require.LessOrEqual(t, lipgloss.Width(view), model.availableWidth())
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())
}

// fakeContextContent is a test-local contextContent implementation - the
// SPEC-CRITERION-4 proof that ScreenHealth's host demonstrably supports a
// second content view without building a real one (#86's mod-details view
// will be the first real second implementation).
type fakeContextContent struct {
	title   string
	lines   []string
	presses int
}

// Title defaults to "FAKE DETAIL" so callers that don't care about the exact
// title (the #86 host-generic tests below construct a bare
// &fakeContextContent{}) still get renderable, assertable content; every
// existing caller that DOES set title explicitly uses this same string
// anyway.
func (f *fakeContextContent) Title() string {
	if f.title != "" {
		return f.title
	}
	return "FAKE DETAIL"
}

func (f *fakeContextContent) Lines(_, _ int) []string { return f.lines }

func (f *fakeContextContent) HandleKey(msg tea.KeyMsg) (contextContent, tea.Cmd, bool) {
	if msg.String() == "x" {
		f.presses++
		return f, nil, true
	}
	return f, nil, false
}

func (f *fakeContextContent) HelpGroup() helpGroup {
	return helpGroup{name: "fake", entries: []string{"x", "press"}}
}

// TestHealthContextHostPushRenderKeyAndEscPop is the SPEC-CRITERION-4 proof:
// pushing a fake content onto ScreenHealth renders its title and lines, its
// HandleKey consumes a key (and the returned "next" is what the host keeps
// rendering), and esc - which the fake declines - pops back to the screen
// that pushed it.
func TestHealthContextHostPushRenderKeyAndEscPop(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 24)
	model.screen = ScreenDashboard

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line one", "fake line two"}}
	model.pushContext(fake)

	require.Equal(t, ScreenDashboard, model.CurrentScreen(), "push must not move the session (#86)")
	view := model.screenView()
	require.Contains(t, view, "FAKE DETAIL")
	require.Contains(t, view, "fake line one")
	require.Contains(t, view, "fake line two")

	updated, cmd := model.Update(keyRunes("x"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Equal(t, ScreenDashboard, model.CurrentScreen(), "a consumed key must not move the screen")
	require.Equal(t, 1, fake.presses, "HandleKey must have consumed the key")

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	require.Equal(t, ScreenDashboard, model.CurrentScreen(), "esc must pop back to the pushing screen")
	require.Nil(t, model.contextContent, "popContext must clear the pushed content")
}

// TestHealthContextHostPromotesPushedContentHelpGroup is fix round 1's
// Finding 2 regression test: while content is pushed onto ScreenHealth, the
// help panel's promoted group (immediately after "global") must be the
// pushed content's own HelpGroup(), not the ambient static "health" group's
// unrelated 7/c/F bindings - otherwise HelpGroup() would be an orphaned
// interface member. Uses a zero-size model (like
// TestHelpViewCurrentScreenGroupFirst) so helpBodyBudget's generous unsized
// default (50 lines) has the most room to work with; even so, the full
// grouped list (now with the pushed "fake" group added on top) still runs
// past that budget and tail-collapses into "+N more" before reaching
// "health" itself, so this compares against "dashboard" (the group that
// would normally be first after global, and one of the fixed groups still
// guaranteed to survive the cap) rather than "health" directly.
func TestHealthContextHostPromotesPushedContentHelpGroup(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.screen = ScreenDashboard

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line"}}
	model.pushContext(fake)
	model.showHelp = true

	view := model.helpView()
	fakeIdx := strings.Index(view, "fake")
	globalIdx := strings.Index(view, "global")
	dashboardIdx := strings.Index(view, "dashboard")
	require.NotEqual(t, -1, fakeIdx, "pushed content's HelpGroup title must appear")
	require.Less(t, globalIdx, fakeIdx, "global must still render first")
	require.Less(t, fakeIdx, dashboardIdx, "pushed content's group must be promoted ahead of the ordinary fixed groups")
}

// TestGotoScreenAwayFromHealthClearsPushedContext is fix round 1's Finding
// 1 regression test, generalized off ScreenHealth by #86 (see
// TestContextHostNavAwayClearsFromAnyScreen below for the any-screen
// version): a global nav key the pushed content DECLINES (a digit jump, here
// "2") must not strand contextContent set while the session sits on a
// different screen - gotoScreen (app.go) must clear it, or returning to
// Health later would re-render the stale pushed content instead of the home
// view.
func TestGotoScreenAwayFromHealthClearsPushedContext(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 24)
	model.screen = ScreenDashboard

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line"}}
	model.pushContext(fake)
	require.Equal(t, ScreenDashboard, model.CurrentScreen())

	// "2" is declined by the fake (only "x" is handled=true), so it falls
	// through to the outer switch's InstalledMods jump.
	updated, _ := model.Update(keyRunes("2"))
	model = updated.(Model)
	require.Equal(t, ScreenInstalledMods, model.CurrentScreen(), "the declined nav key must still navigate")
	require.Nil(t, model.contextContent, "navigating away must clear the stranded pushed content")

	// Visiting Health must render the home view, not the stale fake.
	updated, _ = model.Update(keyRunes("6"))
	model = updated.(Model)
	require.Equal(t, ScreenHealth, model.CurrentScreen())
	view := model.screenView()
	require.NotContains(t, view, "FAKE DETAIL", "the stale pushed content must not resurface")
	require.NotContains(t, view, "fake line")
}

// TestPopContextNoopWhenNothingPushed proves popContext is safe to call (or
// reach via esc) with nothing pushed: the screen stays put and no panic
// occurs.
func TestPopContextNoopWhenNothingPushed(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model.screen = ScreenHealth
	require.Nil(t, model.contextContent, "sanity: nothing pushed yet")

	model.popContext()
	require.Equal(t, ScreenHealth, model.CurrentScreen())
	require.Nil(t, model.contextContent)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	require.Equal(t, ScreenHealth, model.CurrentScreen(), "esc with nothing pushed must not move the screen")
}

// TestContextHostRendersOnPushingScreen (#86): pushing content no longer
// hijacks the session to ScreenHealth. The details view opened from Installed
// Mods must render there, with the nav bar still highlighting Installed Mods -
// a nav bar reading "Health" over a mod details view was the whole reason the
// host got generalized.
func TestContextHostRendersOnPushingScreen(t *testing.T) {
	m := sizedPrototypeModel(t, "wizardry", 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	fake := &fakeContextContent{}

	m.pushContext(fake)

	assert.Equal(t, ScreenInstalledMods, m.screen, "push must not move the session")
	view := m.View()
	assert.Contains(t, view, "FAKE DETAIL", "pushed content must render on the pushing screen")
}

// TestContextHostEscPopsToSameScreen: esc clears the content and leaves the
// session exactly where it was.
func TestContextHostEscPopsToSameScreen(t *testing.T) {
	m := sizedPrototypeModel(t, "wizardry", 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m.pushContext(&fakeContextContent{})

	m = updateWithMsg(t, m, tea.KeyMsg{Type: tea.KeyEsc})

	assert.Nil(t, m.contextContent)
	assert.Equal(t, ScreenInstalledMods, m.screen)
	assert.NotContains(t, m.View(), "FAKE DETAIL")
}

// TestContextHostNavAwayClearsFromAnyScreen generalizes #224's stranded-
// content regression test off ScreenHealth.
func TestContextHostNavAwayClearsFromAnyScreen(t *testing.T) {
	m := sizedPrototypeModel(t, "wizardry", 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m.pushContext(&fakeContextContent{})

	m = updateWithMsg(t, m, keyRunes("3")) // Search

	assert.Equal(t, ScreenSearch, m.screen)
	assert.Nil(t, m.contextContent, "navigating away must never strand pushed content")
	assert.NotContains(t, m.View(), "FAKE DETAIL")
}

// TestContextHostSwallowsDeclinedKeys is the safety half. A key the content
// declines must NOT reach the screen underneath: on Installed Mods that would
// mean arrow keys silently moving the selection behind the view, and e/x/u
// enabling or uninstalling the row the user can no longer see.
func TestContextHostSwallowsDeclinedKeys(t *testing.T) {
	rec := &recordingActions{}
	m := sizedModelWithActions(t, rec, 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	before := m.selected[ScreenInstalledMods]
	m.pushContext(&fakeContextContent{}) // declines everything except "x"

	for _, k := range []string{"j", "e", "u"} {
		m = updateWithMsg(t, m, keyRunes(k))
	}
	m = updateWithMsg(t, m, tea.KeyMsg{Type: tea.KeyDown})

	assert.Equal(t, before, m.selected[ScreenInstalledMods], "selection must not move behind pushed content")
	// rec.EnableCalls alone is vacuous here: 'e' (undeclined) only opens a
	// confirmation modal via m.action.pending - EnableCalls is appended when
	// the modal's confirm cmd actually runs, which this test never drives.
	// Asserting the modal/action state 'e'/'u' would have set is what
	// actually fails if the swallow rule's `default:` arm is removed.
	assert.Nil(t, m.action.pending, "a declined 'e' must not open the enable confirmation modal")
	assert.False(t, m.action.running, "a declined 'u' must not start a check-updates action")
	assert.Zero(t, rec.EnableCalls, "a declined key must not trigger a mutation underneath")
	assert.NotNil(t, m.contextContent, "declined keys must not close the view either")
}

// TestContextHostStillAllowsQuitAndHelp: the swallow rule has exits.
func TestContextHostStillAllowsQuitAndHelp(t *testing.T) {
	m := sizedPrototypeModel(t, "wizardry", 100, 30)
	m, _ = m.gotoScreen(ScreenInstalledMods)
	m.pushContext(&fakeContextContent{})

	updated, _ := m.Update(keyRunes("?"))
	m2, ok := updated.(Model)
	require.True(t, ok)
	assert.NotEqual(t, m.showHelp, m2.showHelp, "help must still toggle over pushed content")

	_, cmd := m.Update(keyRunes("q"))
	assert.NotNil(t, cmd, "quit must still work over pushed content")
}

// --- Task 11: 'c' full (network) health check ---

// TestFullCheckKeyDispatchesOnHealthScreen proves 'c' on ScreenHealth
// dispatches RunHealthCheck's Full tier asynchronously - mirroring
// TestCheckUpdatesKeyDispatchesAsyncFetchFromDashboard's own "running set
// synchronously, provider call happens when the returned cmd runs" shape.
func TestFullCheckKeyDispatchesOnHealthScreen(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{RunHealthCheckOutcome: HealthView{Full: true}}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.NotNil(t, cmd)
	require.True(t, model.action.running)
	require.Equal(t, "Running full health check…", model.action.status)
	require.False(t, model.action.statusIsError)
	require.Empty(t, rec.RunHealthCheckCalls, "the provider call happens when the returned cmd runs, not synchronously")

	msg := runActionCmd(t, cmd)
	require.IsType(t, fullHealthCheckResultMsg{}, msg)
	require.Len(t, rec.RunHealthCheckCalls, 1)
	require.True(t, rec.RunHealthCheckCalls[0].Full, "must request the Full (network) tier")
	require.False(t, rec.RunHealthCheckCalls[0].Fix, "must be a dry run - fix is Task 12's own binding")
}

// TestFullCheckLandsViewAndStatusLine covers both status-line phrasings
// task-11-brief.md specifies, plus the view/summary tie-in a successful
// check must apply.
func TestFullCheckLandsViewAndStatusLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		view       HealthView
		wantStatus string
	}{
		{
			name:       "all ok",
			view:       HealthView{Full: true},
			wantStatus: "full check: all OK",
		},
		{
			name: "issues and warnings",
			view: HealthView{
				Full:     true,
				Issues:   2,
				Warnings: 1,
				Findings: []HealthFinding{{ModID: "skyui", ModName: "SkyUI", Status: "missing"}},
			},
			wantStatus: "full check: 2 issue(s), 1 warning(s)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recordingActions{RunHealthCheckOutcome: tt.view}
			model := modelWithActions(t, rec)
			model.screen = ScreenHealth

			updated, cmd := model.Update(keyRunes("c"))
			model = updated.(Model)
			msg := runActionCmd(t, cmd)

			updated, cmd2 := model.Update(msg)
			model = updated.(Model)
			require.Nil(t, cmd2)
			require.False(t, model.action.running)
			require.Equal(t, tt.wantStatus, model.action.status)
			require.False(t, model.action.statusIsError)
			require.Equal(t, tt.view, model.health)
			require.NotNil(t, model.healthAt)
			require.Empty(t, model.healthErr)
			require.Equal(t, tt.view.Issues, model.summary.HealthIssues)
			require.Equal(t, tt.view.Warnings, model.summary.HealthWarnings)
		})
	}
}

// TestFullCheckFailurePreservesPreviousViewAndSetsErrorStatus proves a
// failed Full-tier call reports the error on the status line but leaves
// m.health/healthAt exactly as they were (task-11-brief.md: "on failure...
// KEEP the previous view"), mirroring dataLoadedMsg's identical posture for
// a failed background scan (app.go).
func TestFullCheckFailurePreservesPreviousViewAndSetsErrorStatus(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{RunHealthCheckErr: errors.New("network unreachable")}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	prevAt := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	model.healthAt = &prevAt
	model.health = HealthView{Findings: []HealthFinding{{ModID: "x", ModName: "X", Status: "missing"}}, Issues: 1}

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	msg := runActionCmd(t, cmd)
	require.IsType(t, fullHealthCheckFailedMsg{}, msg)

	updated, cmd2 := model.Update(msg)
	model = updated.(Model)
	require.Nil(t, cmd2)
	require.False(t, model.action.running)
	require.True(t, model.action.statusIsError)
	require.Contains(t, model.action.status, "network unreachable")
	require.Equal(t, prevAt, *model.healthAt, "a failed full check must keep the previous scan's timestamp")
	require.Len(t, model.health.Findings, 1, "a failed full check must keep the previous view")
}

// TestFullCheckKeyRefusedWhileRunning mirrors TestCheckUpdatesKeyInertWhileRunning:
// the standard single-flight "busy" refusal, no RunHealthCheck call at all.
func TestFullCheckKeyRefusedWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.action.running = true

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFullCheckKeyInertWhileAnotherModalPending mirrors
// TestCheckUpdatesKeyInertWhileAnotherModalPending: a DIFFERENT already-
// pending confirmation modal is left completely undisturbed by 'c'.
func TestFullCheckKeyInertWhileAnotherModalPending(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
		return ActionOutcome{}, nil
	})
	model = model.promptAction(pa)

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Equal(t, actionDeploy, model.action.pending.kind, "the original modal must still be showing")
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFullCheckKeyOtherScreensNoop proves 'c' is inert on every screen other
// than Health (ScreenProfiles is deliberately excluded - see
// TestFullCheckKeyDoesNotStealCFromCreateProfile below, which proves the
// opposite: 'c' DOES still do something there, just not a full check).
func TestFullCheckKeyOtherScreensNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenDashboard, ScreenInstalledMods, ScreenSearch, ScreenSources} {
		rec := &recordingActions{}
		model := modelWithActions(t, rec)
		model.screen = screen

		updated, cmd := model.Update(keyRunes("c"))
		model = updated.(Model)
		require.Nil(t, cmd, "screen %v", screen)
		require.False(t, model.action.running, "screen %v", screen)
		require.Empty(t, rec.RunHealthCheckCalls, "screen %v", screen)
	}
}

// TestFullCheckKeyDoesNotStealCFromCreateProfile is the collision proof
// keys.go's FullCheck doc comment promises: FullCheck and CreateProfile
// share the physical key "c", but updateKey's compound guard (app.go) keeps
// CreateProfile fully functional on ScreenProfiles - see that switch case's
// own doc comment for the "first matching case wins" mechanics this pins.
func TestFullCheckKeyDoesNotStealCFromCreateProfile(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenProfiles

	updated, _ := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.NotNil(t, model.inputModal, "'c' on ScreenProfiles must still open CreateProfile's input modal")
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFullCheckKeyDeclinedWithPushedContextOnHealth proves the "&&
// m.contextContent == nil" half of updateKey's compound guard: 'c' while
// ScreenHealth has pushed context content (contextview.go) must not
// dispatch a full check - it falls through to CreateProfile's own
// unconditional case instead, which no-ops off-Profiles exactly as it
// always has (see TestFullCheckKeyDoesNotStealCFromCreateProfile above for
// the case where "c" DOES reach CreateProfile productively).
func TestFullCheckKeyDeclinedWithPushedContentOnHealth(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	// #86: pushContext no longer forces the screen to ScreenHealth, so this
	// starts there directly - the guard under test is specifically about
	// ScreenHealth with pushed content, not about push's old side effect.
	model.screen = ScreenHealth

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line"}}
	model.pushContext(fake)
	require.Equal(t, ScreenHealth, model.CurrentScreen())

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Empty(t, rec.RunHealthCheckCalls, "'c' must not dispatch a full check while context content is pushed")
	require.NotNil(t, model.contextContent, "the pushed content must remain")
}

// TestRunFullHealthCheckDirectCallRefusedWithPushedContent proves
// runFullHealthCheck's own defense-in-depth guard (mutations.go doc comment:
// "mirrors updateKey's own... m.contextContent == nil... defense-in-depth"),
// independent of updateKey's inline compound switch-case guard that
// TestFullCheckKeyDeclinedWithPushedContentOnHealth already covers - this
// calls runFullHealthCheck directly, bypassing that outer guard entirely, so
// a mismatch between the doc comment and the implementation (Copilot PR #227
// round 9 finding) would show up here even though the key-dispatch test
// above stays green.
func TestRunFullHealthCheckDirectCallRefusedWithPushedContent(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	// #86: pushContext no longer forces the screen to ScreenHealth, so this
	// starts there directly - runFullHealthCheck's own guard checks BOTH
	// m.screen != ScreenHealth and m.contextContent != nil (mutations.go),
	// and starting anywhere else would let the screen half of that guard
	// alone explain the refusal, defeating this test's whole point.
	model.screen = ScreenHealth

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line"}}
	model.pushContext(fake)
	require.Equal(t, ScreenHealth, model.CurrentScreen())

	updated, cmd := model.runFullHealthCheck()
	require.Nil(t, cmd)
	require.False(t, updated.action.running, "a direct call must not start the action while context content is pushed")
	require.Empty(t, rec.RunHealthCheckCalls, "runFullHealthCheck must not dispatch a full check while context content is pushed")
	require.NotNil(t, updated.contextContent, "the pushed content must remain")
}

// TestFullCheckProgressTicksReachStatusLine drives the FULL pump pipeline
// through Model.Update, mirroring
// TestActionProgressStreamsWhileRunningThenActionDoneClearsIt's identical
// shape (actions_test.go): runFullHealthCheck's tea.Batch(actionCmd,
// listenerCmd) is exactly what buildAction's own confirm returns, so the
// same drive-both-sub-cmds-through-Update technique proves the Full tier's
// progress ticks reach the status line too.
func TestFullCheckProgressTicksReachStatusLine(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{
		RunHealthCheckOutcome: HealthView{Full: true},
		RunHealthCheckTicks:   []ActionProgress{{Line: "Checking SkyUI: 3/10", Percent: 30}},
	}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth

	updated, cmd := model.Update(keyRunes("c"))
	model = updated.(Model)
	require.NotNil(t, cmd)

	batchMsg := cmd()
	batch, ok := batchMsg.(tea.BatchMsg)
	require.True(t, ok, "runFullHealthCheck must return tea.Batch(actionCmd, listenerCmd)")
	require.Len(t, batch, 2)

	actionMsg := batch[0]()
	require.IsType(t, fullHealthCheckResultMsg{}, actionMsg)

	// The action cmd already ran do() to completion (sending its one tick and
	// closing the channel) before actionMsg was produced above, so the
	// listener's first receive gets that buffered tick even though the
	// channel is already closed (Go delivers buffered values before
	// signaling closed - see waitForActionProgress's own doc comment).
	progressMsg := batch[1]()
	require.IsType(t, actionProgressMsg{}, progressMsg)

	updated, reissue := model.Update(progressMsg)
	model = updated.(Model)
	require.Equal(t, "Checking SkyUI: 3/10", model.action.progress.Line)
	require.Contains(t, model.statusLine(), "Checking SkyUI: 3/10")
	require.NotNil(t, reissue, "a fresh tick must re-issue the listener")
}

// TestFullHealthCheckSettleClearsStaleProgressLine proves resolveFullHealthCheckResult
// and resolveFullHealthCheckFailure clear m.action.progress on settle, mirroring
// actionDoneMsg/actionFailedMsg's own clearing (app.go) - Copilot round 3 finding:
// without this, a leftover "checking versions..." tick from the JUST-SETTLED full
// check survives into the NEXT action's own run (statusLine prefers a running
// action's progress.Line over its stored status text - see statusLine's own doc
// comment - and some actions, like actionSetPolicy, never post a progress tick of
// their own), so the stale tick would wrongly surface as that next action's status
// line instead of nothing/its own text.
func TestFullHealthCheckSettleClearsStaleProgressLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  func(gen int) tea.Msg
	}{
		{"result", func(gen int) tea.Msg {
			return fullHealthCheckResultMsg{gen: gen, view: HealthView{Full: true}}
		}},
		{"failure", func(gen int) tea.Msg {
			return fullHealthCheckFailedMsg{gen: gen, err: errors.New("network unreachable")}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			model := modelWithActions(t, &recordingActions{})
			model.screen = ScreenHealth
			model.action.gen = 7
			model.action.running = true
			model.action.progress = ActionProgress{Line: "checking versions 1/2: Foo", Percent: 50}

			updated, _ := model.Update(tt.msg(7))
			model = updated.(Model)
			require.False(t, model.action.running)
			require.Empty(t, model.action.progress.Line, "settling a full health check must clear the stale progress line")

			// Simulate the NEXT action starting without ever posting a progress
			// tick of its own (statusLine's "some actions emit none" case) - the
			// stale tick from the settled check above must not resurface.
			model.action.running = true
			require.NotContains(t, model.statusLine(), "checking versions",
				"a stale progress tick from a settled full health check must not surface as the next action's status line")
		})
	}
}

// --- Task 12: 'F' batch fix behind confirmation ---

// TestFixHealthKeyOpensModalWithCategoryDetail proves 'F' on ScreenHealth
// with fixable findings opens the standard confirmation modal, titled with
// the total fixable count and one detail line per status class present -
// task-12-brief.md's exact phrasings - while the "ok" row (unfixable) is
// excluded from both the count and the detail.
func TestFixHealthKeyOpensModalWithCategoryDetail(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{
		Findings: []HealthFinding{
			{ModID: "a", ModName: "A", Status: "missing"},
			{ModID: "b", ModName: "B", Status: "missing"},
			{ModID: "c", ModName: "C", Status: "version_mismatch"},
			{ModID: "d", ModName: "D", Status: "stale_deployment"},
			{ModID: "e", ModName: "E", Status: "needs_reingest"},
			{ModID: "f", ModName: "F", Status: "ok"},
		},
	}

	updated, cmd := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.Nil(t, cmd, "opening the modal does not itself dispatch anything")
	require.NotNil(t, model.action.pending)
	require.Equal(t, "Fix 5 finding(s)?", model.action.pending.title)
	require.Contains(t, model.action.pending.detail, "2 missing file(s) — re-download")
	require.Contains(t, model.action.pending.detail, "1 version mismatch — re-key records (locked mods refused)")
	require.Contains(t, model.action.pending.detail, "1 stale deployment — remove")
	require.Contains(t, model.action.pending.detail, "1 pak needs re-ingest")
	require.Empty(t, rec.RunHealthCheckCalls, "nothing runs until confirmed")
}

// TestFixHealthKeyRefusedWhenNothingFixable proves the "nothing actionable"
// refusal (task-12-brief.md): an empty view, or one whose findings are ALL
// among the four statuses fix cannot touch, refuses on the status line with
// no modal and no provider call.
func TestFixHealthKeyRefusedWhenNothingFixable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		findings []HealthFinding
	}{
		{name: "empty view", findings: nil},
		{
			name: "all unfixable statuses",
			findings: []HealthFinding{
				{ModID: "a", Status: "skipped"},
				{ModID: "b", Status: "version_unverifiable"},
				{ModID: "c", Status: "file_count_mismatch"},
				{ModID: "d", Status: "ok", Note: "lock pending convergence"},
			},
		},
		{
			name: "resolved-only view (fixed_* rows plus ok)",
			findings: []HealthFinding{
				{ModID: "a", Status: "fixed_stale_deployment"},
				{ModID: "b", Status: "fixed_needs_reingest"},
				{ModID: "c", Status: "ok"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recordingActions{}
			model := modelWithActions(t, rec)
			model.screen = ScreenHealth
			model.health = HealthView{Findings: tt.findings}

			updated, cmd := model.Update(keyRunes("F"))
			model = updated.(Model)
			require.Nil(t, cmd)
			require.Nil(t, model.action.pending)
			require.Equal(t, "nothing fixable — run a full check (c) first", model.action.status)
			require.True(t, model.action.statusIsError)
			require.Empty(t, rec.RunHealthCheckCalls)
		})
	}
}

// TestFixHealthCancelLeavesStateUntouched proves n/esc on the fix confirm
// modal leaves m.health/healthAt and every other piece of state exactly as
// they were, with no provider call - mirroring updatePendingActionKey's
// CancelAction contract for every other confirmation modal.
func TestFixHealthCancelLeavesStateUntouched(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	before := HealthView{Findings: []HealthFinding{{ModID: "a", ModName: "A", Status: "missing"}}}
	model.health = before

	updated, _ := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	updated, cmd := model.Update(keyRunes("n"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Equal(t, before, model.health)
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFixHealthConfirmDispatchesFullFixModeAndLandsView is the enforcement
// point T8's review deferred to this task (task-12-brief.md): confirming
// ALWAYS calls RunHealthCheck with full=true (CLI --fix parity includes the
// version pass), never a Local-tier fix. On a successful result: the
// returned view replaces m.health/healthAt/healthErr, summary counts follow
// it, a "fix results" info overlay lists the fixed_* row, and the ordinary
// data refresh (m.loadData) is returned so the dashboard picks it up.
func TestFixHealthConfirmDispatchesFullFixModeAndLandsView(t *testing.T) {
	t.Parallel()

	resultView := HealthView{
		Full:     true,
		Findings: []HealthFinding{{ModID: "x", ModName: "X", FileID: "f1", Status: "fixed_stale_deployment"}},
	}
	rec := &recordingActions{RunHealthCheckOutcome: resultView}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{Findings: []HealthFinding{{ModID: "x", ModName: "X", FileID: "f1", Status: "stale_deployment"}}}

	updated, _ := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.NotNil(t, model.action.pending)

	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	require.True(t, model.action.running)
	msg := runActionCmd(t, cmd)
	require.IsType(t, fixHealthCheckResultMsg{}, msg)

	require.Len(t, rec.RunHealthCheckCalls, 1)
	require.True(t, rec.RunHealthCheckCalls[0].Full, "fix mode must always request the Full tier")
	require.True(t, rec.RunHealthCheckCalls[0].Fix)

	updated, refresh := model.Update(msg)
	model = updated.(Model)
	require.False(t, model.action.running)
	require.Equal(t, resultView, model.health)
	require.NotNil(t, model.healthAt)
	require.Empty(t, model.healthErr)
	require.Equal(t, resultView.Issues, model.summary.HealthIssues)
	require.Equal(t, resultView.Warnings, model.summary.HealthWarnings)

	require.NotNil(t, model.overlay)
	require.Equal(t, "fix results", model.overlay.title)
	require.Contains(t, model.overlay.lines, "✓ X: FIXED STALE DEPLOYMENT")

	require.NotNil(t, refresh, "a successful fix must return the ordinary data refresh")
	require.IsType(t, dataLoadedMsg{}, refresh())
}

// TestFixHealthCheckSettleClearsStaleProgressLine is
// TestFullHealthCheckSettleClearsStaleProgressLine's own sibling for the 'F'
// fix flow's resolvers (Copilot round 3 finding): resolveFixHealthCheckResult
// and resolveFixHealthCheckFailure must clear m.action.progress on settle
// exactly like the full-check pair above, or a stale "checking versions..."
// tick from a just-settled fix survives into whatever action runs next.
func TestFixHealthCheckSettleClearsStaleProgressLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  func(gen int) tea.Msg
	}{
		{"result", func(gen int) tea.Msg {
			return fixHealthCheckResultMsg{gen: gen, view: HealthView{Full: true}}
		}},
		{"failure", func(gen int) tea.Msg {
			return fixHealthCheckFailedMsg{gen: gen, err: errors.New("network unreachable")}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			model := modelWithActions(t, &recordingActions{})
			model.screen = ScreenHealth
			model.action.gen = 9
			model.action.running = true
			model.action.progress = ActionProgress{Line: "checking versions 1/2: Foo", Percent: 50}

			updated, _ := model.Update(tt.msg(9))
			model = updated.(Model)
			require.False(t, model.action.running)
			require.Empty(t, model.action.progress.Line, "settling a fix health check must clear the stale progress line")

			model.action.running = true
			require.NotContains(t, model.statusLine(), "checking versions",
				"a stale progress tick from a settled fix health check must not surface as the next action's status line")
		})
	}
}

// TestHealthCheckResultClampsSelectionOnShorterFindingsList proves both
// success resolvers (resolveFullHealthCheckResult, resolveFixHealthCheckResult)
// clamp the Health screen's selection exactly like dataLoadedMsg's own
// clampSelections call does (app.go) - Copilot round 3 finding: overwriting
// m.health with a SHORTER findings list (e.g. a fix resolving several
// findings down to a handful) without reclamping leaves a selection index
// from the old, longer list pointing past the end of the new one, so
// healthDetailPane's bounds check (idx >= len(m.health.Findings)) falls
// through to "No selection." even though the new list is non-empty.
func TestHealthCheckResultClampsSelectionOnShorterFindingsList(t *testing.T) {
	t.Parallel()

	longFindings := []HealthFinding{
		{ModID: "a", ModName: "A", Status: "missing"},
		{ModID: "b", ModName: "B", Status: "missing"},
		{ModID: "c", ModName: "C", Status: "missing"},
		{ModID: "d", ModName: "D", Status: "missing"},
		{ModID: "e", ModName: "E", Status: "missing"},
	}
	shortView := HealthView{Findings: []HealthFinding{
		{ModID: "a", ModName: "A", Status: "missing"},
		{ModID: "b", ModName: "B", Status: "missing"},
	}}

	tests := []struct {
		name string
		msg  func(gen int) tea.Msg
	}{
		{"full check", func(gen int) tea.Msg {
			return fullHealthCheckResultMsg{gen: gen, view: shortView}
		}},
		{"fix check", func(gen int) tea.Msg {
			return fixHealthCheckResultMsg{gen: gen, view: shortView}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			model := modelWithActions(t, &recordingActions{})
			model.screen = ScreenHealth
			model.health = HealthView{Findings: longFindings}
			// modelWithActions' prototype provider seeds unrelated canned
			// conflicts (#224 Task 15 folds them into the same selection
			// space) - cleared so this test's clamp assertions describe the
			// findings-shrinking scenario alone.
			model.conflicts = nil
			model.selected[ScreenHealth] = len(longFindings) - 1
			model.action.gen = 11
			model.action.running = true

			updated, _ := model.Update(tt.msg(11))
			model = updated.(Model)

			require.Equal(t, len(shortView.Findings)-1, model.selected[ScreenHealth],
				"selection must clamp to the new, shorter findings list")
			detail := model.healthDetailPane(80, 20)
			require.NotContains(t, detail, "No selection.")
			require.Contains(t, detail, "B", "the clamped selection must land on a real finding, not walk off the end")
		})
	}
}

// TestFixHealthResultOverlaySubjectFallbackForModlessFinding proves the fix
// results overlay line (healthFixResultLine) also uses the ModName->ModID->
// FileID subject fallback (#224 Copilot round 1): a fixed_stale_deployment
// row from a dangling cache link carries no mod at all, only a FileID, and
// must not render the bare "✓ : FIXED STALE DEPLOYMENT" the old
// f.ModName-only line produced.
func TestFixHealthResultOverlaySubjectFallbackForModlessFinding(t *testing.T) {
	t.Parallel()

	resultView := HealthView{
		Full:     true,
		Findings: []HealthFinding{{FileID: "stray.pak", Status: "fixed_stale_deployment"}},
	}
	rec := &recordingActions{RunHealthCheckOutcome: resultView}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{Findings: []HealthFinding{{FileID: "stray.pak", Status: "stale_deployment"}}}

	updated, _ := model.Update(keyRunes("F"))
	model = updated.(Model)
	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	msg := runActionCmd(t, cmd)

	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.NotNil(t, model.overlay)
	require.Contains(t, model.overlay.lines, "✓ stray.pak: FIXED STALE DEPLOYMENT")
}

// TestFixHealthLockedVersionMismatchRemainsInOverlay proves the engine's own
// lock refusal survives the fix pass unchanged (task-12-brief.md): a locked
// mod's version_mismatch finding is returned by RunHealthCheck exactly as
// the engine reports it (status still "version_mismatch", Note "locked")
// rather than disappearing or flipping to a fixed_* status - and the fix
// results overlay surfaces it under "remaining", not "fixed".
func TestFixHealthLockedVersionMismatchRemainsInOverlay(t *testing.T) {
	t.Parallel()

	resultView := HealthView{
		Full:     true,
		Issues:   1,
		Findings: []HealthFinding{{ModID: "locked-mod", ModName: "Locked Mod", Status: "version_mismatch", Note: "locked"}},
	}
	rec := &recordingActions{RunHealthCheckOutcome: resultView}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{Findings: []HealthFinding{{ModID: "locked-mod", ModName: "Locked Mod", Status: "version_mismatch"}}}

	updated, _ := model.Update(keyRunes("F"))
	model = updated.(Model)
	updated, cmd := model.Update(keyRunes("y"))
	model = updated.(Model)
	msg := runActionCmd(t, cmd)

	updated, _ = model.Update(msg)
	model = updated.(Model)

	require.Equal(t, resultView, model.health, "the engine's own refusal must survive unchanged")
	require.Len(t, model.health.Findings, 1)
	require.Equal(t, "version_mismatch", model.health.Findings[0].Status, "a locked mod's row is never rewritten to fixed_*")

	require.NotNil(t, model.overlay)
	found := false
	for _, line := range model.overlay.lines {
		if strings.Contains(line, "Locked Mod") && strings.Contains(line, "locked") {
			found = true
		}
	}
	require.True(t, found, "the locked refusal must surface in the fix results overlay: %v", model.overlay.lines)
}

// TestFixHealthKeyRefusedWhileRunning mirrors TestFullCheckKeyRefusedWhileRunning:
// the standard single-flight "busy" refusal, no RunHealthCheck call at all.
func TestFixHealthKeyRefusedWhileRunning(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{Findings: []HealthFinding{{ModID: "a", ModName: "A", Status: "missing"}}}
	model.action.running = true

	updated, cmd := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFixHealthKeyInertWhileAnotherModalPending mirrors
// TestFullCheckKeyInertWhileAnotherModalPending: a DIFFERENT already-pending
// confirmation modal is left completely undisturbed by 'F'.
func TestFixHealthKeyInertWhileAnotherModalPending(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	model.screen = ScreenHealth
	model.health = HealthView{Findings: []HealthFinding{{ModID: "a", ModName: "A", Status: "missing"}}}
	model, pa := model.buildAction(actionDeploy, "Deploy?", nil, "", func(context.Context, func(ActionProgress)) (ActionOutcome, error) {
		return ActionOutcome{}, nil
	})
	model = model.promptAction(pa)

	updated, cmd := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Equal(t, actionDeploy, model.action.pending.kind, "the original modal must still be showing")
	require.Empty(t, rec.RunHealthCheckCalls)
}

// TestFixHealthKeyOtherScreensNoop proves 'F' is inert on every screen other
// than Health.
func TestFixHealthKeyOtherScreensNoop(t *testing.T) {
	t.Parallel()

	for _, screen := range []Screen{ScreenDashboard, ScreenInstalledMods, ScreenSearch, ScreenProfiles, ScreenSources} {
		rec := &recordingActions{}
		model := modelWithActions(t, rec)
		model.screen = screen

		updated, cmd := model.Update(keyRunes("F"))
		model = updated.(Model)
		require.Nil(t, cmd, "screen %v", screen)
		require.Nil(t, model.action.pending, "screen %v", screen)
		require.Empty(t, rec.RunHealthCheckCalls, "screen %v", screen)
	}
}

// TestFixHealthKeyDeclinedWithPushedContentOnHealth mirrors
// TestFullCheckKeyDeclinedWithPushedContentOnHealth: 'F' while ScreenHealth
// has pushed context content must not open the fix confirmation.
func TestFixHealthKeyDeclinedWithPushedContentOnHealth(t *testing.T) {
	t.Parallel()

	rec := &recordingActions{}
	model := modelWithActions(t, rec)
	// #86: pushContext no longer forces the screen to ScreenHealth, so this
	// starts there directly - fixHealthPrompt's own guard checks BOTH
	// m.screen != ScreenHealth and m.contextContent != nil (mutations.go),
	// and starting anywhere else would let the screen half of that guard
	// alone explain the refusal, defeating this test's whole point.
	model.screen = ScreenHealth

	fake := &fakeContextContent{title: "FAKE DETAIL", lines: []string{"fake line"}}
	model.pushContext(fake)
	require.Equal(t, ScreenHealth, model.CurrentScreen())

	updated, cmd := model.Update(keyRunes("F"))
	model = updated.(Model)
	require.Nil(t, cmd)
	require.Nil(t, model.action.pending)
	require.Empty(t, rec.RunHealthCheckCalls)
	require.NotNil(t, model.contextContent, "the pushed content must remain")
}
