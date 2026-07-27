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

	page, err := provider.Search(ctx, "nexusmods", "frost", 0)
	require.NoError(t, err)
	require.Equal(t, "frost", page.Query)
	require.Equal(t, "nexusmods", page.Source)
	require.Len(t, page.Results, 1)
	require.Equal(t, "Frostfall", page.Results[0].Name)
	require.Equal(t, "frostfall", page.Results[0].ID, "ModItem.ID must carry the canned search result's stable ID")
	require.Equal(t, 1, page.TotalCount)

	all, err := provider.Search(ctx, "nexusmods", "", 0)
	require.NoError(t, err)
	require.Len(t, all.Results, len(prototype.Load().SearchResults), "empty query returns everything")

	none, err := provider.Search(ctx, "nexusmods", "zzz-nothing", 0)
	require.NoError(t, err)
	require.Empty(t, none.Results, "no match returns an empty page")
	require.Equal(t, 0, none.TotalCount)
}

func TestPrototypeProviderSearchAllSources(t *testing.T) {
	t.Parallel()

	p := NewPrototypeProvider()
	page, err := p.Search(context.Background(), "", "", 0)
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

	all, err := p.Search(context.Background(), "", "sky", 0)
	require.NoError(t, err)
	require.NotEmpty(t, all.Warnings, "all-sources demo mode must exercise the warning line")

	single, err := p.Search(context.Background(), "nexusmods", "sky", 0)
	require.NoError(t, err)
	require.Empty(t, single.Warnings, "single-source search has no warnings to show")
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
