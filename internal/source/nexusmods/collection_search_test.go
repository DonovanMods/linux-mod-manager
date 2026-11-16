package nexusmods

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #651: searching a game's collections. testdata/collections/
// search-stardewvalley-expanded.json is a REAL collectionsV2 answer
// (recorded anonymously 2026-11-16, see its _provenance), with author
// names and summaries replaced; testdata/collection-search-schema.json is
// the schema it was asked against.

var _ source.GameCollectionSearcher = (*NexusMods)(nil)

// searchServer answers every collectionsV2 request with fixture, recording
// each request's variables (and whether it carried the key).
func searchServer(t *testing.T, fixture string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "collections", fixture))
	require.NoError(t, err)
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, collectionSearchQuery, req.Query)
		seen = append(seen, req.Variables)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func newSearchSource(t *testing.T, fixture string) (*NexusMods, *[]map[string]any) {
	t.Helper()
	srv, seen := searchServer(t, fixture)
	nm := New(nil, "testapikey")
	nm.client.graphqlURL = srv.URL
	return nm, seen
}

func TestSearchGameCollections_RecordedPage(t *testing.T) {
	nm, seen := newSearchSource(t, "search-stardewvalley-expanded.json")

	got, err := nm.SearchGameCollections(context.Background(), "stardewvalley",
		source.CollectionSearchQuery{Query: "expanded", Sort: domain.SortPopular, PageSize: 3})
	require.NoError(t, err)

	require.Len(t, *seen, 1)
	assert.Equal(t, map[string]any{
		"filter": map[string]any{
			"gameDomain":           []any{map[string]any{"value": "stardewvalley", "op": "EQUALS"}},
			"name":                 []any{map[string]any{"value": "expanded", "op": "WILDCARD"}},
			"hasPublishedRevision": []any{map[string]any{"value": true, "op": "EQUALS"}},
		},
		"sort":   []any{map[string]any{"endorsements": map[string]any{"direction": "DESC"}}},
		"count":  float64(3),
		"offset": float64(0),
	}, (*seen)[0])

	assert.Equal(t, 155, got.Total, "the source's own count of every match")
	require.Len(t, got.Collections, 3)
	first := got.Collections[0]
	assert.Equal(t, source.CollectionSummary{
		ID:           "tckf0m",
		URL:          "https://www.nexusmods.com/games/stardewvalley/collections/tckf0m",
		Name:         "Stardew Valley VERY Expanded",
		Author:       "curator1",
		Summary:      "A recorded summary, replaced (tckf0m).",
		Endorsements: 4893,
		Downloads:    767891,
		Revision:     114,
		ModCount:     105,
		SizeBytes:    464703554,
		UpdatedAt:    time.Date(2026, 11, 16, 8, 49, 3, 0, time.UTC),
	}, first)
	assert.False(t, got.Collections[1].Adult)
	assert.True(t, got.Collections[2].Adult, "the adult flag is carried, not filtered: the mod search's policy")
	assert.Equal(t, "9cnwku", got.Collections[2].ID)
}

// Every sort the mod search offers maps onto CollectionsSearchSort, and
// relevance sends none at all (the mod search's rule: the upstream's own
// order).
func TestSearchGameCollections_SortsAndPaging(t *testing.T) {
	cases := []struct {
		sort domain.SearchSort
		want any
	}{
		{domain.SortRelevance, nil},
		{"", nil},
		{domain.SortUpdated, []any{map[string]any{"updatedAt": map[string]any{"direction": "DESC"}}}},
		{domain.SortDownloads, []any{map[string]any{"downloads": map[string]any{"direction": "DESC"}}}},
		{domain.SortPopular, []any{map[string]any{"endorsements": map[string]any{"direction": "DESC"}}}},
	}
	for _, tc := range cases {
		t.Run(string(tc.sort), func(t *testing.T) {
			nm, seen := newSearchSource(t, "search-stardewvalley-expanded.json")
			_, err := nm.SearchGameCollections(context.Background(), "stardewvalley",
				source.CollectionSearchQuery{Query: "x", Sort: tc.sort, Page: 2, PageSize: 10})
			require.NoError(t, err)
			require.Len(t, *seen, 1)
			assert.Equal(t, tc.want, (*seen)[0]["sort"])
			assert.Equal(t, float64(20), (*seen)[0]["offset"], "page 2 of 10 starts at the 21st")
			assert.Equal(t, float64(10), (*seen)[0]["count"])
		})
	}
}

// No query lists the game's collections: the name filter is left out
// rather than sent empty. No page size asks for the source's default.
func TestSearchGameCollections_EmptyQueryAndDefaultPageSize(t *testing.T) {
	nm, seen := newSearchSource(t, "search-stardewvalley-expanded.json")
	_, err := nm.SearchGameCollections(context.Background(), "stardewvalley", source.CollectionSearchQuery{Query: "  "})
	require.NoError(t, err)
	require.Len(t, *seen, 1)
	filter := (*seen)[0]["filter"].(map[string]any)
	_, hasName := filter["name"]
	assert.False(t, hasName)
	assert.Equal(t, float64(defaultCollectionPageSize), (*seen)[0]["count"])
}

