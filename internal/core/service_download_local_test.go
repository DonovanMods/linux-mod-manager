package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLocalIngestService(t *testing.T) (*Service, *cache.Cache) {
	t.Helper()
	svc := &Service{extractor: NewExtractor()}
	return svc, cache.New(t.TempDir())
}

func TestIngestLocalToCacheDirectory(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	modDir := filepath.Join(t.TempDir(), "BiggerBackpack")
	require.NoError(t, os.MkdirAll(filepath.Join(modDir, "Config"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "ModInfo.xml"), []byte("<xml/>"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "Config", "items.xml"), []byte("<items/>"), 0644))

	game := &domain.Game{ID: "7dtd", DeployMode: domain.DeployExtract}
	mod := &domain.Mod{ID: "BiggerBackpack", SourceID: "my-mods", Version: "1.2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "BiggerBackpack"}

	result, err := svc.ingestLocalToCache(gameCache, game, mod, file, modDir)
	require.NoError(t, err)
	assert.Equal(t, 2, result.FilesExtracted)
	assert.Empty(t, result.Checksum)

	files, err := gameCache.ListFiles("7dtd", "my-mods", "BiggerBackpack", "1.2.0")
	require.NoError(t, err)
	assert.Len(t, files, 2)
}

func TestIngestLocalToCacheArchiveCopyMode(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	archive := filepath.Join(t.TempDir(), "coolmod-2.0.zip")
	require.NoError(t, os.WriteFile(archive, []byte("zipbytes"), 0644))

	game := &domain.Game{ID: "hytale", DeployMode: domain.DeployCopy}
	mod := &domain.Mod{ID: "coolmod-2.0", SourceID: "my-mods", Version: "2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "coolmod-2.0.zip"}

	result, err := svc.ingestLocalToCache(gameCache, game, mod, file, archive)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)

	cached := gameCache.GetFilePath("hytale", "my-mods", "coolmod-2.0", "2.0", "coolmod-2.0.zip")
	_, err = os.Stat(cached)
	assert.NoError(t, err)
}

func TestIngestLocalToCacheMissingPath(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	game := &domain.Game{ID: "7dtd"}
	mod := &domain.Mod{ID: "x", SourceID: "my-mods", Version: "1.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "x"}

	_, err := svc.ingestLocalToCache(gameCache, game, mod, file, filepath.Join(t.TempDir(), "gone"))
	assert.Error(t, err)
}
