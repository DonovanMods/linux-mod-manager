package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCopyDir_FollowsSymlinkedSubdir is a regression test for the
// cross-device-rename fallback path in Importer.Import: copyDir used
// filepath.Walk, which lstats every entry and never follows symlinks. A
// symlinked subdirectory therefore reported IsDir()==false, fell through to
// copyFileStreaming, and os.Open on it (which *does* follow the link, landing
// on a directory) failed with EISDIR when read as a byte stream. copyDir must
// os.Stat (follow) to classify entries instead.
func TestCopyDir_FollowsSymlinkedSubdir(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")

	real := filepath.Join(src, "real-assets")
	require.NoError(t, os.MkdirAll(real, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(real, "texture.dds"), []byte("dds-bytes"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "mod.info"), []byte("info"), 0644))

	// "assets" vendors shared content via a symlink instead of a real copy,
	// which is the layout that triggered EISDIR before this fix.
	require.NoError(t, os.Symlink(real, filepath.Join(src, "assets")))

	err := copyDir(src, dst)
	require.NoError(t, err, "copying a nested dir-symlink must not fail with EISDIR")

	got, err := os.ReadFile(filepath.Join(dst, "assets", "texture.dds"))
	require.NoError(t, err)
	assert.Equal(t, "dds-bytes", string(got))

	info, err := os.Lstat(filepath.Join(dst, "assets"))
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "the symlink must be materialized as a real directory in the copy, not left as a link")

	got, err = os.ReadFile(filepath.Join(dst, "mod.info"))
	require.NoError(t, err)
	assert.Equal(t, "info", string(got))
}

// TestCopyDir_DetectsSymlinkCycle guards the symlink-following fix above
// against infinite recursion when a mod's directory symlink points back at
// one of its own ancestors.
func TestCopyDir_DetectsSymlinkCycle(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.Symlink(src, filepath.Join(src, "loop")))

	err := copyDir(src, filepath.Join(t.TempDir(), "dst"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}
