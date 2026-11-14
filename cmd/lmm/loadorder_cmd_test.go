package main

// #620 at the CLI: `lmm load-order list|move|reset` over core's report and
// its Plan/Apply pair, a deploy and a purge reporting what they did to
// Cyberpunk 2077's modlist.txt, and every --json document pinned.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deployedArchivesCLI is a Cyberpunk 2077 game on the cyberpunk2077
// adapter, its mod_path the install root, with mod a shipping a.archive
// and b.archive and mod b shipping c.archive, deployed.
func deployedArchivesCLI(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	app.RegisterAdapters(svc)
	game.InstallPath = game.ModPath
	game.Adapter = "cyberpunk2077"
	require.NoError(t, svc.SaveGame(ctx, game))
	for id, files := range map[string][]string{"a": {"a.archive", "b.archive"}, "b": {"c.archive"}} {
		for _, f := range files {
			require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: "src", ModID: id, Version: "1.0"}, "archive/pc/mod/"+f, []byte(f)))
		}
		seedSyncInstalledMod(t, svc, game, "src", id, id, "1.0", "default", true, nil)
		require.NoError(t, getProfileManager(svc).AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: id, Version: "1.0"}))
	}
	_ = captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })
	resetLoadOrderFlags(t)
	return svc, game, filepath.Join(game.InstallPath, "archive", "pc", "mod")
}

// resetLoadOrderFlags puts every `lmm load-order` flag back to its default
// for the test, and after it.
func resetLoadOrderFlags(t *testing.T) {
	t.Helper()
	setFlag(t, &loadOrderProfile, "")
	setFlag(t, &loadOrderID, "")
	setFlag(t, &loadOrderDryRun, false)
	setFlag(t, &loadOrderBefore, "")
	setFlag(t, &loadOrderAfter, "")
	setFlag(t, &loadOrderFirst, false)
	setFlag(t, &loadOrderLast, false)
}

func TestCLI_LoadOrderListPrintsTheEffectiveOrder(t *testing.T) {
	ctx := context.Background()
	svc, game, modDir := deployedArchivesCLI(t)
	assert.NoFileExists(t, filepath.Join(modDir, "modlist.txt"), "a profile with no order writes no list")

	stdout := captureStdout(t, func() error { return doLoadOrderList(ctx, svc, game) })
	assert.Contains(t, stdout, "Archives load order for "+game.ID+" (profile default)\n")
	assert.Contains(t, stdout, "The profile orders nothing")
	assert.Regexp(t, `1\s+a\.archive\s+default\s+a`, stdout)
	assert.Regexp(t, `3\s+c\.archive\s+default\s+b`, stdout)
}

func TestCLI_LoadOrderMoveDryRunThenApplyThenReset(t *testing.T) {
	ctx := context.Background()
	svc, game, modDir := deployedArchivesCLI(t)
	list := filepath.Join(modDir, "modlist.txt")

	setFlag(t, &loadOrderDryRun, true)
	stdout := captureStdout(t, func() error {
		return doLoadOrderEdit(ctx, svc, game, core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "c.archive", To: core.LoadOrderMoveFirst})
	})
	assert.Contains(t, stdout, "Load-order edit for profile default (dry run)\n")
	assert.Contains(t, stdout, "~ move c.archive to the top")
	assert.Contains(t, stdout, "would be written in this order")
	assert.NoFileExists(t, list, "a dry run writes nothing")

	setFlag(t, &loadOrderDryRun, false)
	stdout = captureStdout(t, func() error {
		return doLoadOrderEdit(ctx, svc, game, core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "c.archive", To: core.LoadOrderMoveFirst})
	})
	assert.Contains(t, stdout, "✓ move c.archive to the top")
	assert.Contains(t, stdout, "Load order cyberpunk2077-archive written: 3 file(s)")
	data, err := os.ReadFile(list)
	require.NoError(t, err)
	assert.Equal(t, "c.archive\r\na.archive\r\nb.archive", string(data))

	// A deploy reports it; a reset hands it back.
	stdout = captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })
	assert.Contains(t, stdout, "Load order cyberpunk2077-archive unchanged: 3 file(s)")
	stdout = captureStdout(t, func() error {
		return doLoadOrderEdit(ctx, svc, game, core.FileLoadOrderEdit{Op: core.LoadOrderOpReset})
	})
	assert.Contains(t, stdout, "list removed (there was none before lmm wrote it)")
	assert.NoFileExists(t, list)
}