func TestSearchGameCollections_Refusals(t *testing.T) {
	nm, seen := newSearchSource(t, "search-stardewvalley-expanded.json")
	ctx := context.Background()

	_, err := nm.SearchGameCollections(ctx, "", source.CollectionSearchQuery{Query: "x"})
	require.ErrorIs(t, err, source.ErrGameIdentifierInvalid)
	_, err = nm.SearchGameCollections(ctx, "Bad Domain", source.CollectionSearchQuery{Query: "x"})
	require.ErrorIs(t, err, source.ErrGameIdentifierInvalid)
	_, err = nm.SearchGameCollections(ctx, "stardewvalley", source.CollectionSearchQuery{Query: "x", Sort: "newest"})
	require.ErrorIs(t, err, domain.ErrInvalidSearchSort)
	assert.Empty(t, *seen, "refused before any request")
}

// A GraphQL error arrives with HTTP 200 and is the search's error; a hit
// with no published revision, or for another game, is not offered (it
// could not be imported here).
func TestSearchGameCollections_ErrorsAndUnimportableHits(t *testing.T) {
	answer := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	nm := New(nil, "")
	nm.client.graphqlURL = srv.URL
	ctx := context.Background()

	answer = `{"errors":[{"message":"Field 'nope' is not defined"}],"data":null}`
	_, err := nm.SearchGameCollections(ctx, "stardewvalley", source.CollectionSearchQuery{Query: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Field 'nope' is not defined")

	answer = `{"data":{"collectionsV2":{"totalCount":3,"nodes":[
		{"slug":"aaaaaa","name":"Unpublished","game":{"domainName":"stardewvalley"},"user":{"name":"a"},"latestPublishedRevision":null},
		{"slug":"bbbbbb","name":"Elsewhere","game":{"domainName":"skyrim"},"user":{"name":"b"},"latestPublishedRevision":{"revisionNumber":1,"modCount":1,"totalSize":"1","adultContent":false}},
		{"slug":"cccccc","name":"Kept","game":{"domainName":"stardewvalley"},"user":null,"latestPublishedRevision":{"revisionNumber":2,"modCount":3,"totalSize":"4","adultContent":false}}
	]}}}`
	got, err := nm.SearchGameCollections(ctx, "stardewvalley", source.CollectionSearchQuery{})
	require.NoError(t, err)
	require.Len(t, got.Collections, 1)
	assert.Equal(t, "cccccc", got.Collections[0].ID)
	assert.Empty(t, got.Collections[0].Author, "no user is no author, not a crash")
	assert.Equal(t, int64(4), got.Collections[0].SizeBytes)
	assert.Equal(t, 3, got.Total, "the total is the source's, unfiltered")
}

// collectionSearchSchema is testdata/collection-search-schema.json: the
// recorded object types (collectionSchema's shape), plus the input
// objects and enums the variables are built from.
type collectionSearchSchema struct {
	collectionSchema
	Inputs map[string]map[string]string `json:"inputs"`
	Enums  map[string][]string          `json:"enums"`
}

func loadCollectionSearchSchema(t *testing.T) collectionSearchSchema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "collection-search-schema.json"))
	require.NoError(t, err)
	var schema collectionSearchSchema
	require.NoError(t, json.Unmarshal(data, &schema))
	require.NoError(t, json.Unmarshal(data, &schema.collectionSchema))
	return schema
}

// TestCollectionSearchQuery_MatchesRecordedSchema is
// TestCollectionRevisionQuery_MatchesRecordedSchema for the search: every
// selected field exists on its type, and every argument is one
// collectionsV2 takes, declared with the type it takes.
func TestCollectionSearchQuery_MatchesRecordedSchema(t *testing.T) {
	schema := loadCollectionSearchSchema(t)
	root, ok := schema.Query["collectionsV2"]
	require.True(t, ok, "the recording must hold Query.collectionsV2")

	toks := gqlTokens(collectionSearchQuery)
	// The operation header: query Name($var: Type, ...) {. A list type is
	// several tokens ("[", "Name!", "]"), joined back up here.
	vars := map[string]string{}
	i := 0
	for toks[i] != "(" {
		i++
	}
	for i++; toks[i] != ")"; {
		require.True(t, strings.HasPrefix(toks[i], "$"), "a variable, not %q", toks[i])
		name := toks[i]
		require.Equal(t, ":", toks[i+1])
		i += 2
		var typ strings.Builder
		for toks[i] != ")" && !strings.HasPrefix(toks[i], "$") {
			typ.WriteString(toks[i])
			i++
		}
		vars[name] = typ.String()
	}
	i++
	require.Equal(t, "{", toks[i])
	i++
	require.Equal(t, "collectionsV2", toks[i])
	i++
	require.Equal(t, "(", toks[i])
	for i++; toks[i] != ")"; i++ {
		arg := toks[i]
		want, ok := root.Args[arg]
		require.True(t, ok, "collectionsV2 takes no argument %q", arg)
		require.Equal(t, ":", toks[i+1])
		v := toks[i+2]
		require.Equal(t, want, vars[v], "argument %s is a %s; the query declares %s as %s", arg, want, v, vars[v])
		i += 2
	}
	i++
	walkSelection(t, &schema.collectionSchema, root.Type, toks, &i)
}

