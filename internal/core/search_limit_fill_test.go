package core_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #511: `--limit N` above a source's page cap must be filled by the source's
// following pages. CurseForge clamps a page to 50 and names the clamp, but
// its #503 slug lookup puts a 51st row on page 0, which used to defeat the
// "the page was full and honest" guards and stop the fill after one page.

// slugStub is CurseForge's shape: 200 mods, 50 per page, the clamp reported,
// offsets computed from the requested size, and page 0 carrying one extra
// hit that ALSO sits in its natural place on page 1.
func slugStub() *pagingStubSource {
	st := newPagingStub("cf", 200, 50)
	st.offsetFromRequestedSize = true
	extra := st.catalog[60] // lives on page 1 (rows 50-99)
	st.prependOnPageZero = &extra
	return st
}

func uniqueIDs(t *testing.T, ids []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, id := range ids {
		assert.False(t, seen[id], "duplicate hit %s", id)
		seen[id] = true
	}
}

func hitIDs(report *core.SearchReport) []string {
	ids := make([]string, len(report.Mods))
	for i, h := range report.Mods {
		ids[i] = h.ID
	}
	return ids
}

func TestSearchLimitIsFilledPastAnExtraRowOnPageZero(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sourceID string // "" is the aggregate path
	}{{"aggregate", ""}, {"named source", "cf"}} {
		t.Run(tc.name, func(t *testing.T) {
			stub := slugStub()
			svc, game := newAggregateTestService(t, map[string]string{"cf": ""}, stub)

			report, err := svc.Search(context.Background(), game, "default", "mod",
				core.SearchOptions{SourceID: tc.sourceID, PageSize: 100, Limit: 100})
			require.NoError(t, err)
			assert.Equal(t, []int{0, 1}, stub.requestedPages(), "two 50-row pages fill 100")
			require.Len(t, report.Mods, 100)
			uniqueIDs(t, hitIDs(report))
			assert.Equal(t, 100, report.TotalResults, "the unique hits fetched, the slug hit counted once")
			assert.True(t, report.HasMore, "the source still has rows past the 100")
		})
	}
}

func TestSearchLimitFillStopsWhenTheSourceHasNoMore(t *testing.T) {
	for _, sourceID := range []string{"", "cf"} {
		t.Run("source="+sourceID, func(t *testing.T) {
			stub := newPagingStub("cf", 120, 50)
			stub.offsetFromRequestedSize = true
			svc, game := newAggregateTestService(t, map[string]string{"cf": ""}, stub)

			report, err := svc.Search(context.Background(), game, "default", "mod",
				core.SearchOptions{SourceID: sourceID, PageSize: 500, Limit: 500})
			require.NoError(t, err)
			assert.Equal(t, []int{0, 1, 2}, stub.requestedPages(), "no page after the last one")
			assert.Len(t, report.Mods, 120)
			assert.Equal(t, 120, report.TotalResults)
			assert.False(t, report.HasMore, "everything the source has was returned")
		})
	}
}

func TestSearchLimitFillIsCappedAtMaxPagesPerSource(t *testing.T) {
	stub := newPagingStub("cf", 0, 3)
	stub.endless = true
	stub.reportTotal = false
	svc, game := newAggregateTestService(t, map[string]string{"cf": ""}, stub)

	report, err := svc.Search(context.Background(), game, "default", "mod",
		core.SearchOptions{SourceID: "cf", PageSize: 3, Limit: 1000})
	require.NoError(t, err)
	assert.Len(t, stub.requestedPages(), core.MaxSearchPagesPerSourceForTest)
	assert.Len(t, report.Mods, 3*core.MaxSearchPagesPerSourceForTest)
	assert.True(t, report.HasMore, "stopping at the cap is not exhaustion")
}

