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

// TestNextPageAcceleratorJumpsSelectionByFetchSize guards #111 Tier 3's n/p
// repurposing: n is now a selection accelerator (a paneful jump), not a
// page turn - there are no pages under infinite scroll.
func TestNextPageAcceleratorJumpsSelectionByFetchSize(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model = updateWithKeyType(t, model, tea.KeyEsc) // n/p are screen-level keys; only reach them once blurred
	model.search.state = searchReady
	model.search.fetchSize = 5
	model.search.buffer = make([]ModItem, 20)
	model.search.providerExhausted = true // no refill to worry about here
	model.search.page = SearchPage{Query: "q", Source: "nexusmods", Results: model.search.buffer}

	updated, _ := model.Update(keyRunes("n"))
	require.Equal(t, 5, updated.(Model).selected[ScreenSearch], "n jumps the selection by fetchSize")
}

// TestAggregateExhaustedTrustedOverSummedTotalCount guards #58 item 1's
// concern under #111 Tier 3's buffered accounting: an all-sources session's
// TotalCount is SUMMED across sources with independent per-round cursors
// and does not bound how much is actually left - only providerExhausted
// (populated from core.AggregateSearchResult.Exhausted) may be trusted, so
// the footer must report "all N shown" once it's true even though a naive
// TotalCount-vs-loaded comparison would still suggest more.
func TestAggregateExhaustedTrustedOverSummedTotalCount(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.state = searchReady
	model.search.buffer = make([]ModItem, 30)
	model.search.fetchSize = 10
	model.search.providerExhausted = true
	model.search.page = SearchPage{Query: "m", Source: "", TotalCount: 30, Results: model.search.buffer}

	require.Contains(t, model.searchFooterLine(), "all 30 shown")
}

// TestAggregateFooterNeverRendersTotalPages guards the footer half of #58
// item 1 under #111 Tier 3's infinite scroll: an all-sources session has no
// "Page N/M" concept at all anymore (there are no pages), and once
// providerExhausted the footer must say "all N shown" using the REAL
// buffer length, never a figure derived from the summed (and possibly
// under-reporting) TotalCount.
func TestAggregateFooterNeverRendersTotalPages(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.state = searchReady
	model.search.buffer = make([]ModItem, 30)
	model.search.providerExhausted = true
	model.search.page = SearchPage{Query: "m", Source: "", TotalCount: 30, Results: model.search.buffer}

	footer := model.searchFooterLine()
	require.NotContains(t, footer, "/", "aggregate sessions never render a total-pages figure")
	require.Equal(t, "all 30 shown", footer)
}

