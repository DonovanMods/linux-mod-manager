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
		[]byte(`{"file_name":"../elsewhere.zip","size":5,"version":"1.0.0"}`), 0o600))

	path, _, ok := svc.reuseRetainedDownload("src", "mod", "file", retainedIdentity{Version: "1.0.0"})
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
		[]byte(`{"file_name":"Thing-1.0.0.zip","size":5,"sha256":"abc","version":"1.0.0"}`), 0o600))

	path, result, ok := svc.reuseRetainedDownload("src", "mod", "file", retainedIdentity{Version: "1.0.0"})
	require.True(t, ok)
	assert.Equal(t, filepath.Join(dir, "Thing-1.0.0.zip"), path)
	require.NotNil(t, result)
	assert.Equal(t, int64(5), result.Size)
	assert.Equal(t, "abc", result.SHA256)
}

// TestReuseRetainedDownload_RequiresTheSameVersion is #602 review S2: the
// entry is keyed by (source, mod, file id), and a file id can outlive a
// release. An archive kept for 1.0.0 must not be installed as 2.0.0 - nor
// one whose source-declared size or checksum has since changed, nor one a
// binary from before the field wrote (no version at all). Each is discarded,
// so the next acquire downloads.
func TestReuseRetainedDownload_RequiresTheSameVersion(t *testing.T) {
	for name, tc := range map[string]struct {
		sidecar string
		want    retainedIdentity
		reused  bool
	}{
		"same version":        {`"version":"1.0.0"`, retainedIdentity{Version: "1.0.0"}, true},
		"newer version":       {`"version":"1.0.0"`, retainedIdentity{Version: "2.0.0"}, false},
		"no recorded version": {``, retainedIdentity{Version: "1.0.0"}, false},
		"declared size moved": {`"version":"1.0.0","declared_size":5`, retainedIdentity{Version: "1.0.0", DeclaredSize: 6}, false},
		"declared sha moved":  {`"version":"1.0.0","declared_sha256":"aa"`, retainedIdentity{Version: "1.0.0", DeclaredSHA256: "bb"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &Service{dataDir: t.TempDir()}
			dir := svc.retainedDownloadDir("src", "mod", "file")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "a.zip"), []byte("bytes"), 0o600))
			sidecar := `{"file_name":"a.zip","size":5`
			if tc.sidecar != "" {
				sidecar += "," + tc.sidecar
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, retainedDownloadFile), []byte(sidecar+"}"), 0o600))

			_, _, ok := svc.reuseRetainedDownload("src", "mod", "file", tc.want)
			assert.Equal(t, tc.reused, ok)
			_, statErr := os.Stat(dir)
			assert.Equal(t, tc.reused, statErr == nil, "a stale entry is removed, a matching one kept")
		})
	}
}

// TestRetainedDownload_AnUnidentifiedFileIsNeitherKeptNorReused is #628's
// third note: a file whose mod has no version and whose source declares no
// size or checksum has nothing a kept archive can be checked against - an
// archive kept for an earlier upstream release under the same file id would
// match it exactly. Such a file is not kept, and an entry already kept for
// that empty identity (a sidecar from before the fields existed, or one an
// earlier lmm wrote) is discarded rather than reused.
func TestRetainedDownload_AnUnidentifiedFileIsNeitherKeptNorReused(t *testing.T) {
	t.Run("not reused", func(t *testing.T) {
		svc := &Service{dataDir: t.TempDir()}
		dir := svc.retainedDownloadDir("src", "mod", "file")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.zip"), []byte("bytes"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, retainedDownloadFile), []byte(`{"file_name":"a.zip","size":5,"version":""}`), 0o600))

		_, _, ok := svc.reuseRetainedDownload("src", "mod", "file", retainedIdentity{})
		assert.False(t, ok)
		_, statErr := os.Stat(dir)
		assert.True(t, os.IsNotExist(statErr), "the unverifiable entry is removed")
	})
	t.Run("not kept", func(t *testing.T) {
		svc := &Service{dataDir: t.TempDir()}
		archive := filepath.Join(t.TempDir(), "a.zip")
		require.NoError(t, os.WriteFile(archive, []byte("bytes"), 0o600))
		svc.retainRefusedDownload(errAcquireBlocked, "src", "mod", "file", archive, &DownloadResult{Path: archive, Size: 5}, retainedIdentity{})
		_, statErr := os.Stat(svc.retainedDownloadDir("src", "mod", "file"))
		assert.True(t, os.IsNotExist(statErr), "nothing is kept that no re-run could verify")
	})
	t.Run("any one field identifies", func(t *testing.T) {
		for name, id := range map[string]retainedIdentity{
			"version":       {Version: "1.0.0"},
			"declared size": {DeclaredSize: 5},
			"declared sha":  {DeclaredSHA256: "aa"},
		} {
			t.Run(name, func(t *testing.T) {
				svc := &Service{dataDir: t.TempDir()}
				archive := filepath.Join(t.TempDir(), "a.zip")
				require.NoError(t, os.WriteFile(archive, []byte("bytes"), 0o600))
				svc.retainRefusedDownload(errAcquireBlocked, "src", "mod", "file", archive, &DownloadResult{Path: archive, Size: 5}, id)
				_, _, ok := svc.reuseRetainedDownload("src", "mod", "file", id)
				assert.True(t, ok)
			})
		}
	})
}
