package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findingStatuses lists a verify result's statuses, so a test can say what a
// run reported without depending on row order beyond what it asserts.
func findingStatuses(res *core.VerifyResult) []string {
	out := make([]string, 0, len(res.Findings))
	for _, f := range res.Findings {
		out = append(out, f.Status)
	}
	return out
}

// findingWithStatus returns the first finding carrying status, or nil.
func findingWithStatus(res *core.VerifyResult, status string) *core.VerifyFinding {
	for i := range res.Findings {
		if res.Findings[i].Status == status {
			return &res.Findings[i]
		}
	}
	return nil
}

// bepinexInstall writes a BepInEx installation into root: the preloader, the
// bootstrap files for mode, and (when loggedAt is non-zero) the LogOutput.log
// the loader writes on every run.
func bepinexInstall(t *testing.T, root string, version string, mode domain.LoaderBootstrap, loggedAt time.Time) {
	t.Helper()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	write("BepInEx/core/BepInEx.Preloader.dll", "preloader")
	if version != "" {
		write("BepInEx/core/BepInEx.dll", "core")
		write(".doorstop_version", version+"\n")
	}
	switch mode {
	case domain.LoaderBootstrapNative:
		write("run_bepinex.sh", "#!/bin/sh\n")
		write("libdoorstop.so", "elf")
	case domain.LoaderBootstrapProton:
		write("winhttp.dll", "pe")
		write("doorstop_config.ini", "[General]\nenabled=true\n")
	}
	if !loggedAt.IsZero() {
		write("BepInEx/LogOutput.log", "[Info] BepInEx 5.4.23.5\n")
		require.NoError(t, os.Chtimes(filepath.Join(root, "BepInEx", "LogOutput.log"), loggedAt, loggedAt))
	}
}

// newVerifyLoaderService builds a loader-declaring game whose install
// directory is also its mod path (the game-root shape), so the loader tier
// has something real to look at.
func newVerifyLoaderService(t *testing.T, loader *domain.GameLoader) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	root := t.TempDir()
	game := &domain.Game{
		ID: "valheim", Name: "Valheim", InstallPath: root, ModPath: root,
		LinkMethod: domain.LinkSymlink, Loader: loader,
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err := svc.NewProfileManager().CreateOrResetDefaultAfterGameSave(context.Background(), game.ID)
	require.NoError(t, err)
	return svc, game
}

// TestVerify_LoaderTier_HealthyInstallReportsNothing: the tier only speaks up
// when something is wrong, like every other pass in the engine.
func TestVerify_LoaderTier_HealthyInstallReportsNothing(t *testing.T) {
	svc, game := newVerifyLoaderService(t, &domain.GameLoader{
		Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5",
		Runtime: domain.LoaderRuntimeMono, Bootstrap: domain.LoaderBootstrapNative,
	})
	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapNative, time.Now())

	res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	assert.NotContains(t, findingStatuses(res.Result), "loader_missing")
	assert.NotContains(t, findingStatuses(res.Result), "loader_version_mismatch")
	assert.NotContains(t, findingStatuses(res.Result), "loader_bootstrap_incomplete")
	assert.NotContains(t, findingStatuses(res.Result), "loader_never_ran")
}

// TestVerify_LoaderTier_MissingPreloader is the first check: BepInEx is
// declared and simply is not there. Not fixable - lmm does not install the
// loader, so the finding points at the setup instead.
func TestVerify_LoaderTier_MissingPreloader(t *testing.T) {
	svc, game := newVerifyLoaderService(t, &domain.GameLoader{
		Kind: domain.LoaderKindBepInEx, Bootstrap: domain.LoaderBootstrapNative,
	})

	res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	f := findingWithStatus(res.Result, "loader_missing")
	require.NotNil(t, f, "statuses were %v", findingStatuses(res.Result))
	assert.False(t, f.Fixable)
	assert.NotEmpty(t, f.FixableReason)
	assert.Positive(t, res.Result.Issues)
}

