package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupDoDeployCompileTest extends setupDoDeployTest into a DeployCompile
// game (#255): ConvertPaks on, base pak present, merge-compiler source
// registered under "fake-compiler", game registered (mergeCompilerForGame
// resolves the game's configured sources).
func setupDoDeployCompileTest(t *testing.T) (*core.Service, *domain.Game, *compilerInstallSource) {
	t.Helper()
	svc, game := setupDoDeployTest(t)
	game.DeployMode = domain.DeployCompile
	game.ConvertPaks = true
	game.InstallPath = t.TempDir()
	game.SourceIDs = map[string]string{"fake-compiler": "external-icarus-id"}
	require.NoError(t, svc.AddGame(game))

	basePak := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	compiler := &compilerInstallSource{fakeInstallSource: newFakeInstallSource("fake-compiler")}
	svc.RegisterSource(compiler)
	return svc, game, compiler
}

// ensureDefaultProfile creates the "default" profile if a seed helper runs
// before any seedDeployableMod call (which otherwise creates it).
func ensureDefaultProfile(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	pm := svc.NewProfileManager()
	if _, err := pm.Get(game.ID, "default"); err != nil {
		require.ErrorIs(t, err, domain.ErrProfileNotFound)
		_, err := pm.Create(game.ID, "default")
		require.NoError(t, err)
	}
}

// seedCompileExmodzMod installs an enabled native merge-source mod: retained
// source only, zero deployment members of its own (#197).
func seedCompileExmodzMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, fileID string) {
	t.Helper()
	ensureDefaultProfile(t, svc, game)
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, "1.0", cache.RetainedSourceName(fileID), []byte(name+"-bytes")))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: "1.0", FileIDs: []string{fileID}}))
}

// seedCompilePakMod installs an enabled convert-eligible pak mod in the
// shape #221 ingest produces: retained source plus a deployable raw copy
// recorded as the manifest's sole member (raw-deploy default until a merge
// flips it). fileID must classify as a convertible kind (suffix ".pak").
func seedCompilePakMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, fileID string) {
	t.Helper()
	ensureDefaultProfile(t, svc, game)
	gameCache := svc.GetGameCache(game)
	pakContent := []byte(name + "-pak-bytes")
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, "1.0", cache.RetainedSourceName(fileID), pakContent))
	member := modID + ".pak"
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, "1.0", member, pakContent))
	versionDir := gameCache.ModPath(game.ID, "fake-compiler", modID, "1.0")
	require.NoError(t, cache.MarkFileCompleteWithMembers(versionDir, fileID, []string{member}))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: name, Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: "1.0", FileIDs: []string{fileID}}))
}

