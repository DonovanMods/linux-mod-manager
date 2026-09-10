package steam

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// Loader is the entry's mod-loader declaration (#416), mirroring
	// games.yaml's own `loader:` block (#359) but carrying only the two
	// members a CATALOG can honestly answer. Optional: nil - which is every
	// shipped entry today - means the game needs no loader, exactly as
	// every entry behaved before this field existed.
	Loader *LoaderInfo
}

// LoaderInfo is a known-games entry's `loader:` block: which mod loader the
// game needs, and optionally at which version.
//
// It is deliberately NARROWER than games.yaml's block, which also carries
// runtime and bootstrap: those are facts about a particular INSTALLATION -
// whether this copy is the native Linux build or a Proton one, especially -
// and a curated list shipped inside the binary cannot know them. `lmm game
// show` reads them off the game directory instead.
//
// Kind is an open string on domain.GameLoader's terms (#359): BepInEx is one
// of several Unity loaders, so a catalog naming one lmm has never heard of
// loads fine and simply fires none of lmm's own rules. What is NOT accepted
// is an empty kind - see LoadKnownGames.
type LoaderInfo struct {
	// Kind is the loader's name, e.g. "bepinex". Required.
	Kind string
	// Version is the loader version the game needs, e.g. "5.4.23.5".
	// Optional: empty means "do not check the version" at verify time.
	Version string
}

// steamGamesYAML is the on-disk format: Steam App ID -> game entry.
type steamGamesYAML map[string]steamGameYAML

// steamGameYAML is one entry of that format.
type steamGameYAML struct {
	Slug       string            `yaml:"slug"`
	Name       string            `yaml:"name"`
	NexusID    string            `yaml:"nexus_id,omitempty"`
	ModPath    string            `yaml:"mod_path"`
	DeployMode string            `yaml:"deploy_mode,omitempty"`
	Sources    map[string]string `yaml:"sources,omitempty"`
	Loader     *loaderYAML       `yaml:"loader,omitempty"`
}

// loaderYAML is the `loader:` block inside one entry.
type loaderYAML struct {
	Kind    string `yaml:"kind"`
	Version string `yaml:"version,omitempty"`
}

// gameInfo converts one parsed entry, validating the loader block. origin
// names the file the entry came from, so a mistake in a user's own override
// does not read as a bug in the embedded catalog.
func (e steamGameYAML) gameInfo(origin string) (GameInfo, error) {
	info := GameInfo{
		Slug: e.Slug, Name: e.Name, NexusID: e.NexusID, ModPath: e.ModPath,
		DeployMode: e.DeployMode, Sources: e.Sources,
	}
	if e.Loader == nil {
		return info, nil
	}
	// An UNKNOWN kind is accepted on purpose (domain.GameLoader.Kind is an
	// open string, #359) - but an EMPTY one is not: it is a block that
	// declares nothing while looking like it should, which would ship a
	// curated entry whose loader fires no rule and whose user is told
	// nothing about why.
	kind := strings.ToLower(strings.TrimSpace(e.Loader.Kind))
	if kind == "" {
		return GameInfo{}, fmt.Errorf("%s: game %q: loader.kind is required when a loader block is present", origin, e.Slug)
	}
	info.Loader = &LoaderInfo{Kind: kind, Version: strings.TrimSpace(e.Loader.Version)}
	return info, nil
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
		info, err := e.gameInfo("steam-games.yaml")
		if err != nil {
			return nil, err
		}
		out[appID] = info
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
		info, err := e.gameInfo(overridePath)
		if err != nil {
			return nil, err
		}
		out[appID] = info
	}
	return out, nil
}
