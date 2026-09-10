package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #415 (found by the #406 review, F2). linker.CleanupEmptyDirs used to walk
// the WHOLE tree under game.ModPath and remove every empty directory it
// found - not only the ones lmm's own deploy created. For a game whose mod
// root IS the install root (`mod_path: ""` - Cyberpunk 2077 as curated by
// #406, and the pre-existing hearts-of-iron-iv, euro-truck-simulator-2 and
// call-of-duty-black-ops-6 entries) every uninstall and every purge swept
// the game's own empty directories away, including the ones its loaders
// expect to exist. Steam's verify-integrity does not restore an empty
// directory, so it was not self-healing either.

// cyberpunkLoaderDirs is the set the review proved gone, and the same five
// #415's own text names: CET's plugin folder, redscript's script and tweak
// folders, the archive mod folder and REDmod's - all shipped empty by their
// installers, all directly under the install root, which for Cyberpunk IS
// the mod root.
var cyberpunkLoaderDirs = []string{
	filepath.Join("bin", "x64", "plugins"),
	filepath.Join("r6", "scripts"),
	filepath.Join("r6", "tweaks"),
	filepath.Join("archive", "pc", "mod"),
	filepath.Join("tools", "redmod", "mods"),
}

// cyberpunkInstall fabricates that install: the mod root is the install
// root, and the four loader directories sit under it, empty.
func cyberpunkInstall(t *testing.T) *domain.Game {
	t.Helper()
	install := t.TempDir()
	for _, dir := range cyberpunkLoaderDirs {
		require.NoError(t, os.MkdirAll(filepath.Join(install, dir), 0o755))
	}
	return &domain.Game{ID: "cyberpunk2077", Name: "Cyberpunk 2077",
		InstallPath: install, ModPath: install, LinkMethod: domain.LinkSymlink}
}

// assertLoaderDirsSurvive is the claim #415 is about: nothing lmm removed
// lived in these, so nothing lmm does may remove them.
func assertLoaderDirsSurvive(t *testing.T, game *domain.Game) {
	t.Helper()
	for _, dir := range cyberpunkLoaderDirs {
		info, err := os.Stat(filepath.Join(game.ModPath, dir))
		if assert.NoError(t, err, "the game's own %s must survive lmm's cleanup", dir) {
			assert.True(t, info.IsDir())
		}
	}
}

// TestService_Uninstall_LeavesTheGamesOwnEmptyDirectoriesAlone: an
// uninstall of a mod that never touched those directories must not remove
// them.
func TestService_Uninstall_LeavesTheGamesOwnEmptyDirectoriesAlone(t *testing.T) {
	svc := newFlowsTestService(t)
	game := cyberpunkInstall(t)

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true,
		map[string][]byte{filepath.Join("mods", "coolmod", "cool.archive"): []byte("a")})
	installSeededMod(t, svc, game, "a")

	require.NoError(t, svc.GetInstallerForTest(game).Uninstall(context.Background(), game,
		&domain.Mod{ID: "a", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	assertLoaderDirsSurvive(t, game)
}

// TestService_PurgeProfile_LeavesTheGamesOwnEmptyDirectoriesAlone: the same
// claim for the other call site (`lmm purge`), which swept once more after
// the per-mod uninstalls.
func TestService_PurgeProfile_LeavesTheGamesOwnEmptyDirectoriesAlone(t *testing.T) {
	svc := newFlowsTestService(t)
	game := cyberpunkInstall(t)

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true,
		map[string][]byte{filepath.Join("mods", "coolmod", "cool.archive"): []byte("a")})
	installSeededMod(t, svc, game, "a")

	mods, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)

	_, err = svc.PurgeProfile(context.Background(), game, "default", mods, core.PurgeOptions{}, nil)
	require.NoError(t, err)

	assertLoaderDirsSurvive(t, game)
}

