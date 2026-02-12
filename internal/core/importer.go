package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/google/uuid"
)

// ImportOptions configures the import operation
type ImportOptions struct {
	SourceID    string // Explicit source (empty = auto-detect or "local")
	ModID       string // Explicit mod ID (empty = auto-detect or generate)
	ProfileName string // Target profile
}

// ImportResult contains the outcome of importing a local mod
type ImportResult struct {
	Mod            *domain.Mod
	FilesExtracted int
	LinkedSource   string // "nexusmods", "local", etc.
	AutoDetected   bool   // true if source/ID was parsed from filename
}

// Importer handles importing mods from local archive files
type Importer struct {
	cache     *cache.Cache
	extractor *Extractor
}

// NewImporter creates a new Importer
func NewImporter(cache *cache.Cache) *Importer {
	return &Importer{
		cache:     cache,
		extractor: NewExtractor(),
	}
}

// Import imports a mod from a local archive file
func (i *Importer) Import(ctx context.Context, archivePath string, game *domain.Game, opts ImportOptions) (result *ImportResult, err error) {
	// Validate archive exists
	if _, err := os.Stat(archivePath); err != nil {
		return nil, fmt.Errorf("archive not found: %w", err)
	}

	filename := filepath.Base(archivePath)

	// Try to parse NexusMods filename pattern
	var sourceID, modID, version string
	var autoDetected bool

	if opts.SourceID != "" && opts.ModID != "" {
		// Explicit linking provided
		sourceID = opts.SourceID
		modID = opts.ModID
		version = extractVersionFromFilename(strings.TrimSuffix(filename, filepath.Ext(filename)))
		if version == "" {
			version = "unknown"
		}
	} else if parsed := ParseNexusModsFilename(filename); parsed != nil {
		// Auto-detected from filename
		sourceID = domain.SourceLocal // Still local until verified via API
		modID = parsed.ModID
		version = parsed.Version
		autoDetected = true
	} else {
		// No pattern - pure local mod
		sourceID = domain.SourceLocal
		modID = uuid.New().String()
		version = extractVersionFromFilename(strings.TrimSuffix(filename, filepath.Ext(filename)))
		if version == "" {
			version = "unknown"
		}
	}

	var modName string
	var fileCount int

	// Handle based on game's deploy mode
	if game.DeployMode == domain.DeployCopy {
		// Copy mode: just copy the file as-is to cache (don't extract)
		modName = strings.TrimSuffix(filename, filepath.Ext(filename))
		if version != "" && version != "unknown" {
			if idx := strings.LastIndex(modName, version); idx > 0 {
				modName = strings.TrimRight(modName[:idx], "-_ ")
			}
		}

		cachePath := i.cache.ModPath(game.ID, sourceID, modID, version)

		// Remove existing cache if present (re-import case)
		// Ignore errors - if removal fails, MkdirAll/copy will error anyway
		os.RemoveAll(cachePath)
		if err := os.MkdirAll(cachePath, 0755); err != nil {
			return nil, fmt.Errorf("creating cache directory: %w", err)
		}

		// Copy the file to cache using streaming to avoid memory spikes
		destPath := filepath.Join(cachePath, filename)
		if err := copyFileStreaming(archivePath, destPath); err != nil {
			return nil, fmt.Errorf("copying to cache: %w", err)
		}
		fileCount = 1
	} else {
		// Extract mode: validate and extract the archive
		if !i.extractor.CanExtract(archivePath) {
			return nil, fmt.Errorf("unsupported archive format: %s", filepath.Ext(archivePath))
		}

		// Extract to temp directory first
		tempDir, err := os.MkdirTemp("", "lmm-import-*")
		if err != nil {
			return nil, fmt.Errorf("creating temp directory: %w", err)
		}
		defer func() {
			if cerr := os.RemoveAll(tempDir); err == nil && cerr != nil {
				err = fmt.Errorf("removing temp directory: %w", cerr)
			}
		}()

		extractedPath := filepath.Join(tempDir, "extracted")
		if err := i.extractor.Extract(archivePath, extractedPath); err != nil {
			return nil, fmt.Errorf("extracting archive: %w", err)
		}

		// Detect mod name from extracted content
		modName = DetectModName(extractedPath, filename)

		// Move extracted files to cache
		cachePath := i.cache.ModPath(game.ID, sourceID, modID, version)
		if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
			return nil, fmt.Errorf("creating cache directory: %w", err)
		}

		// Remove existing cache if present (re-import case)
		if err := os.RemoveAll(cachePath); err != nil {
			return nil, fmt.Errorf("removing existing cache for re-import: %w", err)
		}

		if err := os.Rename(extractedPath, cachePath); err != nil {
			// If rename fails (cross-device), fall back to copy
			if err := copyDir(extractedPath, cachePath); err != nil {
				return nil, fmt.Errorf("moving to cache: %w", err)
			}
		}

		// Count extracted files
		files, err := i.cache.ListFiles(game.ID, sourceID, modID, version)
		if err != nil {
			return nil, fmt.Errorf("listing cached files: %w", err)
		}
		fileCount = len(files)
	}

	mod := &domain.Mod{
		ID:       modID,
		SourceID: sourceID,
		Name:     modName,
		Version:  version,
		GameID:   game.ID,
	}

	return &ImportResult{
		Mod:            mod,
		FilesExtracted: fileCount,
		LinkedSource:   sourceID,
		AutoDetected:   autoDetected,
	}, nil
}

