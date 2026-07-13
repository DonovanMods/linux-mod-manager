package tui

import (
	"context"
	"errors"
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

func TestArrowAndVimKeysNavigateScreens(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	model = updateWithKeyType(t, model, tea.KeyRight)
	require.Equal(t, ScreenInstalledMods, model.CurrentScreen())

	model = updateWithRunes(t, model, "l")
	require.Equal(t, ScreenSearch, model.CurrentScreen())

	model = updateWithKeyType(t, model, tea.KeyLeft)
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

	model := sizedPrototypeModel(t, "wizardry", 120, 36)
	model = updateWithRunes(t, model, "?")

	view := model.View()
	require.Equal(t, 120, lipgloss.Width(view))
	require.Equal(t, 36, lipgloss.Height(view))
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

	// Second entry opens Search.
	moved := updateWithRunes(t, model, "j")
	opened, _ = moved.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, ScreenSearch, opened.(Model).CurrentScreen())
}

func TestDashboardEnterOnOracleEntryStaysPut(t *testing.T) {
	t.Parallel()

	model, err := NewPrototypeModel(Options{Theme: "wizardry"})
	require.NoError(t, err)

	// Move to the last entry (Conflict Oracle) — no screen exists for it yet.
	for range 3 {
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
	require.Contains(t, model.View(), "/ focus · enter search · s source")
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
