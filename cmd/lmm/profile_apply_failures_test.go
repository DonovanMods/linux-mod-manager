package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profileApplyWithAFailure is setupDoProfileSwitchTest's game, whose active
// profile lists a cached mod the apply enables and a local mod it cannot
// fetch - "source not found: local", what a pinned local mod no cache holds
// meets (#445 final gate F-C).
func profileApplyWithAFailure(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "on", "1.0", "on.esp", []byte("on")))
	seedSyncInstalledMod(t, svc, game, "src", "on", "Mod On", "1.0", "default", false, nil)
	pm := getProfileManager(svc)
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "on", Version: "1.0"}))
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "local", ModID: "a", Version: "2.0"}))
	setFlag(t, &profileApplyYes, true)
	setFlag(t, &profileApplyDryRun, false)
	return svc, game
}

// TestDoProfileApply_AFailedModIsNotApplied is #470: an apply in which a mod
// failed exits non-zero and never says "Applied"; what did not fail is
// still applied.
func TestDoProfileApply_AFailedModIsNotApplied(t *testing.T) {
	svc, game := profileApplyWithAFailure(t)
	setFlag(t, &jsonOutput, false)

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	var incomplete *core.ProfileApplyIncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.Equal(t, exitError, exitCodeFor(err))
	assert.NotContains(t, stdout, "Applied profile")
	assert.Contains(t, stdout, "✓ Enabled: Mod On")
	assert.Contains(t, stdout, "Error: failed to fetch mod: source not found: local")
	assert.Contains(t, err.Error(), "local:a")
	assert.Contains(t, err.Error(), "source not found: local")
	assert.FileExists(t, filepath.Join(game.ModPath, "on.esp"))
}

// TestDoProfileApply_JSON_AFailedModIsTheEnvelope pins
// core.ProfileApplyIncompleteError's wire shape: under --json nothing is
// printed by the command itself, and the error envelope's details are the
// whole ProfileApplyResult - its per-mod outcomes included.
func TestDoProfileApply_JSON_AFailedModIsTheEnvelope(t *testing.T) {
	svc, game := profileApplyWithAFailure(t)
	withJSONOutput(t)

	stdout, stderr, err := captureStdoutAndStderr(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})

	require.Error(t, err)
	assert.Empty(t, stdout, "the command prints nothing itself: Execute prints the envelope")
	assert.Empty(t, stderr)
	envelope := captureStdout(t, func() error { reportError(err); return nil })
	var doc struct {
		Error   string `json:"error"`
		Details struct {
			Enabled  int                        `json:"enabled"`
			Failed   []core.InstalledRef        `json:"failed"`
			Outcomes []core.ProfileApplyOutcome `json:"outcomes"`
		} `json:"details"`
	}
	require.NoError(t, json.Unmarshal([]byte(envelope), &doc), envelope)
	assert.Equal(t, err.Error(), doc.Error)
	assert.Equal(t, 1, doc.Details.Enabled)
	require.Len(t, doc.Details.Failed, 1)
	assert.Equal(t, "a", doc.Details.Failed[0].ModID)
	assert.Equal(t, []core.ProfileApplyOutcome{
		{SourceID: "src", ModID: "on", Name: "Mod On", Version: "1.0", Outcome: core.ProfileApplyEnabled},
		{SourceID: "local", ModID: "a", Version: "2.0", Outcome: core.ProfileApplyFailed, Reason: "failed to fetch mod: source not found: local"},
	}, doc.Details.Outcomes)
	assert.JSONEq(t, `{
		"source_id": "local",
		"mod_id": "a",
		"version": "2.0",
		"outcome": "failed",
		"reason": "failed to fetch mod: source not found: local"
	}`, mustJSON(t, doc.Details.Outcomes[1]))
}

// captureIncompleteApply runs fn - a doProfileApply in which a mod fails -
// and returns what it printed, requiring #470's incomplete-apply error.
func captureIncompleteApply(t *testing.T, fn func() error) string {
	t.Helper()
	stdout, _, err := captureStdoutAndStderr(t, fn)
	var incomplete *core.ProfileApplyIncompleteError
	require.ErrorAs(t, err, &incomplete)
	return stdout
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return string(data)
}
