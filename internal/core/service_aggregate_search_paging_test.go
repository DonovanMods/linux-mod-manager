package core_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- #109: `--limit N` must page per-source until N merged hits or every
// source is exhausted, instead of returning whatever one page happened to
// hold. See searchAllSources' doc comment for when the loop engages.

// pagingStubSource is a searchable source with a fixed catalog and a
// SERVER-SIDE page cap: it never returns more than cap mods per page no
// matter how large a pageSize is requested - exactly the shape #109
// describes (NexusMods caps around 30). It records every page it was asked
// for so a test can assert how many round trips the aggregate made.
type pagingStubSource struct {
	id      string
	catalog []domain.Mod
	cap     int
	// reportTotal, when true, answers with the catalog's real size in
	// SearchResult.TotalCount (the precise has-more signal); false leaves it
	// 0 so only the short-page heuristic is available.
	reportTotal bool
	// failOnPage, when >= 0, makes that page (and only that page) return
	// errOnPage instead of results.
	failOnPage int
	// endless, when true, answers every page with a full cap-sized page
	// forever - the misbehaving source the max-pages guard exists for.
	endless bool
	// offsetFromRequestedSize models the shape BOTH built-in sources have
	// (Track C review, finding 1): the rows are clamped to cap, but the
	// upstream offset is still computed from the page size that was
	// REQUESTED, so page 1 of a requested 10 starts at row 10 even though
	// page 0 only returned rows 0-4.
	offsetFromRequestedSize bool
	// hidePageSize models a source that clamps and does not say so:
	// SearchResult.PageSize comes back 0, leaving the short page as the
	// only signal.
	hidePageSize bool

	mu    sync.Mutex
	pages []int // every page index requested, in call order
}

var errPagingStub = errors.New("upstream page failed")

func newPagingStub(id string, count, cap int) *pagingStubSource {
	catalog := make([]domain.Mod, 0, count)
	for i := 1; i <= count; i++ {
		catalog = append(catalog, domain.Mod{
			ID:       fmt.Sprintf("%s-%02d", id, i),
			SourceID: id,
			Name:     fmt.Sprintf("%s mod %02d", id, i),
		})
	}
	return &pagingStubSource{id: id, catalog: catalog, cap: cap, reportTotal: true, failOnPage: -1}
}

func (p *pagingStubSource) requestedPages() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.pages...)
}

func (p *pagingStubSource) ID() string      { return p.id }
func (p *pagingStubSource) Name() string    { return p.id }
func (p *pagingStubSource) AuthURL() string { return "" }
func (p *pagingStubSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}

func (p *pagingStubSource) Search(_ context.Context, q source.SearchQuery) (source.SearchResult, error) {
	p.mu.Lock()
	p.pages = append(p.pages, q.Page)
	p.mu.Unlock()

	if p.failOnPage >= 0 && q.Page == p.failOnPage {
		return source.SearchResult{}, errPagingStub
	}

	size := q.PageSize
	if size <= 0 || size > p.cap {
		size = p.cap // the server-side cap #109 is about
	}
	if p.endless {
		mods := make([]domain.Mod, 0, size)
		for i := range size {
			mods = append(mods, domain.Mod{
				ID:       fmt.Sprintf("%s-p%d-%d", p.id, q.Page, i),
				SourceID: p.id,
				Name:     fmt.Sprintf("%s endless %d", p.id, i),
			})
		}
		return source.SearchResult{Mods: mods, Page: q.Page, PageSize: size}, nil
	}

	stride := size
	if p.offsetFromRequestedSize && q.PageSize > 0 {
		stride = q.PageSize
	}
	start := q.Page * stride
	if start > len(p.catalog) {
		start = len(p.catalog)
	}
	end := min(start+size, len(p.catalog))
	res := source.SearchResult{Mods: p.catalog[start:end], Page: q.Page, PageSize: size}
	if p.hidePageSize {
		res.PageSize = 0
	}
	if p.reportTotal {
		res.TotalCount = len(p.catalog)
	}
	return res, nil
}

func (p *pagingStubSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, nil
}

