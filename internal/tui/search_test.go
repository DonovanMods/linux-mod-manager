package tui

import (
	"context"
	"errors"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// keyRunes builds a KeyMsg carrying a single-character rune press, matching
// the construction updateWithRunes uses internally.
func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func searchScreenModel(t *testing.T) Model {
	t.Helper()
	model := sizedPrototypeModel(t, "wizardry", 100, 30)
	return updateWithRunes(t, model, "3") // jump to search screen (blurred)
}

func TestSlashFocusesSearchInputOnSearchScreen(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model = updateWithRunes(t, model, "/")
	require.True(t, model.search.input.Focused())
}

func TestSlashFromAnyScreenJumpsAndFocuses(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 30)
	require.Equal(t, ScreenDashboard, model.CurrentScreen())

	model = updateWithRunes(t, model, "/")
	require.Equal(t, ScreenSearch, model.CurrentScreen())
	require.True(t, model.search.input.Focused(), "single / must be enough to type")

	for _, r := range "sky" {
		model = updateWithRunes(t, model, string(r))
	}
	require.Equal(t, "sky", model.search.input.Value())
}

func TestNumberThreeJumpsWithoutFocusing(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 30)
	model = updateWithRunes(t, model, "3")
	require.Equal(t, ScreenSearch, model.CurrentScreen())
	require.False(t, model.search.input.Focused(), "3 is pure navigation")
}

func TestTypingWhileFocusedDoesNotTriggerGlobalKeys(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model = updateWithRunes(t, model, "/")
	for _, r := range "quest124" { // q would quit; 1/2/4 would jump screens
		model = updateWithRunes(t, model, string(r))
	}
	require.Equal(t, ScreenSearch, model.CurrentScreen())
	require.Equal(t, "quest124", model.search.input.Value())
}

func TestEscBlursSearchInput(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model = updateWithRunes(t, model, "/")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.False(t, updated.(Model).search.input.Focused())
}

func TestEnterSubmitsSearchAndRendersResults(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model = updateWithRunes(t, model, "/")
	for _, r := range "frost" {
		model = updateWithRunes(t, model, string(r))
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.Equal(t, searchLoading, model.search.state)
	require.NotNil(t, cmd)

	result, _ := model.Update(cmd())
	model = result.(Model)
	require.Equal(t, searchReady, model.search.state)
	require.Len(t, model.search.page.Results, 1)
	require.Equal(t, "Frostfall", model.search.page.Results[0].Name)
}

func TestStaleSearchResultsAreDiscarded(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.gen = 5
	model.search.state = searchLoading

	updated, _ := model.Update(searchResultMsg{gen: 4, page: SearchPage{Query: "stale"}})
	require.Equal(t, searchLoading, updated.(Model).search.state, "stale gen must be ignored")
}

func TestAuthRequiredBecomesFirstClassState(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.gen = 1
	updated, _ := model.Update(searchFailedMsg{
		gen:    1,
		err:    fmt.Errorf("%w: key required", domain.ErrAuthRequired),
		source: "nexusmods",
	})
	m := updated.(Model)
	require.Equal(t, searchAuthRequired, m.search.state)
	require.Equal(t, "nexusmods", m.search.authSource)
}

// TestAllSourcesAuthFailureShowsPerSourceDetail covers the sentinel
// ("" == all sources) case: when every source fails on auth, the joined
// error still satisfies errors.Is(err, domain.ErrAuthRequired), but routing
// it to searchAuthRequired would render "Authentication required for ." and
// a broken "lmm auth login " hint (msg.source is the sentinel, not a real
// source). It must fall through to searchFailed instead, whose rendering
// already names each failing source.
func TestAllSourcesAuthFailureShowsPerSourceDetail(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.gen = 1
	joined := errors.Join(
		fmt.Errorf("source nexusmods: %w", domain.ErrAuthRequired),
		fmt.Errorf("source curseforge: %w", domain.ErrAuthRequired),
	)
	updated, _ := model.Update(searchFailedMsg{
		gen:    1,
		err:    fmt.Errorf("all 2 source(s) failed: %w", joined),
		source: "",
	})
	m := updated.(Model)
	require.Equal(t, searchFailed, m.search.state, "sentinel source must not route to the single-source auth state")

	view := m.View()
	require.Contains(t, view, "nexusmods", "failed view must retain the per-source detail")
	require.NotContains(t, view, "Authentication required for .", "must not render the broken sentinel message")
	require.NotContains(t, view, "lmm auth login '", "must not render a broken auth-login hint for an empty source")
}

func TestCycleSourceKey(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.sources = []string{"curseforge", "nexusmods"}
	model = updateWithRunes(t, model, "s")
	require.Equal(t, 1, model.search.sourceIdx)
	model = updateWithRunes(t, model, "s")
	require.Equal(t, 0, model.search.sourceIdx, "cycling wraps")
}

func TestCycleSourceInvalidatesInFlightAndResults(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.sources = []string{"curseforge", "nexusmods"}
	model.search.state = searchLoading
	model.search.gen = 3

	model = updateWithRunes(t, model, "s")
	require.Equal(t, searchIdle, model.search.state, "cycling resets state")
	require.Greater(t, model.search.gen, 3, "gen bumped so in-flight results are stale")

	// The in-flight result from the old source must now be discarded.
	updated, _ := model.Update(searchResultMsg{gen: 3, page: SearchPage{Source: "curseforge", Query: "x"}})
	require.Equal(t, searchIdle, updated.(Model).search.state)
}

func TestReadyHeaderShowsResultSourceNotTarget(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.sources = []string{"curseforge", "nexusmods"}
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "q", Source: "nexusmods", PageSize: 10, TotalCount: 1,
		Results: []ModItem{{Name: "A", Status: "available"}}}
	model.search.sourceIdx = 0 // target is curseforge, results are nexusmods

	require.Contains(t, model.View(), "nexusmods", "ready view labels the results' actual source")
}

