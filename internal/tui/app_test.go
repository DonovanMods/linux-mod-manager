package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func TestNewPrototypeModelDefaultsToDashboard(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	require.Equal(t, ScreenDashboard, model.CurrentScreen())
}

// TestLastDeployLabel (#106a) pins lastDeployLabel's contract: nil -> "never";
// a coarse relative age for anything less than 7 days old (minutes/hours/
// days, whichever is coarsest without rounding to zero); and a plain date
// once "N days ago" stops being a useful unit. now is passed explicitly
// (mirroring Model.now, see that field's doc comment) so this pure function
// needs no wall-clock read of its own to test deterministically.
func TestLastDeployLabel(t *testing.T) {
	t.Parallel()

	// Constructed in time.Local so the date-fallback expectations are exact
	// on any machine: the label renders dates via t.Local() (deployed_at is
	// stored in UTC by SQLite's CURRENT_TIMESTAMP, and formatting in the
	// stored zone would show an off-by-one date away from UTC). The
	// dedicated UTC case below pins that conversion explicitly.
	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.Local)

	cases := []struct {
		name string
		t    *time.Time
		want string
	}{
		{name: "nil is never deployed", t: nil, want: "never"},
		{name: "under a minute", t: addTo(now, -30*time.Second), want: "just now"},
		{name: "minutes", t: addTo(now, -5*time.Minute), want: "5m ago"},
		{name: "hours", t: addTo(now, -3*time.Hour), want: "3h ago"},
		{name: "just under a day rounds to hours", t: addTo(now, -23*time.Hour), want: "23h ago"},
		{name: "days", t: addTo(now, -2*24*time.Hour), want: "2d ago"},
		{name: "just under a week", t: addTo(now, -6*24*time.Hour-23*time.Hour), want: "6d ago"},
		{name: "exactly a week falls back to a date", t: addTo(now, -7*24*time.Hour), want: "2026-07-19"},
		{name: "a month ago falls back to a date", t: addTo(now, -30*24*time.Hour), want: "2026-06-26"},
	}
	// UTC-stored timestamps render as the user's LOCAL date: the expected
	// string is derived via the same t.Local() conversion the label must
	// perform, so this case fails if the label ever formats in the stored
	// zone again — in any test-machine timezone.
	utcStored := addTo(now, -10*24*time.Hour)
	utcConverted := utcStored.UTC()
	cases = append(cases, struct {
		name string
		t    *time.Time
		want string
	}{name: "utc-stored timestamp renders local date", t: &utcConverted, want: utcStored.Local().Format("2006-01-02")})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, lastDeployLabel(now, tc.t))
		})
	}
}

// addTo returns a *time.Time offset from base by d - a small helper so the
// table above can write "-30*time.Second" style deltas without a temporary
// variable per case.
func addTo(base time.Time, d time.Duration) *time.Time {
	t := base.Add(d)
	return &t
}

func TestNewPrototypeModelRejectsInvalidTheme(t *testing.T) {
	t.Parallel()

	_, err := NewPrototypeModel(Options{Theme: "bogus"})
	require.Error(t, err)
}

// TestNavMarksCurrentScreenWithoutColor is the #91 audit's other finding:
// nav() (app.go) distinguished the current screen from the rest ONLY by
// theme.Selected's color versus theme.MutedText's — with color unavailable
// (NO_COLOR, --no-color, or any non-color terminal) the two labels render
// identically and the current screen becomes unrecoverable from the nav
// line alone. The fix must add a plain-text distinguisher inside the
// styled span, not rely on color — and (PR #107 review) that
// distinguisher must be SAME-WIDTH (•N• replacing [N]), not additive: an
// additive "• " prefix grew the nav line and shifted the hard-truncation
// cut, degrading the tail label at narrow widths (see
// TestNavMarkerAddsNoWidth for that regression's dedicated guard).
//
// Uses the search_test.go Transform-marker technique (see
// TestSearchDetailPaneStylesInstalledStatus) on theme.Selected: this
// non-TTY test environment degrades lipgloss to no color, so a bare
// require.Contains on ANSI bytes would pass vacuously regardless of
// whether nav()'s current-screen branch actually ran. Transform makes the
// Selected-styled span observable (via «...») without depending on color,
// which lets this test assert BOTH that the plain-text marker exists AND
// that it lives inside the real Selected-rendered span for the current
// screen (not just appended somewhere in the line).
func TestNavMarksCurrentScreenWithoutColor(t *testing.T) {
	t.Parallel()

	// 110 cols (Task 9): the 7th screen (Health) pushed tier 1's full nav
	// past 100 cols' 96-cell availableWidth(), which used to be comfortably
	// wide enough for the pre-Health 6-screen nav - see nav()'s own tier
	// doc comment.
	model := sizedPrototypeModel(t, "wizardry", 110, 30)
	model.theme.Selected = model.theme.Selected.Transform(func(s string) string { return "«" + s + "»" })
	model.screen = ScreenSearch

	nav := model.nav()

	currentLabel := fmt.Sprintf("•%d• %s", model.screenIndex()+1, model.screen)
	styled := model.theme.Selected.Render(currentLabel)
	require.Contains(t, nav, styled,
		"marker sanity: the current screen's label must go through the real Selected style, marker included")

	// The actual regression guard: the Selected-rendered span for the
	// current screen must carry a plain-text distinguisher (independent of
	// the Transform/color styling around it), the marker must REPLACE the
	// bracket cell (same width) rather than sit alongside it, and no other
	// screen's label may carry that same marker.
	inner := strings.TrimSuffix(strings.TrimPrefix(styled, "«"), "»")
	require.Contains(t, inner, "•3•", "current screen's number cell must render as •N•")
	require.NotContains(t, nav, "[3]",
		"the bracket cell is replaced by •N•, not kept alongside it — same width is the whole point")

	for i, screen := range screens {
		if screen == model.screen {
			continue
		}
		otherLabel := fmt.Sprintf("[%d] %s", i+1, screen)
		require.Contains(t, nav, model.theme.MutedText.Render(otherLabel),
			"non-current screens keep the plain [N] form")
	}
	require.Equal(t, 2, strings.Count(nav, "•"),
		"exactly one •N• cell: only the current screen carries the marker")
}

