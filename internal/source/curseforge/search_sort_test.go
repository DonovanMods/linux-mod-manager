package curseforge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// searchStub is a CurseForge /v1/mods/search double: the main search (the
// request carrying searchFilter) answers with main, the slug lookup (the
// request carrying slug) with slugHits, or slugStatus when that is non-zero.
// It records every request's query so a test can assert what was asked.
type searchStub struct {
	mu         sync.Mutex
	queries    []url.Values
	main       []Mod
	slugHits   []Mod
	slugStatus int
	onSlug     func()
}

func (s *searchStub) handler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	s.queries = append(s.queries, q)
	s.mu.Unlock()

	data := s.main
	if q.Has("slug") {
		if s.onSlug != nil {
			s.onSlug()
		}
		if s.slugStatus != 0 {
			http.Error(w, "boom", s.slugStatus)
			return
		}
		data = s.slugHits
	}
	if data == nil {
		data = []Mod{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":       data,
		"pagination": map[string]int{"index": 0, "pageSize": 20, "resultCount": len(data), "totalCount": 100},
	})
}

// slugQueries is the recorded slug lookups.
func (s *searchStub) slugQueries() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []url.Values
	for _, q := range s.queries {
		if q.Has("slug") {
			out = append(out, q)
		}
	}
	return out
}

func newSearchStubSource(t *testing.T, stub *searchStub) *CurseForge {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	t.Cleanup(srv.Close)
	cf := New(srv.Client(), "test-api-key")
	cf.client.SetBaseURL(srv.URL)
	return cf
}

func cfMod(id int, name string, downloads int64) Mod {
	return Mod{ID: id, GameID: 432, Name: name, DownloadCount: downloads}
}

func names(mods []domain.Mod) []string {
	out := make([]string, len(mods))
	for i, m := range mods {
		out[i] = m.Name
	}
	return out
}

func TestClient_SearchMods_SortParams(t *testing.T) {
	tests := []struct {
		sort      domain.SearchSort
		wantField string // empty: the param must be absent
	}{
		{domain.SortUpdated, "3"},
		{domain.SortDownloads, "6"},
		{domain.SortPopular, "12"},
		{domain.SortRelevance, ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.sort), func(t *testing.T) {
			stub := &searchStub{}
			cf := newSearchStubSource(t, stub)

			_, _, err := cf.client.SearchMods(context.Background(), 432, "x", 0, 20, 0, tt.sort)
			require.NoError(t, err)
			require.Len(t, stub.queries, 1)
			q := stub.queries[0]
			if tt.wantField == "" {
				assert.False(t, q.Has("sortField"), "relevance sends no sortField")
				assert.False(t, q.Has("sortOrder"), "relevance sends no sortOrder")
				return
			}
			assert.Equal(t, tt.wantField, q.Get("sortField"))
			assert.Equal(t, "desc", q.Get("sortOrder"))
		})
	}
}

