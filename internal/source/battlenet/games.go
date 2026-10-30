package battlenet

import (
	"embed"
	"fmt"
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
	// ModPath is relative to InstallDir; "" is InstallDir itself.
	ModPath string
	// Sources is the generated games.yaml entry's complete source map.
	Sources map[string]string
	// CurseForgeVersionTypes is #544's flavor setting; nil accepts every
	// file.
	CurseForgeVersionTypes []int
	// LaunchCode is Battle.net's --exec="launch <code>" code, when lmm knows
	// it. "" opens the client.
	LaunchCode string
}

// gamesYAML is the on-disk format: product code -> entry.
type gamesYAML map[string]gameYAML

// gameYAML is one entry of that format.
type gameYAML struct {
	Slug       string            `yaml:"slug"`
	Name       string            `yaml:"name"`
	InstallDir string            `yaml:"install_dir,omitempty"`
	ModPath    string            `yaml:"mod_path"`
	Sources    map[string]string `yaml:"sources,omitempty"`
	CurseForge *struct {
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
		Sources: e.Sources, LaunchCode: e.LaunchCode,
	}
	if e.CurseForge != nil {
		info.CurseForgeVersionTypes = e.CurseForge.GameVersionTypes
	}
	return info, nil
}

// LoadKnownGames returns the known product code -> GameInfo map: the
// embedded catalog, then configDir/battlenet-games.yaml merged over it when
// present - steam.LoadKnownGames' arrangement, so a game can be added or
// corrected without rebuilding. Product codes are matched lower-cased.
func LoadKnownGames(configDir string) (map[string]GameInfo, error) {
	data, err := defaultGamesFS.ReadFile(defaultGamesPath)
	if err != nil {
		return nil, fmt.Errorf("reading embedded battlenet-games: %w", err)
	}
	out := make(map[string]GameInfo)
	if err := mergeGames(out, data, "battlenet-games.yaml"); err != nil {
		return nil, err
	}
	overridePath := filepath.Join(configDir, overrideFile)
	overrideData, err := os.ReadFile(overridePath)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("reading %s: %w", overridePath, err)
	}
	if err := mergeGames(out, overrideData, overridePath); err != nil {
		return nil, err
	}
	return out, nil
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
