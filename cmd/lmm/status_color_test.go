package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShowGameStatus_EnabledDisabledCounts_PlainWhenColorDisabled is the
// byte-stability guard: with color off (the default), the Enabled/Disabled
// summary line must carry no ANSI escapes. resetColorFlags undoes
// setupDoDeployTest's noColor=true so this proves the real TTY-detection
// gate keeps piped output plain, not the --no-color flag.
func TestShowGameStatus_EnabledDisabledCounts_PlainWhenColorDisabled(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetColorFlags(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedDeployableMod(t, svc, game, "1", "Enabled Mod", "a.esp")
	seedModWithState(t, svc, game, "2", "Disabled Mod", false, false)

	out := captureStdout(t, func() error {
		return showGameStatus(context.Background(), svc, game.ID)
	})

	assert.NotContains(t, out, "\x1b[")
	assert.Contains(t, out, "Enabled: 1, Disabled: 1")
}

func TestShowGameStatus_EnabledDisabledCounts_ColoredWhenTTY(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetColorFlags(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedDeployableMod(t, svc, game, "1", "Enabled Mod", "a.esp")
	seedModWithState(t, svc, game, "2", "Disabled Mod", false, false)

	withColorCapableStdout(t, true)
	out := captureStdout(t, func() error {
		return showGameStatus(context.Background(), svc, game.ID)
	})

	assert.Contains(t, out, ansiGreen+"1"+ansiReset, "enabled count should be accented green")
	assert.Contains(t, out, ansiDim+"1"+ansiReset, "disabled count should be a dim accent, not a loud red")
}

// TestDoStatus_TableHeader_BoldedWhenTTY_AlignmentUnaffected guards the
// "Configured Games:" summary table: header accenting (#193: bold+cyan, not
// bold-only) must not perturb the tabwriter-computed column alignment (see
// printTable's doc comment).
func TestDoStatus_TableHeader_BoldedWhenTTY_AlignmentUnaffected(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedDeployableMod(t, svc, game, "1", "Test Mod", "a.esp")

	resetColorFlags(t)
	withColorCapableStdout(t, false)
	plain := captureStdout(t, func() error {
		return doStatus(context.Background(), svc)
	})

	withColorCapableStdout(t, true)
	colored := captureStdout(t, func() error {
		return doStatus(context.Background(), svc)
	})

	assert.Contains(t, colored, ansiBold, "table header should be accented when color is enabled")
	assert.Contains(t, colored, ansiCyan, "table header should be accented when color is enabled")
	assert.Equal(t, plain, stripANSI(colored), "color must not change the visible text or alignment")
}

// TestDoStatus_LastColumnCount_CyanWhenTTY_AlignmentUnaffected guards #193's
// richer palette for the "Configured Games:" table: its last column (a mod
// or profile count, depending on --verbose) is safe to color inline since
// tabwriter never pads after the final cell (see printTable's doc comment).
func TestDoStatus_LastColumnCount_CyanWhenTTY_AlignmentUnaffected(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedDeployableMod(t, svc, game, "1", "Test Mod", "a.esp")

	resetColorFlags(t)
	withColorCapableStdout(t, false)
	plain := captureStdout(t, func() error {
		return doStatus(context.Background(), svc)
	})

	withColorCapableStdout(t, true)
	colored := captureStdout(t, func() error {
		return doStatus(context.Background(), svc)
	})

	assert.Contains(t, colored, ansiCyan+"1"+ansiReset, "the last column's count should be a cyan accent")
	assert.Equal(t, plain, stripANSI(colored), "color must not change the visible text or alignment")
}

// TestShowGameStatus_RicherValues_ColoredWhenTTY guards #193's expansion of
// showGameStatus beyond the Enabled/Disabled counts: the active profile
// name, installed-mod count, and per-profile mod count are now colored too
// - "colored values, not just the odd count".
func TestShowGameStatus_RicherValues_ColoredWhenTTY(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetColorFlags(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedDeployableMod(t, svc, game, "1", "Test Mod", "a.esp")
	require.NoError(t, svc.NewProfileManager().SetDefault(context.Background(), game.ID, "default"))

	withColorCapableStdout(t, true)
	out := captureStdout(t, func() error {
		return showGameStatus(context.Background(), svc, game.ID)
	})

	assert.Contains(t, out, ansiGreen+"default"+ansiReset, "the active profile name should be accented green")
	assert.Contains(t, out, ansiGreen+" (active)"+ansiReset, "the active profile marker should be accented green")
	assert.Contains(t, out, ansiCyan+"1"+ansiReset, "a mod count should be a cyan accent")
}

// TestShowGameStatus_RicherValues_PlainWhenColorDisabled is the byte-
// stability guard for the new value accents above. resetColorFlags undoes
// setupDoDeployTest's noColor=true so this proves the real TTY-detection
// gate keeps piped output plain, not the --no-color flag.
func TestShowGameStatus_RicherValues_PlainWhenColorDisabled(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetColorFlags(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	seedDeployableMod(t, svc, game, "1", "Test Mod", "a.esp")
	require.NoError(t, svc.NewProfileManager().SetDefault(context.Background(), game.ID, "default"))

	out := captureStdout(t, func() error {
		return showGameStatus(context.Background(), svc, game.ID)
	})

	assert.NotContains(t, out, "\x1b[")
	assert.Contains(t, out, "Active Profile: default")
	assert.Contains(t, out, "  - default (active): 1 mod(s)")
}