// copyDir recursively copies a directory using streaming I/O to avoid loading
// entire files into memory (important for large mod archives).
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		return copyFileStreaming(path, dstPath)
	})
}

// ScanResult contains the outcome of scanning a single mod file
type ScanResult struct {
	FilePath       string      // Original path in mod_path
	FileName       string      // Base filename
	Mod            *domain.Mod // Detected/created mod info
	MatchedSource  string      // "curseforge", "nexusmods", or "local"
	AlreadyTracked bool        // True if already in lmm database
	Error          error       // Any error during processing
}

// ScanOptions configures the scan operation
type ScanOptions struct {
	ProfileName string
	DryRun      bool // If true, don't actually import, just report what would be done
}

// ScanModPath scans the game's mod_path for untracked mods
func (i *Importer) ScanModPath(ctx context.Context, game *domain.Game, installedMods []domain.InstalledMod, opts ScanOptions) ([]ScanResult, error) {
	if game.ModPath == "" {
		return nil, fmt.Errorf("game has no mod_path configured")
	}

	// Expand mod path
	modPath := game.ModPath
	if strings.HasPrefix(modPath, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			modPath = filepath.Join(home, modPath[2:])
		}
	}

	// Check if mod_path exists
	if _, err := os.Stat(modPath); err != nil {
		return nil, fmt.Errorf("mod_path does not exist: %s", modPath)
	}

	var results []ScanResult

	// Scan based on deploy mode
	if game.DeployMode == domain.DeployCopy {
		// For copy mode, scan for files (.jar, .zip, etc.)
		entries, err := os.ReadDir(modPath)
		if err != nil {
			return nil, fmt.Errorf("reading mod_path: %w", err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue // Skip directories in copy mode
			}

			filename := entry.Name()
			ext := strings.ToLower(filepath.Ext(filename))

			// Only process mod file extensions
			if ext != ".jar" && ext != ".zip" {
				continue
			}

			filePath := filepath.Join(modPath, filename)

			// Check if it's a symlink (already deployed by lmm)
			info, err := os.Lstat(filePath)
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				// It's a symlink - already tracked
				results = append(results, ScanResult{
					FilePath:       filePath,
					FileName:       filename,
					AlreadyTracked: true,
				})
				continue
			}

			// Check if already tracked by comparing to installed mods
			alreadyTracked := i.isFileTracked(filename, installedMods)

			result := ScanResult{
				FilePath:       filePath,
				FileName:       filename,
				AlreadyTracked: alreadyTracked,
			}

			if !alreadyTracked {
				// Try to detect mod info from filename
				result.Mod = i.detectModFromFilename(filename, game.ID)
				result.MatchedSource = domain.SourceLocal // Default, can be upgraded later
			}

			results = append(results, result)
		}
	} else {
		// For extract mode, scan for directories
		entries, err := os.ReadDir(modPath)
		if err != nil {
			return nil, fmt.Errorf("reading mod_path: %w", err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue // Skip files in extract mode
			}

			dirName := entry.Name()
			dirPath := filepath.Join(modPath, dirName)

			// Check if already tracked
			alreadyTracked := i.isFileTracked(dirName, installedMods)

			result := ScanResult{
				FilePath:       dirPath,
				FileName:       dirName,
				AlreadyTracked: alreadyTracked,
			}

			if !alreadyTracked {
				// Create mod info from directory name
				result.Mod = i.detectModFromFilename(dirName, game.ID)
				result.MatchedSource = domain.SourceLocal
			}

			results = append(results, result)
		}
	}

	return results, nil
}

