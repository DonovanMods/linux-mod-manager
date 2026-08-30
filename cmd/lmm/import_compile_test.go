package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// setupDoImportCompileTest builds a DeployCompile game with a registered
// merge-compiler-capable source and an installed base pak - the fixture
// `lmm import <archive>.exmodz` needs to exercise the real archive-mode
// ingest branch end to end.
func setupDoImportCompileTest(t *testing.T) (*core.Service, *domain.Game, *compilerInstallSource) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	compiler := &compilerInstallSource{fakeInstallSource: newFakeInstallSource("fake-compiler")}
	svc.RegisterSource(compiler)

	game := &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))

	oldProfile, oldSource, oldModID, oldForce, oldDryRun, oldSkipMatch :=
		importProfile, importSource, importModID, importForce, importDryRun, importSkipMatch
	oldVerbose, oldNoColor, oldNoHooks := verbose, noColor, noHooks
	importProfile = ""
	importSource = ""
	importModID = ""
	importForce = true
	importDryRun = false
	importSkipMatch = true
	verbose = false
	noColor = true
	noHooks = false
	t.Cleanup(func() {
		importProfile, importSource, importModID, importForce, importDryRun, importSkipMatch =
			oldProfile, oldSource, oldModID, oldForce, oldDryRun, oldSkipMatch
		verbose, noColor, noHooks = oldVerbose, oldNoColor, oldNoHooks
	})

	return svc, game, compiler
}

// TestDoImport_DeployCompile_ImportedModParticipatesInMerge is the #197 C1
// regression test: BEFORE the fix, an imported ".exmodz" mod's retained
// source was keyed by the archive's own filename
// (cache.RetainedSourceName(filename)) but the DB row's FileIDs never
// included that filename - only a resolved source file ID (with --id) or
// nothing at all (without --id, exercised here) - so enabledMergeSources
// could never find it and the mod silently never participated in any
// merge, forever, while `lmm update`/`verify` reported everything healthy
// (the mod was equally invisible on both sides of the staleness
// fingerprint). This proves an imported mod's row-diff actually lands in
// the deployed merged pak.
func TestDoImport_DeployCompile_ImportedModParticipatesInMerge(t *testing.T) {
	svc, game, _ := setupDoImportCompileTest(t)

	archivePath := filepath.Join(t.TempDir(), "Bear_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("bear-exmodz-bytes"), 0o644))

	out, err := captureStdoutErr(t, func() error {
		return doImport(context.Background(), &cobra.Command{}, svc, game, []string{archivePath})
	})
	require.NoError(t, err)
	require.Contains(t, out, "Installed (merged pak updated)",
		"#197 postsmoke UX fix: a zero-file exmodz import must say what happened, not print the misleading 'Files deployed: 0'")

	prof, err := svc.NewProfileManager().Get(context.Background(), game.ID, "default")
	require.NoError(t, err)
	require.Len(t, prof.Mods, 1)
	require.Contains(t, prof.Mods[0].FileIDs, "Bear_Mount.exmodz",
		"the row's FileIDs must include the archive filename - the ONLY identity the retained source is keyed by")

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, readErr := os.ReadFile(deployedPath)
	require.NoError(t, readErr, "the imported mod's content must be deployed via the merged pak - import must sync it")
	require.Equal(t, "bear-exmodz-bytes", string(data), "the merged pak must contain the imported mod's own retained content")
}
