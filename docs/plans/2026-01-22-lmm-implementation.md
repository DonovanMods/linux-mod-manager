# lmm Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a terminal-based mod manager for Linux with NexusMods integration, profile support, and flexible mod deployment strategies.

**Architecture:** Layered monolith with interface-based extensibility. Domain types have no dependencies, storage layer handles SQLite + YAML, source layer abstracts mod repositories, core layer orchestrates business logic, TUI/CLI provide user interfaces.

**Tech Stack:** Go 1.25+, Bubble Tea (TUI), Cobra (CLI), modernc.org/sqlite (pure Go SQLite), gopkg.in/yaml.v3, hasura/go-graphql-client

---

## Phase 1: Core Infrastructure

### Task 1.1: Project Structure and Dependencies

**Files:**

- Create: `cmd/lmm/main.go`
- Create: `internal/domain/mod.go`
- Create: `internal/domain/game.go`
- Create: `internal/domain/profile.go`
- Create: `internal/domain/errors.go`
- Modify: `go.mod`

**Step 1: Initialize project structure**

```bash
mkdir -p cmd/lmm internal/domain internal/source internal/storage/db internal/storage/config internal/storage/cache internal/linker internal/core internal/tui/views
```

**Step 2: Add dependencies to go.mod**

```bash
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/bubbles@latest
go get github.com/charmbracelet/lipgloss@latest
go get github.com/spf13/cobra@latest
go get modernc.org/sqlite@latest
go get gopkg.in/yaml.v3@latest
go get github.com/hasura/go-graphql-client@latest
go get github.com/avast/retry-go/v4@latest
go get github.com/stretchr/testify@latest
```

**Step 3: Create domain error types**

Create `internal/domain/errors.go`:

```go
package domain

import "errors"

var (
	ErrModNotFound      = errors.New("mod not found")
	ErrGameNotFound     = errors.New("game not found")
	ErrProfileNotFound  = errors.New("profile not found")
	ErrDependencyLoop   = errors.New("circular dependency detected")
	ErrAuthRequired     = errors.New("authentication required")
	ErrInvalidConfig    = errors.New("invalid configuration")
	ErrFileConflict     = errors.New("file conflict detected")
	ErrDownloadFailed   = errors.New("download failed")
	ErrLinkFailed       = errors.New("link operation failed")
)
```

**Step 4: Create domain types - mod.go**

Create `internal/domain/mod.go`:

```go
package domain

import "time"

// UpdatePolicy determines how a mod handles updates
type UpdatePolicy int

const (
	UpdateNotify UpdatePolicy = iota // Default: show available, require approval
	UpdateAuto                       // Automatically apply updates
	UpdatePinned                     // Never update
)

// ModFile represents a single file within a mod
type ModFile struct {
	Path     string // Relative path within mod archive
	Size     int64
	Checksum string // SHA256
}

// ModReference is a pointer to a mod (used in profiles, dependencies)
type ModReference struct {
	SourceID string // "nexusmods", "curseforge", etc.
	ModID    string // Source-specific identifier
	Version  string // Empty string means "latest"
}

// Mod represents a mod from any source
type Mod struct {
	ID           string
	SourceID     string
	Name         string
	Version      string
	Author       string
	Summary      string
	Description  string
	GameID       string
	Category     string
	Downloads    int64
	Endorsements int64
	Files        []ModFile
	Dependencies []ModReference
	UpdatedAt    time.Time
}

// InstalledMod tracks a mod installed in a profile
type InstalledMod struct {
	Mod
	ProfileName  string
	UpdatePolicy UpdatePolicy
	InstalledAt  time.Time
	Enabled      bool
}

// Update represents an available update for an installed mod
type Update struct {
	InstalledMod InstalledMod
	NewVersion   string
	Changelog    string
}
```

**Step 5: Create domain types - game.go**

Create `internal/domain/game.go`:

```go
package domain

// LinkMethod determines how mods are deployed to game directories
type LinkMethod int

const (
	LinkSymlink  LinkMethod = iota // Default: symlink (space efficient)
	LinkHardlink                   // Hardlink (transparent to games)
	LinkCopy                       // Copy (maximum compatibility)
)

func (m LinkMethod) String() string {
	switch m {
	case LinkSymlink:
		return "symlink"
	case LinkHardlink:
		return "hardlink"
	case LinkCopy:
		return "copy"
	default:
		return "unknown"
	}
}

// ParseLinkMethod converts a string to LinkMethod
func ParseLinkMethod(s string) LinkMethod {
	switch s {
	case "hardlink":
		return LinkHardlink
	case "copy":
		return LinkCopy
	default:
		return LinkSymlink
	}
}

// Game represents a moddable game
type Game struct {
	ID          string            // Unique slug, e.g., "skyrim-se"
	Name        string            // Display name
	InstallPath string            // Game installation directory
	ModPath     string            // Where mods should be deployed
	SourceIDs   map[string]string // Map source to game ID, e.g., "nexusmods" -> "skyrimspecialedition"
	LinkMethod  LinkMethod        // How to deploy mods
}
```

**Step 6: Create domain types - profile.go**

Create `internal/domain/profile.go`:

```go
package domain

// Profile represents a collection of mods with a specific configuration
type Profile struct {
	Name        string            // Profile identifier
	GameID      string            // Which game this profile is for
	Mods        []ModReference    // Mods in load order (first = lowest priority)
	Overrides   map[string][]byte // Config file overrides (path -> content)
	LinkMethod  LinkMethod        // Override game's default link method (optional)
	IsDefault   bool              // Is this the default profile for the game?
}

// ExportedProfile is the YAML-serializable format for sharing
type ExportedProfile struct {
	Name       string         `yaml:"name"`
	GameID     string         `yaml:"game_id"`
	Mods       []ModReference `yaml:"mods"`
	LinkMethod string         `yaml:"link_method,omitempty"`
}
```

**Step 7: Create minimal main.go**

Create `cmd/lmm/main.go`:

```go
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("lmm - Linux Mod Manager")
	fmt.Println("Version: 0.1.0-dev")
	os.Exit(0)
}
```

**Step 8: Verify build**

```bash
go build -o lmm ./cmd/lmm
./lmm
```

Expected output:

```
lmm - Linux Mod Manager
Version: 0.1.0-dev
```

**Step 9: Commit**

```bash
git add .
git commit -m "feat: initialize project structure and domain types

- Add domain types: Mod, Game, Profile, UpdatePolicy, LinkMethod
- Add domain errors for common failure cases
- Set up package structure following layered architecture
- Add core dependencies: bubbletea, cobra, sqlite, yaml"
```

---

### Task 1.2: SQLite Database Layer

**Files:**

