package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewStagingDir_CreatesUnderRoot pins that scratch space for downloads and
// archive extraction lives under the data dir rather than $TMPDIR. /tmp is tmpfs
// on most modern distros, so staging a multi-GB mod archive there spends RAM.
func TestNewStagingDir_CreatesUnderRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "downloads")

	dir, err := newStagingDir(root, "lmm-download-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	assert.Equal(t, root, filepath.Dir(dir), "staging dir should be created directly under the root")
	assert.DirExists(t, dir)
	assert.True(t, strings.HasPrefix(filepath.Base(dir), "lmm-download-"), "pattern should be honored, got %q", filepath.Base(dir))
}

// TestNewStagingDir_CreatesMissingRoot: the downloads directory is created on
// demand, so core works as a library without the CLI having pre-made it.
func TestNewStagingDir_CreatesMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "downloads")

	dir, err := newStagingDir(root, "lmm-import-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	assert.DirExists(t, root)
	assert.Equal(t, root, filepath.Dir(dir))
}

// TestNewStagingDir_FallsBackToTempDir keeps callers without a configured data
// dir working, rather than failing or writing to the process's working directory.
func TestNewStagingDir_FallsBackToTempDir(t *testing.T) {
	dir, err := newStagingDir("", "lmm-download-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	assert.Equal(t, os.TempDir(), filepath.Dir(dir))
}

// TestService_StagingRoot pins the location against the PRD's documented layout.
func TestService_StagingRoot(t *testing.T) {
	dataDir := t.TempDir()
	svc := &Service{dataDir: dataDir}

	assert.Equal(t, filepath.Join(dataDir, "downloads"), svc.stagingRoot())
}

// TestService_StagingRoot_EmptyDataDir yields "", which newStagingDir reads as
// "fall back to $TMPDIR" — never the relative path "downloads".
func TestService_StagingRoot_EmptyDataDir(t *testing.T) {
	svc := &Service{}

	assert.Empty(t, svc.stagingRoot())
}