// TestNavMarkerAddsNoWidth pins the PR #107 review finding: the first #91
// marker was an additive "• " prefix, which grew the nav line by two cells
// and shifted View()'s hard truncation (see View's nav truncate comment)
// so the rightmost label degraded at widths where the unmarked nav fit
// exactly. The marker must be zero-growth: •N• is the same three cells as
// [N], so marking a screen current never changes where (or whether) the
// nav line truncates.
//
// NOTE the nav line has been wider than an 80-column terminal's budget
// since Task 3 added the sixth entry (View's own comment) — at 80 cols the
// nav now COMPRESSES to tier 2 (see nav()'s tier comment) rather than
// truncating, so "Conflicts readable at 80" is not something this test
// needs to prove via truncation. What IS guaranteed, and asserted here:
// (1) at the exact width where the unmarked (tier 1) nav fits, the marked
// nav still fits — the additive prefix broke exactly this; (2) marker
// zero-growth holds within a tier — marking a screen current never changes
// that tier's line width, whichever screen is current.
func TestNavMarkerAddsNoWidth(t *testing.T) {
	t.Parallel()

	// The unmarked nav line, built the same way nav() builds it: every
	// screen in its plain [N] form. Derived rather than hardcoded so the
	// test tracks future screen additions/renames.
	labels := make([]string, 0, len(screens))
	for i, screen := range screens {
		labels = append(labels, fmt.Sprintf("[%d] %s", i+1, screen))
	}
	unmarked := strings.Join(labels, "  ")

	// (1) Boundary width: terminal sized so availableWidth() == the
	// unmarked nav's width exactly. The full View() pipeline (which owns
	// the truncate call) must still show the LAST screen's marked label
	// intact when it is current — under the additive prefix this was the
	// first width to regress.
	model := sizedPrototypeModel(t, "wizardry", lipgloss.Width(unmarked)+4, 24)
	require.Equal(t, lipgloss.Width(unmarked), model.availableWidth(),
		"width sanity: this terminal width must make the unmarked nav an exact fit")
	model.screen = ScreenHealth
	require.Contains(t, model.View(), fmt.Sprintf("•%d• %s", len(screens), ScreenHealth),
		"zero-growth marker: the last label must survive intact at the exact-fit width")

	// (2) Zero growth everywhere: whichever screen is current, the nav
	// line's cell width is identical — the marker can never move the cut.
	model.screen = ScreenDashboard
	base := lipgloss.Width(model.nav())
	for _, screen := range screens {
		model.screen = screen
		require.Equal(t, base, lipgloss.Width(model.nav()), screen.String())
	}
	require.Equal(t, lipgloss.Width(unmarked), base,
		"marked nav must be exactly as wide as the unmarked form")
}

// TestNavCompressesToFitAt80Columns is #108's RED case: the 6-screen full
// nav (tier 1, ~87 cells - see nav()'s doc comment) never fits an 80-column
// terminal's 76-cell availableWidth(), so before this fix nav() always
// returned the full-width line and relied on View()'s hard truncate to cut
// it down - which chopped the tail label (and, when the CURRENT screen was
// last, its •N• marker) off entirely rather than degrading gracefully. This
// asserts against m.nav() directly (not the truncated View() line) because
// the fix's whole point is that nav() itself must already fit
// availableWidth() - truncate() is a safety net, not the compression
// mechanism. Tier 2 (current-label-only, worst case ~43 cells per the
// design doc) fits comfortably under 76 for every screen, so this also
// pins that the current screen's label text survives at this width.
func TestNavCompressesToFitAt80Columns(t *testing.T) {
	t.Parallel()

	for i, screen := range screens {
		i, screen := i, screen
		t.Run(screen.String(), func(t *testing.T) {
			t.Parallel()

			model := sizedPrototypeModel(t, "wizardry", 80, 24)
			model.screen = screen

			nav := model.nav()
			width := model.availableWidth()

			require.LessOrEqual(t, lipgloss.Width(nav), width,
				"nav() itself must fit availableWidth() without relying on View()'s truncate safety net")
			require.Contains(t, nav, fmt.Sprintf("•%d•", i+1),
				"current screen's marker must survive at 80 columns")
			require.Contains(t, nav, screen.String(),
				"tier 2 (current-label-only) keeps the current screen's label text")
		})
	}
}

// TestNavStaysFullAt120Columns pins the no-regression side of #108: at a
// comfortably wide terminal (120 cols -> 116-cell availableWidth(), well
// over tier 1's ~87), nav() must still pick tier 1 and render every
// screen's full label, not just the current one.
func TestNavStaysFullAt120Columns(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 120, 36)
	nav := model.nav()

	require.LessOrEqual(t, lipgloss.Width(nav), model.availableWidth())
	for _, screen := range screens {
		require.Contains(t, nav, screen.String(),
			"tier 1 (full) must keep every screen's label at wide widths, not just the current one")
	}
}

// TestNavCompressesToNumbersOnlyAt40Columns is #108's pathological-width
// case: availableWidth() floors at 40 cells (max(m.width-frame, 40)), and
// tier 2's worst case - the current screen being "Installed Mods", the
// longest label, at ~43 cells - exceeds that floor. nav() must fall all the
// way to tier 3 (numbers-only, ~28 cells) rather than overflow: every
// screen keeps its number cell, and the current screen's cell is still
// •N•, but no label text (including the current screen's own) survives.
func TestNavCompressesToNumbersOnlyAt40Columns(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 40, 12)
	model.screen = ScreenInstalledMods // longest label: forces tier 2 over the 40-cell floor

	nav := model.nav()
	width := model.availableWidth()

	require.LessOrEqual(t, lipgloss.Width(nav), width)
	// Assert the actual rendered cell tokens, not bare digits — a bare "%d"
	// Contains could be satisfied by an unrelated digit (an ANSI parameter
	// byte, a count elsewhere in the line) and let a missing cell slip
	// through.
	for i := range screens {
		cell := fmt.Sprintf("[%d]", i+1)
		if screens[i] == model.screen {
			cell = fmt.Sprintf("•%d•", i+1)
		}
		require.Contains(t, nav, cell, "every number cell must render as its [N]/•N• token")
	}
	require.Contains(t, nav, "•2•", "current screen keeps its marker in the numbers-only tier")
	require.NotContains(t, nav, "Installed Mods",
		"numbers-only tier drops all labels, including the current screen's")
}

func TestNumberKeysNavigateScreens(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	updated := updateWithRunes(t, model, "2")
	require.Equal(t, ScreenInstalledMods, updated.CurrentScreen())

	updated = updateWithRunes(t, updated, "3")
	require.Equal(t, ScreenSearch, updated.CurrentScreen())
	require.True(t, updated.search.input.Focused(), "3 focuses the search input so typing starts immediately")

	// Esc blurs so the remaining screen-level number keys reach updateKey's
	// outer switch instead of being typed into the now-focused input.
	updated = updateWithKeyType(t, updated, tea.KeyEsc)
	updated = updateWithRunes(t, updated, "4")
	require.Equal(t, ScreenProfiles, updated.CurrentScreen())

	updated = updateWithRunes(t, updated, "1")
	require.Equal(t, ScreenDashboard, updated.CurrentScreen())
}

