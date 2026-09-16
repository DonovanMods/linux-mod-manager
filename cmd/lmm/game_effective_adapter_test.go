package main

// #426 (#413 review F4): `lmm game list`, `lmm game show` and `lmm game edit
// --adapter` name the adapter a game USES. A `deploy_mode: compile` game
// with no `adapter:` key compiles through icarus, and a game with BepInEx
// lays its archives out through bepinex; both used to render
// "generic-files", on the one surface that exists to say which adapter a
// game has.

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// namedAdapter is an identity adapter registered under a real adapter's
// NAME. Which adapter a game resolves to is decided by name and
// registration alone, so the display tests need nothing more.
type namedAdapter string

func (a namedAdapter) ID() string    { return string(a) }
func (a namedAdapter) Label() string { return string(a) }
func (namedAdapter) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}

// setupEffectiveAdapterGames is a build that ships icarus and bepinex, with
// one game of each derivation plus a plain one.
func setupEffectiveAdapterGames(t *testing.T) *core.Service {
	t.Helper()
	svc := setupGameEditTest(t) // skyrim-se: no key, nothing to derive
	svc.RegisterAdapter(namedAdapter("icarus"))
	svc.RegisterAdapter(namedAdapter("bepinex"))
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: "/games/icarus", ModPath: "/games/icarus/Mods",
		DeployMode: domain.DeployCompile, ConvertPaks: true,
	}))
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "valheim", Name: "Valheim", InstallPath: "/games/valheim", ModPath: "/games/valheim",
		Loader: &domain.GameLoader{Kind: domain.LoaderKindBepInEx},
	}))
	return svc
}

func TestDoGameList_NamesTheEffectiveAdapter(t *testing.T) {
	svc := setupEffectiveAdapterGames(t)

	out := captureStdout(t, func() error { return doGameList(&cobra.Command{}, svc) })
	header, rows := gameListCells(t, out)
	require.Equal(t, "ADAPTER", header[4])

	byID := map[string]string{}
	for _, row := range rows {
		byID[row[0]] = row[4]
	}
	assert.Equal(t, map[string]string{
		"icarus":    "icarus",
		"skyrim-se": "generic-files",
		"valheim":   "bepinex",
	}, byID)
}

func TestDoGameShow_NamesTheEffectiveAdapter(t *testing.T) {
	svc := setupEffectiveAdapterGames(t)

	out := captureStdout(t, func() error { return doGameShow(context.Background(), svc, "valheim") })
	assert.Contains(t, out, "Adapter:      bepinex")
	assert.Contains(t, out, "derived", "a name games.yaml does not carry is said to be derived")

	out = captureStdout(t, func() error { return doGameShow(context.Background(), svc, "skyrim-se") })
	assert.Contains(t, out, "Adapter:      generic-files")
	assert.NotContains(t, out, "derived", "the identity needs no explanation")
}

// Clearing the key hands the game back to its derived adapter, and the
// confirmation says which one rather than claiming generic-files.
func TestDoGameEdit_ClearingTheAdapterNamesTheOneInUse(t *testing.T) {
	svc := setupEffectiveAdapterGames(t)
	adapterBefore := gameEditAdapter
	t.Cleanup(func() { gameEditAdapter = adapterBefore })

	gameEditAdapter = "generic-files"
	out := captureStdout(t, func() error { return doGameEdit(context.Background(), svc, "valheim", true) })
	assert.Contains(t, out, "adapter set to generic-files")

	gameEditAdapter = ""
	out = captureStdout(t, func() error { return doGameEdit(context.Background(), svc, "valheim", true) })
	assert.Contains(t, out, "adapter cleared")
	assert.Contains(t, out, "bepinex")
}

func TestJSONGolden_GameListEffectiveAdapter(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterAdapter(namedAdapter("icarus"))
	svc.RegisterAdapter(namedAdapter("bepinex"))
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: "/games/icarus", ModPath: "/games/icarus/Mods",
		DeployMode: domain.DeployCompile, ConvertPaks: true,
	}))
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "valheim", Name: "Valheim", InstallPath: "/games/valheim", ModPath: "/games/valheim",
		Adapter: "bepinex",
	}))
	withJSONOutput(t)

	out := captureStdout(t, func() error { return doGameList(&cobra.Command{}, svc) })
	assertJSONCLIGolden(t, "game_list_effective_adapter", out)
}
