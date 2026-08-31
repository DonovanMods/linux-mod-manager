package app

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/nexusmods"
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
		require.NoError(t, svc.SaveSourceToken(context.Background(), "precedence-src", "token-value"))
		t.Setenv(envVar, "env-value")

		src := &keySource{id: "precedence-src", name: "Precedence Src", envKey: envVar}
		registerSource(t.Context(), svc, src, os.Stderr)

		assert.Equal(t, "env-value", src.apiKey)
	})

	t.Run("stored token applies when no env value is present", func(t *testing.T) {
		svc := newTestService(t)
		require.NoError(t, svc.SaveSourceToken(context.Background(), "precedence-src", "token-value"))
		t.Setenv(envVar, "")

		src := &keySource{id: "precedence-src", name: "Precedence Src", envKey: envVar}
		registerSource(t.Context(), svc, src, os.Stderr)

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
	require.NoError(t, svc.SaveSourceToken(context.Background(), "nexusmods", "stored-db-key"))

	registerSources(t.Context(), svc, t.TempDir(), os.Stderr)

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

	registerSources(t.Context(), svc, cfgDir, os.Stderr)

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
	registerSources(t.Context(), svc, cfgDir, &warnBuf)

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
	registerCustomSources(t.Context(), svc, cfgDir, &warnBuf)

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

// TestLoadSourceDefinitions pins the cmd/lmm-facing wrapper (v2 Phase 2 Task
// 22): 'lmm source list' needs both the loaded definitions and the per-file
// load errors, without importing internal/storage/config itself.
func TestLoadSourceDefinitions(t *testing.T) {
	cfgDir := t.TempDir()
	writeSourceYAML(t, filepath.Join(cfgDir, "sources"), "good.yaml", fmt.Sprintf(`
id: good-src
name: Good Src
type: directory
directory:
  path: %s
`, t.TempDir()))
	writeSourceYAML(t, filepath.Join(cfgDir, "sources"), "broken.yaml", "id: [unclosed")

	defs, loadErrs, err := LoadSourceDefinitions(cfgDir)
	require.NoError(t, err)
	require.Len(t, defs, 1)
	assert.Equal(t, "good-src", defs[0].ID)
	require.Len(t, loadErrs, 1)
	assert.Equal(t, "broken.yaml", loadErrs[0].File)
}

// TestLoadSourceDefinitionFile pins 'lmm source validate's single-file load.
func TestLoadSourceDefinitionFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "good.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
id: my-mods
name: My Mods
type: directory
directory:
  path: `+t.TempDir()+`
`), 0644))

	def, err := LoadSourceDefinitionFile(path)
	require.NoError(t, err)
	assert.Equal(t, "my-mods", def.ID)
}

func TestLoadSourceDefinitionFile_Invalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
id: BAD_ID
name: Bad
type: directory
directory:
  path: ~/x
`), 0644))

	_, err := LoadSourceDefinitionFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must match")
}

// TestConstructSource pins the bare-construction query 'lmm source list'
// uses to recover an unregistered definition's construction error, without
// performing any live probe (directory scan, manifest fetch, API call).
func TestConstructSource(t *testing.T) {
	src, err := ConstructSource(source.SourceDefinition{
		ID: "dir-src", Name: "Dir Src", Type: source.TypeDirectory,
		Directory: &source.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	assert.Equal(t, "dir-src", src.ID())
}

func TestConstructSource_ConstructionFailure(t *testing.T) {
	_, err := ConstructSource(source.SourceDefinition{
		ID: "missing", Name: "Missing", Type: source.TypeDirectory,
		Directory: &source.DirectoryConfig{Path: "/this/path/should/not/exist/lmm-test-fixture"},
	})
	assert.Error(t, err)
}

// TestProbeSource_Directory pins 'lmm source validate --probe's directory/
// manifest summary format.
func TestProbeSource_Directory(t *testing.T) {
	svc := newTestService(t)
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "SomeMod"), 0755))

	summary, err := ProbeSource(t.Context(), svc, source.SourceDefinition{
		ID: "probe-dir", Name: "Probe Dir", Type: source.TypeDirectory,
		Directory: &source.DirectoryConfig{Path: root},
	}, "")
	require.NoError(t, err)
	assert.Contains(t, summary, "ok")
	assert.Contains(t, summary, "1 mod(s)")
}

