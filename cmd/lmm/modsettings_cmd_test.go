package main

// #646 at the CLI: on Baldur's Gate 3 a deploy lists the deployed .pak's
// module in modsettings.lsx and says so; `lmm load-order list` shows the
// .pak order with the list's own entries as external and says the list
// enables; `lmm load-order move` writes it; `lmm verify` reports a list
// the game reset and --fix lists the pak again; a purge puts the user's
// list back. Every pak is synthetic (lariantest).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/larian/lariantest"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cliCameraUUID = "c0ffee00-0000-4000-8000-00000000cafe"

// setupModSettingsCLI is setupPrefixCLI with the camera mod's pak made a
// real (synthetic) one declaring a module, a second mod shipping
// Zoom.pak, and the game's mod list as its first run writes it.
func setupModSettingsCLI(t *testing.T) (*core.Service, *domain.Game, string, []byte) {
	t.Helper()
	ctx := context.Background()
	svc, game, prefix := setupPrefixCLI(t, true)
	require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: "src", ModID: "camera", Version: "1.0"},
		"Camera-1.0/Camera.pak", lariantest.ModPak("Camera", "Native Camera", cliCameraUUID)))
	require.NoError(t, svc.GetGameCache(game).Store(cache.Key{GameID: game.ID, SourceID: "src", ModID: "zoom", Version: "1.0"},
		"Zoom.pak", lariantest.ModPak("Zoom", "Zoom", "0000200a-0000-4000-8000-00000000200a")))
	seedSyncInstalledMod(t, svc, game, "src", "zoom", "zoom", "1.0", "default", true, nil)
	require.NoError(t, getProfileManager(svc).AddMod(ctx, game.ID, "default", domain.ModReference{SourceID: "src", ModID: "zoom", Version: "1.0"}))
	dir := filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Larian Studios", "Baldur's Gate 3", "PlayerProfiles", "Public")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	list := filepath.Join(dir, "modsettings.lsx")
	original := lariantest.ModSettings(lariantest.GustavX)
	require.NoError(t, os.WriteFile(list, original, 0o644))
	resetLoadOrderFlags(t)
	return svc, game, list, original
}

func TestCLI_ModSettingsDeployListMoveVerifyPurge(t *testing.T) {
	ctx := context.Background()
	svc, game, list, original := setupModSettingsCLI(t)

	out := captureCombined(t, func() error { return doDeploy(ctx, svc, game, nil) })
	assert.Contains(t, out, "Load order baldursgate3 written: 3 file(s) (1 kept that lmm did not deploy)")
	assert.Contains(t, out, list)
	assert.Contains(t, readFile(t, list), cliCameraUUID)

	stdout := captureStdout(t, func() error { return doLoadOrderList(ctx, svc, game) })
	assert.Contains(t, stdout, "Paks load order for "+game.ID+" (profile default)\n")
	assert.Contains(t, stdout, "The list enables the files as well as ordering them")
	assert.Contains(t, stdout, "The profile orders nothing yet: lmm keeps its files in the order the list has them")
	assert.Regexp(t, `1\s+GustavX\s+external`, stdout)
	assert.Regexp(t, `2\s+Camera\.pak\s+default\s+camera`, stdout)
	assert.Regexp(t, `3\s+Zoom\.pak\s+default\s+zoom`, stdout)

	stdout = captureStdout(t, func() error {
		return doLoadOrderEdit(ctx, svc, game, core.FileLoadOrderEdit{Op: core.LoadOrderOpMove, File: "Zoom.pak", Before: "Camera.pak"})
	})
	assert.Contains(t, stdout, "✓ move Zoom.pak before Camera.pak")
	data := readFile(t, list)
	assert.Less(t, indexOf(data, "Zoom"), indexOf(data, "Native Camera"), "Zoom loads first now")

	// The game resets its list: verify says so, and --fix lists the paks.
	require.NoError(t, os.WriteFile(list, original, 0o644))
	setFlag(t, &verifyProfile, "default")
	out = captureStdout(t, func() error { return doVerify(ctx, svc, game, nil) })
	assert.Contains(t, out, "Camera.pak is not in modsettings.lsx")
	setFlag(t, &verifyFix, true)
	_ = captureStdout(t, func() error { return doVerify(ctx, svc, game, nil) })
	assert.Contains(t, readFile(t, list), cliCameraUUID)
	setFlag(t, &verifyFix, false)

	setFlag(t, &purgeYes, true)
	out = captureCombined(t, func() error { return doPurge(ctx, svc, game) })
	assert.Contains(t, out, "Load order baldursgate3: the list you had before lmm wrote it is back")
	assert.Equal(t, string(original), readFile(t, list))
}

func indexOf(s, sub string) int { return strings.Index(s, sub) }

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// `lmm load-order list --json` on Baldur's Gate 3 (#646): the .pak order,
// enables set, the game's own entry external.
func TestJSONGolden_LoadOrderModSettings(t *testing.T) {
	ctx := context.Background()
	svc, game, list, _ := setupModSettingsCLI(t)
	_ = captureCombined(t, func() error { return doDeploy(ctx, svc, game, nil) })
	withJSONOutput(t)
	out := captureStdout(t, func() error { return doLoadOrderList(ctx, svc, game) })
	assertJSONCLIGolden(t, "load_order_list_modsettings", out, filepath.Dir(list), "<PROFILE_DIR>")
}
