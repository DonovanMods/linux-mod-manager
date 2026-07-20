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

// TestLoadAssignsStableUniqueModIDs guards the invented demo IDs every
// canned Mod must carry so (Source, ID) can address it for ActionProvider
// calls, mirroring how a real domain.Mod's ID addresses it. IDs must be
// non-empty, unique within each list, and identical across repeated Load()
// calls (no randomness) so the prototype demo and its tests are
// deterministic.
func TestLoadAssignsStableUniqueModIDs(t *testing.T) {
	t.Parallel()

	first := Load()
	second := Load()

	for _, list := range [][]Mod{first.InstalledMods, first.SearchResults} {
		seen := make(map[string]bool, len(list))
		for _, mod := range list {
			require.NotEmpty(t, mod.ID, "mod %q must have a stable ID", mod.Name)
			require.False(t, seen[mod.ID], "mod ID %q must be unique within its list", mod.ID)
			seen[mod.ID] = true
		}
	}

	for i := range first.InstalledMods {
		require.Equal(t, first.InstalledMods[i].ID, second.InstalledMods[i].ID, "IDs must be stable across Load() calls")
	}
	for i := range first.SearchResults {
		require.Equal(t, first.SearchResults[i].ID, second.SearchResults[i].ID, "IDs must be stable across Load() calls")
	}
}
