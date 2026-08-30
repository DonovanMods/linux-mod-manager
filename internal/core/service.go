package core

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"
	"golang.org/x/sync/errgroup"
)

// ServiceConfig holds configuration for the core service
type ServiceConfig struct {
	ConfigDir string       // Directory for configuration files
	DataDir   string       // Directory for database and persistent data
	CacheDir  string       // Directory for mod file cache
	Logger    *slog.Logger // Diagnostics logger; nil means discard
}

// DownloadModResult contains the outcome of downloading a mod file
type DownloadModResult struct {
	FilesExtracted int    `json:"files_extracted"` // Number of files extracted
	Checksum       string `json:"checksum"`        // MD5 hash of downloaded archive
}

// Service is the application facade every frontend talks to.
//
// It is safe for concurrent use with this guarantee: query methods (Get*,
// List*, Plan*, Search*, CheckGameUpdates, Verify without Fix) may run
// concurrently with each other and with at most one in-flight mutation;
// mutating operations (Apply*, DeployProfile, PurgeProfile, UninstallMod,
// EnableMod/DisableMod, Set*, Save*, Delete*, Reorder*, SyncMergedPak, Verify
// with Fix) are serialized service-wide through a one-slot semaphore
// acquired with the caller's ctx, so a waiter is itself cancellable. Reads
// during a mutation observe WAL snapshot state, which is per-mod consistent
// (spec §3).
//
// NewProfileManager returns a ProfileManager whose file mutations are NOT
// serialized through this semaphore; Phase 2 lifts those flows into
// serialized Service methods.
type Service struct {
	config     *config.Config
	db         *db.DB
	cache      *cache.Cache
	registry   *source.Registry
	gamesMu    sync.RWMutex
	games      map[string]*domain.Game
	opSem      chan struct{}
	downloader *Downloader
	extractor  *Extractor
	log        *slog.Logger // Diagnostics logger; nil means discard (see logger()).

	configDir string
	dataDir   string
	cacheDir  string

	// beforeSaveInstalled, when non-nil, runs immediately before the install
	// flow's SaveInstalledMod call - the only point between a successful
	// deploy and the DB write, and therefore the only place a test can arm
	// the failure the deploy-recovery paths exist for. Test-only seam
	// (export_test.go's SetBeforeSaveInstalledForTest); always nil in
	// production.
	beforeSaveInstalled func()
}

