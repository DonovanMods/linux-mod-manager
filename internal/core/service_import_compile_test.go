package core_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// failingCompilerSource wraps fakeCompilerSource (defined in
// service_icarus_compile_test.go) and shadows Compile to always fail,
// letting failure-leg tests below prove a mid-compile error never leaves a
// partial artifact behind (#173, mirroring #136 review's "remove partial
// output pak on mid-compile failure" fix for the download path).
type failingCompilerSource struct {
	*fakeCompilerSource
}

func (s *failingCompilerSource) Compile(ctx context.Context, basePakPath, sourceFilePath, outputPath string) error {
	return fmt.Errorf("boom: compile always fails")
}

// raceCompilerSource wraps fakeCompilerSource and, when sabotage is set,
// writes its declared output and then immediately removes it before
// returning success - deterministically reproducing "compile reported
// success, but the artifact is gone by the time it must be staged into the
// cache" without any OS-specific permission tricks. This is the shape of
// the #173 review defect: the compile step itself succeeds, but the
// subsequent step that gets the artifact into the cache can still fail.
type raceCompilerSource struct {
	*fakeCompilerSource
	sabotage bool
}

func (s *raceCompilerSource) Compile(ctx context.Context, basePakPath, sourceFilePath, outputPath string) error {
	s.compileCalls++
	if err := os.WriteFile(outputPath, []byte("new-content"), 0o644); err != nil {
		return err
	}
	if s.sabotage {
		return os.Remove(outputPath)
	}
	return nil
}

// newImportCompileTestGame builds a DeployCompile game with a registered,
// game-mapped compiler source and an installed base pak - the setup #173's
// import path needs to resolve a Compiler the same way
// Service.DownloadModToCache resolves one from the download's own source,
// except import has no per-download source pinned, so it must resolve the
// compiler from the game's registered sources instead (game.SourceIDs).
func newImportCompileTestGame(t *testing.T) (*core.Service, *fakeCompilerSource, *domain.Game) {
	t.Helper()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

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

	return svc, src, game
}

func TestImportMod_DeployCompile_ExmodzCompiles(t *testing.T) {
	svc, src, game := newImportCompileTestGame(t)

	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "Bear_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-exmodz-bytes"), 0o644))

	importer := svc.NewImporter(game)
	result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, result.FilesExtracted)
	require.Equal(t, 1, src.compileCalls)

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
	require.NoError(t, err)
	require.Equal(t, []string{"Bear_Mount_P.pak"}, files)

	data, err := os.ReadFile(gameCache.GetFilePath(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version, files[0]))
	require.NoError(t, err)
	require.Equal(t, "fake-exmodz-bytes", string(data))
}

// TestImportMod_DeployCompile_RoutesPerFile mirrors
// TestDownloadMod_DeployCompile_RoutesPerFile (service_icarus_compile_test.go):
// only a ".exmodz" suffix (case-insensitive) takes the compile branch. A
// plain ".pak" import is untouched by #173 - it falls through to the
// existing extract-mode branch exactly as it did before this change, which
// today means "unsupported archive format" (pak isn't a recognized
// archive), pinned here as a regression proof that non-exmodz import
// behavior is unchanged.
func TestImportMod_DeployCompile_RoutesPerFile(t *testing.T) {
	tests := []struct {
		name            string
		fileName        string
		wantCompiled    bool
		wantCachedName  string
		wantErrContains string
	}{
		{name: "exmodz file takes the compile branch", fileName: "Bear_Mount.exmodz", wantCompiled: true, wantCachedName: "Bear_Mount_P.pak"},
		{name: "EXMODZ file takes the compile branch case-insensitively", fileName: "Bear_Mount.EXMODZ", wantCompiled: true, wantCachedName: "Bear_Mount_P.pak"},
		{name: "pak file is unaffected: today's unsupported-archive error is unchanged", fileName: "Bear_Mount.pak", wantErrContains: "unsupported archive format"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, src, game := newImportCompileTestGame(t)

			tempDir := t.TempDir()
			archivePath := filepath.Join(tempDir, tt.fileName)
			require.NoError(t, os.WriteFile(archivePath, []byte("fake-bytes"), 0o644))

			importer := svc.NewImporter(game)
			result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})

			if tt.wantErrContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErrContains)
				require.Equal(t, 0, src.compileCalls)
				return
			}

			require.NoError(t, err)
			wantCompileCalls := 0
			if tt.wantCompiled {
				wantCompileCalls = 1
			}
			require.Equal(t, wantCompileCalls, src.compileCalls)

			gameCache := svc.GetGameCache(game)
			files, err := gameCache.ListFiles(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
			require.NoError(t, err)
			require.Equal(t, []string{tt.wantCachedName}, files)
		})
	}
}

// TestImportMod_DeployCompile_ZipPassthroughUnaffected proves a regular
// (non-exmodz) archive import for a DeployCompile game is byte-for-byte
// identical to the same import against a DeployExtract game - #173 only
// inserts a new leading branch keyed on isExmodzFile, it must never change
// behavior for anything else.
func TestImportMod_DeployCompile_ZipPassthroughUnaffected(t *testing.T) {
	makeArchive := func(t *testing.T) string {
		t.Helper()
		tempDir := t.TempDir()
		archivePath := filepath.Join(tempDir, "SomeMod.zip")
		createImportTestZip(t, archivePath, map[string]string{"plugin.txt": "test content"})
		return archivePath
	}

	compileSvc, compileSrc, compileGame := newImportCompileTestGame(t)
	compileImporter := compileSvc.NewImporter(compileGame)
	compileResult, err := compileImporter.Import(context.Background(), makeArchive(t), compileGame, core.ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, compileSrc.compileCalls)

	extractSvc, extractSrc, extractGame := newImportCompileTestGame(t)
	extractGame.DeployMode = domain.DeployExtract
	extractImporter := extractSvc.NewImporter(extractGame)
	extractResult, err := extractImporter.Import(context.Background(), makeArchive(t), extractGame, core.ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, extractSrc.compileCalls)

	require.Equal(t, extractResult.FilesExtracted, compileResult.FilesExtracted)
	require.Equal(t, extractResult.Mod.Name, compileResult.Mod.Name)
}

