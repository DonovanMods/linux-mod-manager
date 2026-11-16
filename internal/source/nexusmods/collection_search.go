package nexusmods

// collection_search.go is NexusMods' collection search (#651):
// SearchGameCollections, over GraphQL's collectionsV2, filtered to one
// game domain. Like the rest of the collection seam it needs no API key
// (collectionsV2 answers anonymously, verified live 2026-11-16); the key
// is sent when there is one, as every GraphQL call here does.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// collectionSearchQuery lists one page of a game's collections. Each hit
// is read with its latest published revision, the one an import without
// "@<revision>" resolves. testdata/collection-search-schema.json records
// what it is checked against (collection_search_test.go).
const collectionSearchQuery = `
query SearchCollections($filter: CollectionsSearchFilter, $sort: [CollectionsSearchSort!], $count: Int, $offset: Int) {
  collectionsV2(filter: $filter, sort: $sort, count: $count, offset: $offset) {
    totalCount
    nodes {
      slug
      name
      summary
      endorsements
      totalDownloads
      updatedAt
      game { domainName }
      user { name }
      latestPublishedRevision { revisionNumber modCount totalSize adultContent }
    }
  }
}`

// defaultCollectionPageSize is the page size when the caller names none,
// the mod search's own default.
const defaultCollectionPageSize = 20

// maxCollectionPageSize and maxCollectionOffset bound what the source
// sends whatever its caller asks (core bounds the request first, with the
// same numbers): a page of at most this many, starting no further in than
// this, so offset = page*size is never computed past it.
const (
	maxCollectionPageSize = 100
	maxCollectionOffset   = 1_000_000
)

