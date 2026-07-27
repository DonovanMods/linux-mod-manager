package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	return updateWithRunes(t, model, "3") // jump to search screen (focused)
}

// TestSlashRefocusesSearchInputAfterEsc covers "/"'s refocus behavior from
// within the search screen itself: entering via "3" already focuses (see
// TestNumberThreeJumpsAndFocuses), so this exercises the Esc-then-"/" path
// instead of a no-op re-press of "/" on an already-focused input.
func TestSlashRefocusesSearchInputAfterEsc(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	require.True(t, model.search.input.Focused(), "entering via 3 already focuses")

	model = updateWithKeyType(t, model, tea.KeyEsc)
	require.False(t, model.search.input.Focused())

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

func TestNumberThreeJumpsAndFocuses(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 100, 30)
	model = updateWithRunes(t, model, "3")
	require.Equal(t, ScreenSearch, model.CurrentScreen())
	require.True(t, model.search.input.Focused(), "3 must focus the input like every other entry path")
}

// TestEscThenSCyclesSourceProvingScreenKeysWork covers the other half of the
// entry-focuses/Esc-blurs contract: once blurred, screen-level keys (here,
// CycleSource's "s") must reach updateKey's outer switch instead of being
// swallowed as literal input.
func TestEscThenSCyclesSourceProvingScreenKeysWork(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t) // "3": ScreenSearch, focused
	model.search.sources = []string{"curseforge", "nexusmods"}
	require.True(t, model.search.input.Focused())

	model = updateWithKeyType(t, model, tea.KeyEsc)
	require.Equal(t, ScreenSearch, model.CurrentScreen())
	require.False(t, model.search.input.Focused())

	model = updateWithRunes(t, model, "s")
	require.Equal(t, 1, model.search.sourceIdx, "s must cycle the source once unfocused")
}

func TestTypingWhileFocusedDoesNotTriggerGlobalKeys(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)  // "3" already focused the input
	for _, r := range "quest124" { // q would quit; 1/2/4 would jump screens
		model = updateWithRunes(t, model, string(r))
	}
	require.Equal(t, ScreenSearch, model.CurrentScreen())
	require.Equal(t, "quest124", model.search.input.Value())
}

func TestEscBlursSearchInput(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t) // "3" already focused the input
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	require.False(t, updated.(Model).search.input.Focused())
}

func TestEnterSubmitsSearchAndRendersResults(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t) // "3" already focused the input
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
	model = updateWithKeyType(t, model, tea.KeyEsc) // s is a screen-level key; only reaches CycleSource once blurred
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

	model = updateWithKeyType(t, model, tea.KeyEsc) // s is a screen-level key; only reaches CycleSource once blurred
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
		model = updateWithRunes(t, model, "3") // already focused
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
	model = updateWithKeyType(t, model, tea.KeyEsc) // n/p are screen-level keys; only reach pagination once blurred
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "q", Source: "nexusmods", Page: 0, PageSize: 10, TotalCount: 25}

	updated, cmd := model.Update(keyRunes("n"))
	require.NotNil(t, cmd, "next page issues a search command")
	_ = updated

	model.search.page.Page = 0
	_, cmd = model.Update(keyRunes("p"))
	require.Nil(t, cmd, "prev on page 0 is a no-op")
}

// TestAggregateHasNextPageRespectsExhaustedNotSummedTotalCount guards #58
// item 1: an all-sources page (Source == "") reports a summed TotalCount
// that does NOT bound a single page the way single-source search's does
// (see hasNextPage's doc comment) - only Exhausted may be trusted. 3 sources
// x 10-mod catalogs (TotalCount 30, PageSize 10) used to make hasNextPage
// claim a page 2 that queries would return empty; Exhausted (now populated
// by coreProvider.Search from core.AggregateSearchResult) is the fix.
func TestAggregateHasNextPageRespectsExhaustedNotSummedTotalCount(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.state = searchReady
	model.search.page = SearchPage{
		Query: "m", Source: "", Page: 0, PageSize: 10, TotalCount: 30, Exhausted: true,
		Results: make([]ModItem, 30),
	}
	require.False(t, model.search.hasNextPage(), "every source already exhausted, despite TotalCount/PageSize suggesting 3 pages")

	model.search.page.Exhausted = false
	require.True(t, model.search.hasNextPage(), "a source might still have more")
}

