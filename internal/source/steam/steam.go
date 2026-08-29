package steam

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// DetectedGame is a Steam game found on disk that lmm knows how to
// configure. The type itself moved to domain.DetectedGame (v2 Phase 2 Task
// 21, Ruling 8) so internal/app and internal/core can consume detected
// games without importing this concrete source; this alias keeps every
// existing steam.DetectedGame reference (this file's DetectGames included)
// valid unchanged.
type DetectedGame = domain.DetectedGame

// FindSteamRoots returns candidate Steam installation roots in search order.
// On many real Linux installs ~/.steam/steam is a symlink to
// ~/.local/share/Steam (or the reverse) - both paths exist and both pass the
// existence check below, but they are the same real directory. Scanning both
// would run DetectGames' whole library scan twice against identical data,
// duplicating every warning it produces (and doing twice the redundant
// work). Resolved-path dedup keeps only the first candidate (this list's own
// priority order) whenever a later one turns out to be the same real
// directory as one already kept.
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
	seenReal := make(map[string]bool)
	for _, p := range candidates {
		if p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			continue
		}
		// realPath falls back to p itself if it can't be resolved (e.g. a
		// permission error mid-resolution) - the existence check above
		// already confirmed p is a real, statable directory, so it is
		// never silently dropped.
		realPath, err := filepath.EvalSymlinks(p)
		if err != nil {
			realPath = p
		}
		if seenReal[realPath] {
			continue
		}
		seenReal[realPath] = true
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
// Warnings are non-fatal errors (e.g. unreadable library, parse failure) so users can diagnose.
func DetectGames(configDir string) (games []DetectedGame, warnings []string, err error) {
	knownGames, err := LoadKnownGames(configDir)
	if err != nil {
		return nil, nil, err
	}
	steamRoots := FindSteamRoots()
	if len(steamRoots) == 0 {
		return nil, nil, nil
	}
	var found []DetectedGame
	seen := make(map[string]bool)

	for _, steamRoot := range steamRoots {
		libraries, err := GetLibraryPaths(steamRoot)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", steamRoot, err))
			continue
		}
		for _, libPath := range libraries {
			steamapps := filepath.Join(libPath, "steamapps")
			entries, err := os.ReadDir(steamapps)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", steamapps, err))
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
				acfPath := filepath.Join(steamapps, name)
				data, err := os.ReadFile(acfPath)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("%s: %v", acfPath, err))
					continue
				}
				manifest, err := ParseAppManifest(string(data))
				if err != nil || manifest.AppID == "" || manifest.InstallDir == "" {
					if err != nil {
						warnings = append(warnings, fmt.Sprintf("%s: parse: %v", acfPath, err))
					}
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
					warnings = append(warnings, fmt.Sprintf("%s: install dir missing: %v", installPath, err))
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
					DeployMode:  info.DeployMode,
					Sources:     info.Sources,
				})
			}
		}
	}

	return found, warnings, nil
}
