package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetGameLoaderFlags zeroes the four --loader* globals for one test, the
// way resetGameEditFlags does for the source flags: they are bound by init()
// and would otherwise leak into every test after this one.
func resetGameLoaderFlags(t *testing.T) {
	t.Helper()
	add := [4]string{gameAddLoader, gameAddLoaderVersion, gameAddLoaderRuntime, gameAddLoaderBootstrap}
	edit := [4]string{gameEditLoader, gameEditLoaderVersion, gameEditLoaderRuntime, gameEditLoaderBootstrap}
	t.Cleanup(func() {
		gameAddLoader, gameAddLoaderVersion, gameAddLoaderRuntime, gameAddLoaderBootstrap = add[0], add[1], add[2], add[3]
		gameEditLoader, gameEditLoaderVersion, gameEditLoaderRuntime, gameEditLoaderBootstrap = edit[0], edit[1], edit[2], edit[3]
	})
	gameAddLoader, gameAddLoaderVersion, gameAddLoaderRuntime, gameAddLoaderBootstrap = "", "", "", ""
	gameEditLoader, gameEditLoaderVersion, gameEditLoaderRuntime, gameEditLoaderBootstrap = "", "", "", ""
}

func TestGameShowCmd_Structure(t *testing.T) {
	assert.Equal(t, "show <game-id>", gameShowCmd.Use)
	assert.NotEmpty(t, gameShowCmd.Short)
	assert.NotEmpty(t, gameShowCmd.Long)
}

// TestLoaderSpecFromFlags: --loader alone is a complete declaration (lmm
// reads the runtime and bootstrap off the install directory), and the three
// detail flags are refused on their own rather than silently ignored - a user
// who typed one has said something specific.
func TestLoaderSpecFromFlags(t *testing.T) {
	spec, err := loaderSpecFromFlags("", "", "", "")
	require.NoError(t, err)
	assert.Nil(t, spec)

	spec, err = loaderSpecFromFlags("bepinex", "5.4.23.5", "mono", "proton")
	require.NoError(t, err)
	require.NotNil(t, spec)
	assert.Equal(t, core.LoaderSpec{Kind: "bepinex", Version: "5.4.23.5", Runtime: "mono", Bootstrap: "proton"}, *spec)

	_, err = loaderSpecFromFlags("", "", "", "proton")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--loader")
}

// TestDoGameEditLoader_DeclaresAndClears is the round trip through the CLI:
// declare a loader, then take it away, with the printed line naming
// `lmm game show` as where the launch option comes from.
func TestDoGameEditLoader_DeclaresAndClears(t *testing.T) {
	svc := setupGameEditTest(t)
	resetGameLoaderFlags(t)

	gameEditLoader, gameEditLoaderVersion, gameEditLoaderBootstrap = "bepinex", "5.4.23.5", "proton"
	out := captureStdout(t, func() error {
		return doGameEditLoader(context.Background(), svc, "skyrim-se", false)
	})
	assert.Contains(t, out, "declares the bepinex loader")
	assert.Contains(t, out, "lmm game show skyrim-se")

	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	require.NotNil(t, game.Loader)
	assert.Equal(t, domain.LoaderBootstrapProton, game.Loader.Bootstrap)

	gameEditLoader, gameEditLoaderVersion, gameEditLoaderBootstrap = "", "", ""
	out = captureStdout(t, func() error {
		return doGameEditLoader(context.Background(), svc, "skyrim-se", false)
	})
	assert.Contains(t, out, "no longer declares a mod loader")
	game, err = svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Nil(t, game.Loader)
}

// The loader and the source map are separate edits: a run asking for both is
// refused rather than silently ordered, because a partial failure would
// otherwise be expressible.
func TestDoGameEditLoader_RefusesACombinedEdit(t *testing.T) {
	svc := setupGameEditTest(t)
	resetGameLoaderFlags(t)
	gameEditLoader = "bepinex"
	gameEditSources = []string{"local-mods=skyrim"}

	err := doGameEditLoader(context.Background(), svc, "skyrim-se", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "separately")
}

// TestDoGameEditLoader_JSONEmitsTheGameRow: Ruling 15 - the GameListEntry
// document and nothing else on stdout.
func TestDoGameEditLoader_JSONEmitsTheGameRow(t *testing.T) {
	svc := setupGameEditTest(t)
	resetGameLoaderFlags(t)
	withJSONOutput(t)
	gameEditLoader = "bepinex"

	out := captureStdout(t, func() error {
		return doGameEditLoader(context.Background(), svc, "skyrim-se", false)
	})
	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal([]byte(out), &entry))
	require.NotNil(t, entry.Loader)
	assert.Equal(t, "bepinex", entry.Loader.Kind)
}

