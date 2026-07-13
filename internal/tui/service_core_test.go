package tui_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/tui"
)

// stubSource implements source.ModSource with canned search results.
// Only Search and identity methods matter; the rest are unreachable in
// these tests.
type stubSource struct {
	result source.SearchResult
	err    error
}

func (s *stubSource) ID() string      { return "stub" }
func (s *stubSource) Name() string    { return "Stub Source" }
func (s *stubSource) AuthURL() string { return "" }
func (s *stubSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, errors.New("not implemented")
}
func (s *stubSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return s.result, s.err
}
func (s *stubSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, errors.New("not implemented")
}
func (s *stubSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, errors.New("not implemented")
}
func (s *stubSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, errors.New("not implemented")
}
func (s *stubSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", errors.New("not implemented")
}
func (s *stubSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, errors.New("not implemented")
}

func newCoreProviderFixture(t *testing.T) (tui.DataProvider, *core.Service, *domain.Game) {
	t.Helper()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID:          "test-game",
		Name:        "Test Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "101",
			SourceID: "nexusmods",
			GameID:   game.ID,
			Name:     "SkyUI",
			Author:   "schlangster",
			Version:  "5.2",
		},
		ProfileName: "default",
		Enabled:     true,
		Deployed:    true,
	}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "102",
			SourceID: "nexusmods",
			GameID:   game.ID,
			Name:     "USSEP",
			Author:   "Arthmoor",
			Version:  "4.3",
		},
		ProfileName: "default",
		Enabled:     false,
	}))

	return tui.NewCoreProvider(svc, game, "default"), svc, game
}

func TestCoreProviderOverview(t *testing.T) {
	provider, _, _ := newCoreProviderFixture(t)

	summary, mods, err := provider.Overview(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Test Game", summary.GameName)
	require.Equal(t, "default", summary.ProfileName)
	require.Equal(t, 2, summary.Installed)
	require.Equal(t, 1, summary.Enabled)
	require.Equal(t, -1, summary.Updates, "updates are unknown until an update check runs")
	require.Equal(t, -1, summary.Conflicts, "conflicts are unknown in the read-only phase")

	require.Len(t, mods, 2)
	byName := map[string]tui.ModItem{}
	for _, m := range mods {
		byName[m.Name] = m
	}
	require.Equal(t, "deployed", byName["SkyUI"].Status)
	require.Equal(t, "nexusmods", byName["SkyUI"].Source)
	require.Equal(t, "5.2", byName["SkyUI"].Version)
	require.Equal(t, "disabled", byName["USSEP"].Status)
}

func TestCoreProviderProfiles(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "hardcore")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "hardcore", domain.ModReference{SourceID: "nexusmods", ModID: "101"}))

	profiles, err := provider.Profiles(context.Background())
	require.NoError(t, err)
	require.Len(t, profiles, 2)

	byName := map[string]tui.ProfileItem{}
	for _, p := range profiles {
		byName[p.Name] = p
	}
	require.True(t, byName["default"].Active)
	require.False(t, byName["hardcore"].Active)
	require.Equal(t, 1, byName["hardcore"].ModCount, "ModCount should map from profile YAML mods, not the DB")
}

func TestCoreProviderSourcesAreSortedGameSources(t *testing.T) {
	provider, _, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"nexusmods": "testgame", "curseforge": "testgame"}

	require.Equal(t, []string{"curseforge", "nexusmods"}, provider.Sources())
}

func TestCoreProviderSearchMarksInstalled(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"stub": "testgame"}
	svc.RegisterSource(&stubSource{result: source.SearchResult{
		Mods: []domain.Mod{
			{ID: "101", SourceID: "stub", Name: "SkyUI-Stub", Author: "a", Version: "5.2"},
			{ID: "999", SourceID: "stub", Name: "NewMod", Author: "b", Version: "1.0"},
		},
		TotalCount: 2, Page: 0, PageSize: 10,
	}})
	// Fixture installed mod 101 under sourceID "nexusmods"; install one under "stub" too:
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:         domain.Mod{ID: "101", SourceID: "stub", GameID: game.ID, Name: "SkyUI-Stub", Version: "5.2"},
		ProfileName: "default", Enabled: true,
	}))

	page, err := provider.Search(context.Background(), "stub", "sky", 0)
	require.NoError(t, err)
	require.Equal(t, "sky", page.Query)
	require.Equal(t, "stub", page.Source)
	require.Equal(t, 2, page.TotalCount)
	require.Len(t, page.Results, 2)

	byName := map[string]tui.ModItem{}
	for _, r := range page.Results {
		byName[r.Name] = r
	}
	require.Equal(t, "installed", byName["SkyUI-Stub"].Status)
	require.Equal(t, "available", byName["NewMod"].Status)
}

func TestCoreProviderSearchPropagatesAuthRequired(t *testing.T) {
	provider, svc, game := newCoreProviderFixture(t)
	game.SourceIDs = map[string]string{"stub": "testgame"}
	svc.RegisterSource(&stubSource{err: fmt.Errorf("%w: key required", domain.ErrAuthRequired)})

	_, err := provider.Search(context.Background(), "stub", "x", 0)
	require.ErrorIs(t, err, domain.ErrAuthRequired)
}