// TestAggregateFooterOmitsMisleadingTotalPages guards the footer half of the
// same fix: aggregate TotalCount/PageSize cannot produce a trustworthy
// "Page N/M" figure (see searchFooterLine's doc comment), so it must not
// render one at all for an all-sources page, while still showing the result
// count and correctly omitting "n next" once Exhausted.
func TestAggregateFooterOmitsMisleadingTotalPages(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.state = searchReady
	model.search.page = SearchPage{
		Query: "m", Source: "", Page: 0, PageSize: 10, TotalCount: 30, Exhausted: true,
		Results: make([]ModItem, 30),
	}

	footer := model.searchFooterLine()
	require.NotContains(t, footer, "/", "aggregate TotalCount/PageSize must not render a misleading total-pages figure")
	require.Contains(t, footer, "30 results")
	require.NotContains(t, footer, "n next", "exhausted aggregate must not offer a dead next page")
}

// TestAggregateZeroResultsHonestNoticeWhenNoSourceSupportsSearch guards #58
// item 3's TUI half: AttemptedCount == 0 on an all-sources zero-results page
// means none of the game's sources support searching at all - a different
// condition than a capable source legitimately finding nothing - so the
// zero-results view must render a distinct, honest message instead of the
// ordinary "No archives matched" copy.
func TestAggregateZeroResultsHonestNoticeWhenNoSourceSupportsSearch(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "m", Source: "", AttemptedCount: 0}

	view := model.View()
	require.NotContains(t, view, "No archives matched", "must not look like an ordinary empty search")
	require.Contains(t, view, "support searching", "must render the honest no-searchable-sources notice")
}

// TestSingleSourceZeroResultsUnaffectedByHonestyNotice guards that the fix
// above is scoped to all-sources mode only: a single real source's ordinary
// zero-result page must keep its existing copy regardless of AttemptedCount
// (which single-source search never populates).
func TestSingleSourceZeroResultsUnaffectedByHonestyNotice(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "nothing", Source: "nexusmods"}

	view := model.View()
	require.Contains(t, view, "No archives matched")
}

func TestCtrlCQuitsWhileSearchInputFocused(t *testing.T) {
	t.Parallel()
	model := searchScreenModel(t) // "3" already focused the input
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

	model = updateWithRunes(t, model, "3") // jump to search screen (focused)
	require.Contains(t, model.View(), "All sources", "header labels the sentinel for humans")
}

