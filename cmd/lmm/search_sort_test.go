package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withSearchSort restores the --sort global doSearch reads (#503).
func withSearchSort(t *testing.T, sort string) {
	t.Helper()
	orig := searchSort
	t.Cleanup(func() { searchSort = orig })
	searchSort = sort
}

func TestSearchCmd_SortFlag(t *testing.T) {
	flag := searchCmd.Flags().Lookup("sort")
	require.NotNil(t, flag, "lmm search --sort")
	assert.Equal(t, "relevance", flag.DefValue)
	for _, name := range []string{"relevance", "updated", "downloads", "popular"} {
		assert.Contains(t, flag.Usage, name)
	}
	assert.Contains(t, searchCmd.Long, "newest first", "the help says which way date sorts run")
	assert.Contains(t, searchCmd.Long, "exact", "the help says an exact name match always leads")
}

func TestSearchCmd_SortFlagCompletes(t *testing.T) {
	complete, ok := searchCmd.GetFlagCompletionFunc("sort")
	require.True(t, ok, "--sort has shell completion")
	got, directive := complete(searchCmd, nil, "")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	names := make([]string, len(got))
	for i, c := range got {
		names[i], _, _ = strings.Cut(c, "\t")
	}
	assert.Equal(t, []string{"relevance", "updated", "downloads", "popular"}, names)
	for _, c := range got {
		assert.Contains(t, c, "\t", "each candidate carries its description")
	}
}

func TestDoSearch_SortFlagReachesTheSource(t *testing.T) {
	for _, source := range []string{"", "spy-sort"} {
		spy := &pageSizeSpySource{id: "spy-sort"}
		svc, game := newPageSizeSpyService(t, spy)
		withSearchFlags(t, source, 10)
		withSearchSort(t, "downloads")

		require.NoError(t, doSearch(context.Background(), svc, game, []string{"query"}))
		assert.Equal(t, domain.SortDownloads, spy.gotSort, "--source=%q", source)
	}
}

func TestDoSearch_DefaultSortIsRelevance(t *testing.T) {
	spy := &pageSizeSpySource{id: "spy-sort"}
	svc, game := newPageSizeSpyService(t, spy)
	withSearchFlags(t, "", 10)
	withSearchSort(t, "relevance")

	require.NoError(t, doSearch(context.Background(), svc, game, []string{"query"}))
	assert.Equal(t, domain.SortRelevance, spy.gotSort)
}

func TestDoSearch_UnknownSortIsRefusedBeforeAnySourceIsAsked(t *testing.T) {
	spy := &pageSizeSpySource{id: "spy-sort"}
	svc, game := newPageSizeSpyService(t, spy)
	withSearchFlags(t, "", 10)
	withSearchSort(t, "newest")

	err := doSearch(context.Background(), svc, game, []string{"query"})
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidSearchSort)
	assert.Contains(t, err.Error(), `"newest"`)
	assert.Contains(t, err.Error(), "relevance, updated, downloads, popular")
	assert.Zero(t, spy.calls)
}

func TestJSONGolden_SearchSorted(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	likes := func(n int64) *int64 { return &n }
	svc.RegisterSource(&goldenSearchSource{mods: []domain.Mod{
		{ID: "old", SourceID: "src", Name: "Auctionator Classic", Version: "1.0", GameID: game.ID, Downloads: 900, UpdatedAt: day(2), Endorsements: likes(4)},
		{ID: "new", SourceID: "src", Name: "Auction Helper", Version: "2.0", GameID: game.ID, Downloads: 30, UpdatedAt: day(28), Endorsements: likes(70)},
		{ID: "exact", SourceID: "src", Name: "Auctionator", Version: "3.0", GameID: game.ID, Downloads: 5, UpdatedAt: day(1), Endorsements: likes(1)},
	}})
	game.SourceIDs = map[string]string{"src": game.ID}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	withSearchFlags(t, "", 10)
	withSearchSort(t, "updated")
	withJSONOutput(t)

	out := captureStdout(t, func() error {
		return doSearch(context.Background(), svc, game, []string{"auctionator"})
	})
	// The exact name leads, then newest first: exact, new, old.
	assertJSONCLIGolden(t, "search_sorted", out)
}
