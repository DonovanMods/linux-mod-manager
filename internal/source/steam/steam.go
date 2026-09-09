package steam

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
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

// DetectOptions tunes a DetectGames scan (#206).
type DetectOptions struct {
	// IncludeUnknown adds every OTHER installed Steam app to the result as
	// an unknown candidate: the manifest's own name and install path, a
	// slug derived from that name, an EMPTY ModPath and no sources. It is
	// off by default so the known-only listing lmm has always produced -
	// `lmm game detect`'s prompt, GET /api/v1/games/detect - does not
	// change shape unless a caller asks for the wider list.
	IncludeUnknown bool
}

// steamToolNamePrefixes are the Steam-shipped tools and runtimes that
// install as ordinary apps under steamapps/common. Every one of them has
// an appmanifest indistinguishable from a game's, so an "every installed
// app" scan would otherwise put Proton and the redistributables at the top
// of a list whose whole job is to name games (#206).
//
// The list is deliberately SMALL and matched by whole leading word (see
// isSteamTool), never by substring: over-matching would silently hide a
// real game, which is worse than showing one extra runtime the user can
// simply not pick. That is why the Proton entries are spelled out rather
// than reduced to a bare "Proton" - "Proton Pulse" and "Protonwar" are
// games, and "Steamworld Dig 2" is not a Steamworks anything. A tool this
// misses is a one-line addition here; nothing else depends on the list.
var steamToolNamePrefixes = []string{
	"Steamworks Common Redistributables",
	"Steamworks Shared",
	"Steam Linux Runtime", // "Steam Linux Runtime 3.0 (sniper)"
	"Proton Experimental",
	"Proton Hotfix",
	"Proton Next",
	"Proton EasyAntiCheat Runtime",
}

// isSteamTool reports whether an app manifest's display name is one of
// Steam's own tools. A prefix matches only as a whole word - the name is
// the prefix exactly, or the prefix followed by a space - plus the one
// pattern a fixed list cannot cover: a numbered Proton release ("Proton
// 9.0 (Beta)"), recognised by the digit that follows the word.
func isSteamTool(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range steamToolNamePrefixes {
		p := strings.ToLower(prefix)
		if lower == p || strings.HasPrefix(lower, p+" ") {
			return true
		}
	}
	if rest, ok := strings.CutPrefix(lower, "proton "); ok && rest != "" && rest[0] >= '0' && rest[0] <= '9' {
		return true
	}
	return false
}

// deriveSlug turns a Steam display name into a candidate lmm game id:
// lower-cased, with every run of characters that is not an ASCII letter or
// digit collapsed to a single dash and the edges trimmed. That is stricter
// than core.DeriveGameID (which only lower-cases and swaps spaces),
// deliberately: a games.yaml key is a path segment, so a name like
// "S.T.A.L.K.E.R. 2" must not derive to something containing ".." and
// "Some/Game" must not derive to something containing a separator.
//
// Non-ASCII letters are dropped rather than transliterated (lmm carries no
// Unicode folding table), so a name with none left over derives to "" and
// the caller falls back to the app id - `lmm game add --game-id` and the
// web form's Game id field are how a user picks something nicer.
func deriveSlug(name string) string {
	var b strings.Builder
	dashPending := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if dashPending && b.Len() > 0 {
				b.WriteByte('-')
			}
			dashPending = false
			b.WriteRune(r)
		default:
			dashPending = true
		}
	}
	return b.String()
}

// uniqueSlug returns deriveSlug(name), disambiguated with the app id
// whenever that slug is already spoken for - by a curated known-games
// entry (taken is seeded with every one of them) or by an earlier unknown
// candidate in the same scan. Steam guarantees the app id is unique, so
// one round is always enough; a name that derives to nothing usable falls
// straight to "app-<id>".
func uniqueSlug(name, appID string, taken map[string]bool) string {
	slug := deriveSlug(name)
	if slug == "" || taken[slug] {
		slug = "app-" + appID
		if base := deriveSlug(name); base != "" {
			slug = base + "-" + appID
		}
	}
	return slug
}

// DetectGames scans Steam libraries for moddable games and returns them.
// configDir is used to load the known-games list (embedded default + optional steam-games.yaml).
// Warnings are non-fatal errors (e.g. unreadable library, parse failure) so users can diagnose.
//
// By default only games in that known-games list come back, exactly as
// they always have. opts.IncludeUnknown widens the scan to every other
// installed app (#206) - minus Steam's own tools - so a frontend can offer
// "add the game you already have installed" for a title nobody has curated
// yet. An unknown candidate carries Known=false, no sources and an EMPTY
// ModPath: detection knows where the game is installed but has no idea
// where it keeps its mods, and inventing a path here would make a guess
// look like a fact. core.GameSpecFromDetected is what defaults it.
//
// A stale manifest (the app dir is gone) warns for a KNOWN game - the user
// means to configure that one - and is skipped silently for an unknown
// one, where a large library carries plenty and none is actionable.
func DetectGames(configDir string, opts DetectOptions) (games []DetectedGame, warnings []string, err error) {
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
	seenApp := make(map[string]bool)
	// Seeded with EVERY curated slug, not just the ones this scan matched:
	// an unknown game must never derive a slug a known-games entry owns,
	// whether or not that game happens to be installed too.
	takenSlugs := make(map[string]bool, len(knownGames))
	for _, info := range knownGames {
		takenSlugs[info.Slug] = true
	}

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
				info, known := knownGames[manifest.AppID]
				if !known && (!opts.IncludeUnknown || isSteamTool(manifest.Name)) {
					continue
				}
				if seenApp[manifest.AppID] || (known && seen[info.Slug]) {
					continue
				}
				installPath := filepath.Join(libPath, "steamapps", "common", manifest.InstallDir)
				if _, err := os.Stat(installPath); err != nil {
					if known {
						warnings = append(warnings, fmt.Sprintf("%s: install dir missing: %v", installPath, err))
					}
					continue
				}
				seenApp[manifest.AppID] = true
				if !known {
					name := manifest.Name
					if strings.TrimSpace(name) == "" {
						name = manifest.InstallDir
					}
					slug := uniqueSlug(name, manifest.AppID, takenSlugs)
					takenSlugs[slug] = true
					found = append(found, DetectedGame{
						SteamAppID:  manifest.AppID,
						Slug:        slug,
						Name:        name,
						InstallPath: installPath,
					})
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
					Known:       true,
				})
			}
		}
	}

	return found, warnings, nil
}
