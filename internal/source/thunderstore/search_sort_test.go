package thunderstore_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCapabilitiesListsOnlyTheSortItsIndexCanFill: the index keeps
// date_updated per package and nothing about downloads or ratings, so
// updated is the one sort whose field the hits carry (#503).
func TestCapabilitiesListsOnlyTheSortItsIndexCanFill(t *testing.T) {
	s := searchable(t)
	caps := s.src.Capabilities()
	assert.Equal(t, []domain.SearchSort{domain.SortUpdated}, caps.Sorts)
	assert.True(t, caps.SupportsSort(domain.SortUpdated))
	assert.False(t, caps.SupportsSort(domain.SortDownloads))
	assert.False(t, caps.SupportsSort(domain.SortPopular))
}

// TestSortUpdatedOrdersTheWholeMatchSetByDateBeforePaging pins that page N of
// a date-sorted search is the right page: the deprecated package updated most
// recently leads (an explicit date sort is purely by date), and pages
// partition that order.
func TestSortUpdatedOrdersTheWholeMatchSetByDateBeforePaging(t *testing.T) {
	s := searchable(t)

	full := s.search(source.SearchQuery{Sort: domain.SortUpdated, PageSize: 100})
	assert.Equal(t, 12, full.TotalCount)
	assert.Equal(t, []string{
		"Ghostbird-Skinwalker_Sounds",  // 09-09, deprecated but still newest
		"RugbugRedfern-Skinwalkers",    // 09-08
		"BepInEx-BepInExPack",          // 09-07
		"denikson-BepInExPack_Valheim", // 09-06
		"BepInEx-MonoMod_Loader",       // 09-05
		"Evaisa-LethalThings",          // 09-04
		"Umlaut-Cafe_Mod",              // 09-03
		"tinyhoot-ShipLoot",            // 09-02
		"notnotnotswipez-MoreCompany",  // 09-01
		"Evaisa-Ghostbird_Tools",       // 08-31
		"NotAtoms-TerminalApi",         // 08-30, first of the two by name
		"Quiet-QuietTerminal",          // 08-30
	}, ids(full))
	for i := 1; i < len(full.Mods); i++ {
		assert.False(t, full.Mods[i].UpdatedAt.After(full.Mods[i-1].UpdatedAt), "hit %d is newer than hit %d", i, i-1)
	}

	first := s.search(source.SearchQuery{Sort: domain.SortUpdated, PageSize: 5})
	second := s.search(source.SearchQuery{Sort: domain.SortUpdated, Page: 2, PageSize: 5})
	assert.Equal(t, ids(full)[:5], ids(first), "page 1 is the newest five")
	assert.Equal(t, ids(full)[5:10], ids(second))
}

// TestSortUpdatedLeavesFilteringAndRelevanceAlone: the sort reorders what the
// filters kept - it never admits a hidden NSFW package - and an empty or
// relevance sort is the existing ranking exactly.
func TestSortUpdatedLeavesFilteringAndRelevanceAlone(t *testing.T) {
	s := searchable(t)

	// A text query still filters; relevance would put the exact name first,
	// the date sort puts the newest first.
	byDate := s.search(source.SearchQuery{Query: "skinwalkers", Sort: domain.SortUpdated})
	assert.Equal(t, []string{
		"Ghostbird-Skinwalker_Sounds", "RugbugRedfern-Skinwalkers", "Quiet-QuietTerminal",
	}, ids(byDate))
	assert.Equal(t, 3, byDate.TotalCount)

	for _, sort := range []domain.SearchSort{"", domain.SortRelevance} {
		assert.Equal(t,
			ids(s.search(source.SearchQuery{Query: "skinwalkers"})),
			ids(s.search(source.SearchQuery{Query: "skinwalkers", Sort: sort})),
			"sort %q keeps the relevance order", sort)
		assert.Equal(t,
			ids(s.search(source.SearchQuery{PageSize: 100})),
			ids(s.search(source.SearchQuery{PageSize: 100, Sort: sort})))
	}

	// Downloads/popular are not natively sortable here: the order is the
	// source's own, and core re-orders afterwards.
	assert.Equal(t,
		ids(s.search(source.SearchQuery{Query: "skinwalkers"})),
		ids(s.search(source.SearchQuery{Query: "skinwalkers", Sort: domain.SortDownloads})))
}

func TestSortUpdatedStillHidesNSFWUnlessAskedFor(t *testing.T) {
	srv := newIndexServer(t, withNSFWPackage(t, "Umlaut-Cafe_Mod"))
	src, _, _ := newSource(t, srv)

	res, err := src.Search(t.Context(), source.SearchQuery{GameID: testCommunity, Sort: domain.SortUpdated, PageSize: 100})
	require.NoError(t, err)
	assert.Equal(t, 11, res.TotalCount)
	assert.NotContains(t, ids(res), "Umlaut-Cafe_Mod")

	opted, err := src.Search(t.Context(), source.SearchQuery{GameID: testCommunity, Sort: domain.SortUpdated, Tags: []string{"NSFW"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"Umlaut-Cafe_Mod"}, ids(opted))
}
