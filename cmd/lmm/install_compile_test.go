package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeBasePak writes a real, minimal but VALID pak at path (#196: the
// compile branch now opens the base pak itself to read its footer IndexHash
// as a compile fingerprint, so a bare byte-stub file no longer parses).
func writeFakeBasePak(t *testing.T, path string) {
	t.Helper()
	w, err := unrealpak.Create(path)
	require.NoError(t, err)
	require.NoError(t, w.AddFile("Data/D_Fixture.json", []byte(`{"fixture":true}`)))
	require.NoError(t, w.Close())
}

// compilerInstallSource wraps fakeInstallSource with a source.MergeCompiler
// implementation, so `lmm install` can drive a real DeployCompile game
// end-to-end through the CLI's exact console-output path (mirrors
// internal/core/service_icarus_compile_test.go's fakeCompilerSource, at the
// CLI layer instead of core's).
type compilerInstallSource struct {
	*fakeInstallSource
	validateCalls int
	compileCalls  int
	mergeWarnings []string
}

// ValidateSource confirms the archive exists - this test only asserts the
// CLI announces the retain step, not that real .exmodz parsing happens
// (internal/source/icarus's own tests cover that).
func (s *compilerInstallSource) ValidateSource(sourceFilePath string) error {
	s.validateCalls++
	_, err := os.Stat(sourceFilePath)
	return err
}

// MergeCompile concatenates every source's bytes - enough for tests to
// prove a merge/regen actually happened and used the retained content,
// without needing a real base pak table to patch (mirrors
// internal/core/service_icarus_compile_test.go's fakeCompilerSource).
func (s *compilerInstallSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, error) {
	s.compileCalls++
	var out []byte
	for _, src := range sources {
		data, err := os.ReadFile(src.ExmodzPath)
		if err != nil {
			return nil, err
		}
		out = append(out, data...)
	}
	return s.mergeWarnings, os.WriteFile(outputPath, out, 0o644)
}

// TestDoInstall_DeployCompile_AnnouncesRetaining guards #190 item 1: an
// install that ingests an ".exmodz" file must announce the retain-for-merge
// step by name, not the generic "Extracting to cache..." line the plain
// extract/copy path uses (which is actively misleading here - nothing is
// extracted). #197: ingest validates+retains only, so this test's premise
// changed from "announces compiling" to "announces retaining" - the actual
// merge is batched across the whole profile and happens later.
func TestDoInstall_DeployCompile_AnnouncesRetaining(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()

	basePak := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	compiler := &compilerInstallSource{fakeInstallSource: src}
	// Re-register under the same ID so doInstall's resolved source is the
	// merge-compiler-capable wrapper, not the plain fake registered by
	// setupDoInstallTest.
	svc.RegisterSource(compiler)

	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Bear Mount", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", Name: "Bear Mount", FileName: "Bear_Mount.exmodz", IsPrimary: true, Category: "MAIN"}})
	src.AddDownload("main", []byte("fake-exmodz-bytes"))

	out := captureStdout(t, func() error {
		return doInstall(context.Background(), svc, game, nil)
	})

	assert.Equal(t, 1, compiler.validateCalls)
	assert.Contains(t, out, "Retaining Bear_Mount.exmodz for merge...\n")
	assert.NotContains(t, out, "Extracting to cache...", "retaining isn't extracting - the generic message must not also print")
}
