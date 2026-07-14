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

	// Search finds the mod.
	res, err := src.Search(ctx, sourceSearchQuery("backpack"))
	require.NoError(t, err)
	require.Len(t, res.Mods, 1)
	mod := res.Mods[0]

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

func sourceSearchQuery(q string) source.SearchQuery {
	return source.SearchQuery{Query: q, PageSize: 20}
}