func TestTabCyclesScreens(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	updated := updateWithKeyType(t, model, tea.KeyTab)
	require.Equal(t, ScreenInstalledMods, updated.CurrentScreen())

	updated = updateWithKeyType(t, updated, tea.KeyShiftTab)
	require.Equal(t, ScreenDashboard, updated.CurrentScreen())
}

// TestTabCyclingOntoSearchDoesNotFocus covers the tab-cycling entry path into
// ScreenSearch. Finding 1 (smoke test): auto-focusing here trapped the user,
// since a focused input swallows every keystroke (see updateKey's
// focused-input branch) — they couldn't keep cycling past Search without
// pressing Esc first. Only the two EXPLICIT "go search" bindings, "/" and
// "3", may focus (see TestSlashFromAnyScreenJumpsAndFocuses and
// TestNumberThreeJumpsAndFocuses); screen-cycling must land here unfocused,
// and a further Tab must keep cycling straight through to the next screen
// instead of being swallowed as literal text — that's the trap repro.
func TestTabCyclingOntoSearchDoesNotFocus(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	updated := updateWithKeyType(t, model, tea.KeyTab)
	require.Equal(t, ScreenInstalledMods, updated.CurrentScreen())

	updated = updateWithKeyType(t, updated, tea.KeyTab)
	require.Equal(t, ScreenSearch, updated.CurrentScreen())
	require.False(t, updated.search.input.Focused(), "tab-cycling onto search must NOT focus the input")

	// Trap repro: a further Tab must move on to the next screen, not be
	// swallowed as literal text by a focused input.
	updated = updateWithKeyType(t, updated, tea.KeyTab)
	require.Equal(t, ScreenProfiles, updated.CurrentScreen())
}

func TestArrowAndVimKeysNavigateScreens(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	model = updateWithKeyType(t, model, tea.KeyRight)
	require.Equal(t, ScreenInstalledMods, model.CurrentScreen())

	model = updateWithRunes(t, model, "l")
	require.Equal(t, ScreenSearch, model.CurrentScreen())
	// Finding 1: cycling onto search must NOT focus the input, so the
	// remaining screen-level arrow/vim keys below keep cycling straight
	// through — no Esc needed first (that was the trap).
	require.False(t, model.search.input.Focused(), "cycling onto search must not focus the input")

	model = updateWithRunes(t, model, "l")
	require.Equal(t, ScreenProfiles, model.CurrentScreen())

	model = updateWithKeyType(t, model, tea.KeyLeft)
	require.Equal(t, ScreenSearch, model.CurrentScreen())

	model = updateWithRunes(t, model, "h")
	require.Equal(t, ScreenInstalledMods, model.CurrentScreen())

	model = updateWithRunes(t, model, "h")
	require.Equal(t, ScreenDashboard, model.CurrentScreen())
}

func TestSelectionMovementIsClamped(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)
	model = updateWithRunes(t, model, "2")

	model = updateWithRunes(t, model, "j")
	model = updateWithKeyType(t, model, tea.KeyDown)
	require.Equal(t, 2, model.SelectedIndex(ScreenInstalledMods))

	for i := 0; i < 20; i++ {
		model = updateWithKeyType(t, model, tea.KeyDown)
	}
	require.Equal(t, 4, model.SelectedIndex(ScreenInstalledMods))

	model = updateWithRunes(t, model, "k")
	require.Equal(t, 3, model.SelectedIndex(ScreenInstalledMods))

	for i := 0; i < 20; i++ {
		model = updateWithKeyType(t, model, tea.KeyUp)
	}
	require.Equal(t, 0, model.SelectedIndex(ScreenInstalledMods))
}

// Selectable list screens must (a) never exceed the height budget and
// (b) keep the selected row visible when navigation walks past the fold —
// previously rows were rendered unbounded, so on short terminals the
// highlight scrolled out of the panel while the detail/selection state kept
// updating invisibly (#42).
func TestListScreensFitHeightBudgetAndFollowSelectionOnShortTerminals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		key    string // number key jumping to the screen
		screen Screen
	}{
		{"installed mods", "2", ScreenInstalledMods},
		{"profiles", "4", ScreenProfiles},
		{"sources", "5", ScreenSources},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := sizedPrototypeModel(t, "wizardry", 80, 12)
			model = updateWithRunes(t, model, tc.key)

			// Walk selection to the last row; the list is longer than the
			// short terminal's budget for at least installed mods.
			for i := 0; i < model.itemCount(tc.screen); i++ {
				model = updateWithMsg(t, model, tea.KeyMsg{Type: tea.KeyDown})
			}
			view := model.screenView()
			require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())

			// The selected (last) row must be rendered: its highlighted "> "
			// marker must appear somewhere in the view.
			require.Contains(t, view, "> ", "selection marker must stay visible after walking past the fold")
		})
	}
}

func TestSearchAndQuitBindings(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	model = updateWithRunes(t, model, "/")
	require.Equal(t, ScreenSearch, model.CurrentScreen())

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
}

func TestHelpToggle(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	model = updateWithRunes(t, model, "?")
	require.True(t, model.HelpVisible())

	model = updateWithRunes(t, model, "?")
	require.False(t, model.HelpVisible())
}

func TestWindowSizeExpandsViewToTerminalBounds(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)

	view := model.View()
	require.Equal(t, 100, lipgloss.Width(view))
	require.Equal(t, 30, lipgloss.Height(view))
}

func TestScreenViewsUseAvailableWidth(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 120, 36)

	for _, screen := range screens {
		model.screen = screen
		require.Equal(t, model.availableWidth(), lipgloss.Width(model.screenView()), screen.String())
	}
}

// TestModRowColumnsAlignRegardlessOfNameLength covers Finding 2 (smoke
// test): a mod name longer than its allotted space used to overflow the
// fixed-width name column unchecked, shifting every subsequent column to
// the right so rows didn't line up. modRow must give the name column room
// proportional to the panel width and hard-truncate any name that still
// overflows, so the Status column starts at the same offset regardless of
// name length. Run at both a common 80-column size and the wider ~160
// columns the smoke test flagged as the normal case.
func TestModRowColumnsAlignRegardlessOfNameLength(t *testing.T) {
	t.Parallel()

	sizes := []struct{ width, height int }{{80, 24}, {160, 40}}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()

			model := sizedPrototypeModel(t, "wizardry", size.width, size.height)
			short := ModItem{Name: "Short", Status: "enabled", Author: "Alice", Version: "1.0.0"}
			long := ModItem{Name: strings.Repeat("VeryLongModName", 6), Status: "disabled", Author: "Bob", Version: "2.1.0"}

			shortRow := model.modRow(0, model.availableWidth(), short)
			longRow := model.modRow(1, model.availableWidth(), long)

			shortIdx := strings.Index(shortRow, "enabled")
			longIdx := strings.Index(longRow, "disabled")
			require.Greater(t, shortIdx, 0, "status column must be present in the short-name row")
			require.Greater(t, longIdx, 0, "status column must be present in the long-name row")
			// Compare DISPLAY columns (lipgloss.Width), not byte offsets: the
			// truncated long name ends in a multi-byte "…" ellipsis rune, so a
			// byte-offset comparison would report a false mismatch even though
			// the row aligns visually - which is the actual bug being fixed.
			shortCol := lipgloss.Width(shortRow[:shortIdx])
			longCol := lipgloss.Width(longRow[:longIdx])
			require.Equal(t, shortCol, longCol,
				"the Status column must start at the same display column regardless of name length")

			require.LessOrEqual(t, lipgloss.Width(longRow), model.availableWidth(),
				"an overlong name must be hard-truncated, never overflow the row")
		})
	}
}

