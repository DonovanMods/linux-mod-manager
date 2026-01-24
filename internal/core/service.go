package core

import (
	"context"
	"fmt"
	"path/filepath"

	"lmm/internal/domain"
	"lmm/internal/linker"
	"lmm/internal/source"
	"lmm/internal/storage/cache"
	"lmm/internal/storage/config"
	"lmm/internal/storage/db"
)

// ServiceConfig holds configuration for the core service
type ServiceConfig struct {
	ConfigDir string // Directory for configuration files
	DataDir   string // Directory for database and persistent data
	CacheDir  string // Directory for mod file cache
}

// Service is the main orchestrator for mod management operations
type Service struct {
	config   *config.Config
	db       *db.DB
	cache    *cache.Cache
	registry *source.Registry
	games    map[string]*domain.Game

	configDir string
	dataDir   string
	cacheDir  string
}

// NewService creates a new core service instance
func NewService(cfg ServiceConfig) (*Service, error) {
	// Load configuration
	appConfig, err := config.Load(cfg.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	// Open database
	dbPath := filepath.Join(cfg.DataDir, "lmm.db")
	database, err := db.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Load games
	games, err := config.LoadGames(cfg.ConfigDir)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("loading games: %w", err)
	}

	return &Service{
		config:    appConfig,
		db:        database,
		cache:     cache.New(cfg.CacheDir),
		registry:  source.NewRegistry(),
		games:     games,
		configDir: cfg.ConfigDir,
		dataDir:   cfg.DataDir,
		cacheDir:  cfg.CacheDir,
	}, nil
}

// Close releases resources held by the service
func (s *Service) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// RegisterSource adds a mod source to the registry
func (s *Service) RegisterSource(src source.ModSource) {
	s.registry.Register(src)
}

// GetSource retrieves a source by ID
func (s *Service) GetSource(id string) (source.ModSource, error) {
	return s.registry.Get(id)
}

// ListSources returns all registered sources
func (s *Service) ListSources() []source.ModSource {
	return s.registry.List()
}

// SearchMods searches for mods in a source
func (s *Service) SearchMods(ctx context.Context, sourceID, gameID, query string) ([]domain.Mod, error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil, err
	}

	// Get the source-specific game ID if we have a game configured
	sourceGameID := gameID
	if game, ok := s.games[gameID]; ok {
		if id, ok := game.SourceIDs[sourceID]; ok {
			sourceGameID = id
		}
	}

	return src.Search(ctx, source.SearchQuery{
		GameID: sourceGameID,
		Query:  query,
	})
}

// GetMod retrieves a specific mod from a source
func (s *Service) GetMod(ctx context.Context, sourceID, gameID, modID string) (*domain.Mod, error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil, err
	}

	// Get the source-specific game ID if we have a game configured
	sourceGameID := gameID
	if game, ok := s.games[gameID]; ok {
		if id, ok := game.SourceIDs[sourceID]; ok {
			sourceGameID = id
		}
	}

	return src.GetMod(ctx, sourceGameID, modID)
}

// GetGame retrieves a game by ID
func (s *Service) GetGame(gameID string) (*domain.Game, error) {
	game, ok := s.games[gameID]
	if !ok {
		return nil, domain.ErrGameNotFound
	}
	return game, nil
}

// ListGames returns all configured games
func (s *Service) ListGames() []*domain.Game {
	games := make([]*domain.Game, 0, len(s.games))
	for _, g := range s.games {
		games = append(games, g)
	}
	return games
}

// AddGame adds a new game configuration
func (s *Service) AddGame(game *domain.Game) error {
	if err := config.SaveGame(s.configDir, game); err != nil {
		return err
	}
	s.games[game.ID] = game
	return nil
}

// GetInstalledMods returns all installed mods for a game/profile
func (s *Service) GetInstalledMods(gameID, profileName string) ([]domain.InstalledMod, error) {
	return s.db.GetInstalledMods(gameID, profileName)
}

// GetLinker returns a linker for the given method
func (s *Service) GetLinker(method domain.LinkMethod) linker.Linker {
	return linker.New(method)
}

// GetDefaultLinkMethod returns the default link method from config
func (s *Service) GetDefaultLinkMethod() domain.LinkMethod {
	return s.config.DefaultLinkMethod
}

// Cache returns the cache manager
func (s *Service) Cache() *cache.Cache {
	return s.cache
}

// DB returns the database
func (s *Service) DB() *db.DB {
	return s.db
}

// ConfigDir returns the configuration directory
func (s *Service) ConfigDir() string {
	return s.configDir
}
