package steamworkshop_test

import (
	"context"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func keyedSource(t *testing.T, baseURL string, now func() time.Time) *steamworkshop.Source {
	t.Helper()
	src := newTestSource(t, baseURL, t.TempDir(), now)
	src.SetAPIKey("k")
	return src
}

func TestSearch_MapsTheQueryOntoQueryFilesParameters(t *testing.T) {
	fx := serveRoutes(t, reply{file: "queryfiles_page1.json"})
	src := keyedSource(t, fx.srv.URL, nil)

	_, err := src.Search(context.Background(), source.SearchQuery{
		GameID:   "1133870",
		Query:    "cargo ship",
		Category: "Blueprint",
		Tags:     []string{"Ship", "Large Grid"},
		Page:     2,
		PageSize: 30,
	})
	require.NoError(t, err)

	q := fx.requests[0]
	assert.Equal(t, "1133870", q.Get("appid"))
	assert.Equal(t, "cargo ship", q.Get("search_text"))
	assert.Equal(t, "12", q.Get("query_type"), "a text query ranks by text search")
	assert.Equal(t, "3", q.Get("page"), "Page is 0-based, Steam's page is 1-based")
	assert.Equal(t, "30", q.Get("numperpage"))
	assert.Equal(t, "Ship", q.Get("requiredtags[0]"))
	assert.Equal(t, "Large Grid", q.Get("requiredtags[1]"))
	assert.Equal(t, "Blueprint", q.Get("requiredtags[2]"), "Category is one more required tag")
}

func TestSearch_AnEmptyQueryRanksByTrend(t *testing.T) {
	fx := serveRoutes(t, reply{file: "queryfiles_page1.json"})
	src := keyedSource(t, fx.srv.URL, nil)

	_, err := src.Search(context.Background(), source.SearchQuery{GameID: "1133870"})
	require.NoError(t, err)

	q := fx.requests[0]
	assert.Equal(t, "3", q.Get("query_type"))
	assert.Empty(t, q.Get("search_text"))
	assert.Equal(t, "20", q.Get("numperpage"), "the default page size")
	assert.Equal(t, "1", q.Get("page"))
}

func TestSearch_PageSizeIsCappedAtOneHundred(t *testing.T) {
	fx := serveRoutes(t, reply{file: "queryfiles_page1.json"})
	src := keyedSource(t, fx.srv.URL, nil)

	_, err := src.Search(context.Background(), source.SearchQuery{GameID: "1133870", PageSize: 5000})
	require.NoError(t, err)
	assert.Equal(t, "100", fx.requests[0].Get("numperpage"))
}

func TestSearch_MapsResultsExactlyAsGetModDoes(t *testing.T) {
	fx := serveRoutes(t, reply{file: "queryfiles_page1.json"})
	src := keyedSource(t, fx.srv.URL, nil)

	res, err := src.Search(context.Background(), source.SearchQuery{GameID: "1133870", Query: "ship", Page: 1, PageSize: 20})
	require.NoError(t, err)

	assert.Equal(t, 137, res.TotalCount, "TotalCount is response.total")
	assert.Equal(t, 1, res.Page, "Page echoes the request")
	assert.Equal(t, 20, res.PageSize)
	require.Len(t, res.Mods, 2)

	m := res.Mods[0]
	assert.Equal(t, "3617086610", m.ID)
	assert.Equal(t, "steamworkshop", m.SourceID)
	assert.Equal(t, "Sample Workshop Item", m.Name)
	assert.Equal(t, "1133870", m.GameID)
	assert.Equal(t, "76561198000000000", m.Author)
	assert.Equal(t, "7987119735124793734", m.Version)
	assert.Equal(t, "Blueprint", m.Category)
	assert.Equal(t, "An item subscribed in the Steam client.",
		m.Description, "QueryFiles calls it file_description; one mapping serves both endpoints")
	assert.Equal(t, "https://steamcommunity.com/sharedfiles/filedetails/?id=3617086610", m.SourceURL)
	assert.Equal(t, time.Unix(1764767935, 0).UTC(), m.UpdatedAt.UTC())
	assert.Equal(t, int64(5678), m.Downloads)
}

func TestSearch_CachesTheNormalisedQueryForFiveMinutes(t *testing.T) {
	fx := serveRoutes(t, reply{file: "queryfiles_page1.json"})
	clock := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	src := keyedSource(t, fx.srv.URL, func() time.Time { return clock })

	q := source.SearchQuery{GameID: "1133870", Query: "Cargo Ship ", Tags: []string{"Ship"}, PageSize: 20}
	_, err := src.Search(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, 1, fx.calls)

	// Same query, differently cased and spaced: the same normalised key.
	_, err = src.Search(context.Background(), source.SearchQuery{GameID: "1133870", Query: "cargo ship", Tags: []string{"Ship"}, PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, 1, fx.calls, "a repeat inside the TTL is served from memory")

	// A different page is a different query.
	_, err = src.Search(context.Background(), source.SearchQuery{GameID: "1133870", Query: "cargo ship", Tags: []string{"Ship"}, PageSize: 20, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, 2, fx.calls)

	clock = clock.Add(6 * time.Minute)
	_, err = src.Search(context.Background(), q)
	require.NoError(t, err)
	assert.Equal(t, 3, fx.calls, "past the 5-minute TTL it is asked again")
}

func TestSearch_ThrottlingIsRetriedWithTheServersRetryAfter(t *testing.T) {
	fx := serveRoutes(t,
		reply{status: 429, file: "queryfiles_403.json"},
		reply{file: "queryfiles_page1.json"},
	)
	src := keyedSource(t, fx.srv.URL, nil)

	res, err := src.Search(context.Background(), source.SearchQuery{GameID: "1133870", Query: "ship"})
	require.NoError(t, err, "the Tier-1 retry transport carries Tier-2 search too")
	assert.Len(t, res.Mods, 2)
	assert.Equal(t, 2, fx.calls)
}
