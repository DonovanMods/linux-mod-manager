package core_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two fixtures below divide the BepInEx game shape in half.
//
// newBepInExGameRootService is a game-root game with NO loader declaration:
// it is the fixture for the normaliser's gate (an ambiguous shape must be
// left alone) and for #359's plan-time refusal (an unmistakable shape into a
// game that declares nothing). newBepInExDeclaredService adds the
// declaration, and is the fixture for every test about what a correctly
// configured BepInEx game does.
//
// newBepInExGameRootService builds the game shape a BepInEx install needs
// and the spike settled on (docs/plans/2026-09-09-bepinex-spike.md §1.3):
// mod_path IS install_path, the same absolute path twice, so a
// game-root-relative archive member deploys where BepInEx looks for it.
//
// NOT mod_path: "" - that is joined verbatim by the installer and deploys
// into the process's working directory, and the import scanner refuses it
// outright.
func newBepInExGameRootService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	root := t.TempDir()
	game := &domain.Game{
		ID: "lethal-company", Name: "Lethal Company",
		InstallPath: root, ModPath: root,
		LinkMethod: domain.LinkSymlink,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game
}

// TestImportArchive_BepInEx_ShapeADeploysIntoTheGameRoot is #358's headline
// claim end to end: the common Thunderstore shape, imported into a
// game-root game, lands at <install_path>/BepInEx/plugins/... through the
// existing linker - and the package metadata does not land at all.
func TestImportArchive_BepInEx_ShapeADeploysIntoTheGameRoot(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)

	archivePath := filepath.Join(t.TempDir(), "Skinwalkers-5.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"BepInEx/plugins/SkinwalkerMod.dll": "assembly",
		"manifest.json":                     `{"name":"Skinwalkers"}`,
		"icon.png":                          "png",
		"README.md":                         "# Skinwalkers",
	})

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Deployed, "the plugin assembly is the only deployable member")

	_, err = os.Lstat(filepath.Join(game.InstallPath, "BepInEx", "plugins", "SkinwalkerMod.dll"))
	require.NoError(t, err, "the plugin must deploy under the game root's BepInEx/plugins/")

	for _, metadata := range []string{"manifest.json", "icon.png", "README.md"} {
		_, err := os.Lstat(filepath.Join(game.InstallPath, metadata))
		assert.True(t, os.IsNotExist(err), "%s is package metadata and must never be deployed", metadata)
	}
}

// TestImportArchive_BepInEx_WrapperDirectoryIsStripped covers shape C, the
// one #237's `.EXMODZ` strip is the precedent for: a pack wrapped in a
// single directory deploys as though it never had one.
func TestImportArchive_BepInEx_WrapperDirectoryIsStripped(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)

	archivePath := filepath.Join(t.TempDir(), "SomePack-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"SomePack/BepInEx/plugins/Thing.dll": "assembly",
		"manifest.json":                      "{}",
	})

	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(game.InstallPath, "BepInEx", "plugins", "Thing.dll"))
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(game.InstallPath, "SomePack"))
	assert.True(t, os.IsNotExist(err), "the wrapper directory must not reach the game root")
}

// TestImportArchive_BepInEx_FrameworkPackIsRefused: BepInEx itself is a
// per-game prerequisite, so importing the pack as a mod fails with the
// message that names the loader setup rather than caching the preloader
// under lmm's deployed-files bookkeeping.
func TestImportArchive_BepInEx_FrameworkPackIsRefused(t *testing.T) {
	svc, game := newBepInExGameRootService(t)

	archivePath := filepath.Join(t.TempDir(), "BepInExPack-5.4.2305.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"BepInExPack/BepInEx/core/BepInEx.Preloader.dll": "preloader",
		"BepInExPack/winhttp.dll":                        "proxy",
		"manifest.json":                                  "{}",
	})

	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrBepInExFrameworkPack)
	assert.Contains(t, err.Error(), "--loader bepinex")

	_, statErr := os.Lstat(filepath.Join(game.InstallPath, "BepInEx"))
	assert.True(t, os.IsNotExist(statErr), "a refused framework pack deploys nothing")
}

