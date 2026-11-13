package core

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #606 re-review item 4: the switch warning is a courtesy on the game page.
// A database that cannot count the game's mods costs the warning, never the
// whole game status the page needs to render.
func TestGameStatus_AModCountFailureOnlyCostsTheWarning(t *testing.T) {
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	svc.RegisterAdapter(rootRouter{})
	svc.SetKnownGames(func() []KnownGame {
		return []KnownGame{{Slug: "g", Name: "G", SteamAppID: "1", Adapter: "root-router"}}
	})
	ctx := context.Background()
	game := &domain.Game{ID: "g", Name: "G", InstallPath: t.TempDir(), ModPath: t.TempDir(), Launch: &domain.GameLaunch{SteamAppID: "1"}}
	require.NoError(t, svc.SaveGame(ctx, game))

	// The mod count's table is gone; everything else GameStatus reads is
	// still there.
	_, err = svc.db.ExecContext(ctx, `DROP TABLE installed_mods`)
	require.NoError(t, err)
	_, err = svc.db.CountGameMods(ctx, "g")
	require.Error(t, err, "the count now fails")

	status, err := svc.GameStatus(ctx, game)
	require.NoError(t, err, "the status still answers")
	assert.Equal(t, "root-router", status.SuggestedAdapter)
	assert.Empty(t, status.SuggestedAdapterWarning)
}
