package tui

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

func TestPrototypeProviderMirrorsFakeData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	provider := NewPrototypeProvider()
	data := prototype.Load()

	summary, err := provider.Summary(ctx)
	require.NoError(t, err)
	require.Equal(t, data.Game.Name, summary.GameName)
	require.Equal(t, data.Profile.Name, summary.ProfileName)
	require.Equal(t, data.Stats.Installed, summary.Installed)
	require.Equal(t, data.Stats.Enabled, summary.Enabled)
	require.Equal(t, data.Stats.Updates, summary.Updates)
	require.Equal(t, data.Stats.Conflicts, summary.Conflicts)

	mods, err := provider.InstalledMods(ctx)
	require.NoError(t, err)
	require.Len(t, mods, len(data.InstalledMods))
	require.Equal(t, data.InstalledMods[0].Name, mods[0].Name)
	require.Equal(t, data.InstalledMods[0].Status, mods[0].Status)

	results, err := provider.SearchResults(ctx)
	require.NoError(t, err)
	require.Len(t, results, len(data.SearchResults))

	profiles, err := provider.Profiles(ctx)
	require.NoError(t, err)
	require.Len(t, profiles, len(data.Profiles))
	require.Equal(t, data.Profiles[0].Name, profiles[0].Name)
	require.True(t, profiles[0].Active)
}
