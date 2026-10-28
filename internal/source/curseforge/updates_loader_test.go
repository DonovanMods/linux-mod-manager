package curseforge

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CurseForge classifies a file on a second dimension besides the game-version
// type: the mod loader (FileIndex.ModLoader). A Minecraft mod publishes one
// file per loader per game version, so "newest file of my game version" alone
// can offer a Forge install a Fabric jar (#504's bug class). Names the loaders
// carry on File are CurseForge's own vocabulary.
const (
	typeMC120 = 75125 // a Minecraft version family, the gameVersionTypeId of 1.20.x
	typeMC121 = 77784
)

// loaderFile builds a latestFiles entry that names its loaders the way the API
// does: as entries of gameVersions and of sortableGameVersions.
func loaderFile(id int, display string, release int, date string, mcType int, loaders ...string) File {
	f := wowFile(id, display, release, date, mcType)
	f.GameVersions = []string{"1.20.1"}
	f.GameVersions = append(f.GameVersions, loaders...)
	f.GameVersions = append(f.GameVersions, "Client", "Server")
	for _, l := range loaders {
		f.SortableGameVersions = append(f.SortableGameVersions, SortableGameVersion{GameVersionName: l})
	}
	return f
}

// loaderIndex builds the latestFilesIndexes entry pointing at f for one
// (game-version type, loader) pair.
func loaderIndex(f File, mcType, loader int) FileIndex {
	idx := indexFor(f, mcType)
	idx.ModLoader = loader
	return idx
}

// minecraftMods is the shape of a real GetMods answer for a multi-loader
// Minecraft mod: several loaders x several game versions, latestFiles in no
// chronological order.
func minecraftMods() []Mod {
	neo121 := loaderFile(5000300, "fixturelib-neoforge-1.21-3.2.0", ReleaseTypeRelease, "2026-08-01T00:00:00Z", typeMC121, "NeoForge")
	forge120 := loaderFile(5000100, "fixturelib-forge-1.20.1-3.0.0", ReleaseTypeRelease, "2026-03-01T00:00:00Z", typeMC120, "Forge")
	fabric120 := loaderFile(5000200, "fixturelib-fabric-1.20.1-3.1.0", ReleaseTypeRelease, "2026-06-01T00:00:00Z", typeMC120, "Fabric")
	quilt120 := loaderFile(5000150, "fixturelib-quilt-1.20.1-3.0.5", ReleaseTypeRelease, "2026-04-01T00:00:00Z", typeMC120, "Quilt")
	return []Mod{{
		ID: 5001, Name: "Fixture Lib",
		LatestFiles: []File{neo121, forge120, fabric120, quilt120},
		LatestFilesIndexes: []FileIndex{
			loaderIndex(neo121, typeMC121, ModLoaderNeoForge),
			loaderIndex(forge120, typeMC120, ModLoaderForge),
			loaderIndex(fabric120, typeMC120, ModLoaderFabric),
			loaderIndex(quilt120, typeMC120, ModLoaderQuilt),
		},
	}}
}

func mcInstalled(version string, fileID int) []domain.InstalledMod {
	return []domain.InstalledMod{wowInstalled(5001, "Fixture Lib", version, fileID)}
}

// TestCheckUpdates_ForgeInstallIsNotOfferedAFabricFile: the newest 1.20 file
// is Fabric's, but a Forge install follows Forge. Its own file is still the
// newest Forge one, so there is nothing to offer and no lookup to spend.
func TestCheckUpdates_ForgeInstallIsNotOfferedAFabricFile(t *testing.T) {
	var batch, single atomic.Int32
	srv := flavorServer(t, minecraftMods(), nil, &batch, &single)
	defer srv.Close()

	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(), mcInstalled("3.0.0", 5000100))
	require.NoError(t, err)

	assert.Empty(t, updates, "a Fabric/Quilt file of the same game version is not an update to a Forge install")
	assert.Zero(t, single.Load())
}

// TestCheckUpdates_SupersededForgeInstallResolvesItsLoaderViaGetModFile: the
// installed Forge file is in neither list, so its loader comes from the one
// GetModFile the check already makes - and the offer is the newer FORGE file,
// not the (newer still) Fabric one.
func TestCheckUpdates_SupersededForgeInstallResolvesItsLoaderViaGetModFile(t *testing.T) {
	forges := map[string]File{
		"named in gameVersions only": func() File {
			f := loaderFile(5000050, "fixturelib-forge-1.20.1-2.9.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Forge")
			f.SortableGameVersions = f.SortableGameVersions[:1] // type id only, no loader name
			return f
		}(),
		"named in sortableGameVersions only": func() File {
			f := loaderFile(5000050, "fixturelib-forge-1.20.1-2.9.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Forge")
			f.GameVersions = []string{"1.20.1"}
			return f
		}(),
		"named by CurseForge's own casing": func() File {
			f := loaderFile(5000050, "fixturelib-forge-1.20.1-2.9.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "forge")
			return f
		}(),
	}
	for name, installedFile := range forges {
		t.Run(name, func(t *testing.T) {
			var batch, single atomic.Int32
			srv := flavorServer(t, minecraftMods(), map[int]File{5000050: installedFile}, &batch, &single)
			defer srv.Close()

			updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(), mcInstalled("2.9.0", 5000050))
			require.NoError(t, err)

			require.Len(t, updates, 1, "got %+v", updates)
			assert.Equal(t, "3.0.0", updates[0].NewVersion)
			assert.Equal(t, map[string]string{"5000050": "5000100"}, updates[0].FileIDReplacements)
			assert.EqualValues(t, 1, single.Load(), "the one lookup the check already made, no more")
		})
	}
}

