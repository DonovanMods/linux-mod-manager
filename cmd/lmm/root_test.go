package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
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

// writeSourceYAML writes a source definition file for registerCustomSources tests.
func writeSourceYAML(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
}

// TestRegisterCustomSources_SkipsInvalidDefinitions exercises all three
// warn-and-skip branches in registerCustomSources (#52 item 7): a per-file
// load error, an id already claimed by a previously-registered source, and a
// definition that parses fine but fails construction. Warnings go straight to
// os.Stderr (not redirectable in a package-level test), so this asserts the
// registration outcome via svc.ListSources() instead of captured output.
func TestRegisterCustomSources_SkipsInvalidDefinitions(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	// Pre-register a source under id "taken" so a definition claiming the
	// same id hits the id-collision skip branch.
	taken, err := custom.NewDirectory(custom.SourceDefinition{
		ID:        "taken",
		Name:      "Pre-registered",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	svc.RegisterSource(taken)

	cfgDir := t.TempDir()
	srcDir := filepath.Join(cfgDir, "sources")

	writeSourceYAML(t, srcDir, "good.yaml", fmt.Sprintf(`
id: good-src
name: Good Src
type: directory
directory:
  path: %s
`, t.TempDir()))
	writeSourceYAML(t, srcDir, "broken.yaml", "id: [unclosed") // load error branch
	writeSourceYAML(t, srcDir, "collide.yaml", fmt.Sprintf(`
id: taken
name: Collide Src
type: directory
directory:
  path: %s
`, t.TempDir())) // id-collision branch
	writeSourceYAML(t, srcDir, "missing-path.yaml", `
id: missing-path
name: Missing Path
type: directory
directory:
  path: /this/path/should/not/exist/lmm-test-fixture
`) // construction-failure branch: Validate passes, NewDirectory's os.Stat fails

	registerCustomSources(svc, cfgDir)

	sources := svc.ListSources()
	byID := make(map[string]source.ModSource, len(sources))
	for _, s := range sources {
		byID[s.ID()] = s
	}

	goodSrc, ok := byID["good-src"]
	require.True(t, ok, "good.yaml should have registered its source")
	assert.Equal(t, "Good Src", goodSrc.Name())

	takenSrc, ok := byID["taken"]
	require.True(t, ok, "the pre-registered id must still be present")
	assert.Equal(t, "Pre-registered", takenSrc.Name(), "collide.yaml must not overwrite the pre-existing source")

	_, ok = byID["missing-path"]
	assert.False(t, ok, "a definition whose directory doesn't exist must fail construction and be skipped")

	assert.Len(t, sources, 2, "only good-src and the pre-registered taken source should be registered")
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
