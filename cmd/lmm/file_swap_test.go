package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #639: a profile that pins another file of the version a mod is installed
// at is a reinstall, and every converge plan says why.

// seedSwappedEntry caches src:m at 1.0 as built from file "20".
func seedSwappedEntry(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "m", Version: "1.0"}
	require.NoError(t, svc.GetGameCache(game).Store(k, "m-a.esp", []byte("a")))
	require.NoError(t, cache.MarkFileComplete(svc.GetGameCache(game).ModPath(k), "20"))
}

func TestDoProfileApply_DryRun_NamesAFileSwapAsAReinstall(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	seedSwappedEntry(t, svc, game)
	seedSyncInstalledMod(t, svc, game, "src", "m", "Mod m", "1.0", "default", true, []string{"20"})
	require.NoError(t, getProfileManager(svc).AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "m", Version: "1.0", FileIDs: []string{"21"}}))
	withProfileDryRun(t, &profileApplyDryRun)

	out := captureStdout(t, func() error {
		return doProfileApply(ctx, svc, game, nil)
	})
	assert.Contains(t, out, "Will install 1 mod(s):\n  ↓ src:m v1.0 (reinstall: files changed)\n")

	jsonOut := runJSONCommand(t, func() error {
		return doProfileApply(ctx, svc, game, nil)
	})
	var plan struct {
		ToInstall []struct {
			Reason string `json:"reason"`
		} `json:"to_install"`
	}
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &plan))
	require.Len(t, plan.ToInstall, 1)
	assert.Equal(t, "files_changed", plan.ToInstall[0].Reason)
}

// An unchanged profile has nothing to do: no reinstall line.
func TestDoProfileApply_DryRun_SameFilesIsNoReinstall(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	seedSwappedEntry(t, svc, game)
	seedSyncInstalledMod(t, svc, game, "src", "m", "Mod m", "1.0", "default", true, []string{"20"})
	require.NoError(t, getProfileManager(svc).AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "m", Version: "1.0", FileIDs: []string{"20"}}))
	withProfileDryRun(t, &profileApplyDryRun)

	out := captureStdout(t, func() error {
		return doProfileApply(ctx, svc, game, nil)
	})
	assert.NotContains(t, out, "reinstall")
	assert.NotContains(t, out, "Will install")
}

func TestDoProfileSwitch_DryRun_NamesAFileSwapAsAReinstall(t *testing.T) {
	ctx := context.Background()
	svc, game := setupDoProfileSwitchTest(t)
	pm := getProfileManager(svc)
	_, err := pm.Create(ctx, game.ID, "other")
	require.NoError(t, err)
	seedSwappedEntry(t, svc, game)
	seedSyncInstalledMod(t, svc, game, "src", "m", "Mod m", "1.0", "other", true, []string{"20"})
	require.NoError(t, pm.AddMod(ctx, game.ID, "other", domain.ModReference{SourceID: "src", ModID: "m", Version: "1.0", FileIDs: []string{"21"}}))
	withProfileDryRun(t, &profileSwitchDryRun)

	out := captureStdout(t, func() error {
		return doProfileSwitch(ctx, svc, game, "other")
	})
	assert.Contains(t, out, "Will install 1 mod(s):\n  ↓ src:m v1.0 (reinstall: files changed)\n")

	jsonOut := runJSONCommand(t, func() error {
		return doProfileSwitch(ctx, svc, game, "other")
	})
	var plan struct {
		ReinstallReasons map[string]string `json:"reinstall_reasons"`
	}
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &plan))
	assert.Equal(t, map[string]string{"src:m": "files_changed"}, plan.ReinstallReasons)
}

func TestDoProfileImport_NamesAFileSwapAsAReinstall(t *testing.T) {
	ctx := context.Background()
	svc, game, _ := setupDoProfileImportTest(t)
	pm := getProfileManager(svc)
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	require.NoError(t, pm.AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "m", Version: "1.0", FileIDs: []string{"20"}}))
	seedSwappedEntry(t, svc, game)
	seedSyncInstalledMod(t, svc, game, "src", "m", "Mod m", "1.0", "default", true, []string{"20"})
	setFlag(t, &profileImportForce, true)
	setFlag(t, &profileImportYes, true)
	data := buildImportProfileData(t, "g1", "default", []domain.ModReference{{SourceID: "src", ModID: "m", Version: "1.0", FileIDs: []string{"21"}}})

	// src serves nothing here, so the reinstall itself fails: the plan's
	// line is what this pins.
	stdout, _, _ := captureStdoutStderrErr(t, func() error {
		return doProfileImport(ctx, svc, game, data)
	})
	assert.Contains(t, stdout, "    - src:m v1.0 (reinstall: files changed)\n")
}
