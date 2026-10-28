package core_test

// #470's twin in `lmm profile switch` (#445 second gate). A switch in which
// a mod could not be fetched, downloaded or deployed still ended "Switched"
// with exit status 0 - a deploy failure in the enable loop was only a
// --verbose note - so `lmm profile switch default && play` played with a
// mod missing. The switch still carries on and still makes the target the
// active profile (its other mods are live, and the old profile's are down),
// but it lists what it did with each mod and returns a
// ProfileSwitchIncompleteError carrying the result when any mod failed.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileSwitch_AFailedModIsReportedAndFailsTheSwitch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	ctx := context.Background()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkCopy))
	// default, active, has old deployed; alt lists on and bad - both
	// cached under default and not deployed, bad's file unreadable - and a
	// local mod nothing holds.
	f.profile(t, "default", true, "old", "on", "bad")
	doc := "name: alt\ngame_id: sky\nmods:\n" +
		"    - source_id: local\n      mod_id: on\n      version: unknown\n" +
		"    - source_id: local\n      mod_id: bad\n      version: unknown\n" +
		"    - source_id: local\n      mod_id: ghost\n      version: \"2.0\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(f.svc.ConfigDir(), "games", "sky", "profiles", "alt.yaml"), []byte(doc), 0o644))
	f.deployed(t, "default", "old", domain.LinkCopy, map[string]string{"old.esp": "old"}, nil)
	cachedRow(t, f.svc, f.game, "default", "on", "unknown", map[string]string{"on.esp": "on"})
	cachedRow(t, f.svc, f.game, "default", "bad", "unknown", map[string]string{"bad.esp": "bad"})
	unreadable := f.svc.GetGameCache(f.game).GetFilePath("sky", "local", "bad", "unknown", "bad.esp")
	require.NoError(t, os.Chmod(unreadable, 0o000))
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })

	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "alt")
	require.NoError(t, err)
	result, err := f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)

	var incomplete *core.ProfileSwitchIncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.Same(t, result, incomplete.Result)
	assert.Contains(t, err.Error(), `profile "alt" is now active, but 2 mod(s) failed:`)
	assert.Contains(t, err.Error(), "Mod bad (local:bad): deploy failed:")
	assert.Contains(t, err.Error(), "local:ghost: failed to fetch mod: source not found: local")

	require.Len(t, result.Failed, 2)
	assert.Equal(t, "bad", result.Failed[0].ModID)
	assert.Equal(t, "ghost", result.Failed[1].ModID)
	assert.Equal(t, 1, result.Disabled)
	assert.Equal(t, 1, result.Enabled)
	require.Len(t, result.Outcomes, 4)
	assert.Equal(t, core.ProfileApplyOutcome{SourceID: "local", ModID: "old", Name: "Mod old", Version: "unknown", Outcome: core.ProfileApplyDisabled}, result.Outcomes[0])
	assert.Equal(t, core.ProfileApplyOutcome{SourceID: "local", ModID: "on", Name: "Mod on", Version: "unknown", Outcome: core.ProfileApplyEnabled, FromProfile: "default"}, result.Outcomes[1])
	assert.Equal(t, core.ProfileApplyFailed, result.Outcomes[2].Outcome)
	assert.Equal(t, "bad", result.Outcomes[2].ModID)
	assert.Equal(t, core.ProfileApplyOutcome{SourceID: "local", ModID: "ghost", Version: "2.0", Outcome: core.ProfileApplyFailed, Reason: "failed to fetch mod: source not found: local"}, result.Outcomes[3])

	// What did not fail is done: alt is active, on is live, old is down.
	active, err := f.svc.NewProfileManager().GetDefault(ctx, "sky")
	require.NoError(t, err)
	assert.Equal(t, "alt", active.Name)
	assert.Equal(t, "on", readLive(t, filepath.Join(f.game.ModPath, "on.esp")))
	assert.NoFileExists(t, filepath.Join(f.game.ModPath, "old.esp"))
	assert.NoFileExists(t, filepath.Join(f.game.ModPath, "bad.esp"))
}

// TestProfileSwitch_AlreadyDeployedCountsNoFailure: a switch with nothing
// failing returns no error and still lists its outcomes.
func TestProfileSwitch_ACleanSwitchListsItsOutcomes(t *testing.T) {
	ctx := context.Background()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkSymlink))
	f.profile(t, "default", true, "old")
	f.profile(t, "alt", false, "on")
	f.deployed(t, "default", "old", domain.LinkSymlink, map[string]string{"old.esp": "old"}, nil)
	cachedRow(t, f.svc, f.game, "default", "on", "unknown", map[string]string{"on.esp": "on"})

	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "alt")
	require.NoError(t, err)
	result, err := f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)

	require.NoError(t, err)
	assert.Empty(t, result.Failed)
	assert.Equal(t, []core.ProfileApplyOutcome{
		{SourceID: "local", ModID: "old", Name: "Mod old", Version: "unknown", Outcome: core.ProfileApplyDisabled},
		{SourceID: "local", ModID: "on", Name: "Mod on", Version: "unknown", Outcome: core.ProfileApplyEnabled, FromProfile: "default"},
	}, result.Outcomes)
}

// requireIncompleteSwitch is what a switch that ran to the end with a
// failed mod returns since #470's twin: the result, and a
// ProfileSwitchIncompleteError carrying it.
func requireIncompleteSwitch(t *testing.T, result *core.SwitchResult, err error, msgAndArgs ...any) {
	t.Helper()
	var incomplete *core.ProfileSwitchIncompleteError
	require.ErrorAs(t, err, &incomplete, msgAndArgs...)
	require.Same(t, result, incomplete.Result)
	require.NotEmpty(t, result.Failed, msgAndArgs...)
}
