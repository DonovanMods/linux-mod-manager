package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatusCmd_FullStructure tests the status command structure in detail
func TestStatusCmd_FullStructure(t *testing.T) {
	assert.Equal(t, "status", statusCmd.Use)
	assert.NotEmpty(t, statusCmd.Short)
	assert.NotEmpty(t, statusCmd.Long)

	// Status command should not require any flags
	assert.NotNil(t, statusCmd.RunE)
}

// TestStatusCmd_NoGames tests status when no games are configured
func TestStatusCmd_NoGames(t *testing.T) {
	// Use temp directories (empty, no games configured)
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = ""

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(statusCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(statusCmd); rootCmd.AddCommand(statusCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status"})

	// Should succeed even with no games
	err := cmd.Execute()
	assert.NoError(t, err)
}

// TestStatusCmd_WithGameFlag_NoGamesConfigured tests status with game flag when no games exist
func TestStatusCmd_WithGameFlag_NoGamesConfigured(t *testing.T) {
	// Use temp directories (empty, no games configured)
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = "some-game"

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(statusCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(statusCmd); rootCmd.AddCommand(statusCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status"})

	// When no games are configured, status returns early with "No games configured"
	// regardless of the gameID flag - this is the actual behavior
	err := cmd.Execute()
	assert.NoError(t, err)
}

// TestStatusCmd_AcceptsGameFlag tests that status accepts the game flag.
//
// Drives this through the real rootCmd rather than a throwaway replica
// root: cobra computes and caches each command's merged inherited-flags
// set the first time it's asked for, and never invalidates the cache. A
// second root registering its own competing "--game" PersistentFlags and
// temporarily adopting the shared statusCmd singleton would permanently
// poison statusCmd's cached flag set with the throwaway's flag object
// (and its usage string) instead of rootCmd's real one - for the rest of
// the test binary, not just this test - which is exactly what broke the
// man-page drift test (#104) before this was found: the generated
// lmm-status.1 rendered "game ID" (the throwaway's usage text) instead of
// the real "game ID to operate on" (root.go's), regardless of test order,
// because nothing can un-cache a cobra command's flag set short of
// ResetFlags(), which would also wipe the command's own real flags.
func TestStatusCmd_AcceptsGameFlag(t *testing.T) {
	// Reset state
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = ""

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"status", "--game", "test-game"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		// nil restores the default os.Stdout/os.Stderr fallback (cobra's
		// getOut/getErr only substitute outWriter/errWriter when non-nil) -
		// rootCmd is a package singleton, so leaving the buffer wired up
		// would swallow output from every test that runs after this one.
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})

	// Command succeeds (shows "No games configured"), but flag should be parsed
	err := rootCmd.Execute()
	assert.NoError(t, err)
	assert.Equal(t, "test-game", gameID)
}

// TestStatusCmd_ShowsDefaultGame tests that status runs when default game is set
func TestStatusCmd_ShowsDefaultGame(t *testing.T) {
	// Use temp directories
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = ""

	// Set up a default game in config
	cfg := &config.Config{DefaultGame: "my-default-game"}
	err := cfg.Save(configDir)
	require.NoError(t, err)

	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(statusCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(statusCmd); rootCmd.AddCommand(statusCmd) })

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"status"})

	// Command succeeds (note: default game is only shown when games are configured)
	err = cmd.Execute()
	assert.NoError(t, err)
}

// TestDoStatus_OrdersGamesByID pins Ruling 4 (#299): `lmm status` (both
// plain-text and --json) lists multiple games ordered by game ID, not
// whatever order Go's games map iteration happened to produce that run. IDs
// are seeded deliberately out of order.
func TestDoStatus_OrdersGamesByID(t *testing.T) {
	svc := setupGameAddTest(t)
	for _, id := range []string{"zulu", "mike", "alpha"} {
		require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{ID: id, Name: id, ModPath: t.TempDir()}))
	}
	gameID = ""
	verbose = false

	jsonOutput = false
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() error {
		return doStatus(context.Background(), svc)
	})
	iAlpha := strings.Index(out, "alpha")
	iMike := strings.Index(out, "mike")
	iZulu := strings.Index(out, "zulu")
	require.True(t, iAlpha >= 0 && iMike >= 0 && iZulu >= 0, "all three games must appear in the output: %q", out)
	assert.True(t, iAlpha < iMike && iMike < iZulu, "games must be listed in ID order (alpha, mike, zulu), got: %q", out)

	jsonOutput = true
	out = captureStdout(t, func() error {
		return doStatus(context.Background(), svc)
	})
	var decoded core.StatusReport
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.Len(t, decoded.Games, 3)
	gotIDs := []string{decoded.Games[0].ID, decoded.Games[1].ID, decoded.Games[2].ID}
	assert.Equal(t, []string{"alpha", "mike", "zulu"}, gotIDs)
}

