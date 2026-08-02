package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
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
	mergeErr      error
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
	if s.mergeErr != nil {
		return nil, s.mergeErr
	}
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
	assert.Contains(t, out, "Installed (merged pak updated)",
		"#197 postsmoke UX fix: 'Files deployed: 0' read as a failure, not the correct expected outcome for a validate+retain-only exmodz mod")
}

// TestBatchInstallMods_DeployCompile_DeploysMergedPak is the #197
// postsmoke regression test: a real user's multi-select install of two
// ".exmodz" mods (the search flow's `len(selectedMods) > 1` branch, doInstall
// -> installMultipleMods -> batchInstallMods) generated the merged pak in
// CACHE but never DEPLOYED it - batchInstallMods is a bespoke
// reimplementation of install/deploy that never went through
// Service.ApplyInstall, the only seam that used to sync the merged pak.
// This drives the REAL production batchInstallMods (not a reimplementation
// or a mock) with two DIFFERENT exmodz mods and proves the merged pak is
// actually deployed on disk afterward, containing BOTH mods' content -
// exactly the class of test whose absence let this ship.
func TestBatchInstallMods_DeployCompile_DeploysMergedPak(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()

	basePak := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	compiler := &compilerInstallSource{fakeInstallSource: src}
	svc.RegisterSource(compiler)
	// SyncMergedPak resolves the game's configured sources (mergeCompilerSourceForGame
	// -> SourcesForGame), which requires the game to be registered - the
	// production CLI always has this via withGameService's svc.GetGame,
	// unlike this fixture's bare *domain.Game construction.
	require.NoError(t, svc.AddGame(game))

	bearMod := &domain.Mod{ID: "bear-mount", SourceID: "test-src", Name: "Bear Mount", Version: "1.0", GameID: "g1"}
	wolfMod := &domain.Mod{ID: "wolf-mount", SourceID: "test-src", Name: "Wolf Mount", Version: "1.0", GameID: "g1"}
	src.AddMod(bearMod, []domain.DownloadableFile{{ID: "bear-exmodz", Name: "Bear Mount", FileName: "Bear_Mount.exmodz", IsPrimary: true, Category: "MAIN"}})
	src.AddMod(wolfMod, []domain.DownloadableFile{{ID: "wolf-exmodz", Name: "Wolf Mount", FileName: "Wolf_Mount.exmodz", IsPrimary: true, Category: "MAIN"}})
	src.AddDownload("bear-exmodz", []byte("bear-bytes"))
	src.AddDownload("wolf-exmodz", []byte("wolf-bytes"))

	out, err := captureStdoutErr(t, func() error {
		return batchInstallMods(context.Background(), svc, game, []*domain.Mod{bearMod, wolfMod}, "default")
	})
	require.NoError(t, err)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, readErr := os.ReadFile(deployedPath)
	require.NoError(t, readErr, "batchInstallMods must sync the merged pak - both mods deploy zero files of their own")
	assert.Contains(t, string(data), "bear-bytes")
	assert.Contains(t, string(data), "wolf-bytes")

	assert.Equal(t, 2, strings.Count(out, "Installed (merged pak updated)"),
		"#197 postsmoke UX fix: each zero-file exmodz mod must say what happened, not print the misleading '(0 files)'")
}

// TestDoInstall_DeployCompile_SyncFailure_PrintsLoudly is the #197
// postsmoke "must be LOUD" regression test: ApplyInstall's own sync call
// used to only append to result.Warnings, which doInstall (the single-mod
// install path) never reads back - a sync failure here was completely
// silent, not even --verbose-gated, the exact plumbing gap the task asked
// to check. Proves a merge failure during a real single-mod install
// reaches stderr unconditionally.
func TestDoInstall_DeployCompile_SyncFailure_PrintsLoudly(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()
	require.NoError(t, svc.AddGame(game))

	basePak := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	compiler := &compilerInstallSource{fakeInstallSource: src, mergeErr: assert.AnError}
	svc.RegisterSource(compiler)

	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Bear Mount", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", Name: "Bear Mount", FileName: "Bear_Mount.exmodz", IsPrimary: true, Category: "MAIN"}})
	src.AddDownload("main", []byte("fake-exmodz-bytes"))

	oldStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	os.Stderr = w
	err := doInstall(context.Background(), svc, game, nil)
	_ = w.Close()
	os.Stderr = oldStderr
	require.NoError(t, err, "a merge failure is non-fatal to the install itself - the mod is still validated+retained+recorded")

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	assert.Contains(t, buf.String(), "Warning:", "a merge failure during install must print a Warning unconditionally, not silently vanish into a discarded result")
}