// TestDoGameShow_PrintsTheLaunchOptionToPaste is the whole point of the
// command: the exact string, and a note that lmm will not write it.
func TestDoGameShow_PrintsTheLaunchOptionToPaste(t *testing.T) {
	svc := setupGameEditTest(t)
	install := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(install, "UnityPlayer.dll"), []byte("pe"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(install, "Game_Data", "Managed"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(install, "Game_Data", "Managed", "Assembly-CSharp.dll"), []byte("x"), 0o644))
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "valheim", Name: "Valheim", InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"nexusmods": "valheim"},
		Loader:    &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5"},
	}))

	out := captureStdout(t, func() error {
		return doGameShow(context.Background(), svc, "valheim")
	})
	assert.Contains(t, out, "mods deploy into the game root")
	assert.Contains(t, out, "bepinex 5.4.23.5")
	assert.Contains(t, out, "mono")
	assert.Contains(t, out, `WINEDLLOVERRIDES="winhttp=n,b" %command%`)
	assert.Contains(t, out, "lmm never writes it for you")
	assert.Contains(t, out, "the preloader is not in the game directory")
}

// A game with no loader still gets the section, because "which build and
// which launch option would this game need" is exactly what someone about to
// install BepInEx is asking.
func TestDoGameShow_UndeclaredGameStillPrintsGuidance(t *testing.T) {
	svc := setupGameEditTest(t)
	install := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(install, "UnityPlayer.so"), []byte("elf"), 0o644))
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "native-game", Name: "Native Game", InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"nexusmods": "native"},
	}))

	out := captureStdout(t, func() error {
		return doGameShow(context.Background(), svc, "native-game")
	})
	assert.Contains(t, out, "Declared:")
	assert.Contains(t, out, "none")
	assert.Contains(t, out, "./run_bepinex.sh %command%")
}

// TestDoGameShow_JSONEmitsTheDetailDocument: Ruling 15, one document.
func TestDoGameShow_JSONEmitsTheDetailDocument(t *testing.T) {
	svc := setupGameEditTest(t)
	withJSONOutput(t)

	out := captureStdout(t, func() error {
		return doGameShow(context.Background(), svc, "skyrim-se")
	})
	var detail core.GameDetail
	require.NoError(t, json.Unmarshal([]byte(out), &detail))
	assert.Equal(t, "skyrim-se", detail.ID)
	require.NotNil(t, detail.Loader)
	assert.Equal(t, "skyrim-se", detail.Loader.GameID)
}

// An unknown game is core's own domain.ErrGameNotFound, not a CLI sentence.
func TestDoGameShow_UnknownGame(t *testing.T) {
	svc := setupGameEditTest(t)
	err := doGameShow(context.Background(), svc, "nope")
	assert.ErrorIs(t, err, domain.ErrGameNotFound)
}

// TestDoGameShow_SkipsTheLoaderSectionForAGameWithNoLoaderInSight is review
// F7's CLI half: `lmm game show icarus` printed a BepInEx-flavoured "Mod
// loader" section - Declared: none, Runtime: unknown, Bootstrap: unknown -
// and then advised setting `--loader-bootstrap` on an Unreal game that
// neither has nor needs a loader.
func TestDoGameShow_SkipsTheLoaderSectionForAGameWithNoLoaderInSight(t *testing.T) {
	svc := setupGameEditTest(t)
	install := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(install, "Content", "Paks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(install, "Content", "Paks", "game.pak"), []byte("pak"), 0o644))
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"icarus": "icarus"},
	}))

	out := captureStdout(t, func() error {
		return doGameShow(context.Background(), svc, "icarus")
	})
	assert.Contains(t, out, "Install path:", "the rest of the game document still prints")
	assert.NotContains(t, out, "Mod loader")
	assert.NotContains(t, out, "--loader-bootstrap")
}

// TestDoGameShow_AnInstalledButUndeclaredLoaderSaysTheDeclarationIsMissing is
// re-review R3's CLI half. BepInEx is in the game directory and has run, but
// the game declares nothing - so `lmm game show` printed no loader section at
// all, when the one thing it had to say ("declare it") was the actionable
// one.
func TestDoGameShow_AnInstalledButUndeclaredLoaderSaysTheDeclarationIsMissing(t *testing.T) {
	svc := setupGameEditTest(t)
	install := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(install, "BepInEx", "core"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(install, "BepInEx", "core", "BepInEx.Preloader.dll"), []byte("pe"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(install, "BepInEx", "LogOutput.log"), []byte("log"), 0o644))
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "mid-setup", Name: "Mid Setup", InstallPath: install, ModPath: install,
		SourceIDs: map[string]string{"nexusmods": "mid-setup"},
	}))

	out := captureStdout(t, func() error {
		return doGameShow(context.Background(), svc, "mid-setup")
	})
	assert.Contains(t, out, "Mod loader", "the section is exactly what this user needs")
	assert.Contains(t, out, "does not declare it")
	assert.Contains(t, out, "--loader bepinex")
}
