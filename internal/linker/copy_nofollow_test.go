package linker_test

// #466 review D2: a copy deploy opened its destination with O_TRUNC, which
// follows a symlink - so a deploy onto a path holding the user's link
// overwrote whatever the link pointed at, outside the game directory. The
// same write went into the inode a hard link shares with another file.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyLinker_DeployNeverWritesThroughALink(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("mod"), 0o644))
	precious := filepath.Join(dir, "precious.txt")
	require.NoError(t, os.WriteFile(precious, []byte("PRECIOUS"), 0o644))
	dst := filepath.Join(dir, "game", "file")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.Symlink(precious, dst))

	err := linker.NewCopy().Deploy(src, dst)

	require.Error(t, err)
	data, rerr := os.ReadFile(precious)
	require.NoError(t, rerr)
	assert.Equal(t, "PRECIOUS", string(data), "the link's target is untouched")
	info, lerr := os.Lstat(dst)
	require.NoError(t, lerr)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "the link is left as it is")
}

func TestCopyLinker_DeployNeverWritesIntoASharedInode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("mod"), 0o644))
	other := filepath.Join(dir, "other")
	require.NoError(t, os.WriteFile(other, []byte("OTHER"), 0o644))
	dst := filepath.Join(dir, "game", "file")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.Link(other, dst))

	require.NoError(t, linker.NewCopy().Deploy(src, dst))

	data, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "mod", string(data))
	data, err = os.ReadFile(other)
	require.NoError(t, err)
	assert.Equal(t, "OTHER", string(data), "the other link to the old inode is untouched")
}

// TestCopyLinker_DeployOverItsOwnSourceKeepsIt: a hard link to the source
// itself - a hardlink deployment redeployed by copy - is replaced, and the
// source keeps its content.
func TestCopyLinker_DeployOverItsOwnSourceKeepsIt(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("mod"), 0o640))
	dst := filepath.Join(dir, "game", "file")
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.Link(src, dst))

	require.NoError(t, linker.NewCopy().Deploy(src, dst))

	for _, p := range []string{src, dst} {
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		assert.Equal(t, "mod", string(data), p)
	}
	si, err := os.Stat(src)
	require.NoError(t, err)
	di, err := os.Stat(dst)
	require.NoError(t, err)
	assert.False(t, os.SameFile(si, di), "the deployed file is a copy now")
	assert.Equal(t, os.FileMode(0o640), di.Mode().Perm())
}
