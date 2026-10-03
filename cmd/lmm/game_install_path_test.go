package main

// #528: `lmm game edit --install-path` - the CLI half of GameEdit.InstallPath.
// core owns the policy (internal/core/game_install_path_test.go); these pin
// the flag, its "~" expansion, the console lines, the --json row and the
// refusal's envelope.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoGameEdit_InstallPathMovesTheModPathWithIt(t *testing.T) {
	svc, game := setupFreshModPathGame(t)
	fixed := t.TempDir()
	gameEditInstallPath, gameEditInstallPathSet = fixed, true

	out := captureStdout(t, func() error { return doGameEdit(context.Background(), svc, game.ID, false) })
	assert.Contains(t, out, "Human Host install path set to "+fixed)
	assert.Contains(t, out, "Human Host mod path moved with it to "+filepath.Join(fixed, "mods"))

	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, fixed, reloaded.InstallPath)
	assert.Equal(t, filepath.Join(fixed, "mods"), reloaded.ModPath)
}

func TestGameEdit_InstallPathFlagExpandsTilde(t *testing.T) {
	setupAdapterWarningGames(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(home, "Games", "Valheim")
	require.NoError(t, os.MkdirAll(target, 0o755))

	stdout, _ := runLMM(t, "game", "edit", "valheim", "--install-path", "~/Games/Valheim")
	assert.Contains(t, stdout, "install path set to "+target)
}

func TestDoGameEdit_InstallPathJSONEmitsTheGameListRow(t *testing.T) {
	svc, game := setupFreshModPathGame(t)
	withJSONOutput(t)
	fixed := t.TempDir()
	gameEditInstallPath, gameEditInstallPathSet = fixed, true

	out := captureStdout(t, func() error { return doGameEdit(context.Background(), svc, game.ID, false) })
	assert.Contains(t, out, `"install_path": "`+fixed+`"`)
}

func TestDoGameEdit_InstallPathMustExist(t *testing.T) {
	svc, game := setupFreshModPathGame(t)
	gameEditInstallPath, gameEditInstallPathSet = filepath.Join(t.TempDir(), "nope"), true

	err := doGameEdit(context.Background(), svc, game.ID, false)
	var spec *core.GameSpecError
	require.ErrorAs(t, err, &spec)
	assert.Equal(t, "install_path", spec.Field)
}

func TestDoGameEdit_InstallPathAndLoaderAreSeparateEdits(t *testing.T) {
	svc, game := setupFreshModPathGame(t)
	gameEditInstallPath, gameEditInstallPathSet = t.TempDir(), true
	err := doGameEditLoader(context.Background(), svc, game.ID, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--install-path")
}

func TestReportError_JSON_GameInstallPathInUseError(t *testing.T) {
	withJSONOutput(t)

	err := &core.GameInstallPathInUseError{
		GameID: "g1", InstallPath: "/g", NewInstallPath: "/h", ModPath: "/g/mods", NewModPath: "/h/mods",
		DeployedFiles: 2, Profiles: []core.ProfileDeployedFiles{{Profile: "default", DeployedFiles: 2}},
		OldInstallPathExists: true,
	}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"cannot change install_path for game g1 from /g to /h: lmm recorded 2 deployed file(s) under /g/mods against the current folder, and changes the install path with them only as a move of the whole folder, but /g still exists; purge them first - run `lmm purge --game g1 --profile default` - then change install_path, then run `lmm deploy --game g1`\",\n"+
		"  \"details\": {\n"+
		"    \"game_id\": \"g1\",\n"+
		"    \"install_path\": \"/g\",\n"+
		"    \"new_install_path\": \"/h\",\n"+
		"    \"mod_path\": \"/g/mods\",\n"+
		"    \"new_mod_path\": \"/h/mods\",\n"+
		"    \"deployed_files\": 2,\n"+
		"    \"profiles\": [\n"+
		"      {\n"+
		"        \"profile\": \"default\",\n"+
		"        \"deployed_files\": 2\n"+
		"      }\n"+
		"    ],\n"+
		"    \"old_install_path_exists\": true\n"+
		"  }\n"+
		"}\n", out)
}
