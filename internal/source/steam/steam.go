package steam

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DetectedGame is a Steam game found on disk that lmm knows how to configure.
type DetectedGame struct {
	SteamAppID  string // Steam App ID
	Slug        string // lmm game ID (from known games list)
	Name        string // Display name
	InstallPath string // Absolute path to game install (e.g. .../common/Skyrim Special Edition)
	ModPath     string // Absolute path to mod directory (InstallPath + ModPath relative)
	NexusID     string // NexusMods game domain ID
}

// FindSteamRoots returns candidate Steam installation roots in search order.
func FindSteamRoots() []string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".local", "share", "Steam"),
	}
	if p := os.Getenv("STEAM_ROOT"); p != "" {
		candidates = append([]string{p}, candidates...)
	}
	var out []string
	for _, p := range candidates {
		if p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			continue
		}
		out = append(out, p)
	}
	return out
}

// GetLibraryPaths returns all Steam library paths from a Steam root (reading libraryfolders.vdf).
func GetLibraryPaths(steamRoot string) ([]string, error) {
	vdfPath := filepath.Join(steamRoot, "steamapps", "libraryfolders.vdf")
	data, err := os.ReadFile(vdfPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Single library: the steam root itself is the library
			return []string{steamRoot}, nil
		}
		return nil, fmt.Errorf("reading libraryfolders: %w", err)
	}
	root, err := ParseVDF(strings.NewReader(string(data)))
	if err != nil {
		return nil, fmt.Errorf("parsing libraryfolders: %w", err)
	}
	paths := getLibraryPathsFromMap(root)
	if len(paths) == 0 {
		return []string{steamRoot}, nil
	}
	return paths, nil
}

// getLibraryPathsFromMap extracts library paths from a parsed libraryfolders vdf map.
func getLibraryPathsFromMap(root VDFMap) []string {
	return getLibraryPaths(root)
}

// DetectGames scans Steam libraries for known moddable games and returns them.
// configDir is used to load the known-games list (embedded default + optional steam-games.yaml).
func DetectGames(configDir string) ([]DetectedGame, error) {
	knownGames, err := LoadKnownGames(configDir)
	if err != nil {
		return nil, err
	}
	steamRoots := FindSteamRoots()
	if len(steamRoots) == 0 {
		return nil, nil
	}
	var found []DetectedGame
	seen := make(map[string]bool) // slug -> true to dedupe same game in multiple libraries

	for _, steamRoot := range steamRoots {
		libraries, err := GetLibraryPaths(steamRoot)
		if err != nil {
			continue
		}
		for _, libPath := range libraries {
			steamapps := filepath.Join(libPath, "steamapps")
			entries, err := os.ReadDir(steamapps)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				name := e.Name()
				if !strings.HasPrefix(name, "appmanifest_") || !strings.HasSuffix(name, ".acf") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(steamapps, name))
				if err != nil {
					continue
				}
				manifest, err := ParseAppManifest(string(data))
				if err != nil || manifest.AppID == "" || manifest.InstallDir == "" {
					continue
				}
				info, ok := knownGames[manifest.AppID]
				if !ok {
					continue
				}
				if seen[info.Slug] {
					continue
				}
				installPath := filepath.Join(libPath, "steamapps", "common", manifest.InstallDir)
				if _, err := os.Stat(installPath); err != nil {
					continue
				}
				modPath := installPath
				if info.ModPath != "" {
					modPath = filepath.Join(installPath, info.ModPath)
				}
				seen[info.Slug] = true
				found = append(found, DetectedGame{
					SteamAppID:  manifest.AppID,
					Slug:        info.Slug,
					Name:        info.Name,
					InstallPath: installPath,
					ModPath:     modPath,
					NexusID:     info.NexusID,
				})
			}
		}
	}

	return found, nil
}
