# P2: Conflict Detection Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Warn users when installing a mod that overwrites files from an existing mod, and provide commands to view file ownership and conflicts.

**Architecture:** Add a `deployed_files` table to track which mod owns each file path in the game directory. Before deploying, check for conflicts and prompt the user. Add `lmm conflicts` command and `lmm mod files` subcommand.

**Tech Stack:** Go, SQLite, Cobra CLI

**GitHub Issue:** #6

---

## Task 1: Database Migration V7 - deployed_files Table

**Files:**

- Modify: `internal/storage/db/migrations.go`
- Modify: `internal/storage/db/migrations_test.go`

**Step 1: Write the failing test**

Add to `migrations_test.go`:

```go
func TestMigrationV7_DeployedFilesTable(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Verify deployed_files table exists
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='deployed_files'
	`).Scan(&tableName)
	require.NoError(t, err)
	assert.Equal(t, "deployed_files", tableName)

	// Verify we can insert and query
	_, err = db.Exec(`
		INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id)
		VALUES ('skyrim-se', 'default', 'meshes/test.nif', 'nexusmods', '12345')
	`)
	require.NoError(t, err)

	var path, sourceID, modID string
	err = db.QueryRow(`
		SELECT relative_path, source_id, mod_id FROM deployed_files
		WHERE game_id = 'skyrim-se' AND profile_name = 'default'
	`).Scan(&path, &sourceID, &modID)
	require.NoError(t, err)
	assert.Equal(t, "meshes/test.nif", path)
	assert.Equal(t, "nexusmods", sourceID)
	assert.Equal(t, "12345", modID)
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/db/... -run TestMigrationV7 -v`
Expected: FAIL (table doesn't exist)

**Step 3: Write minimal implementation**

In `migrations.go`, change `currentVersion` and add migration:

```go
const currentVersion = 7
```

Add `migrateV7` to the migrations slice:

```go
migrations := []func(*DB) error{
	migrateV1,
	migrateV2,
	migrateV3,
	migrateV4,
	migrateV5,
	migrateV6,
	migrateV7,
}
```

Add the migration function:

```go
func migrateV7(d *DB) error {
	_, err := d.Exec(`
		CREATE TABLE deployed_files (
			game_id TEXT NOT NULL,
			profile_name TEXT NOT NULL,
			relative_path TEXT NOT NULL,
			source_id TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			deployed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (game_id, profile_name, relative_path)
		)
	`)
	return err
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/storage/db/... -run TestMigrationV7 -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/storage/db/migrations.go internal/storage/db/migrations_test.go
git commit -m "feat(db): add migration V7 for deployed_files table"
```

---

## Task 2: Database Methods for Deployed Files

**Files:**

- Create: `internal/storage/db/files.go`
- Create: `internal/storage/db/files_test.go`

**Step 1: Write the failing tests**

Create `files_test.go`:

```go
package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveDeployedFile(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	err := db.SaveDeployedFile("skyrim-se", "default", "meshes/test.nif", "nexusmods", "12345")
	require.NoError(t, err)

	// Verify it was saved
	owner, err := db.GetFileOwner("skyrim-se", "default", "meshes/test.nif")
	require.NoError(t, err)
	assert.Equal(t, "nexusmods", owner.SourceID)
	assert.Equal(t, "12345", owner.ModID)
}

func TestSaveDeployedFile_Upsert(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Save initial owner
	err := db.SaveDeployedFile("skyrim-se", "default", "meshes/test.nif", "nexusmods", "111")
	require.NoError(t, err)

	// Overwrite with new owner
	err = db.SaveDeployedFile("skyrim-se", "default", "meshes/test.nif", "nexusmods", "222")
	require.NoError(t, err)

	// Verify new owner
	owner, err := db.GetFileOwner("skyrim-se", "default", "meshes/test.nif")
	require.NoError(t, err)
	assert.Equal(t, "222", owner.ModID)
}

func TestGetFileOwner_NotFound(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	owner, err := db.GetFileOwner("skyrim-se", "default", "nonexistent.nif")
	require.NoError(t, err)
	assert.Nil(t, owner)
}

func TestDeleteDeployedFiles(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Save some files
	db.SaveDeployedFile("skyrim-se", "default", "meshes/a.nif", "nexusmods", "123")
	db.SaveDeployedFile("skyrim-se", "default", "meshes/b.nif", "nexusmods", "123")
	db.SaveDeployedFile("skyrim-se", "default", "meshes/c.nif", "nexusmods", "456")

	// Delete files for mod 123
	err := db.DeleteDeployedFiles("skyrim-se", "default", "nexusmods", "123")
	require.NoError(t, err)

	// Verify 123's files are gone
	owner, _ := db.GetFileOwner("skyrim-se", "default", "meshes/a.nif")
	assert.Nil(t, owner)
	owner, _ = db.GetFileOwner("skyrim-se", "default", "meshes/b.nif")
	assert.Nil(t, owner)

	// Verify 456's files remain
	owner, _ = db.GetFileOwner("skyrim-se", "default", "meshes/c.nif")
	assert.NotNil(t, owner)
	assert.Equal(t, "456", owner.ModID)
}

func TestGetDeployedFilesForMod(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.SaveDeployedFile("skyrim-se", "default", "meshes/a.nif", "nexusmods", "123")
	db.SaveDeployedFile("skyrim-se", "default", "meshes/b.nif", "nexusmods", "123")
	db.SaveDeployedFile("skyrim-se", "default", "meshes/c.nif", "nexusmods", "456")

	files, err := db.GetDeployedFilesForMod("skyrim-se", "default", "nexusmods", "123")
	require.NoError(t, err)
	assert.Len(t, files, 2)
	assert.Contains(t, files, "meshes/a.nif")
	assert.Contains(t, files, "meshes/b.nif")
}

func TestCheckFileConflicts(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// Mod 123 owns some files
	db.SaveDeployedFile("skyrim-se", "default", "meshes/shared.nif", "nexusmods", "123")
	db.SaveDeployedFile("skyrim-se", "default", "meshes/only123.nif", "nexusmods", "123")

	// Check conflicts for new mod that wants to deploy shared.nif and newfile.nif
	paths := []string{"meshes/shared.nif", "meshes/newfile.nif"}
	conflicts, err := db.CheckFileConflicts("skyrim-se", "default", paths)
	require.NoError(t, err)

	// Only shared.nif should conflict
	assert.Len(t, conflicts, 1)
	assert.Equal(t, "meshes/shared.nif", conflicts[0].RelativePath)
	assert.Equal(t, "123", conflicts[0].ModID)
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/storage/db/... -run TestSaveDeployedFile -v`
Run: `go test ./internal/storage/db/... -run TestGetFileOwner -v`
Run: `go test ./internal/storage/db/... -run TestDeleteDeployedFiles -v`
Run: `go test ./internal/storage/db/... -run TestGetDeployedFilesForMod -v`
Run: `go test ./internal/storage/db/... -run TestCheckFileConflicts -v`
Expected: FAIL (functions don't exist)

**Step 3: Write minimal implementation**

Create `files.go`:

```go
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// FileOwner represents the mod that owns a deployed file
type FileOwner struct {
	SourceID string
	ModID    string
}

// FileConflict represents a file that would be overwritten
type FileConflict struct {
	RelativePath string
	SourceID     string
	ModID        string
}

// SaveDeployedFile records that a file is deployed by a specific mod.
// Uses upsert to handle overwrites (new mod takes ownership).
func (d *DB) SaveDeployedFile(gameID, profileName, relativePath, sourceID, modID string) error {
	_, err := d.Exec(`
		INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(game_id, profile_name, relative_path) DO UPDATE SET
			source_id = excluded.source_id,
			mod_id = excluded.mod_id,
			deployed_at = CURRENT_TIMESTAMP
	`, gameID, profileName, relativePath, sourceID, modID)
	if err != nil {
		return fmt.Errorf("saving deployed file: %w", err)
	}
	return nil
}

// GetFileOwner returns the mod that owns a specific file path.
// Returns nil if no mod owns the file.
func (d *DB) GetFileOwner(gameID, profileName, relativePath string) (*FileOwner, error) {
	var owner FileOwner
	err := d.QueryRow(`
		SELECT source_id, mod_id FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND relative_path = ?
	`, gameID, profileName, relativePath).Scan(&owner.SourceID, &owner.ModID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting file owner: %w", err)
	}
	return &owner, nil
}

// DeleteDeployedFiles removes all deployed file records for a specific mod.
func (d *DB) DeleteDeployedFiles(gameID, profileName, sourceID, modID string) error {
	_, err := d.Exec(`
		DELETE FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND source_id = ? AND mod_id = ?
	`, gameID, profileName, sourceID, modID)
	if err != nil {
		return fmt.Errorf("deleting deployed files: %w", err)
	}
	return nil
}

// GetDeployedFilesForMod returns all file paths deployed by a specific mod.
func (d *DB) GetDeployedFilesForMod(gameID, profileName, sourceID, modID string) ([]string, error) {
	rows, err := d.Query(`
		SELECT relative_path FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND source_id = ? AND mod_id = ?
		ORDER BY relative_path
	`, gameID, profileName, sourceID, modID)
	if err != nil {
		return nil, fmt.Errorf("querying deployed files: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scanning path: %w", err)
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

// CheckFileConflicts checks which of the given paths are already owned by other mods.
// Returns a slice of conflicts (empty if no conflicts).
func (d *DB) CheckFileConflicts(gameID, profileName string, paths []string) ([]FileConflict, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(paths))
	args := make([]interface{}, 0, len(paths)+2)
	args = append(args, gameID, profileName)
	for i, p := range paths {
		placeholders[i] = "?"
		args = append(args, p)
	}

	query := fmt.Sprintf(`
		SELECT relative_path, source_id, mod_id FROM deployed_files
		WHERE game_id = ? AND profile_name = ? AND relative_path IN (%s)
		ORDER BY relative_path
	`, strings.Join(placeholders, ","))

	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("checking conflicts: %w", err)
	}
	defer rows.Close()

	var conflicts []FileConflict
	for rows.Next() {
		var c FileConflict
		if err := rows.Scan(&c.RelativePath, &c.SourceID, &c.ModID); err != nil {
			return nil, fmt.Errorf("scanning conflict: %w", err)
		}
		conflicts = append(conflicts, c)
	}
	return conflicts, rows.Err()
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/storage/db/... -run "TestSaveDeployed|TestGetFile|TestDeleteDeployed|TestCheckFile" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/storage/db/files.go internal/storage/db/files_test.go
git commit -m "feat(db): add deployed file tracking methods"
```

---

## Task 3: Track Files on Deploy

**Files:**

- Modify: `internal/core/installer.go`
- Modify: `internal/core/installer_test.go`

**Step 1: Update Installer to accept DB**

The Installer needs access to the database to track deployed files. Modify the struct and constructor.

In `installer.go`, update the struct:

```go
type Installer struct {
	cache  *cache.Cache
	linker linker.Linker
	db     *db.DB
}

func NewInstaller(cache *cache.Cache, linker linker.Linker, db *db.DB) *Installer {
	return &Installer{
		cache:  cache,
		linker: linker,
		db:     db,
	}
}
```

**Step 2: Update Install method to track files**

Modify the `Install` method to save deployed files to the database:

```go
func (i *Installer) Install(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) error {
	// Check if mod is cached
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return fmt.Errorf("mod not in cache: %s/%s@%s", mod.SourceID, mod.ID, mod.Version)
	}

	// Get list of files in the cached mod
	files, err := i.cache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return fmt.Errorf("listing cached files: %w", err)
	}

	// Deploy each file
	for _, file := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		srcPath := i.cache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, file)
		dstPath := filepath.Join(game.ModPath, file)

		if err := i.linker.Deploy(srcPath, dstPath); err != nil {
			return fmt.Errorf("deploying %s: %w", file, err)
		}

		// Track file ownership in database
		if i.db != nil {
			if err := i.db.SaveDeployedFile(game.ID, profileName, file, mod.SourceID, mod.ID); err != nil {
				return fmt.Errorf("tracking deployed file %s: %w", file, err)
			}
		}
	}

	return nil
}
```

**Step 3: Update Uninstall method to remove file tracking**

Modify the `Uninstall` method to remove deployed file records:

```go
func (i *Installer) Uninstall(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) error {
	// Get list of files in the cached mod
	files, err := i.cache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return fmt.Errorf("listing cached files: %w", err)
	}

	// Undeploy each file
	for _, file := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		dstPath := filepath.Join(game.ModPath, file)

		if err := i.linker.Undeploy(dstPath); err != nil {
			return fmt.Errorf("undeploying %s: %w", file, err)
		}
	}

	// Remove file tracking from database
	if i.db != nil {
		if err := i.db.DeleteDeployedFiles(game.ID, profileName, mod.SourceID, mod.ID); err != nil {
			return fmt.Errorf("removing file tracking: %w", err)
		}
	}

	// Clean up any empty directories left behind
	linker.CleanupEmptyDirs(game.ModPath)

	return nil
}
```

Note: The `Uninstall` method signature needs to add `profileName` parameter.

**Step 4: Update all callers of NewInstaller and Uninstall**

Search for all uses of `NewInstaller` and `Uninstall` and update them:

- `cmd/lmm/deploy.go`: Update `NewInstaller` calls to pass `service.DB()`
- `cmd/lmm/install.go`: Update `NewInstaller` calls to pass `service.DB()`
- `cmd/lmm/uninstall.go`: Update `Uninstall` calls to pass `profileName`
- `internal/core/service.go`: Update `GetInstaller` to pass DB

**Step 5: Run all tests**

Run: `go test ./... -v`
Expected: PASS (or identify and fix any compile errors)

**Step 6: Commit**

```bash
git add internal/core/installer.go internal/core/service.go cmd/lmm/deploy.go cmd/lmm/install.go cmd/lmm/uninstall.go
git commit -m "feat(core): track deployed files in database"
```

---

## Task 4: Conflict Detection in Installer

**Files:**

- Modify: `internal/core/installer.go`
- Create: `internal/core/installer_test.go` (add conflict tests)

**Step 1: Add GetConflicts method**

Add a method to check for conflicts before installing:

```go
// Conflict represents a file that would be overwritten by installing a mod
type Conflict struct {
	RelativePath    string
	CurrentSourceID string
	CurrentModID    string
}

