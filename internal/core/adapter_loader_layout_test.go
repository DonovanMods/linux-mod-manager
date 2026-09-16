package core_test

// One adapter per game, applied identically by the plan and by the ingest
// (#411, #358/#359, #413).
//
// This file was written for U1's arrangement, where TWO rewriters ran in a
// fixed order on one import: core's own BepInEx normaliser first, against
// the archive as the user packaged it, then the game's adapter, against what
// normalisation produced. U3 collapsed that pair - the normaliser IS the
// bepinex adapter now - so there is no order left to pin, and what the file
// pins instead is the rule that replaced it: the game's ADAPTER decides how
// an archive is laid out, `loader:` does not, and whichever adapter that is,
// the plan promises exactly what the ingest caches.

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prefixStub moves every member under one extra directory, keeping the rest
// of the path intact. Unlike byNameStub it PRESERVES structure, which is
// what makes it readable in an expectation: "adapted/BepInEx/plugins/x.dll"
// would mean the BepInEx rules had run as well, and they must not - this
// game's adapter is the prefix one.
type prefixStub struct{}

func (prefixStub) ID() string    { return "prefix" }
func (prefixStub) Label() string { return "Prefix" }

func (prefixStub) NormalizeArchive(req adapter.NormalizeRequest) (adapter.Layout, error) {
	rewrites := make(map[string]string, len(req.Members))
	for _, m := range req.Members {
		rewrites[m] = "adapted/" + m
	}
	return adapter.NewLayout("prefix", rewrites), nil
}

// loaderArchiveShapes are the BepInEx archive shapes, with the tree each
// one normalises to. It also covers the loader-rooted shape planIngestCorpus
// cannot carry: #359 refuses one into a game with no loader, which is every
// game that corpus builds.
var loaderArchiveShapes = map[string]struct {
	members []string
	// want is what the BEPINEX adapter produces.
	want []string
	// raw is the archive's own layout, which is what any other adapter
	// sees - it never gets the normalised tree, because there is only ever
	// one adapter in an import.
	raw []string
}{
	"wrapped loader pack": {
		members: []string{"MyPack/BepInEx/plugins/Foo.dll", "MyPack/manifest.json"},
		want:    []string{"BepInEx/plugins/Foo.dll"},
		raw:     []string{"MyPack/BepInEx/plugins/Foo.dll", "MyPack/manifest.json"},
	},
	"loader rooted": {
		members: []string{"BepInEx/plugins/Foo.dll", "BepInEx/config/Foo.cfg"},
		want:    []string{"BepInEx/config/Foo.cfg", "BepInEx/plugins/Foo.dll"},
		raw:     []string{"BepInEx/config/Foo.cfg", "BepInEx/plugins/Foo.dll"},
	},
	"bare plugins root": {
		members: []string{"plugins/Foo.dll"},
		want:    []string{"BepInEx/plugins/Foo.dll"},
		raw:     []string{"plugins/Foo.dll"},
	},
	"loose root dll": {
		members: []string{"Foo.dll"},
		want:    []string{"BepInEx/plugins/Flat-2.0/Foo.dll"},
		raw:     []string{"Foo.dll"},
	},
}

// TestPlanImportArchive_AgreesWithIngestForALoaderGame drives every BepInEx
// archive shape through a game that declares the loader, and asserts the
// seam's whole invariant: the plan's file list is exactly what the ingest
// caches. The paths are the bepinex adapter's, because that is the adapter a
// loader-declaring game resolves to.
func TestPlanImportArchive_AgreesWithIngestForALoaderGame(t *testing.T) {
	for name, tc := range loaderArchiveShapes {
		t.Run(name, func(t *testing.T) {
			svc, game := newImportArchiveTestService(t)
			game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx}
			require.NoError(t, svc.SaveGame(context.Background(), game))

			planned := planAndImport(t, svc, game, tc.members)
			assert.Equal(t, fromSlashAll(tc.want), planned,
				"the plan must promise the paths the bepinex adapter lays out")
		})
	}
}

// TestPlanImportArchive_AnExplicitAdapterOverridesTheLoaderBlock is decision
// 11 made observable: `loader:` describes the INSTALLATION - which loader
// build is present - while `adapter:` decides what lmm does about an
// archive. A game that declares BepInEx but names another adapter gets that
// adapter's rules and no BepInEx normalisation at all, and the plan/ingest
// agreement holds there too.
//
// It also pins the claim's one exception: such a game is NOT refused for
// importing a BepInEx-shaped archive (requireAdapterClaim), because its own
// configuration already declares the loader the claim would ask for.
func TestPlanImportArchive_AnExplicitAdapterOverridesTheLoaderBlock(t *testing.T) {
	for name, tc := range loaderArchiveShapes {
		t.Run(name, func(t *testing.T) {
			svc, game := newImportArchiveTestService(t)
			svc.RegisterAdapter(prefixStub{})
			game.Adapter = prefixStub{}.ID()
			game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx}
			require.NoError(t, svc.SaveGame(context.Background(), game))

			want := make([]string, len(tc.raw))
			for i, p := range tc.raw {
				want[i] = "adapted/" + p
			}
			planned := planAndImport(t, svc, game, tc.members)
			assert.Equal(t, fromSlashAll(want), planned,
				"the game's own adapter sees the RAW archive, never a normalised tree")
		})
	}
}

// planAndImport plans an import of members into game, applies it, and
// asserts the plan's file list against what actually reached the cache -
// returning the list so the caller can assert its contents.
func planAndImport(t *testing.T, svc *core.Service, game *domain.Game, members []string) []string {
	t.Helper()
	files := make(map[string]string, len(members))
	for _, m := range members {
		files[m] = "content of " + m
	}
	archivePath := filepath.Join(t.TempDir(), "Flat-2.0.zip")
	createImportTestZip(t, archivePath, files)

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)

	result, err := svc.ApplyImportArchive(context.Background(), game, "default", plan, core.ImportArchiveOptions{}, nil)
	require.NoError(t, err)

	cached, err := svc.GetGameCache(game).ListFiles(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
	require.NoError(t, err)
	slices.Sort(cached)
	assert.Equal(t, plan.Files, cached,
		"the plan's file list must equal what the ingest actually cached")
	return plan.Files
}