// TestStatusCmd_JSONOutput verifies status --json output structure (JSON contract / E2E shape).
// Encodes the same struct used by status --json and asserts round-trip and expected keys.
func TestStatusCmd_JSONOutput(t *testing.T) {
	out := core.StatusReport{Games: []core.GameSummary{}}
	data, err := json.Marshal(out)
	require.NoError(t, err)

	var decoded struct {
		Games []interface{} `json:"games"`
	}
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err, "status JSON output must be valid JSON with 'games' key")
	assert.NotNil(t, decoded.Games)
	assert.Len(t, decoded.Games, 0)
}

// --- Last Deploy: CLI parity for #106(d) ---
//
// setupDoDeployTest (deploy_test.go) builds the *core.Service/*domain.Game
// pair but never registers the game with the service's config-backed game
// map (doDeploy takes the game directly, so it never needed to) - these
// tests call showGameStatus(JSON) by gameID instead, which resolves through
// service.GetGame, so each test below adds its own svc.SaveGame(context.Background(), game) call.

// TestShowGameStatus_NeverDeployed_ShowsNeverInText pins the "never
// deployed" text rendering: a game whose profile has no deployed_files rows
// renders "Last Deploy: never", not an empty string or a zero time.
func TestShowGameStatus_NeverDeployed_ShowsNeverInText(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp") // profile exists, never deployed

	out := captureStdout(t, func() error {
		return showGameStatus(context.Background(), svc, game.ID)
	})

	assert.Contains(t, out, "Last Deploy: never")
}

// TestShowGameStatus_AfterDeploy_ShowsTimestampInText pins that a real
// deploy populates the Last Deploy line with an absolute local timestamp -
// deliberately NOT a relative-age rendering, since the CLI's output is
// scriptable and an absolute, stable format is preferable there (see
// formatLastDeploy's doc comment in status.go).
func TestShowGameStatus_AfterDeploy_ShowsTimestampInText(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")
	require.NoError(t, doDeploy(context.Background(), svc, game, nil))

	out := captureStdout(t, func() error {
		return showGameStatus(context.Background(), svc, game.ID)
	})

	assert.NotContains(t, out, "Last Deploy: never")
	assert.Regexp(t, `Last Deploy: \d{4}-\d{2}-\d{2} \d{2}:\d{2}`, out)
}

// TestShowGameStatusJSON_NeverDeployed_OmitsLastDeploy pins the omitempty
// contract (task-4-brief.md: additive JSON change, MINOR precedent under
// lmm-repo-conventions) - a never-deployed game's JSON document has no
// "last_deploy" key at all, so existing consumers parsing this shape see
// byte-for-byte the same document as before this change.
func TestShowGameStatusJSON_NeverDeployed_OmitsLastDeploy(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")

	out := captureStdout(t, func() error {
		return showGameStatusJSON(context.Background(), svc, game.ID)
	})

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &raw))
	_, present := raw["last_deploy"]
	assert.False(t, present, "never-deployed game must omit last_deploy entirely")
}

// --- status --json link_method parity with the text twin (issue #155) ---
//
// Since PR #151 the text output resolves the profile-effective link method
// (profile > game > global) while the JSON document's link_method stayed
// game-level - a deliberate JSON-contract non-change. These tests pin the
// additive resolution: link_method keeps its game-level meaning, and the new
// effective_link_method / link_method_source fields carry what the text
// twin shows.

// TestShowGameStatusJSON_ProfileLinkMethodOverride_TextJSONParity is issue
// #155's headline failing case: the game explicitly says symlink, the active
// profile explicitly says copy. The text twin shows "copy (per-profile)";
// the JSON document must agree via effective_link_method while link_method
// keeps reporting the game-level symlink.
func TestShowGameStatusJSON_ProfileLinkMethodOverride_TextJSONParity(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	game.LinkMethod = domain.LinkSymlink
	game.LinkMethodExplicit = true
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")
	setVerifyProfileLinkMethod(t, svc, game.ID, "default", domain.LinkCopy)

	textOut := captureStdout(t, func() error {
		return showGameStatus(context.Background(), svc, game.ID)
	})
	require.Contains(t, textOut, "Link Method: copy (per-profile)", "sanity: the text twin resolves the profile override")

	jsonOut := captureStdout(t, func() error {
		return showGameStatusJSON(context.Background(), svc, game.ID)
	})

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &raw))
	assert.Equal(t, "symlink", raw["link_method"], "link_method keeps its game-level meaning (JSON contract)")
	assert.Equal(t, "copy", raw["effective_link_method"], "effective_link_method must agree with the text twin")
	assert.Equal(t, "profile", raw["link_method_source"])
}

