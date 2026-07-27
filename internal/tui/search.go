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
	input     textinput.Model
	sources   []string
	sourceIdx int
	state     searchState
	// page is the RENDERED view of the current session: page.Results is
	// always the full buffer (#111 Tier 3's infinite scroll - see buffer's
	// own doc comment), and page.{Query,Source,TotalCount,AttemptedCount,
	// Warnings,Exhausted} carry the session-level facts every consumer
	// (searchHeaderLines, searchWarningLine, searchView's zero-results
	// notice, searchFooterLine) already reads - refreshed from each
	// completed round by applyRoundResult, which is the ONLY place that
	// writes this field once a session is under way.
	page       SearchPage
	err        error
	authSource string
	gen        int
	cancel     context.CancelFunc
	// buffer accumulates every ModItem fetched across every provider round
	// of the CURRENT session, in arrival order (#111 Tier 3: infinite
	// scroll replaced display pagination - see task-1-report.md's "Fix
	// round 3" for why: an aggregate DISPLAY page used to equal one fetch
	// round, but a round's union size varies with how many sources still
	// have data left, so no fixed per-page arithmetic could describe it
	// honestly). page.Results is always this buffer in full - j/k (and the
	// n/p accelerators) scroll through it via the existing windowedRows
	// machinery (clamp.go), exactly like every other long list in the TUI.
	buffer []ModItem
	// fetchRound is the NEXT provider round to request (0-based) - startSearch
	// resets it to 0 before dispatching round 0; refillSearch requests
	// fetchRound and, on a NON-EMPTY result, advances it.
	fetchRound int
	// providerExhausted reports whether the provider has said there is
	// nothing left to fetch for this session (see roundExhausted) -
	// maybeRefillSearch's gate against dispatching a refill that could
	// never return anything, and searchFooterLine's "all N shown" vs.
	// "more available" wording.
	providerExhausted bool
	// refilling is true from the moment refillSearch dispatches a round
	// until that round's result (or failure) lands - #111 Tier 3's
	// duplicate-trigger guard: maybeRefillSearch refuses to dispatch a
	// SECOND round while one is already in flight (two Down presses before
	// the first refill resolves must not issue two overlapping requests),
	// and searchFooterLine appends a transient "· fetching…" suffix while
	// it's true.
	refilling bool
	// warnings accumulates every UNIQUE per-source failure text across
	// every round of the session (#111 Tier 3), deduped by exact string
	// match (see mergeWarnings) - a source that failed on round 0 keeps
	// its warning visible even after scrolling well past round 0's rows,
	// instead of the warning line silently vanishing the moment a later
	// round's (warning-free) response overwrote it.
	warnings []string
	// fetchSize is the current query session's sticky per-round quantum
	// (#111 Tier 1/3): startSearch (the Enter/submit path, and ONLY that
	// path) derives it fresh from Model.searchFetchSize() when a NEW
	// session begins, then every later round of that session - refills
	// triggered by scrolling, refreshSearchAfterInstall's rebuild - reuses
	// this stored value unchanged, even across an intervening resize.
	// Serves double duty under infinite scroll: it's both the pageSize
	// argument every provider.Search call for this session uses AND the
	// "one paneful" distance maybeRefillSearch's low-water check and the
	// n/p accelerators (afterSearchSelectionMove) jump by - the SAME
	// worst-case-chrome derivation that used to size one display page now
	// sizes one scroll increment, which is exactly why a low-water
	// trigger set to "one fetchSize before the buffer's end" reliably
	// refills before the user can ever scroll past loaded data. Zero (its
	// natural zero value, before any search has ever run) is not a special
	// case: DataProvider.Search implementations treat pageSize <= 0 as "use
	// SearchPageSize" (see that constant's doc comment), so a stray fetch
	// before the first submit degrades to the old fixed-size behavior
	// rather than fetching nothing.
	fetchSize int
	// gameName is the active game's display name, threaded in at
	// construction (see newSearchModel) so noSourcesConfiguredErr and the
	// all-sources honesty notice (searchView's zero-results branch) can name
	// the game the same way the CLI's own diagnostics do (#58 item 5's
	// wording-parity fix) instead of a generic "this game".
	gameName string
}

