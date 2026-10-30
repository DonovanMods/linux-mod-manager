package steam

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installAppHome sandboxes HOME and STEAM_ROOT (so nothing reads the
// machine's real Steam) and returns the home directory.
func installAppHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STEAM_ROOT", "")
	return home
}

// steamLibrary lays out a Steam library at lib holding the named fixture
// manifests (testdata/appmanifests) with each one's install dir created.
func steamLibrary(t *testing.T, lib string, manifests ...string) {
	t.Helper()
	steamapps := filepath.Join(lib, "steamapps")
	require.NoError(t, os.MkdirAll(steamapps, 0o755))
	for _, name := range manifests {
		data, err := os.ReadFile(filepath.Join("testdata", "appmanifests", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(steamapps, name), data, 0o644))
		m, err := ParseAppManifest(string(data))
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(steamapps, "common", m.InstallDir), 0o755))
	}
}

// libraryFolders writes root's libraryfolders.vdf naming libs.
func libraryFolders(t *testing.T, root string, libs ...string) {
	t.Helper()
	vdf := "\"libraryfolders\"\n{\n"
	for i, lib := range libs {
		vdf += "\t\"" + string(rune('0'+i)) + "\"\n\t{\n\t\t\"path\"\t\t\"" + lib + "\"\n\t}\n"
	}
	vdf += "}\n"
	require.NoError(t, os.MkdirAll(filepath.Join(root, "steamapps"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"), []byte(vdf), 0o644))
}

// #501: a game's install path names its app through the manifest whose
// installdir it is, in whichever library libraryfolders.vdf lists.
func TestAppIDForInstallPath_MatchesTheManifestAcrossLibraries(t *testing.T) {
	home := installAppHome(t)
	root := filepath.Join(home, ".local", "share", "Steam")
	second := filepath.Join(home, "games", "SteamLibrary")
	steamLibrary(t, root, "appmanifest_228980.acf")
	steamLibrary(t, second, "appmanifest_892970.acf", "appmanifest_1149460.acf")
	libraryFolders(t, root, root, second)

	app, ok := AppIDForInstallPath(filepath.Join(second, "steamapps", "common", "Icarus"))
	require.True(t, ok)
	assert.Equal(t, InstalledApp{AppID: "1149460"}, app)

	// A trailing slash, or a path through a symlink, is the same directory.
	link := filepath.Join(home, "lib-link")
	require.NoError(t, os.Symlink(second, link))
	app, ok = AppIDForInstallPath(filepath.Join(link, "steamapps", "common", "Valheim") + "/")
	require.True(t, ok)
	assert.Equal(t, "892970", app.AppID)
}

// A directory no manifest names - a subdirectory of a game, a non-Steam
// game, an empty path - has no app.
func TestAppIDForInstallPath_NoMatch(t *testing.T) {
	home := installAppHome(t)
	root := filepath.Join(home, ".local", "share", "Steam")
	steamLibrary(t, root, "appmanifest_892970.acf")

	for _, p := range []string{
		"",
		filepath.Join(root, "steamapps", "common", "Valheim", "BepInEx"),
		filepath.Join(home, "Games", "wow"),
	} {
		_, ok := AppIDForInstallPath(p)
		assert.False(t, ok, p)
	}
}

// A game in the Flatpak Steam's library is launched through the Flatpak,
// so the answer says which Steam owns it.
func TestAppIDForInstallPath_FlatpakLibrary(t *testing.T) {
	home := installAppHome(t)
	native := filepath.Join(home, ".local", "share", "Steam")
	steamLibrary(t, native, "appmanifest_1149460.acf")
	flatpak := filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam")
	external := filepath.Join(home, "ext", "FlatpakLibrary")
	steamLibrary(t, flatpak)
	steamLibrary(t, external, "appmanifest_892970.acf")
	libraryFolders(t, flatpak, flatpak, external)

	app, ok := AppIDForInstallPath(filepath.Join(external, "steamapps", "common", "Valheim"))
	require.True(t, ok)
	assert.Equal(t, InstalledApp{AppID: "892970", Flatpak: true}, app)

	app, ok = AppIDForInstallPath(filepath.Join(native, "steamapps", "common", "Icarus"))
	require.True(t, ok)
	assert.Equal(t, InstalledApp{AppID: "1149460"}, app)
}
