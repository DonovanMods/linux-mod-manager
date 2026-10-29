package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/safeyaml"

	"go.yaml.in/yaml/v3"
)

// gamesMu serializes read-modify-write of games.yaml to avoid lost updates
var gamesMu sync.Mutex

// ExpandPath expands ~ to the user's home directory
func ExpandPath(path string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	} else if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	return path
}

// ResolveModPath makes a game's mod_path absolute the way #313 settled it:
// a RELATIVE value is joined onto the game's install path, because that is
// what a hand-written "mod_path: Data" means to everybody who writes one.
// Before this it was used verbatim, so every deploy resolved it against
// whatever directory lmm happened to be run from. An absolute value (and
// an empty one, which is not a path at all) passes through untouched.
//
// Callers pass the ALREADY-EXPANDED install path; expansion is the caller's
// job so "~" is handled once, at the same place, for both fields.
//
// A relative mod_path with NO install path to resolve it against is
// refused with ErrRelativeModPath (review M6). Returning it verbatim would
// be exactly the pre-#313 behaviour this function exists to end - a path
// resolved against whatever directory lmm was run from - and silently, in
// the one case #313 is for: a hand-written games.yaml. install_path is
// documented as required; this is what enforces it where it matters.
func ResolveModPath(installPath, modPath string) (string, error) {
	if modPath == "" || filepath.IsAbs(modPath) {
		return modPath, nil
	}
	if installPath == "" {
		return "", fmt.Errorf("%w: %q is relative and there is no install_path to resolve it against", ErrRelativeModPath, modPath)
	}
	return filepath.Join(installPath, modPath), nil
}

// ErrRelativeModPath is the refusal SaveGame makes for a game whose
// ModPath is relative, and the one ResolveModPath makes for a relative
// value with no install_path behind it.
//
// The two are not the same rule. LOADING tolerates a relative value and
// joins it onto install_path; SAVING requires an already-resolved absolute
// one, because the value lmm writes is the value every later run - from any
// working directory - reads back. What #363 changed is where the
// resolution happens on the write path: core.GameSpec.game() now runs
// ResolveModPath itself, so a frontend may accept "Data" from a user and
// still hand SaveGame an absolute path. SaveGame's own refusal stays as
// the backstop for a caller that skipped that step.
var ErrRelativeModPath = errors.New("mod_path must be an absolute path")

// HookConfigYAML is the YAML representation of hook configuration
type HookConfigYAML struct {
	BeforeAll  string `yaml:"before_all"`
	BeforeEach string `yaml:"before_each"`
	AfterEach  string `yaml:"after_each"`
	AfterAll   string `yaml:"after_all"`
}

// GameHooksYAML is the YAML representation of game hooks
type GameHooksYAML struct {
	Install   HookConfigYAML `yaml:"install"`
	Uninstall HookConfigYAML `yaml:"uninstall"`
}

// GameLoaderYAML is the YAML representation of a game's mod-loader
// declaration (#359) - games.yaml's `loader:` block.
//
// Every member is a plain string, like every other enum-valued key in this
// file: domain.LoaderRuntime and domain.LoaderBootstrap both implement
// TextMarshaler, and yaml.v3 honours it, so typing them here would emit the
// name but ALSO decode a typo into the zero value without an error. Strings
// keep the fail-loud parse where the rest of this file does it - in
// loadGamesLocked, naming the game and the valid set.
type GameLoaderYAML struct {
	Kind      string `yaml:"kind"`
	Version   string `yaml:"version,omitempty"`
	Runtime   string `yaml:"runtime,omitempty"`
	Bootstrap string `yaml:"bootstrap,omitempty"`
}

// GameConfig is the YAML representation of a game
type GameConfig struct {
	Name        string            `yaml:"name"`
	InstallPath string            `yaml:"install_path"`
	ModPath     string            `yaml:"mod_path"`
	Sources     map[string]string `yaml:"sources"`
	LinkMethod  string            `yaml:"link_method,omitempty"`
	CachePath   string            `yaml:"cache_path,omitempty"`
	Hooks       GameHooksYAML     `yaml:"hooks,omitempty"`
	DeployMode  string            `yaml:"deploy_mode,omitempty"`
	// Adapter selects the game adapter (#353). Omitted means the
	// generic-files identity, and the writer emits the key only when the
	// user set one - so a games.yaml written before the seam existed
	// round-trips byte-identically.
	Adapter     string `yaml:"adapter,omitempty"`
	ConvertPaks *bool  `yaml:"convert_paks,omitempty"`
	// Loader is #359's optional block. A POINTER, so a game that declares
	// no loader gains no `loader:` key when an unrelated write re-marshals
	// the whole file.
	Loader *GameLoaderYAML `yaml:"loader,omitempty"`
}