func TestCurseForge_Search_NonRelevanceSortKeepsTheAPIOrder(t *testing.T) {
	// API order: the name match with fewer downloads is LAST. Relevance would
	// reorder it first; a native sort must leave the page as the server sent it.
	page := []Mod{cfMod(1, "Unrelated Big", 900), cfMod(2, "Other", 500), cfMod(3, "Foo Mod", 10)}

	for _, sortBy := range []domain.SearchSort{domain.SortUpdated, domain.SortDownloads, domain.SortPopular} {
		stub := &searchStub{main: page}
		cf := newSearchStubSource(t, stub)
		res, err := cf.Search(context.Background(), source.SearchQuery{GameID: "432", Query: "foo", PageSize: 20, Sort: sortBy})
		require.NoError(t, err)
		assert.Equal(t, []string{"Unrelated Big", "Other", "Foo Mod"}, names(res.Mods), "sort %s", sortBy)
	}

	stub := &searchStub{main: page}
	cf := newSearchStubSource(t, stub)
	res, err := cf.Search(context.Background(), source.SearchQuery{GameID: "432", Query: "foo", PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, []string{"Foo Mod", "Unrelated Big", "Other"}, names(res.Mods), "relevance keeps the name-match-first order")
}

func TestCurseForge_Search_ExactNameOnPageSkipsTheLookup(t *testing.T) {
	stub := &searchStub{main: []Mod{cfMod(1, "Auctionator Plus", 9), cfMod(2, "Auctionator", 1)}}
	cf := newSearchStubSource(t, stub)

	res, err := cf.Search(context.Background(), source.SearchQuery{GameID: "432", Query: "auctionator", PageSize: 20})
	require.NoError(t, err)
	assert.Len(t, res.Mods, 2)
	assert.Empty(t, stub.slugQueries(), "an exact-name hit already on the page needs no extra request")
}

func TestCurseForge_Search_ExactNameMissingIsLookedUpBySlugAndPrepended(t *testing.T) {
	stub := &searchStub{
		main:     []Mod{cfMod(1, "Auction House Helper", 900), cfMod(2, "Auctionator Addon", 800)},
		slugHits: []Mod{cfMod(7, "Auctionator", 5)},
	}
	cf := newSearchStubSource(t, stub)

	res, err := cf.Search(context.Background(), source.SearchQuery{
		GameID: "432", Query: "Auctionator", Category: "55", PageSize: 20, Sort: domain.SortDownloads,
	})
	require.NoError(t, err)

	slugs := stub.slugQueries()
	require.Len(t, slugs, 1)
	assert.Equal(t, "432", slugs[0].Get("gameId"))
	assert.Equal(t, "auctionator", slugs[0].Get("slug"))
	assert.Equal(t, "1", slugs[0].Get("pageSize"))
	assert.False(t, slugs[0].Has("categoryId"), "the lookup omits the category so it finds the mod regardless")
	assert.False(t, slugs[0].Has("searchFilter"))

	assert.Equal(t, []string{"Auctionator", "Auction House Helper", "Auctionator Addon"}, names(res.Mods),
		"the looked-up hit is first, even under a non-relevance sort, and the API order follows")
	assert.Equal(t, "7", res.Mods[0].ID)
	assert.Equal(t, 100, res.TotalCount, "totals stay what the main search reported")
	assert.Equal(t, 20, res.PageSize)
}

func TestCurseForge_Search_SlugForMultiWordQuery(t *testing.T) {
	stub := &searchStub{main: []Mod{cfMod(1, "Other", 1)}, slugHits: []Mod{cfMod(7, "Just Enough Items", 5)}}
	cf := newSearchStubSource(t, stub)

	res, err := cf.Search(context.Background(), source.SearchQuery{GameID: "432", Query: "  Just  Enough_Items! ", PageSize: 20})
	require.NoError(t, err)
	require.Len(t, stub.slugQueries(), 1)
	assert.Equal(t, "just-enough-items", stub.slugQueries()[0].Get("slug"))
	assert.Equal(t, "Just Enough Items", res.Mods[0].Name)
}

func TestCurseForge_Search_SlugCollisionWithAnUnrelatedNameIsNotPrepended(t *testing.T) {
	stub := &searchStub{
		main:     []Mod{cfMod(1, "Something Else", 9)},
		slugHits: []Mod{cfMod(7, "Auctionator Reborn", 5)},
	}
	cf := newSearchStubSource(t, stub)

	res, err := cf.Search(context.Background(), source.SearchQuery{GameID: "432", Query: "auctionator", PageSize: 20})
	require.NoError(t, err)
	require.Len(t, stub.slugQueries(), 1)
	assert.Equal(t, []string{"Something Else"}, names(res.Mods))
}

func TestCurseForge_Search_SlugLookupFailureIsSilent(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			stub := &searchStub{main: []Mod{cfMod(1, "Something Else", 9)}, slugStatus: status}
			cf := newSearchStubSource(t, stub)

			res, err := cf.Search(context.Background(), source.SearchQuery{GameID: "432", Query: "auctionator", PageSize: 20})
			require.NoError(t, err)
			assert.Equal(t, []string{"Something Else"}, names(res.Mods))
			assert.Len(t, stub.slugQueries(), 1)
		})
	}
}