func TestCycleSourceRotatesThroughAllThenReal(t *testing.T) {
	t.Parallel()

	// Prototype provider has exactly one real source ("nexusmods"), so the
	// sentinel-prefixed list is ["", "nexusmods"].
	model := searchScreenModel(t)
	require.Equal(t, "", model.search.source(), "starts on All sources")

	model = updateWithKeyType(t, model, tea.KeyEsc) // s is a screen-level key; only reaches CycleSource once blurred
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
func (noSourcesProvider) SourceInfos() []SourceInfo                       { return nil }
func (noSourcesProvider) Search(context.Context, string, string, int) (SearchPage, error) {
	return SearchPage{}, nil
}
func (noSourcesProvider) DeployedFiles(string, string) ([]string, error)    { return nil, nil }
func (noSourcesProvider) ListGames() ([]GameInfo, error)                    { return nil, nil }
func (noSourcesProvider) Conflicts(context.Context) ([]ConflictItem, error) { return nil, nil }

// TestZeroRealSourcesShowsConfiguredSourcesDiagnosticOnConstruction also
// guards #58 item 5's wording-parity fix: the TUI's diagnostic must match
// the CLI's own noSourcesConfiguredErr (cmd/lmm/search.go) verbatim,
// substituting the SAME game name via Options.GameName.
func TestZeroRealSourcesShowsConfiguredSourcesDiagnosticOnConstruction(t *testing.T) {
	t.Parallel()

	model, err := NewModel(Options{Theme: "wizardry", Provider: noSourcesProvider{}, GameName: "Test Game"})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	model = loaded.(Model)

	model = updateWithRunes(t, model, "3") // jump to search screen; no submit
	require.Equal(t, searchFailed, model.search.state, "diagnostic fires at construction, not just on submit")
	require.Contains(t, model.View(), "no mod sources configured")

	// The full message may be display-truncated at typical test widths, so
	// assert parity against the underlying error text directly, not View().
	require.EqualError(t, model.search.err,
		"no mod sources configured for Test Game; add sources with 'lmm game add' or edit games.yaml")
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

// TestZeroResultsWarningFitsPanelWidth guards the zero-results branch of
// searchView, where the warning line renders INSIDE searchSinglePanel
// instead of outside a panel like searchReadyView's header. The panel's
// content width is narrower than availableWidth() by its horizontal frame
// size (border + padding), so a warning line truncated only to
// availableWidth() can still overflow the panel and get re-wrapped by
// lipgloss, growing the view past the fixed height budget — this test
// reproduces that with a long per-source warning at a narrow terminal width.
func TestZeroResultsWarningFitsPanelWidth(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 40, 12)
	model = updateWithRunes(t, model, "3") // jump to search screen
	model.search.state = searchReady
	model.search.page = SearchPage{
		Query:  "sky",
		Source: "",
		Warnings: []string{
			`dead-repo: source "dead-repo": fetching manifest https://example.com/mods/registry/manifest.json: context deadline exceeded`,
		},
	}

	view := model.screenView()
	require.Equal(t, model.availableContentHeight(), lipgloss.Height(view),
		"an overlong warning must not wrap and grow the zero-results panel past its height budget")
	for _, line := range strings.Split(view, "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), model.availableWidth(), "no rendered line exceeds terminal width")
	}
}

func TestTruncateIsDisplayWidthAware(t *testing.T) {
	t.Parallel()

	require.LessOrEqual(t, lipgloss.Width(truncate("模组名称超长测试", 10)), 10)
	require.Equal(t, "short", truncate("short", 10))
}

// TestSubmitWithNoConfiguredSourcesFailsClearly also guards #58 item 5's
// wording-parity fix. searchScreenModel builds via NewPrototypeModel, which
// threads the canned active game's real name ("Skyrim Special Edition", see
// prototype.Data.Game) into searchModel.gameName - so the guard's message
// must name that game, matching the CLI's noSourcesConfiguredErr form.
func TestSubmitWithNoConfiguredSourcesFailsClearly(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t) // "3" already focused the input
	model.search.sources = nil
	for _, r := range "sky" {
		model = updateWithRunes(t, model, string(r))
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m := updated.(Model)
	require.Nil(t, cmd, "no search command without a source")
	require.Equal(t, searchFailed, m.search.state)
	require.Contains(t, m.View(), "no mod sources configured")

	// The full message may be display-truncated at typical test widths, so
	// assert parity against the underlying error text directly, not View().
	require.EqualError(t, m.search.err,
		"no mod sources configured for Skyrim Special Edition; add sources with 'lmm game add' or edit games.yaml")
}

// populatedSearchPage mirrors the field shape TestSearchViewRendersStates
// uses (search_test.go:283-289: Name/Author/Version/Status/Summary), but
// multiplies the rows out well past any short-terminal budget so the
// windowing/clamp tests below have something to scroll and clip. Result 0
// is "installed" so the styled-status test has a real target to find.
func populatedSearchPage() SearchPage {
	results := make([]ModItem, 0, 10)
	for i := range 10 {
		status := "available"
		if i == 0 {
			status = "installed"
		}
		results = append(results, ModItem{
			Name:    fmt.Sprintf("SkyMod%d", i),
			Author:  "schlangster",
			Version: "5.2",
			Status:  status,
			Summary: "UI overhaul.",
		})
	}
	return SearchPage{
		Query: "sky", Source: "nexusmods", Page: 0, PageSize: 10, TotalCount: 25,
		Results: results,
	}
}

// Selection walking past the visible rows previously left NO highlighted
// row anywhere (the pane rendered results[:maxLines] while the selection
// index kept climbing) even though the detail pane tracked the invisible
// selection (#42). The pane now scroll-follows the selection.
func TestSearchResultsPaneFollowsSelectionOnShortTerminals(t *testing.T) {
	t.Parallel()
	model := sizedPrototypeModel(t, "wizardry", 80, 12)
	model = updateWithRunes(t, model, "3")
	model = updateWithKeyType(t, model, tea.KeyEsc) // arrow keys are screen-level; only reach selection once blurred
	model.search.state = searchReady
	model.search.page = populatedSearchPage()

	last := len(model.search.page.Results) - 1
	for i := 0; i < last; i++ {
		model = updateWithMsg(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}
	require.Equal(t, last, model.selected[ScreenSearch])

	view := model.screenView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())
	require.Contains(t, view, "> ", "selected row must be visible")
	require.Contains(t, view, "↑", "clipped rows above must be named")
}

// The detail pane's 8 fixed field lines previously ignored maxLines
// entirely — on a floor-height terminal the pane's content budget can be
// as low as 3, so the fixed fields alone overflowed the pane (#42).
func TestSearchDetailPaneClampsFixedFieldsOnShortTerminals(t *testing.T) {
	t.Parallel()
	model := sizedPrototypeModel(t, "wizardry", 80, 8) // floor height
	model = updateWithRunes(t, model, "3")
	model.search.state = searchReady
	model.search.page = populatedSearchPage()

	view := model.screenView()
	require.LessOrEqual(t, lipgloss.Height(view), model.availableContentHeight())
}

// valueWidth's floor of 1 could exceed innerWidth in pathologically narrow
// panes (innerWidth 1 → label 1 + value 1 = 2 columns) (#42). Zero-width
// values must render nothing rather than overflow; every rendered line
// stays within the pane width.
func TestSearchDetailPaneAllowsZeroWidthValues(t *testing.T) {
	t.Parallel()
	model := sizedPrototypeModel(t, "wizardry", 40, 24)
	model = updateWithRunes(t, model, "3")
	model.search.state = searchReady
	model.search.page = populatedSearchPage()

	view := model.screenView()
	for _, line := range strings.Split(view, "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), model.availableWidth())
	}
}

// The results list styles "installed" via WarningText but the detail
// pane's Status field was plain — the one place the status is spelled out
// was the one place it didn't pop (#42 cosmetic item). Two traps make the
// naive whole-view assertion vacuous, so this test dodges both:
//
//  1. It targets searchDetailPane DIRECTLY rather than the composed
//     screenView — the results list renders the identical styled bytes for
//     the same item, so a whole-view Contains is satisfied by the list and
//     guards nothing about the detail pane.
//  2. It swaps in a Transform-marked WarningText — in this non-TTY test
//     environment lipgloss degrades to no color, so the real style's
//     Render output is byte-identical to the plain value and Contains
//     could never fail. The Transform marker makes "styled" observable
//     without ANSI while still exercising the pane's real
//     m.theme.WarningText code path.
func TestSearchDetailPaneStylesInstalledStatus(t *testing.T) {
	t.Parallel()
	model := sizedPrototypeModel(t, "wizardry", 100, 30)
	model = updateWithRunes(t, model, "3")
	model.search.state = searchReady
	model.search.page = populatedSearchPage() // result 0 ("selected" by default) has Status "installed"
	model.theme.WarningText = model.theme.WarningText.Transform(func(s string) string { return "«" + s + "»" })

	// At width 40 the pane's innerWidth is 36 → labelWidth 13, valueWidth
	// 23, so "installed" (9 runes) renders untruncated and the styled form
	// must appear verbatim.
	view := model.searchDetailPane(40, 30)
	require.Contains(t, view, model.theme.WarningText.Render("installed"))
	require.Equal(t, "«installed»", model.theme.WarningText.Render("installed"),
		"marker sanity: the styled form must be distinguishable from the plain value")
}