// TestAggregateFooterCountsBufferedRowsNotSummedTotals guards the count half
// of the aggregate footer (user smoke finding, PR #110): the summed
// TotalCount under-reports whenever any contributing source doesn't report
// totals (its contribution to the sum is 0 while its rows are real), so a
// 13-row buffer once rendered "(3 results)". The aggregate footer must
// count len(buffer) - the only figure that is true by construction - never
// the summed totals.
func TestAggregateFooterCountsBufferedRowsNotSummedTotals(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.state = searchReady
	// One source reported TotalCount 3; another contributed 10 rows but
	// reports no totals (contributes 0 to the sum) — 13 real rows.
	model.search.buffer = make([]ModItem, 13)
	model.search.providerExhausted = false
	model.search.page = SearchPage{Query: "m", Source: "", TotalCount: 3, Results: model.search.buffer}

	require.Equal(t, "13 loaded · more available", model.searchFooterLine(),
		"the footer must count the buffered rows, never the under-reported summed total")
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

// TestHonestNoticeFallsBackToThisGameWithoutAGameName guards the notice's
// displayGameName fallback: a model constructed without Options.GameName
// (a test double, or any future name-less construction) must render
// "None of this game's sources..." rather than the malformed possessive
// "None of 's sources..." (#58 review follow-up; same fallback
// noSourcesConfiguredErr has always had).
func TestHonestNoticeFallsBackToThisGameWithoutAGameName(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.gameName = ""
	model.search.state = searchReady
	model.search.page = SearchPage{Query: "m", Source: "", AttemptedCount: 0}

	view := model.View()
	require.Contains(t, view, "None of this game's sources", "empty game name must fall back, not render a malformed possessive")
	require.NotContains(t, view, "None of 's sources", "the malformed form must be impossible")
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

// TestPrototypeAllSourcesSearchHasNoDeadNextPageHint is an integration
// counterpart to TestAggregateExhaustedTrustedOverSummedTotalCount that
// drives the REAL --prototype Search path (searchScreenModel's default
// sourceIdx 0 is the all-sources sentinel) instead of hand-built session
// state - a whole-branch-review finding, still relevant under #111 Tier 3:
// prototypeProvider.Search must set Exhausted honestly on its one-round
// canned set, or every --prototype all-sources search would render a dead
// "more available" hint that scrolling into never actually refills.
func TestPrototypeAllSourcesSearchHasNoDeadNextPageHint(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t) // "3" already focused; default source is "" (all-sources)
	for _, r := range "sky" {
		model = updateWithRunes(t, model, string(r))
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)
	result, _ := model.Update(cmd())
	model = result.(Model)
	require.Equal(t, searchReady, model.search.state)

	require.True(t, model.search.providerExhausted, "the canned set is a single-round fetch; there is nothing left to demo scrolling into")
	require.NotContains(t, model.View(), "more available", "must not offer a dead more-available hint")
}

// TestPrototypeAllSourcesZeroMatchRendersOrdinaryEmptyCopy is the
// integration counterpart to
// TestAggregateZeroResultsHonestNoticeWhenNoSourceSupportsSearch: a genuine
// zero-match --prototype all-sources search (query that hits none of the
// canned mods) must render the ORDINARY "No archives matched" copy, not the
// no-searchable-sources honesty notice - prototypeProvider.Search never set
// AttemptedCount, so it defaulted to 0, the exact "no source supports
// search" signal, which is false in --prototype mode (all 3 canned sources
// advertise search capability).
func TestPrototypeAllSourcesZeroMatchRendersOrdinaryEmptyCopy(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t) // default source is "" (all-sources)
	for _, r := range "zzz-nothing-matches" {
		model = updateWithRunes(t, model, string(r))
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)
	result, _ := model.Update(cmd())
	model = result.(Model)
	require.Equal(t, searchReady, model.search.state)
	require.Empty(t, model.search.page.Results)

	view := model.View()
	require.Contains(t, view, "No archives matched", "a genuine zero-match demo search is not the same as zero searchable sources")
	require.NotContains(t, view, "support searching")
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
	require.Contains(t, view, "2 of 12 loaded")
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
func (noSourcesProvider) SourceInfos(bool) []SourceInfo                   { return nil }
func (noSourcesProvider) Search(context.Context, string, string, int, int) (SearchPage, error) {
	return SearchPage{}, nil
}
func (noSourcesProvider) DeployedFiles(string, string) ([]string, error)    { return nil, nil }
func (noSourcesProvider) ListGames() ([]GameInfo, error)                    { return nil, nil }
func (noSourcesProvider) Conflicts(context.Context) ([]ConflictItem, error) { return nil, nil }
func (noSourcesProvider) Health(context.Context) (HealthView, error)        { return HealthView{}, nil }
func (noSourcesProvider) GetModDetails(context.Context, ModItem) (ModDetails, error) {
	return ModDetails{}, nil
}

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

// --- #111 Tier 1: window-sized search fetch ---

// TestSearchFetchSizeDerivesFromPaneBudget pins the exact value
// Model.searchFetchSize derives at 80x24 for an ALL-SOURCES session (the
// search screen's default sourceIdx targets the "" sentinel - see
// newSearchModel), verified against searchReadyView's REAL arithmetic (not
// assumed numbers) via a throwaway probe: availableContentHeight() is 17 at
// this size (24 - App's vertical frame size (2) - contentChromeHeight() (5,
// with no help/status shown)); searchReadyView's own paneContentHeight
// subtracts len(header) (2), the footer (1), and the panel's vertical
// border (2) from that (17-5=12); #111 Tier 2's worst-case reservations
// (PR #114 smoke round) then subtract statusReserve (1, unconditional) and
// warningReserve (1, all-sources only) on top - 12-1-1=10.
func TestSearchFetchSizeDerivesFromPaneBudget(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 80, 24)
	require.Equal(t, 10, model.searchFetchSize(),
		"80x24 all-sources: availableContentHeight(17) - header(2) - footer(1) - panel border(2) - status(1) - warning(1)")
}

// TestSearchFetchSizeReservesOneFewerRowForSingleSourceSessions guards
// #111 Tier 2's split between the two worst-case reservations: a
// single-source session can never render an all-sources warning line (see
// searchFetchSize's own doc comment), so it only pays statusReserve, one
// row MORE than an equivalent all-sources session at the same size (11 vs
// 10 at 80x24 - see TestSearchFetchSizeDerivesFromPaneBudget for that
// value's own derivation).
func TestSearchFetchSizeReservesOneFewerRowForSingleSourceSessions(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 80, 24)
	allSources := model.searchFetchSize()

	model.search.sourceIdx = 1 // a real single source ("nexusmods"), not the "" all-sources sentinel
	require.NotEqual(t, "", model.search.source(), "test sanity: sourceIdx 1 must be a real source")
	single := model.searchFetchSize()

	require.Equal(t, 10, allSources)
	require.Equal(t, 11, single, "single-source sessions skip the warning-line reservation")
}