// GamesFile is the top-level games.yaml structure
type GamesFile struct {
	Games map[string]GameConfig `yaml:"games"`
}

// LoadGames reads all game configurations from the config directory
func LoadGames(configDir string) (map[string]*domain.Game, error) {
	gamesMu.Lock()
	defer gamesMu.Unlock()
	return loadGamesLocked(configDir)
}

// loadGamesLocked reads games; caller must hold gamesMu
func loadGamesLocked(configDir string) (map[string]*domain.Game, error) {
	_, games, err := readGamesLocked(configDir)
	return games, err
}

// readGamesLocked reads games and also returns the file's own bytes (nil
// for a games.yaml that does not exist), which a save patches; caller must
// hold gamesMu.
func readGamesLocked(configDir string) ([]byte, map[string]*domain.Game, error) {
	gamesPath := filepath.Join(configDir, "games.yaml")
	data, err := os.ReadFile(gamesPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, make(map[string]*domain.Game), nil
		}
		return nil, nil, fmt.Errorf("reading games.yaml: %w", err)
	}
	games, err := parseGames(gamesPath, data)
	if err != nil {
		return nil, nil, err
	}
	return data, games, nil
}

// parseGames turns games.yaml's bytes, read from gamesPath, into games.
func parseGames(gamesPath string, data []byte) (map[string]*domain.Game, error) {
	var gamesFile GamesFile
	if err := safeyaml.Unmarshal(data, &gamesFile); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", gamesPath, err)
	}
	games := make(map[string]*domain.Game)
	for id, cfg := range gamesFile.Games {
		linkMethod, ok := domain.ParseLinkMethod(cfg.LinkMethod)
		if !ok {
			return nil, fmt.Errorf("%w: games.yaml: game %q: link_method %q (valid: %s)",
				domain.ErrInvalidLinkMethod, id, cfg.LinkMethod, domain.ValidLinkMethods)
		}
		deployMode, ok := domain.ParseDeployMode(cfg.DeployMode)
		if !ok {
			return nil, fmt.Errorf("%w: games.yaml: game %q: deploy_mode %q (valid: %s)",
				domain.ErrInvalidDeployMode, id, cfg.DeployMode, domain.ValidDeployModes)
		}
		// Syntax only, deliberately: this layer must not learn the adapter
		// registry (design §2, "validation splits by layer"). Whether the
		// named adapter EXISTS is core's question, asked when it resolves
		// the game, so a games.yaml naming an adapter a future build ships
		// fails with the registered set in front of the user rather than
		// with a parse error here.
		if cfg.Adapter != "" && !domain.ValidAdapterName(cfg.Adapter) {
			return nil, fmt.Errorf("%w: games.yaml: game %q: adapter %q is not a valid adapter name (lowercase letters, digits and single interior hyphens)",
				domain.ErrInvalidAdapter, id, cfg.Adapter)
		}
		convertPaks := true // default: paks convert (only meaningful for DeployCompile games)
		convertExplicit := false
		if cfg.ConvertPaks != nil {
			convertPaks = *cfg.ConvertPaks
			convertExplicit = true
		}
		installPath := ExpandPath(cfg.InstallPath)
		modPath, err := ResolveModPath(installPath, ExpandPath(cfg.ModPath))
		if err != nil {
			return nil, fmt.Errorf("games.yaml: game %q: %w", id, err)
		}
		loader, err := parseGameLoader(id, cfg.Loader)
		if err != nil {
			return nil, err
		}
		games[id] = &domain.Game{
			ID:                  id,
			Name:                cfg.Name,
			InstallPath:         installPath,
			ModPath:             modPath,
			SourceIDs:           cfg.Sources,
			LinkMethod:          linkMethod,
			LinkMethodExplicit:  cfg.LinkMethod != "",
			CachePath:           ExpandPath(cfg.CachePath),
			DeployMode:          deployMode,
			Adapter:             cfg.Adapter,
			ConvertPaks:         convertPaks,
			ConvertPaksExplicit: convertExplicit,
			Loader:              loader,
			Hooks: domain.GameHooks{
				Install: domain.HookConfig{
					BeforeAll:  ExpandPath(cfg.Hooks.Install.BeforeAll),
					BeforeEach: ExpandPath(cfg.Hooks.Install.BeforeEach),
					AfterEach:  ExpandPath(cfg.Hooks.Install.AfterEach),
					AfterAll:   ExpandPath(cfg.Hooks.Install.AfterAll),
				},
				Uninstall: domain.HookConfig{
					BeforeAll:  ExpandPath(cfg.Hooks.Uninstall.BeforeAll),
					BeforeEach: ExpandPath(cfg.Hooks.Uninstall.BeforeEach),
					AfterEach:  ExpandPath(cfg.Hooks.Uninstall.AfterEach),
					AfterAll:   ExpandPath(cfg.Hooks.Uninstall.AfterAll),
				},
			},
		}
	}
	return games, nil
}

