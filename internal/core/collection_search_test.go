package core_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// searchingCollectionSource is a source whose collections can be searched
// (#651): it records what it was asked and answers a canned page.
type searchingCollectionSource struct {
	*mockSource
	result  source.CollectionSearchResult
	err     error
	gotGame string
	gotQ    source.CollectionSearchQuery
	calls   int
	// notice, when set, is raised on the call's context before answering:
	// how a throttled source says it is waiting.
	notice *source.Notice
}

func (s *searchingCollectionSource) SearchGameCollections(ctx context.Context, gameSourceID string, q source.CollectionSearchQuery) (source.CollectionSearchResult, error) {
	s.calls++
	s.gotGame, s.gotQ = gameSourceID, q
	if s.notice != nil {
		source.Notify(ctx, *s.notice)
	}
	return s.result, s.err
}

func newCollectionSearchFixture(t *testing.T) (*core.Service, *domain.Game, *searchingCollectionSource) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	src := &searchingCollectionSource{mockSource: newMockSource("nexusmods")}
	svc.RegisterSource(src)
	svc.RegisterSource(newMockSource("curseforge"))
	game := &domain.Game{ID: "stardew", Name: "Stardew Valley", ModPath: t.TempDir(),
		SourceIDs: map[string]string{"nexusmods": "stardewvalley", "curseforge": "669"}}
	return svc, game, src
}

var collectionSearchUpdated = time.Date(2026, 11, 16, 8, 49, 3, 0, time.UTC)

func TestSearchCollections_AsksTheGamesCollectionSource(t *testing.T) {
	svc, game, src := newCollectionSearchFixture(t)
	src.result = source.CollectionSearchResult{Total: 45, Collections: []source.CollectionSummary{
		{ID: "tckf0m", URL: "https://www.nexusmods.com/games/stardewvalley/collections/tckf0m", Name: "Very Expanded",
			Author: "curator", Summary: "A summary.", Endorsements: 4893, Downloads: 767891, Revision: 114,
			ModCount: 105, SizeBytes: 464703554, UpdatedAt: collectionSearchUpdated},
		{ID: "9cnwku", URL: "https://www.nexusmods.com/games/stardewvalley/collections/9cnwku", Name: "Adult One", Revision: 4, Adult: true},
	}}

	report, err := svc.SearchCollections(context.Background(), game, "expanded",
		core.CollectionSearchOptions{Sort: domain.SortPopular, Page: 1, PageSize: 2})
	require.NoError(t, err)

	assert.Equal(t, "stardewvalley", src.gotGame, "asked for the game's own id on the source")
	assert.Equal(t, source.CollectionSearchQuery{Query: "expanded", Sort: domain.SortPopular, Page: 1, PageSize: 2}, src.gotQ)

	assert.Equal(t, "stardew", report.GameID)
	assert.Equal(t, "nexusmods", report.SourceID, "the one source of this game that lists collections")
	assert.Equal(t, "expanded", report.Query)
	assert.Equal(t, 45, report.TotalResults)
	assert.Equal(t, domain.SortPopular, report.Sort)
	assert.Equal(t, 1, report.Page)
	assert.Equal(t, 2, report.PageSize)
	assert.True(t, report.HasMore, "pages 0 and 1 hold 4 of 45")
	require.Len(t, report.Collections, 2)
	assert.Equal(t, core.CollectionHit{
		SourceID: "nexusmods", ID: "tckf0m", URL: "https://www.nexusmods.com/games/stardewvalley/collections/tckf0m",
		Name: "Very Expanded", Author: "curator", Summary: "A summary.", Endorsements: 4893, Downloads: 767891,
		Revision: 114, ModCount: 105, SizeBytes: 464703554, UpdatedAt: collectionSearchUpdated,
	}, report.Collections[0])
	assert.True(t, report.Collections[1].Adult)
	assert.Equal(t, domain.SearchSorts(), report.SortsAvailable, "every sort orders collections")
}

func TestSearchCollections_PagingDefaultsAndTheLastPage(t *testing.T) {
	svc, game, src := newCollectionSearchFixture(t)
	src.result = source.CollectionSearchResult{Total: 3, Collections: []source.CollectionSummary{{ID: "a"}, {ID: "b"}, {ID: "c"}}}

	report, err := svc.SearchCollections(context.Background(), game, "", core.CollectionSearchOptions{})
	require.NoError(t, err)
	assert.Equal(t, core.DefaultCollectionPageSize, src.gotQ.PageSize, "no page size asks for the default, said once here")
	assert.Equal(t, core.DefaultCollectionPageSize, report.PageSize)
	assert.Equal(t, domain.SortRelevance, report.Sort)
	assert.False(t, report.HasMore, "3 of 3 is every match")
	assert.Equal(t, "", src.gotQ.Query, "no query lists the game's collections")

	src.result = source.CollectionSearchResult{Total: 0}
	report, err = svc.SearchCollections(context.Background(), game, "zzz", core.CollectionSearchOptions{})
	require.NoError(t, err)
	assert.NotNil(t, report.Collections, "an empty result is [] on the wire, not null")
	assert.Empty(t, report.Collections)
}

