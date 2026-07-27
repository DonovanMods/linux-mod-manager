package tui

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

func TestPrototypeProviderOverviewMirrorsFakeData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	provider := NewPrototypeProvider()
	data := prototype.Load()

	summary, mods, err := provider.Overview(ctx)
	require.NoError(t, err)
	require.Equal(t, data.Game.Name, summary.GameName)
	require.Equal(t, data.Stats.Installed, summary.Installed)
	require.Equal(t, data.Profile.Name, summary.ProfileName)
	require.Equal(t, data.Stats.Enabled, summary.Enabled)
	require.Equal(t, data.Stats.Updates, summary.Updates)
	require.Equal(t, data.Stats.Conflicts, summary.Conflicts)
	// data and provider each ran their OWN prototype.Load() call, and
	// Stats.LastDeploy is computed relative to time.Now() at that call (see
	// its own doc comment) - the two independent calls land a few
	// nanoseconds apart, so this compares within a generous tolerance rather
	// than requiring bit-for-bit equality.
	require.NotNil(t, summary.LastDeploy, "the primary game's canned Stats.LastDeploy must be reflected in Summary")
	assert.WithinDuration(t, data.Stats.LastDeploy, *summary.LastDeploy, time.Second)
	require.Len(t, mods, len(data.InstalledMods))
	require.Equal(t, data.InstalledMods[0].Name, mods[0].Name)
	require.Equal(t, data.InstalledMods[0].ID, mods[0].ID, "ModItem.ID must carry the canned mod's stable ID")
}

// TestPrototypeProviderModItemIDsStableAcrossCalls guards the "invent stable
// IDs" requirement: independently-constructed providers (and repeated calls
// on the same one) must expose identical, non-empty ModItem.ID values -
// (Source, ID) must deterministically address the same canned mod every
// time, not merely within a single provider instance.
func TestPrototypeProviderModItemIDsStableAcrossCalls(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	first := NewPrototypeProvider()
	second := NewPrototypeProvider()

	_, mods1, err := first.Overview(ctx)
	require.NoError(t, err)
	_, mods2, err := second.Overview(ctx)
	require.NoError(t, err)

	require.Len(t, mods1, len(mods2))
	for i := range mods1 {
		require.NotEmpty(t, mods1[i].ID)
		require.Equal(t, mods1[i].ID, mods2[i].ID, "IDs must be stable across independent provider instances")
	}
}

func TestPrototypeProviderSources(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"nexusmods"}, NewPrototypeProvider().Sources())
}

func TestPrototypeProviderSearchFiltersCannedResults(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	provider := NewPrototypeProvider()

	page, err := provider.Search(ctx, "nexusmods", "frost", 0, 0)
	require.NoError(t, err)
	require.Equal(t, "frost", page.Query)
	require.Equal(t, "nexusmods", page.Source)
	require.Len(t, page.Results, 1)
	require.Equal(t, "Frostfall", page.Results[0].Name)
	require.Equal(t, "frostfall", page.Results[0].ID, "ModItem.ID must carry the canned search result's stable ID")
	require.Equal(t, 1, page.TotalCount)

	all, err := provider.Search(ctx, "nexusmods", "", 0, 0)
	require.NoError(t, err)
	require.Len(t, all.Results, len(prototype.Load().SearchResults), "empty query returns everything")

	none, err := provider.Search(ctx, "nexusmods", "zzz-nothing", 0, 0)
	require.NoError(t, err)
	require.Empty(t, none.Results, "no match returns an empty page")
	require.Equal(t, 0, none.TotalCount)
}

func TestPrototypeProviderSearchAllSources(t *testing.T) {
	t.Parallel()

	p := NewPrototypeProvider()
	page, err := p.Search(context.Background(), "", "", 0, 0)
	require.NoError(t, err)
	assert.NotEmpty(t, page.Results)
	for _, item := range page.Results {
		assert.NotEmpty(t, item.Source)
	}
}

// TestPrototypeProviderSearchAllSourcesRendersWarning guards #58 item 4:
// prototypeProvider.Search never populated Warnings before this fix, so
// --prototype demo mode never exercised searchWarningLine's rendering path
// at all. Single-source search must stay warning-free (Warnings is
// documented as "only meaningful for all-sources searches").
func TestPrototypeProviderSearchAllSourcesRendersWarning(t *testing.T) {
	t.Parallel()

	p := NewPrototypeProvider()

	all, err := p.Search(context.Background(), "", "sky", 0, 0)
	require.NoError(t, err)
	require.NotEmpty(t, all.Warnings, "all-sources demo mode must exercise the warning line")

	single, err := p.Search(context.Background(), "nexusmods", "sky", 0, 0)
	require.NoError(t, err)
	require.Empty(t, single.Warnings, "single-source search has no warnings to show")
}

