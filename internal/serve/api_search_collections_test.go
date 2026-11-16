package serve_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectionSearchingSource is a NexusMods double whose collections can be
// searched (#651): it records the query and answers a canned page.
type collectionSearchingSource struct {
	*fakeSource
	result  source.CollectionSearchResult
	err     error
	gotGame string
	gotQ    source.CollectionSearchQuery
	calls   int
}

func (s *collectionSearchingSource) SearchGameCollections(_ context.Context, gameSourceID string, q source.CollectionSearchQuery) (source.CollectionSearchResult, error) {
	s.calls++
	s.gotGame, s.gotQ = gameSourceID, q
	return s.result, s.err
}

func newCollectionSearchServer(t *testing.T) (*serve.Server, *core.Service, *domain.Game, *collectionSearchingSource) {
	t.Helper()
	src := &collectionSearchingSource{fakeSource: newFakeSource("nexusmods"), result: source.CollectionSearchResult{
		Total: 41, Collections: []source.CollectionSummary{
			{ID: "abcdef", URL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef", Name: "Three Mods",
				Author: "curator", Endorsements: 12, Downloads: 340, Revision: 3, ModCount: 3, SizeBytes: 1024},
			{ID: "ghijkl", URL: "https://www.nexusmods.com/games/skyrimspecialedition/collections/ghijkl", Name: "Grown Up", Revision: 1, Adult: true},
		}}}
	svc, game := newFixtureServiceWithSource(t, src)
	game.SourceIDs["nexusmods"] = "skyrimspecialedition"
	require.NoError(t, svc.SaveGame(context.Background(), game))
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	return srv, svc, game, src
}

func getCollectionSearch(srv *serve.Server, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/search/collections"+query, nil))
	return rec
}

// The endpoint answers exactly the document `lmm search --collections
// --json` emits for the same search, with ?q=, ?sort=, ?page=, ?page_size=
// and ?source= as the CLI's query, --sort, paging and --source.
func TestServer_APISearchCollections_ReturnsTheReport(t *testing.T) {
	srv, svc, game, src := newCollectionSearchServer(t)

	rec := getCollectionSearch(srv, "?q=three+mods&sort=popular&page=1&page_size=2&source=nexusmods")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var report core.CollectionSearchReport
	decodeStrict(t, rec.Body.Bytes(), &report)
	assert.Equal(t, "skyrimspecialedition", src.gotGame)
	assert.Equal(t, source.CollectionSearchQuery{Query: "three mods", Sort: domain.SortPopular, Page: 1, PageSize: 2}, src.gotQ)
	require.Len(t, report.Collections, 2)
	assert.Equal(t, "https://www.nexusmods.com/games/skyrimspecialedition/collections/abcdef", report.Collections[0].URL)
	assert.True(t, report.Collections[1].Adult)
	assert.True(t, report.HasMore)

	want, err := svc.SearchCollections(context.Background(), game, "three mods",
		core.CollectionSearchOptions{SourceID: "nexusmods", Sort: domain.SortPopular, Page: 1, PageSize: 2})
	require.NoError(t, err)
	requireEncodesLike(t, rec.Body.Bytes(), want)
}

// No ?q= lists the game's collections (the CLI's query is optional there
// too), at the default page size.
func TestServer_APISearchCollections_NoQueryLists(t *testing.T) {
	srv, _, _, src := newCollectionSearchServer(t)

	rec := getCollectionSearch(srv, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "", src.gotQ.Query)
	assert.Equal(t, core.DefaultCollectionPageSize, src.gotQ.PageSize)
}

func TestServer_APISearchCollections_Refusals(t *testing.T) {
	srv, _, _, src := newCollectionSearchServer(t)

	for _, tc := range []struct {
		name, query string
		status      int
	}{
		{"bad sort", "?q=x&sort=newest", http.StatusBadRequest},
		{"bad page", "?q=x&page=-1", http.StatusBadRequest},
		{"bad page size", "?q=x&page_size=abc", http.StatusBadRequest},
		{"an unknown source", "?q=x&source=nope", http.StatusBadRequest},
		// #651 review: a query over core.MaxCollectionQueryRunes, and a
		// page past core.MaxCollectionOffset, are the caller's input.
		{"a query too long", "?q=" + strings.Repeat("a", core.MaxCollectionQueryRunes+1), http.StatusBadRequest},
		{"a page too deep", "?q=x&page=" + strconv.Itoa(core.MaxCollectionOffset), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := getCollectionSearch(srv, tc.query)
			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
		})
	}
	assert.Zero(t, src.calls, "every refusal before the source is asked")

	src.err = errors.New("upstream down")
	rec := getCollectionSearch(srv, "?q=x")
	assert.Equal(t, http.StatusBadGateway, rec.Code, "the source failing is the upstream's failure")
	assert.Contains(t, rec.Body.String(), "upstream down")
}

// A game none of whose sources lists collections is the caller's input:
// 400, with core's sentence.
func TestServer_APISearchCollections_GameWithoutCollections(t *testing.T) {
	svc, _ := newFixtureServiceWithSource(t, newFakeSource("fake"))
	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})

	rec := getCollectionSearch(srv, "?q=x")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "lists collections lmm can search")
}
