package steam

import (
	"os"
	"path/filepath"
	"strings"
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
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	base := filepath.Join(home, ".var", "app", "com.valvesoftware.Steam")
	var out []string
	for _, p := range []string{filepath.Join(base, ".local", "share", "Steam"), filepath.Join(base, "data", "Steam")} {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			out = append(out, p)
		}
	}
	return out
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
func AppIDForInstallPath(installPath string) (InstalledApp, bool) {
	if installPath == "" {
		return InstalledApp{}, false
	}
	want := realDir(installPath)
	type steamRoot struct {
		path    string
		flatpak bool
	}
	var roots []steamRoot
	for _, r := range FindSteamRoots() {
		roots = append(roots, steamRoot{r, false})
	}
	for _, r := range FlatpakSteamRoots() {
		roots = append(roots, steamRoot{r, true})
	}
	for _, root := range roots {
		libraries, err := GetLibraryPaths(root.path)
		if err != nil {
			continue
		}
		for _, lib := range libraries {
			if appID, ok := appInLibrary(lib, want); ok {
				return InstalledApp{AppID: appID, Flatpak: root.flatpak}, true
			}
		}
	}
	return InstalledApp{}, false
}

// appInLibrary returns the app id of the manifest in lib whose install
// directory is want (already resolved by realDir).
func appInLibrary(lib, want string) (string, bool) {
	steamapps := filepath.Join(lib, "steamapps")
	entries, err := os.ReadDir(steamapps)
	if err != nil {
		return "", false
	}
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
		if realDir(filepath.Join(steamapps, "common", m.InstallDir)) == want {
			return m.AppID, true
		}
	}
	return "", false
}

// realDir is p cleaned, with symlinks resolved where it exists.
func realDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