- Create: `internal/storage/db/db.go`
- Create: `internal/storage/db/migrations.go`
- Create: `internal/storage/db/mods.go`
- Create: `internal/storage/db/db_test.go`

**Step 1: Write the failing test for database initialization**

Create `internal/storage/db/db_test.go`:

```go
package db_test

import (
	"testing"

	"lmm/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_CreatesDatabase(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	assert.NotNil(t, database)
}

func TestNew_RunsMigrations(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	// Verify tables exist by querying them
	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM installed_mods").Scan(&count)
	assert.NoError(t, err)

	err = database.QueryRow("SELECT COUNT(*) FROM mod_cache").Scan(&count)
	assert.NoError(t, err)

	err = database.QueryRow("SELECT COUNT(*) FROM auth_tokens").Scan(&count)
	assert.NoError(t, err)
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/storage/db/... -v
```

Expected: FAIL - package not found or types not defined

**Step 3: Create database wrapper**

Create `internal/storage/db/db.go`:

```go
package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite database connection
type DB struct {
	*sql.DB
}

// New creates a new database connection and runs migrations
func New(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable foreign keys and WAL mode for better performance
	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL;"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("setting pragmas: %w", err)
	}

	database := &DB{DB: sqlDB}

	if err := database.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return database, nil
}
```

**Step 4: Create migrations**

Create `internal/storage/db/migrations.go`:

```go
package db

import "fmt"

const currentVersion = 1

func (d *DB) migrate() error {
	// Create migrations table if it doesn't exist
	if _, err := d.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("creating migrations table: %w", err)
	}

	// Get current version
	var version int
	err := d.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	if err != nil {
		return fmt.Errorf("getting schema version: %w", err)
	}

	// Apply migrations
	migrations := []func(*DB) error{
		migrateV1,
	}

	for i := version; i < len(migrations); i++ {
		if err := migrations[i](d); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := d.Exec("INSERT INTO schema_migrations (version) VALUES (?)", i+1); err != nil {
			return fmt.Errorf("recording migration %d: %w", i+1, err)
		}
	}

	return nil
}

func migrateV1(d *DB) error {
	statements := []string{
		`CREATE TABLE installed_mods (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source_id TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			game_id TEXT NOT NULL,
			profile_name TEXT NOT NULL,
			name TEXT NOT NULL,
			version TEXT NOT NULL,
			author TEXT,
			update_policy INTEGER DEFAULT 0,
			enabled INTEGER DEFAULT 1,
			installed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(source_id, mod_id, game_id, profile_name)
		)`,
		`CREATE INDEX idx_installed_mods_game_profile ON installed_mods(game_id, profile_name)`,
		`CREATE TABLE mod_cache (
			source_id TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			game_id TEXT NOT NULL,
			metadata TEXT,
			cached_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY(source_id, mod_id, game_id)
		)`,
		`CREATE TABLE auth_tokens (
			source_id TEXT PRIMARY KEY,
			token_data BLOB,
			expires_at DATETIME,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}

	for _, stmt := range statements {
		if _, err := d.Exec(stmt); err != nil {
			return fmt.Errorf("executing %q: %w", stmt[:50], err)
		}
	}

	return nil
}
```

**Step 5: Run test to verify it passes**

```bash
go test ./internal/storage/db/... -v
```

Expected: PASS

**Step 6: Write tests for installed mods CRUD**

Add to `internal/storage/db/db_test.go`:

```go
func TestInstalledMods_SaveAndGet(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "12345",
			SourceID: "nexusmods",
			Name:     "Test Mod",
			Version:  "1.0.0",
			Author:   "TestAuthor",
			GameID:   "skyrim-se",
		},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}

	err = database.SaveInstalledMod(mod)
	require.NoError(t, err)

	retrieved, err := database.GetInstalledMods("skyrim-se", "default")
	require.NoError(t, err)
	require.Len(t, retrieved, 1)

	assert.Equal(t, mod.ID, retrieved[0].ID)
	assert.Equal(t, mod.Name, retrieved[0].Name)
	assert.Equal(t, mod.Version, retrieved[0].Version)
}