func TestCLI_PurgeRestoresTheLoadOrderList(t *testing.T) {
	ctx := context.Background()
	svc, game, modDir := deployedArchivesCLI(t)
	profile, err := config.LoadProfile(svc.ConfigDir(), game.ID, "default")
	require.NoError(t, err)
	profile.LoadOrders = domain.FileLoadOrders{"cyberpunk2077-archive": {"b.archive"}}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "modlist.txt"), []byte("mine.archive\n"), 0o644))
	_ = captureStdout(t, func() error { return doDeploy(ctx, svc, game, nil) })

	setFlag(t, &purgeProfile, "")
	setFlag(t, &purgeYes, true)
	stdout := captureStdout(t, func() error { return doPurge(ctx, svc, game) })
	assert.Contains(t, stdout, "Load order cyberpunk2077-archive: the list you had before lmm wrote it is back")
	data, err := os.ReadFile(filepath.Join(modDir, "modlist.txt"))
	require.NoError(t, err)
	assert.Equal(t, "mine.archive\n", string(data))
}

func TestCLI_LoadOrderRefusesAGameWithoutOne(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	resetLoadOrderFlags(t)
	err := doLoadOrderList(ctx, svc, game)
	require.ErrorIs(t, err, core.ErrNoFileLoadOrder)
}

func TestJSONGolden_LoadOrder(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := deployedArchivesCLI(t)
	withJSONOutput(t)
	subs := []string{game.InstallPath, "<GAME>"}

	out := captureStdout(t, func() error { return doLoadOrderList(ctx, svc, game) })
	assertJSONCLIGolden(t, "load_order_list", out, subs...)

	setFlag(t, &loadOrderDryRun, true)
	out = captureStdout(t, func() error {
		return doLoadOrderEdit(ctx, svc, game, core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "b.archive", Before: "a.archive"})
	})
	assertJSONCLIGolden(t, "load_order_edit_dry_run", out, subs...)

	setFlag(t, &loadOrderDryRun, false)
	out = captureStdout(t, func() error {
		return doLoadOrderEdit(ctx, svc, game, core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "b.archive", Before: "a.archive"})
	})
	assertJSONCLIGolden(t, "load_order_edit_result", out, subs...)
}

// A collection import plan shows the load order it saves (#620): the first
// files, each with its mod when the source says, and whether lmm writes it.
func TestCLI_CollectionPlanPrintsTheLoadOrder(t *testing.T) {
	game := &domain.Game{ID: "cp", Name: "Cyberpunk 2077"}
	files := []core.CollectionLoadOrderFile{{Name: "! fixes.archive", Mod: "Quest Fixes"}}
	for i := 0; i < 11; i++ {
		files = append(files, core.CollectionLoadOrderFile{Name: string(rune('a'+i)) + ".archive"})
	}
	stdout := captureStdout(t, func() error {
		printCollectionLoadOrders([]core.CollectionLoadOrderSummary{
			{ID: "cyberpunk2077-archive", Label: "Archives", Managed: true, FirstWins: true, Files: files},
			{ID: "cyberpunk2077-redmod", Files: []core.CollectionLoadOrderFile{{Name: "x"}}},
		}, game)
		return nil
	})
	assert.Contains(t, stdout, "Archives load order (cyberpunk2077-archive): 12 file(s)\n")
	assert.Contains(t, stdout, "    1. ! fixes.archive  (Quest Fixes)\n")
	assert.Contains(t, stdout, "… and 2 more")
	assert.Contains(t, stdout, "written to Cyberpunk 2077's list at every deploy")
	assert.Contains(t, stdout, "does not write this load order")
}
