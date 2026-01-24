package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lmm/internal/domain"

	"gopkg.in/yaml.v3"
)

// GameConfig is the YAML representation of a game
type GameConfig struct {
	Name        string            `yaml:"name"`
	InstallPath string            `yaml:"install_path"`
	ModPath     string            `yaml:"mod_path"`
	Sources     map[string]string `yaml:"sources"`
	LinkMethod  string            `yaml:"link_method"`
}

// GamesFile is the top-level games.yaml structure
type GamesFile struct {
	Games map[string]GameConfig `yaml:"games"`
}

// LoadGames reads all game configurations from the config directory
func LoadGames(configDir string) (map[string]*domain.Game, error) {
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
			ID:          id,
			Name:        cfg.Name,
			InstallPath: cfg.InstallPath,
			ModPath:     cfg.ModPath,
			SourceIDs:   cfg.Sources,
			LinkMethod:  domain.ParseLinkMethod(cfg.LinkMethod),
		}
	}

	return games, nil
}

// SaveGame adds or updates a game in games.yaml
func SaveGame(configDir string, game *domain.Game) error {
	games, err := LoadGames(configDir)
	if err != nil {
		return err
	}

	games[game.ID] = game

	return saveGames(configDir, games)
}

func saveGames(configDir string, games map[string]*domain.Game) error {
	gamesFile := GamesFile{Games: make(map[string]GameConfig)}

	for id, game := range games {
		gamesFile.Games[id] = GameConfig{
			Name:        game.Name,
			InstallPath: game.InstallPath,
			ModPath:     game.ModPath,
			Sources:     game.SourceIDs,
			LinkMethod:  game.LinkMethod.String(),
		}
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
	games, err := LoadGames(configDir)
	if err != nil {
		return err
	}

	if _, exists := games[gameID]; !exists {
		return domain.ErrGameNotFound
	}

	delete(games, gameID)
	return saveGames(configDir, games)
}