// TestModRowNameColumnGrowsWithPanelWidth proves the name column is
// proportional to the panel's width (Finding 2) rather than a small fixed
// column count: a wider terminal must give the whole row - and so the name
// column - more room, not just a marginally bigger fixed number.
func TestModRowNameColumnGrowsWithPanelWidth(t *testing.T) {
	t.Parallel()

	narrow := sizedPrototypeModel(t, "wizardry", 80, 24)
	wide := sizedPrototypeModel(t, "wizardry", 160, 40)
	mod := ModItem{Name: "X", Status: "enabled", Author: "Alice", Version: "1.0.0"}

	narrowRow := narrow.modRow(0, narrow.availableWidth(), mod)
	wideRow := wide.modRow(0, wide.availableWidth(), mod)

	require.Greater(t, lipgloss.Width(wideRow), lipgloss.Width(narrowRow),
		"a wider terminal must give the name column more room, proportional to the panel width")
}

// TestProfileRowColumnsAlignRegardlessOfNameLength covers the same defect
// class the fix wave fixed in modRow (Finding 2) but flagged as out of scope
// for profilesView: a profile name longer than its allotted space used to
// overflow the fixed-width name column unchecked (the old
// "%s %-22s %3d mods" format had no truncation), shifting the mod-count
// column out of alignment with shorter rows. profileRow must give the name
// column room proportional to the panel width and hard-truncate any name
// that still overflows, so the mod-count column starts at the same offset
// regardless of name length. Run at both a common 80-column size and the
// wider ~160 columns the smoke test flagged as the normal case.
func TestProfileRowColumnsAlignRegardlessOfNameLength(t *testing.T) {
	t.Parallel()

	sizes := []struct{ width, height int }{{80, 24}, {160, 40}}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()

			model := sizedPrototypeModel(t, "wizardry", size.width, size.height)
			short := ProfileItem{Name: "Short", ModCount: 3}
			long := ProfileItem{Name: strings.Repeat("VeryLongProfileName", 6), ModCount: 12}

			shortRow := model.profileRow(0, model.availableWidth(), short)
			longRow := model.profileRow(1, model.availableWidth(), long)

			shortIdx := strings.Index(shortRow, "3 mods")
			longIdx := strings.Index(longRow, "12 mods")
			require.Greater(t, shortIdx, 0, "mod-count column must be present in the short-name row")
			require.Greater(t, longIdx, 0, "mod-count column must be present in the long-name row")
			// Compare DISPLAY columns (lipgloss.Width), not byte offsets: a
			// truncated long name ends in a multi-byte "…" ellipsis rune, so a
			// byte-offset comparison would report a false mismatch even though
			// the row aligns visually - see modRow's TestModRowColumnsAlign...
			// for the same pitfall.
			shortCol := lipgloss.Width(shortRow[:shortIdx])
			longCol := lipgloss.Width(longRow[:longIdx])
			require.Equal(t, shortCol, longCol,
				"the mod-count column must start at the same display column regardless of name length")

			require.LessOrEqual(t, lipgloss.Width(longRow), model.availableWidth(),
				"an overlong name must be hard-truncated, never overflow the row")
		})
	}
}

// TestProfileRowNameColumnGrowsWithPanelWidth proves the name column is
// proportional to the panel's width rather than a small fixed column count:
// a wider terminal must give the whole row - and so the name column - more
// room, not just a marginally bigger fixed number.
func TestProfileRowNameColumnGrowsWithPanelWidth(t *testing.T) {
	t.Parallel()

	narrow := sizedPrototypeModel(t, "wizardry", 80, 24)
	wide := sizedPrototypeModel(t, "wizardry", 160, 40)
	profile := ProfileItem{Name: "X", ModCount: 1}

	narrowRow := narrow.profileRow(0, narrow.availableWidth(), profile)
	wideRow := wide.profileRow(0, wide.availableWidth(), profile)

	require.Greater(t, lipgloss.Width(wideRow), lipgloss.Width(narrowRow),
		"a wider terminal must give the name column more room, proportional to the panel width")
}

func TestDashboardLayoutsDoNotOverflowNarrowTerminals(t *testing.T) {
	t.Parallel()

	for _, themeName := range []string{"wizardry", "dos"} {
		t.Run(themeName, func(t *testing.T) {
			t.Parallel()

			model := sizedPrototypeModel(t, themeName, 40, 24)
			require.Equal(t, ScreenDashboard, model.CurrentScreen())
			require.LessOrEqual(t, lipgloss.Width(model.screenView()), model.availableWidth())
		})
	}
}

// Dashboards must never render more lines than availableContentHeight():
// lipgloss pads short panels but never clips tall ones, so any layout whose
// content exceeds its panel's height budget silently overflows the terminal
// (#42; reproduced at 120x21 where the party layout rendered 15 lines into
// a 14-line budget). Every theme layout is checked at each size because the
// four dashboard views split the budget differently.
func TestDashboardLayoutsFitHeightBudgetOnShortTerminals(t *testing.T) {
	t.Parallel()
	sizes := []struct{ width, height int }{{120, 21}, {80, 14}, {40, 12}, {40, 10}}
	for _, themeName := range []string{"wizardry", "amber", "dos", "green"} {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s-%dx%d", themeName, size.width, size.height), func(t *testing.T) {
				t.Parallel()
				model := sizedPrototypeModel(t, themeName, size.width, size.height)
				view := model.screenView()
				require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())
			})
		}
	}
}

// Commander's half-width panels are narrow enough at small widths (40x12:
// panel width ~19, content ~15) that an untruncated menu label like
// "> Consult Conflict Oracle" lipgloss-auto-wraps into two physical lines
// inside the panel. clampLines counts logical lines, so a wrapped row slips
// past the clamp and silently grows the view over the height budget (#42);
// the fix is per-line truncation to the panel's content width, the same
// pattern sourcesView uses.
func TestCommanderDashboardRowsDoNotWrapAtNarrowWidths(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "dos", 40, 12)
	require.LessOrEqual(t, lipgloss.Height(model.screenView()), model.availableContentHeight())
}