// TestSearchFetchSizeFloorsOnShortTerminals guards the [SearchPageSize, cap]
// clamp's floor: at 40x12, availableContentHeight() itself hits its own
// floor (8), so the raw budget (8-5-1-1=1 all-sources, 8-5-1=2 single-
// source) would starve a search of nearly all results without the clamp -
// SearchPageSize (10) is the floor exactly because a search must always
// return a useful page, even on the smallest supported terminal. Both
// reservations already floor to the same value here, so this test doesn't
// distinguish source flavors the way
// TestSearchFetchSizeReservesOneFewerRowForSingleSourceSessions does.
func TestSearchFetchSizeFloorsOnShortTerminals(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 40, 12)
	require.Equal(t, SearchPageSize, model.searchFetchSize())
}

// TestSearchFetchSizeCapsOnTallTerminals guards the clamp's cap: a very tall
// terminal's raw budget (108 unreserved at 120x120, still 106/107 after the
// worst-case reservations) must not translate into a request for 100+
// results from a real source's API in one call.
func TestSearchFetchSizeCapsOnTallTerminals(t *testing.T) {
	t.Parallel()

	model := sizedPrototypeModel(t, "wizardry", 120, 120)
	require.Equal(t, 50, model.searchFetchSize())
}

// TestSearchFetchSizeStickyAcrossResizeWithinSession is the stickiness half
// of #111 Tier 1/3: a query session's fetch size is fixed at submit and
// must survive a resize until the NEXT fresh submit, even though scroll-
// triggered refills reuse it too. recordingProvider.SearchPageSizes
// captures the pageSize argument of every Search call in order, so this can
// assert on the actual wire value a provider received rather than on
// internal model state alone.
func TestSearchFetchSizeStickyAcrossResizeWithinSession(t *testing.T) {
	t.Parallel()

	rec := &recordingProvider{delegate: NewPrototypeProvider()}
	model, err := NewModel(Options{Theme: "wizardry", Provider: rec})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	sized, _ := loaded.(Model).Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = sized.(Model)

	model = updateWithRunes(t, model, "3") // jump to search, focused
	for _, r := range "sky" {
		model = updateWithRunes(t, model, string(r))
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)
	result, _ := model.Update(cmd())
	model = result.(Model)
	require.Equal(t, searchReady, model.search.state)

	require.Len(t, rec.SearchPageSizes, 1)
	original := rec.SearchPageSizes[0]
	require.Equal(t, 10, original, "80x24 all-sources derived fetch size")
	require.Equal(t, original, model.search.fetchSize, "the session's sticky size must be stored on the model")

	// Force the buffer to look refill-worthy regardless of the canned set's
	// actual size (1 match for "sky").
	model.search.buffer = make([]ModItem, original)
	model.search.page.Results = model.search.buffer
	model.search.providerExhausted = false

	// Resize mid-session: the ALREADY-STARTED session must not pick this up.
	resized, _ := model.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	model = resized.(Model)
	newSize := model.searchFetchSize()
	require.NotEqual(t, original, newSize, "the test needs a resize that actually changes the derived size")

	model = updateWithKeyType(t, model, tea.KeyEsc) // n is a screen-level key; only reaches it once blurred
	updated, cmd = model.Update(keyRunes("n"))
	require.NotNil(t, cmd, "scrolling into refill range issues a search command")
	model = updated.(Model)
	_, _ = model.Update(cmd())

	require.Len(t, rec.SearchPageSizes, 2)
	require.Equal(t, original, rec.SearchPageSizes[1], "the refill must keep the ORIGINAL session's fetch size")

	// A fresh submit after the resize must pick up the NEW size.
	model = updateWithRunes(t, model, "/") // refocus (query text is still "sky")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	model = updated.(Model)
	_, _ = model.Update(cmd())

	require.Len(t, rec.SearchPageSizes, 3)
	require.Equal(t, newSize, rec.SearchPageSizes[2], "a fresh submit after resize must use the NEW size")
}

