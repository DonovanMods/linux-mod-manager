package thunderstore_test

import (
	"math"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// searchable builds a Source with the fixture already indexed, so a search
// test asserts ranking rather than plumbing.
func searchable(t *testing.T) *sourceUnderTest {
	t.Helper()
	srv := newIndexServer(t, fixtureDocument(t))
	src, cacheDir, clock := newSource(t, srv)
	_, err := src.RefreshIndex(t.Context(), testCommunity, false, nil)
	require.NoError(t, err)
	return &sourceUnderTest{t: t, src: src, srv: srv, cacheDir: cacheDir, clock: clock}
}

// search runs one query against the indexed fixture.
func (s *sourceUnderTest) search(query source.SearchQuery) source.SearchResult {
	s.t.Helper()
	if query.GameID == "" {
		query.GameID = testCommunity
	}
	result, err := s.src.Search(s.t.Context(), query)
	require.NoError(s.t, err)
	return result
}

// ids is the ranked list of package ids a result carries.
func ids(result source.SearchResult) []string {
	out := make([]string, 0, len(result.Mods))
	for _, m := range result.Mods {
		out = append(out, m.ID)
	}
	return out
}

// TestSearchRanking is the ranking policy, as a table. Every case names the
// rule it exists for.
func TestSearchRanking(t *testing.T) {
	s := searchable(t)
	tests := []struct {
		name  string
		query source.SearchQuery
		want  []string
		total int
	}{
		{
			name:  "an exact name outranks a mention in a description",
			query: source.SearchQuery{Query: "skinwalkers"},
			want: []string{
				"RugbugRedfern-Skinwalkers",   // the name itself: 100
				"Quiet-QuietTerminal",         // a description mention: 3
				"Ghostbird-Skinwalker_Sounds", // deprecated: never above a live hit
			},
			total: 3,
		},
		{
			name:  "a name prefix outranks a description",
			query: source.SearchQuery{Query: "skinwalker"},
			want: []string{
				"RugbugRedfern-Skinwalkers",
				"Quiet-QuietTerminal",
				"Ghostbird-Skinwalker_Sounds",
			},
			total: 3,
		},
		{
			name:  "every term must hit somewhere",
			query: source.SearchQuery{Query: "skinwalkers voices"},
			want:  []string{"RugbugRedfern-Skinwalkers"},
			total: 1,
		},
		{
			name:  "a term nobody carries finds nothing",
			query: source.SearchQuery{Query: "skinwalkers helicopter"},
			want:  []string{},
			total: 0,
		},
		{
			name:  "a name substring outranks an owner match",
			query: source.SearchQuery{Query: "ghostbird"},
			want: []string{
				"Evaisa-Ghostbird_Tools",      // name prefix: 40
				"Ghostbird-Skinwalker_Sounds", // owner: 10, and deprecated besides
			},
			total: 2,
		},
		{
			name:  "an owner match ties break on the most recently updated",
			query: source.SearchQuery{Query: "evaisa"},
			want:  []string{"Evaisa-LethalThings", "Evaisa-Ghostbird_Tools"},
			total: 2,
		},
		{
			name:  "a category is a weak signal beside a name",
			query: source.SearchQuery{Query: "tools"},
			want: []string{
				"Evaisa-Ghostbird_Tools", // name substring 20 + its own category 8
				"BepInEx-BepInExPack",    // category only: 8, most recent
				"Umlaut-Cafe_Mod",
				"NotAtoms-TerminalApi",
			},
			total: 4,
		},
		{
			name:  "a description-only match still matches",
			query: source.SearchQuery{Query: "cosmetics"},
			want:  []string{"notnotnotswipez-MoreCompany"},
			total: 1,
		},
		{
			name:  "matching is case-folded",
			query: source.SearchQuery{Query: "SKINWALKERS VOICES"},
			want:  []string{"RugbugRedfern-Skinwalkers"},
			total: 1,
		},
		{
			name:  "a non-ASCII description matches too",
			query: source.SearchQuery{Query: "café"},
			want:  []string{"Umlaut-Cafe_Mod"},
			total: 1,
		},
		{
			name:  "an underscore in a name is searchable as it is written",
			query: source.SearchQuery{Query: "cafe_mod"},
			want:  []string{"Umlaut-Cafe_Mod"},
			total: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := s.search(tt.query)
			assert.Equal(t, tt.want, ids(result))
			assert.Equal(t, tt.total, result.TotalCount)
		})
	}
}

