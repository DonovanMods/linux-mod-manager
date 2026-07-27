package tui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// searchState tracks where the search sub-model is in its async query
// lifecycle.
type searchState int

const (
	searchIdle searchState = iota
	searchLoading
	searchReady
	searchFailed
	searchAuthRequired
)

// searchModel is the Archive Search screen's sub-model: a focusable query
// input, the currently selected source, and the state of the most recent
// (or in-flight) search.
type searchModel struct {
	input      textinput.Model
	sources    []string
	sourceIdx  int
	state      searchState
	page       SearchPage
	err        error
	authSource string
	gen        int
	cancel     context.CancelFunc
	// fetchSize is the current query session's sticky page size (#111 Tier
	// 1): the SESSION owns this value, not the page number - startSearch
	// (the Enter/submit path, and ONLY that path) computes it fresh from
	// Model.searchFetchSize() when a NEW session begins, then requerySearch
	// reuses this stored value unchanged for every later fetch of that same
	// session (n/p pagination, refreshSearchAfterInstall's requery),
	// including a PrevPage back to page 0 - even if the terminal is resized
	// in between. page == 0 is deliberately NOT the recompute signal: it
	// recurs mid-session (PrevPage from page 1, or refreshSearchAfterInstall
	// re-fetching whichever page is on screen), so keying recompute off it
	// silently changed an in-flight session's fetch size on those paths
	// (Copilot PR #114 round 2 finding) - exactly the pagination-arithmetic
	// inconsistency stickiness exists to prevent (an earlier page fetched at
	// the old size, page 0 refetched at a new one, boundaries shift/overlap).
	// Zero (its natural zero value, before any search has ever run) is not a
	// special case: DataProvider.Search implementations treat pageSize <= 0
	// as "use SearchPageSize" (see that constant's doc comment), so a stray
	// fetch before the first submit degrades to the old fixed-size behavior
	// rather than fetching nothing.
	fetchSize int
	// gameName is the active game's display name, threaded in at
	// construction (see newSearchModel) so noSourcesConfiguredErr and the
	// all-sources honesty notice (searchView's zero-results branch) can name
	// the game the same way the CLI's own diagnostics do (#58 item 5's
	// wording-parity fix) instead of a generic "this game".
	gameName string
}

// searchResultMsg carries a completed search page, tagged with the
// generation of the query that produced it so stale results can be
// discarded.
type searchResultMsg struct {
	gen  int
	page SearchPage
}

// searchFailedMsg carries a failed search, tagged with the generation of the
// query that produced it so stale failures can be discarded.
type searchFailedMsg struct {
	gen    int
	err    error
	source string
}

// noSourcesConfiguredErr is surfaced whenever a game has zero real (i.e.
// non-sentinel) configured sources, both proactively at model construction
// (newSearchModel) and defensively if startSearch is somehow reached in that
// state. Wording mirrors the CLI's own noSourcesConfiguredErr (cmd/lmm/
// search.go) verbatim, substituting gameName the same way (#58 item 5's
// wording-parity fix — the two used to drift: "for this game"/"add one"
// here vs. "for %s"/"add sources" there). gameName empty (a test double
// that never threads a real name through newSearchModel) falls back to "this
// game", preserving the pre-parity-fix message for those callers.
func noSourcesConfiguredErr(gameName string) error {
	return fmt.Errorf("no mod sources configured for %s; add sources with 'lmm game add' or edit games.yaml", displayGameName(gameName))
}

// displayGameName renders a game name for user-facing copy, falling back to
// "this game" when the caller never threaded a real name through (a test
// double, or any future Options.GameName-less construction). Shared by
// noSourcesConfiguredErr and searchView's no-searchable-sources notice so
// neither can render a malformed possessive like "None of 's sources..."
// (#58 review follow-up).
func displayGameName(gameName string) string {
	if gameName == "" {
		return "this game"
	}
	return gameName
}

// searchInputPromptAllowance reserves room for the query input's "> " prompt
// plus its trailing cursor cell, so searchInputWidthFor's value-viewport
// width keeps prompt+value+cursor inside the panel's content width. Without
// this, a value near the viewport width can overflow by one cell and
// word-wrap inside the width-set search panel instead of h-scrolling.
const searchInputPromptAllowance = 4

// searchInputWidthFor derives the query input's value-viewport width (see
// textinput.Model.Width) from the content width available to the search
// panel and that panel's horizontal frame size (border + padding), so a long
// query scrolls horizontally within the input instead of word-wrapping
// inside the width-set search panel and growing the view past
// availableContentHeight.
func searchInputWidthFor(availableWidth, panelHorizontalFrameSize int) int {
	inner := availableWidth - panelHorizontalFrameSize
	return max(inner-searchInputPromptAllowance, 10)
}

