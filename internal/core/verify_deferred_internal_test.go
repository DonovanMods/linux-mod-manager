package core

import (
	"context"
	"path/filepath"
	"sync"
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