// GetConflicts checks if installing a mod would overwrite files from other mods.
// Returns conflicts for files owned by OTHER mods (not the mod being installed).
func (i *Installer) GetConflicts(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) ([]Conflict, error) {
	if i.db == nil {
		return nil, nil
	}

	// Check if mod is cached
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return nil, fmt.Errorf("mod not in cache: %s/%s@%s", mod.SourceID, mod.ID, mod.Version)
	}

	// Get list of files in the cached mod
	files, err := i.cache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil, fmt.Errorf("listing cached files: %w", err)
	}

	// Check for conflicts
	dbConflicts, err := i.db.CheckFileConflicts(game.ID, profileName, files)
	if err != nil {
		return nil, fmt.Errorf("checking conflicts: %w", err)
	}

	// Filter out conflicts with self (re-installing same mod)
	var conflicts []Conflict
	for _, c := range dbConflicts {
		if c.SourceID != mod.SourceID || c.ModID != mod.ID {
			conflicts = append(conflicts, Conflict{
				RelativePath:    c.RelativePath,
				CurrentSourceID: c.SourceID,
				CurrentModID:    c.ModID,
			})
		}
	}

	return conflicts, nil
}
```

**Step 2: Write tests**

Add to `installer_test.go`:

```go
func TestGetConflicts(t *testing.T) {
	// Setup test with in-memory DB and temp cache
	tempDir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	c := cache.New(tempDir)
	lnk := linker.NewSymlink()
	inst := NewInstaller(c, lnk, database)

	game := &domain.Game{ID: "test-game", ModPath: filepath.Join(tempDir, "mods")}
	os.MkdirAll(game.ModPath, 0755)

	// Create cached mod files for mod A
	modAPath := c.ModPath(game.ID, "nexusmods", "111", "1.0")
	os.MkdirAll(modAPath, 0755)
	os.WriteFile(filepath.Join(modAPath, "shared.txt"), []byte("a"), 0644)

	// Deploy mod A
	modA := &domain.Mod{SourceID: "nexusmods", ID: "111", Version: "1.0"}
	err = inst.Install(context.Background(), game, modA, "default")
	require.NoError(t, err)

	// Create cached mod files for mod B (with overlapping file)
	modBPath := c.ModPath(game.ID, "nexusmods", "222", "1.0")
	os.MkdirAll(modBPath, 0755)
	os.WriteFile(filepath.Join(modBPath, "shared.txt"), []byte("b"), 0644)
	os.WriteFile(filepath.Join(modBPath, "unique.txt"), []byte("b"), 0644)

	// Check conflicts for mod B
	modB := &domain.Mod{SourceID: "nexusmods", ID: "222", Version: "1.0"}
	conflicts, err := inst.GetConflicts(context.Background(), game, modB, "default")
	require.NoError(t, err)

	assert.Len(t, conflicts, 1)
	assert.Equal(t, "shared.txt", conflicts[0].RelativePath)
	assert.Equal(t, "111", conflicts[0].CurrentModID)
}

