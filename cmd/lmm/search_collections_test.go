package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/spf13/cobra"
)

// collectionSearchSource is a NexusMods double that lists collections
// (#651): goldenSearchSource's ModSource, plus a canned collection page.
type collectionSearchSource struct {
	*goldenSearchSource
	result  source.CollectionSearchResult
	gotGame string
	gotQ    source.CollectionSearchQuery
}

func (s *collectionSearchSource) ID() string   { return "nexusmods" }
func (s *collectionSearchSource) Name() string { return "NexusMods" }
func (s *collectionSearchSource) SearchGameCollections(_ context.Context, gameSourceID string, q source.CollectionSearchQuery) (source.CollectionSearchResult, error) {
	s.gotGame, s.gotQ = gameSourceID, q
	return s.result, nil
}

var collectionSearchPage = source.CollectionSearchResult{Total: 155, Collections: []source.CollectionSummary{
	{ID: "tckf0m", URL: "https://www.nexusmods.com/games/stardewvalley/collections/tckf0m", Name: "Stardew Valley VERY Expanded",
		Author: "curator", Summary: "A curated list.", Endorsements: 4893, Downloads: 767891, Revision: 114, ModCount: 105,
		SizeBytes: 464703554, UpdatedAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)},
	{ID: "9cnwku", URL: "https://www.nexusmods.com/games/stardewvalley/collections/9cnwku", Name: "Expanded Xtardew Valley",
		Author: "other", Endorsements: 72, Revision: 4, ModCount: 38, SizeBytes: 288269282, Adult: true},
}}

// withCollectionSearchFlags sets `lmm search --collections`'s flags for one
// test, restoring them after.
func withCollectionSearchFlags(t *testing.T, sourceID string, limit int, sort string) {
	t.Helper()
	withSearchFlags(t, sourceID, limit)
	origCollections, origSort := searchCollections, searchSort
	t.Cleanup(func() { searchCollections, searchSort = origCollections, origSort })
	searchCollections, searchSort = true, sort
}

func newCollectionSearchService(t *testing.T) (*core.Service, *domain.Game, *collectionSearchSource) {
	t.Helper()
	svc, game := setupDoDeployTest(t)
	src := &collectionSearchSource{goldenSearchSource: &goldenSearchSource{}, result: collectionSearchPage}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"nexusmods": "stardewvalley"}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game, src
}

func TestDoSearchCollections_PrintsWhatAnImportNeeds(t *testing.T) {
	svc, game, src := newCollectionSearchService(t)
	withCollectionSearchFlags(t, "", 2, "popular")

	out := captureStdout(t, func() error {
		return doSearch(context.Background(), svc, game, []string{"very", "expanded"})
	})

	assert.Equal(t, "stardewvalley", src.gotGame)
	assert.Equal(t, source.CollectionSearchQuery{Query: "very expanded", Sort: domain.SortPopular, PageSize: 2}, src.gotQ,
		"the words are one query, --limit is the page size, --sort is forwarded")
	for _, want := range []string{
		"NAME", "AUTHOR", "ENDORSEMENTS", "MODS", "REVISION", "URL",
		"Stardew Valley VERY Expanded", "curator", "4893", "105", "114",
		"https://www.nexusmods.com/games/stardewvalley/collections/tckf0m",
		"[adult]",
		"2 of 155 collections",
		"lmm profile import --nexus-collection <url>",
	} {
		assert.Contains(t, out, want)
	}
}

func TestDoSearchCollections_Empty(t *testing.T) {
	svc, game, src := newCollectionSearchService(t)
	src.result = source.CollectionSearchResult{}
	withCollectionSearchFlags(t, "", 10, "")

	out := captureStdout(t, func() error {
		return doSearch(context.Background(), svc, game, nil)
	})
	assert.Equal(t, "", src.gotQ.Query, "no query lists the game's collections")
	assert.Contains(t, out, "No collections found.")
}

