package prototype

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadProvidesConsistentDemoData(t *testing.T) {
	t.Parallel()

	data := Load()

	require.NotEmpty(t, data.InstalledMods)
	require.NotEmpty(t, data.SearchResults)
	require.NotEmpty(t, data.Profiles)

	active := 0
	for _, p := range data.Profiles {
		if p.Active {
			active++
			require.Equal(t, data.Profile.Name, p.Name, "active roster entry must match the current profile")
		}
	}
	require.Equal(t, 1, active, "exactly one profile should be active")

	require.Equal(t, data.Stats.Installed, data.Profile.ModCount, "dashboard stats should agree with the profile mod count")
}
