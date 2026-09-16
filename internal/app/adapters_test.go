package app

// #412/#413: registering a concrete adapter is what makes core's
// derivations LIVE - `deploy_mode: compile` => `icarus`, and a game with
// BepInEx => `bepinex`. U1 pinned both states deliberately: core derives a
// name only for an adapter that is actually registered, so this package is
// where each switch is thrown, and these are the tests that prove it was.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterAdapters_RegistersEveryConcreteAdapterBesideTheIdentity(t *testing.T) {
	svc := newTestService(t)
	RegisterAdapters(svc)

	assert.ElementsMatch(t, []string{adapter.GenericID, "icarus", "bepinex"}, svc.ListAdapters(),
		"internal/app registers every concrete adapter; the identity is the registry's own built-in")
}

// TestRegisterAdapters_LoaderGameMigratesToBepInEx is U3's half of the same
// migration (#413): a game declaring `loader: kind: bepinex` resolves to the
// BepInEx adapter with no `adapter:` key to write, which is what keeps every
// games.yaml written for #359 working unchanged.
func TestRegisterAdapters_LoaderGameMigratesToBepInEx(t *testing.T) {
	svc := newTestService(t)
	RegisterAdapters(svc)

	root := t.TempDir()
	game := &domain.Game{ID: "valheim", InstallPath: root, ModPath: root, Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx}}
	assert.Equal(t, "bepinex", svc.AdapterName(game))

	a, err := svc.AdapterFor(game)
	require.NoError(t, err)
	_, routes := a.(adapter.FileRouter)
	assert.True(t, routes, "the migrated adapter must be the one that routes BepInEx/config/**")
	assert.Empty(t, game.Adapter, "the derivation is in-memory: games.yaml is never rewritten")
}

// TestRegisterAdapters_AnInstalledLoaderMigratesToo is #424 through the
// same door: the declaration is a statement of intent lmm asks for, while
// BepInEx's preloader in the install directory is a FACT lmm can read, and
// a game that has one but not the other is still a BepInEx game.
func TestRegisterAdapters_AnInstalledLoaderMigratesToo(t *testing.T) {
	svc := newTestService(t)
	RegisterAdapters(svc)

	root := t.TempDir()
	preloader := filepath.Join(root, "BepInEx", "core", "BepInEx.Preloader.dll")
	require.NoError(t, os.MkdirAll(filepath.Dir(preloader), 0o755))
	require.NoError(t, os.WriteFile(preloader, []byte("preloader"), 0o644))

	game := &domain.Game{ID: "valheim", InstallPath: root, ModPath: root}
	assert.Equal(t, "bepinex", svc.AdapterName(game))
}

// TestRegisterAdapters_CompileGameMigratesToIcarus is the migration itself:
// an existing games.yaml carrying `deploy_mode: compile` and NO `adapter:`
// key resolves to the Icarus adapter, with nothing for the user to edit and
// nothing written back.
func TestRegisterAdapters_CompileGameMigratesToIcarus(t *testing.T) {
	svc := newTestService(t)
	RegisterAdapters(svc)

	game := &domain.Game{ID: "icarus", DeployMode: domain.DeployCompile}
	assert.Equal(t, "icarus", svc.AdapterName(game))

	a, err := svc.AdapterFor(game)
	require.NoError(t, err)
	_, canCompile := adapter.Compiler(a)
	assert.True(t, canCompile, "the migrated adapter must be the one that actually compiles")
	assert.Empty(t, game.Adapter, "the derivation is in-memory: games.yaml is never rewritten")
}

// A game that says nothing still gets the identity, which is every game lmm
// managed before the seam existed.
func TestRegisterAdapters_PlainGameStaysGeneric(t *testing.T) {
	svc := newTestService(t)
	RegisterAdapters(svc)

	a, err := svc.AdapterFor(&domain.Game{ID: "skyrim-se"})
	require.NoError(t, err)
	assert.Equal(t, adapter.GenericID, a.ID())
	_, canCompile := adapter.Compiler(a)
	assert.False(t, canCompile)
}

// TestOpen_WarnsAboutALoaderBlockTheAdapterIgnores is design decision 11's
// load-time warning (#413 review F5), end to end through the composition
// root: a games.yaml whose loader game names another adapter is reported on
// the warning channel every time lmm opens it - once per game, and not at
// all for a game whose adapter is the loader's.
func TestOpen_WarnsAboutALoaderBlockTheAdapterIgnores(t *testing.T) {
	cfgDir := t.TempDir()
	gameDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "games.yaml"), []byte(`games:
  valheim:
    name: Valheim
    install_path: `+gameDir+`
    mod_path: `+gameDir+`
    adapter: generic-files
    loader:
      kind: bepinex
  lethal-company:
    name: Lethal Company
    install_path: `+gameDir+`
    mod_path: `+gameDir+`
    loader:
      kind: bepinex
`), 0o644))

	var warn bytes.Buffer
	svc, err := Open(t.Context(), Options{ConfigDir: cfgDir, DataDir: t.TempDir(), WarnWriter: &warn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	out := warn.String()
	assert.Equal(t, 1, strings.Count(out, "declares the BepInEx loader"), "one warning, for the one bypassing game: %q", out)
	// #456: one short line per game, pointing at where the fix is.
	assert.Equal(t, "warning: game \"valheim\" declares the BepInEx loader, but lmm ignores it because its adapter is \"generic-files\"; run `lmm game show valheim` for the fix\n", out)
	assert.NotContains(t, out, `"lethal-company"`, "a game resolving to bepinex is configured correctly")

	// #413 re-review M2: a caller that reports a game's warning itself
	// names it, and Open leaves that game's copy out.
	require.NoError(t, svc.Close())
	warn.Reset()
	svc, err = Open(t.Context(), Options{ConfigDir: cfgDir, DataDir: t.TempDir(), WarnWriter: &warn, OmitAdapterWarnings: []string{"valheim"}})
	require.NoError(t, err)
	assert.Empty(t, warn.String())
}
