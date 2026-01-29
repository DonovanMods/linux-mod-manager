# P3 Local Mod Import Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Allow users to install mods from local archive files with smart NexusMods filename detection for update tracking.

**Architecture:** New `import` command extracts local archives to cache, auto-detects NexusMods mod IDs from filenames, and reuses existing installer for deployment. Local mods without NexusMods links use `source_id = "local"` and skip update checks.

**Tech Stack:** Go, Cobra CLI, regex for filename parsing, existing extractor/installer infrastructure

---

## Task 1: Add SourceLocal Constant

**Files:**

- Modify: `internal/domain/mod.go`
- Test: `internal/domain/mod_test.go`

**Step 1: Write the test**

Create `internal/domain/mod_test.go` (if it doesn't exist) or add to it:

```go
package domain

import "testing"

func TestSourceLocal_IsExpectedValue(t *testing.T) {
	if SourceLocal != "local" {
		t.Errorf("SourceLocal = %q, want %q", SourceLocal, "local")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/... -v -run TestSourceLocal`
Expected: FAIL with "undefined: SourceLocal"

**Step 3: Add the constant**

Add to `internal/domain/mod.go` near the top with other constants:

```go
// SourceLocal is the source ID for mods imported from local files
const SourceLocal = "local"
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/domain/... -v -run TestSourceLocal`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/domain/mod.go internal/domain/mod_test.go
git commit -m "feat(domain): add SourceLocal constant for local mod imports"
```

---

## Task 2: Create Filename Parser with Tests

**Files:**

- Create: `internal/core/filename_parser.go`
- Create: `internal/core/filename_parser_test.go`

**Step 1: Write the failing tests**

Create `internal/core/filename_parser_test.go`:

```go
package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseNexusModsFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     *ParsedFilename
	}{
		{
			name:     "standard nexusmods pattern",
			filename: "SkyUI-12604-5-2SE.zip",
			want:     &ParsedFilename{ModID: "12604", Version: "5.2SE", BaseName: "SkyUI"},
		},
		{
			name:     "pattern with underscores in name",
			filename: "SkyUI_5_2_SE-12604-5-2SE.zip",
			want:     &ParsedFilename{ModID: "12604", Version: "5.2SE", BaseName: "SkyUI_5_2_SE"},
		},
		{
			name:     "pattern with timestamp suffix",
			filename: "SKSE64-30379-2-2-6-1703618069.7z",
			want:     &ParsedFilename{ModID: "30379", Version: "2.2.6", BaseName: "SKSE64"},
		},
		{
			name:     "pattern with spaces replaced by dashes",
			filename: "Unofficial-Skyrim-Patch-266-4-3-0a.zip",
			want:     &ParsedFilename{ModID: "266", Version: "4.3.0a", BaseName: "Unofficial-Skyrim-Patch"},
		},
		{
			name:     "no pattern - simple name",
			filename: "my-cool-mod.zip",
			want:     nil,
		},
		{
			name:     "no pattern - no version after id",
			filename: "ModName-12345.zip",
			want:     nil,
		},
		{
			name:     "7z extension",
			filename: "TestMod-99999-1-0.7z",
			want:     &ParsedFilename{ModID: "99999", Version: "1.0", BaseName: "TestMod"},
		},
		{
			name:     "rar extension",
			filename: "TestMod-88888-2-1.rar",
			want:     &ParsedFilename{ModID: "88888", Version: "2.1", BaseName: "TestMod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseNexusModsFilename(tt.filename)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDetectModName(t *testing.T) {
	// This will be tested with actual directories in integration tests
	// For now, test the fallback behavior
	t.Run("empty path returns archive basename", func(t *testing.T) {
		got := DetectModName("", "MyMod-123-1-0.zip")
		assert.Equal(t, "MyMod-123-1-0", got)
	})
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/... -v -run "TestParseNexus|TestDetectMod"`
Expected: FAIL with "undefined: ParseNexusModsFilename"

**Step 3: Implement the parser**

Create `internal/core/filename_parser.go`:

```go
package core

import (
	"path/filepath"
	"regexp"
	"strings"
)

// ParsedFilename contains extracted info from a NexusMods-style filename
type ParsedFilename struct {
	ModID    string // NexusMods mod ID
	Version  string // Mod version (normalized)
	BaseName string // Mod name portion before the ID
}

// nexusPattern matches NexusMods filename format: Name-ModID-Version.ext
// Example: SkyUI-12604-5-2SE.zip -> groups: SkyUI, 12604, 5-2SE
var nexusPattern = regexp.MustCompile(`^(.+)-(\d+)-([^.]+)\.[a-zA-Z0-9]+$`)

// timestampSuffix matches trailing timestamps (10+ digits at end of version)
var timestampSuffix = regexp.MustCompile(`-\d{10,}$`)

// ParseNexusModsFilename attempts to extract mod ID and version from a
// NexusMods-style filename like "SkyUI-12604-5-2SE.zip".
// Returns nil if the filename doesn't match the expected pattern.
func ParseNexusModsFilename(filename string) *ParsedFilename {
	// Get just the filename without path
	filename = filepath.Base(filename)

	matches := nexusPattern.FindStringSubmatch(filename)
	if matches == nil {
		return nil
	}

	baseName := matches[1]
	modID := matches[2]
	version := matches[3]

	// Strip trailing timestamp from version (e.g., -1703618069)
	version = timestampSuffix.ReplaceAllString(version, "")

	// Normalize version: replace dashes with dots
	version = strings.ReplaceAll(version, "-", ".")

	return &ParsedFilename{
		ModID:    modID,
		Version:  version,
		BaseName: baseName,
	}
}

// DetectModName determines a display name for an imported mod.
// It checks for a single top-level directory in the extracted content,
// falling back to the archive basename if not found.
func DetectModName(extractedPath, archiveFilename string) string {
	// If no extracted path provided, use archive basename
	if extractedPath == "" {
		return stripExtension(archiveFilename)
	}

	// Try to find a single top-level directory
	entries, err := filepath.Glob(filepath.Join(extractedPath, "*"))
	if err != nil || len(entries) == 0 {
		return stripExtension(archiveFilename)
	}

	// If there's exactly one entry and it's a directory, use its name
	if len(entries) == 1 {
		info, err := filepath.Abs(entries[0])
		if err == nil {
			// Check if it's a directory by trying to list it
			subEntries, err := filepath.Glob(filepath.Join(info, "*"))
			if err == nil && len(subEntries) > 0 {
				return filepath.Base(entries[0])
			}
		}
	}

	// Fallback to archive basename
	return stripExtension(archiveFilename)
}

// stripExtension removes the file extension from a filename
func stripExtension(filename string) string {
	filename = filepath.Base(filename)
	ext := filepath.Ext(filename)
	return strings.TrimSuffix(filename, ext)
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/... -v -run "TestParseNexus|TestDetectMod"`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/core/filename_parser.go internal/core/filename_parser_test.go
git commit -m "feat(core): add NexusMods filename parser for local imports"
```

---

## Task 3: Add ImportMod Method to Service

**Files:**

- Modify: `internal/core/service.go`
- Create: `internal/core/service_import_test.go`

**Step 1: Write the failing test**

Create `internal/core/service_import_test.go`:

```go
package core_test

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestZip creates a simple zip archive for testing
func createTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
}

func TestImportMod_LocalArchive(t *testing.T) {
	// Setup temp directories
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	archivePath := filepath.Join(tempDir, "TestMod.zip")

	// Create test archive
	createTestZip(t, archivePath, map[string]string{
		"data/plugin.esp":   "test plugin",
		"data/textures.dds": "test texture",
	})

	// Create cache and database
	modCache := cache.New(cacheDir)
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	game := &domain.Game{
		ID:      "testgame",
		Name:    "Test Game",
		ModPath: filepath.Join(tempDir, "game", "mods"),
	}

	// Create importer
	importer := core.NewImporter(modCache, database)

	// Import the mod
	ctx := context.Background()
	result, err := importer.Import(ctx, archivePath, game, core.ImportOptions{
		ProfileName: "default",
	})
	require.NoError(t, err)

	// Verify result
	assert.Equal(t, domain.SourceLocal, result.Mod.SourceID)
	assert.Equal(t, "TestMod", result.Mod.Name)
	assert.Equal(t, 2, result.FilesExtracted)
	assert.False(t, result.AutoDetected)

	// Verify files are in cache
	assert.True(t, modCache.Exists(game.ID, domain.SourceLocal, result.Mod.ID, result.Mod.Version))
}

func TestImportMod_NexusModsFilename(t *testing.T) {
	// Setup temp directories
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	archivePath := filepath.Join(tempDir, "SkyUI-12604-5-2SE.zip")

	// Create test archive
	createTestZip(t, archivePath, map[string]string{
		"SkyUI/plugin.esp": "test plugin",
	})

	modCache := cache.New(cacheDir)
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	game := &domain.Game{
		ID:      "skyrim-se",
		Name:    "Skyrim SE",
		ModPath: filepath.Join(tempDir, "game", "mods"),
	}

	importer := core.NewImporter(modCache, database)

	ctx := context.Background()
	result, err := importer.Import(ctx, archivePath, game, core.ImportOptions{
		ProfileName: "default",
	})
	require.NoError(t, err)

	// Should detect NexusMods pattern but use "local" since we can't verify
	// (no API call in basic import - linking happens in command layer)
	assert.Equal(t, "12604", result.Mod.ID)
	assert.Equal(t, "5.2SE", result.Mod.Version)
	assert.True(t, result.AutoDetected)
}

func TestImportMod_UnsupportedFormat(t *testing.T) {
	tempDir := t.TempDir()
	cacheDir := filepath.Join(tempDir, "cache")
	archivePath := filepath.Join(tempDir, "mod.txt")

	// Create a non-archive file
	require.NoError(t, os.WriteFile(archivePath, []byte("not an archive"), 0644))

	modCache := cache.New(cacheDir)
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	game := &domain.Game{ID: "testgame"}

	importer := core.NewImporter(modCache, database)

	ctx := context.Background()
	_, err = importer.Import(ctx, archivePath, game, core.ImportOptions{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/... -v -run TestImportMod`
Expected: FAIL with "undefined: core.NewImporter"

**Step 3: Implement the Importer**

Add to `internal/core/service.go` (or create new file `internal/core/importer.go`):

```go
// Add to internal/core/importer.go (new file)
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/db"
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
	db        *db.DB
	extractor *Extractor
}

// NewImporter creates a new Importer
func NewImporter(cache *cache.Cache, database *db.DB) *Importer {
	return &Importer{
		cache:     cache,
		db:        database,
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
```

**Step 4: Add uuid dependency**

Run: `go get github.com/google/uuid`

**Step 5: Run tests to verify they pass**

Run: `go test ./internal/core/... -v -run TestImportMod`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/core/importer.go internal/core/service_import_test.go go.mod go.sum
git commit -m "feat(core): add Importer for local mod imports"
```

---

## Task 4: Create Import Command

**Files:**

- Create: `cmd/lmm/import.go`

**Step 1: Create the import command**

Create `cmd/lmm/import.go`:

```go
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	importProfile string
	importSource  string
	importModID   string
	importForce   bool
)

var importCmd = &cobra.Command{
	Use:   "import <archive-path>",
	Short: "Import a mod from a local archive file",
	Long: `Import a mod from a local archive file (zip, 7z, rar).

If the filename matches NexusMods naming pattern (ModName-ModID-Version.ext),
the mod ID and version will be auto-detected. Otherwise, it's tracked as a
local mod with no update checking.

Supported formats: .zip, .7z, .rar

Examples:
  lmm import ~/Downloads/SkyUI-12604-5-2SE.zip --game skyrim-se
  lmm import mod.7z                              # uses default game
  lmm import mod.zip --id 12345 --source nexusmods  # explicit linking`,
	Args: cobra.ExactArgs(1),
	RunE: runImport,
}

func init() {
	importCmd.Flags().StringVarP(&importProfile, "profile", "p", "", "profile to import to (default: default)")
	importCmd.Flags().StringVarP(&importSource, "source", "s", "", "source for update tracking (default: auto-detect or local)")
	importCmd.Flags().StringVar(&importModID, "id", "", "mod ID for linking (requires --source)")
	importCmd.Flags().BoolVarP(&importForce, "force", "f", false, "import without conflict prompts")

	rootCmd.AddCommand(importCmd)
}

func runImport(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	archivePath := args[0]

	// Validate archive exists
	if _, err := os.Stat(archivePath); err != nil {
		return fmt.Errorf("archive not found: %s", archivePath)
	}

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer service.Close()

	// Get game
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	profileName := profileOrDefault(importProfile)

	// Validate explicit linking options
	if (importSource != "" && importModID == "") || (importSource == "" && importModID != "") {
		return fmt.Errorf("--source and --id must be used together")
	}

	ctx := context.Background()

	// Create importer
	importer := core.NewImporter(service.GetGameCache(game), service.DB())

	// Import the mod
	fmt.Printf("Importing %s...\n", archivePath)

	result, err := importer.Import(ctx, archivePath, game, core.ImportOptions{
		SourceID:    importSource,
		ModID:       importModID,
		ProfileName: profileName,
	})
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}

	// Show what was detected
	if result.AutoDetected {
		fmt.Printf("  Detected NexusMods ID: %s\n", result.Mod.ID)
		fmt.Printf("  Detected version: %s\n", result.Mod.Version)
	}
	fmt.Printf("  Mod name: %s\n", result.Mod.Name)
	fmt.Printf("  Files extracted: %d\n", result.FilesExtracted)

	// Check for conflicts
	linkMethod := service.GetGameLinkMethod(game)
	installer := core.NewInstaller(service.GetGameCache(game), service.GetLinker(linkMethod), service.DB())

	if !importForce {
		conflicts, err := installer.GetConflicts(ctx, game, result.Mod, profileName)
		if err != nil {
			if verbose {
				fmt.Printf("  Warning: could not check conflicts: %v\n", err)
			}
		} else if len(conflicts) > 0 {
			fmt.Printf("\n⚠ File conflicts detected:\n")

			// Group conflicts by mod
			modConflicts := make(map[string][]string)
			for _, c := range conflicts {
				key := c.CurrentSourceID + ":" + c.CurrentModID
				modConflicts[key] = append(modConflicts[key], c.RelativePath)
			}

			for key, paths := range modConflicts {
				parts := strings.SplitN(key, ":", 2)
				sourceID, modID := parts[0], parts[1]

				conflictMod, _ := service.GetInstalledMod(sourceID, modID, gameID, profileName)
				modName := modID
				if conflictMod != nil {
					modName = conflictMod.Name
				}

				fmt.Printf("  From %s (%s):\n", modName, modID)
				maxShow := 5
				for i, p := range paths {
					if i >= maxShow {
						fmt.Printf("    ... and %d more\n", len(paths)-maxShow)
						break
					}
					fmt.Printf("    - %s\n", p)
				}
			}

			fmt.Printf("\n%d file(s) will be overwritten. Continue? [y/N]: ", len(conflicts))
			reader := bufio.NewReader(os.Stdin)
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(strings.ToLower(input))
			if input != "y" && input != "yes" {
				return fmt.Errorf("import cancelled")
			}
		}
	}

	// Deploy to game directory
	fmt.Println("\nDeploying to game directory...")

	if err := installer.Install(ctx, game, result.Mod, profileName); err != nil {
		return fmt.Errorf("deployment failed: %w", err)
	}

	// Save to database
	installedMod := &domain.InstalledMod{
		Mod:          *result.Mod,
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   linkMethod,
	}

	if err := service.DB().SaveInstalledMod(installedMod); err != nil {
		return fmt.Errorf("failed to save mod: %w", err)
	}

	// Add to profile
	pm := getProfileManager(service)
	modRef := domain.ModReference{
		SourceID: result.Mod.SourceID,
		ModID:    result.Mod.ID,
		Version:  result.Mod.Version,
	}

	if _, err := pm.Get(gameID, profileName); err != nil {
		if err == domain.ErrProfileNotFound {
			if _, err := pm.Create(gameID, profileName); err != nil {
				if verbose {
					fmt.Printf("  Warning: could not create profile: %v\n", err)
				}
			}
		}
	}

	if err := pm.UpsertMod(gameID, profileName, modRef); err != nil {
		if verbose {
			fmt.Printf("  Warning: could not update profile: %v\n", err)
		}
	}

	// Success message
	sourceDisplay := result.Mod.SourceID
	if sourceDisplay == domain.SourceLocal {
		sourceDisplay = "local"
	}

	fmt.Printf("\n✓ Imported: %s v%s (%s)\n", result.Mod.Name, result.Mod.Version, sourceDisplay)
	fmt.Printf("  Added to profile: %s\n", profileName)

	return nil
}
```

**Step 2: Verify build**

Run: `go build -o lmm ./cmd/lmm`
Expected: Build succeeds

**Step 3: Commit**

```bash
git add cmd/lmm/import.go
git commit -m "feat(cli): add import command for local mod archives"
```

---

## Task 5: Update List Command to Show Local Source

**Files:**

- Modify: `cmd/lmm/list.go`

**Step 1: Find and update the list display**

In `cmd/lmm/list.go`, find where mods are displayed and update to show "(local)" for local mods. Look for the format string that displays source info.

Update the display to handle local source:

```go
// Find the line that displays source and update it to something like:
sourceDisplay := m.SourceID
if m.SourceID == domain.SourceLocal {
    sourceDisplay = "local"
}
// Then use sourceDisplay in the output
```

**Step 2: Verify the change**

Run: `go build -o lmm ./cmd/lmm && ./lmm list --help`
Expected: Build succeeds

**Step 3: Commit**

```bash
git add cmd/lmm/list.go
git commit -m "feat(cli): show (local) source in list output"
```

---

## Task 6: Skip Local Mods in Update Check

**Files:**

- Modify: `cmd/lmm/update.go` (or wherever update checking happens)

**Step 1: Find update check logic**

Search for where `CheckUpdates` is called and add a filter to skip local mods:

```go
// Filter out local mods before checking updates
var modsToCheck []domain.InstalledMod
for _, m := range installedMods {
    if m.SourceID != domain.SourceLocal {
        modsToCheck = append(modsToCheck, m)
    }
}
```

**Step 2: Verify build**

Run: `go build -o lmm ./cmd/lmm`
Expected: Build succeeds

**Step 3: Commit**

```bash
git add cmd/lmm/update.go
git commit -m "fix(cli): skip local mods when checking for updates"
```

---

## Task 7: Add Integration Test for Full Import Flow

**Files:**

- Create: `cmd/lmm/import_test.go`

**Step 1: Create end-to-end test**

Create `cmd/lmm/import_test.go`:

```go
package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImportCommand_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// This is a placeholder for manual testing
	// Full integration tests would require:
	// 1. Setting up a test config directory
	// 2. Creating a test game config
	// 3. Running the import command
	// 4. Verifying files are deployed

	t.Log("Import command integration test - run manually with: ./lmm import testmod.zip -g testgame")
}

func createTestArchive(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		require.NoError(t, err)
		_, err = fw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
}
```

**Step 2: Commit**

```bash
git add cmd/lmm/import_test.go
git commit -m "test(cli): add import command integration test placeholder"
```

---

## Task 8: Run Full Test Suite and Lint

**Step 1: Run all tests**

Run: `go test ./... -v`
Expected: All tests PASS

**Step 2: Run linter**

Run: `trunk check`
Expected: No errors (or only pre-existing ones)

**Step 3: Format code**

Run: `go fmt ./...`

**Step 4: Commit any fixes**

```bash
git add -A
git commit -m "chore: fix lint issues" --allow-empty
```

---

## Task 9: Update Version and Changelog

**Files:**

- Modify: `cmd/lmm/root.go`
- Modify: `CHANGELOG.md`

**Step 1: Bump version**

In `cmd/lmm/root.go`, update:

```go
version = "0.10.0"
```

**Step 2: Update changelog**

Add to `CHANGELOG.md` under a new `## [0.10.0]` section:

```markdown
## [0.10.0] - 2026-01-28

### Added

- `lmm import` command for importing mods from local archive files
- Smart NexusMods filename detection (parses ModName-ModID-Version.ext pattern)
- Local mods tracked with `source_id = "local"` (no update checking)
- Support for .zip, .7z, and .rar archives

### Changed

- `lmm list` now shows "(local)" for locally imported mods
- Update checking skips local mods
```

**Step 3: Commit**

```bash
git add cmd/lmm/root.go CHANGELOG.md
git commit -m "chore: bump version to 0.10.0"
```

---

## Task 10: Final Verification

**Step 1: Build and test manually**

```bash
go build -o lmm ./cmd/lmm
./lmm --version
./lmm import --help
```

**Step 2: Test with a real archive (if available)**

```bash
# Create a test archive
mkdir -p /tmp/testmod/data
echo "test" > /tmp/testmod/data/test.esp
cd /tmp/testmod && zip -r ../TestMod-99999-1-0.zip . && cd -

# Import it
./lmm import /tmp/TestMod-99999-1-0.zip --game <your-test-game>
./lmm list --game <your-test-game>
```

**Step 3: Push changes**

```bash
git push
```

---

## Summary

**Files created:**

- `internal/core/filename_parser.go`
- `internal/core/filename_parser_test.go`
- `internal/core/importer.go`
- `internal/core/service_import_test.go`
- `cmd/lmm/import.go`
- `cmd/lmm/import_test.go`

**Files modified:**

- `internal/domain/mod.go` (add SourceLocal constant)
- `internal/domain/mod_test.go` (add test)
- `cmd/lmm/list.go` (show local source)
- `cmd/lmm/update.go` (skip local mods)
- `cmd/lmm/root.go` (version bump)
- `CHANGELOG.md`
- `go.mod` / `go.sum` (uuid dependency)