// NewService creates a new core service instance
func NewService(cfg ServiceConfig) (*Service, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// Load configuration
	appConfig, err := config.Load(cfg.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	// Open database
	dbPath := filepath.Join(cfg.DataDir, "lmm.db")
	database, err := db.Open(dbPath, log)
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

	modCache := cache.New(cfg.CacheDir)
	modCache.SetLogger(log)
	downloader := NewDownloader(nil)
	downloader.SetLogger(log)

	return &Service{
		config:     appConfig,
		db:         database,
		cache:      modCache,
		registry:   source.NewRegistry(),
		games:      games,
		opSem:      make(chan struct{}, 1),
		downloader: downloader,
		extractor:  NewExtractor(),
		log:        log,
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

// Logger returns the diagnostics logger this Service was constructed with
// (ServiceConfig.Logger), or a discarding logger if none was given.
func (s *Service) Logger() *slog.Logger {
	return s.logger()
}

// discardLogger is handed back by logger() for a Service whose log field is
// nil - a single shared instance rather than allocating one per call.
var discardLogger = slog.New(slog.DiscardHandler)

// logger returns s.log if set, or discardLogger otherwise, so every internal
// log call site is safe even for a Service built by a raw struct literal
// (several white-box tests in this package do this) rather than NewService,
// which is the only place s.log is normally populated.
func (s *Service) logger() *slog.Logger {
	if s.log == nil {
		return discardLogger
	}
	return s.log
}

// RegisterSource adds a mod source to the registry
func (s *Service) RegisterSource(src source.ModSource) {
	s.registry.Register(src)
}

// GetSource retrieves a source by ID
func (s *Service) GetSource(id string) (source.ModSource, error) {
	return s.registry.Get(id)
}

// ValidateInstallFileSelection rejects an install selection that mixes a
// merge-compile source's exmodz variant with any other file (#211): the two
// are alternate forms of the same mod, and installing both double-applies
// its table edits (the pak deploys standalone while the exmodz joins the
// merged pak). Sources that don't implement source.MergeCompiler are never
// restricted, single-file selections are always fine, and an unknown
// sourceID is not this check's problem - pool resolution errors on it
// first.
func (s *Service) ValidateInstallFileSelection(sourceID string, files []domain.DownloadableFile) error {
	if len(files) < 2 {
		return nil
	}
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil
	}
	mc, ok := src.(source.MergeCompiler)
	if !ok {
		return nil
	}
	// The colliding filenames are captured for the error: naming the actual
	// files keeps the message format-agnostic (the vocabulary comes from
	// the selection itself, not a hardcoded format list) and tells the user
	// WHICH two files collided, not just that a collision happened (#256).
	var native, other bool
	var nativeName, otherName string
	for _, f := range files {
		if mc.IsNativeMergeSource(f.FileName) {
			native, nativeName = true, f.FileName
		} else {
			other, otherName = true, f.FileName
		}
	}
	if native && other {
		return fmt.Errorf("%s and %s are alternate forms of the same mod - select one", otherName, nativeName)
	}
	return nil
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
	if game, ok := s.game(gameID); ok {
		// An empty mapping (e.g. directory sources: `donovan-mods: ""`) means
		// "this source applies to any game" — it must not blank out the ID.
		if id, ok := game.SourceIDs[sourceID]; ok && id != "" {
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

// SourcesForGame resolves gameID and returns the subset of its configured
// sources (game.SourceIDs keys) that are currently registered, sorted by
// ID(). A SourceIDs key with no matching registration is silently skipped -
// this function has no per-item error channel, only resolved ModSource
// values come back - matching searchAllSources's existing tolerance for the
// same situation. An unknown game is the only error case.
func (s *Service) SourcesForGame(gameID string) ([]source.ModSource, error) {
	game, ok := s.game(gameID)
	if !ok {
		// Wrap the sentinel like GetGame does, so callers can errors.Is;
		// the visible text stays "game not found: <id>".
		return nil, fmt.Errorf("%w: %s", domain.ErrGameNotFound, gameID)
	}

	srcs := make([]source.ModSource, 0, len(game.SourceIDs))
	for id := range game.SourceIDs {
		src, err := s.registry.Get(id)
		if err != nil {
			continue // unregistered: silently skipped
		}
		srcs = append(srcs, src)
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].ID() < srcs[j].ID() })
	return srcs, nil
}

// mergeCompilerSourceForGame resolves the sole MergeCompiler-capable source
// registered for gameID (#173). The download path pins its MergeCompiler
// check to the specific source a file was downloaded from
// (DownloadModToCache's src.(source.MergeCompiler) check); Importer.Import
// has no such per-archive source to key off of, so it resolves against
// every source the game maps in its registry instead — at most one of a
// game's configured sources implements MergeCompiler today. Zero is the expected
// failure when the game (or its MergeCompiler source) isn't configured;
// more than one is treated as ambiguous rather than picking arbitrarily —
// both fail loud instead of letting an .exmodz import silently skip
// validation.
func (s *Service) mergeCompilerSourceForGame(gameID string) (source.MergeCompiler, error) {
	srcs, err := s.SourcesForGame(gameID)
	if err != nil {
		return nil, err
	}
	var compilers []source.MergeCompiler
	for _, src := range srcs {
		if c, ok := src.(source.MergeCompiler); ok {
			compilers = append(compilers, c)
		}
	}
	return soleMergeCompiler(gameID, compilers)
}

// mergeCompilerForGame is mergeCompilerSourceForGame for callers that
// already hold the *domain.Game (#256): it resolves against the game
// struct's own source map instead of re-looking the game up in s.games, so
// merged-pak paths that always received their game as a parameter keep
// working for a game value that was never registered with the service (a
// distinction only tests exercise today). Same 0/1/many contract.
func (s *Service) mergeCompilerForGame(game *domain.Game) (source.MergeCompiler, error) {
	var compilers []source.MergeCompiler
	for id := range game.SourceIDs {
		src, err := s.registry.Get(id)
		if err != nil {
			continue // unregistered: silently skipped, matching SourcesForGame
		}
		if c, ok := src.(source.MergeCompiler); ok {
			compilers = append(compilers, c)
		}
	}
	return soleMergeCompiler(game.ID, compilers)
}

// soleMergeCompiler enforces the "exactly one compile-capable source per
// game" contract shared by both resolvers above: zero is the expected
// failure when the game's MergeCompiler source isn't configured; more than
// one is treated as ambiguous rather than picking arbitrarily - both fail
// loud instead of letting a compile-path operation silently skip.
func soleMergeCompiler(gameID string, compilers []source.MergeCompiler) (source.MergeCompiler, error) {
	switch len(compilers) {
	case 0:
		return nil, fmt.Errorf("game %q requires DeployCompile but has no merge-compiler-capable source configured (map a source implementing source.MergeCompiler in the game's sources)", gameID)
	case 1:
		return compilers[0], nil
	default:
		return nil, fmt.Errorf("game %q has multiple merge-compiler-capable sources configured; ambiguous compile source", gameID)
	}
}

// SourceWarning reports a per-source failure during an aggregate operation.
type SourceWarning struct {
	SourceID     string `json:"source_id"`
	Err          error  `json:"-"`
	ErrorMessage string `json:"error,omitempty"` // Err.Error(), when Err is set
}

// newSourceWarning builds a SourceWarning with ErrorMessage and Err paired
// from a single error, so a construction site can't emit one without the
// other (final review, Minor #1 / #282).
func newSourceWarning(id string, err error) SourceWarning {
	return SourceWarning{SourceID: id, ErrorMessage: err.Error(), Err: err}
}

// AggregateSearchResult is the merged outcome of searching every source
// configured for a game.
type AggregateSearchResult struct {
	Mods       []domain.Mod    `json:"mods"`               // merged, ranked; each Mod carries its SourceID
	TotalCount int             `json:"total_count"`        // sum of per-source totals (sources reporting 0/unknown contribute 0)
	Warnings   []SourceWarning `json:"warnings,omitempty"` // per-source failures (design §5: warnings, not errors)
	// Exhausted reports whether every source that successfully returned a
	// result for THIS page has nothing left to page through (#58 item 1).
	// TotalCount is summed across sources with INDEPENDENT per-source
	// pagination cursors, so a caller cannot derive "is there a next page"
	// from TotalCount and a single global PageSize the way single-source
	// search can (see sourceHasMore below): 3 sources whose
	// entire 10-mod catalog fits on page 0 sum to a TotalCount of 30, which
	// against a pageSize of 10 falsely implies 3 pages exist, when actually
	// every source already returned everything it has. Exhausted applies
	// hasNextPage's own per-source heuristic (TotalCount-bounded when a
	// source reports one, else "a short page means no more") to EACH
	// contributing source and ANDs the results, so it is the accurate signal
	// callers should gate a next-page offer on instead. True when there were
	// zero successful sources too (nothing left to page through).
	Exhausted bool `json:"exhausted"`
	// AttemptedCount is how many of the game's configured sources actually
	// had a search attempted against them - capability-less sources are
	// skipped silently (see searchAllSources's doc comment) and never
	// counted here. Zero means NONE of the game's sources support searching
	// at all, which is indistinguishable from a genuine zero-result search
	// unless a caller checks this field - the honesty-notice fix (#58 item
	// 3): callers render a distinct "no source supports search" notice
	// instead of a plain "no mods found" when this is 0.
	AttemptedCount int `json:"attempted_count"`
}

// sourceHasMore reports whether res (one source's response to the given
// page/pageSize request) might have a page N+1, using the per-single-source
// heuristic (TotalCount bounds it precisely when the source reports one;
// otherwise a full page might mean more, a short one means none) - applied
// here per CONTRIBUTING SOURCE so searchAllSources can tell a
// truly-exhausted merge from one that might still have more (see
// AggregateSearchResult.Exhausted's doc comment).
// pageSize <= 0 (e.g. the CLI's "let the source apply its own default" case,
// see cmd/lmm/search.go's searchPageSize) has no next-page concept at all.
func sourceHasMore(res source.SearchResult, page, pageSize int) bool {
	if pageSize <= 0 {
		return false
	}
	if res.TotalCount > 0 {
		return (page+1)*pageSize < res.TotalCount
	}
	return len(res.Mods) == pageSize
}

// searchAllSources searches every source configured for a game concurrently
// and merges the results (design §5). Per-source failures become Warnings —
// one flaky API must not hide local modlets; only all-sources-failed is an
// error. Sources without search capability are skipped silently. Pagination
// is per-source: page N requests page N from each source and merges.
func (s *Service) searchAllSources(ctx context.Context, gameID, query, category string, tags []string, page, pageSize int) (AggregateSearchResult, error) {
	game, ok := s.game(gameID)
	if !ok {
		return AggregateSearchResult{}, fmt.Errorf("game not found: %s", gameID)
	}

	// SourcesForGame gives the registered subset in one call; a SourceIDs key
	// missing from it is unregistered, which here (unlike SourcesForGame's
	// own silent-skip contract) must surface as a per-source Warning, so the
	// miss case below still calls registry.Get directly for that error.
	registered, err := s.SourcesForGame(gameID)
	if err != nil {
		return AggregateSearchResult{}, err
	}
	registeredByID := make(map[string]source.ModSource, len(registered))
	for _, src := range registered {
		registeredByID[src.ID()] = src
	}

	sourceIDs := make([]string, 0, len(game.SourceIDs))
	for id := range game.SourceIDs {
		sourceIDs = append(sourceIDs, id)
	}
	sort.Strings(sourceIDs)

	var result AggregateSearchResult
	type slot struct {
		res source.SearchResult
		err error
	}
	slots := make([]slot, len(sourceIDs))
	attempted := make([]bool, len(sourceIDs))

	g, gctx := errgroup.WithContext(ctx)
	for i, sourceID := range sourceIDs {
		src, ok := registeredByID[sourceID]
		if !ok {
			// Reproduce the exact not-found error. Sound while Get stays a
			// pure map read and registration stays startup-only (single call
			// site in cmd/lmm root); revisit if the registry ever hot-reloads.
			_, err := s.registry.Get(sourceID)
			slots[i].err = err
			attempted[i] = true
			continue
		}
		if !source.CapabilitiesOf(src).Search {
			continue // silent skip (design §5)
		}
		attempted[i] = true
		i, sourceID := i, sourceID
		g.Go(func() error {
			res, err := s.SearchMods(gctx, sourceID, gameID, query, category, tags, page, pageSize)
			if err != nil {
				if errors.Is(err, source.ErrNotSupported) {
					attempted[i] = false // runtime capability gap: silent skip, not a warning
					return nil
				}
				slots[i].err = err
				return nil // never abort the group: siblings keep searching
			}
			slots[i].res = res
			return nil
		})
	}
	_ = g.Wait() // goroutines always return nil; errors live in slots

	succeeded := 0
	allExhausted := true // vacuously true until a succeeding source proves otherwise
	for i, sourceID := range sourceIDs {
		if !attempted[i] {
			continue
		}
		if slots[i].err != nil {
			result.Warnings = append(result.Warnings, newSourceWarning(sourceID, slots[i].err))
			continue
		}
		succeeded++
		result.Mods = append(result.Mods, slots[i].res.Mods...)
		result.TotalCount += slots[i].res.TotalCount
		if sourceHasMore(slots[i].res, page, pageSize) {
			allExhausted = false
		}
	}
	result.Exhausted = allExhausted

	rankAggregate(result.Mods, query)

	attemptedCount := 0
	for _, a := range attempted {
		if a {
			attemptedCount++
		}
	}
	result.AttemptedCount = attemptedCount
	if attemptedCount > 0 && succeeded == 0 {
		errs := make([]error, 0, len(result.Warnings))
		for _, w := range result.Warnings {
			errs = append(errs, fmt.Errorf("source %s: %w", w.SourceID, w.Err))
		}
		return result, fmt.Errorf("all %d source(s) failed: %w", attemptedCount, errors.Join(errs...))
	}
	return result, nil
}

// rankAggregate orders merged results: query-name matches first, then by
// Downloads descending, then Name ascending — deterministic regardless of
// which source responded first (design §5: no global re-ranking beyond this).
func rankAggregate(mods []domain.Mod, query string) {
	q := strings.ToLower(query)
	nameMatch := func(m domain.Mod) bool {
		return q != "" && (strings.Contains(strings.ToLower(m.Name), q) || strings.Contains(strings.ToLower(m.ID), q))
	}
	sort.SliceStable(mods, func(i, j int) bool {
		mi, mj := nameMatch(mods[i]), nameMatch(mods[j])
		if mi != mj {
			return mi
		}
		if mods[i].Downloads != mods[j].Downloads {
			return mods[i].Downloads > mods[j].Downloads
		}
		return mods[i].Name < mods[j].Name
	})
}

// GetMod retrieves a specific mod from a source
func (s *Service) GetMod(ctx context.Context, sourceID, gameID, modID string) (*domain.Mod, error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil, err
	}

	// Get the source-specific game ID if we have a game configured. An empty
	// mapping (e.g. directory sources: `donovan-mods: ""`) means "this source
	// applies to any game" — it must not blank out the ID.
	sourceGameID := gameID
	if game, ok := s.game(gameID); ok {
		if id, ok := game.SourceIDs[sourceID]; ok && id != "" {
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

// ResolveModVersion fetches mod's raw file list from sourceID and resolves
// version against it via ResolveVersionFiles (#96). The list is deliberately
// unfiltered - archived files are exactly what a version pin usually names.
func (s *Service) ResolveModVersion(ctx context.Context, sourceID string, mod *domain.Mod, version string) ([]domain.DownloadableFile, error) {
	files, err := s.GetModFiles(ctx, sourceID, mod)
	if err != nil {
		return nil, fmt.Errorf("listing files for version resolution: %w", err)
	}
	return ResolveVersionFiles(sourceID, files, version)
}

// AvailableModVersions lists the distinct per-file versions mod's source
// reports, in first-seen order (#97).
// Wraps source.ErrNotSupported (same format as ResolveVersionFiles) when
// the file list carries no version info at all.
func (s *Service) AvailableModVersions(ctx context.Context, sourceID string, mod *domain.Mod) ([]string, error) {
	files, err := s.GetModFiles(ctx, sourceID, mod)
	if err != nil {
		return nil, fmt.Errorf("listing files for version resolution: %w", err)
	}
	if !anyFileHasVersion(files) {
		return nil, fmt.Errorf("source %q: version resolution: %w", sourceID, source.ErrNotSupported)
	}
	return availableVersions(files), nil
}

// SourceCapabilities reports sourceID's declared capabilities (#97: static
// lock gating). Mirrors searchAllSources' registry access (service.go's
// source.CapabilitiesOf(src) call).
func (s *Service) SourceCapabilities(sourceID string) (source.Capabilities, error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return source.Capabilities{}, err
	}
	return source.CapabilitiesOf(src), nil
}

// DownloadMod downloads a mod file, extracts it, and stores it in the cache
// Returns the download result including files extracted and checksum.
// Multiple files from the same mod can be downloaded to the same cache location.
//
// It stays EXPORTED with no production cmd caller (the install/update/deploy
// flows all reach downloadMod internally): cmd/lmm/verify_test.go seeds its
// fixtures by driving a real download round-trip through it, so unexporting
// it would leave that test with no way to produce a genuine cache entry
// plus checksum. Same reason SaveFileChecksum stays exported. Retire it only
// together with a fixture that no longer needs a real download.
func (s *Service) DownloadMod(ctx context.Context, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, sink EventSink) (result *DownloadModResult, err error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.downloadMod(ctx, sourceID, game, mod, file, sink)
}

func (s *Service) downloadMod(ctx context.Context, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, sink EventSink) (result *DownloadModResult, err error) {
	return s.downloadModToCache(ctx, s.GetGameCache(game), sourceID, game, mod, file, sink)
}

func (s *Service) downloadModToCache(ctx context.Context, gameCache *cache.Cache, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, sink EventSink) (result *DownloadModResult, err error) {

	// Note: We intentionally do NOT check if cache exists here.
	// A mod can have multiple downloadable files (e.g., main mod + optional patches),
	// and each file should be downloaded and extracted to the cache.
	// The cache directory may already exist from a previous file download.

	// Get the source so we can gate the local-ingest branch below to sources
	// that are actually allowed to serve local files.
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil, fmt.Errorf("getting source: %w", err)
	}

	url, err := src.GetDownloadURL(ctx, mod, file.ID)
	if err != nil {
		return nil, fmt.Errorf("getting download URL: %w", err)
	}

	if localPath, ok := strings.CutPrefix(url, "file://"); ok {
		// Only a source.LocalFileServer is allowed to serve local files. A
		// remote source (NexusMods, CurseForge, a compromised custom
		// API/manifest source, ...) returning file:// must never be trusted
		// to read arbitrary paths off disk into the cache.
		lfs, servesLocal := src.(source.LocalFileServer)
		if !servesLocal || !lfs.ServesLocalFiles() {
			return nil, fmt.Errorf("source %q returned a local file:// URL but is not a directory source", sourceID)
		}
		return s.ingestLocalToCache(ctx, gameCache, game, mod, file, localPath)
	}

	// Stage the download under the data dir, not $TMPDIR — see newStagingDir.
	tempDir, err := newStagingDir(s.stagingRoot(), "lmm-download-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := os.RemoveAll(tempDir); err == nil && cerr != nil {
			err = fmt.Errorf("removing temp directory: %w", cerr)
		}
	}()

	// Download the file. safeFileName sanitizes file.FileName - a
	// SOURCE-CONTROLLED value (NexusMods/CurseForge/Icarus/a custom source's
	// own declared filename) - before it is ever used as a path component:
	// an entry like "../../evil" would otherwise let a malicious or buggy
	// source escape tempDir/stagePath (#196 review). Used for every
	// path-construction use of the filename below; file.FileName itself is
	// left untouched for display purposes (the SHA256 mismatch message).
	safeFileName := filepath.Base(file.FileName)
	archivePath := filepath.Join(tempDir, safeFileName)
	var headers map[string]string
	if hp, ok := src.(source.DownloadHeaderProvider); ok {
		headers = hp.DownloadHeaders(url)
	}
	downloadResult, err := s.downloader.DownloadWithHeaders(ctx, url, archivePath, headers, sink)
	if err != nil {
		return nil, fmt.Errorf("downloading mod: %w", err)
	}

	if file.SHA256 != "" && !strings.EqualFold(downloadResult.SHA256, file.SHA256) {
		return nil, fmt.Errorf("verifying download of %s: sha256 mismatch: source declares %s, downloaded file is %s",
			file.FileName, file.SHA256, downloadResult.SHA256)
	}

	// Extract to cache location
	cachePath, stagePath, err := prepareStaging(ctx, gameCache, game, mod)
	if err != nil {
		return nil, err
	}
	defer func() {
		if rmErr := os.RemoveAll(stagePath); rmErr != nil {
			s.logger().Debug("removing staging directory failed", "path", stagePath, "err", rmErr)
		}
	}()

	// convertEligiblePak requires BOTH the game's own eligibility (deploy
	// mode + ConvertPaks) AND this specific src implementing MergeCompiler
	// (#221 I1 fix): the game flags alone don't decide, so a raw pak served
	// by a source that does NOT implement MergeCompiler
	// (a mixed-source game, or a misconfigured/non-icarus source) must fall
	// through to the legacy extract/copy path below - exactly as it did
	// before #221 - rather than hard-erroring the whole download. Unlike a
	// .exmodz file, which has no other valid interpretation and so still
	// hard-errors when src lacks MergeCompiler (see the !ok check below).
	mc, isMergeCompiler := src.(source.MergeCompiler)
	convertEligiblePak := isMergeCompiler && isConvertEligibleArtifact(game, mc, safeFileName)
	if game.DeployMode == domain.DeployCompile && (s.isNativeMergeFile(game, mc, safeFileName) || convertEligiblePak) {
		if !isMergeCompiler {
			return nil, fmt.Errorf("source %q: game %q requires DeployCompile but source does not implement MergeCompiler", src.ID(), game.ID)
		}
		if err := mc.ValidateSource(archivePath); err != nil {
			return nil, fmt.Errorf("validating %s: %w", safeFileName, err)
		}
		// Unlike copyFileStreaming (which mkdirs its destination itself),
		// the retained-source write below needs stagePath to exist first.
		if err := os.MkdirAll(stagePath, 0755); err != nil {
			return nil, fmt.Errorf("preparing staging: %w", err)
		}
		retainedPath := filepath.Join(stagePath, cache.RetainedSourceName(file.ID))
		if err := copyFileStreaming(archivePath, retainedPath); err != nil {
			return nil, fmt.Errorf("retaining %s: %w", safeFileName, err)
		}
		// exmodz: members nil (#197) - the merged pak is the only artifact.
		// pak (#221): ALSO keep a deployable copy as the sole member, so the
		// default state is raw-deploy (today's behavior); the first
		// successful merge flips the manifest to nil (syncMergedPak's
		// reconcile) and the merged pak takes over.
		var members []string
		if convertEligiblePak {
			deployablePath := filepath.Join(stagePath, safeFileName)
			if err := copyFileStreaming(archivePath, deployablePath); err != nil {
				return nil, fmt.Errorf("staging deployable pak %s: %w", safeFileName, err)
			}
			members = []string{safeFileName}
		}
		if err := commitStagedCacheWithMarker(cachePath, stagePath, file.ID, members); err != nil {
			return nil, err
		}
		return &DownloadModResult{FilesExtracted: len(members), Checksum: downloadResult.Checksum}, nil
	}

	if game.DeployMode == domain.DeployCopy || !s.extractor.CanExtract(archivePath) {
		// Copy mode: game wants files as-is (e.g., Hytale .zip mods)
		// Or not an archive - just copy to cache. copyFileStreaming mkdirs
		// stagePath itself (importer.go), so no MkdirAll needed here.
		destPath := filepath.Join(stagePath, safeFileName)
		if err := copyFileStreaming(archivePath, destPath); err != nil {
			return nil, fmt.Errorf("copying to cache: %w", err)
		}
		if err := commitStagedCacheWithMarker(cachePath, stagePath, file.ID, []string{safeFileName}); err != nil {
			return nil, err
		}
		return &DownloadModResult{
			FilesExtracted: 1,
			Checksum:       downloadResult.Checksum,
		}, nil
	}

	members, err := s.extractIntoStaging(ctx, archivePath, cachePath, stagePath)
	if err != nil {
		return nil, fmt.Errorf("extracting mod: %w", err)
	}
	if err := commitStagedCacheWithMarker(cachePath, stagePath, file.ID, members); err != nil {
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

// ingestLocalToCache copies a local mod (directory or archive) into the cache
// using the same staging/commit flow as downloaded mods. Local ingests have no
// HTTP download checksum, so DownloadModResult.Checksum is computed from the
// SOURCE instead (#164): the MD5 of the local file for file/archive ingests
// (the same fingerprint the download path records for a fetched archive), or
// a deterministic digest over the member set for directory ingests
// (digestDirectoryMembers). Both are pure functions of the source content, so
// a later re-ingest of an unchanged source reproduces the stored value and
// install/verify --fix converge instead of looping on NO CHECKSUM. A
// directory with no regular files yields an empty checksum - nothing to
// fingerprint - and callers must report that honestly.
func (s *Service) ingestLocalToCache(ctx context.Context, gameCache *cache.Cache, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, localPath string) (*DownloadModResult, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, fmt.Errorf("local mod path: %w", err)
	}

	// #166: a directory ingest REPLACES the cache entry instead of overlaying
	// it, so it stages UNSEEDED - seeding from the existing entry would let
	// members deleted from the source survive every re-ingest (verify --fix,
	// a re-download into a retained entry) and stay deployed indefinitely,
	// since copyDir below overlays without deleting. Safe because directory
	// sources declare exactly ONE synthetic file ID ("main" - see
	// custom.Directory.GetModFiles), so the seed can never carry sibling
	// files' members or markers worth preserving. File/archive ingests keep
	// the seed: their sources may serve multiple file IDs into one entry.
	var cachePath, stagePath string
	if info.IsDir() {
		cachePath, stagePath, err = prepareUnseededStaging(gameCache, game, mod)
	} else {
		cachePath, stagePath, err = prepareStaging(ctx, gameCache, game, mod)
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		if rmErr := os.RemoveAll(stagePath); rmErr != nil {
			s.logger().Debug("removing staging directory failed", "path", stagePath, "err", rmErr)
		}
	}()

	var members []string
	var checksum string
	switch {
	case info.IsDir():
		if err := copyDir(ctx, localPath, stagePath); err != nil {
			return nil, fmt.Errorf("copying mod directory: %w", err)
		}
		// Attribute the STAGED copies, not localPath's own listing: staging
		// holds exactly what the commit below publishes (it was cleared
		// above, so every file in it is this ingest's), and copyDir
		// DEREFERENCES in-root symlinks into regular files that a walk of
		// the source would skip - those files are cached, listed, and
		// deployed (cache.ListFiles), so the manifest and digest must cover
		// them too or the member-set views drift apart (#166).
		if members, err = relativeFileMembers(stagePath); err != nil {
			return nil, fmt.Errorf("listing staged mod directory: %w", err)
		}
		// Digest the STAGED copies, not localPath: staging holds the exact
		// bytes the commit below publishes, while the live source directory
		// can change mid-ingest - hashing it here could persist a checksum
		// for content that was never cached (review finding on #164).
		if checksum, err = digestDirectoryMembers(stagePath, members); err != nil {
			return nil, fmt.Errorf("fingerprinting mod directory: %w", err)
		}
	case game.DeployMode == domain.DeployCopy || !s.extractor.CanExtract(localPath):
		// file.FileName is the declared name for this mod file - use it so
		// the cached file matches what the source/caller advertised (#52
		// item 12); localPath's own basename is often just a temp file name
		// and falls back only when the caller left FileName unset.
		// copyFileStreaming mkdirs stagePath itself (importer.go), so no
		// MkdirAll needed here. filepath.Base sanitizes whichever name was
		// chosen: file.FileName is SOURCE-CONTROLLED (#196 review) and must
		// not be trusted as a path component verbatim (e.g. "../../evil").
		destName := file.FileName
		if destName == "" {
			destName = filepath.Base(localPath)
		}
		destName = filepath.Base(destName)
		if err := copyFileStreaming(localPath, filepath.Join(stagePath, destName)); err != nil {
			return nil, fmt.Errorf("copying to cache: %w", err)
		}
		members = []string{destName}
		if checksum, err = md5File(localPath); err != nil {
			return nil, fmt.Errorf("hashing local mod file: %w", err)
		}
	default:
		if members, err = s.extractIntoStaging(ctx, localPath, cachePath, stagePath); err != nil {
			return nil, fmt.Errorf("extracting mod: %w", err)
		}
		if checksum, err = md5File(localPath); err != nil {
			return nil, fmt.Errorf("hashing local mod archive: %w", err)
		}
	}

	if err := commitStagedCacheWithMarker(cachePath, stagePath, file.ID, members); err != nil {
		return nil, err
	}

	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil, err
	}
	return &DownloadModResult{FilesExtracted: len(files), Checksum: checksum}, nil
}

// md5File returns the hex MD5 of the file at path - the same fingerprint the
// HTTP download path records for a fetched archive (Downloader), so local
// file/archive ingests store values with identical semantics.
func md5File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// digestDirectoryMembers returns a deterministic hex MD5 fingerprint of a
// directory ingest's member set: each member's root-relative slash path plus
// the MD5 of its content under root, folded in sorted path order. root is
// the directory holding the bytes to fingerprint - for ingests, the STAGING
// copy, so the stored value describes exactly what gets committed to the
// cache even if the live source changes mid-ingest. Re-ingesting an
// unchanged source directory reproduces the value bit-for-bit (#164: verify
// --fix and reinstalls must converge on the stored value), while any member
// edit, rename, addition, or removal changes it - a real drift fingerprint.
// An empty member set returns "": there is nothing to fingerprint, and
// recording a meaningless constant would defeat the honesty guarantee.
func digestDirectoryMembers(root string, members []string) (string, error) {
	if len(members) == 0 {
		return "", nil
	}
	sorted := append([]string(nil), members...)
	sort.Strings(sorted)
	h := md5.New()
	for _, m := range sorted {
		fileSum, err := md5File(filepath.Join(root, m))
		if err != nil {
			return "", fmt.Errorf("hashing member %s: %w", m, err)
		}
		_, _ = fmt.Fprintf(h, "%s\x00%s\n", filepath.ToSlash(m), fileSum) // hash.Hash writes never fail
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// prepareStaging computes the cache/staging paths for (game, mod) and readies
// stagePath for a fresh write: any stale staging directory left behind by a
// previous interrupted run is cleared, then - only if a cache entry already
// exists for this exact (SourceID, ID, Version) - that existing cache is
// copied into stagePath, so the copy-mode/extract-mode branches that follow
// can add to (or replace part of) it in place before commitStagedCache
// atomically swaps stagePath in. Extracted from the verbatim-duplicated
// download/local-ingest staging setup (#52 item 11) - DownloadModToCache and
// ingestLocalToCache differ only in how they populate stagePath afterward,
// never in how they get there.
//
// Contract: on a nil error return, stagePath is clear to write into — it
// EXISTS only when an existing cache entry was staged into it; otherwise
// it does not exist yet and the first writer (copyDir/Extract/
// copyFileStreaming, all of which create their own destinations) brings it
// into being. Either way the CALLER owns its cleanup - callers MUST defer
// os.RemoveAll(stagePath) themselves immediately after (a defer registered
// inside this function would fire before the caller finishes using
// stagePath). On a non-nil error return, no staging debris remains - a
// mid-copy failure must leave stagePath exactly as absent as it was before
// this call, matching the pre-extraction behavior where the caller's own
// defer was armed BEFORE the copy step (see
// TestPrepareStagingCleansPartialStagingOnCopyFailure). A cancelled ctx
// aborts the seed copy per entry (copyDir) and takes that same
// error-with-no-debris path.
func prepareStaging(ctx context.Context, gameCache *cache.Cache, game *domain.Game, mod *domain.Mod) (cachePath, stagePath string, err error) {
	cachePath, stagePath, err = prepareUnseededStaging(gameCache, game, mod)
	if err != nil {
		return "", "", err
	}
	if gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		if err := copyDir(ctx, cachePath, stagePath); err != nil {
			// Best-effort: restore the "no debris" guarantee even though
			// copyDir itself already failed. A cleanup failure here isn't
			// worth surfacing over the original error - it's the same
			// filesystem that just failed on us, and the .staging path will
			// simply be cleared again (os.RemoveAll above) on the next
			// attempt for this exact (SourceID, ID, Version) either way.
			_ = os.RemoveAll(stagePath)
			return "", "", fmt.Errorf("staging existing cache: %w", err)
		}
	}
	return cachePath, stagePath, nil
}

// prepareUnseededStaging is prepareStaging WITHOUT the existing-entry seed:
// it computes the cache/staging paths and clears any stale staging directory,
// but leaves stagePath absent even when a cache entry already exists - the
// commit then REPLACES the entry outright instead of layering onto it.
// Directory ingests use this (#166): their single synthetic file ID owns the
// whole entry, so seeding could only resurrect members the source no longer
// has. Caller contract: as with prepareStaging, the CALLER owns stagePath's
// cleanup (defer os.RemoveAll immediately after) - but unlike prepareStaging,
// on a nil error stagePath NEVER exists yet; the first writer creates it.
func prepareUnseededStaging(gameCache *cache.Cache, game *domain.Game, mod *domain.Mod) (cachePath, stagePath string, err error) {
	cachePath = gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
	stagePath = cachePath + ".staging"
	if err := os.RemoveAll(stagePath); err != nil {
		return "", "", fmt.Errorf("clearing staging cache: %w", err)
	}
	return cachePath, stagePath, nil
}

// commitStagedCacheWithMarker is the single commit point for a per-file
// download/ingest: it stamps fileID's completion marker into the staging
// directory and then commits that directory into place, so the marker and the
// content it vouches for become visible in the SAME atomic swap - a marker
// can never appear for content that isn't there.
//
// The markers are what the cache-first convergence guards read back
// (cache.HasFileIDs, used by ApplyProfileSwitch here and doProfileApply in
// cmd/lmm/profile.go, both #96). They key off the source file ID rather than
// any on-disk name because the default DeployExtract mode stores an archive's
// EXTRACTED MEMBERS, whose names bear no relation to the DownloadableFile's
// FileName.
//
// members lists the stage-relative paths THIS file ID contributed (its
// extracted members, or the single stored file), recorded into the marker as
// the file's member manifest (#144 item 4: cache.MarkFileCompleteWithMembers)
// so a same-version file-only update can later undeploy a superseded file's
// members with positive provenance. Callers must attribute members to the
// file itself - never by diffing the staging dir, which prepareStaging seeds
// with OTHER files' members and which would misattribute overwritten shared
// members.
//
// prepareStaging seeds stagePath from the existing cache entry when one is
// present, and copyDir copies dotfiles, so markers written by a mod's earlier
// files survive into every later file's commit. (Directory ingests stage
// UNSEEDED instead - prepareUnseededStaging, #166 - their single synthetic
// file ID means there are no earlier files' markers to carry forward.)
func commitStagedCacheWithMarker(cachePath, stagePath, fileID string, members []string) error {
	if err := cache.MarkFileCompleteWithMembers(stagePath, fileID, members); err != nil {
		return err
	}
	// A fileID the marker layer cannot verify writes no marker (see
	// writeFileMarker), so the members this very commit contributes would
	// be unattributable - pruning now could delete them. Judge the prune
	// gate only when every contributor is marker-visible (#210).
	if cache.VerifiableFileID(fileID) {
		// #210: with full provenance recorded AND a retained source present, drop
		// anything no manifest claims (pre-#197 compiled paks carried forward by
		// prepareStaging's seeding). A legacy bare marker anywhere, or an entry
		// with no retained source, makes this a silent no-op - see
		// cache.PruneUnclaimed's doc comment for the full gate.
		if err := cache.PruneUnclaimed(stagePath); err != nil {
			return err
		}
	}
	return commitStagedCache(cachePath, stagePath)
}

// extractIntoStaging extracts archivePath into a PRISTINE sibling directory of
// the cache entry, records the exact member set the archive produced, and then
// merges those members into stagePath (new members overwrite same-named seeded
// ones, exactly as extracting straight into the seeded stagePath used to).
//
// The pristine intermediate exists for attribution (#144 item 4): stagePath is
// pre-seeded by prepareStaging with the existing cache entry, so extracting
// into it directly cannot tell this archive's members apart from earlier
// files' - and a before/after diff would silently misattribute a member the
// archive OVERWRITES (one also shipped by an earlier file), breaking the
// shared-member-survives rule. The extractor's own untrusted-archive guards
// (zip-slip, reserved-namespace rejection) run against the pristine dir, where
// "preexisting reserved entries" is correctly empty.
//
// Returned members are extractDir-relative paths of regular files only,
// matching cache.ListFiles semantics (directories and symlinks are never
// listed, deployed, or undeployed).
func (s *Service) extractIntoStaging(ctx context.Context, archivePath, cachePath, stagePath string) ([]string, error) {
	extractPath := cachePath + ".extract"
	if err := os.RemoveAll(extractPath); err != nil {
		return nil, fmt.Errorf("clearing extraction dir: %w", err)
	}
	defer os.RemoveAll(extractPath) //nolint:errcheck

	if err := s.extractor.Extract(ctx, archivePath, extractPath); err != nil {
		return nil, err
	}

	var members []string
	err := filepath.WalkDir(extractPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(extractPath, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(stagePath, rel)
		if d.IsDir() {
			// Preserve (possibly empty) directories, as direct extraction did.
			return os.MkdirAll(dest, 0755)
		}
		if err := os.Rename(path, dest); err != nil {
			return err
		}
		if d.Type().IsRegular() {
			members = append(members, rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("staging extracted members: %w", err)
	}
	return members, nil
}

// relativeFileMembers lists root-relative paths of the regular files under
// root - the member manifest for a directory ingest, matching cache.ListFiles
// semantics exactly: directories and symlinks are excluded, and so are lmm's
// own reserved (cache.ReservedPrefix) bookkeeping entries, which ListFiles
// never serves to deploy/undeploy and the manifest must therefore never
// attribute either.
func relativeFileMembers(root string) ([]string, error) {
	var members []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), cache.ReservedPrefix) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), cache.ReservedPrefix) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		members = append(members, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return members, nil
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

// isNativeMergeFile reports whether fileName is the game's compile source's
// NATIVE merge-source format (#256 - the seam-routed successor to the old
// static isExmodzFile ".exmodz" test). DeployCompile games can also serve
// plain, already-built raw paks (icarus.GetModFiles enumerates "pak"
// before "exmodz") - those must NOT be routed through ingest's
// validate+retain branch as a native archive, since MergeCompile expects a
// native diff, not a whole pak (#136 review, Task 13 fix round 1); a
// prebuilt pak gets its OWN eligibility check instead,
// isConvertEligibleArtifact (#221), a different Kind through the same
// validate+retain branch.
//
// mc is the file's own source's MergeCompiler view (nil when that source
// doesn't implement it). When mc is nil, the GAME's sole compile source is
// consulted instead, so the "native archive served by a non-compile-capable
// source" hard error in DownloadModToCache stays reachable on mixed-source
// games. With no compiler resolvable anywhere, nothing can define "native"
// and this returns false - such files take the legacy extract/copy path,
// preserving #221 I1's protected download fall-through for the non-compile
// source's paks and zips. Known residual, accepted deliberately: because
// the legacy Extractor content-sniffs zip magic, a REAL native archive
// downloaded in that doubly-broken state (a game whose SourceIDs map no
// compiler at all - icarus is always registered, so only a hand-edited map
// gets here - AND a foreign source serving native archives) is ingested as
// a plain archive without ValidateSource. The import path has no such
// residual: Importer.Import hard-errors on an unresolvable compiler, since
// it has no per-archive source contract forcing a fall-through.
func (s *Service) isNativeMergeFile(game *domain.Game, mc source.MergeCompiler, fileName string) bool {
	if mc == nil {
		gmc, err := s.mergeCompilerForGame(game)
		if err != nil {
			return false
		}
		mc = gmc
	}
	return mc.IsNativeMergeSource(fileName)
}

// isConvertEligibleArtifact reports whether fileName is a prebuilt raw
// artifact that should enter the merge-convert pipeline (#221): DeployCompile
// game with convert_paks enabled, and a file the game's compile-capable
// source says it can convert (mc.IsConvertibleArtifact - the format half of
// the pre-#256 isConvertEligiblePakFile, now behind the MergeCompiler seam;
// the policy half stays here). The per-MOD opt-out is consulted at
// merge-membership time (enabledMergeSources), not here - ingest state is
// identical either way (retained + raw-deployable), only participation
// differs. mc must be the source/resolver actually serving the file: a
// source that does not implement source.MergeCompiler falls through to the
// legacy extract/copy path instead (#221 I1 fix), which callers express by
// never reaching this check without one.
func isConvertEligibleArtifact(game *domain.Game, mc source.MergeCompiler, fileName string) bool {
	return game.DeployMode == domain.DeployCompile && game.ConvertPaks &&
		mc.IsConvertibleArtifact(fileName)
}

// GetGame retrieves a game by ID
func (s *Service) GetGame(gameID string) (*domain.Game, error) {
	game, ok := s.game(gameID)
	if !ok {
		return nil, domain.ErrGameNotFound
	}
	return game, nil
}

// ListGames returns all configured games
func (s *Service) ListGames() []*domain.Game {
	return s.gamesSnapshot()
}

// LoadGamesFromDisk re-reads games.yaml directly from disk, unlike ListGames
// which returns the in-memory snapshot NewService loaded at Open. 'lmm game
// detect' uses this for its existing-games lookup so that a games.yaml gone
// unreadable between NewService's load and the detect prompt surfaces as an
// error there, matching the always-fresh read the pre-Task-22 code
// performed (v2 Phase 2 Task 22 fix, #292).
func (s *Service) LoadGamesFromDisk() (map[string]*domain.Game, error) {
	return config.LoadGames(s.configDir)
}

// SaveGame persists game to games.yaml and publishes it to this Service's
// in-memory game set atomically. It replaces an existing entry with the
// same ID. Readers (GetGame, ListGames, SourcesForGame, …) may run
// concurrently with it.
func (s *Service) SaveGame(ctx context.Context, game *domain.Game) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.saveGame(ctx, game)
}

func (s *Service) saveGame(ctx context.Context, game *domain.Game) error {
	s.gamesMu.Lock()
	defer s.gamesMu.Unlock()
	if err := config.SaveGame(s.configDir, game); err != nil {
		return err
	}
	s.games[game.ID] = game
	return nil
}

// game returns the in-memory game for id under the read lock.
func (s *Service) game(id string) (*domain.Game, bool) {
	s.gamesMu.RLock()
	defer s.gamesMu.RUnlock()
	g, ok := s.games[id]
	return g, ok
}

// gamesSnapshot returns the games in a fresh slice under the read lock,
// ordered by game ID (Ruling 4, #299) rather than Go's own map iteration
// order - ListGames' callers (lmm status, lmm game list) need a stable order.
func (s *Service) gamesSnapshot() []*domain.Game {
	s.gamesMu.RLock()
	defer s.gamesMu.RUnlock()
	out := make([]*domain.Game, 0, len(s.games))
	for _, g := range s.games {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GetInstalledMods returns all installed mods for a game/profile (DB order: installed_at).
func (s *Service) GetInstalledMods(ctx context.Context, gameID, profileName string) ([]domain.InstalledMod, error) {
	return s.db.GetInstalledMods(ctx, gameID, profileName)
}

// GetInstalledModsInProfileOrder returns installed mods in profile load order (first = lowest priority).
// Mods not present in the profile are omitted. Use this for deploy/switch so deployment order matches load order.
func (s *Service) GetInstalledModsInProfileOrder(ctx context.Context, gameID, profileName string) ([]domain.InstalledMod, error) {
	profile, err := config.LoadProfile(s.configDir, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile: %w", err)
	}
	all, err := s.db.GetInstalledMods(ctx, gameID, profileName)
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

// getLinker returns a linker for the given method. Unexported with the
// archive-import lift (#291), which removed cmd's last caller.
func (s *Service) getLinker(method domain.LinkMethod) linker.Linker {
	return linker.New(method)
}

// getGameLinkMethod returns the effective link method for a game.
// Uses the game's explicit setting if configured, otherwise falls back to global default.
func (s *Service) getGameLinkMethod(game *domain.Game) domain.LinkMethod {
	if game.LinkMethodExplicit {
		return game.LinkMethod
	}
	return s.config.DefaultLinkMethod
}

// GetEffectiveLinkMethod resolves the link method for operations that deploy
// into (or undeploy from) profileName: profile-explicit > game-explicit >
// global default (#81). A missing or unreadable profile degrades to the
// game-level resolution rather than erroring - callers resolving a method are
// deploying, not validating, and the profile's absence is diagnosed elsewhere.
// An invalid link_method value in an otherwise-loadable profile is different:
// #172 made that a fail-loud load-time error at every explicit LoadProfile
// call site, and degrading here would deploy with the wrong method with
// nothing telling the caller anything was wrong - so that one LoadProfile
// failure mode (errors.Is domain.ErrInvalidLinkMethod) is returned as an
// error instead of folding into the silent-degrade path (#189). The CLI
// --method override sits above all of these and is applied by callers
// (see DeployOptions.LinkMethod).
func (s *Service) GetEffectiveLinkMethod(ctx context.Context, game *domain.Game, profileName string) (domain.LinkMethod, error) {
	profile, err := config.LoadProfile(s.configDir, game.ID, profileName)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidLinkMethod) {
			return 0, fmt.Errorf("resolving effective link method: %w", err)
		}
		return s.getGameLinkMethod(game), nil
	}
	if profile.LinkMethodExplicit {
		return profile.LinkMethod, nil
	}
	return s.getGameLinkMethod(game), nil
}

// GetInstaller returns an Installer configured for the given game.
//
// It stays EXPORTED with no production cmd caller (every flow builds its own
// installer internally): cmd/lmm's tests seed deployed-file fixtures by
// installing through it, and core's export_test.go shims are invisible to
// package main. Same reason DownloadMod and SaveFileChecksum stay exported.
func (s *Service) GetInstaller(game *domain.Game) *Installer {
	return s.newInstallerWithLinker(game, s.getLinker(s.getGameLinkMethod(game)))
}

// getInstallerForProfile returns an Installer whose linker honors
// profileName's effective link method (GetEffectiveLinkMethod) - the
// profile-aware companion to GetInstaller. Propagates GetEffectiveLinkMethod's
// new error case (#189) rather than swallowing it. Unexported with
// doProfileApply's lift (#290): an Installer is a core primitive, and that
// flow held cmd's last reference to one.
func (s *Service) getInstallerForProfile(ctx context.Context, game *domain.Game, profileName string) (*Installer, error) {
	method, err := s.GetEffectiveLinkMethod(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	return s.newInstallerWithLinker(game, s.getLinker(method)), nil
}

// newInstallerWithLinker returns an Installer for the given game using a
// caller-supplied linker — used where a flow overrides the game's default
// link method (e.g. `lmm deploy --method`). Unexported with the archive-
// import lift (#291), which removed cmd's last caller; an Installer is a
// core primitive no frontend touches (see getInstallerForProfile).
func (s *Service) newInstallerWithLinker(game *domain.Game, lnk linker.Linker) *Installer {
	installer := NewInstaller(s.GetGameCache(game), lnk, s.db)
	installer.SetLogger(s.log)
	return installer
}

// NewProfileManager returns a ProfileManager wired to this service's storage,
// so callers do not need direct access to the database or registry.
func (s *Service) NewProfileManager() *ProfileManager {
	return NewProfileManager(s.configDir, s.db)
}

// NewUpdater returns an Updater wired to this service's source registry.
func (s *Service) NewUpdater() *Updater {
	return NewUpdater(s.registry)
}

// GetGameCachePath returns the effective cache path for a game.
// Uses the game's cache_path if configured, otherwise falls back to global cache.
func (s *Service) GetGameCachePath(game *domain.Game) string {
	if game.CachePath != "" {
		return game.CachePath
	}
	return s.cacheDir
}

// GlobalCacheDir returns the top-level cache directory this Service was
// constructed with (ServiceConfig.CacheDir), regardless of any per-game
// CachePath override. A per-game CachePath augments the global cache rather
// than replacing it as a valid location for lmm-owned content (#168/#212
// convergence sweep Finding 1: content can legitimately live under either
// root), so callers that need to recognize BOTH cache roots for a game -
// not just the single one GetGameCachePath resolves to - use this alongside
// game.CachePath.
func (s *Service) GlobalCacheDir() string {
	return s.cacheDir
}

// GetGameCache returns a cache manager for the specified game.
// Uses the game's cache_path if configured (game-scoped: paths omit gameID), otherwise the global cache.
//
// Exported for two reasons: dozens of core files call it internally (this
// is the package's own cache accessor), and `mod files`'s last cmd caller
// (v2 Phase 3 Task 10, #303) moved into ModFiles, leaving cmd/lmm test
// fixtures across nearly every command area as the only EXTERNAL callers -
// they seed cache files directly rather than running a full install. Kept
// exported by the same SaveFileChecksum precedent (Ruling 10) rather than
// rewriting that fixture surface.
func (s *Service) GetGameCache(game *domain.Game) *cache.Cache {
	if game.CachePath != "" {
		gameCache := cache.NewGameScoped(game.CachePath)
		gameCache.SetLogger(s.log)
		return gameCache
	}
	return s.cache
}

// ConfigDir returns the configuration directory
func (s *Service) ConfigDir() string {
	return s.configDir
}

// SaveSourceToken saves an API token for a source
func (s *Service) SaveSourceToken(ctx context.Context, sourceID, apiKey string) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.saveSourceToken(ctx, sourceID, apiKey)
}

func (s *Service) saveSourceToken(ctx context.Context, sourceID, apiKey string) error {
	return s.db.SaveToken(ctx, sourceID, apiKey)
}

// GetSourceToken retrieves an API token for a source
func (s *Service) GetSourceToken(ctx context.Context, sourceID string) (*db.StoredToken, error) {
	return s.db.GetToken(ctx, sourceID)
}

// DeleteSourceToken removes an API token for a source
func (s *Service) DeleteSourceToken(ctx context.Context, sourceID string) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.deleteSourceToken(ctx, sourceID)
}

func (s *Service) deleteSourceToken(ctx context.Context, sourceID string) error {
	return s.db.DeleteToken(ctx, sourceID)
}

// ListSourceTokens returns every stored API token, including ones whose
// source is no longer registered (e.g. the custom-source definition file
// was removed) — used by `lmm auth status` to surface orphaned credentials.
func (s *Service) ListSourceTokens(ctx context.Context) ([]db.StoredToken, error) {
	return s.db.ListTokens(ctx)
}

// IsSourceAuthenticated checks if a source has a stored API token
func (s *Service) IsSourceAuthenticated(ctx context.Context, sourceID string) bool {
	has, err := s.db.HasToken(ctx, sourceID)
	if err != nil {
		s.logger().Warn("checking source authentication failed", "source_id", sourceID, "err", err)
		return false
	}
	return has
}

// updateModVersion updates the version of an installed mod, preserving the
// previous version for rollback. Unexported (phase-2-close review Important
// #3): zero production callers - the only exported form's beginOp gate ever
// added is now provided by UpdateModVersionForTest for core's own tests.
func (s *Service) updateModVersion(ctx context.Context, sourceID, modID, gameID, profileName, newVersion string) error {
	return s.db.UpdateModVersion(ctx, sourceID, modID, gameID, profileName, newVersion)
}

// applyModUpdate updates version and file IDs atomically, preserving
// rollback state. Unexported (phase-2-close review Important #3): zero
// production callers - see updateModVersion's doc comment.
func (s *Service) applyModUpdate(ctx context.Context, sourceID, modID, gameID, profileName, newVersion string, fileIDs []string) error {
	return s.db.ApplyModUpdate(ctx, sourceID, modID, gameID, profileName, newVersion, fileIDs)
}

// rollbackModVersion reverts a mod to its previous version. Unexported
// (v2 Phase 2 Unit I Task 10, #289): the exported RollbackModVersion wrapper
// had zero production callers - only ApplyRollback/ApplyUpdate call this
// directly, already inside their own beginOp - and its DB-level behavior
// (including the "no previous version" failure) is pinned independently by
// internal/storage/db's own TestSwapModVersions/TestSwapModVersions_NoPreviousVersion.
func (s *Service) rollbackModVersion(ctx context.Context, sourceID, modID, gameID, profileName string) error {
	return s.db.SwapModVersions(ctx, sourceID, modID, gameID, profileName)
}

// SetModLinkMethod sets the deployment method for an installed mod
func (s *Service) SetModLinkMethod(ctx context.Context, sourceID, modID, gameID, profileName string, linkMethod domain.LinkMethod) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.setModLinkMethod(ctx, sourceID, modID, gameID, profileName, linkMethod)
}

func (s *Service) setModLinkMethod(ctx context.Context, sourceID, modID, gameID, profileName string, linkMethod domain.LinkMethod) error {
	return s.db.SetModLinkMethod(ctx, sourceID, modID, gameID, profileName, linkMethod)
}

func (s *Service) setModFileIDs(ctx context.Context, sourceID, modID, gameID, profileName string, fileIDs []string) error {
	return s.db.SetModFileIDs(ctx, sourceID, modID, gameID, profileName, fileIDs)
}

// setModEnabled toggles the enabled flag for an installed mod. Unexported
// with doProfileApply's lift (#290): the profile-apply loops were the last
// cmd callers, and every flow that flips this flag now lives in core.
func (s *Service) setModEnabled(ctx context.Context, sourceID, modID, gameID, profileName string, enabled bool) error {
	return s.db.SetModEnabled(ctx, sourceID, modID, gameID, profileName, enabled)
}

// SetModDeployed records whether a mod's files are currently deployed.
func (s *Service) SetModDeployed(ctx context.Context, sourceID, modID, gameID, profileName string, deployed bool) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.setModDeployed(ctx, sourceID, modID, gameID, profileName, deployed)
}

func (s *Service) setModDeployed(ctx context.Context, sourceID, modID, gameID, profileName string, deployed bool) error {
	return s.db.SetModDeployed(ctx, sourceID, modID, gameID, profileName, deployed)
}

// SaveInstalledMod persists an installed-mod record (insert or update).
//
// Exported only as a documented test-seed API (the SaveFileChecksum
// precedent, Ruling 10): `mod edit`/`mod files` lost their own cmd callers
// in v2 Phase 3 Task 10 (#303, replaced by ApplyRelinkMod/ModFiles), but
// dozens of cmd/lmm test fixtures across nearly every command area (install,
// deploy, uninstall, profile, update, verify...) call this directly to seed
// an installed-mod DB row without running a full install - re-seeding all of
// them through ApplyInstall was judged out of this task's scope (see the
// task report). No production caller remains outside this package.
func (s *Service) SaveInstalledMod(ctx context.Context, mod *domain.InstalledMod) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.saveInstalledMod(ctx, mod)
}

