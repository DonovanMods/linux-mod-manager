package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #503: every search result list - one source, the aggregate merge, the
// omnibar's capped fan-out - is ordered by ONE contract in core: hits whose
// name equals the query come first, then the rest by the chosen sort, ties
// keeping the order they arrived in. These tests drive core.Search, the only
// entry point every frontend uses.

func sortI64(v int64) *int64 { return &v }

func sortDay(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }

func sortHit(id, name string) domain.Mod {
	return domain.Mod{ID: id, SourceID: "alpha", Name: name}
}

// searchIDs runs core.Search over one stub source holding mods and returns
// the hit ids in the order the report carries them.
func searchIDs(t *testing.T, query string, opts core.SearchOptions, mods ...domain.Mod) []string {
	t.Helper()
	stub := &searchStubSource{id: "alpha", result: source.SearchResult{Mods: mods, TotalCount: len(mods)}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, stub)
	opts.SourceID = "alpha"
	report, err := svc.Search(context.Background(), game, "default", query, opts)
	require.NoError(t, err)
	ids := make([]string, len(report.Mods))
	for i, h := range report.Mods {
		ids[i] = h.ID
	}
	return ids
}

func TestSearchExactNameMatchesComeFirst(t *testing.T) {
	// The #503 case: the mod whose name IS the query sits behind fuzzier hits.
	got := searchIDs(t, "auctionator", core.SearchOptions{},
		sortHit("a", "Auctionator Classic Skin"),
		sortHit("b", "Auction House Tools"),
		sortHit("c", "Auctionator"))
	assert.Equal(t, []string{"c", "a", "b"}, got, "the exact match leads; the rest keep the source's order")
}

func TestSearchExactMatchIgnoresCaseSpaceAndPunctuation(t *testing.T) {
	for _, tc := range []struct{ query, name string }{
		{"AUCTIONATOR", "Auctionator"},
		{"  auctionator  ", "Auctionator"},
		{"Leatrix Plus", "leatrix-plus"},
		{"leatrix-plus", "Leatrix Plus"},
		{"Leatrix_Plus", "Leatrix Plus"},
		{"skyui", "SkyUI"},
	} {
		got := searchIDs(t, tc.query, core.SearchOptions{},
			sortHit("other", "Something Else"), sortHit("exact", tc.name))
		assert.Equal(t, []string{"exact", "other"}, got, "query %q vs name %q", tc.query, tc.name)
	}
}

func TestSearchExactMatchIsEqualityNotContainment(t *testing.T) {
	got := searchIDs(t, "sky", core.SearchOptions{},
		sortHit("a", "SkyUI"), sortHit("b", "Sky"), sortHit("c", "Skyrim Tweaks"))
	assert.Equal(t, []string{"b", "a", "c"}, got)
}

func TestSearchPunctuationOnlyQueryHasNoExactMatches(t *testing.T) {
	// "!!!" normalises to nothing; it must not equal every name that also
	// normalises to nothing.
	got := searchIDs(t, "!!!", core.SearchOptions{}, sortHit("a", "One"), sortHit("b", "???"))
	assert.Equal(t, []string{"a", "b"}, got)
}

func TestSearchSeveralExactMatchesKeepTheirOrder(t *testing.T) {
	got := searchIDs(t, "ui", core.SearchOptions{Sort: domain.SortRelevance},
		sortHit("a", "Other"), sortHit("b", "UI"), sortHit("c", "More"), sortHit("d", "ui"))
	assert.Equal(t, []string{"b", "d", "a", "c"}, got)
}

func TestSearchSortUpdatedIsNewestFirstWithZeroDatesLast(t *testing.T) {
	mods := []domain.Mod{
		{ID: "undated", Name: "u"},
		{ID: "old", Name: "o", UpdatedAt: sortDay(1)},
		{ID: "new", Name: "n", UpdatedAt: sortDay(20)},
		{ID: "mid", Name: "m", UpdatedAt: sortDay(10)},
		{ID: "mid2", Name: "m2", UpdatedAt: sortDay(10)},
	}
	got := searchIDs(t, "zzz", core.SearchOptions{Sort: domain.SortUpdated}, mods...)
	assert.Equal(t, []string{"new", "mid", "mid2", "old", "undated"}, got,
		"newest at the top, equal dates in source order, zero dates last")
}

