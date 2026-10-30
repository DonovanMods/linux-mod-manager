package core

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedVerifyPair is two Services over one cross-process lock (#317's
// stand-in for two lmm processes), the second with a game and profile to
// verify.
func lockedVerifyPair(t *testing.T) (other, svc *Service, game *domain.Game) {
	t.Helper()
	lockPath := filepath.Join(t.TempDir(), ".oplock")
	other = newLockedOpsService(t, lockPath)
	svc = newLockedOpsService(t, lockPath)
	game = &domain.Game{ID: "g1", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	return other, svc, game
}

// TestVerify_AnotherProcessHoldingTheLockDefersTheCheck is #588 across
// processes: `lmm serve` checking health while a CLI deploy runs (or the
// reverse) cannot see the other's in-process slot, only the lock. A held
// lock defers the check - probed, never waited for.
func TestVerify_AnotherProcessHoldingTheLockDefersTheCheck(t *testing.T) {
	other, svc, game := lockedVerifyPair(t)
	release, err := other.beginOp(context.Background())
	require.NoError(t, err)

	for _, force := range []bool{false, true} {
		result, err := svc.verifyGated(context.Background(), game, "default", VerifyOptions{Tier: VerifyLocal, Force: force}, nil)
		require.NoError(t, err)
		assert.Equal(t, VerifyDeferredJobRunning, result.Deferred, "force=%t", force)
	}

	release()
	result, err := svc.verifyGated(context.Background(), game, "default", VerifyOptions{Tier: VerifyLocal}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Deferred, "with the lock free, the check runs")

	// The probe let the lock go: the other process can take it again at once.
	r2, err := other.tryBeginOp(context.Background())
	require.NoError(t, err)
	r2()
}

// TestVerify_AnotherProcessMutatingDuringTheWalkDefersIt: a mutation in
// another process that comes and goes while the walk runs leaves no held
// lock to find afterwards, only its stamp on the lock file.
func TestVerify_AnotherProcessMutatingDuringTheWalkDefersIt(t *testing.T) {
	other, svc, game := lockedVerifyPair(t)

	var once sync.Once
	result, err := svc.verifyGated(context.Background(), game, "default", VerifyOptions{Tier: VerifyLocal, observe: true}, func(Event) {
		once.Do(func() {
			release, err := other.beginOp(context.Background())
			require.NoError(t, err)
			release()
		})
	})
	require.NoError(t, err)
	assert.Equal(t, VerifyDeferredJobRunning, result.Deferred)

	next, err := svc.verifyGated(context.Background(), game, "default", VerifyOptions{Tier: VerifyLocal}, nil)
	require.NoError(t, err)
	assert.False(t, next.Cached, "the overlapped walk was not memoised")
	assert.Empty(t, next.Deferred)
}

// TestVerify_OnlyFailedTryBeginOpsDuringTheWalkAreNotAMutation (#588
// review): the no-wait acquires - the automatic update pass, the external
// revision reconcile, the startup backfill - run beside a page load's health
// read and are refused whenever anything holds the lock, the check's own
// probe included. A refused one ran nothing, so a walk it overlapped is an
// answer, memoised like any other - not thrown away as deferred.
func TestVerify_OnlyFailedTryBeginOpsDuringTheWalkAreNotAMutation(t *testing.T) {
	_, svc, game := lockedVerifyPair(t)

	// The other process holds the lock on a descriptor of its own for the
	// instant of each refusal: no stamp, nothing run.
	holder, err := os.OpenFile(svc.opLockPath, os.O_RDWR|os.O_CREATE, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Close() })

	var once sync.Once
	result, err := svc.verifyGated(context.Background(), game, "default", VerifyOptions{Tier: VerifyLocal, observe: true}, func(Event) {
		once.Do(func() {
			require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
			for range 3 {
				_, err := svc.tryBeginOp(context.Background())
				require.ErrorIs(t, err, ErrOperationInProgress)
			}
			require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_UN))
		})
	})
	require.NoError(t, err)
	assert.Empty(t, result.Deferred, "refused acquires ran nothing")

	next, err := svc.verifyGated(context.Background(), game, "default", VerifyOptions{Tier: VerifyLocal}, nil)
	require.NoError(t, err)
	assert.True(t, next.Cached, "the walk was memoised")
}

// TestVerify_ASucceededTryBeginOpDuringTheWalkDefersIt is the other half:
// a no-wait acquire that got the slot is a mutation like any other.
func TestVerify_ASucceededTryBeginOpDuringTheWalkDefersIt(t *testing.T) {
	_, svc, game := lockedVerifyPair(t)

	var once sync.Once
	result, err := svc.verifyGated(context.Background(), game, "default", VerifyOptions{Tier: VerifyLocal, observe: true}, func(Event) {
		once.Do(func() {
			release, err := svc.tryBeginOp(context.Background())
			require.NoError(t, err)
			release()
		})
	})
	require.NoError(t, err)
	assert.Equal(t, VerifyDeferredJobRunning, result.Deferred)

	next, err := svc.verifyGated(context.Background(), game, "default", VerifyOptions{Tier: VerifyLocal}, nil)
	require.NoError(t, err)
	assert.False(t, next.Cached, "the overlapped walk was not memoised")
}
