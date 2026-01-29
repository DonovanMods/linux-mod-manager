package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
func (i *Importer) Import(ctx context.Context, archivePath string, game *domain.Game, opts ImportOptions) (*ImportResult, error) {
	// Validate archive exists
	if _, err := os.Stat(archivePath); err != nil {
		return nil, fmt.Errorf("archive not found: %w", err)
	}

	// Validate format is supported
	if !i.extractor.CanExtract(archivePath) {
		return nil, fmt.Errorf("unsupported archive format: %s", filepath.Ext(archivePath))
	}

	filename := filepath.Base(archivePath)

	// Try to parse NexusMods filename pattern
	var sourceID, modID, version string
	var autoDetected bool

	if opts.SourceID != "" && opts.ModID != "" {
		// Explicit linking provided
		sourceID = opts.SourceID
		modID = opts.ModID
		version = "unknown"
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
		version = "unknown"
	}

	// Extract to temp directory first
	tempDir, err := os.MkdirTemp("", "lmm-import-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	extractedPath := filepath.Join(tempDir, "extracted")
	if err := i.extractor.Extract(archivePath, extractedPath); err != nil {
		return nil, fmt.Errorf("extracting archive: %w", err)
	}

	// Detect mod name from extracted content
	modName := DetectModName(extractedPath, filename)

	// Move extracted files to cache
	cachePath := i.cache.ModPath(game.ID, sourceID, modID, version)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0755); err != nil {
		return nil, fmt.Errorf("creating cache directory: %w", err)
	}

	// Remove existing cache if present (re-import case)
	os.RemoveAll(cachePath)

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

	mod := &domain.Mod{
		ID:       modID,
		SourceID: sourceID,
		Name:     modName,
		Version:  version,
		GameID:   game.ID,
	}

	return &ImportResult{
		Mod:            mod,
		FilesExtracted: len(files),
		LinkedSource:   sourceID,
		AutoDetected:   autoDetected,
	}, nil
}

// copyDir recursively copies a directory
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

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dstPath, data, info.Mode())
	})
}