func TestSearchSortDownloadsIsMostFirstWithStableTies(t *testing.T) {
	got := searchIDs(t, "zzz", core.SearchOptions{Sort: domain.SortDownloads},
		domain.Mod{ID: "a", Name: "a", Downloads: 5},
		domain.Mod{ID: "b", Name: "b", Downloads: 500},
		domain.Mod{ID: "c", Name: "c", Downloads: 5},
		domain.Mod{ID: "d", Name: "d"})
	assert.Equal(t, []string{"b", "a", "c", "d"}, got)
}

func TestSearchSortPopularIsMostEndorsedWithNilLast(t *testing.T) {
	got := searchIDs(t, "zzz", core.SearchOptions{Sort: domain.SortPopular},
		domain.Mod{ID: "none", Name: "none"},
		domain.Mod{ID: "zero", Name: "zero", Endorsements: sortI64(0)},
		domain.Mod{ID: "lots", Name: "lots", Endorsements: sortI64(900)},
		domain.Mod{ID: "few", Name: "few", Endorsements: sortI64(3)})
	assert.Equal(t, []string{"lots", "few", "zero", "none"}, got,
		"a source that reports no rating (nil) sorts after one that reports zero")
}

func TestSearchExactMatchBeatsEverySort(t *testing.T) {
	mods := []domain.Mod{
		{ID: "big", Name: "Big Auction Pack", Downloads: 9_000_000, UpdatedAt: sortDay(25), Endorsements: sortI64(9000)},
		{ID: "exact", Name: "Auctionator", Downloads: 10, UpdatedAt: sortDay(1), Endorsements: sortI64(1)},
		{ID: "mid", Name: "Auctionator Plus", Downloads: 5_000, UpdatedAt: sortDay(15), Endorsements: sortI64(50)},
	}
	for _, sort := range []domain.SearchSort{domain.SortUpdated, domain.SortDownloads, domain.SortPopular} {
		got := searchIDs(t, "auctionator", core.SearchOptions{Sort: sort}, mods...)
		assert.Equal(t, "exact", got[0], "sort %s", sort)
		assert.Equal(t, []string{"exact", "big", "mid"}, got, "sort %s orders the rest descending", sort)
	}
}

func TestSearchRelevanceKeepsTheSourcesOrderBehindExactMatches(t *testing.T) {
	got := searchIDs(t, "armor", core.SearchOptions{}, // "" is relevance
		domain.Mod{ID: "z", Name: "Zeta", Downloads: 1},
		domain.Mod{ID: "a", Name: "Alpha", Downloads: 99})
	assert.Equal(t, []string{"z", "a"}, got, "relevance is the source's own order, not a re-sort")
}

func TestSearchLimitCutsAfterOrdering(t *testing.T) {
	// The exact match is the 4th hit; a --limit 2 that cut first would lose it.
	got := searchIDs(t, "auctionator", core.SearchOptions{Limit: 2},
		sortHit("a", "A"), sortHit("b", "B"), sortHit("c", "C"), sortHit("exact", "Auctionator"))
	assert.Equal(t, []string{"exact", "a"}, got)
}

