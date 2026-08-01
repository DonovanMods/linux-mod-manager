package main

import (
	"bytes"
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

// authChecker matches the IsAuthenticated() method every source that accepts
// SetAPIKey also exposes (nexusmods.NexusMods, curseforge.CurseForge, and
// the custom types) — used here to prove a resolved key actually reached the
// constructed source via the registration pipeline's SetAPIKey seam.
type authChecker interface{ IsAuthenticated() bool }

// TestRegisterSources_BuiltinStillAuthenticatesWithEnvAndToken pins that a
// built-in, now constructed keyless (nexusmods.New(nil, "")) and
// key-resolved post-construction by the unified pipeline, still ends up
// authenticated when both an env var and a stored DB token are present —
// the key pipeline must still be reached, not silently dropped now that
// resolution happens after construction instead of before.
//
// NOTE: IsAuthenticated() only reports "some key was applied," not WHICH
// one — this test alone would still pass even if getSourceAPIKey's
// env-over-token precedence were inverted (verified live: with the
// precedence swapped, this test kept passing while
// TestRegisterSource_KeyResolutionPrecedence below correctly failed — see
// that test's doc comment). It stays as the integration-level pin that the
// real built-in pipeline still authenticates at all; precedence itself is
// now pinned by TestRegisterSource_KeyResolutionPrecedence.
func TestRegisterSources_BuiltinStillAuthenticatesWithEnvAndToken(t *testing.T) {
	t.Setenv("NEXUSMODS_API_KEY", "env-key-value")

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	require.NoError(t, svc.SaveSourceToken("nexusmods", "stored-db-key"))

	registerSources(svc, t.TempDir(), t.TempDir())

	src, err := svc.GetSource("nexusmods")
	require.NoError(t, err)
	auth, ok := src.(authChecker)
	require.True(t, ok, "nexusmods must expose IsAuthenticated")
	assert.True(t, auth.IsAuthenticated(), "env var must be applied via SetAPIKey even though a DB token also exists")
}

// recordingKeySource is a mockAuthSource that also implements
// source.EnvKeyProvider with a caller-chosen env var name, so
// TestRegisterSource_KeyResolutionPrecedence can control exactly which env
// var getSourceAPIKey consults instead of relying on the derived
// LMM_<ID>_API_KEY convention. mockAuthSource.apiKey (set via SetAPIKey)
// records whichever key registerSource actually applied.
type recordingKeySource struct {
	mockAuthSource
	envKey string
}

func (r *recordingKeySource) EnvKey() string { return r.envKey }

// TestRegisterSource_KeyResolutionPrecedence pins registerSource's
// env-over-stored-token precedence directly at the unit that implements it,
// with distinct, observable env and token values recorded via SetAPIKey.
// registerSources (the full pipeline) only exercises this through the real
// built-in/custom sources, none of which expose the applied key for
// assertion — routing through registerSource with a recording mock is the
// least invasive seam that can actually distinguish "env applied" from
// "token applied" rather than just "something was applied"
// (see TestRegisterSources_BuiltinStillAuthenticatesWithEnvAndToken's note).
func TestRegisterSource_KeyResolutionPrecedence(t *testing.T) {
	const envVar = "LMM_PRECEDENCE_TEST_API_KEY"

	newService := func(t *testing.T) *core.Service {
		t.Helper()
		svc, err := core.NewService(core.ServiceConfig{
			ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, svc.Close()) })
		return svc
	}

	t.Run("env value wins when both env and a stored token are present", func(t *testing.T) {
		svc := newService(t)
		require.NoError(t, svc.SaveSourceToken("precedence-src", "token-value"))
		t.Setenv(envVar, "env-value")

		mock := &recordingKeySource{
			mockAuthSource: mockAuthSource{id: "precedence-src", name: "Precedence Src"},
			envKey:         envVar,
		}
		registerSource(svc, mock, t.TempDir())

		assert.Equal(t, "env-value", mock.apiKey, "env var must take precedence over a stored DB token")
	})

	t.Run("stored token applies when no env value is present", func(t *testing.T) {
		svc := newService(t)
		require.NoError(t, svc.SaveSourceToken("precedence-src", "token-value"))
		t.Setenv(envVar, "")

		mock := &recordingKeySource{
			mockAuthSource: mockAuthSource{id: "precedence-src", name: "Precedence Src"},
			envKey:         envVar,
		}
		registerSource(svc, mock, t.TempDir())

		assert.Equal(t, "token-value", mock.apiKey, "stored token must apply when no env var is set")
	})
}