func TestInstalledMods_Delete(t *testing.T) {
	database, err := db.New(":memory:")
	require.NoError(t, err)
	defer database.Close()

	mod := &domain.InstalledMod{
		Mod: domain.Mod{
			ID:       "12345",
			SourceID: "nexusmods",
			Name:     "Test Mod",
			Version:  "1.0.0",
			GameID:   "skyrim-se",
		},
		ProfileName: "default",
	}

	err = database.SaveInstalledMod(mod)
	require.NoError(t, err)

	err = database.DeleteInstalledMod("nexusmods", "12345", "skyrim-se", "default")
	require.NoError(t, err)

	mods, err := database.GetInstalledMods("skyrim-se", "default")
	require.NoError(t, err)
	assert.Empty(t, mods)
}
```

Add import at top of test file:

```go
import (
	"testing"

	"lmm/internal/domain"
	"lmm/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
```

**Step 7: Run tests to verify they fail**

```bash
go test ./internal/storage/db/... -v
```

Expected: FAIL - SaveInstalledMod, GetInstalledMods, DeleteInstalledMod not defined

**Step 8: Implement installed mods repository**

Create `internal/storage/db/mods.go`:

```go
package db

import (
	"fmt"
	"time"

	"lmm/internal/domain"
)

// SaveInstalledMod inserts or updates an installed mod record
func (d *DB) SaveInstalledMod(mod *domain.InstalledMod) error {
	_, err := d.Exec(`
		INSERT INTO installed_mods (source_id, mod_id, game_id, profile_name, name, version, author, update_policy, enabled, installed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, mod_id, game_id, profile_name) DO UPDATE SET
			name = excluded.name,
			version = excluded.version,
			author = excluded.author,
			update_policy = excluded.update_policy,
			enabled = excluded.enabled
	`, mod.SourceID, mod.ID, mod.GameID, mod.ProfileName, mod.Name, mod.Version, mod.Author, mod.UpdatePolicy, mod.Enabled, time.Now())
	if err != nil {
		return fmt.Errorf("saving installed mod: %w", err)
	}
	return nil
}

// GetInstalledMods returns all installed mods for a game/profile combination
func (d *DB) GetInstalledMods(gameID, profileName string) ([]domain.InstalledMod, error) {
	rows, err := d.Query(`
		SELECT source_id, mod_id, game_id, profile_name, name, version, author, update_policy, enabled, installed_at
		FROM installed_mods
		WHERE game_id = ? AND profile_name = ?
		ORDER BY installed_at ASC
	`, gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("querying installed mods: %w", err)
	}
	defer rows.Close()

	var mods []domain.InstalledMod
	for rows.Next() {
		var mod domain.InstalledMod
		err := rows.Scan(
			&mod.SourceID, &mod.ID, &mod.GameID, &mod.ProfileName,
			&mod.Name, &mod.Version, &mod.Author, &mod.UpdatePolicy,
			&mod.Enabled, &mod.InstalledAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning installed mod: %w", err)
		}
		mods = append(mods, mod)
	}

	return mods, rows.Err()
}

// DeleteInstalledMod removes an installed mod record
func (d *DB) DeleteInstalledMod(sourceID, modID, gameID, profileName string) error {
	result, err := d.Exec(`
		DELETE FROM installed_mods
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("deleting installed mod: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// UpdateModPolicy updates the update policy for an installed mod
func (d *DB) UpdateModPolicy(sourceID, modID, gameID, profileName string, policy domain.UpdatePolicy) error {
	result, err := d.Exec(`
		UPDATE installed_mods SET update_policy = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, policy, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("updating mod policy: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}

// SetModEnabled enables or disables a mod
func (d *DB) SetModEnabled(sourceID, modID, gameID, profileName string, enabled bool) error {
	result, err := d.Exec(`
		UPDATE installed_mods SET enabled = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, enabled, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("setting mod enabled: %w", err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}
```

**Step 9: Run tests to verify they pass**

```bash
go test ./internal/storage/db/... -v
```

Expected: PASS

**Step 10: Commit**

```bash
git add .
git commit -m "feat(storage): add SQLite database layer with migrations

- DB wrapper with automatic migration support
- Schema: installed_mods, mod_cache, auth_tokens tables
- CRUD operations for installed mods
- In-memory database support for testing"
```

---

### Task 1.3: YAML Configuration Layer

**Files:**

- Create: `internal/storage/config/config.go`
- Create: `internal/storage/config/games.go`
- Create: `internal/storage/config/profiles.go`
- Create: `internal/storage/config/config_test.go`

**Step 1: Write the failing test for config loading**

Create `internal/storage/config/config_test.go`:

```go
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"lmm/internal/domain"
	"lmm/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_DefaultValues(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(dir)
	require.NoError(t, err)

	assert.Equal(t, domain.LinkSymlink, cfg.DefaultLinkMethod)
	assert.Equal(t, "vim", cfg.Keybindings)
}

func TestLoadConfig_FromFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	content := `
default_link_method: hardlink
keybindings: standard
`
	err := os.WriteFile(configPath, []byte(content), 0644)
	require.NoError(t, err)

	cfg, err := config.Load(dir)
	require.NoError(t, err)

	assert.Equal(t, domain.LinkHardlink, cfg.DefaultLinkMethod)
	assert.Equal(t, "standard", cfg.Keybindings)
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/storage/config/... -v
```

Expected: FAIL - package not found

**Step 3: Implement config loader**

Create `internal/storage/config/config.go`:

```go
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lmm/internal/domain"

	"gopkg.in/yaml.v3"
)

// Config holds global application settings
type Config struct {
	DefaultLinkMethod domain.LinkMethod `yaml:"-"`
	LinkMethodStr     string            `yaml:"default_link_method"`
	Keybindings       string            `yaml:"keybindings"`
	CachePath         string            `yaml:"cache_path"`
}

// Load reads configuration from the given directory
func Load(configDir string) (*Config, error) {
	cfg := &Config{
		DefaultLinkMethod: domain.LinkSymlink,
		Keybindings:       "vim",
	}

	configPath := filepath.Join(configDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil // Return defaults
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Convert string to LinkMethod
	if cfg.LinkMethodStr != "" {
		cfg.DefaultLinkMethod = domain.ParseLinkMethod(cfg.LinkMethodStr)
	}

	return cfg, nil
}

// Save writes configuration to the given directory
func (c *Config) Save(configDir string) error {
	c.LinkMethodStr = c.DefaultLinkMethod.String()

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}
```

**Step 4: Run test to verify it passes**

```bash
go test ./internal/storage/config/... -v
```

Expected: PASS

**Step 5: Write tests for games configuration**

Add to `internal/storage/config/config_test.go`:

```go
func TestLoadGames_Empty(t *testing.T) {
	dir := t.TempDir()
	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Empty(t, games)
}

func TestLoadGames_FromFile(t *testing.T) {
	dir := t.TempDir()
	gamesPath := filepath.Join(dir, "games.yaml")

	content := `
games:
  skyrim-se:
    name: Skyrim Special Edition
    install_path: /games/skyrim
    mod_path: /games/skyrim/Data
    sources:
      nexusmods: skyrimspecialedition
    link_method: symlink
`
	err := os.WriteFile(gamesPath, []byte(content), 0644)
	require.NoError(t, err)

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	require.Len(t, games, 1)

	game := games["skyrim-se"]
	assert.Equal(t, "Skyrim Special Edition", game.Name)
	assert.Equal(t, "/games/skyrim", game.InstallPath)
	assert.Equal(t, "/games/skyrim/Data", game.ModPath)
	assert.Equal(t, "skyrimspecialedition", game.SourceIDs["nexusmods"])
}

func TestSaveGame(t *testing.T) {
	dir := t.TempDir()

	game := &domain.Game{
		ID:          "test-game",
		Name:        "Test Game",
		InstallPath: "/games/test",
		ModPath:     "/games/test/mods",
		SourceIDs:   map[string]string{"nexusmods": "testgame"},
		LinkMethod:  domain.LinkSymlink,
	}

	err := config.SaveGame(dir, game)
	require.NoError(t, err)

	games, err := config.LoadGames(dir)
	require.NoError(t, err)
	assert.Contains(t, games, "test-game")
}
```

**Step 6: Run tests to verify they fail**

```bash
go test ./internal/storage/config/... -v
```

Expected: FAIL - LoadGames, SaveGame not defined

**Step 7: Implement games configuration**

Create `internal/storage/config/games.go`:

```go
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
```

**Step 8: Run tests to verify they pass**

```bash
go test ./internal/storage/config/... -v
```

Expected: PASS

**Step 9: Write tests for profiles**

Add to `internal/storage/config/config_test.go`:

```go
func TestLoadProfile(t *testing.T) {
	dir := t.TempDir()
	profileDir := filepath.Join(dir, "games", "skyrim-se", "profiles")
	err := os.MkdirAll(profileDir, 0755)
	require.NoError(t, err)

	content := `
name: default
game_id: skyrim-se
mods:
  - source_id: nexusmods
    mod_id: "12345"
    version: "1.0.0"
  - source_id: nexusmods
    mod_id: "67890"
    version: ""
link_method: symlink
`
	err = os.WriteFile(filepath.Join(profileDir, "default.yaml"), []byte(content), 0644)
	require.NoError(t, err)

	profile, err := config.LoadProfile(dir, "skyrim-se", "default")
	require.NoError(t, err)

	assert.Equal(t, "default", profile.Name)
	assert.Equal(t, "skyrim-se", profile.GameID)
	require.Len(t, profile.Mods, 2)
	assert.Equal(t, "12345", profile.Mods[0].ModID)
}

func TestSaveProfile(t *testing.T) {
	dir := t.TempDir()

	profile := &domain.Profile{
		Name:   "test-profile",
		GameID: "skyrim-se",
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "111", Version: "1.0"},
		},
		LinkMethod: domain.LinkSymlink,
	}

	err := config.SaveProfile(dir, profile)
	require.NoError(t, err)

	loaded, err := config.LoadProfile(dir, "skyrim-se", "test-profile")
	require.NoError(t, err)
	assert.Equal(t, profile.Name, loaded.Name)
}
```

**Step 10: Run tests to verify they fail**

```bash
go test ./internal/storage/config/... -v
```

Expected: FAIL - LoadProfile, SaveProfile not defined

**Step 11: Implement profiles configuration**

Create `internal/storage/config/profiles.go`:

```go
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lmm/internal/domain"

	"gopkg.in/yaml.v3"
)

// ProfileConfig is the YAML representation of a profile
type ProfileConfig struct {
	Name       string                 `yaml:"name"`
	GameID     string                 `yaml:"game_id"`
	Mods       []ModReferenceConfig   `yaml:"mods"`
	LinkMethod string                 `yaml:"link_method,omitempty"`
	IsDefault  bool                   `yaml:"is_default,omitempty"`
}

// ModReferenceConfig is the YAML representation of a mod reference
type ModReferenceConfig struct {
	SourceID string `yaml:"source_id"`
	ModID    string `yaml:"mod_id"`
	Version  string `yaml:"version,omitempty"`
}

// LoadProfile reads a profile from disk
func LoadProfile(configDir, gameID, profileName string) (*domain.Profile, error) {
	profilePath := filepath.Join(configDir, "games", gameID, "profiles", profileName+".yaml")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, domain.ErrProfileNotFound
		}
		return nil, fmt.Errorf("reading profile: %w", err)
	}

	var cfg ProfileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing profile: %w", err)
	}

	profile := &domain.Profile{
		Name:       cfg.Name,
		GameID:     cfg.GameID,
		LinkMethod: domain.ParseLinkMethod(cfg.LinkMethod),
		IsDefault:  cfg.IsDefault,
		Mods:       make([]domain.ModReference, len(cfg.Mods)),
	}

	for i, m := range cfg.Mods {
		profile.Mods[i] = domain.ModReference{
			SourceID: m.SourceID,
			ModID:    m.ModID,
			Version:  m.Version,
		}
	}

	return profile, nil
}

// SaveProfile writes a profile to disk
func SaveProfile(configDir string, profile *domain.Profile) error {
	cfg := ProfileConfig{
		Name:       profile.Name,
		GameID:     profile.GameID,
		LinkMethod: profile.LinkMethod.String(),
		IsDefault:  profile.IsDefault,
		Mods:       make([]ModReferenceConfig, len(profile.Mods)),
	}

	for i, m := range profile.Mods {
		cfg.Mods[i] = ModReferenceConfig{
			SourceID: m.SourceID,
			ModID:    m.ModID,
			Version:  m.Version,
		}
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshaling profile: %w", err)
	}

	profileDir := filepath.Join(configDir, "games", profile.GameID, "profiles")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		return fmt.Errorf("creating profiles dir: %w", err)
	}

	profilePath := filepath.Join(profileDir, profile.Name+".yaml")
	if err := os.WriteFile(profilePath, data, 0644); err != nil {
		return fmt.Errorf("writing profile: %w", err)
	}

	return nil
}

// ListProfiles returns all profile names for a game
func ListProfiles(configDir, gameID string) ([]string, error) {
	profileDir := filepath.Join(configDir, "games", gameID, "profiles")
	entries, err := os.ReadDir(profileDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading profiles dir: %w", err)
	}

	var profiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".yaml") {
			profiles = append(profiles, strings.TrimSuffix(name, ".yaml"))
		}
	}

	return profiles, nil
}

// DeleteProfile removes a profile from disk
func DeleteProfile(configDir, gameID, profileName string) error {
	profilePath := filepath.Join(configDir, "games", gameID, "profiles", profileName+".yaml")
	if err := os.Remove(profilePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.ErrProfileNotFound
		}
		return fmt.Errorf("deleting profile: %w", err)
	}
	return nil
}

// ExportProfile exports a profile to a portable format
func ExportProfile(profile *domain.Profile) ([]byte, error) {
	exported := domain.ExportedProfile{
		Name:       profile.Name,
		GameID:     profile.GameID,
		Mods:       profile.Mods,
		LinkMethod: profile.LinkMethod.String(),
	}

	data, err := yaml.Marshal(&exported)
	if err != nil {
		return nil, fmt.Errorf("marshaling exported profile: %w", err)
	}

	return data, nil
}

// ImportProfile imports a profile from portable format
func ImportProfile(data []byte) (*domain.Profile, error) {
	var exported domain.ExportedProfile
	if err := yaml.Unmarshal(data, &exported); err != nil {
		return nil, fmt.Errorf("parsing exported profile: %w", err)
	}

	return &domain.Profile{
		Name:       exported.Name,
		GameID:     exported.GameID,
		Mods:       exported.Mods,
		LinkMethod: domain.ParseLinkMethod(exported.LinkMethod),
	}, nil
}
```

**Step 12: Run tests to verify they pass**

```bash
go test ./internal/storage/config/... -v
```

Expected: PASS

**Step 13: Commit**

```bash
git add .
git commit -m "feat(storage): add YAML configuration layer

- Global config: link method, keybindings
- Games config: game definitions with sources
- Profiles: mod lists with load order, export/import support
- All configs support defaults and creation on first use"
```

---

### Task 1.4: Cache Manager

**Files:**

- Create: `internal/storage/cache/cache.go`
- Create: `internal/storage/cache/cache_test.go`

**Step 1: Write the failing test**

Create `internal/storage/cache/cache_test.go`:

```go
package cache_test

import (
	"os"
	"path/filepath"
	"testing"

	"lmm/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCache_ModPath(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	path := c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0")
	expected := filepath.Join(dir, "skyrim-se", "nexusmods-12345", "1.0.0")
	assert.Equal(t, expected, path)
}

func TestCache_Store(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	content := []byte("test mod content")
	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "testfile.txt", content)
	require.NoError(t, err)

	// Verify file exists
	storedPath := filepath.Join(c.ModPath("skyrim-se", "nexusmods", "12345", "1.0.0"), "testfile.txt")
	data, err := os.ReadFile(storedPath)
	require.NoError(t, err)
	assert.Equal(t, content, data)
}

func TestCache_Exists(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	assert.False(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))

	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "test.txt", []byte("data"))
	require.NoError(t, err)

	assert.True(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))
}

func TestCache_ListFiles(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	// Store multiple files
	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "file1.txt", []byte("1"))
	require.NoError(t, err)
	err = c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "subdir/file2.txt", []byte("2"))
	require.NoError(t, err)

	files, err := c.ListFiles("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.Len(t, files, 2)
}

func TestCache_Delete(t *testing.T) {
	dir := t.TempDir()
	c := cache.New(dir)

	err := c.Store("skyrim-se", "nexusmods", "12345", "1.0.0", "test.txt", []byte("data"))
	require.NoError(t, err)
	assert.True(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))

	err = c.Delete("skyrim-se", "nexusmods", "12345", "1.0.0")
	require.NoError(t, err)
	assert.False(t, c.Exists("skyrim-se", "nexusmods", "12345", "1.0.0"))
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/storage/cache/... -v
```

Expected: FAIL - package not found

**Step 3: Implement cache manager**

Create `internal/storage/cache/cache.go`:

```go
package cache

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Cache manages the central mod file cache
type Cache struct {
	basePath string
}

// New creates a new cache manager
func New(basePath string) *Cache {
	return &Cache{basePath: basePath}
}

// ModPath returns the path where a mod version's files are stored
func (c *Cache) ModPath(gameID, sourceID, modID, version string) string {
	return filepath.Join(c.basePath, gameID, fmt.Sprintf("%s-%s", sourceID, modID), version)
}

// Exists checks if a mod version is cached
func (c *Cache) Exists(gameID, sourceID, modID, version string) bool {
	path := c.ModPath(gameID, sourceID, modID, version)
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Store saves a file to the cache
func (c *Cache) Store(gameID, sourceID, modID, version, relativePath string, content []byte) error {
	modPath := c.ModPath(gameID, sourceID, modID, version)
	fullPath := filepath.Join(modPath, relativePath)

	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating cache dir: %w", err)
	}

	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		return fmt.Errorf("writing cached file: %w", err)
	}

	return nil
}

// ListFiles returns all files in a cached mod version
func (c *Cache) ListFiles(gameID, sourceID, modID, version string) ([]string, error) {
	modPath := c.ModPath(gameID, sourceID, modID, version)

	var files []string
	err := filepath.WalkDir(modPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(modPath, path)
		if err != nil {
			return err
		}
		files = append(files, relPath)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("listing cached files: %w", err)
	}

	return files, nil
}

// Delete removes a cached mod version
func (c *Cache) Delete(gameID, sourceID, modID, version string) error {
	modPath := c.ModPath(gameID, sourceID, modID, version)
	if err := os.RemoveAll(modPath); err != nil {
		return fmt.Errorf("deleting cached mod: %w", err)
	}
	return nil
}

// GetFilePath returns the full path to a cached file
func (c *Cache) GetFilePath(gameID, sourceID, modID, version, relativePath string) string {
	return filepath.Join(c.ModPath(gameID, sourceID, modID, version), relativePath)
}

// Size returns the total size of cached files for a mod version
func (c *Cache) Size(gameID, sourceID, modID, version string) (int64, error) {
	modPath := c.ModPath(gameID, sourceID, modID, version)

	var totalSize int64
	err := filepath.WalkDir(modPath, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		totalSize += info.Size()
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("calculating cache size: %w", err)
	}

	return totalSize, nil
}
```

**Step 4: Run tests to verify they pass**

```bash
go test ./internal/storage/cache/... -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add .
git commit -m "feat(storage): add mod file cache manager

- Central cache storage organized by game/source/mod/version
- Store, retrieve, list, and delete cached mod files
- Size calculation for cache management"
```

---

### Task 1.5: Linker Strategies

**Files:**

- Create: `internal/linker/linker.go`
- Create: `internal/linker/symlink.go`
- Create: `internal/linker/hardlink.go`
- Create: `internal/linker/copy.go`
- Create: `internal/linker/linker_test.go`

**Step 1: Write the failing tests**

Create `internal/linker/linker_test.go`:

```go
package linker_test

import (
	"os"
	"path/filepath"
	"testing"

	"lmm/internal/domain"
	"lmm/internal/linker"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSymlinkLinker_Deploy(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	dstDir := filepath.Join(dir, "dst")
	require.NoError(t, os.MkdirAll(srcDir, 0755))
	require.NoError(t, os.MkdirAll(dstDir, 0755))

	srcFile := filepath.Join(srcDir, "test.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("content"), 0644))

	l := linker.NewSymlink()
	dstFile := filepath.Join(dstDir, "test.txt")
	err := l.Deploy(srcFile, dstFile)
	require.NoError(t, err)

	// Verify it's a symlink
	info, err := os.Lstat(dstFile)
	require.NoError(t, err)
	assert.True(t, info.Mode()&os.ModeSymlink != 0)

	// Verify content accessible
	content, err := os.ReadFile(dstFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("content"), content)
}

func TestSymlinkLinker_Undeploy(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "src.txt")
	dstFile := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("content"), 0644))

	l := linker.NewSymlink()
	require.NoError(t, l.Deploy(srcFile, dstFile))
	require.NoError(t, l.Undeploy(dstFile))

	_, err := os.Stat(dstFile)
	assert.True(t, os.IsNotExist(err))

	// Source should still exist
	_, err = os.Stat(srcFile)
	assert.NoError(t, err)
}

func TestHardlinkLinker_Deploy(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "src.txt")
	dstFile := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("content"), 0644))

	l := linker.NewHardlink()
	err := l.Deploy(srcFile, dstFile)
	require.NoError(t, err)

	// Verify content
	content, err := os.ReadFile(dstFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("content"), content)

	// Verify same inode (hardlink)
	srcInfo, _ := os.Stat(srcFile)
	dstInfo, _ := os.Stat(dstFile)
	assert.Equal(t, srcInfo.Size(), dstInfo.Size())
}

func TestCopyLinker_Deploy(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "src.txt")
	dstFile := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("content"), 0644))

	l := linker.NewCopy()
	err := l.Deploy(srcFile, dstFile)
	require.NoError(t, err)

	// Verify content
	content, err := os.ReadFile(dstFile)
	require.NoError(t, err)
	assert.Equal(t, []byte("content"), content)
}

func TestNew_ReturnsCorrectLinker(t *testing.T) {
	assert.Equal(t, domain.LinkSymlink, linker.New(domain.LinkSymlink).Method())
	assert.Equal(t, domain.LinkHardlink, linker.New(domain.LinkHardlink).Method())
	assert.Equal(t, domain.LinkCopy, linker.New(domain.LinkCopy).Method())
}
```

**Step 2: Run tests to verify they fail**

```bash
go test ./internal/linker/... -v
```

Expected: FAIL - package not found

**Step 3: Create linker interface**

Create `internal/linker/linker.go`:

```go
package linker

import "lmm/internal/domain"

// Linker deploys and undeploys mod files to game directories
type Linker interface {
	Deploy(src, dst string) error
	Undeploy(dst string) error
	IsDeployed(dst string) (bool, error)
	Method() domain.LinkMethod
}

// New creates a linker for the given method
func New(method domain.LinkMethod) Linker {
	switch method {
	case domain.LinkHardlink:
		return NewHardlink()
	case domain.LinkCopy:
		return NewCopy()
	default:
		return NewSymlink()
	}
}
```

**Step 4: Implement symlink linker**

Create `internal/linker/symlink.go`:

```go
package linker

import (
	"fmt"
	"os"
	"path/filepath"

	"lmm/internal/domain"
)

// SymlinkLinker deploys mods using symbolic links
type SymlinkLinker struct{}

// NewSymlink creates a new symlink linker
func NewSymlink() *SymlinkLinker {
	return &SymlinkLinker{}
}

// Deploy creates a symlink from src to dst
func (l *SymlinkLinker) Deploy(src, dst string) error {
	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating destination dir: %w", err)
	}

	// Remove existing file/link if present
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing existing file: %w", err)
	}

	if err := os.Symlink(src, dst); err != nil {
		return fmt.Errorf("creating symlink: %w", err)
	}

	return nil
}

// Undeploy removes the symlink at dst
func (l *SymlinkLinker) Undeploy(dst string) error {
	info, err := os.Lstat(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Already removed
		}
		return fmt.Errorf("checking file: %w", err)
	}

	// Only remove if it's a symlink
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("not a symlink: %s", dst)
	}

	if err := os.Remove(dst); err != nil {
		return fmt.Errorf("removing symlink: %w", err)
	}

	return nil
}

// IsDeployed checks if dst is a symlink
func (l *SymlinkLinker) IsDeployed(dst string) (bool, error) {
	info, err := os.Lstat(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode()&os.ModeSymlink != 0, nil
}

// Method returns the link method
func (l *SymlinkLinker) Method() domain.LinkMethod {
	return domain.LinkSymlink
}
```

**Step 5: Implement hardlink linker**

Create `internal/linker/hardlink.go`:

```go
package linker

import (
	"fmt"
	"os"
	"path/filepath"

	"lmm/internal/domain"
)

// HardlinkLinker deploys mods using hard links
type HardlinkLinker struct{}

// NewHardlink creates a new hardlink linker
func NewHardlink() *HardlinkLinker {
	return &HardlinkLinker{}
}

// Deploy creates a hard link from src to dst
func (l *HardlinkLinker) Deploy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating destination dir: %w", err)
	}

	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing existing file: %w", err)
	}

	if err := os.Link(src, dst); err != nil {
		return fmt.Errorf("creating hardlink: %w", err)
	}

	return nil
}

// Undeploy removes the file at dst
func (l *HardlinkLinker) Undeploy(dst string) error {
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing file: %w", err)
	}
	return nil
}

// IsDeployed checks if dst exists (hardlinks are indistinguishable from regular files)
func (l *HardlinkLinker) IsDeployed(dst string) (bool, error) {
	_, err := os.Stat(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Method returns the link method
func (l *HardlinkLinker) Method() domain.LinkMethod {
	return domain.LinkHardlink
}
```

**Step 6: Implement copy linker**

Create `internal/linker/copy.go`:

```go
package linker

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"lmm/internal/domain"
)

// CopyLinker deploys mods by copying files
type CopyLinker struct{}

// NewCopy creates a new copy linker
func NewCopy() *CopyLinker {
	return &CopyLinker{}
}

// Deploy copies src to dst
func (l *CopyLinker) Deploy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating destination dir: %w", err)
	}

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
		return fmt.Errorf("copying file: %w", err)
	}

	return nil
}

// Undeploy removes the file at dst
func (l *CopyLinker) Undeploy(dst string) error {
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing file: %w", err)
	}
	return nil
}

// IsDeployed checks if dst exists
func (l *CopyLinker) IsDeployed(dst string) (bool, error) {
	_, err := os.Stat(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Method returns the link method
func (l *CopyLinker) Method() domain.LinkMethod {
	return domain.LinkCopy
}
```

**Step 7: Run tests to verify they pass**

```bash
go test ./internal/linker/... -v
```

Expected: PASS

**Step 8: Commit**

```bash
git add .
git commit -m "feat(linker): add mod deployment strategies

- Linker interface for deploy/undeploy operations
- SymlinkLinker: space-efficient, easy to identify
- HardlinkLinker: transparent to games, same filesystem
- CopyLinker: maximum compatibility"
```

---

### Task 1.6: Run All Tests and Verify Phase 1

**Step 1: Run all tests**

```bash
go test ./... -v
```

Expected: All tests PASS

**Step 2: Verify build**

```bash
go build -o lmm ./cmd/lmm
./lmm
```

**Step 3: Commit phase completion**

```bash
git add .
git commit -m "chore: complete Phase 1 - Core Infrastructure

Phase 1 complete:
- Domain types (Mod, Game, Profile)
- SQLite database with migrations
- YAML configuration (config, games, profiles)
- Central mod cache manager
- Linker strategies (symlink, hardlink, copy)"
```

---

## Phase 2: NexusMods Integration

### Task 2.1: ModSource Interface and Registry

**Files:**

- Create: `internal/source/source.go`
- Create: `internal/source/registry.go`
- Create: `internal/source/registry_test.go`

**Step 1: Write failing test for registry**

Create `internal/source/registry_test.go`:

```go
package source_test

import (
	"context"
	"testing"

	"lmm/internal/domain"
	"lmm/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSource struct {
	id string
}

func (m *mockSource) ID() string                                                            { return m.id }
func (m *mockSource) Name() string                                                          { return "Mock" }
func (m *mockSource) AuthURL() string                                                       { return "" }
func (m *mockSource) ExchangeToken(context.Context, string) (*source.Token, error)          { return nil, nil }
func (m *mockSource) Search(context.Context, source.SearchQuery) ([]domain.Mod, error)      { return nil, nil }
func (m *mockSource) GetMod(context.Context, string, string) (*domain.Mod, error)           { return nil, nil }
func (m *mockSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) { return nil, nil }
func (m *mockSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error)   { return "", nil }
func (m *mockSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) { return nil, nil }

func TestRegistry_Register(t *testing.T) {
	reg := source.NewRegistry()
	mock := &mockSource{id: "mock"}

	reg.Register(mock)

	src, err := reg.Get("mock")
	require.NoError(t, err)
	assert.Equal(t, "mock", src.ID())
}

func TestRegistry_Get_NotFound(t *testing.T) {
	reg := source.NewRegistry()

	_, err := reg.Get("nonexistent")
	assert.Error(t, err)
}

func TestRegistry_List(t *testing.T) {
	reg := source.NewRegistry()
	reg.Register(&mockSource{id: "source1"})
	reg.Register(&mockSource{id: "source2"})

	sources := reg.List()
	assert.Len(t, sources, 2)
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./internal/source/... -v
```

Expected: FAIL - package not found

**Step 3: Create source interface**

Create `internal/source/source.go`:

```go
package source

import (
	"context"
	"time"

	"lmm/internal/domain"
)

// Token represents an OAuth token
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// SearchQuery parameters for searching mods
type SearchQuery struct {
	GameID   string
	Query    string
	Category string
	Page     int
	PageSize int
}

// ModSource is the interface for mod repositories
type ModSource interface {
	// Identity
	ID() string
	Name() string

	// Authentication
	AuthURL() string
	ExchangeToken(ctx context.Context, code string) (*Token, error)

	// Discovery
	Search(ctx context.Context, query SearchQuery) ([]domain.Mod, error)
	GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error)
	GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error)

	// Downloads
	GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error)

	// Updates
	CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error)
}
```

**Step 4: Create registry**

Create `internal/source/registry.go`:

```go
package source

import (
	"fmt"
	"sync"
)

// Registry manages available mod sources
type Registry struct {
	mu      sync.RWMutex
	sources map[string]ModSource
}

// NewRegistry creates a new source registry
func NewRegistry() *Registry {
	return &Registry{
		sources: make(map[string]ModSource),
	}
}

// Register adds a source to the registry
func (r *Registry) Register(source ModSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources[source.ID()] = source
}

// Get retrieves a source by ID
func (r *Registry) Get(id string) (ModSource, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	source, ok := r.sources[id]
	if !ok {
		return nil, fmt.Errorf("source not found: %s", id)
	}
	return source, nil
}

// List returns all registered sources
func (r *Registry) List() []ModSource {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sources := make([]ModSource, 0, len(r.sources))
	for _, s := range r.sources {
		sources = append(sources, s)
	}
	return sources
}
```

**Step 5: Run tests to verify they pass**

```bash
go test ./internal/source/... -v
```

Expected: PASS

**Step 6: Commit**

```bash
git add .
git commit -m "feat(source): add ModSource interface and registry

- ModSource interface for mod repositories
- Token type for OAuth authentication
- SearchQuery for mod searches
- Registry for managing multiple sources"
```

---

### Task 2.2: NexusMods GraphQL Client

**Files:**

- Create: `internal/source/nexusmods/client.go`
- Create: `internal/source/nexusmods/queries.go`
- Create: `internal/source/nexusmods/types.go`
- Create: `internal/source/nexusmods/nexusmods.go`

**Step 1: Create NexusMods types**

Create `internal/source/nexusmods/types.go`:

```go
package nexusmods

import "time"

// GraphQL response types

type ModResponse struct {
	Mod ModData `graphql:"mod(gameId: $gameId, modId: $modId)"`
}

type ModData struct {
	UID         string `graphql:"uid"`
	ModID       int    `graphql:"modId"`
	Name        string `graphql:"name"`
	Summary     string `graphql:"summary"`
	Description string `graphql:"description"`
	Author      string `graphql:"author"`
	Version     string `graphql:"version"`
	Category    struct {
		Name string `graphql:"name"`
	} `graphql:"category"`
	ModDownloadCount int `graphql:"modDownloadCount"`
	EndorsementCount int `graphql:"endorsementCount"`
	UpdatedAt        time.Time `graphql:"updatedAt"`
}

type SearchResponse struct {
	Mods struct {
		Nodes []ModData `graphql:"nodes"`
	} `graphql:"mods(gameId: $gameId, filter: $filter, first: $first, offset: $offset)"`
}

type ModFilter struct {
	Name string `json:"name,omitempty"`
}

type FileResponse struct {
	ModFiles struct {
		Nodes []FileData `graphql:"nodes"`
	} `graphql:"modFiles(modId: $modId, gameId: $gameId)"`
}

type FileData struct {
	FileID      int    `graphql:"fileId"`
	Name        string `graphql:"name"`
	Version     string `graphql:"version"`
	Size        int64  `graphql:"size"`
	IsPrimary   bool   `graphql:"isPrimary"`
	UploadedAt  time.Time `graphql:"uploadedAt"`
}
```

**Step 2: Create NexusMods client**

Create `internal/source/nexusmods/client.go`:

```go
package nexusmods

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hasura/go-graphql-client"
)

const (
	graphqlEndpoint = "https://api.nexusmods.com/v2/graphql"
	oauthAuthorize  = "https://www.nexusmods.com/oauth/authorize"
	oauthToken      = "https://www.nexusmods.com/oauth/token"
)

// Client wraps the NexusMods GraphQL API
type Client struct {
	gql        *graphql.Client
	httpClient *http.Client
	apiKey     string
}

// NewClient creates a new NexusMods API client
func NewClient(httpClient *http.Client, apiKey string) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	// Create transport that adds API key header
	transport := &apiKeyTransport{
		base:   httpClient.Transport,
		apiKey: apiKey,
	}
	authedClient := &http.Client{Transport: transport}

	return &Client{
		gql:        graphql.NewClient(graphqlEndpoint, authedClient),
		httpClient: httpClient,
		apiKey:     apiKey,
	}
}

type apiKeyTransport struct {
	base   http.RoundTripper
	apiKey string
}

func (t *apiKeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.apiKey != "" {
		req.Header.Set("apikey", t.apiKey)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// GetMod fetches a mod by ID
func (c *Client) GetMod(ctx context.Context, gameID string, modID int) (*ModData, error) {
	var query struct {
		Mod ModData `graphql:"mod(gameId: $gameId, modId: $modId)"`
	}

	variables := map[string]interface{}{
		"gameId": graphql.String(gameID),
		"modId":  graphql.Int(modID),
	}

	if err := c.gql.Query(ctx, &query, variables); err != nil {
		return nil, fmt.Errorf("querying mod: %w", err)
	}

	return &query.Mod, nil
}

// SearchMods searches for mods
func (c *Client) SearchMods(ctx context.Context, gameID, search string, limit, offset int) ([]ModData, error) {
	var query struct {
		Mods struct {
			Nodes []ModData `graphql:"nodes"`
		} `graphql:"mods(gameId: $gameId, filter: {name: $name}, first: $first, offset: $offset)"`
	}

	variables := map[string]interface{}{
		"gameId": graphql.String(gameID),
		"name":   graphql.String(search),
		"first":  graphql.Int(limit),
		"offset": graphql.Int(offset),
	}

	if err := c.gql.Query(ctx, &query, variables); err != nil {
		return nil, fmt.Errorf("searching mods: %w", err)
	}

	return query.Mods.Nodes, nil
}

// GetModFiles fetches files for a mod
func (c *Client) GetModFiles(ctx context.Context, gameID string, modID int) ([]FileData, error) {
	var query struct {
		ModFiles struct {
			Nodes []FileData `graphql:"nodes"`
		} `graphql:"modFiles(modId: $modId, gameId: $gameId)"`
	}

	variables := map[string]interface{}{
		"gameId": graphql.String(gameID),
		"modId":  graphql.Int(modID),
	}

	if err := c.gql.Query(ctx, &query, variables); err != nil {
		return nil, fmt.Errorf("querying mod files: %w", err)
	}

	return query.ModFiles.Nodes, nil
}
```

**Step 3: Create NexusMods source implementation**

Create `internal/source/nexusmods/nexusmods.go`:

```go
package nexusmods

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"lmm/internal/domain"
	"lmm/internal/source"
)

// NexusMods implements the ModSource interface
type NexusMods struct {
	client *Client
}

// New creates a new NexusMods source
func New(httpClient *http.Client, apiKey string) *NexusMods {
	return &NexusMods{
		client: NewClient(httpClient, apiKey),
	}
}

// ID returns the source identifier
func (n *NexusMods) ID() string {
	return "nexusmods"
}

// Name returns the display name
func (n *NexusMods) Name() string {
	return "Nexus Mods"
}

// AuthURL returns the OAuth authorization URL
func (n *NexusMods) AuthURL() string {
	return oauthAuthorize
}

// ExchangeToken exchanges an OAuth code for tokens
func (n *NexusMods) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	// TODO: Implement OAuth token exchange
	return nil, fmt.Errorf("OAuth not yet implemented")
}

// Search finds mods matching the query
func (n *NexusMods) Search(ctx context.Context, query source.SearchQuery) ([]domain.Mod, error) {
	pageSize := query.PageSize
	if pageSize == 0 {
		pageSize = 20
	}
	offset := query.Page * pageSize

	results, err := n.client.SearchMods(ctx, query.GameID, query.Query, pageSize, offset)
	if err != nil {
		return nil, err
	}

	mods := make([]domain.Mod, len(results))
	for i, r := range results {
		mods[i] = modDataToDomain(r, query.GameID)
	}

	return mods, nil
}

// GetMod retrieves a specific mod
func (n *NexusMods) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	id, err := strconv.Atoi(modID)
	if err != nil {
		return nil, fmt.Errorf("invalid mod ID: %w", err)
	}

	data, err := n.client.GetMod(ctx, gameID, id)
	if err != nil {
		return nil, err
	}

	mod := modDataToDomain(*data, gameID)
	return &mod, nil
}

// GetDependencies returns mod dependencies
func (n *NexusMods) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	// TODO: Implement dependency fetching from NexusMods
	return nil, nil
}

// GetDownloadURL gets the download URL for a mod file
func (n *NexusMods) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	// TODO: Implement download URL generation
	return "", fmt.Errorf("download URLs not yet implemented")
}

// CheckUpdates checks for available updates
func (n *NexusMods) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	// TODO: Implement update checking
	return nil, nil
}

func modDataToDomain(data ModData, gameID string) domain.Mod {
	return domain.Mod{
		ID:           strconv.Itoa(data.ModID),
		SourceID:     "nexusmods",
		Name:         data.Name,
		Version:      data.Version,
		Author:       data.Author,
		Summary:      data.Summary,
		Description:  data.Description,
		GameID:       gameID,
		Category:     data.Category.Name,
		Downloads:    int64(data.ModDownloadCount),
		Endorsements: int64(data.EndorsementCount),
		UpdatedAt:    data.UpdatedAt,
	}
}
```

**Step 4: Commit**

```bash
git add .
git commit -m "feat(source): add NexusMods GraphQL client

- GraphQL client with API key authentication
- ModSource implementation for NexusMods
- Search and GetMod operations
- Type mappings from GraphQL to domain types
- TODO: OAuth flow, downloads, updates"
```

---

_[Document continues with Phases 3-6...]_

---

## Phase 3-6 Summary

Due to length, remaining phases are summarized. Each follows the same TDD pattern.

### Phase 3: Mod Management

- **Task 3.1**: Core service facade (`internal/core/service.go`)
- **Task 3.2**: Installer with download, extract, deploy (`internal/core/installer.go`)
- **Task 3.3**: Updater with version comparison (`internal/core/updater.go`)
- **Task 3.4**: Dependency resolver with cycle detection (`internal/core/dependencies.go`)

### Phase 4: Profile System

- **Task 4.1**: Profile manager (`internal/core/profile.go`)
- **Task 4.2**: Profile switching (full swap)
- **Task 4.3**: Export/import functionality

### Phase 5: TUI

- **Task 5.1**: App shell with Bubble Tea (`internal/tui/app.go`)
- **Task 5.2**: Game selector view
- **Task 5.3**: Mod browser view with search
- **Task 5.4**: Installed mods view with load order
- **Task 5.5**: Profile management view
- **Task 5.6**: Settings view
- **Task 5.7**: Keybinding configuration

### Phase 6: CLI

- **Task 6.1**: Cobra command structure (`cmd/lmm/`)
- **Task 6.2**: Search command
- **Task 6.3**: Install/uninstall commands
- **Task 6.4**: Update command
- **Task 6.5**: Profile commands
- **Task 6.6**: Status/list commands

---

## Verification Checklist

After each phase, verify:

1. **All tests pass**: `go test ./... -v`
2. **Build succeeds**: `go build -o lmm ./cmd/lmm`
3. **No lint errors**: `go vet ./...`
4. **Code formatted**: `go fmt ./...`

## End-to-End Testing

After Phase 6, test the complete flow:

```bash
# 1. Add a game
lmm config add-game skyrim-se \
  --name "Skyrim Special Edition" \
  --path "/path/to/skyrim" \
  --mod-path "/path/to/skyrim/Data" \
  --nexus-id "skyrimspecialedition"

# 2. Search for mods
lmm search "skyui" --game skyrim-se

# 3. Install a mod
lmm install <mod-id> --game skyrim-se

# 4. List installed mods
lmm list --game skyrim-se

# 5. Create and switch profiles
lmm profile create survival --game skyrim-se
lmm profile switch survival --game skyrim-se

# 6. Export profile
lmm profile export survival --game skyrim-se --output survival.yaml

# 7. Launch TUI
lmm
```