func TestSearchCollections_Refusals(t *testing.T) {
	svc, game, src := newCollectionSearchFixture(t)
	ctx := context.Background()

	_, err := svc.SearchCollections(ctx, game, "x", core.CollectionSearchOptions{Sort: "newest"})
	require.Error(t, err)
	assert.True(t, core.IsInvalidSearchSort(err))

	_, err = svc.SearchCollections(ctx, game, "x", core.CollectionSearchOptions{SourceID: "curseforge"})
	require.Error(t, err)
	assert.True(t, core.IsCollectionSearchUnsupported(err), "a source with no collection search: %v", err)
	assert.Contains(t, err.Error(), "Mock Source")

	_, err = svc.SearchCollections(ctx, game, "x", core.CollectionSearchOptions{SourceID: "nope"})
	require.Error(t, err)
	assert.True(t, core.IsCollectionSearchUnsupported(err), "an unknown source is the caller's input")

	other := &domain.Game{ID: "cf-only", Name: "CF Only", SourceIDs: map[string]string{"curseforge": "1"}}
	_, err = svc.SearchCollections(ctx, other, "x", core.CollectionSearchOptions{})
	require.Error(t, err)
	assert.True(t, core.IsCollectionSearchUnsupported(err))
	assert.Contains(t, err.Error(), "CF Only")

	unmapped := &domain.Game{ID: "bare", Name: "Bare", SourceIDs: map[string]string{"nexusmods": ""}}
	_, err = svc.SearchCollections(ctx, unmapped, "x", core.CollectionSearchOptions{SourceID: "nexusmods"})
	require.Error(t, err)
	assert.True(t, core.IsCollectionSearchUnsupported(err), "a game with no id on the source cannot be searched there")

	assert.Zero(t, src.calls, "every refusal before the source is asked")

	src.err = errors.New("upstream down")
	_, err = svc.SearchCollections(ctx, game, "x", core.CollectionSearchOptions{})
	require.ErrorContains(t, err, "upstream down")
	assert.False(t, core.IsCollectionSearchUnsupported(err))
}

// The source's own waits reach the caller's notice observer, as a mod
// search's do (#436): the query runs on the caller's context.
func TestSearchCollections_SourceNoticesReachTheSink(t *testing.T) {
	svc, game, src := newCollectionSearchFixture(t)
	src.notice = &source.Notice{Kind: source.NoticeRetry, Source: "NexusMods", Reason: source.RetryRateLimited,
		Attempt: 1, MaxAttempts: 3, Wait: 2 * time.Second}

	var events []core.Event
	ctx := core.WithSourceNotices(context.Background(), func(e core.Event) { events = append(events, e) })
	_, err := svc.SearchCollections(ctx, game, "x", core.CollectionSearchOptions{})
	require.NoError(t, err)
	require.Len(t, events, 1)
	warning, ok := events[0].(core.WarningEvent)
	require.True(t, ok)
	assert.Equal(t, core.SourceRetrying, warning.Phase)
	assert.Contains(t, warning.Message, "Rate limited by NexusMods")
}

// #651 review: the request a search sends is bounded. A query is at most
// MaxCollectionQueryRunes runes, a page at most MaxCollectionPageSize hits
// (clamped, not refused: a CLI --limit 500 asks for "many"), and a page
// whose offset would pass MaxCollectionOffset is refused - so page*size
// can never overflow.
func TestSearchCollections_BoundsTheRequest(t *testing.T) {
	svc, game, src := newCollectionSearchFixture(t)
	ctx := context.Background()

	long := strings.Repeat("é", core.MaxCollectionQueryRunes+1)
	_, err := svc.SearchCollections(ctx, game, long, core.CollectionSearchOptions{})
	require.Error(t, err)
	assert.True(t, core.IsInvalidCollectionSearch(err), "%v", err)
	assert.Zero(t, src.calls, "refused before the source is asked")

	_, err = svc.SearchCollections(ctx, game, long[:len(long)-len("é")], core.CollectionSearchOptions{})
	require.NoError(t, err, "exactly the limit is fine, counted in runes")

	report, err := svc.SearchCollections(ctx, game, "x", core.CollectionSearchOptions{PageSize: 500})
	require.NoError(t, err)
	assert.Equal(t, core.MaxCollectionPageSize, src.gotQ.PageSize)
	assert.Equal(t, core.MaxCollectionPageSize, report.PageSize)

	calls := src.calls
	for _, page := range []int{core.MaxCollectionOffset/core.DefaultCollectionPageSize + 1, math.MaxInt} {
		_, err = svc.SearchCollections(ctx, game, "x", core.CollectionSearchOptions{Page: page})
		require.Error(t, err, "page %d", page)
		assert.True(t, core.IsInvalidCollectionSearch(err))
	}
	assert.Equal(t, calls, src.calls)
	_, err = svc.SearchCollections(ctx, game, "x", core.CollectionSearchOptions{Page: core.MaxCollectionOffset / core.DefaultCollectionPageSize})
	require.NoError(t, err, "the last page within the bound")
}
