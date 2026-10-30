package battlenet

import (
	"embed"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/safeyaml"
)

//go:embed data/battlenet-games.yaml
var defaultGamesFS embed.FS

const defaultGamesPath = "data/battlenet-games.yaml"

// overrideFile is the user's catalog override in the config dir, beside
// steam-games.yaml.
const overrideFile = "battlenet-games.yaml"

// GameInfo describes a Battle.net game lmm knows how to configure, keyed by
// its product code (#545).
type GameInfo struct {
	Slug string // lmm game id, e.g. "wow-classic"
	Name string // display name
	// InstallDir is the folder under the product's install root the game
	// lives in - World of Warcraft's flavor folder, "_classic_". It becomes
	// the game's install path, so every flavor is its own game. "" is the
	// root itself.
	InstallDir string
	// ModPathBase is what ModPath is relative to: ModPathBaseInstall
	// (InstallDir, the default) or ModPathBaseDocuments, the prefix user's
	// Documents folder (#546).
	ModPathBase string
	// ModPath is relative to ModPathBase; "" is that folder itself. Under
	// ModPathBaseDocuments its first element is the game's own folder in
	// Documents ("Warcraft III"), which is how the detector tells which of
	// a prefix's users plays the game.
	ModPath string
	// Adapter is the generated games.yaml entry's `adapter:` (#353), ""
	// for the default.
	Adapter string
	// Sources is the generated games.yaml entry's complete source map.
	Sources map[string]string
	// CurseForgeVersionTypes is #544's flavor setting; nil accepts every
	// file.
	CurseForgeVersionTypes []int
	// LaunchCode is Battle.net's --exec="launch <code>" code, when lmm knows
	// it. "" opens the client.
	LaunchCode string
}

// ModPathBaseInstall and ModPathBaseDocuments are mod_path_base's values.
const (
	ModPathBaseInstall   = ""
	ModPathBaseDocuments = "documents"
)

// gamesYAML is the on-disk format: product code -> entry.
type gamesYAML map[string]gameYAML

// gameYAML is one entry of that format.
type gameYAML struct {
	Slug       string `yaml:"slug"`
	Name       string `yaml:"name"`
	InstallDir string `yaml:"install_dir,omitempty"`
	ModPath    string `yaml:"mod_path"`
	// ModPathBase is "install" (or absent) or "documents".
	ModPathBase string            `yaml:"mod_path_base,omitempty"`
	Adapter     string            `yaml:"adapter,omitempty"`
	Sources     map[string]string `yaml:"sources,omitempty"`
	CurseForge  *struct {
		GameVersionTypes []int `yaml:"game_version_types"`
	} `yaml:"curseforge,omitempty"`
	LaunchCode string `yaml:"launch_code,omitempty"`
}

// gameInfo converts one parsed entry. origin names the file it came from,
// so a mistake in a user's override does not read as a bug in the
// embedded catalog.
func (e gameYAML) gameInfo(origin, code string) (GameInfo, error) {
	if strings.TrimSpace(e.Slug) == "" || strings.TrimSpace(e.Name) == "" {
		return GameInfo{}, fmt.Errorf("%s: product %q: slug and name are required", origin, code)
	}
	if len(e.Sources) == 0 {
		return GameInfo{}, fmt.Errorf("%s: product %q: sources is required - the game's mod source map", origin, code)
	}
	for _, p := range []struct{ key, value string }{{"install_dir", e.InstallDir}, {"mod_path", e.ModPath}} {
		if filepath.IsAbs(p.value) || strings.Contains(p.value, `\`) || strings.HasPrefix(filepath.Clean(p.value), "..") {
			return GameInfo{}, fmt.Errorf("%s: product %q: %s %q must be a relative path inside the install", origin, code, p.key, p.value)
		}
	}
	info := GameInfo{
		Slug: e.Slug, Name: e.Name, InstallDir: e.InstallDir, ModPath: e.ModPath,
		Adapter: e.Adapter, Sources: e.Sources, LaunchCode: e.LaunchCode,
	}
	switch strings.ToLower(strings.TrimSpace(e.ModPathBase)) {
	case "", "install":
	case ModPathBaseDocuments:
		info.ModPathBase = ModPathBaseDocuments
		if documentsGameFolder(e.ModPath) == "" {
			return GameInfo{}, fmt.Errorf("%s: product %q: mod_path must name the game's folder in Documents when mod_path_base is documents", origin, code)
		}
	default:
		return GameInfo{}, fmt.Errorf("%s: product %q: mod_path_base %q must be install or documents", origin, code, e.ModPathBase)
	}
	if e.CurseForge != nil {
		info.CurseForgeVersionTypes = e.CurseForge.GameVersionTypes
	}
	return info, nil
}

// LoadKnownGames returns the known product code -> GameInfo map: the
// embedded catalog, then configDir/battlenet-games.yaml merged over it when
// present - steam.LoadKnownGames' arrangement, so a game can be added or
// corrected without rebuilding. Product codes are matched lower-cased. An
// override it cannot read or parse is an error; DetectGames, which must not
// fail a whole scan over it, uses loadCatalog instead.
func LoadKnownGames(configDir string) (map[string]GameInfo, error) {
	out, err := embeddedGames()
	if err != nil {
		return nil, err
	}
	if err := mergeOverride(out, configDir); err != nil {
		return nil, err
	}
	return out, nil
}

// loadCatalog is LoadKnownGames for a scan: an override it cannot read or
// parse is warning, and the catalog is the embedded one alone - never a
// half-merged override - so a typo in battlenet-games.yaml costs the
// user's own entries, not Battle.net detection, and never Steam's.
func loadCatalog(configDir string) (catalog map[string]GameInfo, warning string, err error) {
	out, err := embeddedGames()
	if err != nil {
		return nil, "", err
	}
	merged := maps.Clone(out)
	if err := mergeOverride(merged, configDir); err != nil {
		return out, fmt.Sprintf("%v; ignoring %s and detecting Battle.net games from the built-in catalog", err, overrideFile), nil
	}
	return merged, "", nil
}

// embeddedGames parses the embedded catalog.
func embeddedGames() (map[string]GameInfo, error) {
	data, err := defaultGamesFS.ReadFile(defaultGamesPath)
	if err != nil {
		return nil, fmt.Errorf("reading embedded battlenet-games: %w", err)
	}
	out := make(map[string]GameInfo)
	if err := mergeGames(out, data, "battlenet-games.yaml"); err != nil {
		return nil, err
	}
	return out, nil
}

// mergeOverride merges configDir/battlenet-games.yaml into out, when the
// file exists.
func mergeOverride(out map[string]GameInfo, configDir string) error {
	overridePath := filepath.Join(configDir, overrideFile)
	data, err := os.ReadFile(overridePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading %s: %w", overridePath, err)
	}
	return mergeGames(out, data, overridePath)
}

// mergeGames parses one catalog file into out.
func mergeGames(out map[string]GameInfo, data []byte, origin string) error {
	var y gamesYAML
	if err := safeyaml.Unmarshal(data, &y); err != nil {
		return fmt.Errorf("parsing %s: %w", origin, err)
	}
	for code, e := range y {
		code = strings.ToLower(strings.TrimSpace(code))
		info, err := e.gameInfo(origin, code)
		if err != nil {
			return err
		}
		out[code] = info
	}
	return nil
}