// TestImportArchive_BepInEx_ConfigIsSeededAsARealFileAndNeverOverwritten is
// #358 (b): a plugin config is generated by BepInEx on first run and
// hand-edited afterwards, so a mod shipping one is seeding a DEFAULT.
// Deploying it as a symlink would send the user's edit into the cache, where
// the next re-download destroys it and every profile sharing the entry
// inherits it - so it takes profile-config-override semantics instead.
func TestImportArchive_BepInEx_ConfigIsSeededAsARealFileAndNeverOverwritten(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)

	archivePath := filepath.Join(t.TempDir(), "Configured-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"BepInEx/plugins/Configured.dll": "assembly",
		"BepInEx/config/configured.cfg":  "[General]\nEnabled = true\n",
		"manifest.json":                  "{}",
	})

	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	cfgPath := filepath.Join(game.InstallPath, "BepInEx", "config", "configured.cfg")
	info, err := os.Lstat(cfgPath)
	require.NoError(t, err, "the config must be seeded into the game directory")
	assert.True(t, info.Mode().IsRegular(), "a seeded config is a real file, never a link into the cache")

	dllInfo, err := os.Lstat(filepath.Join(game.InstallPath, "BepInEx", "plugins", "Configured.dll"))
	require.NoError(t, err)
	assert.Equal(t, os.ModeSymlink, dllInfo.Mode()&os.ModeSymlink,
		"everything that is NOT config still deploys through the game's link method")

	// The user edits it, then a re-deploy runs: the edit survives.
	require.NoError(t, os.WriteFile(cfgPath, []byte("[General]\nEnabled = false\n"), 0o644))
	_, err = svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	after, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "[General]\nEnabled = false\n", string(after),
		"copy-on-first-deploy: a seeded config is never overwritten once it exists")
}

// TestPlanImportArchive_BepInEx_PreviewsTheNormalisedPaths: the plan is
// computed from the archive's LISTING, and it must promise exactly the paths
// the ingest produces - that sharing is archive_listing.go's whole reason
// for existing.
func TestPlanImportArchive_BepInEx_PreviewsTheNormalisedPaths(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)

	archivePath := filepath.Join(t.TempDir(), "Wrapped-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"Wrapped/BepInEx/plugins/A.dll": "a",
		"Wrapped/BepInEx/config/a.cfg":  "cfg",
		"manifest.json":                 "{}",
		"icon.png":                      "png",
	})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"BepInEx/config/a.cfg", "BepInEx/plugins/A.dll"}, plan.Files)
}

// newBepInExDeclaredService is newBepInExGameRootService plus #359's loader
// declaration - the game shape that widens the normaliser onto the two
// AMBIGUOUS layouts (a bare plugins/ root, a loose root .dll).
func newBepInExDeclaredService(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	root := t.TempDir()
	game := &domain.Game{
		ID: "valheim", Name: "Valheim",
		InstallPath: root, ModPath: root,
		LinkMethod: domain.LinkSymlink,
		Loader: &domain.GameLoader{
			Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5",
			Runtime: domain.LoaderRuntimeMono, Bootstrap: domain.LoaderBootstrapProton,
		},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game
}

// TestImportArchive_BepInEx_ShapeBNeedsTheDeclaration is the pair that pins
// the gate end to end: the SAME archive - Evaisa/HookGenPatcher's bare
// patchers/ root - normalises for a game that declares the loader and is
// left exactly where it is for one that does not.
func TestImportArchive_BepInEx_ShapeBNeedsTheDeclaration(t *testing.T) {
	members := map[string]string{
		"patchers/HookGen/HookGenPatcher.dll": "assembly",
		"manifest.json":                       "{}",
	}

	t.Run("declared: the BepInEx/ prefix is applied", func(t *testing.T) {
		svc, game := newBepInExDeclaredService(t)
		archivePath := filepath.Join(t.TempDir(), "HookGenPatcher-0.0.5.zip")
		createImportTestZip(t, archivePath, members)

		_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
			core.ImportArchiveOptions{Force: true}, nil)
		require.NoError(t, err)

		_, err = os.Lstat(filepath.Join(game.InstallPath, "BepInEx", "patchers", "HookGen", "HookGenPatcher.dll"))
		assert.NoError(t, err)
	})

	t.Run("undeclared: a plugins-style root deploys exactly where it always did", func(t *testing.T) {
		svc, game := newBepInExGameRootService(t)
		archivePath := filepath.Join(t.TempDir(), "HookGenPatcher-0.0.5.zip")
		createImportTestZip(t, archivePath, members)

		_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
			core.ImportArchiveOptions{Force: true}, nil)
		require.NoError(t, err)

		_, err = os.Lstat(filepath.Join(game.InstallPath, "patchers", "HookGen", "HookGenPatcher.dll"))
		assert.NoError(t, err, "an undeclared game keeps the archive's own layout")
		_, err = os.Lstat(filepath.Join(game.InstallPath, "BepInEx"))
		assert.True(t, os.IsNotExist(err), "and gains no BepInEx directory it never asked for")
	})
}