func TestLongQueryDoesNotBreakSearchHeightInvariant(t *testing.T) {
	t.Parallel()

	for _, width := range []int{44, 48, 60, 80} {
		model := sizedPrototypeModel(t, "wizardry", width, 24)
		model = updateWithRunes(t, model, "3")
		model = updateWithRunes(t, model, "/")
		for range 100 {
			model = updateWithRunes(t, model, "x")
		}
		require.Equal(t, model.availableContentHeight(), lipgloss.Height(model.screenView()), "height at %d", width)
		require.Equal(t, model.availableWidth(), lipgloss.Width(model.screenView()), "width at %d", width)
	}
}

func TestPaginationKeysRequeryWithinBounds(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "q", Source: "nexusmods", Page: 0, PageSize: 10, TotalCount: 25}

	updated, cmd := model.Update(keyRunes("n"))
	require.NotNil(t, cmd, "next page issues a search command")
	_ = updated

	model.search.page.Page = 0
	_, cmd = model.Update(keyRunes("p"))
	require.Nil(t, cmd, "prev on page 0 is a no-op")
}

func TestCtrlCQuitsWhileSearchInputFocused(t *testing.T) {
	t.Parallel()
	model := searchScreenModel(t)
	model = updateWithRunes(t, model, "/") // focus
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	require.NotNil(t, cmd)
	require.Equal(t, tea.Quit(), cmd())
}

func TestSearchViewRendersStates(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)

	require.Contains(t, model.View(), "search the archives", "idle shows the input placeholder")

	model.search.state = searchAuthRequired
	model.search.authSource = "nexusmods"
	view := model.View()
	require.Contains(t, view, "lmm auth login nexusmods")

	model.search.state = searchFailed
	model.search.err = errors.New("the aether is down")
	require.Contains(t, model.View(), "the aether is down")

	model.search.state = searchReady
	model.search.page = SearchPage{
		Query: "sky", Source: "nexusmods", Page: 0, PageSize: 10, TotalCount: 12,
		Results: []ModItem{
			{Name: "SkyUI", Author: "schlangster", Version: "5.2", Status: "installed", Summary: "UI overhaul.", Downloads: 1000, Endorsements: 50, HasEndorsements: true},
			{Name: "SkyFresh", Author: "someone", Version: "1.0", Status: "available"},
		},
	}
	view = model.View()
	require.Contains(t, view, "SkyUI")
	require.Contains(t, view, "installed")
	require.Contains(t, view, "Page 1/2")
	require.Contains(t, view, "UI overhaul.", "detail panel shows the selected result's summary")

	model.search.page.Results = append(model.search.page.Results,
		ModItem{Name: "SkyUnknown", Author: "someone", Version: "0.1", Status: "available", HasEndorsements: false})
	model.selected[ScreenSearch] = len(model.search.page.Results) - 1
	view = model.View()
	require.Contains(t, view, "Endorsements ?", "unknown endorsements render as ?")

	model.search.page = SearchPage{Query: "nothing", Source: "nexusmods", PageSize: 10}
	view = model.View()
	require.Contains(t, view, "No archives matched", "zero-result state renders honest copy")
}