// searchResultKind distinguishes what a completed searchResultMsg/
// searchFailedMsg should do to the session's buffer (#111 Tier 3):
//   - searchResultSubmit: a NEW session's round 0 (startSearch) - replaces
//     the buffer wholesale, resets the selection to the top, and (on
//     failure) is the only kind that ever routes to searchFailed/
//     searchAuthRequired, since it's the only kind with nothing useful
//     already on screen to preserve.
//   - searchResultRefill: one more round appended because the user scrolled
//     within one fetchSize of the buffer's end (see maybeRefillSearch) -
//     appends to the buffer; a FAILURE here is swallowed (see
//     searchFailedMsg's Update case) rather than blanking an
//     already-useful screen over a transient hiccup on data the user
//     hasn't even scrolled to yet.
//   - searchResultRefresh: refreshSearchAfterInstall's rebuilt buffer
//     (every round 0..fetchRound-1 refetched and merged by its Cmd before
//     this message is even constructed - see that function's doc comment)
//   - replaces the buffer wholesale like submit, but clamps rather than
//     resets the selection.
type searchResultKind int

const (
	searchResultSubmit searchResultKind = iota
	searchResultRefill
	searchResultRefresh
)

// searchResultMsg carries a completed provider round for the current query
// session, tagged with the generation of the query that produced it so
// stale results (a superseded submit, a refill that outlived its session)
// can be discarded. rounds is only meaningful for searchResultRefresh: how
// many rounds page.Results represents, so Update can restore fetchRound
// after beginNewSession's reset (see refreshSearchAfterInstall).
type searchResultMsg struct {
	gen    int
	kind   searchResultKind
	page   SearchPage
	rounds int
}