func TestCurseForge_Search_SlugLookupDecodeFailureIsSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Has("slug") {
			_, _ = w.Write([]byte(`{not json`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":1,"name":"Something Else"}],"pagination":{"index":0,"pageSize":20,"resultCount":1,"totalCount":1}}`))
	}))
	defer srv.Close()
	cf := New(srv.Client(), "test-api-key")
	cf.client.SetBaseURL(srv.URL)

	res, err := cf.Search(context.Background(), source.SearchQuery{GameID: "432", Query: "auctionator", PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, []string{"Something Else"}, names(res.Mods))
}

func TestCurseForge_Search_CancelledContextDuringLookupIsReturned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := &searchStub{
		main:       []Mod{cfMod(1, "Something Else", 9)},
		slugStatus: http.StatusInternalServerError,
		onSlug:     cancel,
	}
	cf := newSearchStubSource(t, stub)

	_, err := cf.Search(ctx, source.SearchQuery{GameID: "432", Query: "auctionator", PageSize: 20})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCurseForge_Search_NoLookupAfterThePageZero(t *testing.T) {
	stub := &searchStub{main: []Mod{cfMod(1, "Something Else", 9)}, slugHits: []Mod{cfMod(7, "Auctionator", 5)}}
	cf := newSearchStubSource(t, stub)

	res, err := cf.Search(context.Background(), source.SearchQuery{GameID: "432", Query: "auctionator", Page: 1, PageSize: 20})
	require.NoError(t, err)
	assert.Empty(t, stub.slugQueries())
	assert.Equal(t, []string{"Something Else"}, names(res.Mods))
}

func TestCurseForge_Search_NoLookupWhenTheSlugIsEmpty(t *testing.T) {
	for _, q := range []string{"!!!", "", "   "} {
		stub := &searchStub{main: []Mod{cfMod(1, "Something Else", 9)}, slugHits: []Mod{cfMod(7, "", 5)}}
		cf := newSearchStubSource(t, stub)

		res, err := cf.Search(context.Background(), source.SearchQuery{GameID: "432", Query: q, PageSize: 20})
		require.NoError(t, err)
		assert.Empty(t, stub.slugQueries(), "query %q", q)
		assert.Len(t, res.Mods, 1)
	}
}

func TestCurseForge_Search_LookupHitAlreadyOnThePageIsNotDuplicated(t *testing.T) {
	// The page's own name differs from the hit's (a rename between the two
	// calls), but the id is the same: never two copies of one mod.
	stub := &searchStub{
		main:     []Mod{cfMod(7, "Auctionator (old name)", 9)},
		slugHits: []Mod{cfMod(7, "Auctionator", 5)},
	}
	cf := newSearchStubSource(t, stub)

	res, err := cf.Search(context.Background(), source.SearchQuery{GameID: "432", Query: "auctionator", PageSize: 20})
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	assert.Equal(t, "7", res.Mods[0].ID)
}

func TestNameMatchesQuery(t *testing.T) {
	assert.True(t, nameMatchesQuery("Auctionator", " auctionator "))
	assert.True(t, nameMatchesQuery("Just Enough Items (JEI)", "just-enough-items-jei"))
	assert.False(t, nameMatchesQuery("Auctionator Plus", "auctionator"))
	assert.False(t, nameMatchesQuery("!!!", "???"), "folding to nothing never matches")
	assert.False(t, nameMatchesQuery("", ""))
}

func TestSlugForQuery(t *testing.T) {
	assert.Equal(t, "auctionator", slugForQuery("Auctionator"))
	assert.Equal(t, "just-enough-items", slugForQuery("  Just  Enough_Items! "))
	assert.Equal(t, "a-b-c", slugForQuery("--a..b//c--"))
	assert.Empty(t, slugForQuery("!!!"))
	assert.NotContains(t, slugForQuery("x y"), " ")
}
