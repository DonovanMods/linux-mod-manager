package steam

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// InstalledApp is the Steam app a game's install directory belongs to
// (#501): its app id, and whether the Flatpak Steam owns the library it is
// in - such a game is started through `flatpak run com.valvesoftware.Steam`,
// since the native client knows nothing about it.
type InstalledApp struct {
	AppID   string
	Flatpak bool
}

// FlatpakSteamRoots returns the Flatpak Steam's installation roots that
// exist, in search order: today's layout, then the older data/ one.
func FlatpakSteamRoots() []string {
	base := flatpakSteamBase()
	if base == "" {
		return nil
	}
	var out []string
	for _, p := range []string{filepath.Join(base, ".local", "share", "Steam"), filepath.Join(base, "data", "Steam")} {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// flatpakSteamBase is the Flatpak Steam's per-app data directory, or ""
// with no home directory.
func flatpakSteamBase() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".var", "app", "com.valvesoftware.Steam")
}

// AppIDForInstallPath finds the Steam app whose appmanifest_*.acf names
// installPath as its install directory, across every library of the
// native Steam (FindSteamRoots) and then the Flatpak one. It is how a game
// added from Steam launches with nothing in games.yaml (#501).
//
// The match is on the directory itself - <library>/steamapps/common/
// <installdir> - with symlinks resolved on both sides, so ~/.steam/steam
// and ~/.local/share/Steam spellings agree. Anything unreadable is passed
// over: a library lmm cannot read is one it cannot launch from.
//
// Every call scans every manifest; InstallAppIndex is the cached form.
func AppIDForInstallPath(installPath string) (InstalledApp, bool) {
	var idx InstallAppIndex
	return idx.Lookup(installPath)
}

// InstallAppIndex is AppIDForInstallPath with the scan kept between calls
// (#501): GameStatus asks for every game on every read, which is the web
// UI's hot path. The scan is reused while nothing it read has changed - the
// Steam roots found, each root's libraryfolders.vdf, and each library's
// steamapps directory, whose mtime and appmanifest names move when Steam
// adds, removes or rewrites (by rename) a manifest (see pathStamp). The
// zero value is ready to use and safe for concurrent use.
type InstallAppIndex struct {
	mu     sync.Mutex
	built  bool
	roots  []steamRoot
	stamps []pathStamp
	apps   map[string]InstalledApp // realDir(install dir) -> app
	scans  int                     // full scans, for tests
}

// steamRoot is one Steam installation root, and whether it is the
// Flatpak's.
type steamRoot struct {
	path    string
	flatpak bool
}

// pathStamp is what a scan saw of one path: its mtime and size, or that it
// was missing, and for a directory the names of the appmanifests in it. An
// mtime alone is not enough: on a filesystem with coarse timestamps a
// manifest added in the same tick as the last scan leaves the directory's
// mtime unchanged, and so can an edit to libraryfolders.vdf - the names and
// the size catch what the mtime misses.
type pathStamp struct {
	path      string
	exists    bool
	modTime   time.Time
	size      int64
	manifests string
}

// stampPath stats path, and lists a directory's appmanifests.
func stampPath(path string) pathStamp {
	info, err := os.Stat(path)
	if err != nil {
		return pathStamp{path: path}
	}
	st := pathStamp{path: path, exists: true, modTime: info.ModTime(), size: info.Size()}
	if info.IsDir() {
		st.size = 0 // a directory's size says nothing about its entries
		if entries, err := os.ReadDir(path); err == nil {
			var names []string
			for _, e := range entries {
				if name := e.Name(); strings.HasPrefix(name, "appmanifest_") && strings.HasSuffix(name, ".acf") {
					names = append(names, name)
				}
			}
			st.manifests = strings.Join(names, "\x00") // ReadDir sorts by name
		}
	}
	return st
}

// Lookup is AppIDForInstallPath, scanning only when the libraries changed
// since the last scan.
func (x *InstallAppIndex) Lookup(installPath string) (InstalledApp, bool) {
	if installPath == "" {
		return InstalledApp{}, false
	}
	want := realDir(installPath)
	roots := currentSteamRoots()
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.built || !slices.Equal(roots, x.roots) || x.stale() {
		x.rebuild(roots)
	}
	app, ok := x.apps[want]
	return app, ok
}

// stale reports whether any path the last scan read has changed.
func (x *InstallAppIndex) stale() bool {
	for _, st := range x.stamps {
		if stampPath(st.path) != st {
			return true
		}
	}
	return false
}

// rebuild scans every manifest of every library of roots, in search order;
// the first manifest naming a directory wins.
func (x *InstallAppIndex) rebuild(roots []steamRoot) {
	x.scans++
	x.built, x.roots, x.stamps = true, roots, nil
	x.apps = map[string]InstalledApp{}
	for _, root := range roots {
		x.stamps = append(x.stamps, stampPath(filepath.Join(root.path, "steamapps", "libraryfolders.vdf")))
		libraries, err := GetLibraryPaths(root.path)
		if err != nil {
			continue
		}
		for _, lib := range libraries {
			steamapps := filepath.Join(lib, "steamapps")
			x.stamps = append(x.stamps, stampPath(steamapps))
			for dir, appID := range appsInLibrary(steamapps) {
				if _, taken := x.apps[dir]; !taken {
					x.apps[dir] = InstalledApp{AppID: appID, Flatpak: root.flatpak}
				}
			}
		}
	}
}

// currentSteamRoots is every Steam root, native first and then the
// Flatpak's. A native-looking root (~/.steam/steam) can be a symlink into
// the Flatpak's data; it is the Flatpak Steam all the same.
func currentSteamRoots() []steamRoot {
	var roots []steamRoot
	flatpakBase := realDir(flatpakSteamBase()) + string(filepath.Separator)
	for _, r := range FindSteamRoots() {
		roots = append(roots, steamRoot{r, strings.HasPrefix(realDir(r)+string(filepath.Separator), flatpakBase)})
	}
	for _, r := range FlatpakSteamRoots() {
		roots = append(roots, steamRoot{r, true})
	}
	return roots
}

// appsInLibrary maps the install directory (resolved by realDir) of every
// readable manifest in steamapps to its app id, the first manifest in
// directory order winning a directory two of them name.
func appsInLibrary(steamapps string) map[string]string {
	entries, err := os.ReadDir(steamapps)
	if err != nil {
		return nil
	}
	apps := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "appmanifest_") || !strings.HasSuffix(name, ".acf") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(steamapps, name))
		if err != nil {
			continue
		}
		m, err := ParseAppManifest(string(data))
		if err != nil || m.AppID == "" || m.InstallDir == "" {
			continue
		}
		dir := realDir(filepath.Join(steamapps, "common", m.InstallDir))
		if _, taken := apps[dir]; !taken {
			apps[dir] = m.AppID
		}
	}
	return apps
}

// realDir is p cleaned, with symlinks resolved where it exists.
func realDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
