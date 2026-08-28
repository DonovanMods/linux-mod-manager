package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authChecker matches the IsAuthenticated() method every source that accepts
// SetAPIKey also exposes — used to prove a resolved key actually reached the
// constructed source via the registration pipeline's SetAPIKey seam.
type authChecker interface{ IsAuthenticated() bool }

// keySource is a minimal auth-capable ModSource that records the key applied
// through SetAPIKey and names its own env var via source.EnvKeyProvider, so
// tests control exactly which variable ResolveAPIKey consults.
type keySource struct {
	id, name, envKey, apiKey string
}

func (k *keySource) ID() string      { return k.id }
func (k *keySource) Name() string    { return k.name }
func (k *keySource) AuthURL() string { return "" }
func (k *keySource) EnvKey() string  { return k.envKey }
func (k *keySource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (k *keySource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}
func (k *keySource) GetMod(context.Context, string, string) (*domain.Mod, error) { return nil, nil }
func (k *keySource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (k *keySource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (k *keySource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (k *keySource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}
func (k *keySource) Capabilities() source.Capabilities { return source.Capabilities{Auth: true} }
func (k *keySource) SetAPIKey(key string)              { k.apiKey = key }

func newTestService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}

// writeSourceYAML writes a custom source definition file.
func writeSourceYAML(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
}

func TestEnvKeyFor(t *testing.T) {
	assert.Equal(t, "NEXUSMODS_API_KEY", EnvKeyFor(nexusmods.New(nil, "")))
	assert.Equal(t, "CURSEFORGE_API_KEY", EnvKeyFor(curseforge.New(nil, "")))

	dirSrc, err := custom.NewDirectory(custom.SourceDefinition{
		ID:        "unknown-source",
		Name:      "Unknown Source",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	assert.Equal(t, "LMM_UNKNOWN_SOURCE_API_KEY", EnvKeyFor(dirSrc), "sources without EnvKeyProvider fall back to the derived name")
}

func TestEnvKeyForSourceID(t *testing.T) {
	assert.Equal(t, "LMM_DONOVAN_MODS_API_KEY", EnvKeyForSourceID("donovan-mods"))
	assert.Equal(t, "LMM_MY_REPO_API_KEY", EnvKeyForSourceID("my-repo"))
}

// TestRegisterSource_KeyResolutionPrecedence pins env-over-stored-token
// precedence at the unit that implements it, with distinct observable values.
func TestRegisterSource_KeyResolutionPrecedence(t *testing.T) {
	const envVar = "LMM_PRECEDENCE_TEST_API_KEY"

	t.Run("env value wins when both env and a stored token are present", func(t *testing.T) {
		svc := newTestService(t)
		require.NoError(t, svc.SaveSourceToken("precedence-src", "token-value"))
		t.Setenv(envVar, "env-value")

		src := &keySource{id: "precedence-src", name: "Precedence Src", envKey: envVar}
		registerSource(svc, src, os.Stderr)

		assert.Equal(t, "env-value", src.apiKey)
	})

	t.Run("stored token applies when no env value is present", func(t *testing.T) {
		svc := newTestService(t)
		require.NoError(t, svc.SaveSourceToken("precedence-src", "token-value"))
		t.Setenv(envVar, "")

		src := &keySource{id: "precedence-src", name: "Precedence Src", envKey: envVar}
		registerSource(svc, src, os.Stderr)

		assert.Equal(t, "token-value", src.apiKey)
	})
}

// TestRegisterSources_BuiltinAuthenticatesWithEnvAndToken pins that a
// built-in constructed keyless still ends up authenticated through the
// shared key pipeline. IsAuthenticated() only proves *some* key applied, not
// which one — TestRegisterSource_KeyResolutionPrecedence is what pins
// env-over-token precedence; the two tests are not redundant.
func TestRegisterSources_BuiltinAuthenticatesWithEnvAndToken(t *testing.T) {
	t.Setenv("NEXUSMODS_API_KEY", "env-key-value")
	svc := newTestService(t)
	require.NoError(t, svc.SaveSourceToken("nexusmods", "stored-db-key"))

	registerSources(svc, t.TempDir(), os.Stderr)

	src, err := svc.GetSource("nexusmods")
	require.NoError(t, err)
	auth, ok := src.(authChecker)
	require.True(t, ok, "nexusmods must expose IsAuthenticated")
	assert.True(t, auth.IsAuthenticated())
}

// TestRegisterSources_DerivedEnvKeyForCustom pins that a custom source with no
// EnvKeyProvider resolves its key via the derived LMM_<ID>_API_KEY name.
func TestRegisterSources_DerivedEnvKeyForCustom(t *testing.T) {
	svc := newTestService(t)
	cfgDir := t.TempDir()
	// The definition declares auth: the key pipeline is gated on
	// Capabilities().Auth, and this test pins the env-var NAME derivation,
	// which needs the key to actually apply.
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

	registerSources(svc, cfgDir, os.Stderr)

	src, err := svc.GetSource("my-custom")
	require.NoError(t, err)
	auth, ok := src.(authChecker)
	require.True(t, ok, "custom manifest source must expose IsAuthenticated")
	assert.True(t, auth.IsAuthenticated())
}

// TestRegisterSources_FirstWinsCollision pins that built-ins register first,
// so a custom definition claiming "nexusmods" loses and a warning is written
// to the supplied writer.
func TestRegisterSources_FirstWinsCollision(t *testing.T) {
	svc := newTestService(t)
	cfgDir := t.TempDir()
	writeSourceYAML(t, filepath.Join(cfgDir, "sources"), "nexusmods.yaml", fmt.Sprintf(`
id: nexusmods
name: Shadow NexusMods
type: directory
directory:
  path: %s
`, t.TempDir()))

	var warnBuf bytes.Buffer
	registerSources(svc, cfgDir, &warnBuf)

	src, err := svc.GetSource("nexusmods")
	require.NoError(t, err)
	assert.Equal(t, "Nexus Mods", src.Name(), "built-in nexusmods must win")
	assert.Contains(t, warnBuf.String(), `warning: skipping source "nexusmods": id already in use`)
}

func TestBuiltinSourceFactories_IncludesIcarus(t *testing.T) {
	found := false
	for _, factory := range builtinSourceFactories {
		if factory().ID() == "icarus" {
			found = true
		}
	}
	assert.True(t, found, "builtinSourceFactories should include the icarus source")
}

// TestRegisterCustomSources_SkipsInvalidDefinitions exercises all three
// warn-and-skip branches: a per-file load error, an id already claimed by a
// previously-registered source, and a definition that parses but fails
// construction. Each produces a warning on the writer and no registration.
func TestRegisterCustomSources_SkipsInvalidDefinitions(t *testing.T) {
	svc := newTestService(t)

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
`) // construction-failure branch

	var warnBuf bytes.Buffer
	registerCustomSources(svc, cfgDir, &warnBuf)

	byID := map[string]source.ModSource{}
	for _, s := range svc.ListSources() {
		byID[s.ID()] = s
	}
	require.Contains(t, byID, "good-src")
	assert.Equal(t, "Good Src", byID["good-src"].Name())
	require.Contains(t, byID, "taken")
	assert.Equal(t, "Pre-registered", byID["taken"].Name(), "collide.yaml must not overwrite the pre-existing source")
	assert.NotContains(t, byID, "missing-path")
	assert.Len(t, byID, 2)

	warnings := warnBuf.String()
	assert.Contains(t, warnings, "warning: skipping source definition") // broken.yaml
	assert.Contains(t, warnings, `warning: skipping source "taken": id already in use`)
	assert.Contains(t, warnings, `warning: skipping source "missing-path":`)
}