// TestPrevPageNeverCallsProviderEvenAfterResize guards #111 Tier 3's
// strengthened contract - Copilot PR #114 round 2's finding was that
// PrevPage (page-era) could silently recompute mid-session; under infinite
// scroll that entire class of bug is impossible by construction, since p
// only ever moves the selection backward through what's ALREADY buffered
// and never checks maybeRefillSearch at all (see PrevPage's own handler in
// app.go).
func TestPrevPageNeverCallsProviderEvenAfterResize(t *testing.T) {
	t.Parallel()

	rec := &recordingProvider{delegate: NewPrototypeProvider()}
	model, err := NewModel(Options{Theme: "wizardry", Provider: rec})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	sized, _ := loaded.(Model).Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = sized.(Model)

	model = updateWithRunes(t, model, "3")
	for _, r := range "sky" {
		model = updateWithRunes(t, model, string(r))
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	result, _ := model.Update(cmd())
	model = result.(Model)
	require.Equal(t, searchReady, model.search.state)
	require.Len(t, rec.SearchPageSizes, 1)

	model.search.buffer = make([]ModItem, 20)
	model.search.page.Results = model.search.buffer
	model.selected[ScreenSearch] = 15

	resized, _ := model.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	model = resized.(Model)

	model = updateWithKeyType(t, model, tea.KeyEsc) // p is a screen-level key; only reaches it once blurred
	updated, cmd = model.Update(keyRunes("p"))
	require.Nil(t, cmd, "p must never dispatch a provider call")
	model = updated.(Model)

	require.Less(t, model.selected[ScreenSearch], 15, "p still moves the selection backward")
	require.Len(t, rec.SearchPageSizes, 1, "no additional provider call after p")
}

// TestSearchFetchSizeStickyAcrossResizeThenPostInstallRefresh guards the
// other half of Copilot PR #114 round 2's finding under #111 Tier 3's
// rebuild-the-buffer refresh mechanics: refreshSearchAfterInstall refetches
// the session's already-covered rounds, and must keep the session's
// ORIGINAL fetch size rather than recomputing after a resize.
func TestSearchFetchSizeStickyAcrossResizeThenPostInstallRefresh(t *testing.T) {
	t.Parallel()

	rec := &recordingProvider{delegate: NewPrototypeProvider()}
	model, err := NewModel(Options{Theme: "wizardry", Provider: rec})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	sized, _ := loaded.(Model).Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = sized.(Model)

	model = updateWithRunes(t, model, "3")
	for _, r := range "sky" {
		model = updateWithRunes(t, model, string(r))
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)
	result, _ := model.Update(cmd())
	model = result.(Model)
	require.Equal(t, searchReady, model.search.state)
	require.Len(t, rec.SearchPageSizes, 1)
	original := rec.SearchPageSizes[0]
	require.Equal(t, 10, original, "80x24 all-sources derived fetch size")

	resized, _ := model.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	model = resized.(Model)
	newSize := model.searchFetchSize()
	require.NotEqual(t, original, newSize, "the test needs a resize that actually changes the derived size")

	model, cmd = model.refreshSearchAfterInstall()
	require.NotNil(t, cmd, "refreshSearchAfterInstall must dispatch a rebuild from searchReady")
	_, _ = model.Update(cmd())

	require.Len(t, rec.SearchPageSizes, 2)
	require.Equal(t, original, rec.SearchPageSizes[1],
		"the post-install refresh must keep the ORIGINAL session's fetch size, not recompute after a resize")
}

// --- #111 Tier 3: infinite scroll (design supersedes buffered display
// pagination - see task-1-report.md's "Fix round 3" for the root cause: an
// aggregate DISPLAY page used to equal one fetch round, but a round's union
// size varies with how many sources still have data left, so no per-page
// arithmetic could describe it honestly) ---

// aggregateRoundProvider fakes an all-sources aggregate whose per-round
// union reproduces the user's exact PR #114 scenario, merging the way
// core.Service.SearchAllSources really merges: page N requests page N from
// EACH source independently and concatenates (see that function's own doc
// comment) - deep has enough rows to span multiple rounds, shallow has only
// a few, so round 0's union is bigger than one fetchSize-sized chunk and
// later rounds shrink once shallow runs dry.
type aggregateRoundProvider struct {
	stubProvider
	deep, shallow int
	// calls records every Search call's pageSize, in order - every call in
	// these tests happens synchronously (the returned tea.Cmd is invoked
	// inline), never from a real goroutine, so a plain slice is race-safe.
	calls []int
}

func (p *aggregateRoundProvider) Sources() []string { return []string{"deep", "shallow"} }

func (p *aggregateRoundProvider) Search(_ context.Context, source, query string, page, pageSize int) (SearchPage, error) {
	p.calls = append(p.calls, pageSize)
	slice := func(total int) ([]ModItem, bool) {
		start := min(page*pageSize, total)
		end := min(start+pageSize, total)
		items := make([]ModItem, 0, end-start)
		for i := start; i < end; i++ {
			items = append(items, ModItem{ID: fmt.Sprintf("m%d-%d", total, i), Name: fmt.Sprintf("Mod %d", i)})
		}
		return items, (page+1)*pageSize < total
	}
	deepItems, deepMore := slice(p.deep)
	shallowItems, shallowMore := slice(p.shallow)
	return SearchPage{
		Results: append(deepItems, shallowItems...),
		Query:   query, Source: source, Page: page, PageSize: pageSize,
		TotalCount: p.deep + p.shallow, Exhausted: !deepMore && !shallowMore, AttemptedCount: 2,
	}, nil
}

// aggregateRoundScreenModel builds a Model over provider, sized 80x24 (the
// all-sources fetchSize there is 10 - see TestSearchFetchSizeDerivesFromPaneBudget)
// and focused on the search screen - the shared starting point every
// anchor test below needs.
func aggregateRoundScreenModel(t *testing.T, provider DataProvider) Model {
	t.Helper()
	model, err := NewModel(Options{Theme: "wizardry", Provider: provider})
	require.NoError(t, err)
	loaded, _ := model.Update(model.Init()())
	sized, _ := loaded.(Model).Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updateWithRunes(t, sized.(Model), "3")
}

// submitSearch drives a fresh submit through the real Update loop (typing
// query, then Enter while focused, then running the resulting Cmd) and
// returns the resulting Model.
func submitSearch(t *testing.T, model Model, query string) Model {
	t.Helper()
	for _, r := range query {
		model = updateWithRunes(t, model, string(r))
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	require.NotNil(t, cmd)
	result, _ := model.Update(cmd())
	return result.(Model)
}

// TestInfiniteScrollBuffersAcrossRoundsAndRefillsOnLowWater is the anchor
// test for #111 Tier 3: the user's exact PR #114 scenario (deep=2*B+3
// rows, shallow=3 rows, B=10) - j/k-style scrolling (via n's accelerator)
// walks the ENTIRE merged buffer with no page boundary anywhere, refilling
// exactly once per low-water crossing, and stopping dead once the provider
// says there's nothing left, no matter how far the user keeps scrolling.
func TestInfiniteScrollBuffersAcrossRoundsAndRefillsOnLowWater(t *testing.T) {
	t.Parallel()

	provider := &aggregateRoundProvider{deep: 23, shallow: 3}
	model := aggregateRoundScreenModel(t, provider)
	model = submitSearch(t, model, "x")
	require.Equal(t, searchReady, model.search.state)
	require.Len(t, provider.calls, 1, "round 0 (the submit)")
	require.Len(t, model.search.buffer, 13, "round 0's union: 10 deep + 3 shallow")
	require.False(t, model.search.providerExhausted)

	model = updateWithKeyType(t, model, tea.KeyEsc) // unfocus so n reaches the outer switch

	// n jumps the selection a paneful (10) - within one fetchSize of the
	// 13-row buffer's end, so it must refill exactly once.
	updated, cmd := model.Update(keyRunes("n"))
	require.NotNil(t, cmd, "scrolling within one fetchSize of the buffer's end must refill")
	model = updated.(Model)
	result, _ := model.Update(cmd())
	model = result.(Model)
	require.Len(t, provider.calls, 2, "round 1")
	require.Len(t, model.search.buffer, 23, "13 + 10 more deep rows; shallow already exhausted")
	require.False(t, model.search.providerExhausted, "deep still has 3 rows left")

	updated, cmd = model.Update(keyRunes("n"))
	require.NotNil(t, cmd, "still within refill range of the now-23-row buffer")
	model = updated.(Model)
	result, _ = model.Update(cmd())
	model = result.(Model)
	require.Len(t, provider.calls, 3, "round 2 - the last one")
	require.Len(t, model.search.buffer, 26, "23 + 3 remaining deep rows")
	require.True(t, model.search.providerExhausted)
	require.Equal(t, "all 26 shown", model.searchFooterLine())

	// Exhaustion must stop further calls no matter how far the user scrolls.
	updated, cmd = model.Update(keyRunes("n"))
	require.Nil(t, cmd, "exhausted - no more provider calls, ever")
	model = updated.(Model)
	require.Len(t, provider.calls, 3, "still exactly 3 calls")

	// p walks back through the buffer WITHOUT ever calling the provider.
	_, cmd = model.Update(keyRunes("p"))
	require.Nil(t, cmd, "p never dispatches a provider call")
	require.Len(t, provider.calls, 3)
}

// TestRefillDedupesConcurrentTriggers guards #111 Tier 3's duplicate-
// trigger guard: a SECOND low-water trigger before the first refill's Cmd
// has even run must not dispatch a second provider call, and the footer
// must show the transient "· fetching…" suffix while one is in flight.
func TestRefillDedupesConcurrentTriggers(t *testing.T) {
	t.Parallel()

	provider := &aggregateRoundProvider{deep: 23, shallow: 3}
	model := aggregateRoundScreenModel(t, provider)
	model = submitSearch(t, model, "x")
	model = updateWithKeyType(t, model, tea.KeyEsc)

	updated, cmd1 := model.Update(keyRunes("n")) // dispatches (builds) round 1's cmd
	require.NotNil(t, cmd1)
	model = updated.(Model)
	require.True(t, model.search.refilling)
	require.Contains(t, model.searchFooterLine(), "· fetching…")
	require.Len(t, provider.calls, 1, "cmd1 is built but not yet RUN - only the submit has actually called Search")

	updated, cmd2 := model.Update(keyRunes("n"))
	require.Nil(t, cmd2, "a refill already in flight must suppress a second dispatch")
	model = updated.(Model)
	require.Len(t, provider.calls, 1, "the suppressed trigger issued no call")

	result, _ := model.Update(cmd1())
	model = result.(Model)
	require.Len(t, provider.calls, 2, "cmd1 finally ran")
	require.False(t, model.search.refilling)
	require.NotContains(t, model.searchFooterLine(), "fetching")
}

// TestSearchFooterSingleSourceForms guards the single-source footer forms
// (#111 Tier 3): a reported TotalCount always renders "X of Y loaded";
// without one, single-source falls back to the same loaded/more-available/
// all-shown forms aggregate mode uses.
func TestSearchFooterSingleSourceForms(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.state = searchReady
	model.search.buffer = make([]ModItem, 4)
	model.search.page = SearchPage{Query: "q", Source: "nexusmods", TotalCount: 47, Results: model.search.buffer}
	require.Equal(t, "4 of 47 loaded", model.searchFooterLine())

	model.search.page.TotalCount = 0
	model.search.providerExhausted = false
	require.Equal(t, "4 loaded · more available", model.searchFooterLine())

	model.search.providerExhausted = true
	require.Equal(t, "all 4 shown", model.searchFooterLine())
}

// TestStaleGenRefillResultDropped guards #111 Tier 3's staleness contract
// for the new refill kind specifically: a refill dispatched for an OLD
// session (before a new submit bumped gen) must be discarded on arrival,
// exactly like a stale submit/refresh result already was - mirrors
// TestStaleSearchResultsAreDiscarded's pattern.
func TestStaleGenRefillResultDropped(t *testing.T) {
	t.Parallel()

	model := searchScreenModel(t)
	model.search.gen = 5
	model.search.buffer = []ModItem{{ID: "keep"}}
	model.search.page.Results = model.search.buffer

	updated, _ := model.Update(searchResultMsg{
		gen: 4, kind: searchResultRefill,
		page: SearchPage{Results: []ModItem{{ID: "stale"}}},
	})
	m := updated.(Model)
	require.Equal(t, []ModItem{{ID: "keep"}}, m.search.buffer, "a stale-gen refill must not touch the current session's buffer")
}

// --- #111 Tier 3 fix round 4: non-destructive refresh/refill failures ---

// roundErrorProvider succeeds through round errorOnRound-1 and fails
// starting at errorOnRound - lets a test put a REAL provider error partway
// through a multi-round refill/refresh sequence. calls records every
// requested round (the page argument), in order. A single canned source of
// `total` deterministic rows, sliced by page/pageSize like the real
// providers do.
type roundErrorProvider struct {
	stubProvider
	total        int
	errorOnRound int
	calls        []int
}

func (p *roundErrorProvider) Sources() []string { return []string{"stub"} }

func (p *roundErrorProvider) Search(_ context.Context, source, query string, page, pageSize int) (SearchPage, error) {
	p.calls = append(p.calls, page)
	if page >= p.errorOnRound {
		return SearchPage{}, errors.New("connection reset")
	}
	start := min(page*pageSize, p.total)
	end := min(start+pageSize, p.total)
	items := make([]ModItem, 0, end-start)
	for i := start; i < end; i++ {
		items = append(items, ModItem{ID: fmt.Sprintf("m%d", i), Name: fmt.Sprintf("Mod %d", i)})
	}
	return SearchPage{
		Results: items, Query: query, Source: source, Page: page, PageSize: pageSize,
		TotalCount: p.total, Exhausted: (page+1)*pageSize >= p.total,
	}, nil
}

// TestRefreshFailureMidRebuildPreservesBuffer guards the task-reviewer
// finding: refreshSearchAfterInstall used to reset the buffer (via
// beginNewSession) BEFORE its rebuild Cmd even ran, so a mid-loop error
// discarded an arbitrarily deep scrolled buffer over what might be a
// transient hiccup - contradicting the refill path's own established
// non-destructive principle. A failed refresh must leave the PRE-refresh
// buffer and searchReady state exactly as they were, surfacing only a
// muted status notice.
func TestRefreshFailureMidRebuildPreservesBuffer(t *testing.T) {
	t.Parallel()

	provider := &roundErrorProvider{total: 100, errorOnRound: 999} // no errors yet
	model := aggregateRoundScreenModel(t, provider)
	model = submitSearch(t, model, "x") // round 0: buffer = 10
	model = updateWithKeyType(t, model, tea.KeyEsc)
	updated, cmd := model.Update(keyRunes("n")) // triggers a refill: round 1
	require.NotNil(t, cmd)
	model = updated.(Model)
	result, _ := model.Update(cmd())
	model = result.(Model)
	require.Len(t, model.search.buffer, 20)
	require.Equal(t, 2, model.search.fetchRound)
	preBuffer := append([]ModItem(nil), model.search.buffer...)

	// A post-install refresh must refetch rounds 0 and 1 - fail it on round
	// 1, the SECOND of the two.
	provider.errorOnRound = 1
	provider.calls = nil
	model, cmd = model.refreshSearchAfterInstall()
	require.NotNil(t, cmd)
	updated, _ = model.Update(cmd())
	model = updated.(Model)

	require.Equal(t, []int{0, 1}, provider.calls, "round 0 succeeded, round 1 failed mid-rebuild")
	require.Equal(t, searchReady, model.search.state, "a refresh failure must not blank the screen")
	require.Equal(t, preBuffer, model.search.buffer, "the PRE-refresh buffer must survive a mid-rebuild error")
	require.NotEmpty(t, model.action.status, "a muted notice must surface")
	require.False(t, model.action.statusIsError, "the notice is muted, not a hard error")
}

// TestRefillFailureDoesNotPermanentlyExhaustAndRetries guards the task-
// reviewer finding: a refill ERROR used to unconditionally set
// providerExhausted, permanently claiming "all N shown" with no way to
// retry even though the provider never actually said there was nothing
// left - it was just one failed attempt. A refill failure must clear
// refilling and surface a transient muted notice WITHOUT setting
// providerExhausted, so the next low-water-triggering movement naturally
// retries the SAME round (fetchRound is never advanced on failure).
func TestRefillFailureDoesNotPermanentlyExhaustAndRetries(t *testing.T) {
	t.Parallel()

	provider := &roundErrorProvider{total: 100, errorOnRound: 1} // round 0 ok, round 1 (the refill) fails
	model := aggregateRoundScreenModel(t, provider)
	model = submitSearch(t, model, "x")
	require.Len(t, provider.calls, 1)

	model = updateWithKeyType(t, model, tea.KeyEsc)
	updated, cmd := model.Update(keyRunes("n")) // triggers the failing refill (round 1)
	require.NotNil(t, cmd)
	model = updated.(Model)
	result, _ := model.Update(cmd())
	model = result.(Model)

	require.Len(t, provider.calls, 2, "the failed attempt")
	require.False(t, model.search.providerExhausted, "a failed ATTEMPT is not the provider saying there's nothing left")
	require.False(t, model.search.refilling)
	require.Contains(t, model.searchFooterLine(), "more available", "footer must not falsely claim everything is loaded")
	require.NotEmpty(t, model.action.status)
	require.False(t, model.action.statusIsError, "the notice is muted, not a hard error")

	// A FURTHER movement must retry the SAME round - prove it actually
	// succeeds once the transient error clears.
	provider.errorOnRound = 999
	updated, cmd = model.Update(keyRunes("n"))
	require.NotNil(t, cmd, "a further movement must retry the refill")
	model = updated.(Model)
	result, _ = model.Update(cmd())
	model = result.(Model)

	require.Equal(t, []int{0, 1, 1}, provider.calls, "the retry re-requested round 1, not round 2")
	require.Len(t, model.search.buffer, 20, "the retry's rows were appended")
}

// --- #111 Tier 3 fix round 5 (task re-review): refill/refresh mutual exclusion ---

// TestScrollDuringInFlightRefreshDoesNotRefill guards the task re-review's
// reproduced race: beginRefresh deliberately keeps state searchReady (the
// buffer stays live on screen during a refresh, unlike a genuine new
// submit), so a scroll-triggered refill could dispatch WHILE a refresh is
// in flight - and since refillSearch never bumps gen, the two share one
// generation, so staleness alone can't tell them apart. If the refill
// resolved first (appending rows, advancing fetchRound) and the refresh's
// wholesale replace=true result landed after, the buffer would silently
// regress - the same silent-data-loss class fix round 4 closed for
// refill-vs-refill, reopened here via a different pairing. The fix: a
// refresh in flight (searchModel.refreshing) blocks maybeRefillSearch
// exactly like a refill in flight already does.
func TestScrollDuringInFlightRefreshDoesNotRefill(t *testing.T) {
	t.Parallel()

	provider := &aggregateRoundProvider{deep: 23, shallow: 3}
	model := aggregateRoundScreenModel(t, provider)
	model = submitSearch(t, model, "x") // round 0: buffer = 13, fetchRound = 1
	require.Len(t, model.search.buffer, 13)

	// Dispatch a refresh, but don't run its Cmd yet - it's "in flight".
	model, refreshCmd := model.refreshSearchAfterInstall()
	require.NotNil(t, refreshCmd)
	require.True(t, model.search.refreshing)

	// A scroll while that refresh is in flight must NOT dispatch a refill.
	model = updateWithKeyType(t, model, tea.KeyEsc)
	updated, refillCmd := model.Update(keyRunes("n"))
	require.Nil(t, refillCmd, "scrolling during an in-flight refresh must issue no provider call")
	model = updated.(Model)
	require.False(t, model.search.refilling, "no refill was ever dispatched, so it must not look like one is")

	// Once the refresh completes, a further movement refills normally again.
	updated, _ = model.Update(refreshCmd())
	model = updated.(Model)
	require.False(t, model.search.refreshing)
	require.Len(t, model.search.buffer, 13, "the refresh rebuilt round 0 - same 13 rows")

	updated, refillCmd = model.Update(keyRunes("n"))
	require.NotNil(t, refillCmd, "a movement after the refresh completes must refill normally")
	model = updated.(Model)
	require.True(t, model.search.refilling)
}

// TestRefreshSupersedesInFlightRefillWithoutStuckFlag pins the INVERSE
// pairing the task re-review asked to trace: a refresh starting while a
// refill is already in flight. The data-loss half is already safe by
// construction - refillSearch never bumps gen, so beginRefresh's gen bump
// guarantees the stale refill's eventual result is dropped by the ordinary
// staleness check before it ever touches the buffer (pinned below). But
// WITHOUT beginRefresh also clearing refilling, that drop would leave the
// flag stuck true forever (the discarded message returns before reaching
// the code that would otherwise clear it) - permanently blocking every
// FUTURE refill until the next full submit. beginRefresh clears it
// proactively for exactly this reason.
func TestRefreshSupersedesInFlightRefillWithoutStuckFlag(t *testing.T) {
	t.Parallel()

	provider := &aggregateRoundProvider{deep: 23, shallow: 3}
	model := aggregateRoundScreenModel(t, provider)
	model = submitSearch(t, model, "x")
	require.Len(t, model.search.buffer, 13)
	genAfterSubmit := model.search.gen

	model = updateWithKeyType(t, model, tea.KeyEsc)
	updated, refillCmd := model.Update(keyRunes("n")) // dispatches a refill; NOT run yet
	require.NotNil(t, refillCmd)
	model = updated.(Model)
	require.True(t, model.search.refilling)
	require.Equal(t, genAfterSubmit, model.search.gen, "refillSearch itself never bumps gen")

	// A refresh starts while that refill is still in flight.
	model, refreshCmd := model.refreshSearchAfterInstall()
	require.NotNil(t, refreshCmd)
	require.NotEqual(t, genAfterSubmit, model.search.gen, "beginRefresh bumps gen")
	require.False(t, model.search.refilling,
		"starting a refresh must not leave a superseded refill's flag stuck true forever")

	// The STALE refill's result, now carrying the OLD generation, must be
	// dropped on arrival rather than silently applied.
	preRefreshBuffer := append([]ModItem(nil), model.search.buffer...)
	updated, _ = model.Update(refillCmd())
	model = updated.(Model)
	require.Equal(t, preRefreshBuffer, model.search.buffer, "the stale refill result must be dropped, not applied")
	require.Equal(t, 1, model.search.fetchRound, "the dropped stale refill must not advance fetchRound either")

	// The refresh itself still completes normally.
	updated, _ = model.Update(refreshCmd())
	model = updated.(Model)
	require.Equal(t, searchReady, model.search.state)
	require.Len(t, model.search.buffer, 13, "the refresh rebuilds exactly the round it knew about (round 0)")
	require.False(t, model.search.refreshing)
}
