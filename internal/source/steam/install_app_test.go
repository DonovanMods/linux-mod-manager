package steam

import (
	"os"
	"path/filepath"
	"sync"
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

// ~/.steam/steam pointing into the Flatpak's data is still the Flatpak
// Steam: the root is judged by where it really is, not by its name.
func TestAppIDForInstallPath_NativeRootLinkedIntoTheFlatpak(t *testing.T) {
	home := installAppHome(t)
	flatpak := filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam")
	steamLibrary(t, flatpak, "appmanifest_892970.acf")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".steam"), 0o755))
	require.NoError(t, os.Symlink(flatpak, filepath.Join(home, ".steam", "steam")))

	app, ok := AppIDForInstallPath(filepath.Join(home, ".steam", "steam", "steamapps", "common", "Valheim"))
	require.True(t, ok)
	assert.Equal(t, InstalledApp{AppID: "892970", Flatpak: true}, app)
}

// The index keeps its scan while the libraries are unchanged - GameStatus
// asks on every read - and rescans when Steam adds a manifest or a library.
func TestInstallAppIndex_CachesUntilTheLibrariesChange(t *testing.T) {
	home := installAppHome(t)
	root := filepath.Join(home, ".local", "share", "Steam")
	second := filepath.Join(home, "games", "SteamLibrary")
	steamLibrary(t, root, "appmanifest_892970.acf")
	libraryFolders(t, root, root)

	var idx InstallAppIndex
	valheim := filepath.Join(root, "steamapps", "common", "Valheim")
	app, ok := idx.Lookup(valheim)
	require.True(t, ok)
	assert.Equal(t, "892970", app.AppID)
	_, ok = idx.Lookup(filepath.Join(root, "steamapps", "common", "Icarus"))
	assert.False(t, ok)
	assert.Equal(t, 1, idx.scans, "a second lookup reuses the scan")

	// A new manifest in a known library.
	steamLibrary(t, root, "appmanifest_1149460.acf")
	app, ok = idx.Lookup(filepath.Join(root, "steamapps", "common", "Icarus"))
	require.True(t, ok)
	assert.Equal(t, "1149460", app.AppID)
	assert.Equal(t, 2, idx.scans)

	// A new library in libraryfolders.vdf.
	steamLibrary(t, second, "appmanifest_228980.acf")
	libraryFolders(t, root, root, second)
	app, ok = idx.Lookup(filepath.Join(second, "steamapps", "common", "Steamworks Shared"))
	require.True(t, ok, "the new library is scanned")
	assert.Equal(t, "228980", app.AppID)
	assert.Equal(t, 3, idx.scans)

	// A removed manifest.
	require.NoError(t, os.Remove(filepath.Join(root, "steamapps", "appmanifest_892970.acf")))
	_, ok = idx.Lookup(valheim)
	assert.False(t, ok)
}

// A filesystem with coarse timestamps can leave a directory's mtime
// unchanged when a manifest lands in the same tick as the last scan - CI saw
// exactly that. The index notices the new file by name, whatever the mtime
// says, and an edited libraryfolders.vdf by its size.
func TestInstallAppIndex_NoticesChangesTheMtimeMisses(t *testing.T) {
	home := installAppHome(t)
	root := filepath.Join(home, ".local", "share", "Steam")
	second := filepath.Join(home, "games", "SteamLibrary")
	steamLibrary(t, root, "appmanifest_892970.acf")
	libraryFolders(t, root, root)
	steamapps := filepath.Join(root, "steamapps")
	vdf := filepath.Join(steamapps, "libraryfolders.vdf")

	var idx InstallAppIndex
	_, ok := idx.Lookup(filepath.Join(steamapps, "common", "Valheim"))
	require.True(t, ok)

	// keepMtime puts path's mtime back to what it was, as a same-tick
	// write would leave it.
	keepMtime := func(path string, write func()) {
		t.Helper()
		info, err := os.Stat(path)
		require.NoError(t, err)
		write()
		require.NoError(t, os.Chtimes(path, info.ModTime(), info.ModTime()))
	}

	keepMtime(steamapps, func() { steamLibrary(t, root, "appmanifest_1149460.acf") })
	app, ok := idx.Lookup(filepath.Join(steamapps, "common", "Icarus"))
	require.True(t, ok, "a manifest added in the same mtime tick is still found")
	assert.Equal(t, "1149460", app.AppID)

	steamLibrary(t, second, "appmanifest_228980.acf")
	keepMtime(vdf, func() { libraryFolders(t, root, root, second) })
	app, ok = idx.Lookup(filepath.Join(second, "steamapps", "common", "Steamworks Shared"))
	require.True(t, ok, "a library added in the same mtime tick is still scanned")
	assert.Equal(t, "228980", app.AppID)
}

// One index serves concurrent readers (lmm serve's request goroutines).
func TestInstallAppIndex_ConcurrentLookups(t *testing.T) {
	home := installAppHome(t)
	root := filepath.Join(home, ".local", "share", "Steam")
	steamLibrary(t, root, "appmanifest_892970.acf")
	var idx InstallAppIndex
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			app, ok := idx.Lookup(filepath.Join(root, "steamapps", "common", "Valheim"))
			assert.True(t, ok)
			assert.Equal(t, "892970", app.AppID)
		})
	}
	wg.Wait()
	assert.Equal(t, 1, idx.scans)
}
