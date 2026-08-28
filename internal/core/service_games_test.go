package core_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/require"
)

// TestService_SaveGame_ConcurrentWithReaders pins the games-map contract: a
// writer and many readers may interleave freely (run with -race).
func TestService_SaveGame_ConcurrentWithReaders(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	ctx := context.Background()

	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			errs <- svc.SaveGame(ctx, &domain.Game{ID: fmt.Sprintf("g%d", i), Name: "G", InstallPath: t.TempDir(), ModPath: "Mods"})
		}(i)
		go func() {
			defer wg.Done()
			_ = svc.ListGames()
			_, _ = svc.GetGame("g0")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, svc.ListGames(), 8)
	g, err := svc.GetGame("g3")
	require.NoError(t, err)
	require.Equal(t, "g3", g.ID)
}

// TestService_SaveGame_FailurePath pins that a persistence failure (an
// unwritable ConfigDir) returns an error and leaves the in-memory games map
// unchanged: service.go's saveGame only writes svc.games[game.ID] after
// config.SaveGame returns nil, so a failed write must never become visible
// via GetGame.
func TestService_SaveGame_FailurePath(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission checks are bypassed when running as root")
	}
	configDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	require.NoError(t, os.Chmod(configDir, 0500))
	t.Cleanup(func() { _ = os.Chmod(configDir, 0755) })

	err = svc.SaveGame(context.Background(), &domain.Game{ID: "unwritable", Name: "G", ModPath: t.TempDir()})
	require.Error(t, err)

	_, err = svc.GetGame("unwritable")
	require.Error(t, err, "a failed SaveGame must not publish the game to the in-memory map")
}
