package bethesda

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
)

const (
	modPath = adapter.RootModPath
	root    = adapter.RootInstallPath
)

func routes(files ...string) map[string]adapter.DeployRoot {
	got := adapter.RouteRoots(New(), &domain.Game{ID: "skyrim-se"}, files)
	out := make(map[string]adapter.DeployRoot, len(files))
	for i, f := range files {
		out[f] = got[i]
	}
	return out
}

// Every Tier 1 script extender's loader and DLLs belong beside the game's
// executable; its Data/ content (already lifted by NormalizeArchive) does
// not. Matching is case-insensitive.
func TestRouteRoots_ScriptExtenders(t *testing.T) {
	for _, prefix := range []string{"skse64_", "skse_", "sksevr_", "f4se_", "f4sevr_", "sfse_", "SKSE64_"} {
		t.Run(prefix, func(t *testing.T) {
			got := routes(prefix+"loader.exe", prefix+"1_6_1170.dll", prefix+"steam_loader.dll", prefix+"readme.txt", "Scripts/SKSE.pex")
			assert.Equal(t, map[string]adapter.DeployRoot{
				prefix + "loader.exe":       root,
				prefix + "1_6_1170.dll":     root,
				prefix + "steam_loader.dll": root,
				prefix + "readme.txt":       modPath,
				"Scripts/SKSE.pex":          modPath,
			}, got)
		})
	}
}

// Only the TOP level is the game root: an SKSE plugin DLL lives in
// Data/SKSE/Plugins/ and stays under mod_path, whatever its name.
func TestRouteRoots_NestedDLLStaysInModPath(t *testing.T) {
	got := routes("SKSE/Plugins/skse64_thing.dll", "SKSE/Plugins/d3d11.dll", "foo/enbseries.ini")
	for f, r := range got {
		assert.Equal(t, modPath, r, f)
	}
}

// An ENB preset is recognised by enbseries.ini at its top level, and then
// the whole mod belongs at the game root (Vortex's "enb" mod type).
func TestRouteRoots_ENBPresetIsWholeMod(t *testing.T) {
	got := routes("enbseries.ini", "enblocal.ini", "enbseries/effect.txt", "shaders/a.fx", "d3d11.dll")
	for f, r := range got {
		assert.Equal(t, root, r, f)
	}
}

// Without enbseries.ini, the ENB binaries are still recognised one by one:
// d3d11.dll at the top level, and the enbseries/ folder.
func TestRouteRoots_ENBBinariesWithoutPreset(t *testing.T) {
	got := routes("d3d11.dll", "enbseries/a.txt", "Mod.esp")
	assert.Equal(t, map[string]adapter.DeployRoot{"d3d11.dll": root, "enbseries/a.txt": root, "Mod.esp": modPath}, got)
}

// dinput8.dll at the top level marks a mod Vortex deploys whole to the
// game root (its "dinput" mod type).
func TestRouteRoots_Dinput8IsWholeMod(t *testing.T) {
	got := routes("dinput8.dll", "plugins/thing.ini")
	assert.Equal(t, map[string]adapter.DeployRoot{"dinput8.dll": root, "plugins/thing.ini": root}, got)
}

// An ordinary Data-relative mod routes nothing to the root.
func TestRouteRoots_OrdinaryMod(t *testing.T) {
	got := routes("Mod.esp", "Mod.bsa", "meshes/a.nif", "SKSE/Plugins/x.dll", "readme.txt")
	for f, r := range got {
		assert.Equal(t, modPath, r, f)
	}
	assert.Empty(t, adapter.RouteRoots(New(), &domain.Game{}, nil))
}

// A stated Vortex mod type maps to the root its whole archive is relative
// to: "" (Vortex's default) is Data/, "dinput" and "enb" are the game
// directory. A type the adapter does not know is reported as unknown.
func TestRootForModType(t *testing.T) {
	for modType, want := range map[string]adapter.DeployRoot{"": modPath, "dinput": root, "enb": root} {
		got, ok := adapter.RootForModType(New(), &domain.Game{}, modType)
		assert.True(t, ok, modType)
		assert.Equal(t, want, got, modType)
	}
	_, ok := adapter.RootForModType(New(), &domain.Game{}, "collectionbundledmod")
	assert.False(t, ok)
}
