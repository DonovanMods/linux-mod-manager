package core_test

// #618 (#355 P5): for a game whose adapter manages a plugin list, a FOMOD
// fileDependency's Active and Inactive are the plugin's state in the
// profile's plugin list - a plugin the profile turned off is Inactive - not
// merely whether the file is there.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pluginStateInstaller installs one marker file per plugin state it tests:
// dep.esp active or inactive, ext.esp (a plugin lmm did not deploy)
// inactive, and the game's own Skyrim.esm active.
func pluginStateInstaller(t *testing.T) []byte {
	t.Helper()
	pattern := func(file, state, dest string) string {
		return `<pattern><dependencies><fileDependency file="` + file + `" state="` + state + `"/></dependencies><files><file source="core\plugin.esp" destination="` + dest + `"/></files></pattern>`
	}
	cfg := strings.Replace(flowModuleConfig("4K"), "</installSteps>", `</installSteps><conditionalFileInstalls><patterns>`+
		pattern("dep.esp", "Active", "dep-active.txt")+
		pattern("dep.esp", "Inactive", "dep-inactive.txt")+
		pattern("ext.esp", "Inactive", "ext-inactive.txt")+
		pattern("ext.esp", "Active", "ext-active.txt")+
		pattern("Skyrim.esm", "Active", "base-active.txt")+
		`</patterns></conditionalFileInstalls>`, 1)
	return createTestZipBytes(t, map[string]string{
		"fomod/ModuleConfig.xml": cfg, "core/plugin.esp": "PLUGIN", "tex/2k.dds": "2K", "tex/4k.dds": "4K",
	})
}

// pluginStateFixture is a Skyrim SE game whose default profile deploys
// dep.esp, with ext.esp in its Data folder and the game's Plugins.txt
// listing ext.esp as inactive.
func pluginStateFixture(t *testing.T, plugins domain.ProfilePlugins) *pluginFixture {
	t.Helper()
	f := newPluginFixture(t)
	mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
	t.Cleanup(mock.Close)
	f.svc.RegisterSource(mock)
	mock.AddDownload("fm", pluginStateInstaller(t))
	mock.AddMod(f.game.ID, &domain.Mod{ID: "fm", SourceID: "src", Name: "FOMOD mod", Version: "1.0", GameID: f.game.ID})

	f.mod(t, "dep", map[string][]byte{"dep.esp": pluginBytes(0)})
	f.setPlugins(t, plugins)
	f.deploy(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.game.ModPath, "ext.esp"), pluginBytes(0), 0o644))
	list := filepath.Join(f.listDir, "Plugins.txt")
	data, err := os.ReadFile(list)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(list, append(data, []byte("ext.esp\r\n")...), 0o644))
	return f
}

func installPluginStateMod(t *testing.T, f *pluginFixture) (files []string, states map[string]string) {
	t.Helper()
	ctx := context.Background()
	plan, err := f.svc.PlanInstall(ctx, f.game, "default", "src", "fm", false)
	require.NoError(t, err)
	_, err = f.svc.ApplyInstall(ctx, f.game, plan, core.InstallOptions{Fomod: map[string]domain.FomodChoices{"src:fm": answer("4K")}}, nil)
	require.NoError(t, err)
	files, err = f.svc.GetDeployedFilesForMod(ctx, f.game.ID, "default", "src", "fm")
	require.NoError(t, err)
	p, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "default")
	require.NoError(t, err)
	ref := p.FindRef("src", "fm")
	require.NotNil(t, ref)
	return files, ref.Fomod.FileStates
}

// TestFomodPluginState_ADisabledPluginIsInactive: dep.esp is deployed and
// the profile's plugin list turns it off, so the installer sees it
// Inactive; the external ext.esp the game's list leaves off is Inactive
// too, and the game's own master is Active.
func TestFomodPluginState_ADisabledPluginIsInactive(t *testing.T) {
	f := pluginStateFixture(t, domain.ProfilePlugins{Order: []domain.PluginEntry{{Name: "dep.esp", Enabled: false}}})
	files, states := installPluginStateMod(t, f)
	assert.Contains(t, files, "dep-inactive.txt")
	assert.NotContains(t, files, "dep-active.txt")
	assert.Contains(t, files, "ext-inactive.txt")
	assert.NotContains(t, files, "ext-active.txt")
	assert.Contains(t, files, "base-active.txt")
	assert.Equal(t, "Inactive", states["dep.esp"])
	assert.Equal(t, "Inactive", states["ext.esp"])
}

// TestFomodPluginState_AnEnabledOrUnlistedPluginIsActive: a plugin the
// list enables - or does not list, which a deploy enables - is Active.
func TestFomodPluginState_AnEnabledOrUnlistedPluginIsActive(t *testing.T) {
	for name, plugins := range map[string]domain.ProfilePlugins{
		"enabled":  {Order: []domain.PluginEntry{{Name: "DEP.esp", Enabled: true}}},
		"unlisted": {},
	} {
		t.Run(name, func(t *testing.T) {
			f := pluginStateFixture(t, plugins)
			files, states := installPluginStateMod(t, f)
			assert.Contains(t, files, "dep-active.txt")
			assert.NotContains(t, files, "dep-inactive.txt")
			assert.Equal(t, "Active", states["dep.esp"])
		})
	}
}