// TestProbeSource_APIWithoutSearchRequiresProbeID pins the "no search
// endpoint" guard: an api definition whose only endpoint is get_mod refuses
// to probe without an explicit mod id.
func TestProbeSource_APIWithoutSearchRequiresProbeID(t *testing.T) {
	svc := newTestService(t)

	_, err := ProbeSource(t.Context(), svc, source.SourceDefinition{
		ID: "probe-api", Name: "Probe API", Type: source.TypeAPI,
		API: &source.APIConfig{
			BaseURL: "https://api.x.test",
			Endpoints: source.APIEndpoints{
				GetMod: &source.EndpointConfig{Path: "/mods/{mod_id}"},
			},
			Mappings: source.APIMappings{Mod: map[string]string{"id": "id", "name": "name"}},
		},
	}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--id")
}

func TestProbeSource_ConstructionFailure(t *testing.T) {
	svc := newTestService(t)

	_, err := ProbeSource(t.Context(), svc, source.SourceDefinition{
		ID: "bad", Name: "Bad", Type: source.TypeDirectory,
		Directory: &source.DirectoryConfig{Path: "/this/path/should/not/exist/lmm-test-fixture"},
	}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "constructing source")
}

// --- SourceInfos (v2 Phase 3 Task 3, #301) ---

// newDirectorySource builds a registered-able directory source with the
// given ID, for the SourceInfos scoping tests.
func newDirectorySource(t *testing.T, id string) source.ModSource {
	t.Helper()
	src, err := custom.NewDirectory(custom.SourceDefinition{
		ID:        id,
		Name:      strings.ToUpper(id),
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)
	return src
}

// TestSourceInfos_FullRegistrySortedByID covers the no-game view: every
// registered source, ordered by ID (ListSources is registry-map order), with
// no in-use marking to do.
func TestSourceInfos_FullRegistrySortedByID(t *testing.T) {
	svc := newTestService(t)
	svc.RegisterSource(newDirectorySource(t, "zulu"))
	svc.RegisterSource(newDirectorySource(t, "alpha"))

	rows, err := SourceInfos(t.Context(), svc, nil, false)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "alpha", rows[0].ID)
	assert.Equal(t, "zulu", rows[1].ID)
	assert.Equal(t, "ALPHA", rows[0].Name)
	assert.Equal(t, "directory", rows[0].Type)
	assert.Equal(t, AuthNone, rows[0].Auth, "a source with no auth capability reports AuthNone, not AuthRequired")
	assert.Contains(t, rows[0].Capabilities, "search")
	assert.False(t, rows[0].InUse)
}

// TestSourceInfos_ScopedToGame covers the default game-context view: only the
// game's own configured sources, and no in-use marking (the whole list is in
// use).
func TestSourceInfos_ScopedToGame(t *testing.T) {
	svc := newTestService(t)
	svc.RegisterSource(newDirectorySource(t, "mapped"))
	svc.RegisterSource(newDirectorySource(t, "unmapped"))
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), SourceIDs: map[string]string{"mapped": ""}}
	require.NoError(t, svc.SaveGame(t.Context(), game))

	rows, err := SourceInfos(t.Context(), svc, game, false)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "mapped", rows[0].ID)
	assert.False(t, rows[0].InUse)
}

// TestSourceInfos_AllWithGameMarksInUse covers the one combination that needs
// a marker rather than a filter: the full registry, with the game's own
// sources flagged.
func TestSourceInfos_AllWithGameMarksInUse(t *testing.T) {
	svc := newTestService(t)
	svc.RegisterSource(newDirectorySource(t, "mapped"))
	svc.RegisterSource(newDirectorySource(t, "unmapped"))
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), SourceIDs: map[string]string{"mapped": ""}}
	require.NoError(t, svc.SaveGame(t.Context(), game))

	rows, err := SourceInfos(t.Context(), svc, game, true)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	byID := map[string]SourceInfo{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	assert.True(t, byID["mapped"].InUse)
	assert.False(t, byID["unmapped"].InUse)
}

