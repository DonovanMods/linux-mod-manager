package steam

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

//go:embed data/steam-games.yaml
var defaultSteamGamesFS embed.FS

const defaultSteamGamesPath = "data/steam-games.yaml"

// GameInfo describes a moddable game known to lmm, mapped from Steam App ID.
type GameInfo struct {
	Slug    string // lmm game ID, e.g. "skyrim-se"
	Name    string // Display name, e.g. "Skyrim Special Edition"
	NexusID string // NexusMods game domain ID, e.g. "skyrimspecialedition". Optional: absent for games with no NexusMods presence (e.g. Icarus).
	ModPath string // Relative path from game install to mod directory, e.g. "Data"
	// DeployMode is games.yaml's deploy_mode string ("extract"/"copy"/"compile"),
	// passed through as-is for domain.ParseDeployMode to interpret. Optional:
	// "" means the game uses the default (extract), exactly as every entry
	// behaved before this field existed (#177).
	DeployMode string
	// Sources is a full source-id -> per-source game-id map (games.yaml's
	// "sources:" block), for games whose primary source isn't NexusMods (or
	// that need more than one source, e.g. #177's Icarus: {"icarus": "icarus"}).
	// Optional: nil means "derive {nexusmods: NexusID}", exactly as every
	// entry behaved before this field existed.
	Sources map[string]string
}

// steamGamesYAML is the on-disk format: Steam App ID -> game entry.
type steamGamesYAML map[string]struct {
	Slug       string            `yaml:"slug"`
	Name       string            `yaml:"name"`
	NexusID    string            `yaml:"nexus_id,omitempty"`
	ModPath    string            `yaml:"mod_path"`
	DeployMode string            `yaml:"deploy_mode,omitempty"`
	Sources    map[string]string `yaml:"sources,omitempty"`
}

// LoadKnownGames returns the known Steam App ID -> GameInfo map. It loads the
// embedded default list, then merges in configDir/steam-games.yaml if present
// (so you can add or override games without rebuilding).
func LoadKnownGames(configDir string) (map[string]GameInfo, error) {
	data, err := defaultSteamGamesFS.ReadFile(defaultSteamGamesPath)
	if err != nil {
		return nil, fmt.Errorf("reading embedded steam-games: %w", err)
	}
	var y steamGamesYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		return nil, fmt.Errorf("parsing embedded steam-games: %w", err)
	}
	out := make(map[string]GameInfo)
	for appID, e := range y {
		out[appID] = GameInfo{
			Slug: e.Slug, Name: e.Name, NexusID: e.NexusID, ModPath: e.ModPath,
			DeployMode: e.DeployMode, Sources: e.Sources,
		}
	}

	overridePath := filepath.Join(configDir, "steam-games.yaml")
	overrideData, err := os.ReadFile(overridePath)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("reading %s: %w", overridePath, err)
	}
	var override steamGamesYAML
	if err := yaml.Unmarshal(overrideData, &override); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", overridePath, err)
	}
	for appID, e := range override {
		out[appID] = GameInfo{
			Slug: e.Slug, Name: e.Name, NexusID: e.NexusID, ModPath: e.ModPath,
			DeployMode: e.DeployMode, Sources: e.Sources,
		}
	}
	return out, nil
}
