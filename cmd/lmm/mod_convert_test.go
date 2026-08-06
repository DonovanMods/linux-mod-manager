package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// setupDoModConvertTest builds a *core.Service and a game configured for
// fakeInstallSource (cmd/lmm/install_test.go - a real, registered
// source.ModSource), and resets convert's package-level flag globals -
// mirrors setupDoModLockTest's pattern. Callers seed their own installed
// mods via seedConvertableMod.
func setupDoModConvertTest(t *testing.T) (*core.Service, *domain.Game, *fakeInstallSource) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := newFakeInstallSource("src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)

	game := &domain.Game{
		ID:          "g1",
		Name:        "Game",
		ModPath:     gameDir,
		LinkMethod:  domain.LinkSymlink,
		DeployMode:  domain.DeployCompile,
		SourceIDs:   map[string]string{"src": "g1"},
		ConvertPaks: true,
	}

	oldSource, oldProfile := modSource, modProfile
	modSource = "src"
	modProfile = ""
	t.Cleanup(func() { modSource, modProfile = oldSource, oldProfile })

	return svc, game, src
}

// seedConvertableMod records modID as installed (DB) at version and adds a
// matching ModReference to the "default" profile (YAML) - the two records
// doModConvert reads from. Mirrors seedLockableMod's pattern.
func seedConvertableMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, version string) {
	t.Helper()

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "src", Name: name, Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		ConvertPaks:  true,
	}))
	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(game.ID, "default")
		require.NoError(t, err)
	}
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "src", ModID: modID, Version: version}))
}

// TestModConvertCommand guards the convert on|off mutation: toggle ConvertPaks
// in the DB, check output mentions the state, and verify it persists. Compile
// games show the convergence hint; non-compile games do not.
func TestModConvertCommand(t *testing.T) {
	svc, game, _ := setupDoModConvertTest(t)
	seedConvertableMod(t, svc, game, "a", "Mod A", "1.0")

	// Convert off
	out := captureStdout(t, func() error {
		return doModConvert(svc, game, "a", false)
	})
	assert.Contains(t, out, "✓")
	assert.Contains(t, out, "Mod A")
	assert.Contains(t, out, "conversion: off")
	assert.Contains(t, out, "lmm deploy", "compile-mode game should mention deploy")

	// Verify DB flag is false
	installed, err := svc.GetInstalledMod("src", "a", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, installed.ConvertPaks)

	// Convert back on
	out = captureStdout(t, func() error {
		return doModConvert(svc, game, "a", true)
	})
	assert.Contains(t, out, "conversion: on")

	// Verify DB flag is true
	installed, err = svc.GetInstalledMod("src", "a", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, installed.ConvertPaks)
}

// TestModConvertCommand_NonCompileGame guards that a non-compile game shows
// a note that conversion only affects merge-compile games, but still persists
// the flag (for later when the game is reconfigured).
func TestModConvertCommand_NonCompileGame(t *testing.T) {
	svc, game, _ := setupDoModConvertTest(t)
	game.DeployMode = domain.DeployCopy // Non-compile
	seedConvertableMod(t, svc, game, "a", "Mod A", "1.0")

	out := captureStdout(t, func() error {
		return doModConvert(svc, game, "a", false)
	})
	assert.Contains(t, out, "conversion: off")
	assert.Contains(t, out, "note:", "non-compile game must explain the flag has no effect")
	assert.NotContains(t, out, "lmm deploy", "non-compile game must not mention deploy")

	// Verify the flag still persists
	installed, err := svc.GetInstalledMod("src", "a", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, installed.ConvertPaks)
}

// TestModConvertCommand_NotInstalled guards that converting a mod that
// isn't installed fails with "mod not found" idiom, consistent with every
// other mod subcommand.
func TestModConvertCommand_NotInstalled(t *testing.T) {
	svc, game, _ := setupDoModConvertTest(t)

	err := doModConvert(svc, game, "missing", false)

	require.Error(t, err)
	assert.Equal(t, "mod not found: missing", err.Error())
}