// recordingDataDirSource is a mockAuthSource that also implements the
// optional SetDataDir(string) setter (icarus.Icarus, #136 Task 13), so
// TestRegisterSource_WiresDataDir can pin that registerSource calls it with
// the resolved data directory - the same optional-setter pattern SetAPIKey
// already uses, just for a different capability.
type recordingDataDirSource struct {
	mockAuthSource
	dataDir string
}

func (r *recordingDataDirSource) SetDataDir(dataDir string) { r.dataDir = dataDir }

// TestRegisterSource_WiresDataDir pins that registerSource calls SetDataDir
// on a source that implements it, passing through the exact dataDir it was
// given - the seam icarus.Icarus.SetDataDir relies on to ever get a working
// dumps store outside of a test that constructs Icarus directly.
func TestRegisterSource_WiresDataDir(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := &recordingDataDirSource{mockAuthSource: mockAuthSource{id: "data-dir-src", name: "Data Dir Src"}}
	wantDataDir := t.TempDir()
	registerSource(svc, mock, wantDataDir)

	assert.Equal(t, wantDataDir, mock.dataDir, "registerSource must pass its dataDir through to SetDataDir")
}

// TestRegisterSources_DerivedEnvKeyForCustom pins that a custom source with
// no EnvKeyProvider still resolves its key via the derived LMM_<ID>_API_KEY
// convention (envKeyFor's fallback to envKeyForSourceID) through the unified
// pipeline's registerCustomSources path.
func TestRegisterSources_DerivedEnvKeyForCustom(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	cfgDir := t.TempDir()
	// The definition declares auth: the key pipeline is gated on
	// Capabilities().Auth (a key set on an auth-less source would be
	// stored but never attached to a request), and this test pins the
	// env-var NAME derivation, which needs the key to actually apply.
	writeSourceYAML(t, filepath.Join(cfgDir, "sources"), "custom.yaml", `
id: my-custom
name: My Custom
type: manifest
manifest:
  url: https://example.invalid/mods.yaml
  auth:
    api_key:
      in: header
      name: X-API-Key
`)
	t.Setenv("LMM_MY_CUSTOM_API_KEY", "custom-env-key")

	registerSources(svc, cfgDir, t.TempDir())

	src, err := svc.GetSource("my-custom")
	require.NoError(t, err)
	auth, ok := src.(authChecker)
	require.True(t, ok, "custom manifest source must expose IsAuthenticated")
	assert.True(t, auth.IsAuthenticated(), "derived LMM_<ID>_API_KEY env var must resolve through envKeyFor's fallback path")
}

// TestRegisterSources_FirstWinsCollision pins that built-ins register first
// in the unified pipeline, so a custom definition claiming id "nexusmods"
// still loses the collision (existing rule) and the warning still fires,
// even though built-ins are now constructed via the same factory+registerSource
// path as customs instead of being special-cased ahead of registerCustomSources.
func TestRegisterSources_FirstWinsCollision(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	cfgDir := t.TempDir()
	writeSourceYAML(t, filepath.Join(cfgDir, "sources"), "nexusmods.yaml", fmt.Sprintf(`
id: nexusmods
name: Shadow NexusMods
type: directory
directory:
  path: %s
`, t.TempDir()))

	var warnBuf bytes.Buffer
	customSourceWarnOut = &warnBuf
	t.Cleanup(func() { customSourceWarnOut = nil })

	registerSources(svc, cfgDir, t.TempDir())

	src, err := svc.GetSource("nexusmods")
	require.NoError(t, err)
	assert.Equal(t, "Nexus Mods", src.Name(), "built-in nexusmods must win; the custom definition should be skipped")
	assert.Contains(t, warnBuf.String(), `warning: skipping source "nexusmods": id already in use`)
}

func TestBuiltinSourceFactories_IncludesIcarus(t *testing.T) {
	found := false
	for _, factory := range builtinSourceFactories {
		if factory().ID() == "icarus" {
			found = true
		}
	}
	if !found {
		t.Error("builtinSourceFactories should include the icarus source")
	}
}

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

	registerCustomSources(svc, cfgDir, t.TempDir())

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
