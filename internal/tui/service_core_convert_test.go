package tui_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/tui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newConvertActionsFixture builds a DeployCompile game - #221's pak-to-exmod
// conversion flag is only meaningful there (ModItem.CompileGame) - and
// returns BOTH the ActionProvider and DataProvider views over the identical
// (svc, game, "default") triple. coreProvider carries no in-memory-only
// state (NewCoreActions/NewCoreProvider's own doc comment, service_core.go),
// so the two independently constructed instances always observe the same
// underlying DB/filesystem truth - letting this test mutate via one and
// re-read via the other. Unlike newRecompileActionsFixture
// (service_core_recompile_test.go), no merge-compiler source is registered
// and no base pak is written: this test never deploys or merges anything,
// so that machinery would be pure overhead - mirrors cmd/lmm/mod_convert_
// test.go's setupDoModConvertTest, the CLI's own lightweight convert
// fixture.
func newConvertActionsFixture(t *testing.T) (tui.ActionProvider, tui.DataProvider, *core.Service, *domain.Game) {
	t.Helper()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID:          "icarus",
		Name:        "Icarus",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
		DeployMode:  domain.DeployCompile,
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	return tui.NewCoreActions(svc, game, "default"), tui.NewCoreProvider(svc, game, "default"), svc, game
}

// TestCoreProviderSetConvertPaks proves the #221 TUI toggle seam end to end:
// coreProvider.SetConvertPaks persists via svc.SetModConvertPaks - a local
// DB write, no network, no hooks (mirroring SetUpdatePolicy's own shape) -
// and the next Overview call reflects both the flipped ConvertPaks flag and
// CompileGame (populated from the game's DeployMode, not the mod itself -
// service_core.go's Overview mapping) for every item.
func TestCoreProviderSetConvertPaks(t *testing.T) {
	actions, provider, svc, game := newConvertActionsFixture(t)
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", GameID: game.ID, Name: "Bear Mount", Version: "3.3"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: "bear-mount", Version: "3.3"}))

	item := tui.ModItem{ID: "bear-mount", Source: "fake-compiler", Name: "Bear Mount"}

	outcome, err := actions.SetConvertPaks(context.Background(), item, false)
	require.NoError(t, err)
	assert.Contains(t, outcome.Message, "pak conversion: off")

	mod, err := svc.GetInstalledMod("fake-compiler", "bear-mount", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.ConvertPaks, "DB flag must read back false")

	_, mods, err := provider.Overview(context.Background())
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.False(t, mods[0].ConvertPaks, "Overview must reflect the flipped flag")
	assert.True(t, mods[0].CompileGame, "CompileGame reflects the game's DeployMode, not the mod")
}