// TestShowGameStatusJSON_LinkMethodSource_GameAndGlobal drives the two
// non-profile branches of link_method_source: a game-explicit method reports
// "game", and no override anywhere reports "global" - in both cases
// effective_link_method equals link_method, so pre-#155 consumers reading
// link_method saw the right value all along.
func TestShowGameStatusJSON_LinkMethodSource_GameAndGlobal(t *testing.T) {
	tests := []struct {
		name         string
		gameExplicit bool
		wantMethod   string
		wantSource   string
	}{
		{name: "game-explicit hardlink", gameExplicit: true, wantMethod: "hardlink", wantSource: "game"},
		{name: "global default symlink", gameExplicit: false, wantMethod: "symlink", wantSource: "global"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, game := setupDoDeployTest(t)
			if tt.gameExplicit {
				game.LinkMethod = domain.LinkHardlink
				game.LinkMethodExplicit = true
			}
			require.NoError(t, svc.SaveGame(context.Background(), game))
			seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")

			out := captureStdout(t, func() error {
				return showGameStatusJSON(context.Background(), svc, game.ID)
			})

			var raw map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(out), &raw))
			assert.Equal(t, tt.wantMethod, raw["link_method"])
			assert.Equal(t, tt.wantMethod, raw["effective_link_method"])
			assert.Equal(t, tt.wantSource, raw["link_method_source"])
		})
	}
}

// TestShowGameStatusJSON_NoProfiles_LinkMethodSourceStillPresent pins the
// no-active-profile edge: with no profiles at all, the resolution degrades
// to the game level (mirroring GetEffectiveLinkMethod's missing-profile
// behavior) and the additive fields are still present - not omitted - so
// consumers can rely on them unconditionally.
func TestShowGameStatusJSON_NoProfiles_LinkMethodSourceStillPresent(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	game.LinkMethod = domain.LinkCopy
	game.LinkMethodExplicit = true
	require.NoError(t, svc.SaveGame(context.Background(), game))

	out := captureStdout(t, func() error {
		return showGameStatusJSON(context.Background(), svc, game.ID)
	})

	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(out), &raw))
	assert.Equal(t, "copy", raw["effective_link_method"])
	assert.Equal(t, "game", raw["link_method_source"])
}

// TestShowGameStatusJSON_AfterDeploy_IncludesLastDeploy pins that a real
// deploy surfaces a parseable last_deploy timestamp in the JSON document.
func TestShowGameStatusJSON_AfterDeploy_IncludesLastDeploy(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedDeployableMod(t, svc, game, "1", "Test Mod", "plugin.esp")
	require.NoError(t, doDeploy(context.Background(), svc, game, nil))

	out := captureStdout(t, func() error {
		return showGameStatusJSON(context.Background(), svc, game.ID)
	})

	var decoded core.GameStatus
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	require.NotNil(t, decoded.LastDeploy)
	assert.WithinDuration(t, time.Now(), *decoded.LastDeploy, time.Minute)
}

// TestShowGameStatus_ConversionFailures_ShownInTextAndJSON is #221 design
// §5's status surfacing pin: a merged-pak fingerprint recording one failed
// conversion (Task 8's MergedPakOutcomes) must show up in both status
// surfaces - a "pak conversion failures: N" text line pointing at 'lmm
// verify', and the additive conversion_failures JSON field.
func TestShowGameStatus_ConversionFailures_ShownInTextAndJSON(t *testing.T) {
	svc, game, compiler, _ := setupDoUpdateRecompileTest(t)
	game.ConvertPaks = true

	const modID, version, fileID = "raw-pak-mod", "1.0", "modfile.pak"
	seedEnabledPakModCLI(t, svc, game, "fake-compiler", modID, version, fileID, []byte("pak-bytes"))

	outcome := &pakOutcomeCompilerSource{
		compilerInstallSource: compiler,
		failRefs:              map[string]string{"fake-compiler:" + modID: "table X not present in current base"},
	}
	svc.RegisterSource(outcome)

	_, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	textOut := captureStdout(t, func() error { return showGameStatus(context.Background(), svc, game.ID) })
	assert.Contains(t, textOut, "pak conversion failures: 1")
	assert.Contains(t, textOut, "lmm verify")

	jsonOut := captureStdout(t, func() error { return showGameStatusJSON(context.Background(), svc, game.ID) })
	var decoded core.GameStatus
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &decoded))
	assert.Equal(t, 1, decoded.ConversionFailures)
}