// At 80x16 (content budget 9, panel content budget 7) the commander left
// panel's seven lines - #106a added "Deploy" as the seventh - fit exactly,
// so no clamping may occur: any budget fudge (an earlier fix subtracted an
// extra 1) clamps them to shorter plus "+N more" and silently hides the
// Enabled/Updates/Deploy lines (#42). Only the left panel mentions "Updates"
// — the commander menu rows do not — so its presence pins the whole panel
// rendering unclamped. Originally pinned at the 80x8 floor height when the
// panel had six lines (see git history); #106a's new row no longer fits
// unclamped at that floor, so this now uses the next size up (80x16) where
// all seven lines fit exactly.
func TestCommanderDashboardFloorHeightKeepsWholeLeftPanel(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "dos", 80, 16)
	require.Contains(t, model.screenView(), "Updates")
	require.Contains(t, model.screenView(), "Deploy")
}

// Long dynamic values (game/profile names interpolated into dashboard rows,
// e.g. "Mods:    %d installed / %d enabled" or "> GAME     %s") must not
// lipgloss-auto-wrap inside their panel: clampLines counts logical lines, so
// a wrapped row renders as two physical lines it cannot see, silently
// growing the view past the height budget (#42) — the same defect class
// TestCommanderDashboardRowsDoNotWrapAtNarrowWidths pins for commander's
// static menu labels, here triggered by data instead of layout. Prototype
// data is short, so the plain short-terminal test cannot catch this; the
// long names are set directly on the model to simulate real-world values.
// Multiple sizes matter: at 40x12 party's tiny per-panel budget happens to
// clamp the long lines away before they can wrap, so only 120x21 (where
// topBudget keeps all four lines and the ~90-col names wrap inside the
// 55-col half panel) exposes the party-layout variant of the defect.
func TestDashboardLayoutsFitHeightBudgetWithLongDynamicValues(t *testing.T) {
	t.Parallel()
	sizes := []struct{ width, height int }{{120, 21}, {80, 14}, {40, 12}}
	for _, themeName := range []string{"wizardry", "amber", "dos", "green"} {
		for _, size := range sizes {
			t.Run(fmt.Sprintf("%s-%dx%d", themeName, size.width, size.height), func(t *testing.T) {
				t.Parallel()

				model := sizedPrototypeModel(t, themeName, size.width, size.height)
				model.summary.GameName = strings.Repeat("Very Long Game Name ", 4)
				model.summary.ProfileName = strings.Repeat("Very Long Profile Name ", 4)
				view := model.screenView()
				require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())
			})
		}
	}
}

// TestDashboardLayoutsRenderLastDeploy (#106a) pins the wiring from
// Summary.LastDeploy through to each dashboard layout's rendered text: every
// theme (party/terminal/commander/crt - see layoutForTheme) must render
// lastDeployLabel's EXACT text for a known now/LastDeploy pair, not just
// some deploy-shaped placeholder. model.now is overridden directly (package-
// internal seam, see Model.now's doc comment) so the label is pinned exactly
// rather than asserting a loose "contains a number and 'ago'" pattern.
func TestDashboardLayoutsRenderLastDeploy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC)
	deployedAt := now.Add(-3 * time.Hour)

	for _, themeName := range []string{"wizardry", "amber", "dos", "green"} {
		t.Run(themeName, func(t *testing.T) {
			t.Parallel()

			model := sizedPrototypeModel(t, themeName, 120, 24)
			model.now = func() time.Time { return now }
			model.summary.LastDeploy = &deployedAt

			// Terminal (amber) upper-cases every dashboard row (see its own
			// "> DEPLOY" row) to match its all-caps boot-sequence voice, so
			// the assertion normalizes case rather than special-casing that
			// one theme.
			require.Contains(t, strings.ToUpper(model.screenView()), "3H AGO")
		})
	}
}

// TestDashboardLayoutsRenderNeverDeployed (#106a) is
// TestDashboardLayoutsRenderLastDeploy's nil-value companion: every layout
// must render lastDeployLabel's "never" for a nil LastDeploy (the
// coreProvider-real "this profile has never been deployed" case - see
// Summary.LastDeploy's doc comment), not silently fall back to a zero-time
// date or blank row.
func TestDashboardLayoutsRenderNeverDeployed(t *testing.T) {
	t.Parallel()

	for _, themeName := range []string{"wizardry", "amber", "dos", "green"} {
		t.Run(themeName, func(t *testing.T) {
			t.Parallel()

			model := sizedPrototypeModel(t, themeName, 120, 24)
			model.summary.LastDeploy = nil

			require.Contains(t, strings.ToUpper(model.screenView()), "NEVER")
		})
	}
}

func TestScreenViewsUseExactAvailableHeightOnLargeTerminals(t *testing.T) {
	t.Parallel()

	// All four themes, because each maps to a different dashboard layout
	// (TestThemesUseDistinctLayouts) and the four layouts split the height
	// budget differently — a wizardry-only check left the other three
	// layouts' exact-height invariant unguarded (#42 review).
	for _, themeName := range []string{"wizardry", "amber", "dos", "green"} {
		t.Run(themeName, func(t *testing.T) {
			t.Parallel()

			model := sizedPrototypeModel(t, themeName, 120, 36)

			for _, screen := range screens {
				model.screen = screen
				require.Equal(t, model.availableContentHeight(), lipgloss.Height(model.screenView()), screen.String())
			}
		})
	}
}

// The short-terminal companion to
// TestScreenViewsUseExactAvailableHeightOnLargeTerminals: at ANY size,
// screenView() must render at most availableContentHeight() lines — the
// large-terminal test pins "exactly", this one pins "never more", which is
// the half lipgloss cannot enforce (it pads but never clips) (#42).
func TestScreenViewsFitHeightBudgetAtAllSizes(t *testing.T) {
	t.Parallel()
	sizes := []struct{ width, height int }{{120, 21}, {80, 14}, {80, 12}, {40, 12}, {40, 10}, {80, 8}}
	for _, size := range sizes {
		for i, screen := range screens {
			t.Run(fmt.Sprintf("%v-%dx%d", screen, size.width, size.height), func(t *testing.T) {
				t.Parallel()
				model := sizedPrototypeModel(t, "wizardry", size.width, size.height)
				model = updateWithRunes(t, model, fmt.Sprintf("%d", i+1))
				if screen == ScreenSearch {
					model.search.state = searchReady
					model.search.page = populatedSearchPage()
				}
				view := model.screenView()
				require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight(),
					"screen %v must not overflow at %dx%d", screen, size.width, size.height)
			})
		}
	}
}

