package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"
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

// GetModFiles retrieves available download files for a mod
func (s *Service) GetModFiles(ctx context.Context, sourceID string, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil, err
	}

	return src.GetModFiles(ctx, mod)
}

// GetDownloadURL gets the download URL for a specific mod file
func (s *Service) GetDownloadURL(ctx context.Context, sourceID string, mod *domain.Mod, fileID string) (string, error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return "", err
	}

	return src.GetDownloadURL(ctx, mod, fileID)
}

// DownloadMod downloads a mod file, extracts it, and stores it in the cache
// Returns the number of files extracted from this specific download.
// Multiple files from the same mod can be downloaded to the same cache location.
func (s *Service) DownloadMod(ctx context.Context, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, progressFn ProgressFunc) (int, error) {
	// Get game-specific cache
	gameCache := s.GetGameCache(game)

	// Note: We intentionally do NOT check if cache exists here.
	// A mod can have multiple downloadable files (e.g., main mod + optional patches),
	// and each file should be downloaded and extracted to the cache.
	// The cache directory may already exist from a previous file download.

	// Get download URL
	url, err := s.GetDownloadURL(ctx, sourceID, mod, file.ID)
	if err != nil {
		return 0, fmt.Errorf("getting download URL: %w", err)
	}

	// Create temp directory for download
	tempDir, err := os.MkdirTemp("", "lmm-download-*")
	if err != nil {
		return 0, fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Download the file
	archivePath := filepath.Join(tempDir, file.FileName)
	downloader := NewDownloader(nil)
	if err := downloader.Download(ctx, url, archivePath, progressFn); err != nil {
		return 0, fmt.Errorf("downloading mod: %w", err)
	}

	// Extract to cache location
	cachePath := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
	extractor := NewExtractor()
	if !extractor.CanExtract(file.FileName) {
		// Not an archive - just copy to cache
		if err := os.MkdirAll(cachePath, 0755); err != nil {
			return 0, fmt.Errorf("creating cache directory: %w", err)
		}
		destPath := filepath.Join(cachePath, file.FileName)
		content, err := os.ReadFile(archivePath)
		if err != nil {
			return 0, fmt.Errorf("reading downloaded file: %w", err)
		}
		if err := os.WriteFile(destPath, content, 0644); err != nil {
			return 0, fmt.Errorf("writing to cache: %w", err)
		}
		return 1, nil
	}

	if err := extractor.Extract(archivePath, cachePath); err != nil {
		return 0, fmt.Errorf("extracting mod: %w", err)
	}

	// Count extracted files
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return 0, err
	}

	return len(files), nil
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

// GetGameLinkMethod returns the effective link method for a game.
// Uses the game's explicit setting if configured, otherwise falls back to global default.
func (s *Service) GetGameLinkMethod(game *domain.Game) domain.LinkMethod {
	if game.LinkMethodExplicit {
		return game.LinkMethod
	}
	return s.config.DefaultLinkMethod
}

// GetInstaller returns an Installer configured for the given game
func (s *Service) GetInstaller(game *domain.Game) *Installer {
	lnk := s.GetLinker(s.GetGameLinkMethod(game))
	return NewInstaller(s.GetGameCache(game), lnk)
}

// Cache returns the default cache manager
func (s *Service) Cache() *cache.Cache {
	return s.cache
}

// GetGameCachePath returns the effective cache path for a game.
// Uses the game's cache_path if configured, otherwise falls back to global cache.
func (s *Service) GetGameCachePath(game *domain.Game) string {
	if game.CachePath != "" {
		return game.CachePath
	}
	return s.cacheDir
}

// GetGameCache returns a cache manager for the specified game.
// Uses the game's cache_path if configured, otherwise uses the global cache.
func (s *Service) GetGameCache(game *domain.Game) *cache.Cache {
	if game.CachePath != "" {
		return cache.New(game.CachePath)
	}
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

// Registry returns the source registry
func (s *Service) Registry() *source.Registry {
	return s.registry
}

// SaveSourceToken saves an API token for a source
func (s *Service) SaveSourceToken(sourceID, apiKey string) error {
	return s.db.SaveToken(sourceID, apiKey)
}

// GetSourceToken retrieves an API token for a source
func (s *Service) GetSourceToken(sourceID string) (*db.StoredToken, error) {
	return s.db.GetToken(sourceID)
}

// DeleteSourceToken removes an API token for a source
func (s *Service) DeleteSourceToken(sourceID string) error {
	return s.db.DeleteToken(sourceID)
}

// IsSourceAuthenticated checks if a source has a stored API token
func (s *Service) IsSourceAuthenticated(sourceID string) bool {
	has, err := s.db.HasToken(sourceID)
	if err != nil {
		return false
	}
	return has
}

// UpdateModVersion updates the version of an installed mod, preserving the previous version for rollback
func (s *Service) UpdateModVersion(sourceID, modID, gameID, profileName, newVersion string) error {
	return s.db.UpdateModVersion(sourceID, modID, gameID, profileName, newVersion)
}

// RollbackModVersion reverts a mod to its previous version
func (s *Service) RollbackModVersion(sourceID, modID, gameID, profileName string) error {
	return s.db.SwapModVersions(sourceID, modID, gameID, profileName)
}

// SetModUpdatePolicy sets the update policy for an installed mod
func (s *Service) SetModUpdatePolicy(sourceID, modID, gameID, profileName string, policy domain.UpdatePolicy) error {
	return s.db.UpdateModPolicy(sourceID, modID, gameID, profileName, policy)
}

// SetModLinkMethod sets the deployment method for an installed mod
func (s *Service) SetModLinkMethod(sourceID, modID, gameID, profileName string, linkMethod domain.LinkMethod) error {
	return s.db.SetModLinkMethod(sourceID, modID, gameID, profileName, linkMethod)
}

// SetModFileIDs updates the file IDs for an installed mod
func (s *Service) SetModFileIDs(sourceID, modID, gameID, profileName string, fileIDs []string) error {
	return s.db.SetModFileIDs(sourceID, modID, gameID, profileName, fileIDs)
}

// GetInstalledMod retrieves a single installed mod
func (s *Service) GetInstalledMod(sourceID, modID, gameID, profileName string) (*domain.InstalledMod, error) {
	return s.db.GetInstalledMod(sourceID, modID, gameID, profileName)
}