func TestGetConflicts_ReinstallSelf(t *testing.T) {
	// Setup
	tempDir := t.TempDir()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	c := cache.New(tempDir)
	lnk := linker.NewSymlink()
	inst := NewInstaller(c, lnk, database)

	game := &domain.Game{ID: "test-game", ModPath: filepath.Join(tempDir, "mods")}
	os.MkdirAll(game.ModPath, 0755)

	// Create and deploy mod A
	modAPath := c.ModPath(game.ID, "nexusmods", "111", "1.0")
	os.MkdirAll(modAPath, 0755)
	os.WriteFile(filepath.Join(modAPath, "file.txt"), []byte("a"), 0644)

	modA := &domain.Mod{SourceID: "nexusmods", ID: "111", Version: "1.0"}
	err = inst.Install(context.Background(), game, modA, "default")
	require.NoError(t, err)

	// Check conflicts for same mod (re-install) - should be empty
	conflicts, err := inst.GetConflicts(context.Background(), game, modA, "default")
	require.NoError(t, err)
	assert.Empty(t, conflicts)
}
```

**Step 3: Run tests**

Run: `go test ./internal/core/... -run TestGetConflicts -v`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/core/installer.go internal/core/installer_test.go
git commit -m "feat(core): add conflict detection to installer"
```