// TestService_Uninstall_PrunesTheDirectoriesItsOwnDeployCreated is the
// other half of the contract: the bound must not turn into "prune
// nothing". Every directory on a removed file's ancestor chain that is
// empty afterwards still goes, up to - never including - the mod root.
func TestService_Uninstall_PrunesTheDirectoriesItsOwnDeployCreated(t *testing.T) {
	svc := newFlowsTestService(t)
	game := cyberpunkInstall(t)

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true,
		map[string][]byte{filepath.Join("mods", "coolmod", "deep", "cool.archive"): []byte("a")})
	installSeededMod(t, svc, game, "a")
	require.DirExists(t, filepath.Join(game.ModPath, "mods", "coolmod", "deep"))

	require.NoError(t, svc.GetInstallerForTest(game).Uninstall(context.Background(), game,
		&domain.Mod{ID: "a", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	assert.NoDirExists(t, filepath.Join(game.ModPath, "mods", "coolmod", "deep"))
	assert.NoDirExists(t, filepath.Join(game.ModPath, "mods", "coolmod"))
	assert.NoDirExists(t, filepath.Join(game.ModPath, "mods"))
	assert.DirExists(t, game.ModPath, "the mod root itself is never removed")
	assertLoaderDirsSurvive(t, game)
}

// TestService_Uninstall_KeepsADirectoryAnotherDeployedModStillUses: the
// walk stops at the first directory that is not empty, so a shared folder
// survives until the last mod in it goes.
func TestService_Uninstall_KeepsADirectoryAnotherDeployedModStillUses(t *testing.T) {
	svc := newFlowsTestService(t)
	game := cyberpunkInstall(t)
	shared := filepath.Join("mods", "shared")

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true,
		map[string][]byte{filepath.Join(shared, "a.archive"): []byte("a")})
	seedNamedInstalledMod(t, svc, game, "src", "b", "Mod B", "1.0", true,
		map[string][]byte{filepath.Join(shared, "b.archive"): []byte("b")})
	installSeededMod(t, svc, game, "a")
	installSeededMod(t, svc, game, "b")

	require.NoError(t, svc.GetInstallerForTest(game).Uninstall(context.Background(), game,
		&domain.Mod{ID: "a", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))

	assert.DirExists(t, filepath.Join(game.ModPath, shared), "Mod B is still deployed there")
	_, err := os.Lstat(filepath.Join(game.ModPath, shared, "b.archive"))
	assert.NoError(t, err)

	require.NoError(t, svc.GetInstallerForTest(game).Uninstall(context.Background(), game,
		&domain.Mod{ID: "b", SourceID: "src", Version: "1.0", GameID: game.ID}, "default"))
	assert.NoDirExists(t, filepath.Join(game.ModPath, shared), "the last mod took the directory with it")
}

// TestApplyProfileSwitch_StopsAtASymlinkedDirectoryOnTheChain is the #415
// re-review's I1 at the flow level, and it is deliberately NOT an
// uninstall: every deploy-direction flow prunes now (deploy.go, switch.go,
// mod_toggle.go, merged_pak.go, install.go's replace, profile_apply.go,
// verify_repair.go), all through Installer.Uninstall's removal set. So an
// ordinary `lmm profile switch` reaches the symlink case, and a user who
// keeps their mod folder on another drive - `mods -> /mnt/ssd/gamemods`,
// ordinary practice - would have lost the far-side directory and then the
// symlink itself, which is exactly #415's harm through the one door the
// bound left open.
func TestApplyProfileSwitch_StopsAtASymlinkedDirectoryOnTheChain(t *testing.T) {
	svc := newFlowsTestService(t)
	game := cyberpunkInstall(t)

	// The user's mod folder lives on another drive.
	elsewhere := t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(game.ModPath, "mods")))

	seedNamedInstalledMod(t, svc, game, "src", "a", "Mod A", "1.0", true,
		map[string][]byte{filepath.Join("mods", "coolmod", "cool.archive"): []byte("a")})
	installSeededMod(t, svc, game, "a")
	require.DirExists(t, filepath.Join(elsewhere, "coolmod"), "deploy wrote through the symlink")

	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.UpsertMod(context.Background(), game.ID, "default",
		domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0"}))
	_, err = pm.Create(context.Background(), game.ID, "other")
	require.NoError(t, err)

	plan, err := svc.PlanProfileSwitch(context.Background(), game, "other")
	require.NoError(t, err)
	require.Len(t, plan.ToDisable, 1, "the switch must undeploy Mod A - that is what reaches the prune")
	_, err = svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)

	assert.DirExists(t, filepath.Join(elsewhere, "coolmod"),
		"the switch undeployed the file, but nothing on the far side of the symlink is lmm's to remove")
	info, lerr := os.Lstat(filepath.Join(game.ModPath, "mods"))
	if assert.NoError(t, lerr, "the user's symlink must survive the switch") {
		assert.NotZero(t, info.Mode()&os.ModeSymlink, "and must still BE a symlink")
	}
	assertLoaderDirsSurvive(t, game)
}
