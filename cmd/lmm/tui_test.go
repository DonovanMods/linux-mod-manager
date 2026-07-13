package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunTUIRealModeRequiresAGame tests that real mode (no --prototype) goes
// through the same game-resolution path as other CLI commands, failing
// before the TUI program starts when no game is specified.
func TestRunTUIRealModeRequiresAGame(t *testing.T) {
	tuiOptions.prototype = false
	tuiOptions.theme = "wizardry"
	// Reset flags. configDir must point at an empty tempdir so requireGame
	// does not pick up a default-game from the user's real ~/.config/lmm.
	gameID = ""
	configDir = t.TempDir()
	t.Cleanup(func() {
		tuiOptions.prototype = false
		tuiOptions.theme = "wizardry"
	})

	err := runTUI(tuiCmd, nil)
	require.ErrorContains(t, err, "no game specified")
}

func TestRunTUIRejectsUnknownTheme(t *testing.T) {
	tuiOptions.prototype = true
	tuiOptions.theme = "vaporwave"
	t.Cleanup(func() {
		tuiOptions.prototype = false
		tuiOptions.theme = "wizardry"
	})

	err := runTUI(tuiCmd, nil)
	require.ErrorContains(t, err, `unknown TUI theme "vaporwave"`)
}
