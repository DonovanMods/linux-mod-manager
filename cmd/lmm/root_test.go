package main

import (
	"context"
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

// TestInitService_UsesFlagsNotEnvironment pins the CLI→app seam: the global
// --config/--data flag values are what app.Open receives, so XDG variables in
// the environment must not redirect a run that set the flags.
func TestInitService_UsesFlagsNotEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "should-not-be-used"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "should-not-be-used"))
	configDir = t.TempDir()
	dataDir = filepath.Join(t.TempDir(), "lmm")

	svc, err := initService()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	assert.Equal(t, configDir, svc.ConfigDir())
	_, err = os.Stat(filepath.Join(dataDir, "lmm.db"))
	assert.NoError(t, err, "the database must live under the --data directory")
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
		// ExecuteContext caches its ctx on the singleton (cobra only
		// defaults ctx when nil), so without this reset the cancelled
		// context above poisons every later bare Execute() call in the
		// test binary - surfaced as shuffle-order failures in tests
		// that drive context-sensitive paths.
		rootCmd.SetContext(context.Background())
	})
	rootCmd.SetArgs([]string{"internal-test-wait"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runRoot(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}