// TestCheckUpdates_LoaderAmbiguityAloneTriggersTheLookup: one game-version
// type, several loaders, a superseded install: the flavor dimension is
// unambiguous but the loader one is not, so the installed file is looked up.
// The single-loader twin of the same mod needs no lookup.
func TestCheckUpdates_LoaderAmbiguityAloneTriggersTheLookup(t *testing.T) {
	t.Run("several loaders", func(t *testing.T) {
		forge := loaderFile(6000100, "multi-forge-1.20.1-2.0.0", ReleaseTypeRelease, "2026-03-01T00:00:00Z", typeMC120, "Forge")
		fabric := loaderFile(6000200, "multi-fabric-1.20.1-2.0.1", ReleaseTypeRelease, "2026-04-01T00:00:00Z", typeMC120, "Fabric")
		mods := []Mod{{
			ID: 6001, Name: "Multi", LatestFiles: []File{fabric, forge},
			LatestFilesIndexes: []FileIndex{loaderIndex(fabric, typeMC120, ModLoaderFabric), loaderIndex(forge, typeMC120, ModLoaderForge)},
		}}
		installed := loaderFile(6000050, "multi-forge-1.20.1-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Forge")

		var batch, single atomic.Int32
		srv := flavorServer(t, mods, map[int]File{6000050: installed}, &batch, &single)
		defer srv.Close()

		updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
			[]domain.InstalledMod{wowInstalled(6001, "Multi", "1.0.0", 6000050)})
		require.NoError(t, err)

		require.Len(t, updates, 1)
		assert.Equal(t, "2.0.0", updates[0].NewVersion, "the Forge file, not the newer Fabric one")
		assert.EqualValues(t, 1, single.Load())
	})

	t.Run("one loader", func(t *testing.T) {
		forge := loaderFile(7000100, "solo-forge-1.20.1-2.0.0", ReleaseTypeRelease, "2026-03-01T00:00:00Z", typeMC120, "Forge")
		mods := []Mod{{
			ID: 7001, Name: "Solo", LatestFiles: []File{forge},
			LatestFilesIndexes: []FileIndex{loaderIndex(forge, typeMC120, ModLoaderForge)},
		}}

		var batch, single atomic.Int32
		srv := flavorServer(t, mods, nil, &batch, &single)
		defer srv.Close()

		updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
			[]domain.InstalledMod{wowInstalled(7001, "Solo", "1.0.0", 7000050)})
		require.NoError(t, err)

		require.Len(t, updates, 1)
		assert.Equal(t, "2.0.0", updates[0].NewVersion)
		assert.Zero(t, single.Load(), "a mod with one value on every dimension has nothing to be ambiguous about")
	})
}

// TestCheckUpdates_AnyLoaderFileIsAWildcard: modLoader 0 ("Any") on either
// side is never a mismatch.
func TestCheckUpdates_AnyLoaderFileIsAWildcard(t *testing.T) {
	forge := loaderFile(8000100, "wild-forge-1.20.1-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Forge")
	fabric := loaderFile(8000200, "wild-fabric-1.20.1-1.1.0", ReleaseTypeRelease, "2026-02-01T00:00:00Z", typeMC120, "Fabric")
	anyFile := loaderFile(8000300, "wild-any-1.20.1-1.2.0", ReleaseTypeRelease, "2026-03-01T00:00:00Z", typeMC120)
	mods := []Mod{{
		ID: 8001, Name: "Wild", LatestFiles: []File{anyFile, forge, fabric},
		LatestFilesIndexes: []FileIndex{
			loaderIndex(anyFile, typeMC120, ModLoaderAny),
			loaderIndex(forge, typeMC120, ModLoaderForge),
			loaderIndex(fabric, typeMC120, ModLoaderFabric),
		},
	}}

	cases := []struct {
		name      string
		installed int
		want      string // "" = no update
	}{
		{"a Forge install is offered the Any-loader file, not the Fabric one", 8000100, "1.2.0"},
		{"a Fabric install is offered the Any-loader file", 8000200, "1.2.0"},
		{"an Any-loader install already on the newest file has nothing to update to", 8000300, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var batch, single atomic.Int32
			srv := flavorServer(t, mods, nil, &batch, &single)
			defer srv.Close()

			updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
				[]domain.InstalledMod{wowInstalled(8001, "Wild", "1.0.0", tc.installed)})
			require.NoError(t, err)
			if tc.want == "" {
				assert.Empty(t, updates)
				return
			}
			require.Len(t, updates, 1)
			assert.Equal(t, tc.want, updates[0].NewVersion)
			assert.Zero(t, single.Load())
		})
	}

	t.Run("an Any-loader install is offered a newer file of any loader", func(t *testing.T) {
		base := loaderFile(8100100, "any-base-1.20.1-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120)
		newForge := loaderFile(8100200, "any-forge-1.20.1-1.1.0", ReleaseTypeRelease, "2026-02-01T00:00:00Z", typeMC120, "Forge")
		other := []Mod{{
			ID: 8101, Name: "AnyBase", LatestFiles: []File{base, newForge},
			LatestFilesIndexes: []FileIndex{loaderIndex(base, typeMC120, ModLoaderAny), loaderIndex(newForge, typeMC120, ModLoaderForge)},
		}}
		var batch, single atomic.Int32
		srv := flavorServer(t, other, nil, &batch, &single)
		defer srv.Close()

		updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
			[]domain.InstalledMod{wowInstalled(8101, "AnyBase", "1.0.0", 8100100)})
		require.NoError(t, err)
		require.Len(t, updates, 1)
		assert.Equal(t, "1.1.0", updates[0].NewVersion)
	})
}

