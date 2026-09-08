package core_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewStagingDir pins the exported staging seam `lmm serve`'s upload
// endpoint writes through: the directory lands under the DATA dir's
// downloads/, not $TMPDIR, and it is private.
func TestNewStagingDir(t *testing.T) {
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	dir, err := svc.NewStagingDir("upload-*")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dataDir, "downloads"), filepath.Dir(dir),
		"staging goes under the data dir, never a new tmp root")
	assert.Contains(t, filepath.Base(dir), "upload-")

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}