// TestDoDeploy_Compile_LabelsMergedRawAndLooseAndPrintsFooter is #255's CLI
// acceptance test: on a DeployCompile game the header stops claiming a
// per-mod link method, merge participants are labeled "(merged)", an
// opted-out pak deploying raw is labeled "(raw)", an ordinary loose-file
// mod keeps its plain line, and a post-sync footer names the merged
// artifact with its participant count, directly above "Deployed: N" (which
// still counts merge participants).
func TestDoDeploy_Compile_LabelsMergedRawAndLooseAndPrintsFooter(t *testing.T) {
	svc, game, _ := setupDoDeployCompileTest(t)
	seedCompileExmodzMod(t, svc, game, "bear-mount", "Bear Mount", "exmodz-file")
	seedCompilePakMod(t, svc, game, "raw-pak", "Raw Pak Mod", "raw.pak")
	require.NoError(t, svc.SetModConvertPaks("fake-compiler", "raw-pak", game.ID, "default", false))
	seedDeployableMod(t, svc, game, "loose", "Loose Mod", "loose.esp")

	out := captureStdout(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "Deploying 3 mod(s) — compile mode...\n\n", "the compile header must not claim a per-mod link method")
	assert.NotContains(t, out, "using symlink")
	assert.Contains(t, out, "  ✓ Bear Mount (merged)\n")
	assert.Contains(t, out, "  ✓ Raw Pak Mod (raw)\n")
	assert.Contains(t, out, "  ✓ Loose Mod\n")
	assert.NotContains(t, out, "Loose Mod (", "a loose-file mod keeps its plain, unlabeled line")
	assert.Contains(t, out, "\nMerged 1 mod(s) → zzz_LMM_Merged_P.pak\nDeployed: 3\n",
		"the footer must name the merged artifact and sit directly above the summary")

	_, err := os.Lstat(filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak"))
	assert.NoError(t, err, "the merged artifact must actually be deployed")
	_, err = os.Lstat(filepath.Join(game.ModPath, "raw-pak.pak"))
	assert.NoError(t, err, "the opted-out pak must be deployed raw")
}

// pakFailCompilerSource wraps compilerInstallSource so a CLI test can script
// per-ref pak-conversion failures, mirroring internal/source/icarus/merge.go's
// real failure path (a "... - deploying raw" warning per skipped ref).
type pakFailCompilerSource struct {
	*compilerInstallSource
	failRefs map[string]string
}

func (s *pakFailCompilerSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, []source.MergeFailure, error) {
	var out []byte
	var warnings []string
	var failed []source.MergeFailure
	for _, src := range sources {
		if reason, bad := s.failRefs[src.ModRef]; bad {
			failed = append(failed, source.MergeFailure{ModRef: src.ModRef, Reason: reason})
			warnings = append(warnings, fmt.Sprintf("mod %s: pak conversion failed: %s - deploying raw", src.ModName, reason))
			continue
		}
		data, err := os.ReadFile(src.SourcePath)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, data...)
	}
	return warnings, failed, os.WriteFile(outputPath, out, 0o644)
}

var _ source.MergeCompiler = (*pakFailCompilerSource)(nil)

// TestDoDeploy_Compile_ConversionFailure_FooterCorrectsOptimisticLabel covers
// #255's accepted optimistic case end to end: an opted-in pak is labeled
// "(merged)" inline (at ✓ time the merge hasn't run), its conversion then
// fails during the post-loop sync - the existing warning carries the
// correction, and the footer reports the raw fallback.
func TestDoDeploy_Compile_ConversionFailure_FooterCorrectsOptimisticLabel(t *testing.T) {
	svc, game, compiler := setupDoDeployCompileTest(t)
	svc.RegisterSource(&pakFailCompilerSource{
		compilerInstallSource: compiler,
		failRefs:              map[string]string{"fake-compiler:flaky-pak": "irreconcilable"},
	})
	seedCompileExmodzMod(t, svc, game, "bear-mount", "Bear Mount", "exmodz-file")
	seedCompilePakMod(t, svc, game, "flaky-pak", "Flaky Pak", "flaky.pak")

	out := captureCombined(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "  ✓ Flaky Pak (merged)\n", "inline label is optimistic by design - the footer/warning correct it")
	assert.Contains(t, out, "pak conversion failed", "the existing conversion-failure warning must still print")
	assert.Contains(t, out, "\nMerged 1 mod(s) → zzz_LMM_Merged_P.pak (1 deployed raw)\nDeployed: 2\n",
		"the footer must report the raw fallback and still sit directly above the summary")
}

// TestDoDeploy_NonCompile_NoCompileReadout guards the gate #255 must not
// move: a non-compile deploy's output is byte-identical to before - the
// original header, no labels, no merge footer.
func TestDoDeploy_NonCompile_NoCompileReadout(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedDeployableMod(t, svc, game, "a", "Mod A", "a.esp")

	out := captureStdout(t, func() error {
		return doDeploy(context.Background(), svc, game, nil)
	})

	assert.Contains(t, out, "Deploying 1 mod(s) using symlink...\n\n")
	assert.Contains(t, out, "  ✓ Mod A\n")
	assert.Contains(t, out, "\nDeployed: 1\n")
	assert.NotContains(t, out, "compile mode")
	assert.NotContains(t, out, "(merged)")
	assert.NotContains(t, out, "Merged ")
}
