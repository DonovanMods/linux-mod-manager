package main

// #462 at the CLI: every command that deploys into the game directory is
// refused for a profile that is not the game's active one - naming `lmm
// profile switch`, exiting 1, changing nothing - and the removals
// (uninstall, mod disable) say that they only removed what that profile
// alone recorded.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedAltProfile gives svc's game an active "default" profile deploying
// "shared", and a non-active "alt" listing "shared" and "altonly", both
// cached - alt's rows are not deployed.
func seedAltProfile(t *testing.T, svc *core.Service, game *domain.Game, sourceID string) {
	t.Helper()
	ctx := context.Background()
	pm := getProfileManager(svc)
	for _, name := range []string{"default", "alt"} {
		if _, err := pm.Get(ctx, game.ID, name); err != nil {
			_, err := pm.Create(ctx, game.ID, name)
			require.NoError(t, err)
		}
	}
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	for _, m := range []struct{ profile, id string }{{"default", "shared"}, {"alt", "shared"}, {"alt", "altonly"}} {
		require.NoError(t, svc.GetGameCache(game).Store(game.ID, sourceID, m.id, "1.0", m.id+".esp", []byte(m.id)))
		seedSyncInstalledMod(t, svc, game, sourceID, m.id, "Mod "+m.id, "1.0", m.profile, m.profile == "default", nil)
		require.NoError(t, pm.AddMod(ctx, game.ID, m.profile, domain.ModReference{SourceID: sourceID, ModID: m.id, Version: "1.0"}))
	}
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(game.ModPath, "shared.esp"))
}

// requireRefusedForAlt checks a command's error is #462's refusal for alt,
// its exit code is 1, and the game directory is as before.
func requireRefusedForAlt(t *testing.T, err error, game *domain.Game, before []string) {
	t.Helper()
	require.ErrorIs(t, err, core.ErrProfileNotActive)
	assert.Contains(t, err.Error(), "lmm profile switch alt")
	assert.Equal(t, exitError, exitCodeFor(err))
	assert.Equal(t, before, dirNames(t, game.ModPath), "the game directory is untouched")
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestDoInstall_RefusesANonActiveProfile(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})
	seedAltProfile(t, svc, game, "test-src")
	before := dirNames(t, game.ModPath)
	setFlag(t, &installProfile, "alt")

	_, _, err := captureStdoutAndStderr(t, func() error {
		return doInstall(context.Background(), svc, game, []string{"mod1"})
	})

	requireRefusedForAlt(t, err, game, before)
}

func TestDoModEnable_RefusesANonActiveProfile(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	seedAltProfile(t, svc, game, "src")
	before := dirNames(t, game.ModPath)
	setFlag(t, &modProfile, "alt")
	setFlag(t, &modSource, "")

	_, _, err := captureStdoutAndStderr(t, func() error {
		return doModEnable(context.Background(), svc, game, "altonly")
	})

	requireRefusedForAlt(t, err, game, before)
	assert.NotContains(t, dirNames(t, game.ModPath), "altonly.esp")
}

func TestDoUpdateRollback_RefusesANonActiveProfile(t *testing.T) {
	svc, game, _ := setupDoUpdateTest(t)
	seedAltProfile(t, svc, game, "test-src")
	before := dirNames(t, game.ModPath)
	setFlag(t, &updateProfile, "alt")

	_, _, err := captureStdoutAndStderr(t, func() error {
		return doUpdateRollback(context.Background(), svc, game, "altonly")
	})

	requireRefusedForAlt(t, err, game, before)
}

func TestDoUpdate_RefusesANonActiveProfile(t *testing.T) {
	svc, game, _ := setupDoUpdateTest(t)
	seedAltProfile(t, svc, game, "test-src")
	before := dirNames(t, game.ModPath)
	setFlag(t, &updateProfile, "alt")

	_, _, err := captureStdoutAndStderr(t, func() error {
		return doUpdate(context.Background(), svc, game, []string{"altonly"})
	})

	requireRefusedForAlt(t, err, game, before)
}

func TestDoImport_RefusesANonActiveProfile(t *testing.T) {
	svc, game := setupDoImportTest(t)
	seedAltProfile(t, svc, game, "src")
	before := dirNames(t, game.ModPath)
	archivePath := filepath.Join(t.TempDir(), "mymod.zip")
	createTestArchive(t, archivePath, map[string]string{"mymod.esp": "data"})
	setFlag(t, &importProfile, "alt")

	for name, args := range map[string][]string{"archive": {archivePath}, "scan": nil} {
		_, _, err := captureStdoutAndStderr(t, func() error {
			return doImport(context.Background(), &cobra.Command{}, svc, game, args)
		})
		t.Run(name, func(t *testing.T) {
			requireRefusedForAlt(t, err, game, before)
		})
	}
}

func TestDoUninstall_ANonActiveProfileRemovesOnlyItsOwn(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	seedAltProfile(t, svc, game, "src")
	before := dirNames(t, game.ModPath)
	setFlag(t, &uninstallProfile, "alt")
	setFlag(t, &uninstallSource, "")

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doUninstall(context.Background(), svc, game, "shared")
	})

	require.NoError(t, err)
	assert.Equal(t, before, dirNames(t, game.ModPath), "default's shared.esp stays")
	assert.Contains(t, stdout, "✓ Uninstalled: Mod shared\n")
	assert.Contains(t, stdout, "alt is not the active profile (default is), so only files it alone recorded deploying were removed: 0\n")
}

func TestDoModDisable_ANonActiveProfileRemovesOnlyItsOwn(t *testing.T) {
	svc, game := setupDoProfileSwitchTest(t)
	seedAltProfile(t, svc, game, "src")
	before := dirNames(t, game.ModPath)
	setFlag(t, &modProfile, "alt")
	setFlag(t, &modSource, "")

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doModDisable(context.Background(), svc, game, "shared")
	})

	require.NoError(t, err)
	assert.Equal(t, before, dirNames(t, game.ModPath))
	assert.Equal(t, "✓ Disabled: Mod shared in profile alt\n"+
		"  alt is not the active profile (default is), so only files it alone recorded deploying were removed: 0\n", stdout)
}

func TestDoProfileImport_ANonActiveProfileIsRecordedOnly(t *testing.T) {
	svc, game, _ := setupDoProfileImportTest(t)
	seedAltProfile(t, svc, game, "test-src")
	before := dirNames(t, game.ModPath)
	setFlag(t, &profileImportYes, true)
	setFlag(t, &profileImportForce, true)
	ctx := context.Background()
	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "test-src", "donly", "1.0", "donly.esp", []byte("d")))
	seedSyncInstalledMod(t, svc, game, "test-src", "donly", "Mod donly", "1.0", "default", true, nil)
	require.NoError(t, getProfileManager(svc).AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "test-src", ModID: "donly", Version: "1.0"}))
	_, err := svc.DeployProfile(ctx, game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	before = dirNames(t, game.ModPath)
	data := buildImportProfileData(t, "g1", "alt", []domain.ModReference{{SourceID: "test-src", ModID: "donly", Version: "1.0"}})

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doProfileImport(ctx, svc, game, data)
	})

	require.NoError(t, err)
	assert.Equal(t, before, dirNames(t, game.ModPath))
	assert.Contains(t, stdout, "alt is not the active profile of Game (default is), so the import records these mods in it")
	assert.Contains(t, stdout, "    ✓ Recorded: Mod donly\n")
	assert.Contains(t, stdout, "Recorded: 1 (not deployed")
}
