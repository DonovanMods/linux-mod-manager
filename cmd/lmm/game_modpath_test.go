package main

// #427: a game whose mod_path no longer exists is flagged by every command
// that shows the game, and `lmm game edit --mod-path` repairs it. #456: the
// load-time adapter warning is one short line, and its remedies are
// commands.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupFreshModPathGame is setupGameEditTest plus a game whose mod_path is
// <install>/mods, a directory nobody created - where every newly added game
// starts, since the first deploy creates it.
func setupFreshModPathGame(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc := setupGameEditTest(t)
	root := t.TempDir()
	game := &domain.Game{
		ID: "human-host", Name: "Human Host",
		InstallPath: root, ModPath: filepath.Join(root, "mods"),
		SourceIDs: map[string]string{"nexusmods": "humanhost"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), game.ID)
	require.NoError(t, err)
	return svc, game
}

// setupMissingModPathGame is the owner's #427 case: setupFreshModPathGame's
// game, deployed into, and then its mod directory gone - lmm's records
// point at files that are not there.
func setupMissingModPathGame(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	svc, game := setupFreshModPathGame(t)
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	deployOneFileForTest(t, svc, game, "default", "m1")
	require.NoError(t, os.RemoveAll(game.ModPath))
	return svc, game
}

// deployOneFileForTest deploys one cached file for game's profile through
// core's own deploy, so deployed_files holds a row under its mod_path.
func deployOneFileForTest(t *testing.T, svc *core.Service, game *domain.Game, profile, modID string) {
	t.Helper()
	ctx := context.Background()
	mod := &domain.Mod{ID: modID, SourceID: "nexusmods", Name: "Mod " + modID, Version: "1.0", GameID: game.ID}
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, mod.SourceID, mod.ID, mod.Version, modID+".esp", []byte("x")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: *mod, ProfileName: profile, UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, profile,
		domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version}))
	_, err := svc.DeployProfile(ctx, game, profile, core.DeployOptions{}, nil)
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(game.ModPath, modID+".esp"))
	require.NoError(t, err, "fixture: the file is deployed")
}

// commandWithContext is a bare command carrying the context cobra's
// ExecuteContext gives every real one - a game list reads the database
// since #427 (review F3), and a nil context is a panic there.
func commandWithContext() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

const missingModPathRepair = "lmm game edit human-host --mod-path <path>"

func TestDoGameShow_FlagsAMissingModPath(t *testing.T) {
	svc, game := setupMissingModPathGame(t)
	out := captureStdout(t, func() error { return doGameShow(context.Background(), svc, game.ID) })

	line := lineContaining(out, "does not exist")
	assert.Contains(t, line, game.ModPath)
	assert.Contains(t, line, missingModPathRepair)
}

func TestDoGameList_FlagsAMissingModPath(t *testing.T) {
	svc, game := setupMissingModPathGame(t)
	out, stderr, err := captureStdoutAndStderr(t, func() error { return doGameList(commandWithContext(), svc) })
	require.NoError(t, err)

	assert.Contains(t, lineContaining(out, "human-host"), game.ModPath+" (needs repair)")
	assert.NotContains(t, lineContaining(out, "skyrim-se"), "(needs repair)", "a game whose mod_path exists is not flagged")
	assert.Contains(t, lineContaining(stderr, "warning: human-host:"), missingModPathRepair,
		"the table is followed by the repair, on stderr so stdout stays one table")
	assert.NotContains(t, stderr, "skyrim-se")
}

func TestDoStatus_FlagsAMissingModPath(t *testing.T) {
	svc, game := setupMissingModPathGame(t)
	oldGame := gameID
	t.Cleanup(func() { gameID = oldGame })

	gameID = ""
	_, stderr, err := captureStdoutAndStderr(t, func() error { return doStatus(context.Background(), svc) })
	require.NoError(t, err)
	assert.Contains(t, lineContaining(stderr, "warning: human-host:"), missingModPathRepair, "the summary names the game that needs repair")
	assert.NotContains(t, stderr, "skyrim-se")

	gameID = game.ID
	out := captureStdout(t, func() error { return doStatus(context.Background(), svc) })
	assert.Contains(t, lineContaining(out, "does not exist"), missingModPathRepair)
}

