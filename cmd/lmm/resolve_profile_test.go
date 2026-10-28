package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newResolveTestService builds a service against temp dirs with one game
// registered, returning the service (closed via t.Cleanup).
func newResolveTestService(t *testing.T) *core.Service {
	t.Helper()
	prevConfigDir, prevDataDir := configDir, dataDir
	t.Cleanup(func() { configDir, dataDir = prevConfigDir, prevDataDir })
	configDir = t.TempDir()
	dataDir = t.TempDir()

	svc, err := initService(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:      "testgame",
		Name:    "Test Game",
		ModPath: t.TempDir(),
	}))
	return svc
}

func TestResolveProfile_ExplicitFlagWins(t *testing.T) {
	svc := newResolveTestService(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), "testgame", "active")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(context.Background(), "testgame", "active"))

	got, err := resolveProfile(context.Background(), svc, "testgame", "explicit")
	require.NoError(t, err)
	assert.Equal(t, "explicit", got, "an explicit -p value must win over the active profile")
}

func TestResolveProfile_UsesActiveProfileAfterSwitch(t *testing.T) {
	svc := newResolveTestService(t)
	pm := svc.NewProfileManager()
	for _, name := range []string{"default", "target"} {
		_, err := pm.Create(context.Background(), "testgame", name)
		require.NoError(t, err)
	}
	// Equivalent of `lmm profile switch target` for an empty profile.
	require.NoError(t, pm.SetDefault(context.Background(), "testgame", "target"))

	got, err := resolveProfile(context.Background(), svc, "testgame", "")
	require.NoError(t, err)
	assert.Equal(t, "target", got, "flagless commands must operate on the active profile, not the literal \"default\"")
}

func TestResolveProfile_FallsBackToFirstProfile(t *testing.T) {
	svc := newResolveTestService(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(context.Background(), "testgame", "solo")
	require.NoError(t, err)

	got, err := resolveProfile(context.Background(), svc, "testgame", "")
	require.NoError(t, err)
	assert.Equal(t, "solo", got, "with no IsDefault flag set, GetDefault's first-profile fallback applies")
}

func TestResolveProfile_NoProfilesFallsBackToDefault(t *testing.T) {
	svc := newResolveTestService(t)

	got, err := resolveProfile(context.Background(), svc, "testgame", "")
	require.NoError(t, err)
	assert.Equal(t, "default", got, "a fresh setup with no profiles keeps the historical \"default\" convention")
}

// End-to-end repro of the smoke-test failure: after `lmm profile switch
// target`, a flagless `lmm list` must report the "target" profile.
func TestListCmd_UsesActiveProfileAfterSwitch(t *testing.T) {
	svc := newResolveTestService(t)
	pm := svc.NewProfileManager()
	for _, name := range []string{"default", "target"} {
		_, err := pm.Create(context.Background(), "testgame", name)
		require.NoError(t, err)
	}
	require.NoError(t, pm.SetDefault(context.Background(), "testgame", "target"))

	game, err := svc.GetGame("testgame")
	require.NoError(t, err)

	prevListProfile, prevJSONOutput := listProfile, jsonOutput
	t.Cleanup(func() { listProfile, jsonOutput = prevListProfile, prevJSONOutput })
	listProfile = ""
	jsonOutput = true

	out := captureStdout(t, func() error {
		return doList(context.Background(), &cobra.Command{}, svc, game)
	})
	assert.Contains(t, out, `"profile": "target"`, "flagless list must use the active profile")
}
