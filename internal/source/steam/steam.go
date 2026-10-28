package steam

import (
	"fmt"
	"log/slog"
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
	// NoWorkshop suppresses #269's Steam Workshop prefill: a candidate
	// whose appworkshop manifest declares installed items normally gains
	// `steamworkshop: <appid>` in its Sources map and a WorkshopItems
	// count. Off by default, because a game with items already downloaded
	// is exactly the case where tracking them is free; a user who does not
	// want lmm to know about them says so with `lmm game detect
	// --no-workshop`.
	NoWorkshop bool

	// IncludeUnknown adds every OTHER installed Steam app to the result as
	// an unknown candidate: the manifest's own name and install path, a
	// slug derived from that name, an EMPTY ModPath and no sources. It is
	// off by default so the known-only listing lmm has always produced -
	// `lmm game detect`'s prompt, GET /api/v1/games/detect - does not
	// change shape unless a caller asks for the wider list.
	IncludeUnknown bool

	// Logger receives the scan's NOTICES: facts worth recording that are
	// not the user's problem and are not lmm's fault, so they must not
	// reach a terminal as "Warning:" on every run (#368). Today there is
	// exactly one - a libraryfolders.vdf entry whose directory is gone,
	// which every scan re-discovers and no user action can fix, since
	// Steam wrote it and Steam will rewrite it.
	//
	// A real WARNING - a library that exists but cannot be read - still
	// comes back in the warnings slice, where a caller shows it without
	// being asked. Nil is the ordinary case and discards.
	Logger *slog.Logger
}

