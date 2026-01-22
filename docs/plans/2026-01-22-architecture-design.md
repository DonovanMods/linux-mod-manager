# lmm Architecture Design

## Overview

Layered monolith architecture with interface-based extensibility for mod sources.

## Project Structure

```
lmm/
├── cmd/lmm/main.go           # Entry point, CLI parsing (cobra)
├── internal/
│   ├── domain/               # Core types (Mod, Profile, Game) - no deps
│   ├── source/               # ModSource interface + implementations
│   │   ├── source.go         # Interface definition
│   │   ├── registry.go       # Source registry
│   │   └── nexusmods/        # NexusMods GraphQL client
│   ├── storage/
│   │   ├── db/               # SQLite (mod metadata, auth tokens)
│   │   ├── config/           # YAML parsing (games, profiles)
│   │   └── cache/            # Central mod file cache
│   ├── linker/               # Deploy strategies (symlink, hardlink, copy)
│   ├── core/                 # Business logic orchestration
│   │   ├── service.go        # Main service facade
│   │   ├── installer.go      # Install/uninstall operations
│   │   ├── updater.go        # Update checking & application
│   │   └── profile.go        # Profile switching logic
│   └── tui/                  # Bubble Tea application
│       ├── app.go            # Main model, routing
│       └── views/            # Individual screens
└── docs/
```

## Core Domain Types

```go
// domain/mod.go
type Mod struct {
    ID           string
    SourceID     string            // "nexusmods", "curseforge", etc.
    Name         string
    Version      string
    Author       string
    Description  string
    GameID       string
    Files        []ModFile
    Dependencies []ModReference
    UpdatePolicy UpdatePolicy
}

type ModReference struct {
    SourceID string
    ModID    string
    Version  string  // Empty = latest
}

type UpdatePolicy int
const (
    UpdateNotify UpdatePolicy = iota  // Default
    UpdateAuto
    UpdatePinned
)

// domain/profile.go
type Profile struct {
    Name       string
    GameID     string
    Mods       []ModReference    // In load order
    Overrides  map[string][]byte // Config file overrides
    LinkMethod LinkMethod
}

// domain/game.go
type Game struct {
    ID          string
    Name        string
    InstallPath string
    ModPath     string
    SourceIDs   map[string]string // e.g., "nexusmods" -> "skyrimspecialedition"
}
```

## ModSource Interface

```go
type ModSource interface {
    ID() string
    Name() string

    // Auth
    AuthURL() string
    ExchangeToken(ctx context.Context, code string) (*Token, error)

    // Discovery
    Search(ctx context.Context, q SearchQuery) ([]domain.Mod, error)
    GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error)
    GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error)

    // Downloads
    GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error)

    // Updates
    CheckUpdates(ctx context.Context, installed []InstalledMod) ([]Update, error)
}

type SearchQuery struct {
    GameID   string
    Query    string
    Category string
    Page     int
    PageSize int
}
```

Sources registered at startup:

```go
sources := source.NewRegistry()
sources.Register(nexusmods.New(db, httpClient))
```

## Storage

### SQLite Schema (internal state)

```sql
CREATE TABLE installed_mods (
    id INTEGER PRIMARY KEY,
    source_id TEXT NOT NULL,
    mod_id TEXT NOT NULL,
    game_id TEXT NOT NULL,
    profile_name TEXT NOT NULL,
    version TEXT NOT NULL,
    update_policy INTEGER DEFAULT 0,
    installed_at DATETIME,
    UNIQUE(source_id, mod_id, game_id, profile_name)
);

CREATE TABLE mod_cache (
    source_id TEXT,
    mod_id TEXT,
    game_id TEXT,
    metadata BLOB,
    cached_at DATETIME,
    PRIMARY KEY(source_id, mod_id, game_id)
);

CREATE TABLE auth_tokens (
    source_id TEXT PRIMARY KEY,
    token_data BLOB  -- Encrypted
);
```

Location: `~/.local/share/lmm/lmm.db`

### YAML Config (user-editable)

```
~/.config/lmm/
├── config.yaml          # Global settings
├── games.yaml           # Game configurations
└── games/<game-id>/
    └── profiles/
        └── <profile>.yaml
```

### Cache Structure

```
~/.local/share/lmm/cache/<game-id>/<source-id>-<mod-id>/<version>/files/
```

## Linker Strategies

```go
type Linker interface {
    Deploy(src, dst string) error
    Undeploy(dst string) error
    IsDeployed(dst string) (bool, error)
    Method() domain.LinkMethod
}
```

Implementations: `SymlinkLinker`, `HardlinkLinker`, `CopyLinker`

## TUI Architecture (Bubble Tea)

Elm architecture: Model → Update → View

```go
type App struct {
    currentView View
    game        *domain.Game
    profile     *domain.Profile
    core        *core.Service
    sources     *source.Registry
    width, height int
}

type View int
const (
    ViewDashboard View = iota
    ViewGameSelect
    ViewModBrowser
    ViewInstalled
    ViewProfiles
    ViewSettings
)
```

Each view is its own Bubble Tea model with Update/View methods.

Keybindings configurable: vim-style (hjkl) or standard (arrows).

## CLI Commands (Cobra)

```
lmm                           # Launch TUI (default)
lmm search <query> --game X   # Search mods
lmm install <mod-id> --game X # Install mod
lmm update [mod-id] --game X  # Check/apply updates
lmm list --game X             # List installed
lmm profile switch|export|import
lmm config set|get
lmm auth login|logout --source X
```

CLI shares `core.Service` with TUI.

## Error Handling

- Wrap errors with context: `fmt.Errorf("downloading mod %s: %w", modID, err)`
- Domain errors: `ErrModNotFound`, `ErrDependencyLoop`, `ErrAuthRequired`
- Retry with exponential backoff for network operations

## Testing Strategy

| Layer       | Approach                             |
| ----------- | ------------------------------------ |
| Domain      | Pure unit tests, no mocks            |
| Source      | Mock HTTP client, recorded responses |
| Storage     | In-memory SQLite, temp directories   |
| Core        | Mock sources/storage, test logic     |
| TUI         | Limited (complex to test)            |
| Integration | End-to-end with mock source          |

## Key Dependencies

- `github.com/charmbracelet/bubbletea` - TUI framework
- `github.com/charmbracelet/bubbles` - TUI components
- `github.com/spf13/cobra` - CLI framework
- `github.com/hasura/go-graphql-client` - GraphQL client
- `modernc.org/sqlite` - Pure Go SQLite
- `gopkg.in/yaml.v3` - YAML parsing
- `github.com/avast/retry-go` - Retry logic
