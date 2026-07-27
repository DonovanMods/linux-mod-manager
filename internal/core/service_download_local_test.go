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

// TestIngestLocalToCacheArchiveCopyModeUsesDeclaredFileName is a regression
// test for #52 item 12: ingestLocalToCache's copy-mode branch names the
// cached file filepath.Base(localPath) - the TEMP file's name - instead of
// file.FileName, the caller's declared name. In practice these usually
// match (the caller derives file.FileName from the same path), which is why
// TestIngestLocalToCacheArchiveCopyMode above never caught it; this test
// deliberately mismatches them, so the cached file name must come from the
// declared file.FileName, not the source path's basename.
func TestIngestLocalToCacheArchiveCopyModeUsesDeclaredFileName(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	// The on-disk temp file is named differently from what the source
	// declared as this file's name.
	tempFile := filepath.Join(t.TempDir(), "tmp-download-xyz.bin")
	require.NoError(t, os.WriteFile(tempFile, []byte("zipbytes"), 0644))

	game := &domain.Game{ID: "hytale", DeployMode: domain.DeployCopy}
	mod := &domain.Mod{ID: "coolmod-2.0", SourceID: "my-mods", Version: "2.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "declared.zip"}

	result, err := svc.ingestLocalToCache(gameCache, game, mod, file, tempFile)
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)

	declaredPath := gameCache.GetFilePath("hytale", "my-mods", "coolmod-2.0", "2.0", "declared.zip")
	_, err = os.Stat(declaredPath)
	assert.NoError(t, err, "cached file must be named after the declared file.FileName, not the temp path's basename")

	staleBasenamePath := gameCache.GetFilePath("hytale", "my-mods", "coolmod-2.0", "2.0", "tmp-download-xyz.bin")
	_, err = os.Stat(staleBasenamePath)
	assert.True(t, os.IsNotExist(err), "cached file must NOT be named after localPath's basename when file.FileName is declared")
}

func TestIngestLocalToCacheMissingPath(t *testing.T) {
	svc, gameCache := newLocalIngestService(t)

	game := &domain.Game{ID: "7dtd"}
	mod := &domain.Mod{ID: "x", SourceID: "my-mods", Version: "1.0"}
	file := &domain.DownloadableFile{ID: "main", FileName: "x"}

	_, err := svc.ingestLocalToCache(gameCache, game, mod, file, filepath.Join(t.TempDir(), "gone"))
	assert.Error(t, err)
}