// TestPrototypeProviderSearchAllSourcesReportsExhaustedAndAttempted guards a
// whole-branch-review finding on #58: the canned all-sources page is a
// single, non-paginated fetch (there is no real "page 2" to demo), so it
// must report itself Exhausted like a genuinely-exhausted real aggregate
// would - otherwise --prototype's search screen shows a permanently dead
// "n next" hint (pre-#58, the old TotalCount/PageSize math happened to
// correctly end the canned set; #58's aggregate-aware hasNextPage broke
// that by trusting Exhausted instead, which prototypeProvider never set).
// AttemptedCount must likewise reflect the canned SEARCHABLE source count
// (len(SourceInfos()), all three of which advertise "search" - see
// SourceInfos' doc comment) rather than 0, or every zero-match demo search
// would falsely render the no-searchable-sources honesty notice (#58 item
// 3) instead of the ordinary "No archives matched" copy.
func TestPrototypeProviderSearchAllSourcesReportsExhaustedAndAttempted(t *testing.T) {
	t.Parallel()

	p := NewPrototypeProvider()

	all, err := p.Search(context.Background(), "", "sky", 0, 0)
	require.NoError(t, err)
	require.True(t, all.Exhausted, "the canned set is a single-page fetch; there is no real next page to demo")
	require.Equal(t, len(p.SourceInfos()), all.AttemptedCount, "must derive from the canned searchable-source count, not 0")

	none, err := p.Search(context.Background(), "", "zzz-nothing-matches", 0, 0)
	require.NoError(t, err)
	require.Empty(t, none.Results)
	require.NotZero(t, none.AttemptedCount, "a genuine zero-match demo search must not look like zero searchable sources")
}

// TestPrototypeProviderSearchPaginatesByGivenSize guards #111 Tier 1:
// prototypeProvider.Search must actually PAGINATE the canned SearchResults
// set by the given pageSize, rather than always returning every match on a
// nominal "page 0" (the pre-#111 behavior, which happened to look correct
// only because the old fixed SearchPageSize (10) exceeded the canned set's
// 5 entries). An empty query matches all 5 canned entries.
func TestPrototypeProviderSearchPaginatesByGivenSize(t *testing.T) {
	t.Parallel()

	p := NewPrototypeProvider()

	page0, err := p.Search(context.Background(), "nexusmods", "", 0, 2)
	require.NoError(t, err)
	require.Len(t, page0.Results, 2, "page 0 of a 2-sized page over 5 canned results")
	require.Equal(t, 5, page0.TotalCount, "TotalCount reports the full match count, not the page size")

	page2, err := p.Search(context.Background(), "nexusmods", "", 2, 2)
	require.NoError(t, err)
	require.Len(t, page2.Results, 1, "the last page holds only the remainder (5 - 2*2 = 1)")
}

// TestPrototypeProviderSearchFallsBackToSearchPageSizeWhenPageSizeNotPositive
// mirrors coreProvider's own fallback test: a pageSize <= 0 must produce
// SearchPageSize's worth of pagination, not a degenerate zero-sized (or
// unbounded) page.
func TestPrototypeProviderSearchFallsBackToSearchPageSizeWhenPageSizeNotPositive(t *testing.T) {
	t.Parallel()

	p := NewPrototypeProvider()
	page, err := p.Search(context.Background(), "nexusmods", "", 0, 0)
	require.NoError(t, err)
	require.Equal(t, SearchPageSize, page.PageSize, "pageSize <= 0 must fall back to SearchPageSize")
}

// TestPrototypeProviderAllSourcesExhaustedReflectsActualLastPage guards
// #111 Tier 1's other change to the all-sources canned demo: Exhausted used
// to be unconditionally true (a single, non-paginated fetch never had a
// real "page 2" to demo before this task) - now that pagination is real,
// Exhausted must genuinely reflect whether THIS page reached the end of the
// canned set, not just always claim so.
func TestPrototypeProviderAllSourcesExhaustedReflectsActualLastPage(t *testing.T) {
	t.Parallel()

	p := NewPrototypeProvider()

	page0, err := p.Search(context.Background(), "", "", 0, 2)
	require.NoError(t, err)
	require.False(t, page0.Exhausted, "more canned results remain after a 2-sized first page of 5")

	page2, err := p.Search(context.Background(), "", "", 2, 2)
	require.NoError(t, err)
	require.True(t, page2.Exhausted, "the last page (remainder) must report exhausted")
}

func TestPrototypeProviderProfiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	provider := NewPrototypeProvider()
	data := prototype.Load()

	profiles, err := provider.Profiles(ctx)
	require.NoError(t, err)
	require.Len(t, profiles, len(data.Profiles))
	require.Equal(t, data.Profiles[0].Name, profiles[0].Name)
	require.True(t, profiles[0].Active)
}
