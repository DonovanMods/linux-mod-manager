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

// profileSwitchWithAFailure is setupDoProfileSwitchTest's game plus a
// profile "alt" listing a cached mod the switch enables and a local mod it
// cannot fetch - "source not found: local", what the second deletion gate
// met switching back to a profile whose local mod's cache was gone (D2).
func profileSwitchWithAFailure(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)
	_, err := pm.Create(ctx, game.ID, "alt")
	require.NoError(t, err)
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "src", "on", "1.0", "on.esp", []byte("on")))
	seedSyncInstalledMod(t, svc, game, "src", "on", "Mod On", "1.0", "alt", false, nil)
	require.NoError(t, pm.AddMod(ctx, game.ID, "alt", domain.ModReference{SourceID: "src", ModID: "on", Version: "1.0"}))
	require.NoError(t, pm.AddMod(ctx, game.ID, "alt", domain.ModReference{SourceID: "local", ModID: "a", Version: "2.0"}))
	withProfileSwitchYes(t)
	return svc, game
}

// TestDoProfileSwitch_AFailedModIsNotSwitched is #470's twin: a switch in
// which a mod failed exits non-zero and never says "Switched"; what did
// not fail is still done, and the target is the active profile.
func TestDoProfileSwitch_AFailedModIsNotSwitched(t *testing.T) {
	svc, game := profileSwitchWithAFailure(t)
	setFlag(t, &jsonOutput, false)

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doProfileSwitch(context.Background(), svc, game, "alt")
	})

	var incomplete *core.ProfileSwitchIncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.Equal(t, exitError, exitCodeFor(err))
	assert.NotContains(t, stdout, "Switched to profile")
	assert.Contains(t, stdout, "✓ Enabled: Mod On")
	assert.Contains(t, stdout, "Error: failed to fetch mod: source not found: local")
	assert.Contains(t, stdout, "\n✗ Profile alt is now active, but 1 mod(s) failed.\n")
	assert.Contains(t, err.Error(), "local:a: failed to fetch mod: source not found: local")
	assert.FileExists(t, filepath.Join(game.ModPath, "on.esp"))
	active, err := getProfileManager(svc).GetDefault(context.Background(), game.ID)
	require.NoError(t, err)
	assert.Equal(t, "alt", active.Name)
}

// TestDoProfileSwitch_JSON_AFailedModIsTheEnvelope pins
// core.ProfileSwitchIncompleteError's wire shape: under --json nothing is
// printed by the command itself, and the error envelope's details are the
// whole SwitchResult - its per-mod outcomes included.
func TestDoProfileSwitch_JSON_AFailedModIsTheEnvelope(t *testing.T) {
	svc, game := profileSwitchWithAFailure(t)
	withJSONOutput(t)

	stdout, stderr, err := captureStdoutAndStderr(t, func() error {
		return doProfileSwitch(context.Background(), svc, game, "alt")
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
}
