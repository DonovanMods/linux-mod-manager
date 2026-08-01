package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compilerInstallSource wraps fakeInstallSource with a source.Compiler
// implementation, so `lmm install` can drive a real DeployCompile game
// end-to-end through the CLI's exact console-output path (mirrors
// internal/core/service_icarus_compile_test.go's fakeCompilerSource, at the
// CLI layer instead of core's).
type compilerInstallSource struct {
	*fakeInstallSource
	compileCalls int
}

// Compile copies the downloaded source file through unchanged - this test
// only asserts the CLI announces the compile step and uses its output, not
// that real PAK compilation happens (internal/unrealpak's own tests cover
// that).
func (s *compilerInstallSource) Compile(ctx context.Context, basePakPath, sourceFilePath, outputPath string) error {
	s.compileCalls++
	data, err := os.ReadFile(sourceFilePath)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, data, 0o644)
}

// TestDoInstall_DeployCompile_AnnouncesCompiling guards #190 item 1: an
// install that compiles a .exmodz file must announce the compile step by
// name, not the generic "Extracting to cache..." line the plain
// extract/copy path uses (which is actively misleading here - compiling
// isn't extracting).
func TestDoInstall_DeployCompile_AnnouncesCompiling(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()

	basePak := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	require.NoError(t, os.WriteFile(basePak, []byte("fake-base-pak"), 0o644))

	compiler := &compilerInstallSource{fakeInstallSource: src}
	// Re-register under the same ID so doInstall's resolved source is the
	// compiler-capable wrapper, not the plain fake registered by
	// setupDoInstallTest.
	svc.RegisterSource(compiler)

	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Bear Mount", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", Name: "Bear Mount", FileName: "Bear_Mount.exmodz", IsPrimary: true, Category: "MAIN"}})
	src.AddDownload("main", []byte("fake-exmodz-bytes"))

	out := captureStdout(t, func() error {
		return doInstall(context.Background(), svc, game, nil)
	})

	assert.Equal(t, 1, compiler.compileCalls)
	assert.Contains(t, out, "Compiling Bear_Mount.exmodz → Bear_Mount_P.pak...\n")
	assert.NotContains(t, out, "Extracting to cache...", "compiling isn't extracting - the generic message must not also print")
}
