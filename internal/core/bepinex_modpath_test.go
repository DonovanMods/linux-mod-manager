package core_test

// A BepInEx layout is relative to the GAME ROOT, so the bepinex adapter can
// only be right for a game whose mod_path IS its install path (#413
// re-review P-b).
//
// v1 had no loader support at all, so a v1 user managing Valheim pointed
// mod_path at <install>/BepInEx/plugins and let plugin archives deploy
// exactly as packaged - a loose Foo.dll became BepInEx/plugins/Foo.dll,
// which is correct. U3 (#424's derivation) resolved that game to bepinex
// the moment BepInEx was on disk, rewrote the same archive to
// BepInEx/plugins/Foo/Foo.dll and joined THAT onto mod_path: the plugin
// landed in <install>/BepInEx/plugins/BepInEx/plugins/Foo/, and a seeded
// config in <install>/BepInEx/plugins/BepInEx/config/, where BepInEx never
// reads it. Reproduced on disk with a sandboxed install before this fix.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/bepinex"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPluginsModPathService is the v1-style game: BepInEx installed, and a
// mod_path that is its plugins directory rather than the game root.
func newPluginsModPathService(t *testing.T, loader *domain.GameLoader) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	root := t.TempDir()
	bepinexInstall(t, root, "", domain.LoaderBootstrapNative, time.Now())
	game := &domain.Game{
		ID: "valheim", Name: "Valheim",
		InstallPath: root, ModPath: filepath.Join(root, "BepInEx", "plugins"),
		LinkMethod: domain.LinkSymlink, Loader: loader,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), game.ID)
	require.NoError(t, err)
	return svc, game
}

// TestImportArchive_AModPathInsideBepInExDeploysAsPackaged is the on-disk
// reproduction: the archives a v1 user deployed into BepInEx/plugins land
// exactly where they always did, and nothing is nested a second time.
func TestImportArchive_AModPathInsideBepInExDeploysAsPackaged(t *testing.T) {
	cases := map[string]*domain.GameLoader{
		"installed, undeclared (the v1 configuration)": nil,
		"installed and declared":                       {Kind: domain.LoaderKindBepInEx},
	}
	for name, loader := range cases {
		t.Run(name, func(t *testing.T) {
			svc, game := newPluginsModPathService(t, loader)
			assert.Equal(t, "generic-files", svc.AdapterName(game),
				"a layout relative to the game root cannot be deployed under a mod_path that is not the game root")

			for archive, members := range map[string]map[string]string{
				"LoosePlugin-1.0.0.zip": {"LoosePlugin.dll": "assembly"},
				"CoolFolder-1.0.0.zip": {
					"CoolFolder/CoolFolder.dll": "assembly",
					"CoolFolder/assets.bundle":  "bundle",
				},
			} {
				path := filepath.Join(t.TempDir(), archive)
				createImportTestZip(t, path, members)
				_, err := svc.ImportArchive(context.Background(), game, "default", path,
					core.ImportArchiveOptions{Force: true}, nil)
				require.NoError(t, err, archive)
			}

			plugins := filepath.Join(game.InstallPath, "BepInEx", "plugins")
			for _, rel := range []string{"LoosePlugin.dll", "CoolFolder/CoolFolder.dll", "CoolFolder/assets.bundle"} {
				_, err := os.Lstat(filepath.Join(plugins, filepath.FromSlash(rel)))
				assert.NoError(t, err, "%s must deploy into the plugins directory exactly as packaged", rel)
			}
			_, err := os.Lstat(filepath.Join(plugins, "BepInEx"))
			assert.ErrorIs(t, err, os.ErrNotExist, "nothing may be nested under BepInEx/plugins/BepInEx/")
		})
	}
}

