package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGameCmd_Structure(t *testing.T) {
	assert.Equal(t, "game", gameCmd.Use)
	assert.NotEmpty(t, gameCmd.Short)
}

func TestGameSetDefaultCmd_Structure(t *testing.T) {
	assert.Equal(t, "set-default <game-id>", gameSetDefaultCmd.Use)
	assert.NotEmpty(t, gameSetDefaultCmd.Short)
	assert.NotEmpty(t, gameSetDefaultCmd.Long)
}

func TestGameShowDefaultCmd_Structure(t *testing.T) {
	assert.Equal(t, "show-default", gameShowDefaultCmd.Use)
	assert.NotEmpty(t, gameShowDefaultCmd.Short)
}

func TestGameClearDefaultCmd_Structure(t *testing.T) {
	assert.Equal(t, "clear-default", gameClearDefaultCmd.Use)
	assert.NotEmpty(t, gameClearDefaultCmd.Short)
}

func TestGameSetDefault_NoArgs(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	gameCmdCopy.AddCommand(gameSetDefaultCmd)
	t.Cleanup(func() { gameCmd.RemoveCommand(gameSetDefaultCmd); gameCmd.AddCommand(gameSetDefaultCmd) })
	cmd.AddCommand(gameCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "set-default"})

	err := cmd.Execute()
	assert.Error(t, err)
	// Cobra should complain about missing argument
}

func TestGameSetDefault_GameNotFound(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")

	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	setDefaultCmdCopy := &cobra.Command{
		Use:  "set-default <game-id>",
		Args: cobra.ExactArgs(1),
		RunE: runGameSetDefault,
	}
	gameCmdCopy.AddCommand(setDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "set-default", "non-existent-game"})

	err := cmd.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "game not found")
}

func TestGameShowDefault_NoDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")

	buf := new(bytes.Buffer)
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	showDefaultCmdCopy := &cobra.Command{
		Use:  "show-default",
		Args: cobra.NoArgs,
		RunE: runGameShowDefault,
	}
	gameCmdCopy.AddCommand(showDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)

	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "show-default"})

	err := cmd.Execute()
	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "No default game set")
}

func TestGameShowDefault_WithDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")

	// Set a default game in config
	cfg := &config.Config{DefaultGame: "test-game"}
	require.NoError(t, cfg.Save(tmpDir))

	buf := new(bytes.Buffer)
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	showDefaultCmdCopy := &cobra.Command{
		Use:  "show-default",
		Args: cobra.NoArgs,
		RunE: runGameShowDefault,
	}
	gameCmdCopy.AddCommand(showDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)

	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "show-default"})

	err := cmd.Execute()
	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "test-game")
}

// TestGameShowDefault_PlainTextIsOnStdoutNotStderr pins Ruling 17
// (#309): before this, doGameShowDefault's lines went to
// Command.OutOrStderr() (an accident of cmd.Println/Printf) rather than
// stdout, so a caller piping only stdout got nothing. Uses SEPARATE
// stdout/stderr buffers - TestGameShowDefault_NoDefault/_WithDefault point
// both cmd.SetOut and cmd.SetErr at the SAME buffer, which cannot
// distinguish the two streams.
func TestGameShowDefault_PlainTextIsOnStdoutNotStderr(t *testing.T) {
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")

	outBuf, errBuf := new(bytes.Buffer), new(bytes.Buffer)
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	showDefaultCmdCopy := &cobra.Command{Use: "show-default", Args: cobra.NoArgs, RunE: runGameShowDefault}
	gameCmdCopy.AddCommand(showDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"game", "show-default"})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, outBuf.String(), "No default game set")
	assert.Empty(t, errBuf.String(), "the plain lines must not land on stderr (Ruling 17)")
}