---

## Task 5: Integrate Conflict Detection into Install Command

**Files:**

- Modify: `cmd/lmm/install.go`

**Step 1: Add --force flag**

Add the flag variable and register it:

```go
var (
	// ... existing flags ...
	installForce bool
)

func init() {
	// ... existing flags ...
	installCmd.Flags().BoolVarP(&installForce, "force", "f", false, "install without conflict prompts")
	// ...
}
```

**Step 2: Add conflict checking before deploy**

After downloading and before deploying, check for conflicts:

```go
// After extraction, before deploying...

// Check for conflicts (unless --force)
if !installForce {
	conflicts, err := installer.GetConflicts(ctx, game, mod, profileName)
	if err != nil {
		if verbose {
			fmt.Printf("Warning: could not check conflicts: %v\n", err)
		}
	} else if len(conflicts) > 0 {
		fmt.Printf("\n⚠ File conflicts detected:\n")

		// Group conflicts by mod
		modConflicts := make(map[string][]string) // modID -> []paths
		for _, c := range conflicts {
			key := c.CurrentSourceID + ":" + c.CurrentModID
			modConflicts[key] = append(modConflicts[key], c.RelativePath)
		}

		for key, paths := range modConflicts {
			parts := strings.SplitN(key, ":", 2)
			sourceID, modID := parts[0], parts[1]

			// Try to get mod name
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
			return fmt.Errorf("installation cancelled")
		}
	}
}
```

