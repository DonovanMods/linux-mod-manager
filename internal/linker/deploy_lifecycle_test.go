package linker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/linker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strategies enumerates the three deploy strategies under a common Linker
// interface, for table-driven lifecycle tests.
func strategies() map[string]linker.Linker {
	return map[string]linker.Linker{
		"symlink":  linker.NewSymlink(),
		"hardlink": linker.NewHardlink(),
		"copy":     linker.NewCopy(),
	}
}

func TestLinker_DeployUndeployLifecycle(t *testing.T) {
	for name, l := range strategies() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			srcFile := filepath.Join(dir, "src.txt")
			dstFile := filepath.Join(dir, "dst", "nested", "dst.txt")
			require.NoError(t, os.WriteFile(srcFile, []byte("content"), 0644))

			// Never deployed yet.
			deployed, err := l.IsDeployed(dstFile)
			require.NoError(t, err)
			assert.False(t, deployed, "should not be deployed before Deploy")

			require.NoError(t, l.Deploy(srcFile, dstFile))

			deployed, err = l.IsDeployed(dstFile)
			require.NoError(t, err)
			assert.True(t, deployed, "should be deployed after Deploy")

			require.NoError(t, l.Undeploy(dstFile))

			deployed, err = l.IsDeployed(dstFile)
			require.NoError(t, err)
			assert.False(t, deployed, "should not be deployed after Undeploy")

			// Target file actually gone from disk.
			_, err = os.Lstat(dstFile)
			assert.True(t, os.IsNotExist(err), "target file should be removed from disk")

			// Source untouched.
			content, err := os.ReadFile(srcFile)
			require.NoError(t, err)
			assert.Equal(t, []byte("content"), content, "source content must be untouched")
		})
	}
}

func TestLinker_IsDeployed_NeverDeployed(t *testing.T) {
	for name, l := range strategies() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			dstFile := filepath.Join(dir, "never-deployed.txt")

			deployed, err := l.IsDeployed(dstFile)
			require.NoError(t, err)
			assert.False(t, deployed)
		})
	}
}

// TestLinker_Undeploy_NeverDeployed pins the actual behavior: all three
// strategies treat Undeploy of a target that was never deployed as a no-op
// success (they swallow the underlying os.IsNotExist / os.Lstat-not-exist
// error rather than surfacing it).
func TestLinker_Undeploy_NeverDeployed(t *testing.T) {
	for name, l := range strategies() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			dstFile := filepath.Join(dir, "never-deployed.txt")

			err := l.Undeploy(dstFile)
			assert.NoError(t, err, "Undeploy on a never-deployed target should be a no-op, not an error")
		})
	}
}

// TestSymlinkLinker_Undeploy_RefusesNonSymlink pins symlink-specific
// behavior: Undeploy refuses to remove a regular file sitting at dst that
// isn't actually a symlink, to avoid accidentally deleting real game files.
func TestSymlinkLinker_Undeploy_RefusesNonSymlink(t *testing.T) {
	dir := t.TempDir()
	dstFile := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(dstFile, []byte("not a symlink"), 0644))

	l := linker.NewSymlink()
	err := l.Undeploy(dstFile)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a symlink")

	// The file must be left alone since it was refused.
	content, err := os.ReadFile(dstFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("not a symlink"), content)
}

func TestCopyLinker_Undeploy_RemovesCopyLeavesSourceIntact(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "src.txt")
	dstFile := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("original"), 0644))

	l := linker.NewCopy()
	require.NoError(t, l.Deploy(srcFile, dstFile))

	// Mutate the deployed copy to prove it's an independent file, not a link.
	require.NoError(t, os.WriteFile(dstFile, []byte("mutated"), 0644))
	srcContent, err := os.ReadFile(srcFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), srcContent, "copy is independent of source")

	require.NoError(t, l.Undeploy(dstFile))

	_, err = os.Stat(dstFile)
	assert.True(t, os.IsNotExist(err), "copy should be removed from target")

	srcContent, err = os.ReadFile(srcFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), srcContent, "source untouched by undeploy")
}

func TestHardlinkLinker_DeployUndeploy_SharesInodeThenSourceIntact(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "src.txt")
	dstFile := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("shared"), 0644))

	l := linker.NewHardlink()
	require.NoError(t, l.Deploy(srcFile, dstFile))

	srcInfo, err := os.Stat(srcFile)
	require.NoError(t, err)
	dstInfo, err := os.Stat(dstFile)
	require.NoError(t, err)
	assert.True(t, os.SameFile(srcInfo, dstInfo), "deployed file must be the same inode as source (true hardlink)")

	require.NoError(t, l.Undeploy(dstFile))

	_, err = os.Stat(dstFile)
	assert.True(t, os.IsNotExist(err), "hardlink should be removed from target")

	// Source data survives because the inode had another link (the source
	// path itself); removing the dst directory entry doesn't touch it.
	content, err := os.ReadFile(srcFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("shared"), content, "source data intact after undeploy")
}

