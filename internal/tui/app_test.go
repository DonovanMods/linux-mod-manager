package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

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

func TestNewPrototypeModelRejectsInvalidTheme(t *testing.T) {
	t.Parallel()

	_, err := NewPrototypeModel(Options{Theme: "bogus"})
	require.Error(t, err)
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

func TestScreenViewsUseExactAvailableHeightOnLargeTerminals(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 120, 36)

	for _, screen := range screens {
		model.screen = screen
		require.Equal(t, model.availableContentHeight(), lipgloss.Height(model.screenView()), screen.String())
	}
}

func TestViewFitsTerminalBoundsWithHelpVisible(t *testing.T) {
	t.Parallel()

	// Height bumped 37->39->40 across Phase 5b Task 5's two new help lines
	// ("i" install, added first; "u" check-updates, added second - see
	// helpView). Verified empirically at each step (scratch probes sweeping
	// a height range, since removed) the same way 5a proved its own 36->37
	// bump: below the fitting height, the rendered view consistently comes
	// out one row taller than the requested terminal height (lipgloss pads
	// SHORT content but never clips content taller than the requested
	// budget) - the party-sheet dashboard's split-panel math
	// (partyDashboardView's topHeight/menuHeight, both integer divisions of
	// availableContentHeight) hits its natural minimum before the requested
	// budget does. Height=40 is the first value where the requested content
	// budget (with BOTH new help lines present) finally reaches that same
	// natural minimum, so the view fits with exactly zero slack (41 and
	// above, the content grows to fill the larger budget instead). This
	// pins the current zero-slack floor - see task-5-brief.md's "prove
	// pre-existing saturation... like 5a did" allowance for justified
	// height adjustments.
	model := sizedPrototypeModel(t, "wizardry", 120, 40)
	model = updateWithRunes(t, model, "?")

	view := model.View()
	require.Equal(t, 120, lipgloss.Width(view))
	require.Equal(t, 40, lipgloss.Height(view))
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

func TestDashboardEnterOnOracleEntryStaysPut(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	// Move to the last entry (Conflict Oracle) — no screen exists for it yet.
	// 4 presses: Installed Mods -> Search -> Profiles -> Sources -> Oracle.
	for range 4 {
		model = updateWithRunes(t, model, "j")
	}
	opened, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenDashboard, opened.(Model).CurrentScreen())
}

func TestEnterOutsideDashboardIsANoop(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)
	model = updateWithRunes(t, model, "2")

	opened, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenInstalledMods, opened.(Model).CurrentScreen())
}

type failingProvider struct{ err error }

func (f failingProvider) Overview(context.Context) (Summary, []ModItem, error) {
	return Summary{}, nil, f.err
}
func (f failingProvider) Profiles(context.Context) ([]ProfileItem, error) { return nil, f.err }
func (f failingProvider) Sources() []string                               { return []string{"nexusmods"} }
func (f failingProvider) SourceInfos() []SourceInfo                       { return nil }
func (f failingProvider) Search(context.Context, string, string, int) (SearchPage, error) {
	return SearchPage{}, f.err
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

type emptyProvider struct{}

func (emptyProvider) Overview(context.Context) (Summary, []ModItem, error) {
	return Summary{}, nil, nil
}
func (emptyProvider) Profiles(context.Context) ([]ProfileItem, error) { return nil, nil }
func (emptyProvider) Sources() []string                               { return []string{"nexusmods"} }
func (emptyProvider) SourceInfos() []SourceInfo                       { return nil }
func (emptyProvider) Search(context.Context, string, string, int) (SearchPage, error) {
	return SearchPage{}, nil
}

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

// recordingProvider wraps a delegate DataProvider and records the context
// passed to Overview for test verification.
type recordingProvider struct {
	delegate   DataProvider
	onOverview func(context.Context)
}

func (r recordingProvider) Overview(ctx context.Context) (Summary, []ModItem, error) {
	if r.onOverview != nil {
		r.onOverview(ctx)
	}
	return r.delegate.Overview(ctx)
}

func (r recordingProvider) Profiles(ctx context.Context) ([]ProfileItem, error) {
	return r.delegate.Profiles(ctx)
}

func (r recordingProvider) Sources() []string {
	return r.delegate.Sources()
}

func (r recordingProvider) SourceInfos() []SourceInfo {
	return r.delegate.SourceInfos()
}

func (r recordingProvider) Search(ctx context.Context, source, query string, page int) (SearchPage, error) {
	return r.delegate.Search(ctx, source, query, page)
}

func TestModelUsesProvidedContext(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")

	var seen context.Context
	provider := recordingProvider{
		delegate:   NewPrototypeProvider(),
		onOverview: func(c context.Context) { seen = c },
	}
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider, Ctx: ctx})
	require.NoError(t, err)

	model.Init()()
	require.Equal(t, "marker", seen.Value(ctxKey{}))
}
