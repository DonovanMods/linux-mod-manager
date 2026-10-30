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

// A mod whose name has a space can never be loaded: Battle.net splits its
// "Additional command line arguments" on whitespace, so `-mod Loot v2`
// names a mod "Loot". The archive is refused at ingest, naming the rename
// that fixes it, rather than deployed to do nothing.
func TestNormalizeArchive_RefusesANameWithASpace(t *testing.T) {
	for _, members := range [][]string{
		{"Loot v2/Loot v2.mpq/modinfo.json"},
		{"Loot v2.mpq/modinfo.json"},
		{"mods/Loot\tv2/Loot\tv2.mpq/modinfo.json"},
	} {
		_, err := New().NormalizeArchive(adapter.NormalizeRequest{Game: &domain.Game{ID: "diablo2resurrected"}, Members: members})
		require.Error(t, err, "%v", members)
		assert.Contains(t, err.Error(), "space")
		assert.Contains(t, err.Error(), "lmm import")
	}
	// A space in a wrapper folder above the .mpq is not the mod's name.
	_, err := New().NormalizeArchive(adapter.NormalizeRequest{Game: &domain.Game{ID: "diablo2resurrected"}, Members: []string{"Loot v2/mods/Loot/Loot.mpq/modinfo.json"}})
	assert.NoError(t, err)
}

// adviceOf is the d2r adapter's advice for mods, after previous.
func adviceOf(previous string, mods ...adapter.DeployedMod) adapter.Advice {
	return adapter.LaunchAdvice(New(), &domain.Game{ID: "diablo2resurrected", Name: "Diablo II: Resurrected"}, mods, previous)
}

// The launch advice names the one -mod D2R can load: the enabled mod last
// in load order (the one that wins), and says when several compete.
func TestLaunchAdvice(t *testing.T) {
	g := &domain.Game{ID: "diablo2resurrected", Name: "Diablo II: Resurrected"}

	assert.Empty(t, adviceOf(""), "no mod, nothing to say")
	assert.Empty(t, adviceOf("", adapter.DeployedMod{Name: "Readme", Files: []string{"notes.txt"}}))

	advice := adviceOf("", adapter.DeployedMod{Name: "Loot Filter", Files: []string{"Loot/Loot.mpq/modinfo.json", "Loot/Loot.mpq/data/x.txt"}})
	assert.Equal(t, "-mod Loot -txt", advice.Args)
	require.Len(t, advice.Lines, 1)
	assert.Contains(t, advice.Lines[0], "-mod Loot -txt")
	assert.Contains(t, advice.Lines[0], "Additional command line arguments")

	advice = adviceOf("",
		adapter.DeployedMod{Name: "Loot Filter", Files: []string{"Loot/Loot.mpq/modinfo.json"}},
		adapter.DeployedMod{Name: "Stash Tabs", Files: []string{"Stash/Stash.mpq/modinfo.json"}},
	)
	assert.Equal(t, "-mod Stash -txt", advice.Args, "the last in load order wins")
	require.Len(t, advice.Lines, 2)
	assert.Contains(t, advice.Lines[0], "-mod Stash -txt")
	assert.Contains(t, advice.Lines[1], "Loot Filter")
	assert.Contains(t, advice.Lines[1], "Stash Tabs")
	assert.Contains(t, advice.Lines[1], "one mod at a time")

	// A generic game has no advice.
	assert.Empty(t, adapter.LaunchAdvice(adapter.Generic{}, g, []adapter.DeployedMod{{Name: "x", Files: []string{"Loot/Loot.mpq/a"}}}, "-mod x -txt"))
}

// Two mods that carry the same <name>.mpq deploy into one folder, merged
// under the conflict rules: D2R loads them together, so the advice says
// that rather than that only the last one loads.
func TestLaunchAdvice_SameFolderMerges(t *testing.T) {
	advice := adviceOf("",
		adapter.DeployedMod{Name: "Loot Filter", Files: []string{"Loot/Loot.mpq/modinfo.json"}},
		adapter.DeployedMod{Name: "Loot Colours", Files: []string{"Loot/Loot.mpq/data/x.txt"}},
	)
	assert.Equal(t, "-mod Loot -txt", advice.Args)
	require.Len(t, advice.Lines, 2)
	assert.Contains(t, advice.Lines[1], "Loot Filter")
	assert.Contains(t, advice.Lines[1], "Loot Colours")
	assert.Contains(t, advice.Lines[1], "mods/Loot/")
	assert.Contains(t, advice.Lines[1], "merge")
	assert.Contains(t, advice.Lines[1], "load order")
	assert.NotContains(t, advice.Lines[1], "one mod at a time")

	// Merged and competing at once: the merged pair is what loads, the
	// other folder is what does not.
	advice = adviceOf("",
		adapter.DeployedMod{Name: "Stash Tabs", Files: []string{"Stash/Stash.mpq/modinfo.json"}},
		adapter.DeployedMod{Name: "Loot Filter", Files: []string{"Loot/Loot.mpq/modinfo.json"}},
		adapter.DeployedMod{Name: "Loot Colours", Files: []string{"Loot/Loot.mpq/data/x.txt"}},
	)
	assert.Equal(t, "-mod Loot -txt", advice.Args)
	require.Len(t, advice.Lines, 3)
	assert.Contains(t, advice.Lines[1], "merge")
	assert.Contains(t, advice.Lines[2], "Stash Tabs")
	assert.Contains(t, advice.Lines[2], "one mod at a time")
}

// The last deploy advised a -mod and now no D2R mod is enabled: one line
// says to take it back out of Battle.net's settings.
func TestLaunchAdvice_NoModAfterOne(t *testing.T) {
	advice := adviceOf("-mod Loot -txt")
	assert.Empty(t, advice.Args)
	require.Len(t, advice.Lines, 1)
	assert.Contains(t, advice.Lines[0], "remove")
	assert.Contains(t, advice.Lines[0], "-mod Loot -txt")
	assert.Contains(t, advice.Lines[0], "Additional command line arguments")
}
