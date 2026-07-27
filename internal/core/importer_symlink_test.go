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

// TestCopyDir_RefusesNestedDirSymlinkEscapingRoot is a path-traversal
// regression test (Copilot PR #110 round 4, importer.go:244): an extracted
// archive is untrusted internet content, and copyDirFollowing's symlink
// support let a nested directory symlink resolve anywhere on disk - e.g. to
// /etc or $HOME - pulling arbitrary external content into cache/staging.
// Nested symlinks must resolve to a path within the copy root; escaping
// symlinks must fail the copy with a clear error, not follow silently.
func TestCopyDir_RefusesNestedDirSymlinkEscapingRoot(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")

	outside := filepath.Join(t.TempDir(), "outside-secrets")
	require.NoError(t, os.MkdirAll(outside, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("do-not-copy"), 0644))

	require.NoError(t, os.Symlink(outside, filepath.Join(src, "escape-link")))
	require.NoError(t, os.WriteFile(filepath.Join(src, "mod.info"), []byte("info"), 0644))

	err := copyDir(src, dst)
	require.Error(t, err, "a nested dir symlink escaping the copy root must fail the copy, not silently follow")
	assert.Contains(t, err.Error(), "escape-link", "error should name the offending entry")

	_, statErr := os.Stat(filepath.Join(dst, "escape-link"))
	assert.True(t, os.IsNotExist(statErr), "nothing from outside the root should land in dst")
	_, statErr = os.Stat(filepath.Join(dst, "secret.txt"))
	assert.True(t, os.IsNotExist(statErr), "the outside file's content must not be copied in")
}

// TestCopyDir_RefusesNestedFileSymlinkEscapingRoot mirrors the dir case for a
// symlinked *file*: copyFileStreaming's os.Open follows symlinks too, so a
// nested file symlink pointing outside the root (e.g. standing in for
// /etc/passwd) is exactly as dangerous as a directory symlink and must be
// refused the same way.
func TestCopyDir_RefusesNestedFileSymlinkEscapingRoot(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")

	outsideFile := filepath.Join(t.TempDir(), "passwd-stand-in.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("root:x:0:0"), 0644))

	require.NoError(t, os.Symlink(outsideFile, filepath.Join(src, "sneaky.conf")))

	err := copyDir(src, dst)
	require.Error(t, err, "a nested file symlink escaping the copy root must fail the copy")
	assert.Contains(t, err.Error(), "sneaky.conf", "error should name the offending entry")

	_, statErr := os.Stat(filepath.Join(dst, "sneaky.conf"))
	assert.True(t, os.IsNotExist(statErr), "the outside file must not be copied into dst")
}

// TestCopyDir_TopLevelSymlinkedRootStillCopies pins the exemption: the copy
// root itself may be (or resolve through) a symlink - that's the #52
// top-level symlinked mod dir feature - and must keep working even though
// nested symlinks are now containment-checked against it.
func TestCopyDir_TopLevelSymlinkedRootStillCopies(t *testing.T) {
	real := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(real, "mod.info"), []byte("info"), 0644))

	linkedRoot := filepath.Join(t.TempDir(), "linked-root")
	require.NoError(t, os.Symlink(real, linkedRoot))

	dst := filepath.Join(t.TempDir(), "dst")
	err := copyDir(linkedRoot, dst)
	require.NoError(t, err, "the copy root itself being a symlink must still work")

	got, err := os.ReadFile(filepath.Join(dst, "mod.info"))
	require.NoError(t, err)
	assert.Equal(t, "info", string(got))
}
