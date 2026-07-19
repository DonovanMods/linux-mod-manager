package tui

import (
	"context"
	"errors"

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

// errNoSourcesConfigured is surfaced whenever a game has zero real (i.e.
// non-sentinel) configured sources, both proactively at model construction
// (newSearchModel) and defensively if startSearch is somehow reached in that
// state. It mirrors the CLI's noSourcesConfiguredErr diagnostic.
var errNoSourcesConfigured = errors.New("no mod sources configured for this game; add one with 'lmm game add' or edit games.yaml")

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
func newSearchModel(provider DataProvider, panelHorizontalFrameSize int) searchModel {
	input := textinput.New()
	input.Placeholder = "search the archives"
	input.CharLimit = 120
	input.Width = searchInputWidthFor(defaultContentWidth, panelHorizontalFrameSize)

	realSources := provider.Sources()
	s := searchModel{input: input, sources: append([]string{""}, realSources...)}
	if len(realSources) == 0 {
		s.state = searchFailed
		s.err = errNoSourcesConfigured
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
func (s searchModel) hasNextPage() bool {
	if s.page.TotalCount > 0 {
		return (s.page.Page+1)*s.page.PageSize < s.page.TotalCount
	}
	return len(s.page.Results) == s.page.PageSize // full page ⇒ maybe more
}

// startSearch cancels any in-flight search, bumps the generation, and returns
// the model plus a command executing the new query.
func (m Model) startSearch(query string, page int) (Model, tea.Cmd) {
	// Guard: no REAL sources configured for this game. The "" sentinel is
	// now a valid search target (meaning "search every configured source"),
	// so this can no longer key off source() == "": sources always contains
	// at least the sentinel once newSearchModel has run. The actual invalid
	// case is zero real sources, i.e. the sentinel-only (or empty) list.
	if len(m.search.sources) <= 1 {
		m.search.state = searchFailed
		m.search.err = errNoSourcesConfigured
		return m, nil
	}

	if m.search.cancel != nil {
		m.search.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.search.cancel = cancel
	m.search.gen++
	m.search.state = searchLoading

	gen := m.search.gen
	provider := m.provider
	source := m.search.source()
	return m, func() tea.Msg {
		result, err := provider.Search(ctx, source, query, page)
		if err != nil {
			return searchFailedMsg{gen: gen, err: err, source: source}
		}
		return searchResultMsg{gen: gen, page: result}
	}
}