// TestVerify_LoaderTier_VersionDrift: the declaration says one version and
// the installation says another. Reported, never repaired - lmm does not
// choose BepInEx builds.
func TestVerify_LoaderTier_VersionDrift(t *testing.T) {
	svc, game := newVerifyLoaderService(t, &domain.GameLoader{
		Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5", Bootstrap: domain.LoaderBootstrapNative,
	})
	bepinexInstall(t, game.InstallPath, "5.4.21.0", domain.LoaderBootstrapNative, time.Now())

	res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	f := findingWithStatus(res.Result, "loader_version_mismatch")
	require.NotNil(t, f, "statuses were %v", findingStatuses(res.Result))
	assert.Equal(t, "5.4.23.5", f.Recorded)
	assert.Equal(t, "5.4.21.0", f.Effective)
	assert.False(t, f.Fixable)
}

// TestVerify_LoaderTier_BootstrapDoesNotMatchTheDeclaredMode: the pack
// installed is the wrong one for the declared bootstrap - the single most
// common BepInEx-on-Linux mistake, and one that produces no error anywhere
// else because the game launches perfectly and loads nothing.
func TestVerify_LoaderTier_BootstrapDoesNotMatchTheDeclaredMode(t *testing.T) {
	svc, game := newVerifyLoaderService(t, &domain.GameLoader{
		Kind: domain.LoaderKindBepInEx, Bootstrap: domain.LoaderBootstrapNative,
	})
	// A Proton pack (winhttp.dll) installed for a game declared native.
	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapProton, time.Now())

	res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	f := findingWithStatus(res.Result, "loader_bootstrap_incomplete")
	require.NotNil(t, f, "statuses were %v", findingStatuses(res.Result))
	assert.Contains(t, f.Note, "run_bepinex.sh")
	assert.False(t, f.Fixable)
}

// TestVerify_LoaderTier_NeverRan is the honest "did it actually load?"
// signal: the files are all in the right places and BepInEx has never written
// a log, so the launch option is missing or wrong. It costs one stat and is
// the difference between "configured" and "working".
func TestVerify_LoaderTier_NeverRan(t *testing.T) {
	svc, game := newVerifyLoaderService(t, &domain.GameLoader{
		Kind: domain.LoaderKindBepInEx, Bootstrap: domain.LoaderBootstrapProton,
	})
	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapProton, time.Time{})

	res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	f := findingWithStatus(res.Result, "loader_never_ran")
	require.NotNil(t, f, "statuses were %v", findingStatuses(res.Result))
	assert.Contains(t, f.Note, `WINEDLLOVERRIDES="winhttp=n,b" %command%`,
		"the finding names the launch option to paste, which is the whole remedy")
	assert.False(t, f.Fixable)
}

// TestVerify_LoaderTier_LogOlderThanTheLastDeploy: the loader ran, but not
// since the mods currently on disk were deployed - so nothing proves the
// CURRENT set ever loaded.
func TestVerify_LoaderTier_LogOlderThanTheLastDeploy(t *testing.T) {
	svc, game := newVerifyLoaderService(t, &domain.GameLoader{
		Kind: domain.LoaderKindBepInEx, Bootstrap: domain.LoaderBootstrapNative,
	})
	old := time.Now().Add(-48 * time.Hour)
	bepinexInstall(t, game.InstallPath, "5.4.23.5", domain.LoaderBootstrapNative, old)

	// A plugin deployed AFTER that log was written.
	plugin := filepath.Join(game.InstallPath, "BepInEx", "plugins", "Thing.dll")
	require.NoError(t, os.MkdirAll(filepath.Dir(plugin), 0o755))
	require.NoError(t, os.WriteFile(plugin, []byte("assembly"), 0o644))

	res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	f := findingWithStatus(res.Result, "loader_stale_log")
	require.NotNil(t, f, "statuses were %v", findingStatuses(res.Result))
	assert.False(t, f.Fixable)
}

// TestVerify_LoaderTier_SkipsAGameThatDeclaresNoLoader: the tier is scoped to
// loader-enabled games, so every other game's verify run is byte-identical to
// what it was.
func TestVerify_LoaderTier_SkipsAGameThatDeclaresNoLoader(t *testing.T) {
	svc, game := newVerifyLoaderService(t, nil)

	res, err := svc.VerifyReport(context.Background(), game, "default", core.VerifyOptions{Force: true}, nil)
	require.NoError(t, err)
	for _, s := range findingStatuses(res.Result) {
		assert.NotContains(t, s, "loader_")
	}
}
