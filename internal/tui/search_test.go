package tui

import (
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