// isFileTracked checks if a file is already tracked by comparing to installed mods
func (i *Importer) isFileTracked(filename string, installedMods []domain.InstalledMod) bool {
	// Strip extension for comparison
	baseName := strings.TrimSuffix(filename, filepath.Ext(filename))
	baseNameLower := strings.ToLower(baseName)
	filenameLower := strings.ToLower(filename)

	for _, mod := range installedMods {
		modNameLower := strings.ToLower(mod.Name)

		// Check various ways the mod might be identified (case-insensitive)
		if modNameLower == baseNameLower || modNameLower == filenameLower {
			return true
		}
		// Check if the mod's cached filename matches
		if strings.Contains(filenameLower, strings.ToLower(mod.ID)) {
			return true
		}
	}
	return false
}

// findDuplicateMod checks if a mod with similar name already exists (for duplicate prevention)
func (i *Importer) FindDuplicateMod(modName string, installedMods []domain.InstalledMod) *domain.InstalledMod {
	modNameLower := strings.ToLower(modName)
	// Normalize: remove common suffixes like version numbers, underscores, dashes
	normalized := normalizeModName(modNameLower)

	for idx := range installedMods {
		existingNorm := normalizeModName(strings.ToLower(installedMods[idx].Name))
		if existingNorm == normalized {
			return &installedMods[idx]
		}
	}
	return nil
}

// normalizeModName removes version suffixes and normalizes separators for comparison
func normalizeModName(name string) string {
	// Remove common version patterns like -1.0.5, _v2, etc.
	re := regexp.MustCompile(`[-_]?v?\d+(\.\d+)*$`)
	name = re.ReplaceAllString(name, "")
	// Normalize separators
	name = strings.ReplaceAll(name, "-", "")
	name = strings.ReplaceAll(name, "_", "")
	name = strings.ReplaceAll(name, " ", "")
	return name
}

// detectModFromFilename attempts to parse mod info from a filename
func (i *Importer) detectModFromFilename(filename string, gameID string) *domain.Mod {
	// Strip extension
	baseName := strings.TrimSuffix(filename, filepath.Ext(filename))

	// Try to extract version
	version := extractVersionFromFilename(baseName)

	// Extract mod name (everything before the version)
	name := baseName
	if version != "" {
		// Find where the version starts and take everything before
		idx := strings.LastIndex(baseName, version)
		if idx > 0 {
			name = strings.TrimRight(baseName[:idx], "-_ ")
		}
	}

	return &domain.Mod{
		ID:       uuid.New().String(),
		SourceID: domain.SourceLocal,
		Name:     name,
		Version:  version,
		GameID:   gameID,
	}
}

// extractVersionFromFilename extracts version from a filename using regex
// versionRegexImporter matches semantic version patterns like 1.2.3, v1.2.3, etc.
var versionRegexImporter = regexp.MustCompile(`[vV]?(\d+\.\d+(?:\.\d+)?(?:\.\d+)?(?:[-+][a-zA-Z][\w.]*)?)`)

func extractVersionFromFilename(filename string) string {
	// Match semantic version patterns
	matches := versionRegexImporter.FindAllStringSubmatch(filename, -1)
	if len(matches) > 0 {
		return matches[len(matches)-1][1]
	}
	return ""
}

// copyFileStreaming copies a file using streaming to avoid loading it all into memory
func copyFileStreaming(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return fmt.Errorf("creating destination: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copying: %w", err)
	}

	return dstFile.Sync()
}
