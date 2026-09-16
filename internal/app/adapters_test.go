package app

// #412/#413: registering a concrete adapter is what makes core's
// derivations LIVE - `deploy_mode: compile` => `icarus`, and a game with
// BepInEx => `bepinex`. U1 pinned both states deliberately: core derives a
// name only for an adapter that is actually registered, so this package is
// where each switch is thrown, and these are the tests that prove it was.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterAdapters_RegistersEveryConcreteAdapterBesideTheIdentity(t *testing.T) {
	svc := newTestService(t)
	registerAdapters(svc)

	assert.ElementsMatch(t, []string{adapter.GenericID, "icarus", "bepinex"}, svc.ListAdapters(),
		"internal/app registers every concrete adapter; the identity is the registry's own built-in")
}

// TestRegisterAdapters_LoaderGameMigratesToBepInEx is U3's half of the same
// migration (#413): a game declaring `loader: kind: bepinex` resolves to the
// BepInEx adapter with no `adapter:` key to write, which is what keeps every
// games.yaml written for #359 working unchanged.
func TestRegisterAdapters_LoaderGameMigratesToBepInEx(t *testing.T) {
	svc := newTestService(t)
	registerAdapters(svc)

	game := &domain.Game{ID: "valheim", Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx}}
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
	registerAdapters(svc)

	root := t.TempDir()
	preloader := filepath.Join(root, "BepInEx", "core", "BepInEx.Preloader.dll")
	require.NoError(t, os.MkdirAll(filepath.Dir(preloader), 0o755))
	require.NoError(t, os.WriteFile(preloader, []byte("preloader"), 0o644))

	game := &domain.Game{ID: "valheim", InstallPath: root}
	assert.Equal(t, "bepinex", svc.AdapterName(game))
}

// TestRegisterAdapters_CompileGameMigratesToIcarus is the migration itself:
// an existing games.yaml carrying `deploy_mode: compile` and NO `adapter:`
// key resolves to the Icarus adapter, with nothing for the user to edit and
// nothing written back.
func TestRegisterAdapters_CompileGameMigratesToIcarus(t *testing.T) {
	svc := newTestService(t)
	registerAdapters(svc)

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
	registerAdapters(svc)

	a, err := svc.AdapterFor(&domain.Game{ID: "skyrim-se"})
	require.NoError(t, err)
	assert.Equal(t, adapter.GenericID, a.ID())
	_, canCompile := adapter.Compiler(a)
	assert.False(t, canCompile)
}
