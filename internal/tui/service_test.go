package tui

import (
	"context"
	"testing"

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