// TestImportArchive_BepInEx_LooseDLLLandsUnderItsOwnPluginDirectory: the
// other ambiguous shape, for a declared game. The directory is named after
// the mod, which is what `lmm list` shows and what a user looks for on disk.
func TestImportArchive_BepInEx_LooseDLLLandsUnderItsOwnPluginDirectory(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)

	archivePath := filepath.Join(t.TempDir(), "CoolMod-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"CoolMod.dll":   "assembly",
		"manifest.json": "{}",
	})

	result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	_, err = os.Lstat(filepath.Join(game.InstallPath, "BepInEx", "plugins", result.Mod.Name, "CoolMod.dll"))
	assert.NoError(t, err)
}

// TestPlanImportArchive_BepInEx_UnrecognisedLayoutWarnsOnThePlan: "warns,
// never guesses" reaches a user where it can still change their mind - the
// plan both frontends render before committing to the import.
func TestPlanImportArchive_BepInEx_UnrecognisedLayoutWarnsOnThePlan(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)

	archivePath := filepath.Join(t.TempDir(), "Odd-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"Data/StreamingAssets/thing.bundle": "bytes",
	})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Warnings)
	assert.Contains(t, plan.Warnings[0], "did not recognise")
	assert.Equal(t, []string{filepath.Join("Data", "StreamingAssets", "thing.bundle")}, plan.Files,
		"an unrecognised layout is previewed exactly as the archive lists it")
}

// TestImportArchive_BepInEx_WrappedPackageDropsTheMetadataInsideTheWrapper is
// the review's F1 shape, and it is the shape Thunderstore actually produces:
// a wrapper directory containing BOTH BepInEx/ and the package metadata.
// Metadata outside the wrapper (the two tests above) is not a layout any real
// package has - a Thunderstore zip either has metadata at the root and no
// wrapper, or a wrapper containing the metadata.
//
// For a BepInEx game mod_path IS the game root, so a manifest.json that
// survives the strip lands in the Steam install directory. The plan and the
// ingest are asserted member-for-member on the same archive, because the whole
// contract is that a preview promises the layout the deploy produces.
func TestImportArchive_BepInEx_WrappedPackageDropsTheMetadataInsideTheWrapper(t *testing.T) {
	members := map[string]string{
		"SomePack/BepInEx/plugins/Thing.dll": "assembly",
		"SomePack/manifest.json":             `{"name":"SomePack"}`,
		"SomePack/icon.png":                  "png",
		"SomePack/README.md":                 "# SomePack",
		"SomePack/CHANGELOG.md":              "## 1.0.0",
	}
	want := []string{filepath.Join("BepInEx", "plugins", "Thing.dll")}

	t.Run("the plan previews only the payload", func(t *testing.T) {
		svc, game := newBepInExDeclaredService(t)
		archivePath := filepath.Join(t.TempDir(), "SomePack-1.0.0.zip")
		createImportTestZip(t, archivePath, members)

		plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
		require.NoError(t, err)
		assert.Equal(t, want, plan.Files)
	})

	t.Run("the archive ingest deploys only the payload", func(t *testing.T) {
		svc, game := newBepInExDeclaredService(t)
		archivePath := filepath.Join(t.TempDir(), "SomePack-1.0.0.zip")
		createImportTestZip(t, archivePath, members)

		result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
			core.ImportArchiveOptions{Force: true}, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Deployed, "the plugin assembly is the only deployable member")

		_, err = os.Lstat(filepath.Join(game.InstallPath, "BepInEx", "plugins", "Thing.dll"))
		require.NoError(t, err)
		for _, metadata := range []string{"manifest.json", "icon.png", "README.md", "CHANGELOG.md"} {
			_, err := os.Lstat(filepath.Join(game.InstallPath, metadata))
			assert.True(t, os.IsNotExist(err),
				"%s is package metadata and must never reach the game root", metadata)
		}
	})
}

