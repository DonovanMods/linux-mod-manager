package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// newImportCompileTestGame builds a DeployCompile game with a registered,
// game-mapped merge-compiler source and an installed base pak - the setup
// #173/#197's import path needs to resolve a MergeCompiler the same way
// Service.DownloadModToCache resolves one from the download's own source,
// except import has no per-download source pinned, so it must resolve the
// merge-compiler from the game's registered sources instead (game.SourceIDs).
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
	require.NoError(t, svc.SaveGame(context.Background(), game))

	return svc, src, game
}

// TestImportMod_DeployCompile_ValidatesAndRetainsNoPerModPak proves the
// #197 merged-only ingest contract for the import path: an imported
// .exmodz is validated and its bytes retained, but no per-mod pak is
// compiled - matching the download path's behavior exactly.
func TestImportMod_DeployCompile_ValidatesAndRetainsNoPerModPak(t *testing.T) {
	svc, src, game := newImportCompileTestGame(t)

	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "Bear_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-exmodz-bytes"), 0o644))

	importer := svc.NewImporterForTest(game)
	result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, src.validateCalls)
	require.Equal(t, 0, src.compileCalls, "import must NOT compile a per-mod pak (#197: merged-only)")
	require.Equal(t, 0, result.FilesExtracted)

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
	require.NoError(t, err)
	require.Empty(t, files)

	// Import has no real DownloadableFile.ID (see the field's own doc
	// comment) - it keys the retained source by the ARCHIVE'S OWN filename
	// instead, exactly as the #196-era destName-keying did.
	retainedPath := gameCache.GetFilePath(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version, cache.RetainedSourceName("Bear_Mount.exmodz"))
	data, err := os.ReadFile(retainedPath)
	require.NoError(t, err)
	require.Equal(t, "fake-exmodz-bytes", string(data))
}

// TestImportPakRetainsAndDeploysRaw mirrors the download-path
// TestDownloadPakRetainsAndDeploysRaw (#221) for import: a convert-eligible
// prebuilt .pak import validates+retains like an .exmodz, ALSO stages a
// deployable copy of itself, and MarkImportedFileComplete's generic
// entry-listing member computation picks that deployable copy up as the
// manifest's sole member - the raw-deploy default.
func TestImportPakRetainsAndDeploysRaw(t *testing.T) {
	svc, src, game := newImportCompileTestGame(t)
	game.ConvertPaks = true

	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "CoolMod.pak")
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-pak-bytes"), 0o644))

	importer := svc.NewImporterForTest(game)
	result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, src.validateCalls, "import must validate the pak via ValidateSource")
	require.Equal(t, 0, src.compileCalls, "import must NOT compile at import time")
	require.Equal(t, 1, result.FilesExtracted, "raw-deploy default: the deployable copy counts as the one member")
	require.Equal(t, "CoolMod.pak", result.RetainedFileID, "import keys retention by the archive's own filename")

	gameCache := svc.GetGameCache(game)

	retainedPath := gameCache.GetFilePath(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version, cache.RetainedSourceName("CoolMod.pak"))
	retainedData, err := os.ReadFile(retainedPath)
	require.NoError(t, err)
	require.Equal(t, "fake-pak-bytes", string(retainedData), "the original pak bytes must be retained")

	deployablePath := gameCache.GetFilePath(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version, "CoolMod.pak")
	deployableData, err := os.ReadFile(deployablePath)
	require.NoError(t, err)
	require.Equal(t, "fake-pak-bytes", string(deployableData), "a deployable copy of the raw pak must also be staged")

	require.NoError(t, svc.MarkImportedFileCompleteForTest(context.Background(), game, result.Mod, result.RetainedFileID))

	manifests, err := gameCache.FileManifests(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
	require.NoError(t, err)
	require.True(t, manifests["CoolMod.pak"].Recorded)
	require.Equal(t, []string{"CoolMod.pak"}, manifests["CoolMod.pak"].Members, "raw-deploy default: the deployable copy is the sole member")
}

// TestImportMod_DeployCompile_MalformedExmodz_FailsLoud proves validation
// happens at import time - the only failure mode left in this branch since
// there is no per-mod compile step anymore.
func TestImportMod_DeployCompile_MalformedExmodz_FailsLoud(t *testing.T) {
	svc, src, game := newImportCompileTestGame(t)
	_ = src // validation failure is injected by wrapping, not by this fake

	failing := &failingValidateCompilerSource{fakeCompilerSource: &fakeCompilerSource{}}
	// Re-register under the same source ID so the importer resolves the
	// failing wrapper instead of the passing fake newImportCompileTestGame
	// already registered.
	svc.RegisterSource(failing)

	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "Bad_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("not-a-valid-exmodz"), 0o644))

	importer := svc.NewImporterForTest(game)
	_, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.Error(t, err)
}