// searchFailedMsg carries a failed provider round, tagged the same way
// searchResultMsg is (gen for staleness, kind for what Update should do -
// see searchResultKind's doc comment on why a refill failure is handled
// differently from a submit/refresh failure).
type searchFailedMsg struct {
	gen    int
	kind   searchResultKind
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

// roundExhausted reports whether round - one provider.Search response for a
// single fetch round - is the FINAL round available, mirroring the CLI
// picker's per-fetch "maybe more" logic (install.go). Aggregate rounds
// trust the provider's own Exhausted (core.AggregateSearchResult.Exhausted
// already accounts for every contributing source's independent
// cursor - see AggregateSearchResult's doc comment for why a summed
// TotalCount can't be trusted the same way); single-source rounds fall back
// to TotalCount-or-short-page math.
func roundExhausted(round SearchPage) bool {
	if round.Source == "" {
		return round.Exhausted
	}
	if round.TotalCount > 0 {
		return (round.Page+1)*round.PageSize >= round.TotalCount
	}
	return len(round.Results) < round.PageSize
}

// mergeWarnings appends entries from newWarnings not already present in
// existing, preserving existing's order and every entry's FIRST occurrence
// (#111 Tier 3): a session's warnings accumulate across provider rounds - a
// source that failed on round 0 keeps its warning visible even once the
// user has scrolled well past round 0's rows - deduped by exact text. The
// coreProvider wire format ("<sourceID>: <err>") already uniquely
// identifies the source, so an exact-string dedupe is "unique per source"
// in practice without parsing the prefix back out.
func mergeWarnings(existing, newWarnings []string) []string {
	seen := make(map[string]bool, len(existing))
	for _, w := range existing {
		seen[w] = true
	}
	merged := existing
	for _, w := range newWarnings {
		if seen[w] {
			continue
		}
		seen[w] = true
		merged = append(merged, w)
	}
	return merged
}

// applyRoundResult merges one already-fetched round into the session buffer
// and warnings (#111 Tier 3): replace=true resets the buffer to round's
// Results wholesale (submit, refresh); replace=false appends (refill).
// exhausted is the caller's already-computed providerExhausted verdict -
// roundExhausted(round) for a single round, or the LAST looped round's own
// verdict for a multi-round refresh (see refreshSearchAfterInstall) -
// rather than recomputed here, since a refresh's merged round.Results spans
// EVERY refetched round and would give roundExhausted's short-page/
// TotalCount heuristics the wrong Page/Results-length to compare against.
// Returns how many rows round.Results contributed, for callers that only
// advance fetchRound on a non-empty round (see Update's searchResultRefill
// case).
func (s *searchModel) applyRoundResult(round SearchPage, replace bool, exhausted bool) int {
	if replace {
		s.buffer = append([]ModItem(nil), round.Results...)
	} else {
		s.buffer = append(s.buffer, round.Results...)
	}
	s.warnings = mergeWarnings(s.warnings, round.Warnings)
	s.providerExhausted = exhausted
	s.page.Query = round.Query
	s.page.Source = round.Source
	s.page.TotalCount = round.TotalCount
	s.page.AttemptedCount = round.AttemptedCount
	s.page.Warnings = s.warnings
	s.page.Exhausted = s.providerExhausted
	s.page.Results = s.buffer
	return len(round.Results)
}

// beginNewSession cancels any in-flight search, bumps the generation, and
// resets every per-session accumulator (#111 Tier 3's buffer/fetchRound/
// providerExhausted/warnings/refilling) to start fresh - exclusively
// startSearch's (a genuine new query, with nothing on screen worth
// preserving - it flips state to searchLoading, which blanks the results
// pane until round 0 lands). Returns the derived ctx and generation the
// caller's dispatch closure needs.
//
// refreshSearchAfterInstall (mutations.go) does NOT use this - see
// beginRefresh below for why a refresh needs a non-destructive sibling
// instead (#111 Tier 3 fix round 4).
func (m Model) beginNewSession() (Model, context.Context, int) {
	if m.search.cancel != nil {
		m.search.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.search.cancel = cancel
	m.search.gen++
	m.search.state = searchLoading
	m.search.buffer = nil
	m.search.fetchRound = 0
	m.search.providerExhausted = false
	m.search.refilling = false
	m.search.warnings = nil
	return m, ctx, m.search.gen
}

// beginRefresh cancels any in-flight search and bumps the generation
// WITHOUT touching state, buffer, or any other accumulator (#111 Tier 3 fix
// round 4) - refreshSearchAfterInstall's non-destructive sibling to
// beginNewSession. Unlike a genuine new query, a refresh has a
// PERFECTLY GOOD buffer already on screen: the whole point of task review's
// finding is that a mid-rebuild error must leave that buffer (and
// searchReady) exactly as they were, which is only possible if nothing gets
// reset before the rebuild is known to have fully succeeded. The rebuild
// Cmd (refreshSearchAfterInstall) accumulates into its own local buffer and
// only reaches Update via a searchResultRefresh message on success -
// applyRoundResult is what actually swaps it in, and ONLY on that success
// path.
func (m Model) beginRefresh() (Model, context.Context, int) {
	if m.search.cancel != nil {
		m.search.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.search.cancel = cancel
	m.search.gen++
	return m, ctx, m.search.gen
}

// startSearch begins a NEW query session (the Enter/submit path - "3"/"/"
// then Enter, the dashboard's Search Archives entry - and ONLY that path):
// it derives THIS session's sticky fetch size from the window's CURRENT
// size (see searchModel.fetchSize's doc comment), resets every
// accumulator via beginNewSession, and dispatches round 0.
func (m Model) startSearch(query string) (Model, tea.Cmd) {
	// Guard: no REAL sources configured for this game. The "" sentinel is
	// a valid search target (meaning "search every configured source"), so
	// this can no longer key off source() == "": sources always contains
	// at least the sentinel once newSearchModel has run. The actual invalid
	// case is zero real sources, i.e. the sentinel-only (or empty) list.
	if len(m.search.sources) <= 1 {
		m.search.state = searchFailed
		m.search.err = noSourcesConfiguredErr(m.search.gameName)
		return m, nil
	}

	m.search.fetchSize = m.searchFetchSize()
	m, ctx, gen := m.beginNewSession()

	provider := m.provider
	source := m.search.source()
	fetchSize := m.search.fetchSize
	return m, func() tea.Msg {
		result, err := provider.Search(ctx, source, query, 0, fetchSize)
		if err != nil {
			return searchFailedMsg{gen: gen, kind: searchResultSubmit, err: err, source: source}
		}
		return searchResultMsg{gen: gen, kind: searchResultSubmit, page: result}
	}
}

// maybeRefillSearch issues ONE more provider round (#111 Tier 3's infinite
// scroll) when the selection has walked within one fetchSize of the
// buffer's end, the provider hasn't already said it's exhausted, and no
// refill is already in flight for this session (see searchModel.refilling's
// doc comment - two Down presses before the first refill resolves must not
// issue two overlapping requests). Called from afterSearchSelectionMove
// after any selection-moving key on the search screen EXCEPT PrevPage,
// which never checks this at all (see its own handler in app.go) so
// scrolling backward can never trigger a fetch.
func (m Model) maybeRefillSearch() (Model, tea.Cmd) {
	s := m.search
	// fetchSize <= 0 means this session never actually went through
	// startSearch (its natural zero value - see fetchSize's own doc
	// comment), most commonly a test that pokes searchReady/page directly
	// without a real submit. Without this guard, lowWater below evaluates
	// to a bare 0 and every selection at or past index 0 - i.e. always -
	// would look refill-worthy, dispatching a spurious fetch on the very
	// first Down press of an otherwise-static test fixture.
	if s.fetchSize <= 0 || s.providerExhausted || s.refilling {
		return m, nil
	}
	lowWater := len(s.buffer) - s.fetchSize
	if m.selected[ScreenSearch] < lowWater {
		return m, nil
	}
	return m.refillSearch()
}

// afterSearchSelectionMove is the hook every selection-moving key on the
// search screen (Up/Down, and NextPage's accelerator) runs through after
// moving m.selected[ScreenSearch], deciding whether that move needs a
// refill (see maybeRefillSearch). A no-op off the search screen or before a
// session has completed its first round (mirrors every other search
// dispatch's own searchReady guard).
func (m Model) afterSearchSelectionMove() (Model, tea.Cmd) {
	if m.screen != ScreenSearch || m.search.state != searchReady {
		return m, nil
	}
	return m.maybeRefillSearch()
}

// refillSearch dispatches ONE more provider round (#111 Tier 3's infinite
// scroll). Unlike startSearch/refreshSearchAfterInstall, this does NOT
// cancel the in-flight session, bump its generation, or set state to
// searchLoading: the buffer already on screen stays fully interactive and
// visible while the refill runs in the background (searchModel.refilling,
// set here and cleared in Update's searchResultMsg/searchFailedMsg cases,
// is what the footer's transient "· fetching…" suffix and
// maybeRefillSearch's own dedup check key off of instead). It also does not
// derive a per-refill cancellable context - a refill superseded by a brand
// new submit (which DOES cancel/bump gen) simply has its eventual result
// discarded by the ordinary stale-gen check, the same wasted-but-harmless
// tradeoff many other async paths in this package already accept.
func (m Model) refillSearch() (Model, tea.Cmd) {
	m.search.refilling = true
	gen := m.search.gen
	provider := m.provider
	source := m.search.page.Source
	query := m.search.page.Query
	round := m.search.fetchRound
	fetchSize := m.search.fetchSize
	ctx := m.ctx
	return m, func() tea.Msg {
		result, err := provider.Search(ctx, source, query, round, fetchSize)
		if err != nil {
			return searchFailedMsg{gen: gen, kind: searchResultRefill, err: err, source: source}
		}
		return searchResultMsg{gen: gen, kind: searchResultRefill, page: result}
	}
}
