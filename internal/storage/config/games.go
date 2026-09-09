package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"gopkg.in/yaml.v3"
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
	ConvertPaks *bool             `yaml:"convert_paks,omitempty"`
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
	gamesPath := filepath.Join(configDir, "games.yaml")
	data, err := os.ReadFile(gamesPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]*domain.Game), nil
		}
		return nil, fmt.Errorf("reading games.yaml: %w", err)
	}
	var gamesFile GamesFile
	if err := yaml.Unmarshal(data, &gamesFile); err != nil {
		return nil, fmt.Errorf("parsing games.yaml: %w", err)
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
			ConvertPaks:         convertPaks,
			ConvertPaksExplicit: convertExplicit,
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

// SaveGame adds or updates a game in games.yaml
func SaveGame(configDir string, game *domain.Game) error {
	if game.ModPath != "" && !filepath.IsAbs(game.ModPath) {
		return fmt.Errorf("%w: game %q: got %q", ErrRelativeModPath, game.ID, game.ModPath)
	}
	gamesMu.Lock()
	defer gamesMu.Unlock()
	games, err := loadGamesLocked(configDir)
	if err != nil {
		return err
	}
	games[game.ID] = game
	return saveGamesLocked(configDir, games)
}

// saveGamesLocked writes games; caller must hold gamesMu.
//
// It re-marshals the WHOLE file from the loaded map, whose paths have
// already been ExpandPath-ed and (since #313) ResolveModPath-ed - so any
// write, from any command, normalises every OTHER game's entry too: a
// hand-written "~" becomes the expanded path and a relative mod_path
// becomes the absolute one it already resolved to. Recorded here (review
// M9) so it is a decision rather than a surprise. It is the intended
// direction - what lmm writes is what every later run reads, with no
// working directory in the answer - and SaveGame's OWN guard cannot fail an
// unrelated write, since the values in the map are already absolute by the
// time it sees them. A games.yaml that no longer LOADS does fail every
// write - an invalid link_method/deploy_mode, or (review M6) a relative
// mod_path with no install_path to resolve it against - because SaveGame
// reaches this through loadGamesLocked and returns that error; it has
// always behaved that way for the first two. Preserving hand-written
// relative/tilde values verbatim would be a separate change to this
// function.
func saveGamesLocked(configDir string, games map[string]*domain.Game) error {
	gamesFile := GamesFile{Games: make(map[string]GameConfig)}

	for id, game := range games {
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
		// Only write convert_paks if explicitly set
		if game.ConvertPaksExplicit {
			v := game.ConvertPaks
			cfg.ConvertPaks = &v
		}
		gamesFile.Games[id] = cfg
	}

	data, err := yaml.Marshal(&gamesFile)
	if err != nil {
		return fmt.Errorf("marshaling games: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	gamesPath := filepath.Join(configDir, "games.yaml")
	if err := os.WriteFile(gamesPath, data, 0644); err != nil {
		return fmt.Errorf("writing games.yaml: %w", err)
	}

	return nil
}

// DeleteGame removes a game from games.yaml
func DeleteGame(configDir string, gameID string) error {
	gamesMu.Lock()
	defer gamesMu.Unlock()
	games, err := loadGamesLocked(configDir)
	if err != nil {
		return err
	}
	if _, exists := games[gameID]; !exists {
		return domain.ErrGameNotFound
	}
	delete(games, gameID)
	return saveGamesLocked(configDir, games)
}
