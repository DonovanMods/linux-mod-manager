package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/steam"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newGameDetectTestService builds a *core.Service backed by configDir (the
// package-level var doGameDetect's caller resolves via getServiceConfig in
// production), so games saved during the test are visible to
// config.LoadGames afterward, without app.Open's source-registration
// pipeline (no network) — mirrors setupGameAddTest in game_add_test.go.
func newGameDetectTestService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

func detectedGamesFixture() []steam.DetectedGame {
	return []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
		{Slug: "starrupture", Name: "Star Rupture", InstallPath: "/games/starrupture", NexusID: "starrupture"},
		{Slug: "icarus", Name: "Icarus", InstallPath: "/games/icarus", Sources: map[string]string{"icarus": "icarus"}},
	}
}

// TestGameDetectSelectionIndices_AllExcludesConfigured pins #205 item 2:
// "all" defaults to only the NOT-yet-configured games.
func TestGameDetectSelectionIndices_AllExcludesConfigured(t *testing.T) {
	games := detectedGamesFixture()
	existing := map[string]*domain.Game{"skyrim-se": {ID: "skyrim-se"}}

	indices, err := gameDetectSelectionIndices("all", games, existing)
	require.NoError(t, err)
	assert.Equal(t, []int{2, 3}, indices)
}

// TestGameDetectSelectionIndices_AllWhenNoneConfigured is the pre-#205
// baseline: nothing configured yet, "all" still selects everything.
func TestGameDetectSelectionIndices_AllWhenNoneConfigured(t *testing.T) {
	games := detectedGamesFixture()

	indices, err := gameDetectSelectionIndices("all", games, map[string]*domain.Game{})
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3}, indices)
}

// TestGameDetectSelectionIndices_AllWhenEveryGameConfigured pins the empty
// result when every detected game is already configured.
func TestGameDetectSelectionIndices_AllWhenEveryGameConfigured(t *testing.T) {
	games := detectedGamesFixture()
	existing := map[string]*domain.Game{
		"skyrim-se":   {ID: "skyrim-se"},
		"starrupture": {ID: "starrupture"},
		"icarus":      {ID: "icarus"},
	}

	indices, err := gameDetectSelectionIndices("all", games, existing)
	require.NoError(t, err)
	assert.Empty(t, indices)
}

// TestGameDetectSelectionIndices_ExplicitSelectionIncludesConfigured pins
// the repair path (#205 item 2): explicitly naming an already-configured
// game's number still selects it - this is how a user deliberately
// re-adds/repairs a game, mirroring 'lmm game add's unconditional overwrite.
func TestGameDetectSelectionIndices_ExplicitSelectionIncludesConfigured(t *testing.T) {
	games := detectedGamesFixture()
	existing := map[string]*domain.Game{"skyrim-se": {ID: "skyrim-se"}}

	indices, err := gameDetectSelectionIndices("1,2", games, existing)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, indices)
}

func TestGameDetectSelectionIndices_NoneAndEmpty(t *testing.T) {
	games := detectedGamesFixture()
	for _, in := range []string{"", "n", "none", "NONE"} {
		indices, err := gameDetectSelectionIndices(in, games, nil)
		require.NoError(t, err)
		assert.Empty(t, indices, "input %q", in)
	}
}

func TestGameDetectSelectionIndices_InvalidSelection(t *testing.T) {
	games := detectedGamesFixture()
	_, err := gameDetectSelectionIndices("99", games, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid selection")
}

// TestDoGameDetect_MarksConfiguredGamesAndExcludesFromAll drives the full
// interactive flow with a stubbed detected-games list (no real Steam scan)
// against a configDir that already has skyrim-se configured: the printed
// list marks it [configured], and answering "all" adds only starrupture.
func TestDoGameDetect_MarksConfiguredGamesAndExcludesFromAll(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Skyrim Special Edition"}))

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
		{Slug: "starrupture", Name: "Star Rupture", InstallPath: "/games/starrupture", NexusID: "starrupture"},
	}

	svc := newGameDetectTestService(t)
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	reader := bufio.NewReader(strings.NewReader("all\n"))

	err := doGameDetect(context.Background(), cmd, reader, svc, games)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, lineContaining(out, "skyrim-se"), "[configured]")
	assert.NotContains(t, lineContaining(out, "starrupture"), "[configured]")
	assert.Contains(t, out, "Added: Star Rupture (starrupture)")
	assert.NotContains(t, out, "Added: Skyrim Special Edition")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	_, ok := saved["starrupture"]
	assert.True(t, ok)
}

// TestDoGameDetect_ExplicitSelectionRepairsConfiguredGame pins the repair
// path end to end: naming an already-configured game's number re-saves it.
func TestDoGameDetect_ExplicitSelectionRepairsConfiguredGame(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Stale Name"}))

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
	}

	svc := newGameDetectTestService(t)
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	reader := bufio.NewReader(strings.NewReader("1\n"))

	err := doGameDetect(context.Background(), cmd, reader, svc, games)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Added: Skyrim Special Edition (skyrim-se)")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, "skyrim-se")
	assert.Equal(t, "Skyrim Special Edition", saved["skyrim-se"].Name)
}