// TestEmptyQueryBrowsesByMostRecentlyUpdated pins the browse case, and with
// it the two tie-break keys that make paging stable.
func TestEmptyQueryBrowsesByMostRecentlyUpdated(t *testing.T) {
	s := searchable(t)
	result := s.search(source.SearchQuery{PageSize: 100})

	assert.Equal(t, 12, result.TotalCount)
	assert.Equal(t, []string{
		"RugbugRedfern-Skinwalkers",    // 09-08
		"BepInEx-BepInExPack",          // 09-07
		"denikson-BepInExPack_Valheim", // 09-06
		"BepInEx-MonoMod_Loader",       // 09-05
		"Evaisa-LethalThings",          // 09-04
		"Umlaut-Cafe_Mod",              // 09-03
		"tinyhoot-ShipLoot",            // 09-02
		"notnotnotswipez-MoreCompany",  // 09-01
		"Evaisa-Ghostbird_Tools",       // 08-31
		"NotAtoms-TerminalApi",         // 08-30, and first of the two by name
		"Quiet-QuietTerminal",          // 08-30
		"Ghostbird-Skinwalker_Sounds",  // 09-09, but deprecated
	}, ids(result))
}

// TestFiltersApplyBeforeRanking pins that TotalCount counts FILTERED hits -
// a paginated frontend showing "12 results" for a filtered query would be
// lying about what it can page to.
func TestFiltersApplyBeforeRanking(t *testing.T) {
	s := searchable(t)

	byCategory := s.search(source.SearchQuery{Category: "Libraries", PageSize: 100})
	assert.Equal(t, 4, byCategory.TotalCount)
	assert.Equal(t, []string{
		"BepInEx-BepInExPack", "denikson-BepInExPack_Valheim",
		"BepInEx-MonoMod_Loader", "NotAtoms-TerminalApi",
	}, ids(byCategory))

	// Case-folded, and ANDed with the query.
	folded := s.search(source.SearchQuery{Query: "bepinexpack", Category: "libraries", PageSize: 100})
	assert.Equal(t, 2, folded.TotalCount)

	// Tags filter the same field, ANDed with each other.
	byTags := s.search(source.SearchQuery{Tags: []string{"Mods", "Client-side"}, PageSize: 100})
	assert.Equal(t, 4, byTags.TotalCount)
	assert.Equal(t, []string{
		"RugbugRedfern-Skinwalkers", "Evaisa-LethalThings",
		"tinyhoot-ShipLoot", "Quiet-QuietTerminal",
	}, ids(byTags))

	// A category nothing carries filters everything out - not an error.
	none := s.search(source.SearchQuery{Category: "Nonexistent"})
	assert.Zero(t, none.TotalCount)
	assert.Empty(t, none.Mods)
}

// TestPaginationIsExactAndDeep pins what makes Thunderstore the first
// source that knows its own total: pages slice a ranked list whose length
// is a fact, and a page past the end is empty rather than an error.
func TestPaginationIsExactAndDeep(t *testing.T) {
	s := searchable(t)

	first := s.search(source.SearchQuery{PageSize: 5})
	assert.Equal(t, 12, first.TotalCount)
	assert.Equal(t, 1, first.Page)
	assert.Equal(t, 5, first.PageSize)
	require.Len(t, first.Mods, 5)

	second := s.search(source.SearchQuery{Page: 2, PageSize: 5})
	require.Len(t, second.Mods, 5)
	third := s.search(source.SearchQuery{Page: 3, PageSize: 5})
	require.Len(t, third.Mods, 2)

	// The three pages partition the ranked list exactly: no repeat, no gap.
	var walked []string
	walked = append(walked, ids(first)...)
	walked = append(walked, ids(second)...)
	walked = append(walked, ids(third)...)
	assert.Equal(t, ids(s.search(source.SearchQuery{PageSize: 100})), walked)

	past := s.search(source.SearchQuery{Page: 99, PageSize: 5})
	assert.Empty(t, past.Mods, "a page past the end is empty...")
	assert.Equal(t, 12, past.TotalCount, "...with the same total")
	assert.Equal(t, 99, past.Page)
}