// TestGameShowDefault_UnresolvableID matches the plain path's own
// fallback: a configured default that no longer names a known game prints
// just the bare ID, no "(name)".
func TestGameShowDefault_UnresolvableID(t *testing.T) {
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")
	cfg := &config.Config{DefaultGame: "ghost-game"}
	require.NoError(t, cfg.Save(tmpDir))

	outBuf := new(bytes.Buffer)
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	showDefaultCmdCopy := &cobra.Command{Use: "show-default", Args: cobra.NoArgs, RunE: runGameShowDefault}
	gameCmdCopy.AddCommand(showDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)
	cmd.SetOut(outBuf)
	cmd.SetErr(outBuf)
	cmd.SetArgs([]string{"game", "show-default"})

	require.NoError(t, cmd.Execute())
	assert.Equal(t, "Default game: ghost-game\n", outBuf.String())
}

// TestGameShowDefault_MalformedGamesYAML pins the pre-#309 tolerance (task
// A review round 1, Important 1): a malformed games.yaml must not turn a
// successful "default game" readout into a hard error - only the
// best-effort name enrichment may fail, falling back to the bare-ID
// line/document, exactly like the pre-#309 code's best-effort initService.
func TestGameShowDefault_MalformedGamesYAML(t *testing.T) {
	setup := func(t *testing.T) {
		t.Helper()
		tmpDir := t.TempDir()
		configDir = tmpDir
		dataDir = filepath.Join(tmpDir, "data")
		cfg := &config.Config{DefaultGame: "test-game"}
		require.NoError(t, cfg.Save(tmpDir))
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "games.yaml"), []byte("games: [\n"), 0o644))
	}

	t.Run("plain", func(t *testing.T) {
		setup(t)
		outBuf := new(bytes.Buffer)
		cmd := &cobra.Command{}
		cmd.SetOut(outBuf)
		cmd.SetContext(context.Background())

		err := runGameShowDefault(cmd, nil)
		require.NoError(t, err, "a malformed games.yaml must not fail the command (Important 1)")
		assert.Equal(t, "Default game: test-game\n", outBuf.String())
	})

	t.Run("json", func(t *testing.T) {
		setup(t)
		withJSONOutput(t)
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())

		out := captureStdout(t, func() error { return runGameShowDefault(cmd, nil) })
		var got core.DefaultGame
		decodeStrict(t, out, &got)
		assert.Equal(t, core.DefaultGame{Set: true, ID: "test-game"}, got, "Name stays empty when the service cannot open")
	})
}

// TestGameShowDefault_MalformedConfigYAML pins the exact pre-#309 error
// bytes (task A review round 1, Important 1): a config.yaml load failure
// must keep "loading config: %w", never gaining withService's "initializing
// service: " prefix - the default ID is resolved config-only, before any
// service is opened.
func TestGameShowDefault_MalformedConfigYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte("default_game: [\n"), 0o644))

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runGameShowDefault(cmd, nil)
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "loading config: parsing config: "), "got %q", err.Error())
	assert.NotContains(t, err.Error(), "initializing service:")
}

// TestDoGameShowDefault_JSON pins the DefaultGame document's framing (one
// document, empty stderr) and its recorded golden (#309), for both the
// "set, resolves" and "none set" shapes.
func TestDoGameShowDefault_JSON(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		svc := setupGameAddTest(t)
		require.NoError(t, svc.SaveGame(context.Background(), goldenStatusGame("skyrim-se", "Skyrim SE")))
		require.NoError(t, svc.SetDefaultGame(context.Background(), "skyrim-se"))
		info, err := svc.DefaultGameInfo(context.Background())
		require.NoError(t, err)
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())

		out := runJSONCommand(t, func() error {
			return doGameShowDefault(cmd, info)
		})
		var got core.DefaultGame
		decodeStrict(t, out, &got)
		assertJSONCLIGolden(t, "game_show_default_set", out)
	})

	t.Run("none", func(t *testing.T) {
		svc := setupGameAddTest(t)
		info, err := svc.DefaultGameInfo(context.Background())
		require.NoError(t, err)
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())

		out := runJSONCommand(t, func() error {
			return doGameShowDefault(cmd, info)
		})
		var got core.DefaultGame
		decodeStrict(t, out, &got)
		assertJSONCLIGolden(t, "game_show_default_none", out)
	})
}