func TestSearchDefaultsToAllSources(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 30)
	require.Equal(t, "", model.search.sources[0], "the all-sources sentinel is prepended")
	require.Equal(t, "", model.search.source(), "default target is All sources")

	model = updateWithRunes(t, model, "3") // jump to search screen (blurred)
	require.Contains(t, model.View(), "All sources", "header labels the sentinel for humans")
}

func TestCycleSourceRotatesThroughAllThenReal(t *testing.T) {
	t.Parallel()

	// Prototype provider has exactly one real source ("nexusmods"), so the
	// sentinel-prefixed list is ["", "nexusmods"].
	model := searchScreenModel(t)
	require.Equal(t, "", model.search.source(), "starts on All sources")

	model = updateWithRunes(t, model, "s")
	require.Equal(t, "nexusmods", model.search.source(), "cycles to the one real source")

	model = updateWithRunes(t, model, "s")
	require.Equal(t, "", model.search.source(), "wraps back to All sources")
}

func TestSearchWarningLineRendered(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.gen = 1
	updated, _ := model.Update(searchResultMsg{gen: 1, page: SearchPage{
		Query: "sky", Source: "", PageSize: 10, TotalCount: 1,
		Results:  []ModItem{{Name: "SkyUI", Source: "nexusmods", Status: "available"}},
		Warnings: []string{"curseforge: connection refused"},
	}})
	m := updated.(Model)

	view := m.searchView()
	require.Contains(t, view, "⚠", "warning marker renders")
	require.Contains(t, view, "curseforge", "warning names the failing source")
}

// noSourcesProvider has zero configured sources, exercising the
// zero-real-sources diagnostic path (see newSearchModel).
type noSourcesProvider struct{}

func (noSourcesProvider) Overview(context.Context) (Summary, []ModItem, error) {
	return Summary{}, nil, nil
}
func (noSourcesProvider) Profiles(context.Context) ([]ProfileItem, error) { return nil, nil }
func (noSourcesProvider) Sources() []string                               { return nil }
func (noSourcesProvider) Search(context.Context, string, string, int) (SearchPage, error) {
	return SearchPage{}, nil
}

func TestZeroRealSourcesShowsConfiguredSourcesDiagnosticOnConstruction(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: noSourcesProvider{}})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	model = updateWithRunes(t, model, "3") // jump to search screen; no submit
	require.Equal(t, searchFailed, model.search.state, "diagnostic fires at construction, not just on submit")
	require.Contains(t, model.View(), "no mod sources configured")
}

func TestSearchViewStaysWithinBounds(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t) // 100x30
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "q", Source: "nexusmods", PageSize: 10, TotalCount: 10,
		Results: []ModItem{{Name: "A", Status: "available"}}}
	require.Equal(t, model.availableWidth(), lipgloss.Width(model.screenView()))
	require.Equal(t, model.availableContentHeight(), lipgloss.Height(model.screenView()))
}

func TestSearchReadyViewFitsNarrowTerminals(t *testing.T) {
	t.Parallel()

	for _, width := range []int{40, 48, 54, 80} {
		model := sizedPrototypeModel(t, "wizardry", width, 24)
		model = updateWithRunes(t, model, "3")
		model.search.state = searchReady
		model.search.page = SearchPage{
			Query: "sky", Source: "nexusmods", Page: 0, PageSize: 10, TotalCount: 25,
			Results: []ModItem{{Name: "SkyUI", Author: "schlangster", Version: "5.2", Status: "installed", Summary: "UI overhaul."}},
		}
		require.Equal(t, model.availableWidth(), lipgloss.Width(model.screenView()), "width %d", width)
	}
}

func TestTruncateIsDisplayWidthAware(t *testing.T) {
	t.Parallel()

	require.LessOrEqual(t, lipgloss.Width(truncate("模组名称超长测试", 10)), 10)
	require.Equal(t, "short", truncate("short", 10))
}

func TestSubmitWithNoConfiguredSourcesFailsClearly(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.sources = nil
	model = updateWithRunes(t, model, "/")
	for _, r := range "sky" {
		model = updateWithRunes(t, model, string(r))
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m := updated.(Model)
	require.Nil(t, cmd, "no search command without a source")
	require.Equal(t, searchFailed, m.search.state)
	require.Contains(t, m.View(), "no mod sources configured")
}