func (p *pagingStubSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

func (p *pagingStubSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}

func (p *pagingStubSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}

func (p *pagingStubSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// TestSearchAllSourcesPagesUntilLimit is #109's own test sketch: one source
// holding 12, asked for 10 in pages of 5. A single page can only ever yield
// 5, so the aggregate must advance that source's cursor and pull page 1 as
// well. The requested page size is the source's own cap here - a page size
// it cannot honour is a page size it cannot be paged on either, which is
// TestSearchAllSourcesNeverSkipsRowsOfAClampingSource's case.
func TestSearchAllSourcesPagesUntilLimit(t *testing.T) {
	capped := newPagingStub("capped", 12, 5)
	svc, game := newAggregateTestService(t, map[string]string{"capped": ""}, capped)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "mod", "", nil, 0, 5, 10)
	require.NoError(t, err)
	assert.Equal(t, []int{0, 1}, capped.requestedPages(), "a 5/page source must be paged twice to fill a limit of 10")
	assert.Len(t, res.Mods, 10, "two 5-mod pages satisfy the limit exactly")
	assert.False(t, res.Exhausted, "12 in the catalog, 10 pulled: there is still a page 2")
}

// TestSearchAllSourcesNeverSkipsRowsOfAClampingSource is the Track C
// review's finding 1, in core. Both built-in sources compute their upstream
// offset from the page size that was REQUESTED while clamping the rows they
// return, so advancing the cursor after a clamped page asks for rows past
// the ones the clamp left behind - `lmm search --limit 100` against
// CurseForge fetched rows 0-49 and then 100-149, handed the user 100 rows
// presented as the top 100 matches, and rankAggregate reordered them until
// the holes were invisible. A source is now only asked for another page
// when it returned a FULL page at the size that was asked for.
func TestSearchAllSourcesNeverSkipsRowsOfAClampingSource(t *testing.T) {
	tests := []struct {
		name         string
		hidePageSize bool
	}{
		{"a source that reports its clamp", false},
		{"a source that clamps silently", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clamping := newPagingStub("clamping", 40, 5)
			clamping.offsetFromRequestedSize = true
			clamping.hidePageSize = tc.hidePageSize
			svc, game := newAggregateTestService(t, map[string]string{"clamping": ""}, clamping)

			res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "mod", "", nil, 0, 20, 20)
			require.NoError(t, err)

			assert.Equal(t, []int{0}, clamping.requestedPages(),
				"a source that could not honour the requested page size must not be paged on it")

			ids := make([]string, 0, len(res.Mods))
			for _, m := range res.Mods {
				ids = append(ids, m.ID)
			}
			sort.Strings(ids)
			want := []string{"clamping-01", "clamping-02", "clamping-03", "clamping-04", "clamping-05"}
			assert.Equal(t, want, ids, "the rows returned must be contiguous, with no strided holes")
			assert.False(t, res.Exhausted,
				"stopping because we cannot page safely is not the same as having everything")
		})
	}
}

// TestSearchAllSourcesPagesUnevenSources covers the second half of #109's
// premise: two sources with different caps and different catalog sizes. The
// small one exhausts and drops out of the loop; the large one keeps being
// paged until the merged count reaches the limit.
func TestSearchAllSourcesPagesUnevenSources(t *testing.T) {
	small := newPagingStub("small", 3, 5)  // whole catalog on page 0, then done
	large := newPagingStub("large", 40, 4) // 4/page, plenty left over
	svc, game := newAggregateTestService(t, map[string]string{"small": "", "large": ""}, small, large)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "mod", "", nil, 0, 4, 12)
	require.NoError(t, err)
	assert.Equal(t, []int{0}, small.requestedPages(), "an exhausted source must not be asked again")
	assert.GreaterOrEqual(t, len(res.Mods), 12, "the limit is reachable across the two sources")
	assert.Greater(t, len(large.requestedPages()), 1, "the source that still has pages must be advanced")
	assert.False(t, res.Exhausted)
}

// TestSearchAllSourcesPagingGuardStopsAnEndlessSource pins the bound: a
// source that answers every page with a full page forever must not spin the
// loop. maxSearchPagesPerSource rounds is the documented ceiling.
func TestSearchAllSourcesPagingGuardStopsAnEndlessSource(t *testing.T) {
	endless := newPagingStub("endless", 0, 3)
	endless.endless = true
	endless.reportTotal = false
	svc, game := newAggregateTestService(t, map[string]string{"endless": ""}, endless)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "mod", "", nil, 0, 3, 1000)
	require.NoError(t, err)
	assert.Len(t, endless.requestedPages(), core.MaxSearchPagesPerSourceForTest,
		"the loop must stop at the documented max-pages guard")
	assert.Less(t, len(res.Mods), 1000, "the guard trips before the limit is filled")
	assert.False(t, res.Exhausted, "stopping at the guard is not exhaustion")
}

// TestSearchAllSourcesPagingMidLoopErrorIsWarning covers a source that
// answers page 0 and then fails on page 1: reported exactly the way a
// first-page failure is today (a Warning, never an error), with the hits it
// did return kept.
func TestSearchAllSourcesPagingMidLoopErrorIsWarning(t *testing.T) {
	flaky := newPagingStub("flaky", 40, 4)
	flaky.failOnPage = 1
	svc, game := newAggregateTestService(t, map[string]string{"flaky": ""}, flaky)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "mod", "", nil, 0, 4, 10)
	require.NoError(t, err, "a mid-loop page failure must not fail the aggregate")
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "flaky", res.Warnings[0].SourceID)
	assert.ErrorIs(t, res.Warnings[0].Err, errPagingStub)
	assert.Len(t, res.Mods, 4, "page 0's hits survive the page-1 failure")
	assert.False(t, res.Exhausted,
		"a source whose paging was cut short by an error might still have more (Track C review, finding 6)")
}

