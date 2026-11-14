package serve

// #635 through the entry points the SPA drives: on a Witcher 3 game a mod
// of the witcher3tl type (the game directory) is planned with its bin/
// file named "<game root>/..." and its mods/ folder as mod_path files, and
// the deploy job puts each where the plan said.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlowModType_Witcher3TopLevelDeploysFromTheGameRoot(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	ctx := context.Background()
	app.RegisterAdapters(svc)
	game.ModPath = filepath.Join(game.InstallPath, "mods")
	game.Adapter = "witcher3"
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(ctx, game))
	key := cache.Key{GameID: game.ID, SourceID: fixtureSourceID, ModID: "m1", Version: "1.0"}
	require.NoError(t, svc.GetGameCache(game).Store(key, "mods/modMenu/content/blob0.bundle", []byte("bundle")))
	require.NoError(t, svc.GetGameCache(game).Store(key, "bin/config/r4game/user_config_matrix/pc/modMenu.xml", []byte("menu")))
	row, err := svc.GetInstalledMod(ctx, fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	row.ModType = "witcher3tl"
	require.NoError(t, svc.SaveInstalledMod(ctx, row))

	_, raw := planFlow(t, s, game, "deploy", `{}`)
	assert.Contains(t, string(raw), `"<game root>/bin/config/r4game/user_config_matrix/pc/modMenu.xml"`)
	assert.Contains(t, string(raw), `"modMenu/content/blob0.bundle"`, "the archive's mods/ folder is the mod_path")

	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	assert.Equal(t, "menu", readFile(t, filepath.Join(game.InstallPath, "bin", "config", "r4game", "user_config_matrix", "pc", "modMenu.xml")))
	assert.Equal(t, "bundle", readFile(t, filepath.Join(game.ModPath, "modMenu", "content", "blob0.bundle")))
}