// TestAdapterFor_RefusesAnExplicitBepInExAdapterOffTheGameRoot is the same
// rule for the one configuration that names the adapter outright: every
// deploy it made would be nested a level too deep, so it is refused the
// way a compile game's non-compiling adapter is - by name, with the fix -
// and SetGameAdapter cannot write it.
func TestAdapterFor_RefusesAnExplicitBepInExAdapterOffTheGameRoot(t *testing.T) {
	svc, game := newPluginsModPathService(t, nil)

	explicit := *game
	explicit.Adapter = "bepinex"
	_, err := svc.AdapterFor(&explicit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mod_path")
	assert.Contains(t, err.Error(), game.InstallPath, "the refusal names the value mod_path needs")

	_, err = svc.SetGameAdapter(context.Background(), game.ID, "bepinex")
	var specErr *core.GameSpecError
	require.ErrorAs(t, err, &specErr)
	assert.Equal(t, "adapter", specErr.Field)

	atRoot := explicit
	atRoot.ModPath = game.InstallPath + string(filepath.Separator)
	_, err = svc.AdapterFor(&atRoot)
	assert.NoError(t, err, "a trailing separator is still the game root")
}

// TestVerify_AModPathInsideBepInExReportsNoMisplacedPlugins: the
// misplaced-deployment check asks where the BepInEx LAYOUT would put a
// file, and a game whose adapter is not bepinex has no such layout. Asked
// anyway, it read every plugin a v1 user deployed into BepInEx/plugins -
// recorded relative to that mod_path, so outside "BepInEx/" - as misplaced,
// and --fix would have re-laid the cache entry out and nested it.
func TestVerify_AModPathInsideBepInExReportsNoMisplacedPlugins(t *testing.T) {
	svc, game := newPluginsModPathService(t, nil)
	path := filepath.Join(t.TempDir(), "LoosePlugin-1.0.0.zip")
	createImportTestZip(t, path, map[string]string{"LoosePlugin.dll": "assembly"})
	_, err := svc.ImportArchive(context.Background(), game, "default", path, core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(res.Result, "loader_deployed_outside_loader"), "statuses: %v", findingStatuses(res.Result))
}

// TestAddGame_ABepInExGameDefaultsItsModPathToTheInstallPath: the default
// every other game gets, <install>/mods, is a directory BepInEx never
// reads, and with the rule above it would leave a freshly added loader game
// on generic-files. A BepInEx spec with no mod path therefore deploys into
// the game root, which is what the known-games catalog writes for the same
// games.
func TestAddGame_ABepInExGameDefaultsItsModPathToTheInstallPath(t *testing.T) {
	cases := map[string]core.GameSpec{
		"a loader declaration": {Loader: &core.LoaderSpec{Kind: "bepinex"}},
		"the bepinex adapter":  {Adapter: "bepinex"},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			svc := newGameAddService(t)
			svc.RegisterAdapter(bepinex.New())
			install := t.TempDir()
			spec.SourceID, spec.Identifier, spec.Name, spec.InstallPath = "nexusmods", "valheim", "Valheim", install

			assert.Equal(t, install, spec.DefaultModPath(), "the prompt offers the default AddGame writes")
			entry, err := svc.AddGame(t.Context(), spec)
			require.NoError(t, err)
			assert.Equal(t, install, entry.ModPath)
			assert.Equal(t, "bepinex", entry.EffectiveAdapter)
		})
	}

	// #413 final review F1: `game add --from-detected` and POST
	// /api/v1/games prefill an uncatalogued candidate's mod path before
	// AddGame's own default can run, so the prefill must apply the same
	// rule - or `--loader bepinex` writes a game contradicted from birth,
	// and `--adapter bepinex` is refused over a mod path nobody typed.
	for name, overrides := range cases {
		t.Run("from a detected, uncatalogued game: "+name, func(t *testing.T) {
			svc := newGameAddService(t)
			svc.RegisterAdapter(bepinex.New())
			install := t.TempDir()
			overrides.SourceID, overrides.Identifier = "nexusmods", "oddity"
			spec, err := svc.PrefillGameSpecFromDetected(domain.DetectedGame{
				SteamAppID: "777777", Slug: "oddity", Name: "Oddity", InstallPath: install,
			}, overrides)
			require.NoError(t, err)
			assert.Equal(t, install, spec.ModPath)

			entry, err := svc.AddGame(t.Context(), spec)
			require.NoError(t, err)
			assert.Equal(t, install, entry.ModPath)
			assert.Equal(t, "bepinex", entry.EffectiveAdapter)
			assert.Empty(t, svc.AdapterConfigWarning(entry.ID), "no contradiction at birth")
		})
	}

	t.Run("every other game keeps <install>/mods", func(t *testing.T) {
		spec := core.GameSpec{InstallPath: "/games/skyrim"}
		assert.Equal(t, filepath.Join("/games/skyrim", "mods"), spec.DefaultModPath())
	})

	t.Run("an explicit bepinex adapter with a mod path off the game root is refused", func(t *testing.T) {
		svc := newGameAddService(t)
		svc.RegisterAdapter(bepinex.New())
		_, err := svc.AddGame(t.Context(), core.GameSpec{
			SourceID: "nexusmods", Identifier: "valheim", Name: "Valheim",
			InstallPath: t.TempDir(), ModPath: "BepInEx/plugins", Adapter: "bepinex",
		})
		var specErr *core.GameSpecError
		require.ErrorAs(t, err, &specErr)
		assert.Equal(t, "adapter", specErr.Field)
	})
}