// TestImportArchive_BepInEx_FrameworkPackIsRefusedInEverySpelling is
// re-review R1 end to end, on the game state that has no second line of
// defence: a DECLARED one. On an undeclared game #359's LoaderRequiredError
// intercepts a framework pack first, so the hole was invisible there; on a
// declared game a pack spelling the loader directory `BepInEx/Core/` was
// installed AS A MOD - the preloader, winhttp.dll and doorstop_config.ini
// all entered into deployed_files, where the next profile switch, uninstall
// or purge tears the loader out from under every plugin, and winhttp.dll
// deployed as a symlink into a cache a prune may empty.
func TestImportArchive_BepInEx_FrameworkPackIsRefusedInEverySpelling(t *testing.T) {
	for _, spelling := range []string{"core", "Core", "CORE"} {
		t.Run(spelling, func(t *testing.T) {
			svc, game := newBepInExDeclaredService(t)

			archivePath := filepath.Join(t.TempDir(), "BepInExPack-5.4.2305.zip")
			createImportTestZip(t, archivePath, map[string]string{
				"BepInExPack/BepInEx/" + spelling + "/BepInEx.Preloader.dll": "preloader",
				"BepInExPack/winhttp.dll":                                    "proxy",
				"BepInExPack/doorstop_config.ini":                            "[General]\n",
				"manifest.json":                                              "{}",
			})

			_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
				core.ImportArchiveOptions{Force: true}, nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, core.ErrBepInExFrameworkPack)

			mods, listErr := svc.ListMods(context.Background(), game, "default")
			require.NoError(t, listErr)
			assert.Empty(t, mods.Mods, "a refused framework pack installs no mod, so nothing enters deployed_files")

			assert.Empty(t, gameTreeForTest(t, game.InstallPath), "and nothing reaches the game directory")
		})
	}
}

// TestImportArchive_BepInEx_CaseVariantShapeBSeedsARealConfig is re-review
// R2: a shape-B root spelled `Plugins/` and `Config/` used to keep the
// archive's own spelling on the way through the BepInEx/ prefix, so the
// plugin deployed to BepInEx/Plugins/ (where the loader never looks on a
// case-sensitive filesystem) and the config missed isBepInExConfigMember
// entirely - seeding the user's hand-edited .cfg as a symlink into lmm's
// cache, which is exactly what #358 (b) exists to prevent.
func TestImportArchive_BepInEx_CaseVariantShapeBSeedsARealConfig(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)

	archivePath := filepath.Join(t.TempDir(), "Cased-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"Plugins/Cfg.dll": "assembly",
		"Config/cfg.cfg":  "[General]\nEnabled = true\n",
		"manifest.json":   "{}",
	})

	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"BepInEx/config/cfg.cfg", "BepInEx/plugins/Cfg.dll"},
		gameTreeForTest(t, game.InstallPath),
		"a case-variant root deploys to the paths the loader actually reads")

	info, err := os.Lstat(filepath.Join(game.InstallPath, "BepInEx", "config", "cfg.cfg"))
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "a seeded config is a real file, never a link into the cache")

	dllInfo, err := os.Lstat(filepath.Join(game.InstallPath, "BepInEx", "plugins", "Cfg.dll"))
	require.NoError(t, err)
	assert.Equal(t, os.ModeSymlink, dllInfo.Mode()&os.ModeSymlink,
		"everything that is NOT config still deploys through the game's link method")
}

// gameTreeForTest lists every FILE under root, slash-separated and relative
// to it, sorted - the "what actually reached the game directory" assertion
// the two tests above are both about.
func gameTreeForTest(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		found = append(found, filepath.ToSlash(rel))
		return nil
	}))
	sort.Strings(found)
	return found
}

