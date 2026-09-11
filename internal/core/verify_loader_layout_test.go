package core_test

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

// stalePreFixJotunn reproduces the owner's actual state (#424), by the route
// that actually produced it: Jotunn is installed while the game declares no
// loader and lmm has no plugin-folder shape, so the archive extracts
// verbatim - the cache entry holds Jotunn/Jotunn.dll, the deployed file sits
// in the game ROOT, and the deployed_files row records Jotunn/Jotunn.dll.
// THEN the loader is declared, which is what #416 did to every curated
// BepInEx game.
//
// It is built through the real flows rather than by seeding the DB, so the
// cache, the disk and the row agree the way they would on a user's machine.
func stalePreFixJotunn(t *testing.T, profiles ...string) (*core.Service, *domain.Game, *domain.Mod) {
	t.Helper()
	if len(profiles) == 0 {
		profiles = []string{"default"}
	}
	svc, game := newBepInExGameRootService(t)
	pm := svc.NewProfileManager()

	archivePath := filepath.Join(t.TempDir(), "Jotunn-2.30.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"Jotunn/Jotunn.dll": "assembly",
		"Jotunn/Jotunn.xml": "<doc/>",
	})

	var mod *domain.Mod
	for _, profile := range profiles {
		if profile != "default" {
			_, err := pm.Create(context.Background(), game.ID, profile)
			require.NoError(t, err)
		}
		// An explicit identity, so every profile shares ONE cache entry -
		// which is the whole point of the sibling-profile case.
		result, err := svc.ImportArchive(context.Background(), game, profile, archivePath,
			core.ImportArchiveOptions{SourceID: domain.SourceLocal, ModID: "1138", Force: true}, nil)
		require.NoError(t, err)
		mod = result.Mod
	}

	// The pre-fix reality, asserted so this fixture cannot silently start
	// building a state that is already correct.
	require.Equal(t, []string{"Jotunn/Jotunn.dll", "Jotunn/Jotunn.xml"},
		gameTreeForTest(t, game.InstallPath))
	for _, profile := range profiles {
		recorded, err := svc.GetDeployedFilesForMod(context.Background(), game.ID, profile,
			mod.SourceID, mod.ID)
		require.NoError(t, err)
		require.Contains(t, recorded, filepath.FromSlash("Jotunn/Jotunn.dll"),
			"profile %s must record the pre-fix path", profile)
	}

	// ...and then the game learns it is a BepInEx game.
	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapProton, time.Now())
	game.Loader = &domain.GameLoader{
		Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5",
		Bootstrap: domain.LoaderBootstrapProton,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	return svc, game, mod
}

// TestVerify_LoaderTier_ReportsADeploymentOutsideBepInEx is #424's third
// half: the fix stops the bug happening again, but it does nothing for the
// installs that already happened. A plugin deployed into the game root is
// not missing, not stale by any existing test, and loads nothing - only the
// loader tier can see it, because only it knows where a plugin belongs.
func TestVerify_LoaderTier_ReportsADeploymentOutsideBepInEx(t *testing.T) {
	svc, game, _ := stalePreFixJotunn(t)

	res, err := svc.VerifyReport(context.Background(), game, "default",
		core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)

	f := findingWithStatus(res.Result, "loader_deployed_outside_loader")
	require.NotNil(t, f, "statuses were %v", findingStatuses(res.Result))
	assert.Contains(t, f.Note, "Jotunn/Jotunn.dll")
	assert.True(t, f.Fixable, "--fix can re-lay this out and re-deploy it")
}