func TestSearchLimitFillChecksTheContextBetweenPages(t *testing.T) {
	for _, sourceID := range []string{"", "cf"} {
		t.Run("source="+sourceID, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stub := newPagingStub("cf", 500, 50)
			stub.offsetFromRequestedSize = true
			stub.onSearch = func(page int) {
				if page == 1 {
					cancel()
				}
			}
			svc, game := newAggregateTestService(t, map[string]string{"cf": ""}, stub)

			_, err := svc.Search(ctx, game, "default", "mod",
				core.SearchOptions{SourceID: sourceID, PageSize: 400, Limit: 400})
			require.ErrorIs(t, err, context.Canceled)
			assert.Equal(t, []int{0, 1}, stub.requestedPages(), "no page is requested once the context is done")
		})
	}
}

func TestSearchLimitFillOrdersTheCombinedListBeforeTheCut(t *testing.T) {
	stub := newPagingStub("cf", 200, 50)
	stub.offsetFromRequestedSize = true
	stub.catalog[70].Name = "Exact Name" // page 1: must lead a --limit 60 cut
	svc, game := newAggregateTestService(t, map[string]string{"cf": ""}, stub)

	for _, sourceID := range []string{"", "cf"} {
		report, err := svc.Search(context.Background(), game, "default", "exact name",
			core.SearchOptions{SourceID: sourceID, PageSize: 60, Limit: 60})
		require.NoError(t, err)
		require.Len(t, report.Mods, 60, fmt.Sprintf("source=%q", sourceID))
		assert.Equal(t, stub.catalog[70].ID, report.Mods[0].ID, "an exact match from page 2 survives the cut")
	}
}

func TestSearchWithoutALimitOrWithAPageStaysOnePage(t *testing.T) {
	stub := newPagingStub("cf", 200, 50)
	stub.offsetFromRequestedSize = true
	svc, game := newAggregateTestService(t, map[string]string{"cf": ""}, stub)

	for _, opts := range []core.SearchOptions{
		{SourceID: "cf", PageSize: 50},
		{SourceID: "cf", PageSize: 50, Page: 1, Limit: 100},
		{SourceID: "cf", Limit: 100},
	} {
		_, err := svc.Search(context.Background(), game, "default", "mod", opts)
		require.NoError(t, err)
	}
	assert.Equal(t, []int{0, 1, 0}, stub.requestedPages(),
		"no limit, an explicit page, or no page size each stay a single request")
}

func TestSearchLimitFillStopsOnAPageThatAddsNothingNew(t *testing.T) {
	// A source that names its page size but ignores the page index answers
	// every page with the same rows: asking again can only repeat itself.
	stub := newPagingStub("cf", 50, 50)
	stub.ignorePage = true
	stub.reportTotal = false
	svc, game := newAggregateTestService(t, map[string]string{"cf": ""}, stub)

	for _, sourceID := range []string{"", "cf"} {
		stub.pages = nil
		report, err := svc.Search(context.Background(), game, "default", "mod",
			core.SearchOptions{SourceID: sourceID, PageSize: 50, Limit: 500})
		require.NoError(t, err)
		assert.Len(t, report.Mods, 50, "source=%q", sourceID)
		assert.Equal(t, []int{0, 1}, stub.requestedPages(), "the repeat page is the last one asked for (source=%q)", sourceID)
	}
}

func TestSearchLimitFillKeepsEarlierPagesWhenALaterPageFails(t *testing.T) {
	stub := newPagingStub("cf", 200, 50)
	stub.offsetFromRequestedSize = true
	stub.failOnPage = 1
	svc, game := newAggregateTestService(t, map[string]string{"cf": ""}, stub)

	report, err := svc.Search(context.Background(), game, "default", "mod",
		core.SearchOptions{SourceID: "cf", PageSize: 100, Limit: 100})
	require.NoError(t, err)
	assert.Len(t, report.Mods, 50, "page 0's hits are kept")
	require.Len(t, report.Warnings, 1)
	assert.ErrorIs(t, report.Warnings[0].Err, errPagingStub)
	assert.True(t, report.HasMore, "a source cut short by an error might have more")
}
