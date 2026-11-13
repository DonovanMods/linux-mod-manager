package core_test

// #614 F2b re-review blocker, the reviewer's reproduction as a regression:
// a profile switch to another TREE of the same version (other FOMOD
// choices) replaces the old tree, removing its extra files, exactly as a
// switch to another version does (the control).

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func switchVariantZip(t *testing.T, files map[string]string) []byte {
	p := createTestZip(t, t.TempDir(), files)
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return b
}

func TestFomodSwitch_AnotherVariantReplacesTheOldTree(t *testing.T) {
	f := newFomodFixture(t)
	cfg := strings.Replace(flowModuleConfig("4K"), `<file source="tex\4k.dds" destination="textures\t.dds"/>`, `<file source="tex\4k.dds" destination="textures\t.dds"/><file source="tex\4k.dds" destination="extra4k.txt"/>`, 1)
	f.mock.AddDownload("mod1", switchVariantZip(t, map[string]string{"fomod/ModuleConfig.xml": cfg, "core/plugin.esp": "P", "tex/2k.dds": "2K", "tex/4k.dds": "4K"}))
	f.mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod mod1", Version: "1.0", GameID: "g1"})
	ctx := context.Background()
	pm := f.svc.NewProfileManager()
	_, err := f.install(t, "mod1", map[string]domain.FomodChoices{"src:mod1": answer("4K")})
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, "g1", "default"))
	require.Equal(t, "4K", f.deployed(t, "extra4k.txt"))
	_, err = pm.Create(ctx, "g1", "alt")
	require.NoError(t, err)
	alt, _ := pm.Get(ctx, "g1", "alt")
	alt.Mods = []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.0", Fomod: answer("2K")}}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), alt))
	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "alt")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, "2K", f.deployed(t, "textures/t.dds"))
	assert.Equal(t, "", f.deployed(t, "extra4k.txt"), "variant: old tree's extra file")
}

func TestSwitch_AnotherVersionReplacesTheOldTree(t *testing.T) {
	f := newFomodFixture(t)
	f.mock.AddDownload("mod1", switchVariantZip(t, map[string]string{"a.txt": "A1", "extra.txt": "E"}))
	f.mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod mod1", Version: "1.0", GameID: "g1"})
	ctx := context.Background()
	pm := f.svc.NewProfileManager()
	_, err := f.install(t, "mod1", nil)
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, "g1", "default"))
	require.Equal(t, "E", f.deployed(t, "extra.txt"))
	f.mock.AddDownload("mod1", switchVariantZip(t, map[string]string{"a.txt": "A2"}))
	f.mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod mod1", Version: "1.1", GameID: "g1"})
	_, err = pm.Create(ctx, "g1", "alt")
	require.NoError(t, err)
	alt, _ := pm.Get(ctx, "g1", "alt")
	alt.Mods = []domain.ModReference{{SourceID: "src", ModID: "mod1", Version: "1.1"}}
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), alt))
	plan, err := f.svc.PlanProfileSwitch(ctx, f.game, "alt")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, plan, nil)
	require.NoError(t, err)
	assert.Equal(t, "A2", f.deployed(t, "a.txt"))
	assert.Equal(t, "", f.deployed(t, "extra.txt"), "version: old tree's extra file")
}
