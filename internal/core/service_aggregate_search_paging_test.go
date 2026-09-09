package core_test

import (
	"context"
	"errors"
	"fmt"
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

	start := q.Page * size
	if start > len(p.catalog) {
		start = len(p.catalog)
	}
	end := min(start+size, len(p.catalog))
	res := source.SearchResult{Mods: p.catalog[start:end], Page: q.Page, PageSize: size}
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
// server-capped at 5 mods per page holding 12, asked for 10. A single page
// can only ever yield 5, so the aggregate must advance that source's cursor
// and pull page 1 as well.
func TestSearchAllSourcesPagesUntilLimit(t *testing.T) {
	capped := newPagingStub("capped", 12, 5)
	svc, game := newAggregateTestService(t, map[string]string{"capped": ""}, capped)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "mod", "", nil, 0, 10, 10)
	require.NoError(t, err)
	assert.Equal(t, []int{0, 1}, capped.requestedPages(), "a 5/page source must be paged twice to fill a limit of 10")
	assert.Len(t, res.Mods, 10, "two 5-mod pages satisfy the limit exactly")
	assert.False(t, res.Exhausted, "12 in the catalog, 10 pulled: there is still a page 2")
}

// TestSearchAllSourcesPagesUnevenSources covers the second half of #109's
// premise: two sources with different caps and different catalog sizes. The
// small one exhausts and drops out of the loop; the large one keeps being
// paged until the merged count reaches the limit.
func TestSearchAllSourcesPagesUnevenSources(t *testing.T) {
	small := newPagingStub("small", 3, 5)  // whole catalog on page 0, then done
	large := newPagingStub("large", 40, 4) // 4/page, plenty left over
	svc, game := newAggregateTestService(t, map[string]string{"small": "", "large": ""}, small, large)

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "mod", "", nil, 0, 12, 12)
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

	res, err := svc.SearchAllSourcesForTest(context.Background(), game.ID, "mod", "", nil, 0, 10, 10)
	require.NoError(t, err, "a mid-loop page failure must not fail the aggregate")
	require.Len(t, res.Warnings, 1)
	assert.Equal(t, "flaky", res.Warnings[0].SourceID)
	assert.ErrorIs(t, res.Warnings[0].Err, errPagingStub)
	assert.Len(t, res.Mods, 4, "page 0's hits survive the page-1 failure")
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
// every frontend actually calls: `lmm search --limit 10` (PageSize == Limit,
// page 0) against a 5-per-page source now returns 10 rows, not 5.
func TestSearchLimitIsFilledByPaging(t *testing.T) {
	capped := newPagingStub("capped", 12, 5)
	svc, game := newAggregateTestService(t, map[string]string{"capped": ""}, capped)

	report, err := svc.Search(context.Background(), game, "default", "mod",
		core.SearchOptions{Page: 0, PageSize: 10, Limit: 10})
	require.NoError(t, err)
	assert.Len(t, report.Mods, 10, "--limit 10 must return 10 when the sources hold 12")
	assert.Equal(t, 10, report.TotalResults)
	assert.True(t, report.HasMore)
}
