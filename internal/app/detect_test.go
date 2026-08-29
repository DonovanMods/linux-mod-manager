package app

import (
	"context"
	"errors"
	"testing"

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

	games, warnings, err := DetectGames(context.Background(), configDir)

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

	_, _, err := DetectGames(ctx, t.TempDir())

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}