// newSearchModel builds the search sub-model, seeding its source list from
// the DataProvider with the all-sources sentinel ("") prepended, so index 0
// — the default sourceIdx — targets "search every configured source" rather
// than an arbitrary real one. The input's Width defaults from
// defaultContentWidth (the same zero-size fallback availableWidth uses) so
// the input stays bounded even in tests that never send a
// tea.WindowSizeMsg; Update's tea.WindowSizeMsg case recomputes it once real
// terminal dimensions arrive.
//
// When the provider has zero real sources, the sentinel is meaningless (there
// is nothing to search), so the model starts in searchFailed with the
// configured-sources diagnostic rather than silently offering a dead "All
// sources" default (CARRIED REVIEW NOTE from issue #54 hardening).
//
// gameName is stored on the returned model (searchModel.gameName) for
// noSourcesConfiguredErr and the all-sources honesty notice to name the
// active game, mirroring the CLI's own diagnostics (#58 item 5).
func newSearchModel(provider DataProvider, panelHorizontalFrameSize int, gameName string) searchModel {
	input := textinput.New()
	input.Placeholder = "search the archives"
	input.CharLimit = 120
	input.Width = searchInputWidthFor(defaultContentWidth, panelHorizontalFrameSize)

	realSources := provider.Sources()
	s := searchModel{input: input, sources: append([]string{""}, realSources...), gameName: gameName}
	if len(realSources) == 0 {
		s.state = searchFailed
		s.err = noSourcesConfiguredErr(gameName)
	}
	return s
}

// source returns the currently selected source ID: "" is the all-sources
// sentinel, meaning "search every configured source". Also "" when the
// sources list itself is empty/unset (defensive: see startSearch's guard for
// the real "no sources configured" case).
func (s searchModel) source() string {
	if len(s.sources) == 0 {
		return ""
	}
	return s.sources[s.sourceIdx]
}

// sourceLabel renders a source ID for display: the all-sources sentinel ""
// becomes "All sources"; any real source ID renders as itself.
func sourceLabel(source string) string {
	if source == "" {
		return "All sources"
	}
	return source
}

// hasNextPage reports whether another page of results is available for the
// current search, mirroring the CLI picker's logic (see install.go).
//
// All-sources pages (Source == "") are a special case (#58 item 1):
// TotalCount there is core.AggregateSearchResult.TotalCount, SUMMED across
// sources that each paginate independently, so it cannot bound a single
// merged page the way a single source's TotalCount can - 3 sources whose
// entire 10-mod catalog fits on page 0 sum to a TotalCount of 30, which
// against a PageSize of 10 falsely implies a page 2 exists. Exhausted (set
// by coreProvider.Search from the aggregate result) is the only signal that
// actually accounts for every contributing source, so it - not the
// TotalCount math below - decides for aggregate pages.
func (s searchModel) hasNextPage() bool {
	if s.page.Source == "" {
		return !s.page.Exhausted
	}
	if s.page.TotalCount > 0 {
		return (s.page.Page+1)*s.page.PageSize < s.page.TotalCount
	}
	return len(s.page.Results) == s.page.PageSize // full page ⇒ maybe more
}

// startSearch begins a NEW query session (the Enter/submit path - "3"/"/"
// then Enter, the dashboard's Search Archives entry - and ONLY that path):
// it derives THIS session's sticky fetch size from the window's CURRENT
// size (see searchModel.fetchSize's doc comment) and dispatches page 0.
// Every other fetch of this same session must go through requerySearch
// below instead, which does NOT touch fetchSize - see its own doc comment
// for why page == 0 alone is not a safe "new session" signal.
func (m Model) startSearch(query string) (Model, tea.Cmd) {
	m.search.fetchSize = m.searchFetchSize()
	return m.dispatchSearch(query, 0)
}

// requerySearch continues the CURRENT query session at page - n/p pagination
// (app.go's NextPage/PrevPage handlers) and refreshSearchAfterInstall's
// post-install requery (mutations.go), both of which can legitimately land
// back on page 0 (PrevPage from page 1; a refresh of whatever page is
// currently on screen) without that being a new session. It reuses
// m.search.fetchSize exactly as startSearch last set it, so a resize
// between the original submit and this call never shifts the session's
// pagination arithmetic.
func (m Model) requerySearch(query string, page int) (Model, tea.Cmd) {
	return m.dispatchSearch(query, page)
}

// dispatchSearch is startSearch/requerySearch's shared body: cancels any
// in-flight search, bumps the generation, and issues the fetch at page
// using whatever fetch size is already stored in m.search.fetchSize -
// deriving that value is exclusively startSearch's job (see its doc
// comment), never this method's.
func (m Model) dispatchSearch(query string, page int) (Model, tea.Cmd) {
	// Guard: no REAL sources configured for this game. The "" sentinel is
	// now a valid search target (meaning "search every configured source"),
	// so this can no longer key off source() == "": sources always contains
	// at least the sentinel once newSearchModel has run. The actual invalid
	// case is zero real sources, i.e. the sentinel-only (or empty) list.
	if len(m.search.sources) <= 1 {
		m.search.state = searchFailed
		m.search.err = noSourcesConfiguredErr(m.search.gameName)
		return m, nil
	}

	if m.search.cancel != nil {
		m.search.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.search.cancel = cancel
	m.search.gen++
	m.search.state = searchLoading

	fetchSize := m.search.fetchSize

	gen := m.search.gen
	provider := m.provider
	source := m.search.source()
	return m, func() tea.Msg {
		result, err := provider.Search(ctx, source, query, page, fetchSize)
		if err != nil {
			return searchFailedMsg{gen: gen, err: err, source: source}
		}
		return searchResultMsg{gen: gen, page: result}
	}
}
