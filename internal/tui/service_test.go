package tui

import (
	"context"
	"testing"

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
	require.Equal(t, 1, page.TotalCount)

	all, err := provider.Search(ctx, "nexusmods", "", 0)
	require.NoError(t, err)
	require.Len(t, all.Results, len(prototype.Load().SearchResults), "empty query returns everything")

	none, err := provider.Search(ctx, "nexusmods", "zzz-nothing", 0)
	require.NoError(t, err)
	require.Empty(t, none.Results, "no match returns an empty page")
	require.Equal(t, 0, none.TotalCount)
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
