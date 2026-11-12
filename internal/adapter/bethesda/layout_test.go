package bethesda

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// layoutOf runs NormalizeArchive and returns every member's destination
// ("" for a dropped one), whether the Layout applies, and its warnings.
func layoutOf(t *testing.T, members ...string) (map[string]string, bool, []string) {
	t.Helper()
	l, err := New().NormalizeArchive(adapter.NormalizeRequest{Game: &domain.Game{ID: "skyrim-se"}, Members: members})
	require.NoError(t, err)
	out := make(map[string]string, len(members))
	for _, m := range members {
		dest, ok := l.Rewrite(m)
		if !ok {
			dest = ""
		}
		out[m] = dest
	}
	return out, l.Applies(), l.Warnings
}

// A mod_path (Data/)-relative archive - the shape almost every Bethesda
// mod comes in - is left exactly as packaged.
func TestNormalizeArchive_DataRelativeIsIdentity(t *testing.T) {
	got, applies, warnings := layoutOf(t, "SkyUI_SE.esp", "SkyUI_SE.bsa", "interface/skyui/config.txt")
	assert.False(t, applies)
	assert.Empty(t, warnings)
	assert.Equal(t, map[string]string{
		"SkyUI_SE.esp": "SkyUI_SE.esp", "SkyUI_SE.bsa": "SkyUI_SE.bsa", "interface/skyui/config.txt": "interface/skyui/config.txt",
	}, got)
}

// An archive rooted at the game directory carries a Data/ folder; its
// content belongs in mod_path, so the Data/ prefix is lifted. Matching is
// case-insensitive, as Windows-authored archives are.
func TestNormalizeArchive_LiftsDataFolder(t *testing.T) {
	for _, data := range []string{"Data", "data", "DATA"} {
		t.Run(data, func(t *testing.T) {
			got, applies, warnings := layoutOf(t, data+"/Mod.esp", data+"/meshes/a.nif", "readme.txt")
			assert.True(t, applies)
			assert.Empty(t, warnings)
			assert.Equal(t, map[string]string{
				data + "/Mod.esp": "Mod.esp", data + "/meshes/a.nif": "meshes/a.nif", "readme.txt": "readme.txt",
			}, got)
		})
	}
}

// SKSE64's own archive: a version-named wrapper holding the loader and its
// DLLs, Data/Scripts, and the source code. The wrapper goes, Data/ is
// lifted, and the loader and DLLs stay at the entry's top level, where
// RouteRoots recognises them.
func TestNormalizeArchive_ScriptExtenderArchive(t *testing.T) {
	members := []string{
		"skse64_2_02_06/Data/Scripts/SKSE.pex",
		"skse64_2_02_06/skse64_1_6_1170.dll",
		"skse64_2_02_06/skse64_loader.exe",
		"skse64_2_02_06/skse64_readme.txt",
		"skse64_2_02_06/src/skse64/main.cpp",
	}
	got, applies, warnings := layoutOf(t, members...)
	assert.True(t, applies)
	assert.Empty(t, warnings)
	assert.Equal(t, map[string]string{
		"skse64_2_02_06/Data/Scripts/SKSE.pex": "Scripts/SKSE.pex",
		"skse64_2_02_06/skse64_1_6_1170.dll":   "skse64_1_6_1170.dll",
		"skse64_2_02_06/skse64_loader.exe":     "skse64_loader.exe",
		"skse64_2_02_06/skse64_readme.txt":     "skse64_readme.txt",
		"skse64_2_02_06/src/skse64/main.cpp":   "src/skse64/main.cpp",
	}, got)
}

// An ENB preset in a wrapper folder: the wrapper goes, so enbseries.ini is
// at the top level where RouteRoots finds it.
func TestNormalizeArchive_ENBPresetWrapper(t *testing.T) {
	got, applies, _ := layoutOf(t, "Preset v2/enbseries.ini", "Preset v2/enbseries/effect.txt", "Preset v2/enblocal.ini")
	assert.True(t, applies)
	assert.Equal(t, map[string]string{
		"Preset v2/enbseries.ini":        "enbseries.ini",
		"Preset v2/enbseries/effect.txt": "enbseries/effect.txt",
		"Preset v2/enblocal.ini":         "enblocal.ini",
	}, got)
}

// A single top-level folder is NOT a wrapper unless what is inside it is
// recognisably game-root-relative: "textures/" or "SKSE/" at the top of a
// Data-relative archive is ordinary mod content.
func TestNormalizeArchive_OrdinaryFolderIsNotAWrapper(t *testing.T) {
	for name, members := range map[string][]string{
		"textures":    {"textures/a.dds", "textures/b/c.dds"},
		"skse plugin": {"SKSE/Plugins/po3_Tweaks.dll", "SKSE/Plugins/po3_Tweaks.ini"},
		"mod folder":  {"My Mod/My Mod.esp", "My Mod/meshes/a.nif"},
	} {
		t.Run(name, func(t *testing.T) {
			got, applies, warnings := layoutOf(t, members...)
			assert.False(t, applies)
			assert.Empty(t, warnings)
			for _, m := range members {
				assert.Equal(t, m, got[m])
			}
		})
	}
}

// Lifting Data/ must not merge two members into one destination: an
// archive with both Data/Mod.esp and Mod.esp is left as packaged, with a
// warning, rather than refused by core's validator or silently losing one.
func TestNormalizeArchive_LiftCollisionIsLeftAsPackaged(t *testing.T) {
	got, applies, warnings := layoutOf(t, "Data/Mod.esp", "Mod.esp")
	assert.False(t, applies)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "Mod.esp")
	assert.Equal(t, map[string]string{"Data/Mod.esp": "Data/Mod.esp", "Mod.esp": "Mod.esp"}, got)
}

// A file literally named "Data" (no folder) is not a folder to lift.
func TestNormalizeArchive_DataFileIsNotLifted(t *testing.T) {
	got, applies, _ := layoutOf(t, "Data", "Mod.esp")
	assert.False(t, applies)
	assert.Equal(t, "Data", got["Data"])
}

// No members, no opinion.
func TestNormalizeArchive_Empty(t *testing.T) {
	l, err := New().NormalizeArchive(adapter.NormalizeRequest{Game: &domain.Game{}})
	require.NoError(t, err)
	assert.False(t, l.Applies())
}

// An archive the adapter leaves untouched gets no collision warning, even
// when two members differ only in case: nothing was rewritten, so nothing
// the adapter did could have merged them.
func TestNormalizeArchive_UntouchedCaseVariantsAreNotWarned(t *testing.T) {
	got, applies, warnings := layoutOf(t, "Textures/a.dds", "textures/a.dds")
	assert.False(t, applies)
	assert.Empty(t, warnings)
	assert.Equal(t, map[string]string{"Textures/a.dds": "Textures/a.dds", "textures/a.dds": "textures/a.dds"}, got)
}

// A Data-relative mod whose files all sit under one folder - an SKSE plugin
// is the common case - is never mistaken for a wrapper: SKSE/ is Data
// content, and stripping it would deploy the DLL to Data/Plugins/.
func TestNormalizeArchive_SingleDataFolderIsNotStripped(t *testing.T) {
	got, applies, warnings := layoutOf(t, "SKSE/Plugins/x.dll")
	assert.False(t, applies)
	assert.Empty(t, warnings)
	assert.Equal(t, map[string]string{"SKSE/Plugins/x.dll": "SKSE/Plugins/x.dll"}, got)
}
