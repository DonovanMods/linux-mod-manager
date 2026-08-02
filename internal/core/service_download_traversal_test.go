package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// TestDownloadModToCache_TraversalFileName_SanitizedAgainstEscape is the
// download-path sibling of updater_test.go's ApplyRecompile traversal test
// (#196 review): DownloadModToCache is the MAIN download path, and its
// DownloadableFile.FileName is exactly as source-controlled as the
// redownload fallback's - a malicious or buggy source declaring a FileName
// like "../evil.zip" must never be able to write outside the intended
// staging/cache directories.
//
// DeployCopy exercises BOTH vulnerable joins in one call: the download's
// own archivePath (tempDir) and the copy-mode destPath (stagePath) that
// lands the file in the cache.
func TestDownloadModToCache_TraversalFileName_SanitizedAgainstEscape(t *testing.T) {
	dataDir := t.TempDir()
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := newMockSourceWithDownloads("test")
	defer mock.Close()
	svc.RegisterSource(mock)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: filepath.Join(t.TempDir(), "mods"), DeployMode: domain.DeployCopy}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "123", SourceID: "test", Name: "Evil Mod", Version: "1.0.0", GameID: "testgame"}
	file := &domain.DownloadableFile{ID: "file1", Name: "Evil File", FileName: "../evil-traversal.zip"}
	mock.AddDownload(file.ID, []byte("payload"))

	result, err := svc.DownloadMod(context.Background(), "test", game, mod, file, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.FilesExtracted)

	// newStagingDir("lmm-download-*") creates its scratch dir directly under
	// dataDir/downloads (Service.stagingRoot) - an UNSANITIZED
	// filepath.Join(tempDir, "../evil-traversal.zip") climbs exactly one
	// level out of that scratch dir, landing at dataDir/downloads/
	// evil-traversal.zip. That parent is never removed (only tempDir itself
	// is), so an escaped write would persist right here.
	escapedPath := filepath.Join(dataDir, "downloads", "evil-traversal.zip")
	_, statErr := os.Stat(escapedPath)
	require.True(t, os.IsNotExist(statErr), "a traversal filename must never write outside the staging tempDir")

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.Equal(t, []string{"evil-traversal.zip"}, files, "the sanitized (Base'd) filename is what must actually land in the cache")
}

// TestDownloadMod_DeployCompile_TraversalFileID_SanitizedAgainstEscape
// covers the #197-era equivalent of the #196-review site above: the
// DeployCompile branch now retains the .exmodz under
// cache.RetainedSourceName(file.ID) instead of compiling a per-mod pak
// named from file.FileName - file.ID is exactly as source-controlled as
// FileName was, so a traversal payload in the ID (e.g. "../evil-id") must
// not survive into the retained source's on-disk name either.
func TestDownloadMod_DeployCompile_TraversalFileID_SanitizedAgainstEscape(t *testing.T) {
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fake-exmodz-bytes"))
	}))
	defer dlSrv.Close()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	cacheDir := t.TempDir()
	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: cacheDir}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &fakeCompilerSource{downloadURL: dlSrv.URL}
	svc.RegisterSource(src)

	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "3.3"}
	file := &domain.DownloadableFile{ID: "../evil-traversal-id", FileName: "Bear_Mount.exmodz"}

	result, err := svc.DownloadMod(context.Background(), "fake-compiler", game, mod, file, nil)
	require.NoError(t, err)
	require.Equal(t, 0, result.FilesExtracted, "#197: DeployCompile ingest retains only, no per-mod deployment member")

	// The mod's own cache dir is cacheDir/icarus/fake-compiler-bear-mount/3.3
	// - an unsanitized "../evil-traversal-id" fileID would climb into
	// fake-compiler-bear-mount/ (one level up from the version dir).
	gameCache := svc.GetGameCache(game)
	escapedPath := filepath.Join(gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version), "..", "evil-traversal-id")
	_, statErr := os.Stat(escapedPath)
	require.True(t, os.IsNotExist(statErr), "a traversal fileID's retained source must never escape the version directory")

	retainedPath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName(file.ID))
	data, err := os.ReadFile(retainedPath)
	require.NoError(t, err, "the sanitized (Base'd) retained source must actually land inside the version directory")
	require.Equal(t, "fake-exmodz-bytes", string(data))
}