// ...and --fix repairs it end to end: the cache entry is re-laid out under
// BepInEx/plugins/, the deployed_files rows are rewritten, the plugin is
// linked where the loader reads it, nothing is left in the game root, and a
// second run is clean.
func TestVerify_LoaderTier_FixRelaysOutAndRedeploysADeploymentOutsideBepInEx(t *testing.T) {
	svc, game, mod := stalePreFixJotunn(t)

	fixed, err := svc.VerifyReport(context.Background(), game, "default",
		core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	assert.NotNil(t, findingWithStatus(fixed.Result, "fixed_loader_deployed_outside_loader"),
		"statuses were %v", findingStatuses(fixed.Result))

	cached, err := svc.GetGameCache(game).ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	slashed := make([]string, 0, len(cached))
	for _, c := range cached {
		slashed = append(slashed, filepath.ToSlash(c))
	}
	assert.ElementsMatch(t, []string{
		"BepInEx/plugins/Jotunn/Jotunn.dll",
		"BepInEx/plugins/Jotunn/Jotunn.xml",
	}, slashed, "the cache entry IS the game directory's layout, so it is what has to change")

	recorded, err := svc.GetDeployedFilesForMod(context.Background(), game.ID, "default",
		mod.SourceID, mod.ID)
	require.NoError(t, err)
	slashedRows := make([]string, 0, len(recorded))
	for _, p := range recorded {
		slashedRows = append(slashedRows, filepath.ToSlash(p))
	}
	assert.ElementsMatch(t, []string{
		"BepInEx/plugins/Jotunn/Jotunn.dll",
		"BepInEx/plugins/Jotunn/Jotunn.xml",
	}, slashedRows, "`lmm mod files` reads these rows")

	_, statErr := os.Lstat(filepath.Join(game.InstallPath, "BepInEx", "plugins", "Jotunn", "Jotunn.dll"))
	assert.NoError(t, statErr, "and the plugin is where the loader reads it")
	_, statErr = os.Lstat(filepath.Join(game.InstallPath, "Jotunn"))
	assert.True(t, os.IsNotExist(statErr), "with nothing left in the game root")

	again, err := svc.VerifyReport(context.Background(), game, "default",
		core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(again.Result, "loader_deployed_outside_loader"),
		"the second run is clean; statuses were %v", findingStatuses(again.Result))
}

// A cache entry whose layout the normaliser would NOT rewrite cannot be
// repaired by re-deploying it: the deploy would put the files straight back
// where they are. Such a row reports with the remedy that does work rather
// than claiming a repair it cannot make.
func TestVerify_LoaderTier_ARepairItCannotMakeIsNotClaimed(t *testing.T) {
	svc, game := newBepInExGameRootService(t)

	// A root that mixes a DLL folder with loose files: shape F refuses to
	// guess at it, so there is nothing for a re-layout to do.
	archivePath := filepath.Join(t.TempDir(), "Mixed-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"Thing/Thing.dll":     "assembly",
		"install-by-hand.txt": "copy me",
	})
	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapProton, time.Now())
	game.Loader = &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Bootstrap: domain.LoaderBootstrapProton}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	res, err := svc.VerifyReport(context.Background(), game, "default",
		core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)

	f := findingWithStatus(res.Result, "loader_deployed_outside_loader")
	require.NotNil(t, f, "statuses were %v", findingStatuses(res.Result))
	assert.False(t, f.Fixable, "a re-deploy would put the files straight back")
	assert.Contains(t, f.FixableReason, "re-import")
}

// A correctly deployed BepInEx mod reports nothing, like every other check
// in this engine.
func TestVerify_LoaderTier_ACorrectlyPlacedPluginIsNotAFinding(t *testing.T) {
	svc, game := newVerifyLoaderService(t, &domain.GameLoader{
		Kind: domain.LoaderKindBepInEx, Bootstrap: domain.LoaderBootstrapNative,
	})
	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapNative, time.Now())

	archivePath := filepath.Join(t.TempDir(), "Jotunn-2.30.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"Jotunn/Jotunn.dll": "assembly",
		"manifest.json":     "{}",
	})
	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	res, err := svc.VerifyReport(context.Background(), game, "default",
		core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(res.Result, "loader_deployed_outside_loader"),
		"statuses were %v", findingStatuses(res.Result))
}

// TestVerify_LoaderTier_FixRelaysOutEveryProfileSharingTheCacheEntry is the
// guard the repair needs to be allowed to touch the cache at all: the entry
// is shared by every profile of the game holding that version, so moving
// its files out from under a sibling's deployment would leave that profile
// linked to paths nothing provides any more.
func TestVerify_LoaderTier_FixRelaysOutEveryProfileSharingTheCacheEntry(t *testing.T) {
	svc, game, mod := stalePreFixJotunn(t, "default", "second")

	_, err := svc.VerifyReport(context.Background(), game, "default",
		core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)

	for _, profile := range []string{"default", "second"} {
		rows, err := svc.GetDeployedFilesForMod(context.Background(), game.ID, profile,
			mod.SourceID, mod.ID)
		require.NoError(t, err)
		slashed := make([]string, 0, len(rows))
		for _, p := range rows {
			slashed = append(slashed, filepath.ToSlash(p))
		}
		assert.ElementsMatch(t, []string{
			"BepInEx/plugins/Jotunn/Jotunn.dll",
			"BepInEx/plugins/Jotunn/Jotunn.xml",
		}, slashed, "profile %s must be re-linked, not left pointing at the old layout", profile)
	}
}

// TestVerify_LoaderTier_ContentThatIsNotAPluginIsNotAFinding: for a BepInEx
// game mod_path IS the game root, so a mod that legitimately writes into the
// game's own directories has every one of its files "outside BepInEx/". The
// check is about an ASSEMBLY nothing will load, not about a path - a mod
// with no assembly outside BepInEx/ is not a misplaced plugin, and telling
// its owner otherwise would put a permanent finding on a working install
// whose "remedy" would do nothing.
func TestVerify_LoaderTier_ContentThatIsNotAPluginIsNotAFinding(t *testing.T) {
	svc, game := newVerifyLoaderService(t, &domain.GameLoader{
		Kind: domain.LoaderKindBepInEx, Bootstrap: domain.LoaderBootstrapNative,
	})
	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapNative, time.Now())

	archivePath := filepath.Join(t.TempDir(), "Textures-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"valheim_Data/textures/rock.bundle": "bytes",
		"valheim_Data/textures/tree.bundle": "bytes",
	})
	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	res, err := svc.VerifyReport(context.Background(), game, "default",
		core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(res.Result, "loader_deployed_outside_loader"),
		"statuses were %v", findingStatuses(res.Result))
}