// TestHardlinkLinker_IsDeployed_CannotDistinguishFromRegularFile and
// TestCopyLinker_IsDeployed_CannotDistinguishFromRegularFile pin documented,
// surprising behavior: IsDeployed for both strategies only checks that a
// filesystem entry exists at dst (see hardlink.go/copy.go IsDeployed doc
// comments) — it does NOT verify the file is actually linked/copied from the
// expected source. Any pre-existing file at dst reads as "deployed".
func TestHardlinkLinker_IsDeployed_CannotDistinguishFromRegularFile(t *testing.T) {
	dir := t.TempDir()
	dstFile := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(dstFile, []byte("unrelated file"), 0644))

	l := linker.NewHardlink()
	deployed, err := l.IsDeployed(dstFile)
	require.NoError(t, err)
	assert.True(t, deployed, "IsDeployed is existence-only; it cannot tell this apart from a real hardlink")
}

func TestCopyLinker_IsDeployed_CannotDistinguishFromRegularFile(t *testing.T) {
	dir := t.TempDir()
	dstFile := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(dstFile, []byte("unrelated file"), 0644))

	l := linker.NewCopy()
	deployed, err := l.IsDeployed(dstFile)
	require.NoError(t, err)
	assert.True(t, deployed, "IsDeployed is existence-only; it cannot tell this apart from a real copy")
}

func TestCleanupEmptyDirs_RemovesNestedEmptyDirs(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b", "c")
	require.NoError(t, os.MkdirAll(nested, 0755))

	linker.CleanupEmptyDirs(base)

	_, err := os.Stat(filepath.Join(base, "a"))
	assert.True(t, os.IsNotExist(err), "top-level empty subdir should be removed")

	// basePath itself must survive.
	info, err := os.Stat(base)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestCleanupEmptyDirs_PreservesNonEmptyDirs(t *testing.T) {
	base := t.TempDir()
	keepDir := filepath.Join(base, "keep")
	emptyDir := filepath.Join(base, "empty")
	require.NoError(t, os.MkdirAll(keepDir, 0755))
	require.NoError(t, os.MkdirAll(emptyDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(keepDir, "file.txt"), []byte("data"), 0644))

	linker.CleanupEmptyDirs(base)

	_, err := os.Stat(emptyDir)
	assert.True(t, os.IsNotExist(err), "empty sibling dir should be removed")

	info, err := os.Stat(keepDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "non-empty dir should be preserved")

	_, err = os.Stat(filepath.Join(keepDir, "file.txt"))
	assert.NoError(t, err, "file inside non-empty dir should be preserved")
}

func TestCleanupEmptyDirs_MissingRootDoesNotPanic(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "does-not-exist")

	assert.NotPanics(t, func() {
		linker.CleanupEmptyDirs(missing)
	})
}

// TestLinker_Deploy_OverwritesExistingForeignFile pins current, deliberate
// remove-then-place (or truncate-in-place) semantics: Deploy does not check
// whether something unrelated already sits at dst before replacing it.
//
//   - symlink.go: Deploy explicitly os.Remove(dst)s any existing entry, then
//     os.Symlink(src, dst).
//   - hardlink.go: Deploy explicitly os.Remove(dst)s any existing entry, then
//     os.Link(src, dst).
//   - copy.go: Deploy has no explicit removal step; it opens dst with
//     os.O_CREATE|os.O_WRONLY|os.O_TRUNC and overwrites its content in place.
//
// In all three cases whatever previously occupied dst — including a foreign,
// unrelated file — is silently discarded. This pairs with the existence-only
// IsDeployed behavior pinned above: the caller has no way to detect, via this
// package alone, that dst held something else before Deploy ran.
func TestLinker_Deploy_OverwritesExistingForeignFile(t *testing.T) {
	tests := []struct {
		name      string
		newLinker func() linker.Linker
		verify    func(t *testing.T, srcFile, dstFile string)
	}{
		{
			name:      "symlink",
			newLinker: func() linker.Linker { return linker.NewSymlink() },
			verify: func(t *testing.T, srcFile, dstFile string) {
				t.Helper()
				info, err := os.Lstat(dstFile)
				require.NoError(t, err)
				assert.True(t, info.Mode()&os.ModeSymlink != 0, "dst should now be a symlink")

				target, err := os.Readlink(dstFile)
				require.NoError(t, err)
				assert.Equal(t, srcFile, target, "symlink should point at source")
			},
		},
		{
			name:      "hardlink",
			newLinker: func() linker.Linker { return linker.NewHardlink() },
			verify: func(t *testing.T, srcFile, dstFile string) {
				t.Helper()
				srcInfo, err := os.Stat(srcFile)
				require.NoError(t, err)
				dstInfo, err := os.Stat(dstFile)
				require.NoError(t, err)
				assert.True(t, os.SameFile(srcInfo, dstInfo), "dst should now share source's inode")
			},
		},
		{
			name:      "copy",
			newLinker: func() linker.Linker { return linker.NewCopy() },
			verify: func(t *testing.T, srcFile, dstFile string) {
				t.Helper()
				content, err := os.ReadFile(dstFile)
				require.NoError(t, err)
				assert.Equal(t, []byte("source content"), content, "dst should now hold source's content")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			srcFile := filepath.Join(dir, "src.txt")
			dstFile := filepath.Join(dir, "dst.txt")
			require.NoError(t, os.WriteFile(srcFile, []byte("source content"), 0644))
			require.NoError(t, os.WriteFile(dstFile, []byte("foreign content"), 0644))

			l := tt.newLinker()
			require.NoError(t, l.Deploy(srcFile, dstFile))

			content, err := os.ReadFile(dstFile)
			require.NoError(t, err)
			assert.NotEqual(t, []byte("foreign content"), content, "foreign content must be gone after Deploy")

			tt.verify(t, srcFile, dstFile)
		})
	}
}
