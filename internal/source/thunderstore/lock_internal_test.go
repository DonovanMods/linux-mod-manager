package thunderstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSameFile_ALockFileUnlinkedUnderItsHolderIsNotTheLock is the check
// that closes the race a prune introduces (#410): a waiter that wins the
// flock on a lock file which has since been removed - what a prune does
// while holding it - holds nothing a newcomer can see, because the newcomer
// opens a NEW file at the same path. acquireLock drops such a lock and
// takes the one at the path instead; this pins the test it decides on.
func TestSameFile_ALockFileUnlinkedUnderItsHolderIsNotTheLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, lockFileName)
	held, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	require.NoError(t, err)
	defer func() { _ = held.Close() }()
	assert.True(t, sameFileAtPath(t, held, path), "the file just opened is the one at the path")

	require.NoError(t, os.Remove(path))
	assert.False(t, sameFileAtPath(t, held, path), "a removed file is nobody's lock")

	replacement, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	require.NoError(t, err)
	defer func() { _ = replacement.Close() }()
	assert.False(t, sameFileAtPath(t, held, path), "nor is it once another file has taken the name")
	assert.True(t, sameFileAtPath(t, replacement, path))
}

// TestLockCommunity_LocksTheFileAtThePathAfterARemoval: once a prune has
// removed the directory, the next lock re-creates it and holds the file a
// third process will find.
func TestLockCommunity_LocksTheFileAtThePathAfterARemoval(t *testing.T) {
	st := newStore(t.TempDir())
	const community = "lethal-company"

	release, err := st.lockCommunity(t.Context(), community)
	require.NoError(t, err)
	path := filepath.Join(st.dir(community), lockFileName)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Remove(st.dir(community)))
	release()

	release, err = st.lockCommunity(t.Context(), community)
	require.NoError(t, err)
	defer release()

	probe, err := os.OpenFile(path, os.O_RDWR, 0)
	require.NoError(t, err, "the lock file was re-created")
	defer func() { _ = probe.Close() }()
	err = syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	assert.ErrorIs(t, err, syscall.EWOULDBLOCK, "a third process finds the path locked")
}

// sameFileAtPath is sameFile against whatever is at path now.
func sameFileAtPath(t *testing.T, file *os.File, path string) bool {
	t.Helper()
	now, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return sameFile(file, now)
}

// TestLockCommunity_NeverOpensALockThroughASymlink: a link planted at .lock
// is refused, and its target is neither created nor locked.
func TestLockCommunity_NeverOpensALockThroughASymlink(t *testing.T) {
	st := newStore(t.TempDir())
	const community = "lethal-company"
	require.NoError(t, os.MkdirAll(st.dir(community), 0o755))
	outside := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.Symlink(outside, filepath.Join(st.dir(community), lockFileName)))

	_, err := st.lockCommunity(t.Context(), community)
	require.Error(t, err)
	assert.NoFileExists(t, outside)
}

// pathTarget is the lockTarget lockCommunity builds, with a hook that runs
// once the file is open.
func pathTarget(path string, opened func()) lockTarget {
	return lockTarget{
		open: func() (*os.File, error) {
			f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
			if err == nil && opened != nil {
				opened()
			}
			return f, err
		},
		stat: func() (os.FileInfo, error) { return os.Lstat(path) },
	}
}

// TestAcquireLock_AWaiterThatWinsARemovedLockTakesTheOneAtThePath is the
// race the same-file re-check exists for, staged for real (T3 review P4
// B3): a waiter blocked on the lock file a prune then removes wins the
// flock on the removed inode the moment the prune lets go - while a
// newcomer already holds the NEW file at the path. The waiter must not
// think it holds the lock then.
func TestAcquireLock_AWaiterThatWinsARemovedLockTakesTheOneAtThePath(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), lockFileName)

	releasePrune, err := acquireLock(ctx, "c", pathTarget(path, nil))
	require.NoError(t, err)

	opened := make(chan struct{}, 8)
	acquired := make(chan func(), 1)
	go func() {
		release, err := acquireLock(ctx, "c", pathTarget(path, func() { opened <- struct{}{} }))
		if err == nil {
			acquired <- release
		}
	}()
	<-opened // the waiter holds the original file open and is polling its flock

	require.NoError(t, os.Remove(path)) // what the prune does while holding it
	releaseNewcomer, err := acquireLock(ctx, "c", pathTarget(path, nil))
	require.NoError(t, err, "a newcomer takes the new file at once")
	releasePrune() // the waiter now wins the flock on the removed file

	select {
	case release := <-acquired:
		release()
		t.Fatal("the waiter holds a lock on a removed file while the newcomer holds the one at the path")
	case <-time.After(300 * time.Millisecond):
	}

	releaseNewcomer()
	select {
	case release := <-acquired:
		release()
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter never took the lock at the path")
	}
}

// TestAcquireLock_GivesUpWhenItCanNeverHoldTheFileAtThePath is T3 review
// F5's other half: the re-acquire loop consulted neither the caller's
// context nor the deadline, so a lock that never matched its path spun
// forever - in `lmm serve`, pinning the mutation slot until a restart.
func TestAcquireLock_GivesUpWhenItCanNeverHoldTheFileAtThePath(t *testing.T) {
	dir := t.TempDir()
	lockPath, elsewhere := filepath.Join(dir, "lock"), filepath.Join(dir, "elsewhere")
	require.NoError(t, os.WriteFile(elsewhere, nil, 0o600))
	never := lockTarget{
		open: func() (*os.File, error) { return os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600) },
		stat: func() (os.FileInfo, error) { return os.Lstat(elsewhere) },
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		release, err := acquireLock(ctx, "c", never)
		if release != nil {
			release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.DeadlineExceeded), "the caller's deadline ends the wait: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("acquireLock is still looping after its context expired")
	}
}

// TestRemoveIndex_NeverLocksThroughASymlinkInsideTheDirectory is T3 review
// F5: os.Root follows a symbolic link that stays inside the root even with
// O_NOFOLLOW, so a ".lock" linked to index.json had RemoveIndex lock the
// index and spin, and a dangling one had it CREATE the link's target. The
// lock is now opened relative to the proven directory with the kernel's
// own O_NOFOLLOW, and a link is refused at once.
func TestRemoveIndex_NeverLocksThroughASymlinkInsideTheDirectory(t *testing.T) {
	for name, target := range map[string]string{
		"to a file in the index": indexFileName,
		"dangling":               "created-by-lmm",
	} {
		t.Run(name, func(t *testing.T) {
			cacheDir := t.TempDir()
			src := New(Options{CacheDir: cacheDir, BaseURL: "http://127.0.0.1:9"})
			dir := src.store.dir("lethal-company")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, indexFileName), []byte(`{"schema":3}`), 0o644))
			require.NoError(t, os.Symlink(target, filepath.Join(dir, lockFileName)))

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := src.RemoveIndex(ctx, "lethal-company", time.Time{})
				done <- err
			}()
			select {
			case err := <-done:
				require.Error(t, err)
				assert.Contains(t, err.Error(), "symbolic link")
			case <-time.After(time.Second):
				t.Fatal("RemoveIndex is still running: it locked through the link")
			}
			assert.NoFileExists(t, filepath.Join(dir, "created-by-lmm"), "nothing is created through a dangling link")
			assert.FileExists(t, filepath.Join(dir, indexFileName))
		})
	}
}
