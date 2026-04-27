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

// DownloadModResult contains the outcome of downloading a mod file
type DownloadModResult struct {
	FilesExtracted int    // Number of files extracted
	Checksum       string // MD5 hash of downloaded archive
}

// Service is the main orchestrator for mod management operations
type Service struct {
	config     *config.Config
	db         *db.DB
	cache      *cache.Cache
	registry   *source.Registry
	games      map[string]*domain.Game
	downloader *Downloader
	extractor  *Extractor

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
		if closeErr := database.Close(); closeErr != nil {
			return nil, &domain.DeployError{Op: "loading games", Primary: err, Cleanup: closeErr}
		}
		return nil, fmt.Errorf("loading games: %w", err)
	}

	return &Service{
		config:     appConfig,
		db:         database,
		cache:      cache.New(cfg.CacheDir),
		registry:   source.NewRegistry(),
		games:      games,
		downloader: NewDownloader(nil),
		extractor:  NewExtractor(),
		configDir:  cfg.ConfigDir,
		dataDir:    cfg.DataDir,
		cacheDir:   cfg.CacheDir,
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
func (s *Service) SearchMods(ctx context.Context, sourceID, gameID, query string, category string, tags []string, page, pageSize int) (source.SearchResult, error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return source.SearchResult{}, err
	}

	sourceGameID := gameID
	if game, ok := s.games[gameID]; ok {
		if id, ok := game.SourceIDs[sourceID]; ok {
			sourceGameID = id
		}
	}

	return src.Search(ctx, source.SearchQuery{
		GameID:   sourceGameID,
		Query:    query,
		Category: category,
		Tags:     tags,
		Page:     page,
		PageSize: pageSize,
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
// Returns the download result including files extracted and checksum.
// Multiple files from the same mod can be downloaded to the same cache location.
func (s *Service) DownloadMod(ctx context.Context, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, progressFn ProgressFunc) (result *DownloadModResult, err error) {
	return s.DownloadModToCache(ctx, s.GetGameCache(game), sourceID, game, mod, file, progressFn)
}

// DownloadModToCache downloads a mod file, extracts it, and stores it in the provided cache.
func (s *Service) DownloadModToCache(ctx context.Context, gameCache *cache.Cache, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, progressFn ProgressFunc) (result *DownloadModResult, err error) {

	// Note: We intentionally do NOT check if cache exists here.
	// A mod can have multiple downloadable files (e.g., main mod + optional patches),
	// and each file should be downloaded and extracted to the cache.
	// The cache directory may already exist from a previous file download.

	// Get download URL
	url, err := s.GetDownloadURL(ctx, sourceID, mod, file.ID)
	if err != nil {
		return nil, fmt.Errorf("getting download URL: %w", err)
	}

	// Create temp directory for download
	tempDir, err := os.MkdirTemp("", "lmm-download-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp directory: %w", err)
	}
	defer func() {
		if cerr := os.RemoveAll(tempDir); err == nil && cerr != nil {
			err = fmt.Errorf("removing temp directory: %w", cerr)
		}
	}()

	// Download the file
	archivePath := filepath.Join(tempDir, file.FileName)
	downloadResult, err := s.downloader.Download(ctx, url, archivePath, progressFn)
	if err != nil {
		return nil, fmt.Errorf("downloading mod: %w", err)
	}

	// Extract to cache location
	cachePath := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
	stagePath := cachePath + ".staging"
	if err := os.RemoveAll(stagePath); err != nil {
		return nil, fmt.Errorf("clearing staging cache: %w", err)
	}
	defer os.RemoveAll(stagePath) //nolint:errcheck
	if gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		if err := copyDir(cachePath, stagePath); err != nil {
			return nil, fmt.Errorf("staging existing cache: %w", err)
		}
	}
	if game.DeployMode == domain.DeployCopy || !s.extractor.CanExtract(archivePath) {
		// Copy mode: game wants files as-is (e.g., Hytale .zip mods)
		// Or not an archive - just copy to cache
		if err := os.MkdirAll(stagePath, 0755); err != nil {
			return nil, fmt.Errorf("creating cache directory: %w", err)
		}
		destPath := filepath.Join(stagePath, file.FileName)
		if err := copyFileStreaming(archivePath, destPath); err != nil {
			return nil, fmt.Errorf("copying to cache: %w", err)
		}
		if err := commitStagedCache(cachePath, stagePath); err != nil {
			return nil, err
		}
		return &DownloadModResult{
			FilesExtracted: 1,
			Checksum:       downloadResult.Checksum,
		}, nil
	}

	if err := s.extractor.Extract(archivePath, stagePath); err != nil {
		return nil, fmt.Errorf("extracting mod: %w", err)
	}
	if err := commitStagedCache(cachePath, stagePath); err != nil {
		return nil, err
	}

	// Count extracted files
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil, err
	}

	return &DownloadModResult{
		FilesExtracted: len(files),
		Checksum:       downloadResult.Checksum,
	}, nil
}