// TestVerify_LoaderTier_AGameOwnedDirectoryIsNeverMisplaced is #424 review
// finding 1's other direction, by the route that actually produces it
// (#416): a patch replacing one of the game's OWN managed assemblies is
// installed before the loader is declared, so it deploys verbatim; then the
// loader is declared.
//
// "An assembly outside BepInEx/" is true of it, and it is exactly where it
// belongs - the game's engine reads <Game>_Data/Managed/, and BepInEx never
// will. A finding here would be permanent, and a --fix acting on it would
// silently revert the game to stock behaviour while reporting a repair.
func TestVerify_LoaderTier_AGameOwnedDirectoryIsNeverMisplaced(t *testing.T) {
	svc, game := newBepInExGameRootService(t)
	seedGameOwnedTree(t, game.InstallPath,
		"valheim_Data/Managed/UnityEngine.dll",
		"valheim_Data/Managed/Assembly-CSharp.dll",
		"valheim_Data/resources.assets",
	)

	archivePath := filepath.Join(t.TempDir(), "Patch-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"valheim_Data/Managed/Assembly-CSharp.dll": "patched assembly",
	})
	_, err := svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)

	// ...and then the game learns it is a BepInEx game.
	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapProton, time.Now())
	game.Loader = &domain.GameLoader{
		Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5",
		Bootstrap: domain.LoaderBootstrapProton,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	before := gameTreeForTest(t, game.InstallPath)
	require.Contains(t, before, "valheim_Data/Managed/Assembly-CSharp.dll")

	res, err := svc.VerifyReport(context.Background(), game, "default",
		core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(res.Result, "loader_deployed_outside_loader"),
		"a game-data assembly patch is where it belongs; statuses were %v", findingStatuses(res.Result))

	fixed, err := svc.VerifyReport(context.Background(), game, "default",
		core.VerifyOptions{Fix: true, Force: true}, nil)
	require.NoError(t, err)
	assert.Nil(t, findingWithStatus(fixed.Result, "fixed_loader_deployed_outside_loader"),
		"and --fix has nothing to repair; statuses were %v", findingStatuses(fixed.Result))
	assert.Equal(t, before, gameTreeForTest(t, game.InstallPath),
		"--fix must not move the game's own assembly under BepInEx/plugins/")
}

// TestVerify_LoaderTier_TheRemedyDoesNotReproduceTheProblem is #424 review
// finding 2's second half. An archive whose root carries `BepInEx/` beside
// a plugin folder deploys that folder into the game root; the finding
// reports it as not fixable and used to name "re-import the archive (or
// reinstall the mod) so the layout rules run over a fresh copy of it" - a
// remedy that re-runs the identical ingest and reproduces the identical
// deployment. A permanent dead end for the user.
//
// The re-import is performed here rather than argued about, so the remedy
// this row names can never drift back to one that does nothing.
func TestVerify_LoaderTier_TheRemedyDoesNotReproduceTheProblem(t *testing.T) {
	svc, game := newBepInExDeclaredService(t)
	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapProton, time.Now())

	archivePath := filepath.Join(t.TempDir(), "Sibling-1.0.0.zip")
	createImportTestZip(t, archivePath, map[string]string{
		"BepInEx/patchers/Pre.dll": "patcher",
		"Jotunn/Jotunn.dll":        "assembly",
	})

	plan, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Warnings, "a root lmm cannot read is never placed silently")
	assert.Contains(t, plan.Warnings[0], "did not recognise")

	_, err = svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: domain.SourceLocal, ModID: "77", Force: true}, nil)
	require.NoError(t, err)
	deployed := gameTreeForTest(t, game.InstallPath)
	require.Contains(t, deployed, "Jotunn/Jotunn.dll")

	res, err := svc.VerifyReport(context.Background(), game, "default",
		core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	f := findingWithStatus(res.Result, "loader_deployed_outside_loader")
	require.NotNil(t, f, "statuses were %v", findingStatuses(res.Result))
	require.False(t, f.Fixable, "lmm cannot place this layout on its own")

	// The same archive, imported again, lands in exactly the same place...
	_, err = svc.ImportArchive(context.Background(), game, "default", archivePath,
		core.ImportArchiveOptions{SourceID: domain.SourceLocal, ModID: "77", Force: true}, nil)
	require.NoError(t, err)
	require.Equal(t, deployed, gameTreeForTest(t, game.InstallPath),
		"re-importing the same archive reproduces the deployment")

	// ...so the remedy must not be "re-import it and the rules will run".
	assert.NotContains(t, f.FixableReason, "so the layout rules run over a fresh copy")
	assert.Contains(t, f.FixableReason, "BepInEx/plugins/",
		"the remedy has to name where the files actually have to go")
}