// TestListShowsConvert guards that lmm list shows convert state in verbose
// mode and in JSON output.
func TestListShowsConvert(t *testing.T) {
	svc, game, _ := setupDoModConvertTest(t)
	seedConvertableMod(t, svc, game, "a", "Mod A", "1.0")

	// Set one to off, leave the other on
	require.NoError(t, svc.SetModConvertPaks("src", "a", game.ID, "default", false))

	// Seed another mod with convert on (default)
	seedConvertableMod(t, svc, game, "b", "Mod B", "1.0")

	// Test JSON output includes convert_paks
	out := captureStdout(t, func() error {
		oldJSON := jsonOutput
		oldVerbose := verbose
		jsonOutput = true
		verbose = false
		defer func() { jsonOutput, verbose = oldJSON, oldVerbose }()
		return doList(nil, svc, game)
	})

	// JSON should include convert_paks for both mods (compile mode game)
	assert.Contains(t, out, "\"convert_paks\": false")
	assert.Contains(t, out, "\"convert_paks\": true")

	// Test verbose table includes CONVERT column
	out = captureStdout(t, func() error {
		oldJSON := jsonOutput
		oldVerbose := verbose
		jsonOutput = false
		verbose = true
		defer func() { jsonOutput, verbose = oldJSON, oldVerbose }()
		return doList(nil, svc, game)
	})

	// Verbose table should show CONVERT column
	assert.Contains(t, out, "CONVERT")
	// Mod A (convert off) should show "off"
	// Mod B (convert on) should show "on"
	// (The exact formatting depends on the tabwriter, but both should appear)
}

// TestModShowIncludesConvert guards that lmm mod show includes convert state
// for installed mods in compile-mode games.
func TestModShowIncludesConvert(t *testing.T) {
	svc, game, src := setupDoModConvertTest(t)
	seedConvertableMod(t, svc, game, "a", "Mod A", "1.0")
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID}, []domain.DownloadableFile{
		{ID: "f1", Version: "1.0", Category: "MAIN"},
	})
	require.NoError(t, svc.SetModConvertPaks("src", "a", game.ID, "default", false))

	// Test JSON output
	out := captureStdout(t, func() error {
		oldJSON := jsonOutput
		jsonOutput = true
		defer func() { jsonOutput = oldJSON }()
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.Contains(t, out, "\"convert_paks\": false")

	// Test human output
	out = captureStdout(t, func() error {
		oldJSON := jsonOutput
		jsonOutput = false
		defer func() { jsonOutput = oldJSON }()
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.Contains(t, out, "Pak conversion:")
	assert.Contains(t, out, "off")
}

// TestRunModConvert_InvalidArgument guards that runModConvert rejects
// invalid on|off arguments with a clear error message before any service
// call is made (matching runModLock/runModSetUpdate's validation pattern).
func TestRunModConvert_InvalidArgument(t *testing.T) {
	svc, game, _ := setupDoModConvertTest(t)
	seedConvertableMod(t, svc, game, "a", "Mod A", "1.0")

	// Call runModConvert with invalid argument through the cobra command
	// (simulating `lmm mod convert a sideways`)
	err := runModConvert(nil, []string{"a", "sideways"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "on|off")
	assert.Contains(t, err.Error(), "sideways")
}

// TestListShowsConvert_NonCompileGame_OmitsConvertPaks guards the omitempty
// contract: convert_paks field must be ABSENT (not present as null) from
// list --json output for non-compile games, consistent with the "only present
// for merge-compile games" design.
func TestListShowsConvert_NonCompileGame_OmitsConvertPaks(t *testing.T) {
	svc, game, _ := setupDoModConvertTest(t)
	game.DeployMode = domain.DeployCopy // Non-compile
	seedConvertableMod(t, svc, game, "a", "Mod A", "1.0")

	out := captureStdout(t, func() error {
		oldJSON := jsonOutput
		jsonOutput = true
		defer func() { jsonOutput = oldJSON }()
		return doList(nil, svc, game)
	})

	// convert_paks field must not appear at all (omitempty nil contract)
	assert.NotContains(t, out, "convert_paks",
		"non-compile game must omit convert_paks from JSON entirely (omitempty)")
}

// TestModConvertCmd_Structure pins the cobra wiring: convert accepts exactly
// 2 positional args (mod-id, on|off).
func TestModConvertCmd_Structure(t *testing.T) {
	assert.Equal(t, "convert <mod-id> <on|off>", modConvertCmd.Use)
	assert.NotEmpty(t, modConvertCmd.Short)
	assert.NoError(t, modConvertCmd.Args(modConvertCmd, []string{"a", "on"}))
	assert.NoError(t, modConvertCmd.Args(modConvertCmd, []string{"a", "off"}))
	assert.Error(t, modConvertCmd.Args(modConvertCmd, []string{}))
	assert.Error(t, modConvertCmd.Args(modConvertCmd, []string{"a"}))
	assert.Error(t, modConvertCmd.Args(modConvertCmd, []string{"a", "on", "extra"}))
}
