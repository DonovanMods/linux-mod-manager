package core_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
	"github.com/stretchr/testify/require"
)

// writeFakeBasePak writes a real, minimal but VALID pak at path (#196: the
// compile branch now opens the base pak itself to read its footer IndexHash
// as a compile fingerprint, so a bare byte-stub file - fine when only the
// fake compiler ever touched this path - no longer parses). One tiny table
// entry is enough; its content is never asserted on by these tests.
func writeFakeBasePak(t *testing.T, path string) {
	t.Helper()
	w, err := unrealpak.Create(path)
	require.NoError(t, err)
	require.NoError(t, w.AddFile("Data/D_Fixture.json", []byte(`{"fixture":true}`)))
	require.NoError(t, w.Close())
}

// fakeCompilerSource is a minimal ModSource that also implements
// source.MergeCompiler, standing in for internal/source/icarus.Icarus
// without pulling that package into internal/core's tests — this test only
// needs to prove Service validates and retains (never compiles a per-mod
// pak) when DeployMode is DeployCompile (#197: merged-only).
type fakeCompilerSource struct {
	downloadURL   string
	compileCalls  int
	validateCalls int
	mergeWarnings []string
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

// ValidateSource implements source.MergeCompiler by confirming the archive
// exists — this test only asserts Service invoked it, not that it performs
// real .exmodz parsing (Task 1 covers that in the icarus package itself).
func (s *fakeCompilerSource) ValidateSource(sourceFilePath string) error {
	s.validateCalls++
	if _, err := os.Stat(sourceFilePath); err != nil {
		return err
	}
	return nil
}

// MergeCompile implements source.MergeCompiler by concatenating every
// source's bytes - enough for tests to distinguish "which sources were
// actually merged" without needing a real base pak table to patch.
func (s *fakeCompilerSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, []source.MergeFailure, error) {
	s.compileCalls++
	var out []byte
	for _, src := range sources {
		data, err := os.ReadFile(src.SourcePath)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, data...)
	}
	return s.mergeWarnings, nil, os.WriteFile(outputPath, out, 0o644)
}

var (
	_ source.ModSource     = (*fakeCompilerSource)(nil)
	_ source.MergeCompiler = (*fakeCompilerSource)(nil)
)

// writeFakeBasePakWithTable is writeFakeBasePak's table-content-controlling
// variant - needed to simulate a base pak refresh (a new IndexHash) for
// staleness tests.
func writeFakeBasePakWithTable(t *testing.T, path string, tables map[string][]byte) {
	t.Helper()
	w, err := unrealpak.Create(path)
	require.NoError(t, err)
	for mountPath, data := range tables {
		require.NoError(t, w.AddFile(mountPath, data))
	}
	require.NoError(t, w.Close())
}

// failingValidateCompilerSource wraps fakeCompilerSource and always fails
// ValidateSource - simulates a corrupt/malformed downloaded .exmodz.
type failingValidateCompilerSource struct {
	*fakeCompilerSource
}

func (s *failingValidateCompilerSource) ValidateSource(sourceFilePath string) error {
	return fmt.Errorf("boom: not a valid .EXMODZ")
}

// TestDownloadMod_DeployCompile_ValidatesAndRetainsNoPerModPak proves the
// #197 merged-only ingest contract: a downloaded .exmodz is validated and
// its bytes retained, but no per-mod pak is compiled or deployed - the
// merged pak (a separate, profile-level cache entry) is what actually
// deploys.
func TestDownloadMod_DeployCompile_ValidatesAndRetainsNoPerModPak(t *testing.T) {
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fake-exmodz-bytes"))
	}))
	defer dlSrv.Close()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

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
	require.Equal(t, 1, src.validateCalls, "ingest must validate the .exmodz")
	require.Equal(t, 0, src.compileCalls, "ingest must NOT compile a per-mod pak (#197: merged-only)")
	require.Equal(t, 0, result.FilesExtracted, "a per-mod exmodz cache entry has no deployment members under the merged-only model")

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.Empty(t, files, "ListFiles must report zero deployment members - the retained source is reserved, not a member")

	retainedPath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName(file.ID))
	data, err := os.ReadFile(retainedPath)
	require.NoError(t, err)
	require.Equal(t, "fake-exmodz-bytes", string(data), "the original .exmodz bytes must still be retained")
}

// TestDownloadMod_DeployCompile_MalformedExmodz_FailsLoudAtIngest proves
// validation happens at ingest time, not deferred to the next merge.
func TestDownloadMod_DeployCompile_MalformedExmodz_FailsLoudAtIngest(t *testing.T) {
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-a-valid-exmodz"))
	}))
	defer dlSrv.Close()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &failingValidateCompilerSource{fakeCompilerSource: &fakeCompilerSource{downloadURL: dlSrv.URL}}
	svc.RegisterSource(src)

	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "bad-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "1.0"}
	file := &domain.DownloadableFile{ID: "exmodz", FileName: "Bad_Mount.exmodz"}

	_, err = svc.DownloadMod(context.Background(), "fake-compiler", game, mod, file, nil)
	require.Error(t, err)

	gameCache := svc.GetGameCache(game)
	require.False(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version), "a validation failure must leave no cache entry")
}