// TestSearchAllSourcesPagingFirstPageErrorStillWarns is the unchanged
// baseline the case above is measured against.
func TestSearchAllSourcesPagingFirstPageErrorStillWarns(t *testing.T) {
	broken := newPagingStub("broken", 40, 4)
	broken.failOnPage = 0
	ok := newPagingStub("ok", 6, 6)
	svc, game := newAggregateTestService(t, map[string]string{"broken": "", "ok": ""}, broken, ok)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "mod", "", nil, 0, 10, 10)
	require.NoError(t, err)
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "broken", res.Warnings[0].SourceID)
	assert.Len(t, res.Mods, 6)
}

// TestSearchAllSourcesSingleRoundWithoutALimit pins the three shapes that
// must stay exactly one round each, so `lmm serve`'s own paginated search
// page (page+page_size, no limit) is not silently advanced past the page it
// asked for.
func TestSearchAllSourcesSingleRoundWithoutALimit(t *testing.T) {
	tests := []struct {
		name                  string
		page, pageSize, limit int
	}{
		{"no limit at all", 0, 5, 0},
		{"no page size", 0, 0, 10},
		{"an explicit page the caller drives itself", 2, 5, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capped := newPagingStub("capped", 40, 5)
			svc, game := newAggregateTestService(t, map[string]string{"capped": ""}, capped)

			_, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "mod", "", nil,
				tc.page, tc.pageSize, tc.limit)
			require.NoError(t, err)
			assert.Equal(t, []int{tc.page}, capped.requestedPages(),
				"this shape has no cursor to advance: exactly one round, at the requested page")
		})
	}
}

// TestSearchLimitIsFilledByPaging is the same fix seen from the entry point
// every frontend calls: a limit of 10 over pages of 5, against a source
// holding 12, returns 10 rows rather than the single page's 5.
func TestSearchLimitIsFilledByPaging(t *testing.T) {
	capped := newPagingStub("capped", 12, 5)
	svc, game := newAggregateTestService(t, map[string]string{"capped": ""}, capped)

	report, err := svc.Search(context.Background(), game, "default", "mod",
		core.SearchOptions{Page: 0, PageSize: 5, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, report.Mods, 10, "a limit of 10 must return 10 when the sources hold 12")
	assert.Equal(t, 10, report.TotalResults)
	assert.True(t, report.HasMore)
}

// TestSearchPagingHasMoreStaysOptimisticWithoutATotal pins the Track C
// re-review's N4 - a deliberate over-claim, so it does not drift by
// accident. A source that reports no TotalCount and hands over its ENTIRE
// catalogue in one short page is indistinguishable from one that was
// clamped: both answer short, and NexusMods (which reports neither) is the
// real instance. Core therefore answers has_more: true, and the same query
// without a limit - which runs the single-round path and its
// requested-size arithmetic - answers false. Over-claiming costs the user
// an empty next page; under-claiming would hide rows a clamped source
// really does still have, so the optimism is the right direction.
func TestSearchPagingHasMoreStaysOptimisticWithoutATotal(t *testing.T) {
	newStub := func() *pagingStubSource {
		st := newPagingStub("nototal", 3, 10)
		st.reportTotal = false
		return st
	}

	paged := newStub()
	svc, game := newAggregateTestService(t, map[string]string{"nototal": ""}, paged)
	report, err := svc.Search(context.Background(), game, "default", "mod",
		core.SearchOptions{Page: 0, PageSize: 10, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, report.Mods, 3, "the whole catalogue came back in one page")
	assert.Equal(t, []int{0}, paged.requestedPages(), "a short page is never asked again")
	assert.True(t, report.HasMore,
		"a short page from a source reporting no total is ambiguous, and core over-claims deliberately")

	single := newStub()
	svc2, game2 := newAggregateTestService(t, map[string]string{"nototal": ""}, single)
	plain, err := svc2.Search(context.Background(), game2, "default", "mod",
		core.SearchOptions{Page: 0, PageSize: 10})
	require.NoError(t, err)
	assert.Len(t, plain.Mods, 3)
	assert.False(t, plain.HasMore,
		"the single-round path keeps sourceHasMore verbatim, so the same query answers differently")
}

// TestSearchLimitStopsShortOfAClampingSource pins the OTHER half of the
// bargain, and the shape `lmm search --limit N` itself has (the CLI asks
// for a page size equal to the limit, cmd/lmm/search.go's searchPageSize):
// a source that cannot answer a page that large is asked once and no more,
// so the answer is short rather than strided. HasMore says so, and it is
// the honest report - the rows that came back really are the source's first
// N (Track C review, finding 1).
func TestSearchLimitStopsShortOfAClampingSource(t *testing.T) {
	capped := newPagingStub("capped", 12, 5)
	capped.offsetFromRequestedSize = true
	svc, game := newAggregateTestService(t, map[string]string{"capped": ""}, capped)

	report, err := svc.Search(context.Background(), game, "default", "mod",
		core.SearchOptions{Page: 0, PageSize: 10, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, report.Mods, 5, "the source's own cap decides, and its rows are contiguous")
	assert.Equal(t, []int{0}, capped.requestedPages())
	assert.True(t, report.HasMore, "there ARE more - they just cannot be fetched at a safe offset")
}