// stateFailed/searchFailed/zero-results all interpolate unbounded dynamic
// text (an arbitrary error's .Error() string, or the user's own search
// query) directly into a Width-constrained panel — the same wrap defect
// class TestScreenViewsFitHeightBudgetAtAllSizes pinned for the dashboards
// (reachable there by fixed layout data; reachable here by runtime data an
// attacker or a misbehaving source could make arbitrarily long) (#42).
func TestErrorAndEmptyStatesFitHeightBudgetWithLongDynamicText(t *testing.T) {
	t.Parallel()

	longText := strings.Repeat("catastrophic failure contacting the archive ", 20) // ~900 runes
	longQuery := strings.Repeat("ancient-tome-of-forbidden-modding-secrets ", 20)  // ~860 runes

	sizes := []struct{ width, height int }{{40, 12}, {80, 8}}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("stateFailed-%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			model := sizedPrototypeModel(t, "wizardry", size.width, size.height)
			model.state = stateFailed
			model.loadErr = errors.New(longText)

			view := model.screenView()
			require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight(),
				"stateFailed must not overflow at %dx%d", size.width, size.height)
		})

		t.Run(fmt.Sprintf("searchFailed-%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			model := sizedPrototypeModel(t, "wizardry", size.width, size.height)
			model = updateWithRunes(t, model, "3") // ScreenSearch's nav key (its index+1 in screens)
			model.search.state = searchFailed
			model.search.err = errors.New(longText)

			view := model.screenView()
			require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight(),
				"searchFailed must not overflow at %dx%d", size.width, size.height)
		})

		t.Run(fmt.Sprintf("searchReadyZeroResults-%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			model := sizedPrototypeModel(t, "wizardry", size.width, size.height)
			model = updateWithRunes(t, model, "3")
			model.search.state = searchReady
			model.search.page = SearchPage{Query: longQuery, Source: "nexusmods"}

			view := model.screenView()
			require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight(),
				"zero-results must not overflow at %dx%d", size.width, size.height)
		})
	}
}

func TestViewFitsTerminalBoundsWithHelpVisible(t *testing.T) {
	t.Parallel()

	// Height bumped 37->39->40->60->61->65->66->68->69->70->71->72 over time
	// (37->39->40 across Phase 5b Task 5's two new help lines; 40->60 in Task
	// 9, when helpView grew from a flat ~15-line list into per-screen groups
	// covering every Tasks 4-8 binding - see helpGroups/helpBodyBudget;
	// 60->61 for the dashboard group's "enter open menu entry" line; 61->65
	// in Task 3, whose new "conflicts" help group adds a blank separator, a
	// header, and two entries; 65->66 in Task 3's review fix wave, which
	// added the Deploy entry to that group; 66->68 in Task 4, whose
	// MoveDown/MoveUp entries added two more lines to the installed-mods
	// group's uncapped content; 68->69 in Phase 6b Task 6, whose new
	// Rollback entry added one more line to that same group; 69->70 in
	// Phase 6b Task 9, whose new profiles-group ImportProfile entry added
	// one more line to that group; 70->71 in Phase 6b Task 10, whose new
	// profiles-group ExportProfile entry added one more line to that same
	// group - see helpGroups' own conflicts group doc comment; 71->72 in
	// fix-wave-2's list-scoped changelog viewer, whose new Changelog entry
	// added one more line to the installed-mods group. Verified empirically
	// each time (scratch probes sweeping a height range, since removed) the
	// same way 5a proved its own 36->37 bump: below the fitting height, the
	// rendered view consistently comes out taller than the requested
	// terminal height (lipgloss pads SHORT content but never clips content
	// taller than the requested budget) - the party-sheet dashboard's
	// split-panel math (partyDashboardView's topHeight/menuHeight, both
	// integer divisions of availableContentHeight) hits its natural minimum
	// before the requested budget does. Height=72 is the first value where
	// the requested content budget finally reaches that same natural
	// minimum, so the view fits with exactly zero slack (73 and above, the
	// content grows to fill the larger budget instead; 71 now overflows to
	// 72, confirmed by re-running this test unmodified against the new
	// installed-mods-group entry). This pins the current zero-slack floor -
	// see task-5-brief.md's "prove pre-existing saturation... like 5a did"
	// allowance for justified height adjustments.
	model := sizedPrototypeModel(t, "wizardry", 120, 72)
	model = updateWithRunes(t, model, "?")

	view := model.View()
	require.Equal(t, 120, lipgloss.Width(view))
	require.Equal(t, 72, lipgloss.Height(view))
}

func TestThemesUseDistinctLayouts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		themeName string
		want      Layout
	}{
		{themeName: "wizardry", want: LayoutPartySheet},
		{themeName: "amber", want: LayoutMonochromeTerminal},
		{themeName: "dos", want: LayoutCommander},
		{themeName: "green", want: LayoutCrtStack},
	}

	for _, tt := range tests {
		t.Run(tt.themeName, func(t *testing.T) {
			model, err := NewPrototypeModel(Options{Theme: tt.themeName})
			require.NoError(t, err)
			require.Equal(t, tt.want, model.Layout())
		})
	}
}

func sizedPrototypeModel(t *testing.T, themeName string, width, height int) Model {
	t.Helper()

	model, err := NewPrototypeModel(Options{Theme: themeName})
	require.NoError(t, err)

	loaded, _ := model.Update(model.Init()())
	updated, _ := loaded.Update(tea.WindowSizeMsg{Width: width, Height: height})
	updatedModel, ok := updated.(Model)
	require.True(t, ok)
	return updatedModel
}