// TestPagingCannotOverflowIntoANegativeSlice is T1 review #1: `start :=
// (page - 1) * pageSize` overflows int for a large enough page, and a
// NEGATIVE start walks straight past the `start > total` clamp into
// matches[start:end]. A page number is a QUERY PARAMETER - `lmm serve`
// forwards ?page= verbatim - so no value of it may panic a source.
func TestPagingCannotOverflowIntoANegativeSlice(t *testing.T) {
	s := searchable(t)

	pages := []int{math.MaxInt, math.MaxInt - 1, 1 << 62, 1<<62 - 1, 1 << 31}
	sizes := []int{0, 1, 5, 20, 100, 500, -1}
	for _, page := range pages {
		for _, size := range sizes {
			result := s.search(source.SearchQuery{Page: page, PageSize: size})
			assert.Empty(t, result.Mods, "page %d (size %d) is past the end", page, size)
			assert.Equal(t, 12, result.TotalCount, "...and still knows the total")
		}
	}

	// A negative page stays what it always was - page 1, a page of results
	// - rather than becoming another way to reach an empty one.
	for _, page := range []int{-1, math.MinInt, math.MinInt + 1} {
		first := s.search(source.SearchQuery{Page: page, PageSize: 5})
		assert.Equal(t, 1, first.Page, "page %d clamps to the first page", page)
		assert.Len(t, first.Mods, 5)
	}
}

// TestPageSizeClamping pins the defaults a caller gets for asking for
// nothing, and the ceiling for asking for too much.
func TestPageSizeClamping(t *testing.T) {
	s := searchable(t)

	byDefault := s.search(source.SearchQuery{})
	assert.Equal(t, 20, byDefault.PageSize)
	assert.Len(t, byDefault.Mods, 12, "twelve is under the default page size")

	capped := s.search(source.SearchQuery{PageSize: 500})
	assert.Equal(t, 100, capped.PageSize)

	negative := s.search(source.SearchQuery{Page: -3, PageSize: -1})
	assert.Equal(t, 1, negative.Page)
	assert.Equal(t, 20, negative.PageSize)
}

// TestSearchHitsAreFullyMappedMods pins §3.2's field mapping - what a
// frontend renders out of a search result, with no second call.
func TestSearchHitsAreFullyMappedMods(t *testing.T) {
	s := searchable(t)
	result := s.search(source.SearchQuery{Query: "skinwalkers"})
	require.NotEmpty(t, result.Mods)
	mod := result.Mods[0]

	assert.Equal(t, "RugbugRedfern-Skinwalkers", mod.ID, "identity is full_name, not the uuid4")
	assert.Equal(t, "thunderstore", mod.SourceID)
	assert.Equal(t, "Skinwalkers", mod.Name)
	assert.Equal(t, "RugbugRedfern", mod.Author)
	assert.Equal(t, "3.0.2", mod.Version, "versions are newest-first")
	assert.Equal(t, testCommunity, mod.GameID)
	assert.Contains(t, mod.Description, "mimic the voices")
	assert.Equal(t, "Mods, BepInEx, Client-side", mod.Category)
	assert.Equal(t, "https://thunderstore.io/c/lethal-company/p/RugbugRedfern/Skinwalkers/", mod.SourceURL)
	assert.Equal(t, "https://gcdn.thunderstore.io/live/repository/icons/RugbugRedfern-Skinwalkers-3.0.2.png", mod.PictureURL)
	assert.Equal(t, time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC), mod.UpdatedAt)

	// An underscore in a package name is a space in its display name.
	cafe := s.search(source.SearchQuery{Query: "cafe_mod"})
	require.Len(t, cafe.Mods, 1)
	assert.Equal(t, "Cafe Mod", cafe.Mods[0].Name)

	// Deprecation gets no new field: it rides in the categories every
	// existing renderer already prints.
	deprecated := s.search(source.SearchQuery{Query: "skinwalker_sounds"})
	require.Len(t, deprecated.Mods, 1)
	assert.Equal(t, "Mods, Deprecated", deprecated.Mods[0].Category)
}

// TestColdSearchBuildsTheIndexItself pins that correctness does not depend
// on a frontend remembering to prime the index first.
func TestColdSearchBuildsTheIndexItself(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, _, _ := newSource(t, srv)

	result, err := src.Search(t.Context(), source.SearchQuery{GameID: testCommunity, Query: "skinwalkers"})
	require.NoError(t, err)
	assert.Equal(t, 3, result.TotalCount)

	status, err := src.IndexStatus(t.Context(), testCommunity)
	require.NoError(t, err)
	assert.True(t, status.Present)
}

// TestConcurrentColdSearchesFetchOnce pins the per-community build lock:
// two searches racing on a cold index must not both download the document.
func TestConcurrentColdSearchesFetchOnce(t *testing.T) {
	srv := newIndexServer(t, fixtureDocument(t))
	src, _, _ := newSource(t, srv)

	done := make(chan error, 4)
	for range 4 {
		go func() {
			_, err := src.Search(t.Context(), source.SearchQuery{GameID: testCommunity, Query: "skinwalkers"})
			done <- err
		}()
	}
	for range 4 {
		require.NoError(t, <-done)
	}

	requests, _, _, _ := srv.counts()
	assert.Equal(t, 1, requests)
}
