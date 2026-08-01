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
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
	"github.com/stretchr/testify/require"
)

// basePakIndexHash opens path and returns its footer IndexHash - the same
// value Service's compile branches record, computed independently here so
// tests can assert against it without depending on internal/core internals.
func basePakIndexHash(t *testing.T, path string) string {
	t.Helper()
	r, err := unrealpak.Open(path)
	require.NoError(t, err)
	defer r.Close() //nolint:errcheck
	return r.IndexHash()
}

// TestDownloadMod_DeployCompile_RecordsBaseIndexHashAndRetainedSource pins
// #196 design points 1-2 for the DOWNLOAD compile path: compiling an
// .exmodz must record the base pak's IndexHash under the file's real
// DownloadableFile.ID, retain the original .exmodz bytes beside the
// compiled pak, and keep both out of ListFiles/deploy.
func TestDownloadMod_DeployCompile_RecordsBaseIndexHashAndRetainedSource(t *testing.T) {
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("original-exmodz-bytes"))
	}))
	defer dlSrv.Close()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)
	wantHash := basePakIndexHash(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &fakeCompilerSource{downloadURL: dlSrv.URL}
	svc.RegisterSource(src)

	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "3.3"}
	file := &domain.DownloadableFile{ID: "exmodz-file-id", FileName: "Bear_Mount.exmodz"}

	_, err = svc.DownloadMod(context.Background(), "fake-compiler", game, mod, file, nil)
	require.NoError(t, err)

	gameCache := svc.GetGameCache(game)

	hashes, err := gameCache.BaseIndexHashes(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"exmodz-file-id": wantHash}, hashes)

	retainedPath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName("exmodz-file-id"))
	retainedData, err := os.ReadFile(retainedPath)
	require.NoError(t, err)
	require.Equal(t, "original-exmodz-bytes", string(retainedData))

	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.Equal(t, []string{"Bear_Mount_P.pak"}, files, "retained source and base-index marker must never be deployable content")
}

// TestImportMod_DeployCompile_RecordsBaseIndexHashAndRetainedSource mirrors
// the above for the IMPORT compile path (keyed by the compiled output's own
// filename, since Import has no real DownloadableFile.ID available at
// compile time - see stageCompileFingerprint's doc comment).
func TestImportMod_DeployCompile_RecordsBaseIndexHashAndRetainedSource(t *testing.T) {
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)
	wantHash := basePakIndexHash(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &fakeCompilerSource{}
	svc.RegisterSource(src)

	game := &domain.Game{
		ID:          "icarus",
		InstallPath: installDir,
		ModPath:     t.TempDir(),
		DeployMode:  domain.DeployCompile,
		SourceIDs:   map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))

	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "Bear_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("original-exmodz-bytes"), 0o644))

	importer := svc.NewImporter(game)
	result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.NoError(t, err)

	gameCache := svc.GetGameCache(game)

	hashes, err := gameCache.BaseIndexHashes(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"Bear_Mount_P.pak": wantHash}, hashes)

	retainedPath := gameCache.GetFilePath(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version, cache.RetainedSourceName("Bear_Mount_P.pak"))
	retainedData, err := os.ReadFile(retainedPath)
	require.NoError(t, err)
	require.Equal(t, "original-exmodz-bytes", string(retainedData))

	files, err := gameCache.ListFiles(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
	require.NoError(t, err)
	require.Equal(t, []string{"Bear_Mount_P.pak"}, files, "retained source and base-index marker must never be deployable content")
}
