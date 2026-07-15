package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectorySourceEndToEnd(t *testing.T) {
	// A modlets directory with one mod.
	root := t.TempDir()
	modDir := filepath.Join(root, "BiggerBackpack")
	require.NoError(t, os.MkdirAll(modDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "ModInfo.xml"), []byte(
		`<?xml version="1.0"?><xml><Name value="BiggerBackpack"/><Version value="1.2.0"/></xml>`), 0644))

	src, err := custom.New(custom.SourceDefinition{
		ID:        "my-mods",
		Name:      "My Mods",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: root},
	})
	require.NoError(t, err)

	svc := &Service{extractor: NewExtractor()}
	gameCache := cache.New(t.TempDir())
	game := &domain.Game{ID: "7dtd", DeployMode: domain.DeployExtract}
	ctx := context.Background()

	// Search finds the mod and stamps GameID onto it for downstream identity.
	res, err := src.Search(ctx, sourceSearchQuery("backpack", game.ID))
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	mod := res.Mods[0]
	assert.Equal(t, game.ID, mod.GameID, "directory source must echo the queried GameID onto results")

	// Files + download URL + local ingest land it in the cache.
	files, err := src.GetModFiles(ctx, &mod)
	require.NoError(t, err)
	require.Len(t, files, 1)

	url, err := src.GetDownloadURL(ctx, &mod, files[0].ID)
	require.NoError(t, err)

	result, err := svc.ingestLocalToCache(gameCache, game, &mod, &files[0], url[len("file://"):])
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)
	assert.True(t, gameCache.Exists("7dtd", "my-mods", "BiggerBackpack", "1.2.0"))
}

func sourceSearchQuery(q, gameID string) source.SearchQuery {
	return source.SearchQuery{Query: q, GameID: gameID, PageSize: 20}
}

// TestDirectorySourceGameIDSurvivesEmptySourceMapping is a regression test for
// the orphaned-install bug (final review finding 1): a directory source mapped
// with the README-documented empty value (`sources: {my-mods: ""}`) must not
// blank out the mod's GameID. It exercises both root causes together:
// Directory.Search/GetMod echoing the gameID they're given, and
// Service.SearchMods/GetMod only applying a mapped source-game-ID override when
// it is non-empty. The key assertion is DB visibility: an install-shaped save
// must show up via GetInstalledMods, which filters by game_id.
func TestDirectorySourceGameIDSurvivesEmptySourceMapping(t *testing.T) {
	root := t.TempDir()
	modDir := filepath.Join(root, "BiggerBackpack")
	require.NoError(t, os.MkdirAll(modDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "ModInfo.xml"), []byte(
		`<?xml version="1.0"?><xml><Name value="BiggerBackpack"/><Version value="1.2.0"/></xml>`), 0644))

	svc, err := NewService(ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src, err := custom.New(custom.SourceDefinition{
		ID:        "my-mods",
		Name:      "My Mods",
		Type:      custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: root},
	})
	require.NoError(t, err)
	svc.RegisterSource(src)

	game := &domain.Game{
		ID:         "7dtd",
		Name:       "7 Days to Die",
		SourceIDs:  map[string]string{"my-mods": ""}, // README-documented: directory sources ignore the value
		DeployMode: domain.DeployExtract,
	}
	require.NoError(t, svc.AddGame(game))

	ctx := context.Background()

	// SearchMods must return a mod carrying the lmm game's ID, not the blank override.
	res, err := svc.SearchMods(ctx, "my-mods", game.ID, "backpack", "", nil, 0, 20)
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	assert.Equal(t, game.ID, res.Mods[0].GameID, "SearchMods must not blank GameID via an empty source mapping")

	// GetMod must agree.
	mod, err := svc.GetMod(ctx, "my-mods", game.ID, res.Mods[0].ID)
	require.NoError(t, err)
	assert.Equal(t, game.ID, mod.GameID, "GetMod must not blank GameID via an empty source mapping")

	// The key assertion: an install-shaped save must be visible via GetInstalledMods,
	// which filters by game_id — a blank GameID would silently orphan the row.
	installed := &domain.InstalledMod{Mod: *mod, ProfileName: "default", Enabled: true}
	require.NoError(t, svc.SaveInstalledMod(installed))

	got, err := svc.GetInstalledMods(game.ID, "default")
	require.NoError(t, err)
	require.Len(t, got, 1, "installed mod must be visible under the game's own ID")
	assert.Equal(t, mod.ID, got[0].ID)
}
