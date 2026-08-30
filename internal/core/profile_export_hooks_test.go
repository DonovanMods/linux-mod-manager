package core_test

// #296: `lmm profile export` -> `lmm profile import` must carry the
// profile's own hook overrides. Before this, ProfileManager.Export dropped
// them silently, so a shared profile arrived on the other machine inheriting
// the game's hooks instead of overriding them - and an explicitly-DISABLED
// hook (the *string-pointer encoding's "present but empty") came back as
// "inherit", which is the opposite instruction.
//
// The storage-layer encoding itself is pinned in internal/storage/config's
// profiles_hooks_test.go; these are the seam tests: through
// ProfileManager.Export and through the PlanImport/ApplyImport flow.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeProfileWithHooks hand-writes a profile file carrying both halves of
// the override encoding: install.after_all explicitly DISABLED (present,
// empty) and uninstall.after_all explicitly SET.
func writeProfileWithHooks(t *testing.T, configDir, gameID, profileName, hookPath string) {
	t.Helper()
	dir := filepath.Join(configDir, "games", gameID, "profiles")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	yaml := `name: ` + profileName + `
game_id: ` + gameID + `
mods:
    - source_id: src
      mod_id: "42"
      version: 1.0.0
hooks:
    install:
        after_all: ""
    uninstall:
        after_all: "` + hookPath + `"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, profileName+".yaml"), []byte(yaml), 0o644))
}

// TestProfileManager_Export_WritesHookOverrides is the export half of #296.
func TestProfileManager_Export_WritesHookOverrides(t *testing.T) {
	svc := newFlowsTestService(t)
	configDir := svc.ConfigDir()
	writeProfileWithHooks(t, configDir, "g1", "modded", "/opt/lmm/cleanup.sh")

	data, err := svc.NewProfileManager().Export("g1", "modded")
	require.NoError(t, err)

	round, err := config.ImportProfile(data)
	require.NoError(t, err)
	assert.Equal(t, "", round.Hooks.Install.AfterAll)
	assert.True(t, round.HooksExplicit.Install.AfterAll, "an explicitly-disabled hook must survive the export")
	assert.Equal(t, "/opt/lmm/cleanup.sh", round.Hooks.Uninstall.AfterAll)
	assert.True(t, round.HooksExplicit.Uninstall.AfterAll)
	assert.False(t, round.HooksExplicit.Install.BeforeAll, "an unset hook must stay unset (inherit)")
}

// TestProfileManager_Export_NoHooks_OmitsTheBlock is the compatibility pin
// at this seam: a profile with nothing overridden exports no hooks: key, so
// nothing about an ordinary export moved.
func TestProfileManager_Export_NoHooks_OmitsTheBlock(t *testing.T) {
	svc := newFlowsTestService(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create("g1", "plain")
	require.NoError(t, err)

	data, err := pm.Export("g1", "plain")
	require.NoError(t, err)
	assert.NotContains(t, string(data), "hooks:")
}

// TestPlanImport_ApplyImport_RoundTripsHookOverrides is the import half:
// the plan's parsed profile carries the overrides, and the profile
// ApplyImport saves keeps them on disk - the whole point of exporting them.
func TestPlanImport_ApplyImport_RoundTripsHookOverrides(t *testing.T) {
	svc := newFlowsTestService(t)
	configDir := svc.ConfigDir()
	writeProfileWithHooks(t, configDir, "g1", "modded", "/opt/lmm/cleanup.sh")

	data, err := svc.NewProfileManager().Export("g1", "modded")
	require.NoError(t, err)

	// Import it under a fresh name so the save is a real create, not an
	// overwrite of the file it came from.
	data = []byte(strings.Replace(string(data), "name: modded", "name: shared", 1))

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.NotNil(t, plan.Profile)
	assert.True(t, plan.Profile.HooksExplicit.Install.AfterAll, "the plan's preview profile must carry the overrides")
	assert.Equal(t, "/opt/lmm/cleanup.sh", plan.Profile.Hooks.Uninstall.AfterAll)

	_, err = svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)

	saved, err := config.LoadProfile(configDir, "g1", "shared")
	require.NoError(t, err)
	assert.Equal(t, "", saved.Hooks.Install.AfterAll)
	assert.True(t, saved.HooksExplicit.Install.AfterAll)
	assert.Equal(t, "/opt/lmm/cleanup.sh", saved.Hooks.Uninstall.AfterAll)
	assert.True(t, saved.HooksExplicit.Uninstall.AfterAll)
}

// TestPlanImport_LegacyExportWithoutHooks proves the read side stayed
// backward-compatible: an export recorded before #296 still imports.
func TestPlanImport_LegacyExportWithoutHooks(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	legacy := []byte("name: legacy\ngame_id: g1\nmods: []\n")
	plan, err := svc.PlanImport(context.Background(), game, legacy)
	require.NoError(t, err)
	require.NotNil(t, plan.Profile)
	assert.Equal(t, domain.GameHooksExplicit{}, plan.Profile.HooksExplicit)

	_, err = svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)

	saved, err := config.LoadProfile(svc.ConfigDir(), "g1", "legacy")
	require.NoError(t, err)
	assert.Equal(t, domain.GameHooksExplicit{}, saved.HooksExplicit)
}