// jotunnMembers is the exact member list the owner confirmed for Jotunn
// 2.30.0 from NexusMods (mod id 1138, #424): a single top-level directory
// holding the assembly plus its debug symbols, its XML documentation and
// the package's own docs. It is the standard NexusMods Valheim layout - a
// PLUGIN FOLDER - and #358's five shapes did not cover it, so it deployed
// verbatim into the Steam install directory.
var jotunnMembers = map[string]string{
	"Jotunn/Jotunn.dll":   "assembly",
	"Jotunn/Jotunn.pdb":   "symbols",
	"Jotunn/Jotunn.xml":   "<doc/>",
	"Jotunn/README.md":    "# Jotunn",
	"Jotunn/CHANGELOG.md": "## 2.30.0",
}

// jotunnDeployed is where every one of those members must land: the folder
// moves under BepInEx/plugins/ whole, so the .pdb, .xml and the docs stay
// beside the assembly exactly as the author shipped them.
var jotunnDeployed = []string{
	"BepInEx/plugins/Jotunn/CHANGELOG.md",
	"BepInEx/plugins/Jotunn/Jotunn.dll",
	"BepInEx/plugins/Jotunn/Jotunn.pdb",
	"BepInEx/plugins/Jotunn/Jotunn.xml",
	"BepInEx/plugins/Jotunn/README.md",
}

// TestImportArchive_BepInEx_PluginFolderDeploysUnderBepInExPlugins is #424's
// headline claim, asserted where it broke: the plan PREVIEWS the normalised
// paths, the ingest DEPLOYS them, and `lmm mod files` RECORDS them - the
// three surfaces the owner read when they found Jotunn in the game root.
func TestImportArchive_BepInEx_PluginFolderDeploysUnderBepInExPlugins(t *testing.T) {
	t.Run("the plan previews the plugin folder under BepInEx/plugins/", func(t *testing.T) {
		svc, game := newBepInExDeclaredService(t)
		archivePath := filepath.Join(t.TempDir(), "Jotunn-2.30.0.zip")
		createImportTestZip(t, archivePath, jotunnMembers)

		plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath,
			core.ImportArchiveOptions{})
		require.NoError(t, err)
		assert.Equal(t, fromSlashAll(jotunnDeployed), plan.Files)
		assert.Empty(t, plan.Warnings, "a recognised layout is not a layout lmm could not place")
	})

	t.Run("the archive ingest deploys exactly what the plan promised", func(t *testing.T) {
		svc, game := newBepInExDeclaredService(t)
		archivePath := filepath.Join(t.TempDir(), "Jotunn-2.30.0.zip")
		createImportTestZip(t, archivePath, jotunnMembers)

		result, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
			core.ImportArchiveOptions{Force: true}, nil)
		require.NoError(t, err)
		assert.Equal(t, len(jotunnDeployed), result.Deployed)
		assert.Equal(t, jotunnDeployed, gameTreeForTest(t, game.InstallPath),
			"nothing may reach the game root: the folder goes under BepInEx/plugins/ whole")

		files, err := svc.ModFiles(context.Background(), game, "default",
			result.Mod.SourceID, result.Mod.ID)
		require.NoError(t, err)
		recorded := make([]string, 0, len(files.Files))
		for _, f := range files.Files {
			recorded = append(recorded, filepath.ToSlash(f.Path))
			assert.True(t, f.Deployed, "%s must be on disk", f.Path)
		}
		sort.Strings(recorded)
		assert.Equal(t, jotunnDeployed, recorded,
			"`lmm mod files` records the deploy paths, which is where the owner read the bug")
	})
}

// TestImportArchive_BepInEx_TwoPluginFoldersEachKeepTheirOwnDirectory: an
// archive shipping two plugins side by side is still a plugin-folder root,
// and each folder keeps its own name under BepInEx/plugins/.
func TestImportArchive_BepInEx_TwoPluginFoldersEachKeepTheirOwnDirectory(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)
	archivePath := filepath.Join(t.TempDir(), "TwoPlugins-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"ModA/ModA.dll":      "a",
		"ModA/data/a.bundle": "bytes",
		"ModB/ModB.dll":      "b",
		"manifest.json":      "{}",
	})

	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"BepInEx/plugins/ModA/ModA.dll",
		"BepInEx/plugins/ModA/data/a.bundle",
		"BepInEx/plugins/ModB/ModB.dll",
	}, gameTreeForTest(t, game.InstallPath))
}