func commitStagedCache(cachePath, stagePath string) error {
	parentDir := filepath.Dir(cachePath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("creating cache parent directory: %w", err)
	}

	backupPath := cachePath + ".backup"
	if err := os.RemoveAll(backupPath); err != nil {
		return fmt.Errorf("clearing cache backup: %w", err)
	}
	if _, err := os.Stat(cachePath); err == nil {
		if err := os.Rename(cachePath, backupPath); err != nil {
			return fmt.Errorf("backing up existing cache: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking existing cache: %w", err)
	}
	if err := os.Rename(stagePath, cachePath); err != nil {
		if _, statErr := os.Stat(backupPath); statErr == nil {
			_ = os.Rename(backupPath, cachePath)
		}
		return fmt.Errorf("activating staged cache: %w", err)
	}
	if err := os.RemoveAll(backupPath); err != nil {
		return fmt.Errorf("removing old cache backup: %w", err)
	}
	return nil
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

// GetInstalledMods returns all installed mods for a game/profile (DB order: installed_at).
func (s *Service) GetInstalledMods(gameID, profileName string) ([]domain.InstalledMod, error) {
	return s.db.GetInstalledMods(gameID, profileName)
}

// GetInstalledModsInProfileOrder returns installed mods in profile load order (first = lowest priority).
// Mods not present in the profile are omitted. Use this for deploy/switch so deployment order matches load order.
func (s *Service) GetInstalledModsInProfileOrder(gameID, profileName string) ([]domain.InstalledMod, error) {
	profile, err := config.LoadProfile(s.configDir, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile: %w", err)
	}
	all, err := s.db.GetInstalledMods(gameID, profileName)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]*domain.InstalledMod)
	for i := range all {
		byKey[domain.ModKey(all[i].SourceID, all[i].ID)] = &all[i]
	}
	var ordered []domain.InstalledMod
	for _, ref := range profile.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if m, ok := byKey[key]; ok {
			ordered = append(ordered, *m)
		}
	}
	return ordered, nil
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
	return s.NewInstallerWithLinker(game, s.GetLinker(s.GetGameLinkMethod(game)))
}

// NewInstallerWithLinker returns an Installer for the given game using a
// caller-supplied linker — used when the CLI overrides the game's default
// link method (e.g. `lmm deploy --method`).
func (s *Service) NewInstallerWithLinker(game *domain.Game, lnk linker.Linker) *Installer {
	return NewInstaller(s.GetGameCache(game), lnk, s.db)
}

// NewProfileManager returns a ProfileManager wired to this service's storage,
// so callers do not need direct access to the database or registry.
func (s *Service) NewProfileManager() *ProfileManager {
	return NewProfileManager(s.configDir, s.db, s.cache, s.GetLinker(s.config.DefaultLinkMethod))
}