**Step 3: Run tests and manual verification**

Run: `go test ./cmd/lmm/... -v`
Run: `go build -o lmm ./cmd/lmm && ./lmm install --help`
Expected: Shows --force flag

**Step 4: Commit**

```bash
git add cmd/lmm/install.go
git commit -m "feat(cli): add conflict detection with --force flag to install"
```

---

## Task 6: Add lmm mod files Subcommand

**Files:**

- Modify: `cmd/lmm/mod.go`
- Modify: `cmd/lmm/mod_test.go`

**Step 1: Read existing mod.go structure**

First understand the current mod command structure, then add a `files` subcommand.

**Step 2: Add files subcommand**

Add to `mod.go`:

```go
var (
	modFilesSource  string
	modFilesProfile string
)

var modFilesCmd = &cobra.Command{
	Use:   "files <mod-id>",
	Short: "List files deployed by a mod",
	Long: `Show all files that a mod has deployed to the game directory.

This helps identify which files a mod owns, useful for debugging
conflicts or understanding mod contents.

Examples:
  lmm mod files 12345 --game skyrim-se
  lmm mod files 12345 --game skyrim-se --profile survival`,
	Args: cobra.ExactArgs(1),
	RunE: runModFiles,
}

func init() {
	// ... existing init code ...

	modFilesCmd.Flags().StringVarP(&modFilesSource, "source", "s", "nexusmods", "mod source")
	modFilesCmd.Flags().StringVarP(&modFilesProfile, "profile", "p", "", "profile (default: default)")

	modCmd.AddCommand(modFilesCmd)
}

func runModFiles(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	modID := args[0]
	profileName := profileOrDefault(modFilesProfile)

	svc, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer svc.Close()

	// Get mod info for display
	mod, err := svc.GetInstalledMod(modFilesSource, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	// Get deployed files from database
	files, err := svc.DB().GetDeployedFilesForMod(gameID, profileName, modFilesSource, modID)
	if err != nil {
		return fmt.Errorf("getting deployed files: %w", err)
	}

	fmt.Printf("Files deployed by %s (%s):\n\n", mod.Name, modID)

	if len(files) == 0 {
		fmt.Println("  No deployed files tracked.")
		fmt.Println("  (Files are tracked on install; existing mods may need to be redeployed)")
		return nil
	}

	for _, f := range files {
		fmt.Printf("  %s\n", f)
	}
	fmt.Printf("\nTotal: %d file(s)\n", len(files))

	return nil
}
```