func TestSearchAggregateOrdersAcrossSources(t *testing.T) {
	a := &searchStubSource{id: "alpha", result: source.SearchResult{TotalCount: 2, Mods: []domain.Mod{
		{ID: "a1", SourceID: "alpha", Name: "Cool One", Downloads: 10, UpdatedAt: sortDay(2)},
		{ID: "a2", SourceID: "alpha", Name: "Cool Two", Downloads: 900, UpdatedAt: sortDay(3)},
	}}}
	b := &searchStubSource{id: "beta", result: source.SearchResult{TotalCount: 2, Mods: []domain.Mod{
		{ID: "b1", SourceID: "beta", Name: "Cool Three", Downloads: 400, UpdatedAt: sortDay(28)},
		{ID: "b2", SourceID: "beta", Name: "cool", Downloads: 1, UpdatedAt: sortDay(1)},
	}}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": "", "beta": ""}, a, b)

	ids := func(sort domain.SearchSort) []string {
		report, err := svc.Search(context.Background(), game, "default", "cool", core.SearchOptions{Sort: sort})
		require.NoError(t, err)
		out := make([]string, len(report.Mods))
		for i, h := range report.Mods {
			out[i] = h.ID
		}
		return out
	}
	assert.Equal(t, []string{"b2", "a2", "b1", "a1"}, ids(domain.SortDownloads), "exact first, then downloads across both sources")
	assert.Equal(t, []string{"b2", "b1", "a2", "a1"}, ids(domain.SortUpdated))
	// Relevance keeps the existing aggregate ranking (name match, downloads
	// descending) - unchanged by #503 apart from the exact match leading.
	assert.Equal(t, []string{"b2", "a2", "b1", "a1"}, ids(domain.SortRelevance))
}

func TestSearchForwardsTheSortToEverySource(t *testing.T) {
	a := &searchStubSource{id: "alpha", result: source.SearchResult{Mods: mods("alpha", "x")}}
	b := &searchStubSource{id: "beta", result: source.SearchResult{Mods: mods("beta", "y")}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": "", "beta": ""}, a, b)

	_, err := svc.Search(context.Background(), game, "default", "q", core.SearchOptions{Sort: domain.SortDownloads})
	require.NoError(t, err)
	assert.Equal(t, domain.SortDownloads, a.gotSort)
	assert.Equal(t, domain.SortDownloads, b.gotSort)

	_, err = svc.Search(context.Background(), game, "default", "q", core.SearchOptions{SourceID: "alpha", Sort: domain.SortUpdated})
	require.NoError(t, err)
	assert.Equal(t, domain.SortUpdated, a.gotSort, "the named-source path forwards it too")

	_, err = svc.Search(context.Background(), game, "default", "q", core.SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, domain.SortRelevance, a.gotSort, "the default is relevance, never an empty string a source must interpret")
}

func TestSearchRejectsAnUnknownSortBeforeAskingAnySource(t *testing.T) {
	a := &searchStubSource{id: "alpha", result: source.SearchResult{Mods: mods("alpha", "x")}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, a)

	_, err := svc.Search(context.Background(), game, "default", "q", core.SearchOptions{Sort: "newest"})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidSearchSort)
	assert.True(t, core.IsInvalidSearchSort(err), "the classifier a frontend branches on")
	assert.Empty(t, a.gotGame, "no source was searched")
}

func TestSearchReportEchoesTheSortAndOffersOnlyMeaningfulOnes(t *testing.T) {
	caps := func(sorts ...domain.SearchSort) *source.Capabilities {
		return &source.Capabilities{Search: true, Sorts: sorts}
	}
	rich := &capsStubSource{&searchStubSource{id: "rich", caps: caps(domain.SortUpdated, domain.SortDownloads, domain.SortPopular),
		result: source.SearchResult{Mods: mods("rich", "r")}}}
	plain := &capsStubSource{&searchStubSource{id: "plain", caps: caps(domain.SortUpdated),
		result: source.SearchResult{Mods: mods("plain", "p")}}}
	broken := &capsStubSource{&searchStubSource{id: "broken", caps: caps(domain.SortPopular), err: assert.AnError}}
	svc, game := newAggregateTestService(t, map[string]string{"rich": "", "plain": "", "broken": ""}, rich, plain, broken)

	report, err := svc.Search(context.Background(), game, "default", "q", core.SearchOptions{Sort: domain.SortUpdated})
	require.NoError(t, err)
	assert.Equal(t, domain.SortUpdated, report.Sort)
	assert.Equal(t, []domain.SearchSort{domain.SortRelevance, domain.SortUpdated, domain.SortDownloads, domain.SortPopular},
		report.SortsAvailable, "the union over sources that answered, in display order")

	report, err = svc.Search(context.Background(), game, "default", "q", core.SearchOptions{SourceID: "plain"})
	require.NoError(t, err)
	assert.Equal(t, domain.SortRelevance, report.Sort, "an unset sort reports the default")
	assert.Equal(t, []domain.SearchSort{domain.SortRelevance, domain.SortUpdated}, report.SortsAvailable,
		"a named source offers only its own")

	// A source that fails contributes nothing: a sort only it could honour
	// would be a sort that orders nothing.
	svc2, game2 := newAggregateTestService(t, map[string]string{"plain": "", "broken": ""}, plain, broken)
	report, err = svc2.Search(context.Background(), game2, "default", "q", core.SearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, []domain.SearchSort{domain.SortRelevance, domain.SortUpdated}, report.SortsAvailable)
}

func TestSearchReportWithNoHitsStillCarriesTheSort(t *testing.T) {
	a := &capsStubSource{&searchStubSource{id: "alpha", caps: &source.Capabilities{Search: true, Sorts: []domain.SearchSort{domain.SortDownloads}},
		result: source.SearchResult{}}}
	svc, game := newAggregateTestService(t, map[string]string{"alpha": ""}, a)
	report, err := svc.Search(context.Background(), game, "default", "q", core.SearchOptions{Sort: domain.SortDownloads})
	require.NoError(t, err)
	assert.Equal(t, domain.SortDownloads, report.Sort)
	assert.Equal(t, []domain.SearchSort{domain.SortRelevance, domain.SortDownloads}, report.SortsAvailable)
}