func TestDoSearchCollections_RefusesAGameWithoutCollections(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	svc.RegisterSource(&goldenSearchSource{})
	game.SourceIDs = map[string]string{"src": game.ID}
	withCollectionSearchFlags(t, "", 10, "")

	err := doSearch(context.Background(), svc, game, []string{"x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lists collections lmm can search")
}

// A query is optional with --collections (it lists the game's collections)
// and required without, as before.
func TestSearchArgs_QueryOptionalOnlyForCollections(t *testing.T) {
	orig := searchCollections
	t.Cleanup(func() { searchCollections = orig })

	searchCollections = false
	require.Error(t, searchCmd.Args(searchCmd, nil))
	require.NoError(t, searchCmd.Args(searchCmd, []string{"x"}))

	searchCollections = true
	require.NoError(t, searchCmd.Args(searchCmd, nil))
}

// The mod search's own filters mean nothing to a collection search, so
// they are refused beside --collections rather than ignored.
func TestSearchCollections_RefusesModOnlyFlags(t *testing.T) {
	for _, flag := range []string{"category", "tag", "refresh", "profile"} {
		t.Run(flag, func(t *testing.T) {
			cmd := newSearchCmdForTest(t)
			value := "x"
			if flag == "refresh" {
				value = "true"
			}
			require.NoError(t, cmd.Flags().Set("collections", "true"))
			require.NoError(t, cmd.Flags().Set(flag, value))
			err := cmd.ValidateFlagGroups()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "collections")
		})
	}
}

func TestJSONGolden_SearchCollections(t *testing.T) {
	svc, game, _ := newCollectionSearchService(t)
	withCollectionSearchFlags(t, "", 2, "popular")
	withJSONOutput(t)

	out := captureStdout(t, func() error {
		return doSearch(context.Background(), svc, game, []string{"expanded"})
	})
	assertJSONCLIGolden(t, "search_collections", out)
}

// newSearchCmdForTest is a command with `lmm search`'s --collections and
// the mod-only flags, unbound from the package's flag variables, grouped by
// the same markCollectionsExclusive the real command uses.
func newSearchCmdForTest(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "search"}
	cmd.Flags().Bool("collections", false, "")
	cmd.Flags().String("category", "", "")
	cmd.Flags().StringSlice("tag", nil, "")
	cmd.Flags().Bool("refresh", false, "")
	cmd.Flags().StringP("profile", "p", "", "")
	markCollectionsExclusive(cmd)
	return cmd
}

// hostileText is source text that must never reach the terminal raw: an OSC
// title sequence, a CSI colour, a tab and a newline (which would break the
// table's columns and rows) and U+202E (which reverses what follows).
const hostileText = "Evil\x1b]0;pwned\x07 \x1b[31mred\tcol\nrow‮esrever"

// assertSanitizedTable holds a printed table to SanitizeLine: no control or
// bidi rune survives, and the table has exactly the rows it should.
func assertSanitizedTable(t *testing.T, out string, rows int) {
	t.Helper()
	for _, bad := range []string{"\x1b", "\x07", "‮", "\t"} {
		assert.NotContains(t, out, bad)
	}
	table := strings.SplitN(out, "\n\n", 2)[0]
	assert.Len(t, strings.Split(strings.TrimRight(table, "\n"), "\n"), 2+rows, "header, separator and one line per row:\n%s", out)
	assert.Contains(t, out, truncate(domain.SanitizeLine(hostileText), 20)[:10], "the name is kept, made safe")
}

// #651 review: a collection's name, author and URL are NexusMods' text,
// printed into a terminal table.
func TestDoSearchCollections_SanitizesSourceText(t *testing.T) {
	svc, game, src := newCollectionSearchService(t)
	src.result = source.CollectionSearchResult{Total: 1, Collections: []source.CollectionSummary{
		{ID: "abcdef", Name: hostileText, Author: hostileText, URL: "https://www.nexusmods.com/x\x1b[2J", Revision: 1, Adult: true},
	}}
	withCollectionSearchFlags(t, "", 10, "")

	out := captureStdout(t, func() error {
		return doSearch(context.Background(), svc, game, []string{"x"})
	})
	assertSanitizedTable(t, out, 1)
	assert.Contains(t, out, "[adult]")
}

// The mod search's table had the same gap: a mod's id, name, author,
// version and source all come from the source.
func TestDoSearch_SanitizesSourceText(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	svc.RegisterSource(&goldenSearchSource{mods: []domain.Mod{
		{ID: "a\x1b[2J", SourceID: "src", Name: hostileText, Version: "1.0‮", Author: hostileText, GameID: game.ID},
	}})
	game.SourceIDs = map[string]string{"src": game.ID}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	withSearchFlags(t, "", 10)

	out := captureStdout(t, func() error {
		return doSearch(context.Background(), svc, game, []string{"evil"})
	})
	assertSanitizedTable(t, out, 1)
}
