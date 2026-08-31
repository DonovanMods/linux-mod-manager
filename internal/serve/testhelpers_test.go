package serve_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/require"
)

// newFixtureService returns a *core.Service backed by fresh temp dirs, with
// one game (ID "g1", name "Fixture Game") registered - the seeded fixture
// every internal/serve page test renders against, matching the construction
// pattern internal/core's own tests use (see core's testhelpers_test.go).
func newFixtureService(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:          "g1",
		Name:        "Fixture Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
	}))
	return svc
}

// newFixtureServiceNoGames is newFixtureService without the seeded game -
// the "nothing configured yet" case every page must still render cleanly.
func newFixtureServiceNoGames(t *testing.T) *core.Service {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}