// TestCollectionSearchVariables_MatchRecordedSchema checks the filter and
// sort the client actually sends - every sort, with and without a query -
// against the recorded input objects: each filter member exists on
// CollectionsSearchFilter, each entry carries exactly the members its
// value type defines, each op is a FilterComparisonOperator, and each sort
// is a CollectionsSearchSort member with a SortDirection. The #337/#343
// lesson: a renamed filter member fails here, not in a user's search.
func TestCollectionSearchVariables_MatchRecordedSchema(t *testing.T) {
	schema := loadCollectionSearchSchema(t)
	filterType := schema.Inputs["CollectionsSearchFilter"]
	sortType := schema.Inputs["CollectionsSearchSort"]
	require.NotEmpty(t, filterType)
	require.NotEmpty(t, sortType)

	for _, sort := range domain.SearchSorts() {
		for _, query := range []string{"", "armor"} {
			vars, err := collectionSearchVariables("skyrimspecialedition", source.CollectionSearchQuery{Query: query, Sort: sort, PageSize: 5})
			require.NoError(t, err)
			data, err := json.Marshal(vars)
			require.NoError(t, err)
			var sent map[string]any
			require.NoError(t, json.Unmarshal(data, &sent))

			for member, entries := range sent["filter"].(map[string]any) {
				valueType, ok := filterType[member]
				require.True(t, ok, "CollectionsSearchFilter has no member %q", member)
				members := schema.Inputs[valueType]
				require.NotEmpty(t, members, "the recording has no input %s", valueType)
				for _, e := range entries.([]any) {
					entry := e.(map[string]any)
					assert.ElementsMatch(t, mapsKeys(members), mapsKeys(entry), "%s entry", member)
					assert.Contains(t, schema.Enums[members["op"]], entry["op"])
				}
			}
			if s, ok := sent["sort"]; ok {
				for _, e := range s.([]any) {
					for member, v := range e.(map[string]any) {
						valueType, ok := sortType[member]
						require.True(t, ok, "CollectionsSearchSort has no member %q", member)
						dir := v.(map[string]any)["direction"]
						assert.Contains(t, schema.Enums[schema.Inputs[valueType]["direction"]], dir)
					}
				}
			}
		}
	}
}

func mapsKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// #651 review: a hit's URL is only ever one ResolveGameCollection accepts.
// A node whose slug fails parseCollectionRef's own pattern is not offered:
// search must never hand out an address the import would refuse.
func TestSearchGameCollections_SkipsSlugsImportWouldRefuse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"collectionsV2":{"totalCount":4,"nodes":[
			{"slug":"../evil","name":"Traversal","game":{"domainName":"stardewvalley"},"latestPublishedRevision":{"revisionNumber":1,"modCount":1,"totalSize":"1","adultContent":false}},
			{"slug":"UPPER","name":"Upper","game":{"domainName":"stardewvalley"},"latestPublishedRevision":{"revisionNumber":1,"modCount":1,"totalSize":"1","adultContent":false}},
			{"slug":"a?b=c","name":"Query","game":{"domainName":"stardewvalley"},"latestPublishedRevision":{"revisionNumber":1,"modCount":1,"totalSize":"1","adultContent":false}},
			{"slug":"ok1234","name":"Fine","game":{"domainName":"stardewvalley"},"latestPublishedRevision":{"revisionNumber":1,"modCount":1,"totalSize":"1","adultContent":false}}
		]}}}`)
	}))
	t.Cleanup(srv.Close)
	nm := New(nil, "")
	nm.client.graphqlURL = srv.URL

	got, err := nm.SearchGameCollections(context.Background(), "stardewvalley", source.CollectionSearchQuery{})
	require.NoError(t, err)
	require.Len(t, got.Collections, 1)
	assert.Equal(t, "ok1234", got.Collections[0].ID)
	r, err := parseCollectionRef(got.Collections[0].URL)
	require.NoError(t, err, "the URL a hit carries parses as an import reference")
	assert.Equal(t, "ok1234", r.slug)
}

// The source clamps what it sends on its own, whatever its caller: at most
// maxCollectionPageSize per page, and an offset that cannot overflow.
func TestSearchGameCollections_ClampsTheRequest(t *testing.T) {
	vars, err := collectionSearchVariables("stardewvalley", source.CollectionSearchQuery{Page: math.MaxInt, PageSize: 10_000})
	require.NoError(t, err)
	assert.Equal(t, maxCollectionPageSize, vars["count"])
	assert.Equal(t, maxCollectionOffset, vars["offset"])
}
