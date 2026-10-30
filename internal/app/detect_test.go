package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDetectGames_NoSteamLibrary mirrors cmd/lmm's
// TestRunGameDetect_OpensServiceOnFreshInstall fixture: HOME isolated to a
// fresh temp dir with no Steam library, so steam.DetectGames' FindSteamRoots
// finds nothing and DetectGames returns a clean empty result (not an
// error), matching the pre-lift steam.DetectGames(configDir) call
// runGameDetect made directly.
func TestDetectGames_NoSteamLibrary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configDir := t.TempDir()

	games, warnings, err := DetectGames(context.Background(), configDir, DetectOptions{})

	require.NoError(t, err)
	assert.Empty(t, games)
	assert.Empty(t, warnings)
}

// TestDetectGames_CancelledContext pins app.Open's convention (paths.go/
// app.go: "an already-cancelled ctx aborts before any ... work") applied to
// DetectGames: a cancelled ctx returns ctx.Err() before the scan runs, so a
// caller cancelling detect gets a normal cancellation error instead of
// whatever steam.DetectGames would have produced.
func TestDetectGames_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := DetectGames(ctx, t.TempDir(), DetectOptions{})

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

// writeTestFile writes content to path, making its directory.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// #545: one scan answers for both detectors - Steam's rows, then
// Battle.net's - so every frontend that calls DetectGames (game detect,
// game add --from-detected, init, serve's detect and Add Game) lists a
// Battle.net game with no code of its own.
func TestDetectGames_MergesBattleNetWithSteam(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")
	t.Setenv("WINEPREFIX", "")

	steamapps := filepath.Join(home, ".steam", "steam", "steamapps")
	require.NoError(t, os.MkdirAll(filepath.Join(steamapps, "common", "Skyrim Special Edition"), 0o755))
	writeTestFile(t, filepath.Join(steamapps, "appmanifest_489830.acf"),
		"\"AppState\"\n{\n\t\"appid\"\t\t\"489830\"\n\t\"name\"\t\t\"Skyrim Special Edition\"\n\t\"installdir\"\t\t\"Skyrim Special Edition\"\n}\n")

	prefix := filepath.Join(home, "Games", "battlenet")
	writeTestFile(t, filepath.Join(prefix, "system.reg"), "WINE REGISTRY Version 2\n")
	root := filepath.Join(prefix, "drive_c", "Program Files (x86)", "World of Warcraft")
	writeTestFile(t, filepath.Join(root, ".build.info"), "Branch!STRING:0|Active!DEC:1|Product!STRING:0\nus|1|wow\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_retail_"), 0o755))

	games, warnings, err := DetectGames(context.Background(), t.TempDir(), DetectOptions{})
	require.NoError(t, err)
	assert.Empty(t, warnings)
	var refs []string
	for _, g := range games {
		refs = append(refs, g.Ref())
	}
	assert.Equal(t, []string{"steam:489830", "battlenet:wow"}, refs)
}

// An unknown Battle.net row never takes a slug a Steam row already has: a
// slug is how a detect selection names a row.
func TestDetectGames_BattleNetSlugsNeverCollideWithSteams(t *testing.T) {
	steamRows := []domain.DetectedGame{{Detector: domain.DetectorSteam, DetectorID: "1", Slug: "starcraft"}}
	bnetRows := []domain.DetectedGame{{Detector: domain.DetectorBattleNet, DetectorID: "s1", Slug: "starcraft"}}

	merged := mergeDetected(steamRows, bnetRows)
	require.Len(t, merged, 2)
	assert.Equal(t, "starcraft", merged[0].Slug)
	assert.Equal(t, "starcraft-battlenet", merged[1].Slug)
}
