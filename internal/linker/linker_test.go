package linker_test

import (
	"os"
	"path/filepath"
	"testing"

	"lmm/internal/domain"
	"lmm/internal/linker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSymlinkLinker_Deploy(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	dstDir := filepath.Join(dir, "dst")
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.MkdirAll(dstDir, 0755))

	srcFile := filepath.Join(srcDir, "test.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("content"), 0644))

	l := linker.NewSymlink()
	dstFile := filepath.Join(dstDir, "test.txt")
	err := l.Deploy(srcFile, dstFile)
	require.NoError(t, err)

	// Verify it's a symlink
	info, err := os.Lstat(dstFile)
	require.NoError(t, err)
	assert.True(t, info.Mode()&os.ModeSymlink != 0)

	// Verify content accessible
	content, err := os.ReadFile(dstFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("content"), content)
}

func TestSymlinkLinker_Undeploy(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "src.txt")
	dstFile := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("content"), 0644))

	l := linker.NewSymlink()
	require.NoError(t, l.Deploy(srcFile, dstFile))
	require.NoError(t, l.Undeploy(dstFile))

	_, err := os.Stat(dstFile)
	assert.True(t, os.IsNotExist(err))

	// Source should still exist
	_, err = os.Stat(srcFile)
	assert.NoError(t, err)
}

func TestHardlinkLinker_Deploy(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "src.txt")
	dstFile := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("content"), 0644))

	l := linker.NewHardlink()
	err := l.Deploy(srcFile, dstFile)
	require.NoError(t, err)

	// Verify content
	content, err := os.ReadFile(dstFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("content"), content)

	// Verify same inode (hardlink)
	srcInfo, _ := os.Stat(srcFile)
	dstInfo, _ := os.Stat(dstFile)
	assert.Equal(t, srcInfo.Size(), dstInfo.Size())
}

func TestCopyLinker_Deploy(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "src.txt")
	dstFile := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("content"), 0644))

	l := linker.NewCopy()
	err := l.Deploy(srcFile, dstFile)
	require.NoError(t, err)

	// Verify content
	content, err := os.ReadFile(dstFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("content"), content)
}

func TestNew_ReturnsCorrectLinker(t *testing.T) {
	assert.Equal(t, domain.LinkSymlink, linker.New(domain.LinkSymlink).Method())
	assert.Equal(t, domain.LinkHardlink, linker.New(domain.LinkHardlink).Method())
	assert.Equal(t, domain.LinkCopy, linker.New(domain.LinkCopy).Method())
}
