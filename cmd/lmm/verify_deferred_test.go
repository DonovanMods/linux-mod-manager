package main

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoVerify_AnotherProcessMutatingRefusesRatherThanReporting is #588 in
// the terminal: `lmm verify` beside a running `lmm serve` job (or another
// CLI mutation) does not walk files being rewritten. It says so and exits
// non-zero - a half-written tree's findings would be false, and an empty
// "all verified" would be just as false - in both output modes.
func TestDoVerify_AnotherProcessMutatingRefusesRatherThanReporting(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".oplock")
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), OpLockPath: lockPath,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err = svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	verifyProfile = "default"
	t.Cleanup(func() { verifyProfile = "" })

	// The other process: a descriptor of its own holding the lock.
	holder, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Close() })
	require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))

	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, asJSON := range []bool{false, true} {
		jsonOutput = asJSON
		out, _, runErr := captureStdoutStderrErr(t, func() error {
			return doVerify(context.Background(), svc, game, nil)
		})
		require.Error(t, runErr, "json=%t", asJSON)
		assert.ErrorIs(t, runErr, core.ErrOperationInProgress)
		assert.Contains(t, runErr.Error(), "run `lmm verify` again once it finishes")
		assert.NotContains(t, out, "verified OK", "json=%t", asJSON)
		assert.NotContains(t, out, "Nothing to report", "json=%t", asJSON)
	}

	require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_UN))
	jsonOutput = false
	_, _, runErr := captureStdoutStderrErr(t, func() error {
		return doVerify(context.Background(), svc, game, nil)
	})
	assert.NoError(t, runErr, "with the lock free, verify runs")
}