**Step 3: Run tests**

Run: `go build -o lmm ./cmd/lmm && ./lmm mod files --help`
Expected: Shows help for files subcommand

**Step 4: Commit**

```bash
git add cmd/lmm/mod.go
git commit -m "feat(cli): add 'lmm mod files' subcommand"
```

---

## Task 7: Add lmm conflicts Command

**Files:**

- Create: `cmd/lmm/conflicts.go`
- Create: `cmd/lmm/conflicts_test.go`

**Step 1: Create the conflicts command**

Create `conflicts.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var conflictsProfile string

var conflictsCmd = &cobra.Command{
	Use:   "conflicts",
	Short: "Show all file conflicts in the current profile",
	Long: `Display all file conflicts in the current profile.

A conflict occurs when multiple mods deploy the same file path.
The mod listed as "owner" is the one whose file is currently deployed.

Examples:
  lmm conflicts --game skyrim-se
  lmm conflicts --game skyrim-se --profile survival`,
	RunE: runConflicts,
}

func init() {
	conflictsCmd.Flags().StringVarP(&conflictsProfile, "profile", "p", "", "profile (default: default)")

	rootCmd.AddCommand(conflictsCmd)
}

func runConflicts(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	profileName := profileOrDefault(conflictsProfile)

	svc, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer svc.Close()

	// Get all installed mods
	mods, err := svc.GetInstalledMods(gameID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	if len(mods) == 0 {
		fmt.Println("No installed mods.")
		return nil
	}

	// Build map of mod ID to name for display
	modNames := make(map[string]string)
	for _, m := range mods {
		key := m.SourceID + ":" + m.ID
		modNames[key] = m.Name
	}

	// For each mod, get its files and check if any are owned by another mod
	type conflictInfo struct {
		path     string
		ownerKey string
		wantKeys []string // mods that want this file
	}

	// Map of relative_path -> mod keys that have this file
	fileMods := make(map[string][]string)

	for _, m := range mods {
		files, err := svc.DB().GetDeployedFilesForMod(gameID, profileName, m.SourceID, m.ID)
		if err != nil {
			continue
		}
		key := m.SourceID + ":" + m.ID
		for _, f := range files {
			fileMods[f] = append(fileMods[f], key)
		}
	}

	// Find files with multiple mods
	var conflicts []conflictInfo
	for path, keys := range fileMods {
		if len(keys) > 1 {
			// Get current owner from database
			owner, err := svc.DB().GetFileOwner(gameID, profileName, path)
			if err != nil || owner == nil {
				continue
			}
			ownerKey := owner.SourceID + ":" + owner.ModID

			// Other mods that wanted this file
			var wantKeys []string
			for _, k := range keys {
				if k != ownerKey {
					wantKeys = append(wantKeys, k)
				}
			}

			conflicts = append(conflicts, conflictInfo{
				path:     path,
				ownerKey: ownerKey,
				wantKeys: wantKeys,
			})
		}
	}

	if len(conflicts) == 0 {
		fmt.Println("No conflicts found.")
		return nil
	}

	fmt.Printf("Found %d conflicting file(s):\n\n", len(conflicts))

	for _, c := range conflicts {
		ownerName := modNames[c.ownerKey]
		if ownerName == "" {
			ownerName = c.ownerKey
		}

		fmt.Printf("  %s\n", c.path)
		fmt.Printf("    Owner: %s\n", ownerName)
		fmt.Printf("    Also in: ")
		for i, wk := range c.wantKeys {
			wantName := modNames[wk]
			if wantName == "" {
				wantName = wk
			}
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Print(wantName)
		}
		fmt.Println()
		fmt.Println()
	}

	return nil
}
```