// TestCheckUpdates_MultiLoaderFileMatchesEachOfItsLoaders: one jar published
// for Forge and NeoForge appears under both loaders in the index, so it is an
// update to an install of either, and still not to a Fabric one.
func TestCheckUpdates_MultiLoaderFileMatchesEachOfItsLoaders(t *testing.T) {
	forgeOld := loaderFile(9000100, "both-forge-1.20.1-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Forge")
	fabricOld := loaderFile(9000110, "both-fabric-1.20.1-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Fabric")
	both := loaderFile(9000200, "both-1.20.1-1.1.0", ReleaseTypeRelease, "2026-02-01T00:00:00Z", typeMC120, "Forge", "NeoForge")
	mods := []Mod{{
		ID: 9001, Name: "Both", LatestFiles: []File{fabricOld, both, forgeOld},
		LatestFilesIndexes: []FileIndex{
			loaderIndex(fabricOld, typeMC120, ModLoaderFabric),
			loaderIndex(both, typeMC120, ModLoaderForge),
			loaderIndex(both, typeMC120, ModLoaderNeoForge),
			loaderIndex(forgeOld, typeMC120, ModLoaderForge),
		},
	}}

	for _, tc := range []struct {
		name      string
		installed int
		want      string
	}{
		{"forge", 9000100, "1.1.0"},
		{"fabric", 9000110, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var batch, single atomic.Int32
			srv := flavorServer(t, mods, nil, &batch, &single)
			defer srv.Close()

			updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
				[]domain.InstalledMod{wowInstalled(9001, "Both", "1.0.0", tc.installed)})
			require.NoError(t, err)
			if tc.want == "" {
				assert.Empty(t, updates)
				return
			}
			require.Len(t, updates, 1)
			assert.Equal(t, tc.want, updates[0].NewVersion)
		})
	}
}

// TestCheckUpdates_LoaderDimensionIsInertWhenTheModReportsNone: WoW-style mods
// report modLoader 0 on every index entry, so the loader dimension does not
// exist for them - not even a stray loader-looking name on a file changes
// anything, and no extra lookup is made.
func TestCheckUpdates_LoaderDimensionIsInertWhenTheModReportsNone(t *testing.T) {
	retailOld := wowFile(100, "Addon-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeRetail)
	retailNew := wowFile(200, "Addon-1.1.0", ReleaseTypeRelease, "2026-02-01T00:00:00Z", typeRetail)
	retailNew.GameVersions = []string{"Fabric"} // not a loader for this mod: the index says Any everywhere
	mods := []Mod{{
		ID: 4001, Name: "Addon", LatestFiles: []File{retailNew},
		LatestFilesIndexes: []FileIndex{indexFor(retailNew, typeRetail)},
	}}

	var batch, single atomic.Int32
	srv := flavorServer(t, mods, map[int]File{100: retailOld}, &batch, &single)
	defer srv.Close()

	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
		[]domain.InstalledMod{wowInstalled(4001, "Addon", "1.0.0", 100)})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "1.1.0", updates[0].NewVersion)
	assert.Zero(t, single.Load())
}

// TestLoaderByName_IsCurseForgesOwnVocabulary pins the one name->constant
// table: CurseForge's loader names, matched case-insensitively.
func TestLoaderByName_IsCurseForgesOwnVocabulary(t *testing.T) {
	for name, want := range map[string]int{
		"Forge": ModLoaderForge, "Cauldron": ModLoaderCauldron, "LiteLoader": ModLoaderLiteLoader,
		"Fabric": ModLoaderFabric, "Quilt": ModLoaderQuilt, "NeoForge": ModLoaderNeoForge, "neoforge": ModLoaderNeoForge,
	} {
		got, ok := loaderByName(name)
		assert.True(t, ok, name)
		assert.Equal(t, want, got, name)
	}
	_, ok := loaderByName("1.20.1")
	assert.False(t, ok)
}
