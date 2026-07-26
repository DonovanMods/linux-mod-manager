package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitService_RegistersSources(t *testing.T) {
	// Use temp directories to avoid polluting real config
	configDir = t.TempDir()
	dataDir = t.TempDir()

	svc, err := initService()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	// NexusMods should be registered by default
	src, err := svc.GetSource("nexusmods")
	require.NoError(t, err, "nexusmods source should be registered by default")
	assert.Equal(t, "nexusmods", src.ID())
	assert.Equal(t, "Nexus Mods", src.Name())

	// CurseForge should be registered by default
	src, err = svc.GetSource("curseforge")
	require.NoError(t, err, "curseforge source should be registered by default")
	assert.Equal(t, "curseforge", src.ID())
	assert.Equal(t, "CurseForge", src.Name())
}

// TestInitService_DataDirIsOwnerOnly pins that the data directory is created 0700.
// It contains lmm.db, which holds auth tokens in plaintext (#79); an owner-only
// directory also closes the window between SQLite creating the DB at 0644 and the
// db package chmod'ing it.
func TestInitService_DataDirIsOwnerOnly(t *testing.T) {
	configDir = t.TempDir()
	// Nest under the temp dir so initService does the creating — t.TempDir() itself
	// is already 0700, which would make the assertion vacuous.
	dataDir = filepath.Join(t.TempDir(), "lmm")

	svc, err := initService()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	info, err := os.Stat(dataDir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0700), info.Mode().Perm(), "data dir must not be group- or world-readable")
}

// TestInitService_TightensExistingDataDir covers installs predating the 0700
// change: MkdirAll is a no-op on an existing directory, so a legacy 0755 data
// dir stays permissive unless it is explicitly tightened. It now holds the
// downloads staging root as well as the DB.
func TestInitService_TightensExistingDataDir(t *testing.T) {
	configDir = t.TempDir()
	dataDir = filepath.Join(t.TempDir(), "lmm")
	require.NoError(t, os.MkdirAll(dataDir, 0755))

	svc, err := initService()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	info, err := os.Stat(dataDir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0700), info.Mode().Perm(), "an existing permissive data dir should be tightened")
}

// TestRunRoot_PropagatesContextCancellation pins the contract that the root command
// runs under the caller's context, so SIGINT and explicit cancellation reach RunE
// handlers via cmd.Context(). Regression guard against reverting to rootCmd.Execute().
func TestRunRoot_PropagatesContextCancellation(t *testing.T) {
	waitCmd := &cobra.Command{
		Use:    "internal-test-wait",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			<-cmd.Context().Done()
			return cmd.Context().Err()
		},
	}
	rootCmd.AddCommand(waitCmd)
	t.Cleanup(func() {
		rootCmd.RemoveCommand(waitCmd)
		rootCmd.SetArgs(nil)
	})
	rootCmd.SetArgs([]string{"internal-test-wait"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runRoot(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}