// TestDoGameDetect_AllExcludedPrintsFriendlyMessage guards the fully-
// configured case: "all" selects nothing, and the message says so instead
// of the generic "No games added." (which reads as if the user declined).
func TestDoGameDetect_AllExcludedPrintsFriendlyMessage(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Skyrim Special Edition"}))

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
	}

	svc := newGameDetectTestService(t)
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	reader := bufio.NewReader(strings.NewReader("all\n"))

	err := doGameDetect(context.Background(), cmd, reader, svc, games)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "already configured")
}

func TestDoGameDetect_NoGamesFound(t *testing.T) {
	configDir = t.TempDir()

	svc := newGameDetectTestService(t)
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	reader := bufio.NewReader(strings.NewReader(""))

	err := doGameDetect(context.Background(), cmd, reader, svc, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No moddable Steam games found.")
}

// TestDoGameDetect_LaterConversionFailureLeavesEarlierGamesPersisted pins
// the pre-lift doGameDetect loop's partial-persistence contract (Task 21
// review Important #1, 2026-08-28): the old loop converted, saved, and
// printed one selected game at a time, so a conversion failure on a later
// game (e.g. an unrecognized deploy_mode) left every earlier selected game
// already saved to games.yaml, its default profile created, and its
// "Added:" line printed - only the failing game and anything after it were
// skipped. Converting every selection up front before saving any of them
// would abort the whole batch instead, undoing that guarantee.
func TestDoGameDetect_LaterConversionFailureLeavesEarlierGamesPersisted(t *testing.T) {
	configDir = t.TempDir()

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
		{Slug: "bad-game", Name: "Bad Game", InstallPath: "/games/bad", DeployMode: "bogus"},
	}

	svc := newGameDetectTestService(t)
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	reader := bufio.NewReader(strings.NewReader("1,2\n"))

	err := doGameDetect(context.Background(), cmd, reader, svc, games)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "converting detected game bad-game")

	out := buf.String()
	assert.Contains(t, out, "Added: Skyrim Special Edition (skyrim-se)\n")
	assert.NotContains(t, out, "Added: Bad Game")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Contains(t, saved, "skyrim-se")
	assert.NotContains(t, saved, "bad-game")

	profile, err := config.LoadProfile(configDir, "skyrim-se", "default")
	require.NoError(t, err)
	assert.True(t, profile.IsDefault)
}

// TestDoGameDetect_JSONOutputReturnsConfirmationRequired pins the
// non-interactive rule (v2 Phase 3 Ruling 2) at doGameDetect's "Add games to
// config?" prompt: under --json with neither --all nor --select, the
// command must fail with core.ErrConfirmationRequired before ever reading
// stdin, and games.yaml is left untouched.
func TestDoGameDetect_JSONOutputReturnsConfirmationRequired(t *testing.T) {
	configDir = t.TempDir()
	withJSONOutput(t)

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
	}
	svc := newGameDetectTestService(t)
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := assertStdinNeverRead(t, func() error {
		return doGameDetect(context.Background(), cmd, bufio.NewReader(poisonReader{t}), svc, games)
	})

	require.ErrorIs(t, err, core.ErrConfirmationRequired)
	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.NotContains(t, saved, "skyrim-se")
}

// TestDoGameDetect_AllFlagSelectsSameSetAsInteractiveAll pins --all:
// non-interactive end state (which games get saved) must match typing "all"
// at the prompt, with no prompt printed and no stdin read.
func TestDoGameDetect_AllFlagSelectsSameSetAsInteractiveAll(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Skyrim Special Edition"}))
	oldAll := gameDetectAll
	gameDetectAll = true
	t.Cleanup(func() { gameDetectAll = oldAll })

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
		{Slug: "starrupture", Name: "Star Rupture", InstallPath: "/games/starrupture", NexusID: "starrupture"},
	}
	svc := newGameDetectTestService(t)
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := assertStdinNeverRead(t, func() error {
		return doGameDetect(context.Background(), cmd, bufio.NewReader(poisonReader{t}), svc, games)
	})

	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "Add games to config?")
	assert.Contains(t, buf.String(), "Added: Star Rupture (starrupture)")
	assert.NotContains(t, buf.String(), "Added: Skyrim Special Edition", "already-configured must stay excluded from --all, matching interactive \"all\"")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Contains(t, saved, "starrupture")
}