**Step 2: Run manual test**

Run: `go build -o lmm ./cmd/lmm && ./lmm conflicts --help`
Expected: Shows help for conflicts command

**Step 3: Commit**

```bash
git add cmd/lmm/conflicts.go
git commit -m "feat(cli): add 'lmm conflicts' command"
```

---

## Task 8: Update Deploy Command for File Tracking

**Files:**

- Modify: `cmd/lmm/deploy.go`

The deploy command creates a new Installer and calls Install, but needs to pass the DB. Update to match the new Installer signature.

**Step 1: Update deploy.go**

Update the Installer creation to pass DB:

```go
installer := core.NewInstaller(service.GetGameCache(game), lnk, service.DB())
```

Also update the Uninstall call to pass profileName:

```go
if err := installer.Uninstall(ctx, game, &mod.Mod, profileName); err != nil {
```

**Step 2: Run tests**

Run: `go test ./cmd/lmm/... -v`
Run: `go build -o lmm ./cmd/lmm`
Expected: Builds successfully

**Step 3: Commit**

```bash
git add cmd/lmm/deploy.go
git commit -m "fix(cli): pass DB to installer in deploy command"
```

---

## Task 9: Update Uninstall Command

**Files:**

- Modify: `cmd/lmm/uninstall.go`

**Step 1: Update uninstall to pass profileName**

The Uninstall method now requires profileName. Update all calls.

**Step 2: Run tests**

Run: `go test ./cmd/lmm/... -v`
Expected: PASS

**Step 3: Commit**

```bash
git add cmd/lmm/uninstall.go
git commit -m "fix(cli): pass profile name to installer.Uninstall"
```

---

## Task 10: Final Testing and Version Bump

**Files:**

- Modify: `cmd/lmm/root.go`
- Modify: `CHANGELOG.md`

**Step 1: Run full test suite**

Run: `go test ./... -v`
Expected: All tests pass

**Step 2: Run linter**

Run: `trunk check`
Expected: No errors (warnings OK)

**Step 3: Bump version**

In `root.go`, change:

```go
version = "0.9.0"
```

**Step 4: Update CHANGELOG.md**

Add to CHANGELOG.md under `## [0.9.0] - 2026-01-28`:

```markdown
### Added

- **Conflict detection**: Warns when installing mods that overwrite files from other mods
  - Shows which files conflict and which mod currently owns them
  - Prompts for confirmation before proceeding
  - Use `--force` flag to skip conflict prompts
- New `lmm conflicts` command to view all file conflicts in a profile
- New `lmm mod files <mod-id>` subcommand to see files deployed by a mod
- Database tracks which mod owns each deployed file

### Changed

- Installer now tracks file ownership in database during deploy/undeploy
- Deploy command properly tracks files when re-deploying mods
```

**Step 5: Commit version bump**

```bash
git add cmd/lmm/root.go CHANGELOG.md
git commit -m "chore: bump version to 0.9.0"
```

---

## Summary

This plan implements P2: Conflict Detection with:

1. **Database migration V7** - Creates `deployed_files` table
2. **File tracking methods** - CRUD operations for deployed files
3. **Installer integration** - Track files on install/uninstall
4. **Conflict detection** - GetConflicts method checks for overwrites
5. **CLI integration** - `--force` flag, conflict prompts
6. **New commands** - `lmm mod files` and `lmm conflicts`

All existing tests should continue to pass, and new functionality is fully tested.
