package d2r

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// layoutOf runs NormalizeArchive and returns every member's destination
// ("" for a dropped one), with the Layout's warnings.
func layoutOf(t *testing.T, members ...string) (map[string]string, []string) {
	t.Helper()
	l, err := New().NormalizeArchive(adapter.NormalizeRequest{Game: &domain.Game{ID: "diablo2resurrected"}, Members: members})
	require.NoError(t, err)
	out := make(map[string]string, len(members))
	for _, m := range members {
		dest, ok := l.Rewrite(m)
		if !ok {
			dest = ""
		}
		out[m] = dest
	}
	return out, l.Warnings
}

// Every shape a D2R mod archive comes in lands as <name>/<name>.mpq/ under
// the game's mods folder: already rooted there, wrapped in a mods/ folder,
// or the bare <name>.mpq folder. Files beside the .mpq folder stay in the
// mod's own folder.
func TestNormalizeArchive_MpqLayouts(t *testing.T) {
	for name, tt := range map[string]struct {
		members []string
		want    map[string]string
	}{
		"already <name>/<name>.mpq": {
			members: []string{"Loot/Loot.mpq/data/global/excel/armor.txt", "Loot/Loot.mpq/modinfo.json"},
			want:    map[string]string{"Loot/Loot.mpq/data/global/excel/armor.txt": "Loot/Loot.mpq/data/global/excel/armor.txt", "Loot/Loot.mpq/modinfo.json": "Loot/Loot.mpq/modinfo.json"},
		},
		"wrapped in mods/": {
			members: []string{"mods/Loot/Loot.mpq/modinfo.json", "mods/Loot/readme.txt"},
			want:    map[string]string{"mods/Loot/Loot.mpq/modinfo.json": "Loot/Loot.mpq/modinfo.json", "mods/Loot/readme.txt": "Loot/readme.txt"},
		},
		"bare <name>.mpq, readme at the root": {
			members: []string{"Loot.mpq/modinfo.json", "Loot.mpq/data/x.txt", "README.md"},
			want:    map[string]string{"Loot.mpq/modinfo.json": "Loot/Loot.mpq/modinfo.json", "Loot.mpq/data/x.txt": "Loot/Loot.mpq/data/x.txt", "README.md": "Loot/README.md"},
		},
		"deeper wrapper, upper-case extension": {
			members: []string{"Loot v2/mods/Loot/Loot.MPQ/modinfo.json"},
			want:    map[string]string{"Loot v2/mods/Loot/Loot.MPQ/modinfo.json": "Loot/Loot.MPQ/modinfo.json"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, warnings := layoutOf(t, tt.members...)
			assert.Equal(t, tt.want, got)
			assert.Empty(t, warnings)
		})
	}
}

// An archive with no <name>.mpq folder, or with two, is left exactly as
// packaged, with a warning: the adapter warns, it never guesses.
func TestNormalizeArchive_LeavesWhatItCannotPlace(t *testing.T) {
	got, warnings := layoutOf(t, "data/global/excel/armor.txt")
	assert.Equal(t, map[string]string{"data/global/excel/armor.txt": "data/global/excel/armor.txt"}, got)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], ".mpq")

	got, warnings = layoutOf(t, "A/A.mpq/modinfo.json", "B/B.mpq/modinfo.json")
	assert.Equal(t, "A/A.mpq/modinfo.json", got["A/A.mpq/modinfo.json"])
	assert.Equal(t, "B/B.mpq/modinfo.json", got["B/B.mpq/modinfo.json"])
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "A")
	assert.Contains(t, warnings[0], "B")

	// A packed .mpq FILE is not the folder D2R's -mod reads.
	_, warnings = layoutOf(t, "Loot.mpq")
	assert.Len(t, warnings, 1)
}

// The launch advice names the one -mod D2R can load: the enabled mod last
// in load order (the one that wins), and says when several compete.
func TestLaunchAdvice(t *testing.T) {
	g := &domain.Game{ID: "diablo2resurrected", Name: "Diablo II: Resurrected"}
	a := New()

	assert.Empty(t, adapter.LaunchAdvice(a, g, nil), "no mod, nothing to say")
	assert.Empty(t, adapter.LaunchAdvice(a, g, []adapter.DeployedMod{{Name: "Readme", Files: []string{"notes.txt"}}}))

	advice := adapter.LaunchAdvice(a, g, []adapter.DeployedMod{
		{Name: "Loot Filter", Files: []string{"Loot/Loot.mpq/modinfo.json", "Loot/Loot.mpq/data/x.txt"}},
	})
	require.Len(t, advice, 1)
	assert.Contains(t, advice[0], "-mod Loot -txt")
	assert.Contains(t, advice[0], "Additional command line arguments")

	advice = adapter.LaunchAdvice(a, g, []adapter.DeployedMod{
		{Name: "Loot Filter", Files: []string{"Loot/Loot.mpq/modinfo.json"}},
		{Name: "Stash Tabs", Files: []string{"Stash/Stash.mpq/modinfo.json"}},
	})
	require.Len(t, advice, 2)
	assert.Contains(t, advice[0], "-mod Stash -txt", "the last in load order wins")
	assert.Contains(t, advice[1], "Loot Filter")
	assert.Contains(t, advice[1], "Stash Tabs")
	assert.Contains(t, advice[1], "one mod at a time")

	// A generic game has no advice.
	assert.Empty(t, adapter.LaunchAdvice(adapter.Generic{}, g, []adapter.DeployedMod{{Name: "x", Files: []string{"Loot/Loot.mpq/a"}}}))
}