// TestSourceInfos_ErrorRows covers the three ways a definition fails to
// become a source - an unparseable file, an ID already taken, and a
// construction failure - all of which stay visible in every view (they never
// registered, so they have no game to scope by), each carrying both the
// structured error and its message.
func TestSourceInfos_ErrorRows(t *testing.T) {
	svc := newTestService(t)
	// A BUILT-IN holds the colliding ID: a custom source registered under a
	// definition's own ID is that definition's own source, not a collision.
	svc.RegisterSource(nexusmods.New(nil, ""))
	srcDir := filepath.Join(svc.ConfigDir(), "sources")

	writeSourceYAML(t, srcDir, "broken.yaml", "id: [unclosed")
	writeSourceYAML(t, srcDir, "collide.yaml", fmt.Sprintf(`
id: nexusmods
name: Collide Src
type: directory
directory:
  path: %s
`, t.TempDir()))
	writeSourceYAML(t, srcDir, "missing-path.yaml", `
id: missing-path
name: Missing Path
type: directory
directory:
  path: /this/path/should/not/exist/lmm-test-fixture
`)

	rows, err := SourceInfos(t.Context(), svc, nil, false)
	require.NoError(t, err)

	byID := map[string]SourceInfo{}
	for _, r := range rows {
		byID[r.ID] = r
	}

	collision := byID["nexusmods"]
	require.Equal(t, "error", collision.Type, "the colliding definition's row is an error row (appended after the built-in's own)")
	assert.Equal(t, "id already in use", collision.ErrorMessage)
	require.Error(t, collision.Err, "the structured error travels with its message")
	assert.Equal(t, AuthUnknown, collision.Auth, "an error row's auth state was never determined - not AuthNone's evaluated 'no auth capability' claim (final review, Important #2 / #302)")

	failed := byID["missing-path"]
	assert.Equal(t, "error", failed.Type)
	assert.NotEmpty(t, failed.ErrorMessage)
	assert.Error(t, failed.Err)

	loadErr := byID["broken.yaml"]
	assert.Equal(t, "error", loadErr.Type, "a file that would not parse is keyed by its filename")
	assert.NotEmpty(t, loadErr.ErrorMessage)
	assert.Error(t, loadErr.Err)
}

// TestSourceInfo_ErrorRowMarshalsWithNoAuthKey pins the wire consequence of
// AuthUnknown + omitzero (final review, Important #2 / #302): an error row
// must carry no "auth" key at all, not the pre-fix "auth":"none" a consumer
// could misread as an evaluated "no auth capability" claim.
func TestSourceInfo_ErrorRowMarshalsWithNoAuthKey(t *testing.T) {
	row := newSourceInfoError("broken-mods", errors.New("boom"))
	assert.Equal(t, AuthUnknown, row.Auth)

	b, err := json.Marshal(row)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"auth"`, "an error row's auth state was never determined, so the key must be absent")
}

// TestCapabilitySummary_IncludesVersions pins that capabilitySummary appends
// the "versions" token after "auth" (#96). Moved here from cmd/lmm with the
// helper itself, when SourceInfos took over the row assembly.
func TestCapabilitySummary_IncludesVersions(t *testing.T) {
	assert.Equal(t, []string{"search", "deps", "updates", "auth", "versions"}, capabilitySummary(source.Capabilities{
		Search: true, Dependencies: true, Updates: true, Auth: true, Versions: true,
	}))
	assert.Equal(t, []string{"search"}, capabilitySummary(source.Capabilities{Search: true}))
}

// TestAuthState_StringMarshalUnmarshal round-trips every AuthState value
// through String/MarshalText/UnmarshalText (final review, Important #1 /
// #301: "enum coverage" for the type source list --json now carries
// directly, replacing the pre-#301 display string).
func TestAuthState_StringMarshalUnmarshal(t *testing.T) {
	tests := []struct {
		state AuthState
		want  string
	}{
		{AuthUnknown, "unknown"},
		{AuthNone, "none"},
		{AuthRequired, "required"},
		{AuthAuthenticated, "authenticated"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.state.String())

			b, err := tt.state.MarshalText()
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(b))

			var got AuthState
			require.NoError(t, got.UnmarshalText(b))
			assert.Equal(t, tt.state, got)
		})
	}
}

// TestAuthState_UnmarshalTextRejectsUnknown pins the fail-loud contract every
// other wire enum in this codebase follows (LinkMethod, UpdateStatus): an
// unrecognized value is a parse error, not a silent zero-value fallback.
func TestAuthState_UnmarshalTextRejectsUnknown(t *testing.T) {
	var a AuthState
	err := a.UnmarshalText([]byte("bogus"))
	require.Error(t, err)
}
