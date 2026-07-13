package tui_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/tui"
)

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

func TestCoreProviderSummary(t *testing.T) {
	provider, _, _ := newCoreProviderFixture(t)

	summary, err := provider.Summary(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Test Game", summary.GameName)
	require.Equal(t, "default", summary.ProfileName)
	require.Equal(t, 2, summary.Installed)
	require.Equal(t, 1, summary.Enabled)
	require.Equal(t, -1, summary.Updates, "updates are unknown until an update check runs")
	require.Equal(t, -1, summary.Conflicts, "conflicts are unknown in the read-only phase")
}

func TestCoreProviderInstalledMods(t *testing.T) {
	provider, _, _ := newCoreProviderFixture(t)

	mods, err := provider.InstalledMods(context.Background())
	require.NoError(t, err)
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

func TestCoreProviderSearchResultsAreEmptyUntilPhase4(t *testing.T) {
	provider, _, _ := newCoreProviderFixture(t)

	results, err := provider.SearchResults(context.Background())
	require.NoError(t, err)
	require.Empty(t, results)
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
