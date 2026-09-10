package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReuseRetainedDownload_TreatsTheSidecarFileNameAsABareName is
// re-review N4. lmm writes the sidecar itself, 0600, inside a 0700
// directory, so a traversing file_name is not reachable today - but the
// reuse joined it to the retention directory unvalidated, which made that a
// property of who can write the file rather than of the code. A bare name is
// the only thing that field ever means.
func TestReuseRetainedDownload_TreatsTheSidecarFileNameAsABareName(t *testing.T) {
	svc := &Service{dataDir: t.TempDir()}
	dir := svc.retainedDownloadDir("src", "mod", "file")
	require.NotEmpty(t, dir)
	require.NoError(t, os.MkdirAll(dir, 0o700))

	// A real archive one level up - what a traversing name would reach.
	outside := filepath.Join(filepath.Dir(dir), "elsewhere.zip")
	require.NoError(t, os.WriteFile(outside, []byte("bytes"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, retainedDownloadFile),
		[]byte(`{"file_name":"../elsewhere.zip","size":5}`), 0o600))

	path, _, ok := svc.reuseRetainedDownload("src", "mod", "file")
	assert.False(t, ok, "a sidecar naming a path outside its own directory reuses nothing, got %q", path)
}

// And the ordinary case is untouched: a bare name beside its sidecar is
// exactly what a retention writes, and is reused.
func TestReuseRetainedDownload_ReusesTheArchiveBesideItsSidecar(t *testing.T) {
	svc := &Service{dataDir: t.TempDir()}
	dir := svc.retainedDownloadDir("src", "mod", "file")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Thing-1.0.0.zip"), []byte("bytes"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, retainedDownloadFile),
		[]byte(`{"file_name":"Thing-1.0.0.zip","size":5,"sha256":"abc"}`), 0o600))

	path, result, ok := svc.reuseRetainedDownload("src", "mod", "file")
	require.True(t, ok)
	assert.Equal(t, filepath.Join(dir, "Thing-1.0.0.zip"), path)
	require.NotNil(t, result)
	assert.Equal(t, int64(5), result.Size)
	assert.Equal(t, "abc", result.SHA256)
}