// logger is opts.Logger, or a discarding one - the ordinary case, since
// every caller that has nothing to do with a notice passes none.
func (o DetectOptions) logger() *slog.Logger {
	if o.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return o.Logger
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

// workshopItemCount reports how many Workshop items Steam has installed
// for appID inside a library, and whether a manifest existed at all (#269).
//
// It answers a narrower question than internal/source/steamworkshop's own
// parser - "are there any, and how many" rather than "what are they" - and
// deliberately keeps its own tiny reader rather than importing that package:
// steamworkshop already imports THIS one for the VDF dialect and the
// library walk, so the dependency cannot run both ways.
//
// An EMPTY-STUB manifest (WorkshopItemsInstalled {}, the shape Steam leaves
// for a workshop-capable app with nothing subscribed) reports 0, which is
// what suppresses the prefill: nothing is lost, since `lmm game edit` and
// the web UI's sources map can add the mapping later.
func workshopItemCount(libPath, appID string) int {
	data, err := os.ReadFile(filepath.Join(libPath, "steamapps", "workshop", "appworkshop_"+appID+".acf"))
	if err != nil {
		return 0
	}
	root, err := ParseVDF(strings.NewReader(string(data)))
	if err != nil {
		return 0
	}
	block, ok := root["AppWorkshop"].(VDFMap)
	if !ok {
		return 0
	}
	installed, ok := block["WorkshopItemsInstalled"].(VDFMap)
	if !ok {
		return 0
	}
	return len(installed)
}

// withWorkshopPrefill stamps #269's Steam Workshop mapping onto a candidate
// when this library has Workshop items installed for it.
//
// The mapping is ADDED to whatever sources the candidate already carries -
// a curated entry keeps its own, and the nil-Sources case is left alone
// here because core.GameFromDetected/GameSpecFromDetected derive
// {nexusmods: NexusID} from a nil map; a map materialised here would
// suppress that derivation. So a curated game with a nil map gets its
// workshop entry only once the derivation has happened, which is why the
// derivation is what this seeds FROM rather than replaces.
//
// An UNKNOWN game (Known false, empty ModPath) gets the workshop entry as
// its ONLY source, which is exactly right: lmm cannot deploy to it, but it
// can track what Steam already downloaded.
func withWorkshopPrefill(g DetectedGame, libPath string, noWorkshop bool) DetectedGame {
	if noWorkshop {
		return g
	}
	count := workshopItemCount(libPath, g.SteamAppID)
	if count == 0 {
		return g
	}
	g.WorkshopItems = count
	sources := make(map[string]string, len(g.Sources)+2)
	for k, v := range g.Sources {
		sources[k] = v
	}
	// Preserve the pre-#269 derivation for a curated entry that declared
	// only a nexus id: adding a key to a nil map would otherwise silence it.
	if len(g.Sources) == 0 && g.NexusID != "" {
		sources["nexusmods"] = g.NexusID
	}
	sources["steamworkshop"] = g.SteamAppID
	g.Sources = sources
	return g
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
// candidate in the same scan. A name that derives to nothing usable falls
// straight to "app-<id>".
//
// It LOOPS rather than disambiguating once (Unit 9 gate, Minor 9). Steam
// guarantees the APP ID is unique, but not the string built from it: a
// curated entry, or a differently-named app, can already hold exactly
// "half-life-70" or "app-70", and a single round would then hand back a
// slug that is taken - the one thing this function exists to prevent.
// Later rounds append an ordinal, which terminates because each candidate
// is distinct and taken is finite.
func uniqueSlug(name, appID string, taken map[string]bool) string {
	slug := deriveSlug(name)
	if slug == "" || taken[slug] {
		slug = "app-" + appID
		if base := deriveSlug(name); base != "" {
			slug = base + "-" + appID
		}
	}
	base := slug
	for n := 2; taken[slug]; n++ {
		slug = fmt.Sprintf("%s-%d", base, n)
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
				// A MISSING library is Steam's own bookkeeping, not a
				// problem lmm found: libraryfolders.vdf still lists a drive
				// that is unplugged or a library removed outside the
				// client. Shouting "Warning:" about it on every `lmm game
				// detect` reads like an lmm fault and there is nothing to
				// act on, so it is a notice (#368). Anything else -
				// permissions, a broken mount - is about a directory that
				// IS there, and stays a warning.
				if os.IsNotExist(err) {
					opts.logger().Info("skipping a Steam library that is listed in libraryfolders.vdf but missing",
						"library", libPath)
					continue
				}
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
					found = append(found, withWorkshopPrefill(DetectedGame{
						SteamAppID:  manifest.AppID,
						Slug:        slug,
						Name:        name,
						InstallPath: installPath,
					}, libPath, opts.NoWorkshop))
					continue
				}
				modPath := installPath
				if info.ModPath != "" {
					modPath = filepath.Join(installPath, info.ModPath)
				}
				seen[info.Slug] = true
				found = append(found, withWorkshopPrefill(DetectedGame{
					SteamAppID:  manifest.AppID,
					Slug:        info.Slug,
					Name:        info.Name,
					InstallPath: installPath,
					ModPath:     modPath,
					NexusID:     info.NexusID,
					DeployMode:  info.DeployMode,
					Adapter:     info.Adapter,
					Sources:     info.Sources,
					Loader:      detectedLoader(info.Loader),
					Known:       true,
				}, libPath, opts.NoWorkshop))
			}
		}
	}

	return found, warnings, nil
}

// detectedLoader converts a known-games entry's loader block into the domain
// declaration a candidate carries (#416), or nil when the entry declares
// none. A fresh value each time: the candidate's declaration travels into
// domain.Game and then into games.yaml, and a pointer shared with the
// process-wide known-games map would make an edit to one game's loader an
// edit to every game curated from the same entry.
//
// Runtime and Bootstrap stay at their zero values ("not answered yet") -
// LoaderInfo deliberately carries neither, because a catalog cannot know
// them and `lmm game show` answers them from disk.
func detectedLoader(info *LoaderInfo) *domain.GameLoader {
	if info == nil {
		return nil
	}
	return &domain.GameLoader{Kind: info.Kind, Version: info.Version}
}