// SaveGame adds or updates a game in games.yaml.
//
// Only that game's entry is written (#362): every other game keeps its own
// text - a hand-written `~/` path, a relative mod_path, comments and
// indentation included - and the file is replaced atomically (#403). See
// saveGamesLocked.
func SaveGame(configDir string, game *domain.Game) error {
	if game.ModPath != "" && !filepath.IsAbs(game.ModPath) {
		return fmt.Errorf("%w: game %q: got %q", ErrRelativeModPath, game.ID, game.ModPath)
	}
	gamesMu.Lock()
	defer gamesMu.Unlock()
	data, games, err := readGamesLocked(configDir)
	if err != nil {
		return err
	}
	games[game.ID] = game
	cfg := gameConfigOf(game)
	return saveGamesLocked(configDir, data, games, game.ID, &cfg)
}

// DeleteGame removes a game from games.yaml, leaving every other game's
// text as it was (#362).
func DeleteGame(configDir string, gameID string) error {
	gamesMu.Lock()
	defer gamesMu.Unlock()
	data, games, err := readGamesLocked(configDir)
	if err != nil {
		return err
	}
	if _, exists := games[gameID]; !exists {
		return domain.ErrGameNotFound
	}
	delete(games, gameID)
	return saveGamesLocked(configDir, data, games, gameID, nil)
}

