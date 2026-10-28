package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #512: `lmm install <query>` shares `lmm search`'s ordering contract (#503)
// - the mod whose name IS the query leads, then --sort - and -y only
// auto-installs a hit that earned that lead.

func orderedFixtureMods() []domain.Mod {
	return []domain.Mod{
		{ID: "fuzzy", SourceID: "test-src", Name: "Auctionator Classic Skin", Version: "1.0", GameID: "g1", Downloads: 900},
		{ID: "exact", SourceID: "test-src", Name: "Auctionator", Version: "1.0", GameID: "g1", Downloads: 5},
		{ID: "other", SourceID: "test-src", Name: "Auction House Tools", Version: "1.0", GameID: "g1", Downloads: 50},
	}
}

func selectFor(t *testing.T, svc *core.Service, gameID, query string, sortBy domain.SearchSort) ([]*domain.Mod, error) {
	t.Helper()
	var mods []*domain.Mod
	var err error
	captureStdout(t, func() error {
		mods, err = searchAndSelectMods(context.Background(), svc, gameID, "test-src", query, "default", sortBy)
		return nil
	})
	return mods, err
}

func TestSearchAndSelectMods_ExactNameMatchLeadsThePicker(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	installYes = false
	src.searchResults = orderedFixtureMods()

	var mods []*domain.Mod
	var err error
	captureStdout(t, func() error {
		// An empty answer takes [1], the top of the listing.
		withStdin(t, "\n", func() {
			mods, err = searchAndSelectMods(context.Background(), svc, game.ID, "test-src", "auctionator", "default", domain.SortRelevance)
		})
		return nil
	})

	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, "exact", mods[0].ID, "[1] is the mod named like the query, not the source's first hit")
}

func TestSearchAndSelectMods_ForwardsSortToEveryPage(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	installYes = false
	src.searchResults = orderedFixtureMods()

	captureStdout(t, func() error {
		withStdin(t, "\n", func() {
			_, _ = searchAndSelectMods(context.Background(), svc, game.ID, "test-src", "auctionator", "default", domain.SortDownloads)
		})
		return nil
	})

	require.NotEmpty(t, src.searchSorts)
	for _, s := range src.searchSorts {
		assert.Equal(t, domain.SortDownloads, s)
	}
}

func TestSearchAndSelectMods_YesInstallsAnExactMatchEvenBehindOtherHits(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	installYes = true
	src.searchResults = orderedFixtureMods()

	mods, err := selectFor(t, svc, game.ID, "Auctionator", domain.SortRelevance)

	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, "exact", mods[0].ID)
}

func TestSearchAndSelectMods_YesWithNoExactMatchRefusesWithCandidates(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	installYes = true
	src.searchResults = orderedFixtureMods()

	mods, err := selectFor(t, svc, game.ID, "auction", domain.SortRelevance)

	assert.Nil(t, mods, "a guess is never installed")
	require.ErrorIs(t, err, core.ErrConfirmationRequired)
	var refusal *installNoExactMatchError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "auction", refusal.Query)
	assert.Equal(t, []installCandidate{
		{ID: "fuzzy", Name: "Auctionator Classic Skin"},
		{ID: "exact", Name: "Auctionator"},
		{ID: "other", Name: "Auction House Tools"},
	}, refusal.Candidates)
	assert.Contains(t, err.Error(), "--id")
	assert.Contains(t, err.Error(), "fuzzy", "the text names the candidates too")
}

func TestSearchAndSelectMods_YesRefusalCapsTheCandidateList(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	installYes = true
	src.searchResults = nil
	for i := 0; i < 14; i++ {
		src.searchResults = append(src.searchResults, domain.Mod{
			ID: fmt.Sprintf("m%02d", i), SourceID: "test-src", Name: fmt.Sprintf("Pack %02d", i), Version: "1.0", GameID: "g1",
		})
	}

	_, err := selectFor(t, svc, game.ID, "armor", domain.SortRelevance)

	var refusal *installNoExactMatchError
	require.ErrorAs(t, err, &refusal)
	assert.Len(t, refusal.Candidates, maxInstallCandidates)
	assert.Equal(t, "m00", refusal.Candidates[0].ID)
}

func TestSearchAndSelectMods_YesWithASingleHitInstallsItWhateverItIsCalled(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	installYes = true
	src.searchResults = []domain.Mod{
		{ID: "only", SourceID: "test-src", Name: "Totally Different Name", Version: "1.0", GameID: "g1"},
	}

	mods, err := selectFor(t, svc, game.ID, "query", domain.SortRelevance)

	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, "only", mods[0].ID)
}

func TestInstallCmd_SortFlagSharesSearchsCompletionAndHelp(t *testing.T) {
	f := installCmd.Flags().Lookup("sort")
	require.NotNil(t, f)
	assert.Equal(t, string(domain.SortRelevance), f.DefValue)
	assert.Equal(t, searchCmd.Flags().Lookup("sort").Usage, f.Usage, "one wording: date sorts are newest first")
	fn, ok := installCmd.GetFlagCompletionFunc("sort")
	require.True(t, ok, "--sort completes")
	got, _ := fn(installCmd, nil, "")
	want, _ := completeSearchSort(nil, nil, "")
	assert.Equal(t, want, got)
}

func TestDoInstall_UnknownSortIsRefusedBeforeAnySearch(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	installModID = ""
	src.searchResults = orderedFixtureMods()
	old := installSort
	t.Cleanup(func() { installSort = old })
	installSort = "newest"

	err := doInstall(context.Background(), svc, game, []string{"query"})

	require.ErrorIs(t, err, domain.ErrInvalidSearchSort)
	assert.Empty(t, src.searchSorts, "no source was asked")
}

func TestReportError_JSON_InstallNoExactMatchError(t *testing.T) {
	err := newInstallNoExactMatchError("auction", source.SearchResult{
		Mods: []domain.Mod{
			{ID: "fuzzy", Name: "Auctionator Classic Skin"},
			{ID: "other", Name: "Auction House Tools"},
		},
		TotalCount: 12,
	})
	withJSONOutput(t)

	out := captureStdout(t, func() error { reportError(err); return nil })

	assertJSONCLIGolden(t, "install_no_exact_match", out)
}