// NewUpdater returns an Updater wired to this service's source registry.
func (s *Service) NewUpdater() *Updater {
	return NewUpdater(s.registry)
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
// Uses the game's cache_path if configured (game-scoped: paths omit gameID), otherwise the global cache.
func (s *Service) GetGameCache(game *domain.Game) *cache.Cache {
	if game.CachePath != "" {
		return cache.NewGameScoped(game.CachePath)
	}
	return s.cache
}

// ConfigDir returns the configuration directory
func (s *Service) ConfigDir() string {
	return s.configDir
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

// ApplyModUpdate updates version and file IDs atomically, preserving rollback state.
func (s *Service) ApplyModUpdate(sourceID, modID, gameID, profileName, newVersion string, fileIDs []string) error {
	return s.db.ApplyModUpdate(sourceID, modID, gameID, profileName, newVersion, fileIDs)
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

// SetModEnabled toggles the enabled flag for an installed mod.
func (s *Service) SetModEnabled(sourceID, modID, gameID, profileName string, enabled bool) error {
	return s.db.SetModEnabled(sourceID, modID, gameID, profileName, enabled)
}

// SetModDeployed records whether a mod's files are currently deployed.
func (s *Service) SetModDeployed(sourceID, modID, gameID, profileName string, deployed bool) error {
	return s.db.SetModDeployed(sourceID, modID, gameID, profileName, deployed)
}

// SaveInstalledMod persists an installed-mod record (insert or update).
func (s *Service) SaveInstalledMod(mod *domain.InstalledMod) error {
	return s.db.SaveInstalledMod(mod)
}

// DeleteInstalledMod removes the installed-mod record from the active profile.
func (s *Service) DeleteInstalledMod(sourceID, modID, gameID, profileName string) error {
	return s.db.DeleteInstalledMod(sourceID, modID, gameID, profileName)
}

// GetDeployedFilesForMod returns the relative paths the given mod has deployed
// in the named profile.
func (s *Service) GetDeployedFilesForMod(gameID, profileName, sourceID, modID string) ([]string, error) {
	return s.db.GetDeployedFilesForMod(gameID, profileName, sourceID, modID)
}

// GetFileOwner reports which mod currently owns a deployed file. The bool is
// false when no record exists; err is non-nil only on storage errors.
func (s *Service) GetFileOwner(gameID, profileName, relativePath string) (sourceID, modID string, found bool, err error) {
	owner, err := s.db.GetFileOwner(gameID, profileName, relativePath)
	if err != nil {
		return "", "", false, err
	}
	if owner == nil {
		return "", "", false, nil
	}
	return owner.SourceID, owner.ModID, true, nil
}

// DeployedFile is a service-boundary view of a tracked mod file with its checksum.
type DeployedFile struct {
	SourceID string
	ModID    string
	FileID   string
	Checksum string
}

// GetFilesWithChecksums returns every tracked file in the profile with its
// recorded checksum (empty when none has been computed yet).
func (s *Service) GetFilesWithChecksums(gameID, profileName string) ([]DeployedFile, error) {
	rows, err := s.db.GetFilesWithChecksums(gameID, profileName)
	if err != nil {
		return nil, err
	}
	out := make([]DeployedFile, len(rows))
	for i, r := range rows {
		out[i] = DeployedFile{SourceID: r.SourceID, ModID: r.ModID, FileID: r.FileID, Checksum: r.Checksum}
	}
	return out, nil
}

// SaveFileChecksum records the verified checksum for a downloaded mod file.
func (s *Service) SaveFileChecksum(sourceID, modID, gameID, profileName, fileID, checksum string) error {
	return s.db.SaveFileChecksum(sourceID, modID, gameID, profileName, fileID, checksum)
}

// GetInstalledMod retrieves a single installed mod
func (s *Service) GetInstalledMod(sourceID, modID, gameID, profileName string) (*domain.InstalledMod, error) {
	return s.db.GetInstalledMod(sourceID, modID, gameID, profileName)
}

// GetDependencies returns dependencies for a mod from the specified source
func (s *Service) GetDependencies(ctx context.Context, sourceID string, mod *domain.Mod) ([]domain.ModReference, error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil, err
	}
	return src.GetDependencies(ctx, mod)
}

// copyFileStreaming copies a file using streaming to avoid loading it all into memory
