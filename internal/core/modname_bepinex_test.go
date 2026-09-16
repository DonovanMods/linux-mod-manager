package core_test

// #450: `lmm import` named every BepInEx-rooted plugin package "BepInEx",
// because "a sole top-level directory names the mod" read the loader's own
// directory as the mod's. BepInEx/ and its well-known subdirectories are
// structure, not a name: the name is the one plugin directory beneath them,
// or the archive's own name.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bepinexNamingCases are archive member lists and the name each must
// import under.
var bepinexNamingCases = map[string]struct {
	archive string
	members []string
	want    string
}{
	"a loose plugin under BepInEx/plugins takes the archive's name": {
		archive: "Rooted-400.zip", members: []string{"BepInEx/plugins/Rooted.dll"}, want: "Rooted-400",
	},
	"one plugin folder names the mod": {
		archive: "pkg.zip",
		members: []string{"BepInEx/plugins/CoolFolder/CoolFolder.dll", "BepInEx/plugins/CoolFolder/assets.bundle"},
		want:    "CoolFolder",
	},
	"a config file beside the plugin folder does not hide its name": {
		archive: "pkg.zip",
		members: []string{"BepInEx/plugins/CoolFolder/CoolFolder.dll", "BepInEx/config/CoolFolder.cfg"},
		want:    "CoolFolder",
	},
	"a patcher folder names the mod, whatever the case of BepInEx": {
		archive: "pkg.zip", members: []string{"bepinex/patchers/EarlyPatch/EarlyPatch.dll"}, want: "EarlyPatch",
	},
	"two plugin folders take the archive's name": {
		archive: "Bundle-1.0.zip",
		members: []string{"BepInEx/plugins/A/a.dll", "BepInEx/plugins/B/b.dll"},
		want:    "Bundle-1.0",
	},
	"a bare plugins/ root is structure too": {
		archive: "pkg.zip", members: []string{"plugins/MyMod/MyMod.dll"}, want: "MyMod",
	},
	"only a config file takes the archive's name": {
		archive: "Settings-2.zip", members: []string{"BepInEx/config/Settings.cfg"}, want: "Settings-2",
	},
	"a wrapper around BepInEx is still the mod's name": {
		archive: "pkg.zip", members: []string{"MyMod/BepInEx/plugins/MyMod.dll"}, want: "MyMod",
	},
}

func TestDetectModName_BepInExIsStructureNotAName(t *testing.T) {
	// A game that DECLARES the loader (#450): every case here relies on the
	// bare plugins/patchers/config-style shapes counting as structure, which
	// only holds for a game that actually loads mods through BepInEx.
	game := &domain.Game{ID: "bepinex-game", Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx}}
	for name, tc := range bepinexNamingCases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, m := range tc.members {
				p := filepath.Join(dir, filepath.FromSlash(m))
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
			}
			assert.Equal(t, tc.want, core.DetectModName(game, dir, tc.archive))
		})
	}
}

// TestDetectModName_NonBepInExGame_BareStructureNamesDirsStillNameTheMod
// (#450 nit): for a game with no BepInEx involved at all, "Core-1.0.zip"
// containing "Core/..." is a real mod named "Core" - the bare-subdirectory
// half of isLoaderStructure's rule never applies to it, so the old
// sole-top-level-directory rule stands. (A literal top-level "BepInEx/" is
// still always structure, whatever the game - that case is covered by
// TestImportArchive_ABepInExRootedPackageIsNamedAfterItself's fixtures,
// none of which use a non-BepInEx game.)
func TestDetectModName_NonBepInExGame_BareStructureDirNamesTheMod(t *testing.T) {
	for _, name := range []string{"Core", "Config", "Plugins"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, name, "file.dll")
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))

			got := core.DetectModName(&domain.Game{ID: "no-loader"}, dir, name+"-1.0.zip")
			assert.Equal(t, name, got, "a non-BepInEx game keeps the sole-top-level-directory rule")
		})
	}
}

// TestImportArchive_ABepInExRootedPackageIsNamedAfterItself runs a real
// package through both halves - the plan names the mod from the archive's
// listing, the ingest from the extracted tree - and both must agree.
func TestImportArchive_ABepInExRootedPackageIsNamedAfterItself(t *testing.T) {
	for name, tc := range bepinexNamingCases {
		t.Run(name, func(t *testing.T) {
			svc, game := newBepInExDeclaredService(t)
			_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), game.ID)
			require.NoError(t, err)
			bepinexInstall(t, game.InstallPath, "", domain.LoaderBootstrapUnknown, time.Time{})

			files := map[string]string{}
			for _, m := range tc.members {
				files[m] = "x"
			}
			archivePath := filepath.Join(t.TempDir(), tc.archive)
			createImportTestZip(t, archivePath, files)

			plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
			require.NoError(t, err)
			assert.Equal(t, tc.want, plan.Mod.Name, "the plan's name")

			result, err := svc.ApplyImportArchive(context.Background(), game, "default", plan, core.ImportArchiveOptions{}, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.want, result.Mod.Name, "the recorded name")
		})
	}
}

// TestImportArchive_NonBepInExGame_SoleTopLevelDirectoryStillNamesTheMod
// (#450): a game with no BepInEx declared, no BepInEx adapter, and no
// BepInEx installed on disk applies the OLD sole-top-level-directory rule
// unconditionally - a mod genuinely named "Core" ("Core-1.0.zip" containing
// "Core/...") must import as "Core", not fall through to the archive's
// whole filename the way a BepInEx game's structure-directory case does.
func TestImportArchive_NonBepInExGame_SoleTopLevelDirectoryStillNamesTheMod(t *testing.T) {
	svc, game := newBepInExGameRootService(t)
	_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), game.ID)
	require.NoError(t, err)

	archivePath := filepath.Join(t.TempDir(), "Core-1.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"Core/mod.dll": "x"})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.NoError(t, err)
	assert.Equal(t, "Core", plan.Mod.Name, "the plan's name")

	result, err := svc.ApplyImportArchive(context.Background(), game, "default", plan, core.ImportArchiveOptions{}, nil)
	require.NoError(t, err)
	assert.Equal(t, "Core", result.Mod.Name, "the recorded name")
}