func TestGameClearDefault_NoDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")

	buf := new(bytes.Buffer)
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	clearDefaultCmdCopy := &cobra.Command{
		Use:  "clear-default",
		Args: cobra.NoArgs,
		RunE: runGameClearDefault,
	}
	gameCmdCopy.AddCommand(clearDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)

	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "clear-default"})

	err := cmd.Execute()
	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "No default game was set")
}

func TestGameClearDefault_WithDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")

	// Set a default game in config
	cfg := &config.Config{DefaultGame: "test-game"}
	require.NoError(t, cfg.Save(tmpDir))

	buf := new(bytes.Buffer)
	cmd := &cobra.Command{Use: "test"}
	gameCmdCopy := &cobra.Command{Use: "game"}
	clearDefaultCmdCopy := &cobra.Command{
		Use:  "clear-default",
		Args: cobra.NoArgs,
		RunE: runGameClearDefault,
	}
	gameCmdCopy.AddCommand(clearDefaultCmdCopy)
	cmd.AddCommand(gameCmdCopy)

	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"game", "clear-default"})

	err := cmd.Execute()
	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "Cleared default game")
	assert.Contains(t, buf.String(), "test-game")

	// Verify it was actually cleared
	loadedCfg, err := config.Load(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, loadedCfg.DefaultGame)
}

func TestRequireGame_WithFlag(t *testing.T) {
	gameID = "test-game"
	err := requireGame(nil)
	assert.NoError(t, err)
	assert.Equal(t, "test-game", gameID)
}

func TestRequireGame_WithDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")
	gameID = "" // No flag

	// Set a default game in config
	cfg := &config.Config{DefaultGame: "default-game"}
	require.NoError(t, cfg.Save(tmpDir))

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := requireGame(cmd)
	assert.NoError(t, err)
	assert.Equal(t, "default-game", gameID)
}

func TestRequireGame_NoGameNoDefault(t *testing.T) {
	// Use temp directories
	tmpDir := t.TempDir()
	configDir = tmpDir
	dataDir = filepath.Join(tmpDir, "data")
	gameID = "" // No flag

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := requireGame(cmd)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no game specified")
	assert.Contains(t, err.Error(), "game set-default")
}

func TestConfigDefaultGame_Persistence(t *testing.T) {
	tmpDir := t.TempDir()

	// Save config with default game
	cfg := &config.Config{DefaultGame: "my-game"}
	require.NoError(t, cfg.Save(tmpDir))

	// Verify file was created
	configPath := filepath.Join(tmpDir, "config.yaml")
	_, err := os.Stat(configPath)
	require.NoError(t, err)

	// Load and verify
	loaded, err := config.Load(tmpDir)
	require.NoError(t, err)
	assert.Equal(t, "my-game", loaded.DefaultGame)
}

// TestDoGameSetDefault_LoadFailureReportedAsLoadError pins Task 22 review
// Important #1's first occurrence (2026-08-28): pre-lift, doGameSetDefault
// distinguished a config.Load failure ("loading config: %w") from a
// cfg.Save failure ("saving config: %w"). service.SetDefaultGame does both
// steps as one call, and 989eb71's cmd wrap around it
// (fmt.Errorf("saving config: %w", err)) mislabels a load failure as a save
// failure. Reachable via a config.yaml that goes unreadable between
// NewService's own load and this call - simulated here by opening the
// service against a valid config.yaml, then revoking read permission before
// calling doGameSetDefault directly.
func TestDoGameSetDefault_LoadFailureReportedAsLoadError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("chmod is not enforced for root")
	}

	tmpDir := t.TempDir()
	require.NoError(t, config.SaveGame(tmpDir, &domain.Game{ID: "test-game", Name: "Test Game"}))
	require.NoError(t, (&config.Config{}).Save(tmpDir))

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: tmpDir, DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	configPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.Chmod(configPath, 0000))
	t.Cleanup(func() { _ = os.Chmod(configPath, 0644) })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err = doGameSetDefault(cmd, svc, "test-game")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading config:", "a load failure must be reported as a load error, not a save error")
	assert.NotContains(t, err.Error(), "saving config:")
}