func (s *Service) saveInstalledMod(ctx context.Context, mod *domain.InstalledMod) error {
	return s.db.SaveInstalledMod(ctx, mod)
}

// setModVersion corrects an installed mod's recorded version without
// shifting PreviousVersion (unlike a real version update) or re-keying its
// file-ID rows (unlike SaveInstalledMod's full-row upsert, whose
// replaceModFileIDsTx would silently wipe stored checksums even when the
// file IDs themselves haven't changed - see internal/storage/db/mods.go).
// For repairing a version string known to be WRONG (verify --fix's
// version-record repair, issue #94), where the file IDs and their
// checksums are already correct.
func (s *Service) setModVersion(ctx context.Context, sourceID, modID, gameID, profileName, version string) error {
	return s.db.SetModVersion(ctx, sourceID, modID, gameID, profileName, version)
}

// DeleteInstalledMod removes the installed-mod record from the active
// profile.
//
// Exported only as a documented test-seed API (the SaveFileChecksum
// precedent, Ruling 10) - see SaveInstalledMod's doc comment. `mod edit`'s
// re-link (ApplyRelinkMod) is its only production caller and reaches it
// through the unexported deleteInstalledMod, already inside its own
// beginOp; verify_convert_test.go seeds through this exported form to
// simulate an uninstall without the full flow. No other production caller
// remains outside this package.
func (s *Service) DeleteInstalledMod(ctx context.Context, sourceID, modID, gameID, profileName string) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.deleteInstalledMod(ctx, sourceID, modID, gameID, profileName)
}

