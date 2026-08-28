package core_test

import (
	"context"
	"fmt"
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

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			require.NoError(t, svc.SaveGame(ctx, &domain.Game{ID: fmt.Sprintf("g%d", i), Name: "G", InstallPath: t.TempDir(), ModPath: "Mods"}))
		}(i)
		go func() {
			defer wg.Done()
			_ = svc.ListGames()
			_, _ = svc.GetGame("g0")
		}()
	}
	wg.Wait()
	require.Len(t, svc.ListGames(), 8)
	g, err := svc.GetGame("g3")
	require.NoError(t, err)
	require.Equal(t, "g3", g.ID)
}