// TestPlanImportArchive_BepInEx_AMixedRootIsReportedNotGuessed: "warns,
// never guesses" is the whole reason shape F can be safe. A root that
// carries a plugin folder AND something else says something about that
// something else which lmm cannot read, so it is reported on the plan and
// deployed exactly as the archive lists it.
func TestPlanImportArchive_BepInEx_AMixedRootIsReportedNotGuessed(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)
	archivePath := filepath.Join(t.TempDir(), "Mixed-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"Jotunn/Jotunn.dll":   "assembly",
		"install-by-hand.txt": "copy me somewhere",
		"Assets/thing.bundle": "bytes",
	})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Warnings)
	assert.Contains(t, plan.Warnings[0], "did not recognise")
	assert.Contains(t, plan.Warnings[0], "Jotunn")
	assert.Equal(t, fromSlashAll([]string{
		"Assets/thing.bundle", "Jotunn/Jotunn.dll", "install-by-hand.txt",
	}), plan.Files, "an unrecognised layout is previewed exactly as the archive lists it")
}

// TestImportArchive_BepInEx_SevenDaysToDieModsFolderIsUntouched pins the
// shape shape F is most likely to steal: 7 Days to Die ships
// Mods/<Mod>/ModInfo.xml beside the mod's own assembly, and that game has
// no BepInEx anywhere near it. The gate is what keeps it where it belongs.
func TestImportArchive_BepInEx_SevenDaysToDieModsFolderIsUntouched(t *testing.T) {
	svc, game := newBepInExGameRootService(t)
	archivePath := filepath.Join(t.TempDir(), "MyMod-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"Mods/MyMod/ModInfo.xml": "<xml/>",
		"Mods/MyMod/MyMod.dll":   "assembly",
	})

	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{"Mods/MyMod/ModInfo.xml", "Mods/MyMod/MyMod.dll"},
		gameTreeForTest(t, game.InstallPath),
		"a game with no BepInEx keeps the archive's own layout")
}

// fromSlashAll converts slash-separated paths to the host separator, for
// comparing against a plan's Files (which are filepath-native).
func fromSlashAll(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.FromSlash(p))
	}
	return out
}

// seedGameOwnedTree writes files the GAME owns into its install directory -
// the Unity tree every real BepInEx game has and a bare t.TempDir() does
// not. Shape F's game-root rule is decided from what is actually there, so
// a fixture asking about it needs the game to actually be there.
func seedGameOwnedTree(t *testing.T, root string, members ...string) {
	t.Helper()
	for _, m := range members {
		p := filepath.Join(root, filepath.FromSlash(m))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("the game's own "+m), 0o644))
	}
}

// TestImportArchive_BepInEx_AGameOwnedDirectoryIsDeployedVerbatim is #424
// review finding 1 end to end: a patch that replaces one of the game's own
// managed assemblies has exactly shape F's signature - one root directory,
// an assembly inside it, no name BepInEx owns - and must NOT be moved under
// BepInEx/plugins/, where the engine would never read it.
func TestImportArchive_BepInEx_AGameOwnedDirectoryIsDeployedVerbatim(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)
	seedGameOwnedTree(t, game.InstallPath,
		"valheim_Data/Managed/UnityEngine.dll",
		"valheim_Data/Managed/Assembly-CSharp.dll",
		"valheim_Data/resources.assets",
	)

	archivePath := filepath.Join(t.TempDir(), "Patch-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"valheim_Data/Managed/Assembly-CSharp.dll": "patched assembly",
	})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Equal(t, fromSlashAll([]string{"valheim_Data/Managed/Assembly-CSharp.dll"}), plan.Files,
		"a game-root overlay is previewed exactly where it goes")

	_, err = svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"valheim_Data/Managed/Assembly-CSharp.dll",
		"valheim_Data/Managed/UnityEngine.dll",
		"valheim_Data/resources.assets",
	}, gameTreeForTest(t, game.InstallPath),
		"the patch replaces the game's assembly in place; nothing moves under BepInEx/")
}