func TestPrintDetectedGameRow_MarksAConfiguredGameWhoseModPathIsMissing(t *testing.T) {
	svc, game := setupMissingModPathGame(t)
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	existing := map[string]*domain.Game{game.ID: game}

	detected := domain.DetectedGame{Slug: game.ID, Name: game.Name, InstallPath: game.InstallPath, Known: true}
	needsRepair, err := gamesNeedingRepair(context.Background(), svc, existing)
	require.NoError(t, err)
	printDetectedGameRow(cmd, 1, detected, existing, needsRepair)
	assert.Contains(t, buf.String(), "[configured] [needs repair: see `lmm game show human-host`]")

	buf.Reset()
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	needsRepair, err = gamesNeedingRepair(context.Background(), svc, existing)
	require.NoError(t, err)
	printDetectedGameRow(cmd, 1, detected, existing, needsRepair)
	assert.Contains(t, buf.String(), "[configured]")
	assert.NotContains(t, buf.String(), "needs repair")
}

// TestFreshGame_NoCommandSaysItIsBroken (#427 review F3): a game nobody has
// deployed to has no mod directory yet - `lmm game add` leaves it to the
// first deploy - and every surface used to call it broken: "(missing)" in
// `game list`, a stderr warning from `game list` and `status`, a red line
// in `game show`, and "[needs repair]" in `game detect`. Nothing is wrong,
// so nothing says otherwise; `game show` notes it quietly.
func TestFreshGame_NoCommandSaysItIsBroken(t *testing.T) {
	setupAdapterWarningGames(t)
	install := t.TempDir()
	_, stderr := runLMM(t, "game", "add", "--source", "thunderstore", "--id", "valheim",
		"--game-id", "fresh", "--name", "Fresh Game", "--path", install)
	assert.NotContains(t, stderr, "does not exist")
	modPath := filepath.Join(install, "mods")
	_, err := os.Stat(modPath)
	require.ErrorIs(t, err, os.ErrNotExist, "fixture: game add leaves the mod directory to the first deploy")

	stdout, stderr := runLMM(t, "game", "list")
	assert.Contains(t, lineContaining(stdout, "fresh"), modPath)
	assert.NotContains(t, stdout, "needs repair")
	assert.NotContains(t, stderr, "fresh")

	stdout, stderr = runLMM(t, "status")
	assert.NotContains(t, stdout+stderr, "does not exist")

	stdout, stderr = runLMM(t, "game", "show", "fresh")
	assert.NotContains(t, stdout+stderr, "does not exist")
	assert.Contains(t, stdout, "not created yet - the first deploy creates it")

	stdout, _ = runLMM(t, "game", "list", "--json")
	assert.NotContains(t, stdout, "mod_path_error")
}

func TestDoGameEdit_ModPathRepairsTheGame(t *testing.T) {
	svc, game := setupFreshModPathGame(t)
	gameEditModPath, gameEditModPathSet = game.InstallPath, true

	out := captureStdout(t, func() error { return doGameEdit(context.Background(), svc, game.ID, false) })
	assert.Contains(t, out, "Human Host mod path set to "+game.InstallPath)
	assert.NotContains(t, out, "does not exist")

	reloaded, err := svc.GetGame(game.ID)
	require.NoError(t, err)
	assert.Equal(t, game.InstallPath, reloaded.ModPath)
}

// TestDoGameEdit_ModPathSaysWhenTheDirectoryIsNotThereYet: an absent
// directory is accepted (a deploy creates it), and the run says so, quietly,
// rather than leaving the user to wonder where it is.
func TestDoGameEdit_ModPathSaysWhenTheDirectoryIsNotThereYet(t *testing.T) {
	svc, game := setupFreshModPathGame(t)
	target := filepath.Join(game.InstallPath, "BepInEx", "plugins")
	gameEditModPath, gameEditModPathSet = target, true

	out := captureStdout(t, func() error { return doGameEdit(context.Background(), svc, game.ID, false) })
	assert.Contains(t, out, "mod path set to "+target)
	assert.Contains(t, out, "not created yet - the first deploy creates it")
	assert.NotContains(t, out, "does not exist", "nothing is wrong with it")
}

func TestDoGameEdit_ModPathJSONEmitsTheGameListRow(t *testing.T) {
	svc, game := setupFreshModPathGame(t)
	withJSONOutput(t)
	gameEditModPath, gameEditModPathSet = game.InstallPath, true

	out := captureStdout(t, func() error { return doGameEdit(context.Background(), svc, game.ID, false) })
	assert.Contains(t, out, `"mod_path": "`+game.InstallPath+`"`)
	assert.NotContains(t, out, "mod_path_error")
}