// collectionSearchResponse is collectionSearchQuery's answer.
type collectionSearchResponse struct {
	Data struct {
		CollectionsV2 *struct {
			TotalCount int `json:"totalCount"`
			Nodes      []struct {
				Slug           string    `json:"slug"`
				Name           string    `json:"name"`
				Summary        string    `json:"summary"`
				Endorsements   int       `json:"endorsements"`
				TotalDownloads int       `json:"totalDownloads"`
				UpdatedAt      time.Time `json:"updatedAt"`
				Game           struct {
					DomainName string `json:"domainName"`
				} `json:"game"`
				User *struct {
					Name string `json:"name"`
				} `json:"user"`
				Latest *struct {
					RevisionNumber int `json:"revisionNumber"`
					ModCount       int `json:"modCount"`
					// TotalSize is a BigInt, which NexusMods serialises
					// as a JSON string.
					TotalSize    json.Number `json:"totalSize"`
					AdultContent bool        `json:"adultContent"`
				} `json:"latestPublishedRevision"`
			} `json:"nodes"`
		} `json:"collectionsV2"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// collectionsSortMember names the CollectionsSearchSort member a search
// sort orders by, or "" for relevance, which sends no sort: the mod
// search's rule (modsSortMember), leaving NexusMods' own order.
func collectionsSortMember(sort domain.SearchSort) string {
	switch sort {
	case domain.SortUpdated:
		return "updatedAt"
	case domain.SortDownloads:
		return "downloads"
	case domain.SortPopular:
		return "endorsements"
	}
	return ""
}

// collectionSearchVariables builds the query's variables. The filter's
// member names are a contract with CollectionsSearchFilter, checked offline
// against the recording (TestCollectionSearchVariables_MatchRecordedSchema).
//
// Adult content is not filtered, as the mod search does not filter it:
// NexusMods' default answer (adult collections included, verified live
// anonymously and with a key) is what is shown, and each hit carries its
// adult flag for a frontend to say so. hasPublishedRevision leaves out a
// collection that has never been published, which nothing could import.
func collectionSearchVariables(gameSourceID string, q source.CollectionSearchQuery) (map[string]any, error) {
	sort, err := domain.ParseSearchSort(string(q.Sort))
	if err != nil {
		return nil, err
	}
	size := q.PageSize
	if size <= 0 {
		size = defaultCollectionPageSize
	}
	size = min(size, maxCollectionPageSize)
	// Divided before it is multiplied, so a huge page cannot overflow.
	offset := min(max(q.Page, 0), maxCollectionOffset/size) * size
	filter := map[string]any{
		"gameDomain":           []map[string]any{{"value": gameSourceID, "op": "EQUALS"}},
		"hasPublishedRevision": []map[string]any{{"value": true, "op": "EQUALS"}},
	}
	if text := strings.TrimSpace(q.Query); text != "" {
		// The text goes to the WILDCARD filter unescaped, so a "*" or "?"
		// the user types passes through to it (WILDCARD's own pattern
		// characters; as typed is the search they asked for). Core bounds
		// the query's length (core.MaxCollectionQueryRunes).
		filter["name"] = []map[string]any{{"value": text, "op": "WILDCARD"}}
	}
	vars := map[string]any{"filter": filter, "count": size, "offset": offset}
	if member := collectionsSortMember(sort); member != "" {
		vars["sort"] = []map[string]any{{member: map[string]any{"direction": "DESC"}}}
	}
	return vars, nil
}

// SearchGameCollections implements source.GameCollectionSearcher: one page
// of the collections NexusMods lists for the game it knows as gameSourceID.
// Each hit's URL is its collection page, which ResolveGameCollection takes
// as a reference. A hit with no published revision, a slug the import
// would refuse, or one NexusMods files under another game, is left out (it
// could not be imported into this one); Total stays NexusMods' own count.
func (n *NexusMods) SearchGameCollections(ctx context.Context, gameSourceID string, q source.CollectionSearchQuery) (source.CollectionSearchResult, error) {
	if !domainPattern.MatchString(gameSourceID) {
		return source.CollectionSearchResult{}, fmt.Errorf("%w: NexusMods game %q", source.ErrGameIdentifierInvalid, gameSourceID)
	}
	vars, err := collectionSearchVariables(gameSourceID, q)
	if err != nil {
		return source.CollectionSearchResult{}, err
	}
	var resp collectionSearchResponse
	if err := n.client.graphql(ctx, collectionSearchQuery, vars, &resp); err != nil {
		return source.CollectionSearchResult{}, fmt.Errorf("searching NexusMods collections: %w", err)
	}
	if len(resp.Errors) > 0 {
		msgs := make([]string, len(resp.Errors))
		for i, e := range resp.Errors {
			msgs[i] = e.Message
		}
		return source.CollectionSearchResult{}, fmt.Errorf("searching NexusMods collections: GraphQL errors: %s", strings.Join(msgs, "; "))
	}
	page := resp.Data.CollectionsV2
	if page == nil {
		return source.CollectionSearchResult{Collections: []source.CollectionSummary{}}, nil
	}
	out := source.CollectionSearchResult{Total: page.TotalCount, Collections: make([]source.CollectionSummary, 0, len(page.Nodes))}
	for _, node := range page.Nodes {
		// A slug the import's own parser would refuse (parseCollectionRef's
		// slugPattern) is never turned into a URL: a hit is only ever an
		// address `lmm profile import --nexus-collection` accepts.
		if node.Latest == nil || !slugPattern.MatchString(node.Slug) {
			continue
		}
		if d := node.Game.DomainName; d != "" && d != gameSourceID {
			continue
		}
		hit := source.CollectionSummary{
			ID:           node.Slug,
			URL:          collectionPageURL(gameSourceID, node.Slug),
			Name:         node.Name,
			Summary:      node.Summary,
			Endorsements: node.Endorsements,
			Downloads:    node.TotalDownloads,
			Revision:     node.Latest.RevisionNumber,
			ModCount:     node.Latest.ModCount,
			Adult:        node.Latest.AdultContent,
			UpdatedAt:    node.UpdatedAt,
		}
		if node.User != nil {
			hit.Author = node.User.Name
		}
		if size, err := node.Latest.TotalSize.Int64(); err == nil {
			hit.SizeBytes = size
		}
		out.Collections = append(out.Collections, hit)
	}
	return out, nil
}
