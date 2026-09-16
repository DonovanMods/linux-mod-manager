package main

// #455: `lmm conflicts` on a game whose adapter is refused reports the
// refusal itself - the sentence, and under --json its game and adapter -
// the same answer GET /api/v1/conflicts gives.

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoConflicts_ARefusedGameReportsTheRefusal(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")
	game.Adapter = "no-such-adapter"
	require.NoError(t, svc.SaveGame(context.Background(), game))

	err := doConflicts(context.Background(), svc, game)
	var refused *core.AdapterRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, `game "`+game.ID+`": unknown adapter "no-such-adapter" (registered: generic-files)`, err.Error(),
		"the refusal, not a wrapped \"getting conflicts\" failure")
}

func TestReportError_JSON_AdapterRefusedError(t *testing.T) {
	withJSONOutput(t)
	svc, game := setupDoDeployTest(t)
	game.Adapter = "no-such-adapter"
	_, err := svc.AdapterFor(&domain.Game{ID: game.ID, Adapter: "no-such-adapter"})
	var refused *core.AdapterRefusedError
	require.True(t, errors.As(err, &refused))

	out := captureStdout(t, func() error { reportError(err); return nil })
	assert.Equal(t, "{\n"+
		"  \"error\": \"game \\\""+game.ID+"\\\": unknown adapter \\\"no-such-adapter\\\" (registered: generic-files)\",\n"+
		"  \"details\": {\n"+
		"    \"game_id\": \""+game.ID+"\",\n"+
		"    \"adapter\": \"no-such-adapter\"\n"+
		"  }\n"+
		"}\n", out)
}
