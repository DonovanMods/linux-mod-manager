package core

// collection_search.go is the collection search (#651): finding a game's
// collections on a source that lists them (NexusMods), for `lmm search
// --collections` and the web search page's Collections tab. A hit's URL is
// the reference `lmm profile import --nexus-collection` takes, so a result
// imports through the ordinary collection import, unchanged.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// DefaultCollectionPageSize is how many collections a search returns when
// the caller names no page size.
const DefaultCollectionPageSize = 20

// ErrCollectionSearchUnsupported is a collection search no source of the
// game can answer: the named source is not registered or lists no
// collections, the game has no such source, or its id on the source is not
// configured. It is the
// caller's input (IsCollectionSearchUnsupported), not a failure.
var ErrCollectionSearchUnsupported = errors.New("no collection search")

// IsCollectionSearchUnsupported reports whether err is
// ErrCollectionSearchUnsupported: a classifier for a frontend that may
// import no more than core.
func IsCollectionSearchUnsupported(err error) bool {
	return errors.Is(err, ErrCollectionSearchUnsupported)
}

// CollectionSearchOptions narrows a SearchCollections. SourceID empty means
// the game's one source that lists collections. Sort is the mod search's
// vocabulary (domain.SearchSorts; empty is relevance), validated before
// the source is asked. Page is zero-based, and PageSize 0 or less is
// DefaultCollectionPageSize.
type CollectionSearchOptions struct {
	SourceID string
	Sort     domain.SearchSort
	Page     int
	PageSize int
}

// CollectionHit is one collection a search found. URL is its page on the
// source and the reference an import takes (`lmm profile import
// --nexus-collection <url>`). Revision, ModCount, SizeBytes and Adult
// describe the latest published revision, which that import resolves.
// Adult is the source's adult-content flag: hits are not filtered on it,
// as the mod search does not filter, so a frontend says it instead.
type CollectionHit struct {
	SourceID     string    `json:"source_id"`
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Author       string    `json:"author,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	URL          string    `json:"url"`
	Endorsements int       `json:"endorsements"`
	Downloads    int       `json:"downloads"`
	Revision     int       `json:"revision"`
	ModCount     int       `json:"mod_count"`
	SizeBytes    int64     `json:"size_bytes"`
	Adult        bool      `json:"adult"`
	UpdatedAt    time.Time `json:"updated_at,omitzero"`
}

// CollectionSearchReport is everything `lmm search --collections` renders
// and GET /api/v1/search/collections answers: one page of hits in the
// source's order, and what the frontends need to page through the rest.
//
//   - SourceID is the source that was searched.
//   - TotalResults is the source's own count of every match, across every
//     page, not len(Collections).
//   - Sort is the order the hits are in (relevance when none was asked
//     for), and SortsAvailable the sorts worth offering: every sort, since
//     a collection source orders by each server-side.
//   - Page and PageSize are the page answered and its size, the default
//     applied; HasMore is whether a page after this one holds matches.
type CollectionSearchReport struct {
	GameID         string              `json:"game_id"`
	SourceID       string              `json:"source_id"`
	Query          string              `json:"query"`
	Collections    []CollectionHit     `json:"collections"`
	TotalResults   int                 `json:"total_results"`
	Sort           domain.SearchSort   `json:"sort"`
	SortsAvailable []domain.SearchSort `json:"sorts_available"`
	Page           int                 `json:"page"`
	PageSize       int                 `json:"page_size"`
	HasMore        bool                `json:"has_more"`
}

// SearchCollections searches the game's collections on the source that
// lists them for query (empty lists them all, in the sort's order). It
// writes nothing and takes no operation slot: a read, like Search.
//
// The source is asked on ctx, so its waits - a throttle, a suspension -
// reach whatever notice observer the caller installed (WithSourceNotices),
// as a mod search's do. A refusal before the source is asked is
// ErrCollectionSearchUnsupported or an invalid sort (IsInvalidSearchSort);
// the source's own failure is returned as it is.
func (s *Service) SearchCollections(ctx context.Context, game *domain.Game, query string, opts CollectionSearchOptions) (*CollectionSearchReport, error) {
	sortBy, err := domain.ParseSearchSort(string(opts.Sort))
	if err != nil {
		return nil, err
	}
	sourceID, searcher, err := s.collectionSearcher(game, opts.SourceID)
	if err != nil {
		return nil, err
	}
	size := opts.PageSize
	if size <= 0 {
		size = DefaultCollectionPageSize
	}
	page := max(opts.Page, 0)
	result, err := searcher.SearchGameCollections(ctx, game.SourceIDs[sourceID], source.CollectionSearchQuery{
		Query: query, Sort: sortBy, Page: page, PageSize: size,
	})
	if err != nil {
		return nil, err
	}

	report := &CollectionSearchReport{
		GameID: game.ID, SourceID: sourceID, Query: query,
		Collections:  make([]CollectionHit, len(result.Collections)),
		TotalResults: result.Total,
		Sort:         sortBy, SortsAvailable: domain.SearchSorts(),
		Page: page, PageSize: size,
		HasMore: (page+1)*size < result.Total,
	}
	for i, c := range result.Collections {
		report.Collections[i] = CollectionHit{
			SourceID: sourceID, ID: c.ID, Name: c.Name, Author: c.Author, Summary: c.Summary, URL: c.URL,
			Endorsements: c.Endorsements, Downloads: c.Downloads, Revision: c.Revision, ModCount: c.ModCount,
			SizeBytes: c.SizeBytes, Adult: c.Adult, UpdatedAt: c.UpdatedAt,
		}
	}
	return report, nil
}

// collectionSearcher resolves which source a collection search asks: the
// one named, or else the game's sources that list collections, in id
// order - the first of them (NexusMods is the only one today).
func (s *Service) collectionSearcher(game *domain.Game, sourceID string) (string, source.GameCollectionSearcher, error) {
	if sourceID != "" {
		src, err := s.GetSource(sourceID)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %w", ErrCollectionSearchUnsupported, err)
		}
		searcher, ok := src.(source.GameCollectionSearcher)
		if !ok {
			return "", nil, fmt.Errorf("%w: %s has no collections lmm can search", ErrCollectionSearchUnsupported, s.sourceDisplayName(sourceID))
		}
		if game.SourceIDs[sourceID] == "" {
			return "", nil, fmt.Errorf("%w: game %s has no %s id (games.yaml source_ids: %s), so lmm cannot search its collections there",
				ErrCollectionSearchUnsupported, game.ID, sourceID, sourceID)
		}
		return sourceID, searcher, nil
	}
	ids := make([]string, 0, len(game.SourceIDs))
	for id, gameSourceID := range game.SourceIDs {
		if gameSourceID != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		src, err := s.GetSource(id)
		if err != nil {
			continue
		}
		if searcher, ok := src.(source.GameCollectionSearcher); ok {
			return id, searcher, nil
		}
	}
	return "", nil, fmt.Errorf("%w: none of %s's sources lists collections lmm can search (NexusMods does)", ErrCollectionSearchUnsupported, game.Name)
}
