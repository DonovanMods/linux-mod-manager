package curseforge

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// latestFiles is a short list: CurseForge's own listing of a busy mod names
// only a few full files, while latestFilesIndexes keeps naming the newest file
// of every (game version, loader) pair - including files latestFiles no longer
// carries. Such a file has only what the index entry carries: its id, filename,
// release type, game-version type and loader.

// indexOnly builds the latestFilesIndexes entry for a file that is NOT in
// latestFiles.
func indexOnly(id int, filename string, release, mcType, loader int) FileIndex {
	return FileIndex{FileID: id, Filename: filename, ReleaseType: release, GameVersionTypeID: mcType, ModLoader: loader}
}

// minecraftModsWithIndexOnly is minecraftMods plus files only the index names:
// a newer Forge 1.20.1 release (5000400), a still newer Forge beta (5000450),
// and the newest Fabric 1.20.1 release (5000500).
func minecraftModsWithIndexOnly() []Mod {
	mods := minecraftMods()
	mods[0].LatestFilesIndexes = append(mods[0].LatestFilesIndexes,
		indexOnly(5000400, "fixturelib-forge-1.20.1-3.3.0.jar", ReleaseTypeRelease, typeMC120, ModLoaderForge),
		indexOnly(5000450, "fixturelib-forge-1.20.1-3.4.0-beta.jar", ReleaseTypeBeta, typeMC120, ModLoaderForge),
		indexOnly(5000500, "fixturelib-fabric-1.20.1-3.5.0.jar", ReleaseTypeRelease, typeMC120, ModLoaderFabric),
	)
	return mods
}

// TestCheckUpdates_IndexOnlyForgeFileIsOfferedToASupersededForgeInstall: the
// newest Forge 1.20.1 release is named only by latestFilesIndexes. The
// installed Forge file is superseded, so its loader comes from the one
// GetModFile the check already makes - and the offer is the index-only file,
// not the older Forge file in latestFiles, nor the newer Fabric/beta ones.
func TestCheckUpdates_IndexOnlyForgeFileIsOfferedToASupersededForgeInstall(t *testing.T) {
	installedFile := loaderFile(5000050, "fixturelib-forge-1.20.1-2.9.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Forge")

	var batch, single atomic.Int32
	srv := flavorServer(t, minecraftModsWithIndexOnly(), map[int]File{5000050: installedFile}, &batch, &single)
	defer srv.Close()

	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(), mcInstalled("2.9.0", 5000050))
	require.NoError(t, err)

	require.Len(t, updates, 1, "got %+v", updates)
	assert.Equal(t, "3.3.0", updates[0].NewVersion, "version comes from the index entry's filename")
	assert.Equal(t, map[string]string{"5000050": "5000400"}, updates[0].FileIDReplacements)
	assert.EqualValues(t, 1, batch.Load())
	assert.EqualValues(t, 1, single.Load(), "the one lookup the check already made; index candidates cost nothing")
}

// TestCheckUpdates_IndexOnlyFileIsOfferedToAnInstallListedInLatestFiles: the
// installed file is still listed, yet a newer file of its own game version and
// loader exists only in the index.
func TestCheckUpdates_IndexOnlyFileIsOfferedToAnInstallListedInLatestFiles(t *testing.T) {
	cases := []struct {
		name      string
		installed int
		version   string
		want      string // "" = no update
		wantID    string
	}{
		{"forge: the index-only release, not the older listed forge file or the beta", 5000100, "3.0.0", "3.3.0", "5000400"},
		{"fabric: its own index-only release", 5000200, "3.1.0", "3.5.0", "5000500"},
		{"quilt: nothing newer of its loader", 5000150, "3.0.5", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var batch, single atomic.Int32
			srv := flavorServer(t, minecraftModsWithIndexOnly(), nil, &batch, &single)
			defer srv.Close()

			updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(), mcInstalled(tc.version, tc.installed))
			require.NoError(t, err)
			assert.Zero(t, single.Load())
			if tc.want == "" {
				assert.Empty(t, updates)
				return
			}
			require.Len(t, updates, 1)
			assert.Equal(t, tc.want, updates[0].NewVersion)
			assert.Equal(t, map[string]string{itoa(tc.installed): tc.wantID}, updates[0].FileIDReplacements)
		})
	}
}

