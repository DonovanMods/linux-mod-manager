package modtypes

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each adapter knows exactly the types its table lists (#635), and reports
// every other one as unknown - the default type "" included, which no table
// lists: core routes an untyped mod by RouteRoots, never by RootForModType - - which core refuses rather than deploying it
// under the mod_path.
func TestRootForModType(t *testing.T) {
	cases := []struct {
		a    adapter.GameAdapter
		want map[string]adapter.DeployRoot
		not  []string
	}{
		{Witcher3(), map[string]adapter.DeployRoot{
			"witcher3tl": adapter.RootInstallPath,
			"dinput":     adapter.RootInstallPath,
		}, []string{"", "witcher3dlc", "witcher3menumodroot", "witcher3menumoddocuments", "w3modlimitpatcher", "bg3-loose", "starfield-data-folder"}},
		{BaldursGate3(), map[string]adapter.DeployRoot{
			"bg3-loose":    adapter.RootModPath,
			"bg3-replacer": adapter.RootModPath,
		}, []string{"", "dinput", "bg3-bg3se", "bg3-lslib-divine-tool", "witcher3tl"}},
	}
	for _, c := range cases {
		for typ, want := range c.want {
			got, ok := adapter.RootForModType(c.a, &domain.Game{ID: "any"}, typ)
			assert.True(t, ok, "%s knows %q", c.a.ID(), typ)
			assert.Equal(t, want, got, "%s: %q", c.a.ID(), typ)
		}
		for _, typ := range c.not {
			_, ok := adapter.RootForModType(c.a, &domain.Game{ID: "any"}, typ)
			assert.False(t, ok, "%s does not know %q", c.a.ID(), typ)
		}
	}
}

// The Witcher 3's adapter has no per-file rules: an untyped mod deploys as
// packaged, under the mod_path, exactly as the generic adapter deploys it -
// which is its adapter.LayoutNeutral claim. Baldur's Gate 3's does not
// make the claim (#636).
func TestNoPerFileRules(t *testing.T) {
	assert.True(t, adapter.IsLayoutNeutral(Witcher3()))
	assert.False(t, adapter.IsLayoutNeutral(BaldursGate3()), "a .pak deploys into the prefix")
	for _, a := range []adapter.GameAdapter{Witcher3()} {
		files := []string{"bin/x64/dinput8.dll", "mods/modA/content/blob0.bundle", "Public/x.lsx"}
		assert.Equal(t, []adapter.DeployRoot{adapter.RootModPath, adapter.RootModPath, adapter.RootModPath},
			adapter.RouteRoots(a, &domain.Game{}, files), a.ID())
		l, err := a.NormalizeArchive(adapter.NormalizeRequest{Members: files})
		assert.NoError(t, err)
		assert.False(t, l.Applies(), "%s leaves an archive as packaged", a.ID())
	}
}

func TestIdentity(t *testing.T) {
	assert.Equal(t, "witcher3", Witcher3().ID())
	assert.Equal(t, "baldurs-gate-3", BaldursGate3().ID())
	ids := map[string]bool{}
	for _, a := range All() {
		assert.NotEmpty(t, a.Label())
		assert.False(t, ids[a.ID()], "IDs are unique")
		ids[a.ID()] = true
	}
	assert.Len(t, ids, 2)
}

// #636: Baldur's Gate 3 loads a .pak mod from the Mods folder in its
// prefix (Vortex's default type, modsPath), flat; a pak named like one of
// the game's own is a replacer for Data/, and loose files stay in Data/.
func TestBG3_RoutesPaksIntoThePrefix(t *testing.T) {
	a := BaldursGate3()
	g := &domain.Game{ID: "baldurs-gate-3"}
	files := []string{"MyMod.pak", "Wrapper/Other.PAK", "Gustav.pak", "Public/Mod/x.lsx", "info.json"}
	assert.Equal(t, []adapter.DeployRoot{adapter.RootPrefix, adapter.RootPrefix, adapter.RootModPath, adapter.RootModPath, adapter.RootModPath},
		adapter.RouteRoots(a, g, files))
	got, ok := adapter.PrefixPath(a, g, "Wrapper/Other.PAK")
	assert.True(t, ok)
	assert.Equal(t, "AppData/Local/Larian Studios/Baldur's Gate 3/Mods/Other.PAK", got, "the wrapper folder is dropped")
	assert.True(t, adapter.UsesPrefix(a, g))
	l, err := a.NormalizeArchive(adapter.NormalizeRequest{Members: files})
	assert.NoError(t, err)
	assert.False(t, l.Applies(), "the cache keeps the archive as packaged")
}

// Vortex never deploys a BG3 mod's info.json.
func TestBG3_SkipsInfoJSON(t *testing.T) {
	g := &domain.Game{}
	assert.Equal(t, adapter.RouteSkip, adapter.Route(BaldursGate3(), g, "MyMod/info.json"))
	assert.Equal(t, adapter.RouteSkip, adapter.Route(BaldursGate3(), g, "Info.JSON"))
	assert.Equal(t, adapter.RouteLink, adapter.Route(BaldursGate3(), g, "MyMod.pak"))
}

// The advice names what lmm does not do yet (modsettings.lsx, #646), and
// only when a .pak is deployed into the prefix.
func TestBG3_LaunchAdviceNamesModsettings(t *testing.T) {
	g := &domain.Game{}
	none := adapter.LaunchAdvice(BaldursGate3(), g, []adapter.DeployedMod{{Name: "loose", Files: []string{"Public/x.lsx"}}}, "")
	assert.Empty(t, none.Lines)
	adv := adapter.LaunchAdvice(BaldursGate3(), g, []adapter.DeployedMod{{Name: "pak",
		PrefixFiles: []string{"AppData/Local/Larian Studios/Baldur's Gate 3/Mods/MyMod.pak"}}}, "")
	assert.Empty(t, adv.Args)
	require.NotEmpty(t, adv.Lines)
	assert.Contains(t, adv.Lines[0], "modsettings.lsx")
	assert.Contains(t, adv.Lines[0], "lmm doesn't write that file yet")
}