// TestImportMod_DeployCompile_ZipPassthroughUnaffected proves a regular
// (non-exmodz) archive import for a DeployCompile game is byte-for-byte
// identical to the same import against a DeployExtract game - #173/#197
// only insert a new leading branch keyed on isExmodzFile, it must never
// change behavior for anything else.
func TestImportMod_DeployCompile_ZipPassthroughUnaffected(t *testing.T) {
	makeArchive := func(t *testing.T) string {
		t.Helper()
		tempDir := t.TempDir()
		archivePath := filepath.Join(tempDir, "SomeMod.zip")
		createImportTestZip(t, archivePath, map[string]string{"plugin.txt": "test content"})
		return archivePath
	}

	compileSvc, compileSrc, compileGame := newImportCompileTestGame(t)
	compileImporter := compileSvc.NewImporterForTest(compileGame)
	compileResult, err := compileImporter.Import(context.Background(), makeArchive(t), compileGame, core.ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, compileSrc.compileCalls)

	extractSvc, extractSrc, extractGame := newImportCompileTestGame(t)
	extractGame.DeployMode = domain.DeployExtract
	extractImporter := extractSvc.NewImporterForTest(extractGame)
	extractResult, err := extractImporter.Import(context.Background(), makeArchive(t), extractGame, core.ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, extractSrc.compileCalls)

	require.Equal(t, extractResult.FilesExtracted, compileResult.FilesExtracted)
	require.Equal(t, extractResult.Mod.Name, compileResult.Mod.Name)
}

// TestImportMod_DeployCompile_NoCompilerSourceFailsLoud pins the "never
// silently cache an unvalidated .exmodz" requirement (#173/#197): a
// DeployCompile game with no MergeCompiler-capable source mapped in its
// SourceIDs must fail loud with an actionable error instead of falling
// through to extract/copy. (#256: the failure now happens up front - the
// compiler is resolved for every DeployCompile import, since only it can
// say which files are native - but the message still names the compiler
// gap and nothing is ever cached, same as always.)
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
	// alone a MergeCompiler-capable one.
	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "Bear_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-exmodz-bytes"), 0o644))

	importer := svc.NewImporterForTest(game)
	result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "compiler")

	_, statErr := os.Stat(filepath.Join(cfg.CacheDir, game.ID))
	require.True(t, os.IsNotExist(statErr), "no cache entry should have been created")
}

// TestImportMod_DeployCompile_PakNoCompilerSourceFailsLoud mirrors
// TestImportMod_DeployCompile_NoCompilerSourceFailsLoud's setup (a
// DeployCompile game with NO MergeCompiler-capable source mapped) for a
// .pak filename: like every other import into such a game, it must fail
// loud on the compiler-resolution error, caching nothing.
//
// History: #221 I1 made this exact case fall through to the legacy path
// (whose "unsupported archive format" error it then reported), because the
// resolver message was misleading while core could statically single out
// .exmodz as the only file kind that truly REQUIRED the compiler. #256
// moved that knowledge behind the seam (IsNativeMergeSource), which
// removes the basis for the carve-out: with no resolvable compiler, core
// cannot tell a raw pak from a native merge archive, and falling through
// would let the legacy path's zip-content-sniffing extractor silently
// ingest a real native archive unvalidated. Failing every import of a
// compiler-less compile game with the actionable resolver message ("map a
// source implementing source.MergeCompiler") is now both the safe and the
// accurate behavior. The download path's I1 fall-through is unchanged -
// it keys on the file's own source, a per-archive signal Import lacks
// (TestDownloadPak_NonMergeCompilerSource_FallsThroughToLegacyPath).
func TestImportMod_DeployCompile_PakNoCompilerSourceFailsLoud(t *testing.T) {
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	// No RegisterSource call at all - the game has no source mapped, let
	// alone a MergeCompiler-capable one.
	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile, ConvertPaks: true}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "CoolMod.pak")
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-pak-bytes"), 0o644))

	importer := svc.NewImporterForTest(game)
	result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "merge-compiler-capable source", "must fail loud on the actionable compiler-resolution error, never reach the sniffing legacy path")

	_, statErr := os.Stat(filepath.Join(cfg.CacheDir, game.ID))
	require.True(t, os.IsNotExist(statErr), "no cache entry (and no retained source) should have been created")
}

// TestImportMod_DeployCompile_StandaloneImporterFailsLoud proves an
// Importer constructed without Service context (core.NewImporter, used
// directly in older tests) still fails loud rather than silently caching an
// unvalidated .exmodz - it simply has no compiler resolver to consult.
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