// TestCheckUpdates_IndexOnlyCandidateWithoutAVersionIsSkipped: nothing to show
// or compare, as for a listed file with no extractable version.
func TestCheckUpdates_IndexOnlyCandidateWithoutAVersionIsSkipped(t *testing.T) {
	cur := loaderFile(100, "thing-forge-1.20.1-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Forge")
	mods := []Mod{{
		ID: 9100, Name: "Thing", LatestFiles: []File{cur},
		LatestFilesIndexes: []FileIndex{
			loaderIndex(cur, typeMC120, ModLoaderForge),
			indexOnly(200, "thing.jar", ReleaseTypeRelease, typeMC120, ModLoaderForge),
		},
	}}
	var batch, single atomic.Int32
	srv := flavorServer(t, mods, nil, &batch, &single)
	defer srv.Close()

	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
		[]domain.InstalledMod{wowInstalled(9100, "Thing", "1.0.0", 100)})
	require.NoError(t, err)
	assert.Empty(t, updates)
}

// TestCheckUpdates_IndexOnlyFilesAreMergedPerFileID: a file in latestFiles and
// in several index entries is one candidate with the union of its classes and
// the full File's own data; an index-only id is not double counted either.
func TestCheckUpdates_IndexOnlyFilesAreMergedPerFileID(t *testing.T) {
	old := loaderFile(100, "m-forge-1.20.1-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Forge")
	fabricOld := loaderFile(110, "m-fabric-1.20.1-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Fabric")
	both := loaderFile(200, "m-1.20.1-1.1.0", ReleaseTypeRelease, "2026-02-01T00:00:00Z", typeMC120, "Forge", "NeoForge")
	mods := []Mod{{
		ID: 9200, Name: "M", LatestFiles: []File{both, old, fabricOld},
		LatestFilesIndexes: []FileIndex{
			loaderIndex(old, typeMC120, ModLoaderForge),
			loaderIndex(fabricOld, typeMC120, ModLoaderFabric),
			loaderIndex(both, typeMC120, ModLoaderForge),
			loaderIndex(both, typeMC120, ModLoaderNeoForge),
			// the same index-only file under two game versions of one type
			indexOnly(300, "m-1.20.1-1.2.0.jar", ReleaseTypeRelease, typeMC120, ModLoaderForge),
			indexOnly(300, "m-1.20.1-1.2.0.jar", ReleaseTypeRelease, typeMC120, ModLoaderForge),
		},
	}}
	for name, tc := range map[string]struct {
		installed int
		want      string
	}{"forge gets the index-only 1.2.0": {100, "1.2.0"}, "fabric gets nothing": {110, ""}} {
		t.Run(name, func(t *testing.T) {
			var batch, single atomic.Int32
			srv := flavorServer(t, mods, nil, &batch, &single)
			defer srv.Close()
			updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
				[]domain.InstalledMod{wowInstalled(9200, "M", "1.0.0", tc.installed)})
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

// TestCheckUpdates_NewestCandidateComparesDatesOnlyAmongDatedFiles: with an
// index-only candidate in the pool a date is never compared to an id - the
// newest is then decided by file id; with every candidate dated, the date
// still decides (here against the id order).
func TestCheckUpdates_NewestCandidateComparesDatesOnlyAmongDatedFiles(t *testing.T) {
	t.Run("a mix decides by file id", func(t *testing.T) {
		cur := loaderFile(100, "n-forge-1.20.1-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Forge")
		// A far-future date on a LOWER id: the index-only file has no date to
		// compare, so the dated file must not outrank it on that date.
		dated := loaderFile(150, "n-forge-1.20.1-1.1.0", ReleaseTypeRelease, "2030-01-01T00:00:00Z", typeMC120, "Forge")
		mods := []Mod{{
			ID: 9300, Name: "N", LatestFiles: []File{cur, dated},
			LatestFilesIndexes: []FileIndex{
				loaderIndex(cur, typeMC120, ModLoaderForge),
				loaderIndex(dated, typeMC120, ModLoaderForge),
				indexOnly(250, "n-forge-1.20.1-1.2.0.jar", ReleaseTypeRelease, typeMC120, ModLoaderForge),
			},
		}}
		var batch, single atomic.Int32
		srv := flavorServer(t, mods, nil, &batch, &single)
		defer srv.Close()
		updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
			[]domain.InstalledMod{wowInstalled(9300, "N", "1.0.0", 100)})
		require.NoError(t, err)
		require.Len(t, updates, 1)
		assert.Equal(t, "1.2.0", updates[0].NewVersion)
	})
	t.Run("all dated decides by date", func(t *testing.T) {
		cur := loaderFile(100, "d-forge-1.20.1-1.0.0", ReleaseTypeRelease, "2026-01-01T00:00:00Z", typeMC120, "Forge")
		lowIDNewer := loaderFile(150, "d-forge-1.20.1-1.1.0", ReleaseTypeRelease, "2026-06-01T00:00:00Z", typeMC120, "Forge")
		highIDOlder := loaderFile(160, "d-forge-1.20.1-1.0.5", ReleaseTypeRelease, "2026-03-01T00:00:00Z", typeMC120, "Forge")
		mods := []Mod{{
			ID: 9301, Name: "D", LatestFiles: []File{cur, highIDOlder, lowIDNewer},
			LatestFilesIndexes: []FileIndex{
				loaderIndex(cur, typeMC120, ModLoaderForge),
				loaderIndex(highIDOlder, typeMC120, ModLoaderForge),
				loaderIndex(lowIDNewer, typeMC120, ModLoaderForge),
			},
		}}
		var batch, single atomic.Int32
		srv := flavorServer(t, mods, nil, &batch, &single)
		defer srv.Close()
		updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
			[]domain.InstalledMod{wowInstalled(9301, "D", "1.0.0", 100)})
		require.NoError(t, err)
		require.Len(t, updates, 1)
		assert.Equal(t, "1.1.0", updates[0].NewVersion)
	})
}

// TestCheckUpdates_IndexOnlyRetailFileForAWoWInstall: the flavor-only
// (loader-less) game: an index-only newer file of the installed flavor is
// offered with no extra call, and an index-only file of another flavor is not.
// The rest of WoW's behaviour is pinned by updates_flavor_test.go, unchanged.
func TestCheckUpdates_IndexOnlyRetailFileForAWoWInstall(t *testing.T) {
	retail := wowFile(6500002, "Addon-1.0.0", ReleaseTypeRelease, "2026-08-01T00:00:00Z", typeRetail)
	classic := wowFile(6500001, "Addon-0.9.0", ReleaseTypeRelease, "2026-07-01T00:00:00Z", typeClassic)
	mods := []Mod{{
		ID: 9400, Name: "Addon", LatestFiles: []File{classic, retail},
		LatestFilesIndexes: []FileIndex{
			indexFor(classic, typeClassic), indexFor(retail, typeRetail),
			{FileID: 6500100, Filename: "Addon-1.1.0.zip", ReleaseType: ReleaseTypeRelease, GameVersionTypeID: typeRetail},
			{FileID: 6500200, Filename: "Addon-0.9.5.zip", ReleaseType: ReleaseTypeRelease, GameVersionTypeID: typeClassic},
		},
	}}
	var batch, single atomic.Int32
	srv := flavorServer(t, mods, nil, &batch, &single)
	defer srv.Close()

	updates, err := newFlavorTestSource(srv).CheckUpdates(context.Background(),
		[]domain.InstalledMod{wowInstalled(9400, "Addon", "1.0.0", 6500002)})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, "1.1.0", updates[0].NewVersion)
	assert.Equal(t, map[string]string{"6500002": "6500100"}, updates[0].FileIDReplacements)
	assert.Zero(t, single.Load())
}

func itoa(n int) string { return strconv.Itoa(n) }
