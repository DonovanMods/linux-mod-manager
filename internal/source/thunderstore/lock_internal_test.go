package thunderstore

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

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