func (s *Service) deleteInstalledMod(ctx context.Context, sourceID, modID, gameID, profileName string) error {
	return s.db.DeleteInstalledMod(ctx, sourceID, modID, gameID, profileName)
}

// GetDeployedFilesForMod returns the relative paths the given mod has deployed
// in the named profile.
func (s *Service) GetDeployedFilesForMod(ctx context.Context, gameID, profileName, sourceID, modID string) ([]string, error) {
	return s.db.GetDeployedFilesForMod(ctx, gameID, profileName, sourceID, modID)
}

// getLastDeployTime returns the timestamp of the most recent deploy for the
// given game/profile (#106a's dashboard "Last deploy" row), or nil if it has
// never been deployed - see db.DB.GetLastDeployTime's own doc comment for
// why nil is not an error. Unexported (final review, Important #3 / #301):
// GameStatus is the only caller left, cmd's --json switched from its own
// hand-built read to service.GameStatus, and core.VerifyReport/Status don't
// need a last-deploy timestamp at all.
func (s *Service) getLastDeployTime(ctx context.Context, gameID, profileName string) (*time.Time, error) {
	return s.db.GetLastDeployTime(ctx, gameID, profileName)
}

// GetFileOwner reports which mod currently owns a deployed file. The bool is
// false when no record exists; err is non-nil only on storage errors.
func (s *Service) GetFileOwner(ctx context.Context, gameID, profileName, relativePath string) (sourceID, modID string, found bool, err error) {
	owner, err := s.db.GetFileOwner(ctx, gameID, profileName, relativePath)
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
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	FileID   string `json:"file_id"`
	Checksum string `json:"checksum,omitempty"`
}

// GetFilesWithChecksums returns every tracked file in the profile with its
// recorded checksum (empty when none has been computed yet).
func (s *Service) GetFilesWithChecksums(ctx context.Context, gameID, profileName string) ([]DeployedFile, error) {
	rows, err := s.db.GetFilesWithChecksums(ctx, gameID, profileName)
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
func (s *Service) SaveFileChecksum(ctx context.Context, sourceID, modID, gameID, profileName, fileID, checksum string) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.saveFileChecksum(ctx, sourceID, modID, gameID, profileName, fileID, checksum)
}

func (s *Service) saveFileChecksum(ctx context.Context, sourceID, modID, gameID, profileName, fileID, checksum string) error {
	return s.db.SaveFileChecksum(ctx, sourceID, modID, gameID, profileName, fileID, checksum)
}

// GetInstalledMod retrieves a single installed mod
func (s *Service) GetInstalledMod(ctx context.Context, sourceID, modID, gameID, profileName string) (*domain.InstalledMod, error) {
	return s.db.GetInstalledMod(ctx, sourceID, modID, gameID, profileName)
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
