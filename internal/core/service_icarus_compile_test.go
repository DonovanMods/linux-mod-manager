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
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/stretchr/testify/require"
)

// fakeCompilerSource is a minimal ModSource that also implements
// source.Compiler, standing in for internal/source/icarus.Icarus (Tasks
// 8/13) without pulling that package into internal/core's tests — this test
// only needs to prove Service invokes Compile when DeployMode is
// DeployCompile, which Task 12 already tests in isolation.
type fakeCompilerSource struct {
	downloadURL  string
	compileCalls int
}

func (s *fakeCompilerSource) ID() string      { return "fake-compiler" }
func (s *fakeCompilerSource) Name() string    { return "Fake Compiler Source" }
func (s *fakeCompilerSource) AuthURL() string { return "" }
func (s *fakeCompilerSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, source.ErrNotSupported
}
func (s *fakeCompilerSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return s.downloadURL, nil
}
func (s *fakeCompilerSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, source.ErrNotSupported
}

// Compile implements source.Compiler by copying the downloaded source file
// through unchanged — this test only asserts Service invoked it with the
// right arguments and used its output, not that it performs real PAK
// compilation (Task 12 covers that).
func (s *fakeCompilerSource) Compile(ctx context.Context, basePakPath, baseDataPath, sourceFilePath, outputPath string) error {
	s.compileCalls++
	data, err := os.ReadFile(sourceFilePath)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, data, 0o644)
}

var (
	_ source.ModSource = (*fakeCompilerSource)(nil)
	_ source.Compiler  = (*fakeCompilerSource)(nil)
)

func TestDownloadMod_DeployCompile_InvokesCompiler(t *testing.T) {
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fake-exmodz-bytes"))
	}))
	defer dlSrv.Close()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	require.NoError(t, os.WriteFile(basePak, []byte("fake-base-pak"), 0o644))

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &fakeCompilerSource{downloadURL: dlSrv.URL}
	svc.RegisterSource(src)

	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "3.3"}
	file := &domain.DownloadableFile{ID: "exmodz", FileName: "Bear_Mount.exmodz"}

	result, err := svc.DownloadMod(context.Background(), "fake-compiler", game, mod, file, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.FilesExtracted)
	require.Equal(t, 1, src.compileCalls)

	gameCache := svc.GetGameCache(game)
	require.True(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version))
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "Bear_Mount_P.pak", files[0])

	data, err := os.ReadFile(gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, files[0]))
	require.NoError(t, err)
	require.Equal(t, "fake-exmodz-bytes", string(data))
}