// TestGameEdit_ModPathThenAdapterInOneRun is #456's first remedy as ONE
// command: the adapter edit runs after the mod_path edit, because bepinex
// is refused off the game root.
func TestGameEdit_ModPathThenAdapterInOneRun(t *testing.T) {
	setupAdapterWarningGames(t)
	svc, err := app.Open(context.Background(), app.Options{ConfigDir: configDir, DataDir: dataDir, WarnWriter: &strings.Builder{}})
	require.NoError(t, err)
	game, err := svc.GetGame("valheim")
	require.NoError(t, err)
	root := game.InstallPath
	plugins := filepath.Join(root, "BepInEx", "plugins")
	require.NoError(t, os.MkdirAll(plugins, 0o755))
	_, err = svc.SetGameModPath(context.Background(), "valheim", plugins)
	require.NoError(t, err)
	require.NoError(t, svc.Close())

	stdout, stderr := runLMM(t, "game", "edit", "valheim", "--mod-path", root, "--adapter", "bepinex")
	assert.Contains(t, stdout, "Valheim mod path set to "+root)
	assert.Contains(t, stdout, "Valheim adapter set to bepinex")
	assert.NotContains(t, stdout+stderr, valheimContradiction, "the run fixes the contradiction, so it does not print it")
}

func TestReportError_JSON_ModPathMissingError(t *testing.T) {
	withJSONOutput(t)

	err := &core.ModPathMissingError{GameID: "human-host", ModPath: "/g/mods", Reason: "does not exist", DeployedFiles: 2, SuggestedModPath: "/g"}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"mod_path /g/mods does not exist, but lmm recorded 2 deployed file(s) under it; a BepInEx game deploys into its root: purge them, then run `lmm game edit human-host --mod-path /g`, which names the purge each profile needs\",\n"+
		"  \"details\": {\n"+
		"    \"game_id\": \"human-host\",\n"+
		"    \"mod_path\": \"/g/mods\",\n"+
		"    \"reason\": \"does not exist\",\n"+
		"    \"deployed_files\": 2,\n"+
		"    \"suggested_mod_path\": \"/g\"\n"+
		"  }\n"+
		"}\n", out)
}

func TestReportError_JSON_GameModPathInUseError(t *testing.T) {
	withJSONOutput(t)

	err := &core.GameModPathInUseError{GameID: "g1", ModPath: "/g/mods", DeployedFiles: 2}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"2 file(s) are deployed under /g/mods, and lmm records each one relative to the mod_path; run `lmm purge --game g1` first, then change the mod_path, then run `lmm deploy --game g1`\",\n"+
		"  \"details\": {\n"+
		"    \"game_id\": \"g1\",\n"+
		"    \"mod_path\": \"/g/mods\",\n"+
		"    \"deployed_files\": 2\n"+
		"  }\n"+
		"}\n", out)
}

// TestAdapterWarning_TheLoadTimeLineIsShort is #456: every command prints
// the load-time line until the configuration is fixed, so it is one short
// sentence naming the game, the contradiction and where the fix is - not the
// ~900-character paragraph `lmm game show` carries.
func TestAdapterWarning_TheLoadTimeLineIsShort(t *testing.T) {
	setupAdapterWarningGames(t)
	_, stderr := runLMM(t, "status")

	line := lineContaining(stderr, `"lethal-company"`)
	assert.Equal(t, "warning: "+lethalLoadTime+"; run `lmm game show lethal-company` for the fix", line)
	assert.Less(t, len(line), 200, "one line, not the paragraph")

	stdout, _ := runLMM(t, "game", "show", "lethal-company")
	assert.Contains(t, stdout, "exactly as packaged", "game show still carries the whole explanation")
	assert.Contains(t, stdout, "lmm game edit lethal-company --adapter bepinex", "and the remedies")
}

// TestDoVerify_SaysTheModPathIsMissing: verify names the missing mod_path
// and its repair once lmm has deployed there (#427).
func TestDoVerify_SaysTheModPathIsMissing(t *testing.T) {
	svc, game := setupMissingModPathGame(t)
	oldProfile, oldFix := verifyProfile, verifyFix
	verifyProfile, verifyFix = "default", false
	t.Cleanup(func() { verifyProfile, verifyFix = oldProfile, oldFix })
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	out := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })
	assert.Contains(t, lineContaining(out, "? mod_path - "), missingModPathRepair)
	assert.Contains(t, out, "1 warning(s)")
}
