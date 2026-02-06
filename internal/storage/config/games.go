package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"

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
	LinkMethod  string            `yaml:"link_method"`
	CachePath   string            `yaml:"cache_path"`
	Hooks       GameHooksYAML     `yaml:"hooks,omitempty"`
	DeployMode  string            `yaml:"deploy_mode"`
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
		games[id] = &domain.Game{
			ID:                 id,
			Name:               cfg.Name,
			InstallPath:        ExpandPath(cfg.InstallPath),
			ModPath:            ExpandPath(cfg.ModPath),
			SourceIDs:          cfg.Sources,
			LinkMethod:         domain.ParseLinkMethod(cfg.LinkMethod),
			LinkMethodExplicit: cfg.LinkMethod != "",
			CachePath:          ExpandPath(cfg.CachePath),
			DeployMode:         domain.ParseDeployMode(cfg.DeployMode),
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
	gamesMu.Lock()
	defer gamesMu.Unlock()
	games, err := loadGamesLocked(configDir)
	if err != nil {
		return err
	}
	games[game.ID] = game
	return saveGamesLocked(configDir, games)
}

// saveGamesLocked writes games; caller must hold gamesMu
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
