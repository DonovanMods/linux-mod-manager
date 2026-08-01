package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoModSource is a minimal ModSource returning a fixed two-mod result set,
// so search's results table has both an installed and a not-installed row.
type twoModSource struct{ id string }

func (s *twoModSource) ID() string      { return s.id }
func (s *twoModSource) Name() string    { return s.id }
func (s *twoModSource) AuthURL() string { return "" }
func (s *twoModSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, nil
}
func (s *twoModSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{
		Mods: []domain.Mod{
			{ID: "m1", SourceID: s.id, Name: "Installed Mod"},
			{ID: "m2", SourceID: s.id, Name: "Not Installed Mod"},
		},
		TotalCount: 2,
	}, nil
}
func (s *twoModSource) GetMod(context.Context, string, string) (*domain.Mod, error) { return nil, nil }
func (s *twoModSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *twoModSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (s *twoModSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (s *twoModSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// setupSearchColorTest wires a real core.Service around a twoModSource, with
// "m1" already installed - so doSearch's real "already installed" and table
// rendering code paths run end to end.
func setupSearchColorTest(t *testing.T) (*core.Service, *domain.Game) {
	t.Helper()
	spy := &twoModSource{id: "src"}
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(spy)

	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: t.TempDir(),
		SourceIDs: map[string]string{spy.id: ""},
	}
	require.NoError(t, svc.AddGame(game))

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: "m1", SourceID: spy.id, Name: "Installed Mod", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "default", domain.ModReference{SourceID: spy.id, ModID: "m1", Version: "1.0"}))

	withSearchFlags(t, spy.id, 10)
	return svc, game
}

func TestDoSearch_InstalledMarker_PlainWhenColorDisabled(t *testing.T) {
	svc, game := setupSearchColorTest(t)

	out := captureStdout(t, func() error {
		return doSearch(context.Background(), svc, game, []string{"query"})
	})

	assert.NotContains(t, out, "\x1b[")
	assert.Contains(t, out, "[installed]")
}

func TestDoSearch_InstalledMarker_GreenWhenTTY_AlignmentUnaffected(t *testing.T) {
	svc, game := setupSearchColorTest(t)
	resetColorFlags(t)

	withColorCapableStdout(t, false)
	plain := captureStdout(t, func() error {
		return doSearch(context.Background(), svc, game, []string{"query"})
	})

	withColorCapableStdout(t, true)
	colored := captureStdout(t, func() error {
		return doSearch(context.Background(), svc, game, []string{"query"})
	})

	assert.Contains(t, colored, ansiGreen+"[installed]"+ansiReset)
	assert.Contains(t, colored, ansiBold, "header line should be bolded")
	assert.Equal(t, plain, stripANSI(colored), "color must not change the visible text or alignment")
}