func updateWithRunes(t *testing.T, model Model, key string) Model {
	t.Helper()

	return updateWithMsg(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}

func updateWithKeyType(t *testing.T, model Model, keyType tea.KeyType) Model {
	t.Helper()

	return updateWithMsg(t, model, tea.KeyMsg{Type: keyType})
}

func updateWithMsg(t *testing.T, model Model, msg tea.KeyMsg) Model {
	t.Helper()

	updated, _ := model.Update(msg)
	updatedModel, ok := updated.(Model)
	require.True(t, ok)
	return updatedModel
}

// TestUpdateKeyConsultsKeyMap proves key handling reads the KeyMap rather
// than hard-coded strings: rebinding NextScreen must change which key cycles.
func TestUpdateKeyConsultsKeyMap(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	model.keys.NextScreen = key.NewBinding(key.WithKeys("n"))

	moved := updateWithRunes(t, model, "n")
	require.Equal(t, ScreenInstalledMods, moved.CurrentScreen())

	// The old default must no longer cycle once rebound away.
	stay := updateWithRunes(t, model, "l")
	require.Equal(t, ScreenDashboard, stay.CurrentScreen())
}

func TestNumberKeysJumpToScreens(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	for keyPress, want := range map[string]Screen{
		"1": ScreenDashboard,
		"2": ScreenInstalledMods,
		"3": ScreenSearch,
		"4": ScreenProfiles,
	} {
		require.Equal(t, want, updateWithRunes(t, model, keyPress).CurrentScreen(), "key %q", keyPress)
	}
}

func TestDashboardEnterOpensSelectedMenuEntry(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	// Initial selection is the first menu entry: Installed Mods.
	opened, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenInstalledMods, opened.(Model).CurrentScreen())

	// Second entry opens Search. Per the reporter's governing principle
	// (mop-up follow-up to Finding 1): EXPLICIT search intent focuses ("/"
	// and "3" already do); passive screen-cycling doesn't. Selecting "Search
	// Archives" from the dashboard menu via Enter IS explicit intent — the
	// user picked "search" by name — so this path must focus, unlike
	// NextScreen/PrevScreen/direct-jump cycling landing on Search in
	// passing (see TestTabCyclingOntoSearchDoesNotFocus).
	moved := updateWithRunes(t, model, "j")
	opened, _ = moved.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenSearch, opened.(Model).CurrentScreen())
	require.True(t, opened.(Model).search.input.Focused(), "dashboard menu's explicit Search Archives entry must auto-focus")
}

// TestDashboardEnterOnOracleEntryOpensConflicts replaces the old
// ...StaysPut test, which pinned Enter-on-Oracle as a NO-OP with the
// comment "no screen exists for it yet". Phase 6b shipped ScreenConflicts
// (v1.14.0) but neither the menu entry's target nor this test was updated,
// so the stale pin silently protected a dead menu entry until a user smoke
// test caught it (PR #113 round). Both dashboardMenu variants (default
// "Consult Conflict Oracle" and amber's "ASK CONFLICT ORACLE") must
// navigate.
func TestDashboardEnterOnOracleEntryOpensConflicts(t *testing.T) {
	t.Parallel()

	for _, themeName := range []string{"wizardry", "amber"} {
		t.Run(themeName, func(t *testing.T) {
			t.Parallel()

			model := sizedPrototypeModel(t, themeName, 100, 30)

			// Move to the last entry (Conflict Oracle). 4 presses:
			// Installed Mods -> Search -> Profiles -> Sources -> Oracle.
			for range 4 {
				model = updateWithRunes(t, model, "j")
			}
			opened, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			require.Equal(t, ScreenConflicts, opened.(Model).CurrentScreen(),
				"the Oracle menu entry must open the Conflicts screen")
			require.False(t, opened.(Model).search.input.Focused(),
				"conflicts is not a search-intent entry — no input focus")
		})
	}
}

func TestEnterOutsideDashboardIsANoop(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model = updateWithRunes(t, model, "2")

	opened, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenInstalledMods, opened.(Model).CurrentScreen())
}

// stubProvider is a no-op DataProvider implementing all 8 methods with their
// zero value (empty Summary/nil slice/nil error throughout) - meant to be
// embedded by a test fake that only needs to override the ONE method its
// test actually exercises, instead of restating every other method just to
// satisfy the interface (see failingProvider/emptyProvider/
// sentinelUpdatesProvider below for the pattern this replaced). Do NOT
// reach for this where a fake's explicitness IS the point - e.g.
// recordingProvider (below) exists specifically for its per-field
// configurability and call recording, and several fakes elsewhere in this
// package (noSourcesProvider, fakeSwitchableProvider, searchCancelProvider,
// conflictsFakeProvider, longSourcesProvider/longConflictsProvider) spell
// out every method because each one's return value independently matters to
// what its test documents - collapsing those onto this stub would trade a
// self-describing fake for one where "why does this method return X" needs
// a diff against stubProvider to answer.
type stubProvider struct{}

func (stubProvider) Overview(context.Context) (Summary, []ModItem, error) {
	return Summary{}, nil, nil
}
func (stubProvider) Profiles(context.Context) ([]ProfileItem, error) { return nil, nil }
func (stubProvider) Sources() []string                               { return nil }
func (stubProvider) SourceInfos(bool) []SourceInfo                   { return nil }
func (stubProvider) Search(context.Context, string, string, int, int) (SearchPage, error) {
	return SearchPage{}, nil
}
func (stubProvider) DeployedFiles(string, string) ([]string, error)    { return nil, nil }
func (stubProvider) ListGames() ([]GameInfo, error)                    { return nil, nil }
func (stubProvider) Conflicts(context.Context) ([]ConflictItem, error) { return nil, nil }
func (stubProvider) Health(context.Context) (HealthView, error)        { return HealthView{}, nil }

// failingProvider embeds stubProvider and overrides only Overview - the ONE
// method that matters here: loadData (app.go) calls Overview first and
// returns immediately on its error, before Profiles/Conflicts are ever
// reached, so TestLoadFailureRendersErrorState below never exercises any
// other method. (Sources() is still called unconditionally at NewModel time
// by newSearchModel, but that happens before Init/loadData runs and this
// test never touches the search screen, so stubProvider's nil default there
// is harmless too.)
type failingProvider struct {
	stubProvider
	err error
}

func (f failingProvider) Overview(context.Context) (Summary, []ModItem, error) {
	return Summary{}, nil, f.err
}

func TestModelShowsLoadingBeforeDataArrives(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: NewPrototypeProvider()})
	require.NoError(t, err)

	require.Contains(t, model.View(), "Consulting the archives")
}

func TestInitLoadsDataThroughProvider(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: NewPrototypeProvider()})
	require.NoError(t, err)

	msg := model.Init()()
	updated, _ := model.Update(msg)
	view := updated.(Model).View()

	require.Contains(t, view, "Skyrim Special Edition")
	require.NotContains(t, view, "Consulting the archives")
}

func TestLoadFailureRendersErrorState(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: failingProvider{err: errors.New("the archive door is sealed")}})
	require.NoError(t, err)

	msg := model.Init()()
	updated, _ := model.Update(msg)
	view := updated.(Model).View()

	require.Contains(t, view, "the archive door is sealed")
	require.Contains(t, view, "q: quit")
}

func TestNewModelRequiresProvider(t *testing.T) {
	t.Parallel()

	_, err := NewModel(Options{Theme: "wizardry"})
	require.ErrorContains(t, err, "provider")
}

// emptyProvider embeds stubProvider and overrides only Sources - and that
// override is load-bearing, not tidiness: newSearchModel (search.go) calls
// provider.Sources() unconditionally at construction, and an empty result
// flips the search screen straight to searchFailed ("no mod sources
// configured"), which TestEmptyStatesRenderHonestCopy below never expects -
// it drives screen 3 and asserts the ordinary idle-search hint. Every other
// DataProvider method's zero-value response (no mods, no profiles, empty
// search page) is exactly the honest-empty-state behavior this fake exists
// to produce, so stubProvider's defaults serve those unchanged.
type emptyProvider struct{ stubProvider }