// TestDoGameDetect_AllFlagUnderJSON_ProceedsWithoutReadingStdin guards the
// combination the Task 9 review flagged as untested: --all under --json
// together completes the detect rather than hitting the --json/stdin guard.
func TestDoGameDetect_AllFlagUnderJSON_ProceedsWithoutReadingStdin(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Skyrim Special Edition"}))
	withJSONOutput(t)
	oldAll := gameDetectAll
	gameDetectAll = true
	t.Cleanup(func() { gameDetectAll = oldAll })

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
		{Slug: "starrupture", Name: "Star Rupture", InstallPath: "/games/starrupture", NexusID: "starrupture"},
	}
	svc := newGameDetectTestService(t)
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := assertStdinNeverRead(t, func() error {
		return doGameDetect(context.Background(), cmd, bufio.NewReader(poisonReader{t}), svc, games)
	})

	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "Add games to config?")
	assert.Contains(t, buf.String(), "Added: Star Rupture (starrupture)")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	assert.Contains(t, saved, "starrupture")
}

// TestDoGameDetect_SelectFlagSelectsExplicitIndices pins --select: naming an
// already-configured game's index still repairs it, matching the
// interactive explicit-selection path (#205 item 2), with no prompt and no
// stdin read.
func TestDoGameDetect_SelectFlagSelectsExplicitIndices(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Stale Name"}))
	oldSelect := gameDetectSelect
	gameDetectSelect = "1"
	t.Cleanup(func() { gameDetectSelect = oldSelect })

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
	}
	svc := newGameDetectTestService(t)
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := assertStdinNeverRead(t, func() error {
		return doGameDetect(context.Background(), cmd, bufio.NewReader(poisonReader{t}), svc, games)
	})

	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "Add games to config?")
	assert.Contains(t, buf.String(), "Added: Skyrim Special Edition (skyrim-se)")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, "skyrim-se")
	assert.Equal(t, "Skyrim Special Edition", saved["skyrim-se"].Name)
}

// TestDoGameDetect_SelectFlagUnderJSON_ProceedsWithoutReadingStdin guards
// the combination the Task 9 review flagged as untested: --select under
// --json together completes the detect rather than hitting the
// --json/stdin guard.
func TestDoGameDetect_SelectFlagUnderJSON_ProceedsWithoutReadingStdin(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Stale Name"}))
	withJSONOutput(t)
	oldSelect := gameDetectSelect
	gameDetectSelect = "1"
	t.Cleanup(func() { gameDetectSelect = oldSelect })

	games := []steam.DetectedGame{
		{Slug: "skyrim-se", Name: "Skyrim Special Edition", InstallPath: "/games/skyrim", NexusID: "skyrimspecialedition"},
	}
	svc := newGameDetectTestService(t)
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	err := assertStdinNeverRead(t, func() error {
		return doGameDetect(context.Background(), cmd, bufio.NewReader(poisonReader{t}), svc, games)
	})

	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "Add games to config?")
	assert.Contains(t, buf.String(), "Added: Skyrim Special Edition (skyrim-se)")

	saved, err := config.LoadGames(configDir)
	require.NoError(t, err)
	require.Contains(t, saved, "skyrim-se")
	assert.Equal(t, "Skyrim Special Edition", saved["skyrim-se"].Name)
}

// TestGameDetectCmd_AllAndSelectAreMutuallyExclusive pins the flag
// definition: passing both --all and --select is a cobra flag-parse error,
// not a runtime one.
func TestGameDetectCmd_AllAndSelectAreMutuallyExclusive(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.AddCommand(gameCmd)
	t.Cleanup(func() { rootCmd.RemoveCommand(gameCmd); rootCmd.AddCommand(gameCmd) })
	cmd.SetArgs([]string{"game", "detect", "--all", "--select", "1"})
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "if any flags in the group [all select] are set none of the others can be")
}

// TestDoGameDetect_ExistingGamesLoadFailureReportedAsLoadingGames pins Task
// 22 review Important #1's third occurrence (2026-08-28): pre-lift,
// doGameDetect read games.yaml fresh via config.LoadGames on every call,
// wrapping a failure as "loading games: %w". 989eb71 switched to
// service.ListGames(), the in-memory snapshot NewService already loaded
// once - which can't fail, silently dropping this error path. Reachable via
// a games.yaml that goes unreadable between NewService's own load and this
// call - simulated here by opening the service against a valid games.yaml,
// then revoking read permission before calling doGameDetect.
func TestDoGameDetect_ExistingGamesLoadFailureReportedAsLoadingGames(t *testing.T) {
	configDir = t.TempDir()
	require.NoError(t, config.SaveGame(configDir, &domain.Game{ID: "skyrim-se", Name: "Skyrim Special Edition"}))

	svc := newGameDetectTestService(t)

	gamesPath := filepath.Join(configDir, "games.yaml")
	require.NoError(t, os.Chmod(gamesPath, 0000))
	t.Cleanup(func() { _ = os.Chmod(gamesPath, 0644) })

	games := []steam.DetectedGame{
		{Slug: "starrupture", Name: "Star Rupture", InstallPath: "/games/starrupture", NexusID: "starrupture"},
	}
	var buf strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	reader := bufio.NewReader(strings.NewReader("all\n"))

	err := doGameDetect(context.Background(), cmd, reader, svc, games)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading games:")
}
