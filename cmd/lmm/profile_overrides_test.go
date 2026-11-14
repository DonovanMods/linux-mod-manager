package main

// #631: `lmm profile switch` and `lmm profile apply` converge the profile's
// overrides on game files, and their plans list every override change -
// in the text preview and in the --dry-run --json document.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupOverrideSwitchTest is a game whose install path holds a stock
// Engine.ini, with profiles "default" (active) and "other" sharing one mod
// live under both, so a switch between them changes no mod.
func setupOverrideSwitchTest(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()
	svc, game := setupDoProfileSwitchTest(t)
	game.InstallPath = t.TempDir()
	ini := filepath.Join(game.InstallPath, "Engine.ini")
	require.NoError(t, os.WriteFile(ini, []byte("stock\n"), 0o644))
	pm := getProfileManager(svc)
	_, err := pm.Create(context.Background(), game.ID, "other")
	require.NoError(t, err)
	seedDeployableMod(t, svc, game, "shared", "Shared Mod", "shared.esp")
	seedLiveRowUnderProfile(t, svc, game, "other", "shared", "Shared Mod")
	require.NoError(t, pm.AddMod(context.Background(), game.ID, "other", domain.ModReference{SourceID: "src", ModID: "shared", Version: "1.0"}))
	return svc, game, ini
}

// setOverrides replaces profile's overrides.
func setOverrides(t *testing.T, svc *core.Service, gameID, profile string, overrides map[string]string) {
	t.Helper()
	p, err := getProfileManager(svc).Get(context.Background(), gameID, profile)
	require.NoError(t, err)
	p.Overrides = map[string][]byte{}
	for k, v := range overrides {
		p.Overrides[k] = []byte(v)
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), p))
}

func TestDoProfileSwitch_DryRun_ListsOverrideChanges(t *testing.T) {
	svc, game, ini := setupOverrideSwitchTest(t)
	setOverrides(t, svc, game.ID, "other", map[string]string{"Engine.ini": "tweaked\n"})
	withProfileDryRun(t, &profileSwitchDryRun)

	out := captureStdout(t, func() error {
		return doProfileSwitch(context.Background(), svc, game, "other")
	})

	assert.Equal(t, "Switch plan for profile \"other\" (dry run)\n\n"+
		"Will change 1 file(s) for the profile's overrides:\n"+
		"  + <game root>/Engine.ini (write the override)\n", out)
	assert.Equal(t, "stock\n", readFileText(t, ini), "a dry run writes nothing")
}

func TestDoProfileSwitch_OverridesOnly_AsksAndWritesThem(t *testing.T) {
	svc, game, ini := setupOverrideSwitchTest(t)
	setOverrides(t, svc, game.ID, "other", map[string]string{"Engine.ini": "tweaked\n"})
	oldYes := profileSwitchYes
	profileSwitchYes = true
	t.Cleanup(func() { profileSwitchYes = oldYes })

	out := captureStdout(t, func() error {
		return doProfileSwitch(context.Background(), svc, game, "other")
	})
	assert.Contains(t, out, "  + <game root>/Engine.ini (write the override)\n")
	assert.Contains(t, out, "✓ Switched to profile: other")
	assert.Equal(t, "tweaked\n", readFileText(t, ini))

	// And back: the game's own file returns.
	out = captureStdout(t, func() error {
		return doProfileSwitch(context.Background(), svc, game, "default")
	})
	assert.Contains(t, out, "  - <game root>/Engine.ini (take the override away)\n")
	assert.Equal(t, "stock\n", readFileText(t, ini))
}

func TestDoProfileSwitch_DryRunJSON_CarriesOverrides(t *testing.T) {
	svc, game, _ := setupOverrideSwitchTest(t)
	setOverrides(t, svc, game.ID, "other", map[string]string{"Engine.ini": "tweaked\n"})
	withProfileDryRun(t, &profileSwitchDryRun)

	out := runJSONCommand(t, func() error {
		return doProfileSwitch(context.Background(), svc, game, "other")
	})
	var plan struct {
		Overrides []map[string]string `json:"overrides"`
		NoChanges bool                `json:"no_changes"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &plan))
	assert.Equal(t, []map[string]string{{"path": "Engine.ini", "root": "install_path", "action": "place"}}, plan.Overrides)
	assert.False(t, plan.NoChanges)
}

func TestDoProfileApply_OverridesOnly_PreviewsAndWritesThem(t *testing.T) {
	svc, game, ini := setupOverrideSwitchTest(t)
	setOverrides(t, svc, game.ID, "default", map[string]string{"Engine.ini": "tweaked\n"})

	withProfileDryRun(t, &profileApplyDryRun)
	out := captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})
	assert.Equal(t, "Apply plan for profile \"default\" (dry run)\n\n"+
		"Will change 1 file(s) for the profile's overrides:\n"+
		"  + <game root>/Engine.ini (write the override)\n", out)
	assert.Equal(t, "stock\n", readFileText(t, ini))

	profileApplyDryRun = false
	oldYes := profileApplyYes
	profileApplyYes = true
	t.Cleanup(func() { profileApplyYes = oldYes })
	out = captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})
	assert.NotContains(t, out, "already matches")
	assert.Equal(t, "tweaked\n", readFileText(t, ini))

	out = captureStdout(t, func() error {
		return doProfileApply(context.Background(), svc, game, nil)
	})
	assert.Contains(t, out, "System already matches profile default.")
}

func readFileText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
