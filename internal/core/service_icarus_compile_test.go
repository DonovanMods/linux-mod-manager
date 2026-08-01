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

// newCompileTestGame builds a DeployCompile game backed by fakeCompilerSource,
// serving dlBody for every download - shared setup for
// TestDownloadMod_DeployCompile_RoutesPerFile's cases.
func newCompileTestGame(t *testing.T, dlBody string) (*core.Service, *fakeCompilerSource, *domain.Game) {
	t.Helper()

	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(dlBody))
	}))
	t.Cleanup(dlSrv.Close)

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

	return svc, src, game
}

// TestDownloadMod_DeployCompile_RoutesPerFile pins the fix-round-1 gap: a
// DeployCompile game's compile branch must key off the FILE (".exmodz"
// suffix, case-insensitive), not the game alone - icarus.GetModFiles can
// serve a mod's already-built ".pak" alongside its ".exmodz" diff, and a pak
// routed into Compile fails (it isn't a zip ParseExmodz can read).
func TestDownloadMod_DeployCompile_RoutesPerFile(t *testing.T) {
	tests := []struct {
		name           string
		fileName       string
		wantCompiled   bool
		wantCachedName string
	}{
		{"exmodz file takes the compile branch", "Bear_Mount.exmodz", true, "Bear_Mount_P.pak"},
		{"EXMODZ file takes the compile branch case-insensitively", "Bear_Mount.EXMODZ", true, "Bear_Mount_P.pak"},
		{"pak file skips the compiler entirely", "Bear_Mount.pak", false, "Bear_Mount.pak"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const body = "fake-download-bytes"
			svc, src, game := newCompileTestGame(t, body)

			mod := &domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "3.3"}
			file := &domain.DownloadableFile{ID: "the-file", FileName: tt.fileName}

			result, err := svc.DownloadMod(context.Background(), "fake-compiler", game, mod, file, nil)
			require.NoError(t, err)
			require.Equal(t, 1, result.FilesExtracted)

			wantCompileCalls := 0
			if tt.wantCompiled {
				wantCompileCalls = 1
			}
			require.Equal(t, wantCompileCalls, src.compileCalls)

			gameCache := svc.GetGameCache(game)
			files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
			require.NoError(t, err)
			require.Equal(t, []string{tt.wantCachedName}, files)

			data, err := os.ReadFile(gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, tt.wantCachedName))
			require.NoError(t, err)
			require.Equal(t, body, string(data))
		})
	}

	// Regression proof for the ".pak" case above: rather than only asserting
	// "Compile wasn't called" (a fake-tautology that would also pass if
	// routing were broken some other way), this proves a DeployCompile game
	// handling a plain ".pak" produces EXACTLY what a DeployExtract game
	// produces for the identical file through the identical source - the
	// genuine pre-Task-13 extract/copy path, byte-for-byte.
	t.Run("pak file on a compile-mode game matches a non-compile game byte-for-byte", func(t *testing.T) {
		const body = "fake-pak-bytes"
		mod := &domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "3.3"}
		file := &domain.DownloadableFile{ID: "pak", FileName: "Bear_Mount.pak"}

		compileSvc, compileSrc, compileGame := newCompileTestGame(t, body)
		compileResult, err := compileSvc.DownloadMod(context.Background(), "fake-compiler", compileGame, mod, file, nil)
		require.NoError(t, err)

		extractSvc, extractSrc, extractGame := newCompileTestGame(t, body)
		extractGame.DeployMode = domain.DeployExtract
		extractResult, err := extractSvc.DownloadMod(context.Background(), "fake-compiler", extractGame, mod, file, nil)
		require.NoError(t, err)

		require.Equal(t, 0, compileSrc.compileCalls)
		require.Equal(t, 0, extractSrc.compileCalls)
		require.Equal(t, extractResult, compileResult)

		compileFiles, err := compileSvc.GetGameCache(compileGame).ListFiles(compileGame.ID, mod.SourceID, mod.ID, mod.Version)
		require.NoError(t, err)
		extractFiles, err := extractSvc.GetGameCache(extractGame).ListFiles(extractGame.ID, mod.SourceID, mod.ID, mod.Version)
		require.NoError(t, err)
		require.Equal(t, extractFiles, compileFiles)

		compileData, err := os.ReadFile(compileSvc.GetGameCache(compileGame).GetFilePath(compileGame.ID, mod.SourceID, mod.ID, mod.Version, compileFiles[0]))
		require.NoError(t, err)
		extractData, err := os.ReadFile(extractSvc.GetGameCache(extractGame).GetFilePath(extractGame.ID, mod.SourceID, mod.ID, mod.Version, extractFiles[0]))
		require.NoError(t, err)
		require.Equal(t, extractData, compileData)
	})
}

// TestDownloadMod_DeployCompile_MixedFileMod pins that a single mod shipping
// both a prebuilt ".pak" and an ".exmodz" diff (icarus.GetModFiles's "pak"
// then "exmodz" enumeration, neither marked primary when both are present)
// gets each file routed independently within the same DeployCompile game:
// one DownloadMod call per DownloadableFile, exactly as the real CLI/TUI
// download flow drives it.
func TestDownloadMod_DeployCompile_MixedFileMod(t *testing.T) {
	svc, src, game := newCompileTestGame(t, "fake-bytes")
	mod := &domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "3.3"}

	exmodzFile := &domain.DownloadableFile{ID: "exmodz", FileName: "Bear_Mount.exmodz"}
	pakFile := &domain.DownloadableFile{ID: "pak", FileName: "Bear_Mount.pak"}

	_, err := svc.DownloadMod(context.Background(), "fake-compiler", game, mod, exmodzFile, nil)
	require.NoError(t, err)
	require.Equal(t, 1, src.compileCalls, "exmodz file must compile")

	_, err = svc.DownloadMod(context.Background(), "fake-compiler", game, mod, pakFile, nil)
	require.NoError(t, err)
	require.Equal(t, 1, src.compileCalls, "pak file must not trigger a second compile")

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"Bear_Mount_P.pak", "Bear_Mount.pak"}, files,
		"both the compiled exmodz output and the untouched pak must be cached")
}
