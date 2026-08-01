package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShowGameStatus_EnabledDisabledCounts_PlainWhenColorDisabled is the
// byte-stability guard: with color off (the default), the Enabled/Disabled
// summary line must carry no ANSI escapes.
func TestShowGameStatus_EnabledDisabledCounts_PlainWhenColorDisabled(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	require.NoError(t, svc.AddGame(game))
	seedDeployableMod(t, svc, game, "1", "Enabled Mod", "a.esp")
	seedModWithState(t, svc, game, "2", "Disabled Mod", false, false)

	out := captureStdout(t, func() error {
		return showGameStatus(svc, game.ID)
	})

	assert.NotContains(t, out, "\x1b[")
	assert.Contains(t, out, "Enabled: 1, Disabled: 1")
}

func TestShowGameStatus_EnabledDisabledCounts_ColoredWhenTTY(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetColorFlags(t)
	require.NoError(t, svc.AddGame(game))
	seedDeployableMod(t, svc, game, "1", "Enabled Mod", "a.esp")
	seedModWithState(t, svc, game, "2", "Disabled Mod", false, false)

	withColorCapableStdout(t, true)
	out := captureStdout(t, func() error {
		return showGameStatus(svc, game.ID)
	})

	assert.Contains(t, out, ansiGreen+"1"+ansiReset, "enabled count should be accented green")
	assert.Contains(t, out, ansiDim+"1"+ansiReset, "disabled count should be a dim accent, not a loud red")
}

// TestDoStatus_TableHeader_BoldedWhenTTY_AlignmentUnaffected guards the
// "Configured Games:" summary table: header bolding must not perturb the
// tabwriter-computed column alignment (see printTable's doc comment).
func TestDoStatus_TableHeader_BoldedWhenTTY_AlignmentUnaffected(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	require.NoError(t, svc.AddGame(game))
	seedDeployableMod(t, svc, game, "1", "Test Mod", "a.esp")

	resetColorFlags(t)
	withColorCapableStdout(t, false)
	plain := captureStdout(t, func() error {
		return doStatus(svc)
	})

	withColorCapableStdout(t, true)
	colored := captureStdout(t, func() error {
		return doStatus(svc)
	})

	assert.Contains(t, colored, ansiBold, "table header should be bolded when color is enabled")
	assert.Equal(t, plain, stripANSI(colored), "color must not change the visible text or alignment")
}
