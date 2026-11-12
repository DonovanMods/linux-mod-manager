package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #606: the composition root hands core the shipped Steam catalog, so a
// Skyrim SE already in games.yaml without `adapter: bethesda` is offered it.
func TestRegisterCatalogAdapters_SuggestsTheCatalogAdapter(t *testing.T) {
	svc := newTestService(t)
	RegisterAdapters(svc)
	RegisterCatalogAdapters(svc, t.TempDir())

	game := &domain.Game{ID: "skyrim", InstallPath: t.TempDir(), Launch: &domain.GameLaunch{SteamAppID: "489830"}}
	assert.Equal(t, "bethesda", svc.SuggestedAdapter(game))

	other := &domain.Game{ID: "witcher", InstallPath: t.TempDir(), Launch: &domain.GameLaunch{SteamAppID: "292030"}}
	assert.Empty(t, svc.SuggestedAdapter(other), "a catalog entry with no adapter suggests nothing")
}

// A user's own steam-games.yaml overrides the catalog here as it does for
// detection, and one that cannot be read suggests nothing rather than
// failing anything.
func TestRegisterCatalogAdapters_ReadsTheUsersOverride(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "steam-games.yaml"),
		[]byte("\"489830\":\n  slug: skyrim-se\n  name: Skyrim Special Edition\n  nexus_id: skyrimspecialedition\n  mod_path: Data\n"), 0o644))
	svc := newTestService(t)
	RegisterAdapters(svc)
	RegisterCatalogAdapters(svc, dir)
	game := &domain.Game{ID: "skyrim", InstallPath: t.TempDir(), Launch: &domain.GameLaunch{SteamAppID: "489830"}}
	assert.Empty(t, svc.SuggestedAdapter(game), "the user's entry names no adapter")

	bad := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bad, "steam-games.yaml"), []byte(":::not yaml"), 0o644))
	svc2 := newTestService(t)
	RegisterAdapters(svc2)
	RegisterCatalogAdapters(svc2, bad)
	assert.Empty(t, svc2.SuggestedAdapter(game))
}