// TestImportMod_DeployCompile_NoCompilerSourceFailsLoud pins the "never
// silently cache an uncompiled .exmodz" requirement (#173): a DeployCompile
// game with no Compiler-capable source mapped in its SourceIDs must fail
// loud with an actionable error instead of falling through to extract/copy.
func TestImportMod_DeployCompile_NoCompilerSourceFailsLoud(t *testing.T) {
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	// No RegisterSource call at all - the game has no source mapped, let
	// alone a Compiler-capable one.
	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "Bear_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-exmodz-bytes"), 0o644))

	importer := svc.NewImporter(game)
	result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "compiler")

	_, statErr := os.Stat(filepath.Join(cfg.CacheDir, game.ID))
	require.True(t, os.IsNotExist(statErr), "no cache entry should have been created")
}

// TestImportMod_DeployCompile_MissingBasePakFailsLoud pins the second
// "fail loud when compilation is impossible" leg (#173): a game whose
// installed base pak is missing must error instead of compiling against
// nothing or silently caching the raw archive.
func TestImportMod_DeployCompile_MissingBasePakFailsLoud(t *testing.T) {
	installDir := t.TempDir() // no Icarus/Content/Data/data.pak written

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
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-exmodz-bytes"), 0o644))

	importer := svc.NewImporter(game)
	result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "base pak")
	require.Equal(t, 0, src.compileCalls)
}

// TestImportMod_DeployCompile_CompileFailureLeavesNoPartialArtifact proves a
// mid-compile failure never lands a partial/uncompiled file in the cache
// (#173 - "never silently cache an uncompiled .exmodz").
func TestImportMod_DeployCompile_CompileFailureLeavesNoPartialArtifact(t *testing.T) {
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &failingCompilerSource{fakeCompilerSource: &fakeCompilerSource{}}
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
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-exmodz-bytes"), 0o644))

	importer := svc.NewImporter(game)
	result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "compiling mod")

	_, statErr := os.Stat(filepath.Join(cfg.CacheDir, game.ID))
	require.True(t, os.IsNotExist(statErr), "no partial cache entry should have been created")
}

// TestImportMod_DeployCompile_StandaloneImporterFailsLoud proves an
// Importer constructed without Service context (core.NewImporter, used
// directly in older tests) still fails loud rather than silently caching an
// uncompiled .exmodz - it simply has no compiler resolver to consult.
func TestImportMod_DeployCompile_StandaloneImporterFailsLoud(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	archivePath := filepath.Join(tempDir, "Bear_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-exmodz-bytes"), 0o644))

	modCache := cache.New(cacheDir)
	game := &domain.Game{ID: "icarus", DeployMode: domain.DeployCompile}

	importer := core.NewImporter(modCache)
	result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.Error(t, err)
	require.Nil(t, result)
	// The message must name the actual cause - a standalone Importer with
	// no service context, not "no compiler-capable source configured" (a
	// different failure covered by TestImportMod_DeployCompile_NoCompilerSourceFailsLoud).
	require.Contains(t, err.Error(), "without service context")
	require.Contains(t, err.Error(), "core.NewImporter")
}

// TestImportMod_DeployCompile_ReimportSurvivesStagingFailure pins the #173
// review defect: the compile branch used to os.RemoveAll(cachePath) and
// then copy the compiled artifact into place, so a failure in that copy
// step destroyed a pre-existing good cache entry before ever writing its
// replacement. It now stages the compiled artifact into an isolated
// directory and commits it to cachePath atomically (commitStagedCache,
// mirroring the download path) - cachePath is never touched by a failed
// staging attempt, so a pre-existing entry must survive.
func TestImportMod_DeployCompile_ReimportSurvivesStagingFailure(t *testing.T) {
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &raceCompilerSource{fakeCompilerSource: &fakeCompilerSource{}}
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
	require.NoError(t, os.WriteFile(archivePath, []byte("good-exmodz-bytes"), 0o644))

	opts := core.ImportOptions{SourceID: "fake-compiler", ModID: "bear-mount"}
	importer := svc.NewImporter(game)

	// First import succeeds and leaves a good cache entry.
	result1, err := importer.Import(context.Background(), archivePath, game, opts)
	require.NoError(t, err)

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, result1.Mod.SourceID, result1.Mod.ID, result1.Mod.Version)
	require.NoError(t, err)
	require.Equal(t, []string{"Bear_Mount_P.pak"}, files)

	filePath := gameCache.GetFilePath(game.ID, result1.Mod.SourceID, result1.Mod.ID, result1.Mod.Version, files[0])
	origData, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "new-content", string(origData))

	// Re-import the same archive; the compiler's declared output vanishes
	// before it can be staged, so this import must fail...
	src.sabotage = true
	result2, err := importer.Import(context.Background(), archivePath, game, opts)
	require.Error(t, err)
	require.Nil(t, result2)

	// ...and the prior good entry must still be exactly what it was.
	survivingData, err := os.ReadFile(filePath)
	require.NoError(t, err, "the prior good cache entry must survive a failed re-import")
	require.Equal(t, "new-content", string(survivingData))
}
