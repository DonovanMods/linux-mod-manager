package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

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

	// The other process: a descriptor of its own holding the lock, stamped
	// as a holder stamps it.
	holder, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Close() })
	require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	started := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	_, err = fmt.Fprintf(holder, "4242\n%s\n", started.Format(time.RFC3339))
	require.NoError(t, err)

	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, asJSON := range []bool{false, true} {
		jsonOutput = asJSON
		out, _, runErr := captureStdoutStderrErr(t, func() error {
			return doVerify(context.Background(), svc, game, nil)
		})
		require.Error(t, runErr, "json=%t", asJSON)
		// #588 review: the standard refusal, naming the holder, so --json
		// carries its pid and start as every other refusal does.
		var opErr *core.OperationInProgressError
		require.ErrorAs(t, runErr, &opErr, "json=%t", asJSON)
		assert.Equal(t, 4242, opErr.PID)
		assert.True(t, started.Equal(opErr.StartedAt))
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

// deferredReport is a VerifyReport core answered without walking (#588).
func deferredReport() *core.VerifyReport {
	return &core.VerifyReport{Result: &core.VerifyResult{Deferred: core.VerifyDeferredJobRunning}}
}

// TestVerifySettled_AnOverlapThatHasEndedChecksAgain (#588 review): a walk
// another lmm's mutation overlapped, where that mutation has already
// finished, is not "an operation in progress" - nothing is. The tree is
// settled now, so verify walks it once more and reports that walk.
func TestVerifySettled_AnOverlapThatHasEndedChecksAgain(t *testing.T) {
	clean := &core.VerifyReport{Result: &core.VerifyResult{Checked: 3}}
	answers := []*core.VerifyReport{deferredReport(), clean}
	runs, retries := 0, 0
	report, err := verifySettled(
		func() (*core.VerifyReport, error) { runs++; return answers[runs-1], nil },
		func() error { return nil },
		func() { retries++ },
	)
	require.NoError(t, err)
	assert.Same(t, clean, report)
	assert.Equal(t, 2, runs)
	assert.Equal(t, 1, retries, "the user is told it is checking again")
}

// TestVerifySettled_OverlappedTwiceRefuses: a second overlap is refused -
// without claiming a job is running when none is.
func TestVerifySettled_OverlappedTwiceRefuses(t *testing.T) {
	runs := 0
	_, err := verifySettled(
		func() (*core.VerifyReport, error) { runs++; return deferredReport(), nil },
		func() error { return nil },
		func() {},
	)
	require.Error(t, err)
	assert.Equal(t, 2, runs, "re-run once, not forever")
	assert.NotErrorIs(t, err, core.ErrOperationInProgress, "nothing is in progress now")
	assert.Contains(t, err.Error(), "run `lmm verify` again")
}

// TestVerifySettled_ARunningMutationRefusesAtOnce: one that is still
// running is the standard refusal, and is not waited out by a re-run.
func TestVerifySettled_ARunningMutationRefusesAtOnce(t *testing.T) {
	running := &core.OperationInProgressError{PID: 4242}
	runs := 0
	_, err := verifySettled(
		func() (*core.VerifyReport, error) { runs++; return deferredReport(), nil },
		func() error { return running },
		func() { t.Fatal("no re-run while it is still running") },
	)
	assert.Same(t, running, err)
	assert.Equal(t, 1, runs)

	// And a walk's own error is returned as it is.
	boom := errors.New("boom")
	_, err = verifySettled(
		func() (*core.VerifyReport, error) { return nil, boom },
		func() error { return running },
		func() {},
	)
	assert.Same(t, boom, err)
}
