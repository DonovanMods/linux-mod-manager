package main

// #445 review F2 at the CLI: with no single profile marked active, `lmm
// deploy` and `lmm purge` refuse and name `lmm profile list`, and `lmm
// profile switch` - the way out - only marks the profile it is given,
// saying that nothing was deployed or removed and what to run next.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unmarkProfile takes `is_default: true` out of profile's file by hand.
func unmarkProfile(t *testing.T, gameID, profile string) {
	t.Helper()
	path := filepath.Join(configDir, "games", gameID, "profiles", profile+".yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "is_default: true\n", "")), 0o644))
}

func TestDoProfileSwitch_WithNoSingleActiveProfileOnlyMarksTheTarget(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t) // "default" is active
	_, err := getProfileManager(svc).Create(ctx, game.ID, "alt")
	require.NoError(t, err)
	unmarkProfile(t, game.ID, "default")

	setFlag(t, &deployProfile, "default")
	_, _, err = captureStdoutAndStderr(t, func() error { return doDeploy(ctx, svc, game, nil) })
	require.ErrorIs(t, err, core.ErrActiveProfileUnknown)
	assert.Contains(t, err.Error(), "lmm profile list")

	setFlag(t, &profileSwitchDryRun, true)
	stdout, stderr, err := captureStdoutAndStderr(t, func() error { return doProfileSwitch(ctx, svc, game, "alt") })
	require.NoError(t, err)
	assert.Contains(t, stderr, "Warning: no single profile of g1 is marked active")
	assert.Contains(t, stderr, "only marks alt as the active profile - nothing is deployed or removed")
	assert.NotContains(t, stdout, "Marked")

	setFlag(t, &profileSwitchDryRun, false)
	setFlag(t, &profileSwitchYes, true)
	stdout, stderr, err = captureStdoutAndStderr(t, func() error { return doProfileSwitch(ctx, svc, game, "alt") })
	require.NoError(t, err)
	assert.Contains(t, stdout, "✓ Marked alt as the active profile of g1")
	assert.Contains(t, stderr, "Warning: alt is now the active profile of g1, but nothing was deployed or removed")
	assert.Contains(t, stderr, "may still hold files another profile deployed")
	assert.Contains(t, stderr, "run `lmm deploy` to deploy alt's mods, then `lmm verify`")

	alt, err := getProfileManager(svc).Get(ctx, game.ID, "alt")
	require.NoError(t, err)
	assert.True(t, alt.IsDefault)
	setFlag(t, &deployProfile, "alt")
	setFlag(t, &deployDryRun, true)
	_, _, err = captureStdoutAndStderr(t, func() error { return doDeploy(ctx, svc, game, nil) })
	require.NoError(t, err, "one profile is marked again, so deploy runs")
}