func (emptyProvider) Sources() []string { return []string{"nexusmods"} }

func TestEmptyStatesRenderHonestCopy(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: emptyProvider{}})
	require.NoError(t, err)

	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	model = updateWithRunes(t, model, "2")
	require.Contains(t, model.View(), "No mods installed yet. 'lmm install <mod>' begins the quest.")

	model = updateWithRunes(t, model, "3")
	require.Contains(t, model.View(), "enter search · esc unfocus", "3 already focused the input")

	model = updateWithKeyType(t, model, tea.KeyEsc)
	require.Contains(t, model.View(), "/ focus · s source", "unfocused idle hint still tells the user how to refocus")
}

// sentinelUpdatesProvider mirrors coreProvider.Overview's real shape: no
// update check has ever run, so Updates/Conflicts report the -1 "unknown"
// sentinel from the very first load. Embeds stubProvider and overrides only
// Overview - TestFirstLoadHonorsProviderUpdatesSentinel below only ever
// reads model.summary.Updates after Init(), never the search screen (unlike
// emptyProvider above), so stubProvider's nil Sources() default - which
// would otherwise flip search to searchFailed - never comes into play here.
type sentinelUpdatesProvider struct{ stubProvider }

func (sentinelUpdatesProvider) Overview(context.Context) (Summary, []ModItem, error) {
	return Summary{Updates: -1, Conflicts: -1}, nil, nil
}

// TestFirstLoadHonorsProviderUpdatesSentinel guards the dataLoadedMsg
// preserve behavior (see mutations_test.go's
// TestDataLoadedMsgPreservesKnownUpdatesCountAcrossUnrelatedRefresh) against
// misfiring on the model's very FIRST load. Before any dataLoadedMsg has
// ever landed, m.summary is the zero-value Summary{} - Updates == 0 - which
// must NOT be mistaken for a "known" count of zero updates: a provider
// reporting the -1 sentinel on the first load (exactly coreProvider's real
// behavior before any check has run) must still render "?", not "0".
func TestFirstLoadHonorsProviderUpdatesSentinel(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: sentinelUpdatesProvider{}})
	require.NoError(t, err)

	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	require.Equal(t, -1, model.summary.Updates, "the first load must take the provider's own sentinel, not a preserved zero-value")
}

// recordingProvider wraps a delegate DataProvider and records the context
// passed to Overview for test verification. DeployedFilesResult/
// DeployedFilesErr configure DeployedFiles directly (no delegate call, no
// recording) - simple canned-return fields, since no test using this fake
// needs to observe DeployedFiles' arguments the way onOverview observes
// Overview's context. ListGamesResult/ListGamesErr/SetGameCalls/SetGameErr
// are Task 8's game-switch additions, mirroring DeployedFiles' own
// canned-return shape for ListGames and recordingActions' own *Calls-slice
// convention (actions_provider_test.go) for SetGame; AltSourceInfos/
// AltSources, once set, become what SourceInfos()/Sources() return after
// the FIRST SetGame call - letting a test prove the post-switch source
// re-seed (mutations.go's resolveGameSwitch) actually reads a fresh call
// rather than a value cached from before the switch. Now a POINTER receiver
// throughout (Task 8): SetGameCalls must accumulate visibly to the caller's
// own variable, which a value-receiver method can never do.
type recordingProvider struct {
	delegate            DataProvider
	onOverview          func(context.Context)
	DeployedFilesResult []string
	DeployedFilesErr    error
	// ConflictsResult/ConflictsErr are Task 3's canned-return fields for
	// Conflicts, mirroring DeployedFilesResult/-Err's own shape exactly (a
	// plain canned return, never delegated - see DeployedFiles' doc comment
	// for why: no test using this fake needs Conflicts to reflect the
	// delegate's own state).
	ConflictsResult []ConflictItem
	ConflictsErr    error

	ListGamesResult []GameInfo
	ListGamesErr    error
	SetGameCalls    []string
	SetGameErr      error
	AltSourceInfos  []SourceInfo
	AltSources      []string

	// SearchPageSizes records the pageSize argument of every Search call, in
	// order (#111 Tier 1's stickiness test) - lets a test prove a session's
	// n/p requeries keep passing the size computed at submit, even across an
	// intervening resize, without needing to inspect anything else about the
	// call.
	SearchPageSizes []int
}

func (r *recordingProvider) Overview(ctx context.Context) (Summary, []ModItem, error) {
	if r.onOverview != nil {
		r.onOverview(ctx)
	}
	return r.delegate.Overview(ctx)
}

func (r *recordingProvider) Profiles(ctx context.Context) ([]ProfileItem, error) {
	return r.delegate.Profiles(ctx)
}

func (r *recordingProvider) Sources() []string {
	if len(r.SetGameCalls) > 0 && r.AltSources != nil {
		return r.AltSources
	}
	return r.delegate.Sources()
}

func (r *recordingProvider) SourceInfos(all bool) []SourceInfo {
	if len(r.SetGameCalls) > 0 && r.AltSourceInfos != nil {
		return r.AltSourceInfos
	}
	return r.delegate.SourceInfos(all)
}

func (r *recordingProvider) Search(ctx context.Context, source, query string, page, pageSize int) (SearchPage, error) {
	r.SearchPageSizes = append(r.SearchPageSizes, pageSize)
	return r.delegate.Search(ctx, source, query, page, pageSize)
}

func (r *recordingProvider) DeployedFiles(sourceID, modID string) ([]string, error) {
	return r.DeployedFilesResult, r.DeployedFilesErr
}

func (r *recordingProvider) Conflicts(context.Context) ([]ConflictItem, error) {
	return r.ConflictsResult, r.ConflictsErr
}

func (r *recordingProvider) Health(ctx context.Context) (HealthView, error) {
	return r.delegate.Health(ctx)
}

func (r *recordingProvider) ListGames() ([]GameInfo, error) {
	return r.ListGamesResult, r.ListGamesErr
}

// SetGame implements actions.go's optional gameRebinder hook.
func (r *recordingProvider) SetGame(id string) error {
	r.SetGameCalls = append(r.SetGameCalls, id)
	return r.SetGameErr
}

func TestModelUsesProvidedContext(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")

	var seen context.Context
	provider := &recordingProvider{
		delegate:   NewPrototypeProvider(),
		onOverview: func(c context.Context) { seen = c },
	}
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Ctx: ctx})
	require.NoError(t, err)

	model.Init()()
	require.Equal(t, "marker", seen.Value(ctxKey{}))
}
