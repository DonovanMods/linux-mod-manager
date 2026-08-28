package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openForTest(t *testing.T, opts Options) {
	t.Helper()
	svc, err := Open(t.Context(), opts)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	// Built-ins are registered unconditionally.
	src, err := svc.GetSource("nexusmods")
	require.NoError(t, err)
	assert.Equal(t, "Nexus Mods", src.Name())
}

// TestOpen_DataDirIsOwnerOnly pins that the data directory is created 0700:
// it holds lmm.db (auth tokens in plaintext) and the downloads staging root.
func TestOpen_DataDirIsOwnerOnly(t *testing.T) {
	// Nest under the temp dir so Open does the creating — t.TempDir() itself
	// is already 0700, which would make the assertion vacuous.
	dataDir := filepath.Join(t.TempDir(), "lmm")
	openForTest(t, Options{ConfigDir: t.TempDir(), DataDir: dataDir})

	info, err := os.Stat(dataDir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0700), info.Mode().Perm(), "data dir must not be group- or world-readable")
}

// TestOpen_TightensExistingDataDir covers installs predating the 0700 rule:
// MkdirAll is a no-op on an existing directory, so a legacy 0755 data dir
// stays permissive unless explicitly tightened.
func TestOpen_TightensExistingDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "lmm")
	require.NoError(t, os.MkdirAll(dataDir, 0755))
	openForTest(t, Options{ConfigDir: t.TempDir(), DataDir: dataDir})

	info, err := os.Stat(dataDir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0700), info.Mode().Perm(), "an existing permissive data dir should be tightened")
}

// TestOpen_CreatesConfigAndCacheDirs pins that a fresh install gets its whole
// layout created, cache included.
func TestOpen_CreatesConfigAndCacheDirs(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "cfg", "lmm")
	dataDir := filepath.Join(root, "data", "lmm")
	openForTest(t, Options{ConfigDir: cfgDir, DataDir: dataDir})

	for _, dir := range []string{cfgDir, dataDir, filepath.Join(dataDir, "cache")} {
		info, err := os.Stat(dir)
		require.NoError(t, err, dir)
		assert.True(t, info.IsDir(), dir)
	}
}

// TestOpen_AlreadyCancelledContext pins that a cancelled ctx aborts Open
// before any directory creation or service work, returning ctx.Err() rather
// than opening successfully or failing later with an unrelated error.
func TestOpen_AlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Open(ctx, Options{ConfigDir: t.TempDir(), DataDir: t.TempDir()})
	require.ErrorIs(t, err, context.Canceled)
}
