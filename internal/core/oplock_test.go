package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLockedOpsService builds a Service whose mutations take the
// cross-process lock at lockPath (#317). Two of them over ONE path is the
// whole test surface: flock is per OPEN FILE DESCRIPTION, so two
// descriptors contend whether they belong to two processes or one, and one
// process is a test that cannot flake on a subprocess.
func newLockedOpsService(t *testing.T, lockPath string) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		OpLockPath: lockPath,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

// TestBeginOp_CrossProcessLockRefusesASecondHolder is #317: within one
// process beginOp's semaphore serializes mutations, but a CLI mutation
// racing `lmm serve`'s had nothing but SQLite's own locking between them -
// the deploy-tree operations could interleave. The second holder now fails
// with a typed error naming the first.
func TestBeginOp_CrossProcessLockRefusesASecondHolder(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".oplock")
	first := newLockedOpsService(t, lockPath)
	second := newLockedOpsService(t, lockPath)

	release, err := first.beginOp(context.Background())
	require.NoError(t, err)

	started := time.Now()
	_, err = second.beginOp(context.Background())
	require.Error(t, err, "a second process must not enter the mutation slot")
	assert.ErrorIs(t, err, ErrOperationInProgress)
	assert.GreaterOrEqual(t, time.Since(started), 100*time.Millisecond,
		"contention waits before it refuses - a mutation that is about to finish should not fail its neighbour")
	assert.Less(t, time.Since(started), opLockWait+2*time.Second, "and the wait is BOUNDED")

	var inProgress *OperationInProgressError
	require.ErrorAs(t, err, &inProgress)
	assert.Equal(t, os.Getpid(), inProgress.PID, "the holder is named by pid")
	assert.False(t, inProgress.StartedAt.IsZero(), "and by when it started")
	assert.Contains(t, err.Error(), "another lmm operation is in progress")

	// Released, the lock is free for the next holder.
	release()
	r2, err := second.beginOp(context.Background())
	require.NoError(t, err, "the lock must be released with the in-process slot")
	r2()
}

// TestBeginOp_CrossProcessLockReleasesOnContextCancel pins that a caller
// whose ctx dies mid-mutation does not leave the lock file held: the
// release func is what drops both halves, and every flow defers it.
func TestBeginOp_CrossProcessLockReleasesOnContextCancel(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".oplock")
	svc := newLockedOpsService(t, lockPath)
	other := newLockedOpsService(t, lockPath)

	ctx, cancel := context.WithCancel(context.Background())
	release, err := svc.beginOp(ctx)
	require.NoError(t, err)
	cancel()
	release()

	r, err := other.beginOp(context.Background())
	require.NoError(t, err, "a cancelled mutation must not strand the lock")
	r()
}

// TestBeginOp_LockFileIsPrivate: the file carries the holder's pid, so it
// is created 0600 rather than world-readable.
func TestBeginOp_LockFileIsPrivate(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".oplock")
	svc := newLockedOpsService(t, lockPath)

	release, err := svc.beginOp(context.Background())
	require.NoError(t, err)
	defer release()

	info, err := os.Stat(lockPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

// TestBeginOp_WithoutALockPathIsUnchanged: a Service built with no lock
// path (every core test that does not care, and any embedder that does not
// supply one) keeps exactly the in-process behaviour it had.
func TestBeginOp_WithoutALockPathIsUnchanged(t *testing.T) {
	a := newOpsService(t)
	b := newOpsService(t)

	ra, err := a.beginOp(context.Background())
	require.NoError(t, err)
	defer ra()

	rb, err := b.beginOp(context.Background())
	require.NoError(t, err, "two Services with no lock path never contend")
	rb()
	assert.False(t, errors.Is(err, ErrOperationInProgress))
}

// TestAcquireOpLock_ContentionRespectsContextCancellation covers the
// branch TestBeginOp_CrossProcessLockReleasesOnContextCancel does not: the
// `case <-ctx.Done()` INSIDE the bounded wait. A caller cancelled while
// queueing behind another lmm must get ctx.Err() and get it promptly - not
// an OperationInProgressError (which would report a refusal that the
// caller's own cancellation caused), and not the full opLockWait (which
// would be a cancellation the wait ignored).
func TestAcquireOpLock_ContentionRespectsContextCancellation(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".oplock")
	holder := newLockedOpsService(t, lockPath)
	release, err := holder.beginOp(context.Background())
	require.NoError(t, err)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	started := time.Now()
	lock, err := acquireOpLock(ctx, lockPath)
	waited := time.Since(started)

	require.Error(t, err)
	assert.Nil(t, lock, "a failed acquire hands back no descriptor to release")
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrOperationInProgress,
		"the caller's own cancellation is not another process refusing it")
	assert.Less(t, waited, opLockWait, "and the wait ends at the cancellation, not at the deadline")
}
