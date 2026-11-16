package core

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #659: a recorded member is present only as a regular file reached through
// real directories; one under a link to a directory is gone, and judging it
// never looks through the link (the target directory is unreadable).
func TestMemberPresent_NeverLooksThroughALink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "locked"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "locked", "a.esp"), []byte("x"), 0o644))
	require.NoError(t, os.Chmod(filepath.Join(outside, "locked"), 0))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(outside, "locked"), 0o755) })

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "real"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "real", "ok.esp"), []byte("x"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(outside, "locked"), filepath.Join(dir, "linked")))
	require.NoError(t, os.Symlink("/etc/hostname", filepath.Join(dir, "real", "link.esp")))
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "real", "pipe.esp"), 0o644))

	gone := goneMembers(dir, []string{
		filepath.Join("real", "ok.esp"),
		filepath.Join("linked", "a.esp"),
		filepath.Join("real", "link.esp"),
		filepath.Join("real", "pipe.esp"),
		filepath.Join("real", "absent.esp"),
	})
	assert.Equal(t, []string{"linked/a.esp", "real/link.esp", "real/pipe.esp", "real/absent.esp"}, gone)
}

// cacheLinks reports a link to a directory itself and never descends into
// it, and leaves regular files and directories alone.
func TestCacheLinks_ReportsWithoutDescending(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(outside, "inner")))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "ok.esp"), []byte("x"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "sub", "dirlink")))

	links, err := cacheLinks(dir)
	require.NoError(t, err)
	assert.Equal(t, []cacheLink{{rel: filepath.Join("sub", "dirlink"), kind: "symbolic link"}}, links)

	none, err := cacheLinks(filepath.Join(dir, "absent"))
	require.NoError(t, err)
	assert.Empty(t, none)
}

// removeCacheLink re-checks before removing: a path that is a regular file
// by then is left, and a link is unlinked without touching its target.
func TestRemoveCacheLink_RechecksAndUnlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(target, []byte("T"), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "link")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "regular"), []byte("R"), 0o644))

	require.NoError(t, removeCacheLink(dir, "link"))
	_, err := os.Lstat(filepath.Join(dir, "link"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	b, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "T", string(b))

	require.Error(t, removeCacheLink(dir, "regular"))
	assert.FileExists(t, filepath.Join(dir, "regular"))
}