// saveGamesLocked writes games.yaml with game id set to cfg - or removed,
// for a nil cfg - where data is the file as it was read and games is every
// game after the change; caller must hold gamesMu.
//
// It edits data rather than re-marshalling games (#362): games' paths have
// been ExpandPath-ed and ResolveModPath-ed, so writing them back would turn
// every OTHER game's hand-written "~" and relative mod_path into the
// absolute values they resolve to. patchGames replaces only id's entry, and
// checks its result reads back as the file it read with exactly that change
// before it is used. A file it cannot patch - and one with no game in it,
// where there is nothing to keep - is written whole from games, as every
// save did before #362; that is never wrong, only less faithful to the
// file's own spelling. The game being saved is always written from its
// resolved values.
//
// A games.yaml that no longer LOADS fails every write - an invalid
// link_method/deploy_mode, or (review M6) a relative mod_path with no
// install_path to resolve it against - because SaveGame and DeleteGame
// read it first and return that error.
//
// The write is atomic (#403): a temporary file beside games.yaml is renamed
// over it, so a reader in another process - `lmm serve` re-reads this file
// on every request it serves (freshGames, #376) - sees the old file or the
// new one, never a partial one, and an interrupted write leaves the old
// file in place.
func saveGamesLocked(configDir string, data []byte, games map[string]*domain.Game, id string, cfg *GameConfig) error {
	out, ok := patchGames(data, id, cfg)
	if !ok {
		var err error
		if out, err = marshalGames(games); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	return replaceFile(filepath.Join(configDir, "games.yaml"), out, 0644, "games.yaml")
}

// marshalGames is games.yaml written whole from games.
func marshalGames(games map[string]*domain.Game) ([]byte, error) {
	gamesFile := GamesFile{Games: make(map[string]GameConfig, len(games))}
	for id, game := range games {
		gamesFile.Games[id] = gameConfigOf(game)
	}
	data, err := yaml.Marshal(&gamesFile)
	if err != nil {
		return nil, fmt.Errorf("marshaling games: %w", err)
	}
	return data, nil
}

// gameConfigOf is game as games.yaml writes it.
func gameConfigOf(game *domain.Game) GameConfig {
	cfg := GameConfig{
		Name:        game.Name,
		InstallPath: game.InstallPath,
		ModPath:     game.ModPath,
		Sources:     game.SourceIDs,
		CachePath:   game.CachePath,
		Hooks: GameHooksYAML{
			Install: HookConfigYAML{
				BeforeAll:  game.Hooks.Install.BeforeAll,
				BeforeEach: game.Hooks.Install.BeforeEach,
				AfterEach:  game.Hooks.Install.AfterEach,
				AfterAll:   game.Hooks.Install.AfterAll,
			},
			Uninstall: HookConfigYAML{
				BeforeAll:  game.Hooks.Uninstall.BeforeAll,
				BeforeEach: game.Hooks.Uninstall.BeforeEach,
				AfterEach:  game.Hooks.Uninstall.AfterEach,
				AfterAll:   game.Hooks.Uninstall.AfterAll,
			},
		},
	}
	// Only write link_method if explicitly set
	if game.LinkMethodExplicit {
		cfg.LinkMethod = game.LinkMethod.String()
	}
	// Only write deploy_mode if not the default (extract)
	if game.DeployMode != domain.DeployExtract {
		cfg.DeployMode = game.DeployMode.String()
	}
	// Only write adapter if the game carries one. Empty is the
	// generic-files default and stays absent, which is what keeps a
	// pre-#353 games.yaml byte-identical across any write.
	cfg.Adapter = game.Adapter
	// Only write convert_paks if explicitly set
	if game.ConvertPaksExplicit {
		v := game.ConvertPaks
		cfg.ConvertPaks = &v
	}
	// Only write loader: for a game that declares one. The enums emit
	// "" for "not answered yet", which omitempty drops, so lmm never
	// writes back a runtime or bootstrap nobody chose.
	if game.Loader != nil {
		cfg.Loader = &GameLoaderYAML{
			Kind:      game.Loader.Kind,
			Version:   game.Loader.Version,
			Runtime:   game.Loader.Runtime.String(),
			Bootstrap: game.Loader.Bootstrap.String(),
		}
	}
	return cfg
}

// parseGameLoader turns games.yaml's `loader:` block into the domain type,
// or nil when the game declares none. A nil block is "this game needs no
// loader", which is not the same as a zero-valued declaration - Game.
// DeclaresBepInEx must never answer from a struct nobody wrote.
//
// The two closed values fail loud the way link_method and deploy_mode do,
// naming the game, the offending value and the valid set (domain.
// ErrInvalidLoaderRuntime / ErrInvalidLoaderBootstrap). An unrecognised
// KIND is not an error: kind is deliberately an open string so a second
// loader costs nothing, and lmm's own rules simply do not fire for a kind
// they do not know. An EMPTY kind is: it declares nothing while looking
// like it should, and core.LoaderSpec.loader - the same field's other door,
// from `lmm game edit --loader` and POST /api/v1/games - already refuses
// the identical input naming loader.kind.
func parseGameLoader(gameID string, cfg *GameLoaderYAML) (*domain.GameLoader, error) {
	if cfg == nil {
		return nil, nil
	}
	kind := strings.ToLower(strings.TrimSpace(cfg.Kind))
	if kind == "" {
		return nil, fmt.Errorf("%w: games.yaml: game %q: loader.kind %q (a loader kind is required; today lmm knows %s)",
			domain.ErrInvalidLoaderKind, gameID, cfg.Kind, domain.LoaderKindBepInEx)
	}
	runtime, ok := domain.ParseLoaderRuntime(cfg.Runtime)
	if !ok {
		return nil, fmt.Errorf("%w: games.yaml: game %q: loader.runtime %q (valid: %s)",
			domain.ErrInvalidLoaderRuntime, gameID, cfg.Runtime, domain.ValidLoaderRuntimes)
	}
	bootstrap, ok := domain.ParseLoaderBootstrap(cfg.Bootstrap)
	if !ok {
		return nil, fmt.Errorf("%w: games.yaml: game %q: loader.bootstrap %q (valid: %s)",
			domain.ErrInvalidLoaderBootstrap, gameID, cfg.Bootstrap, domain.ValidLoaderBootstraps)
	}
	return &domain.GameLoader{
		Kind:      kind,
		Version:   strings.TrimSpace(cfg.Version),
		Runtime:   runtime,
		Bootstrap: bootstrap,
	}, nil
}
