# Linux Mod Manager (lmm) - Product Requirements Document

## Overview

**lmm** is a terminal-based mod manager for Linux that provides a unified interface for searching, installing, updating, and managing game mods from various sources. The initial version focuses on NexusMods integration with an architecture designed to support additional mod sources (ESOUI, CurseForge, etc.) in the future.

## Goals

1. Provide Linux gamers with a native, efficient mod management experience
2. Support any game through a game-agnostic design
3. Enable mod organization through profiles with shareable mod lists
4. Minimize disk usage through intelligent caching and linking strategies
5. Offer both TUI and CLI interfaces for different use cases

## Target Users

- **Primary**: Linux gamers who mod their games and prefer terminal-based tools
- **Secondary**: Power users who want scriptable mod management
- **Skill Level**: Both general Linux users (sensible defaults, guidance) and power users (keyboard shortcuts, efficiency)

---

## Core Features

### 1. Mod Source Integration

#### NexusMods (MVP)

- OAuth authentication flow via browser redirect
- GraphQL API integration (https://graphql.nexusmods.com/)
- Search mods by game, name, category, tags
- View mod details, descriptions, images, changelogs
- Download files with automatic retry on failure

#### Future Sources

- ESOUI (Elder Scrolls Online addons)
- CurseForge
- Steam Workshop (where applicable)
- Manual/local mod imports

### 2. Mod Lifecycle Management

| Action             | Description                                                                         |
| ------------------ | ----------------------------------------------------------------------------------- |
| **Search**         | Query mod sources by name, category, tags                                           |
| **Install**        | Download and deploy mod to game directory                                           |
| **Update**         | Check for and apply mod updates (manual trigger, with optional auto-update per mod) |
| **Uninstall**      | Remove mod and clean up links/files                                                 |
| **Enable/Disable** | Toggle mod without removing                                                         |

### 3. Storage & Linking System

#### Central Cache

- Location: `~/.local/share/lmm/cache/<game-slug>/`
- All downloaded mods stored centrally, organized by game
- Deduplication across profiles

#### Deployment Strategies

User-configurable linking method per game:

| Method                | Pros                                  | Cons                             |
| --------------------- | ------------------------------------- | -------------------------------- |
| **Symlink** (default) | Space efficient, easy to identify     | Some games don't follow symlinks |
| **Hardlink**          | Space efficient, transparent to games | Same filesystem required         |
| **Copy**              | Maximum compatibility                 | Uses more disk space             |

### 4. Profile System

#### Profile Features

- Named collections of mods with specific load order
- Per-profile configuration overrides (INI tweaks, etc.)
- Full swap when switching (unlink current, link new)
- Default profile per game

#### Profile Portability

- Export as YAML file containing:
  - Mod references (source, ID, version)
  - Load order
  - Configuration overrides
  - Linking preferences
- Import recreates profile, fetching mods from sources
- Shareable mod lists for community collaboration

### 5. Game Configuration

#### Auto-Detection

Scan common installation locations:

- Steam (`~/.steam/steam/steamapps/common/`)
- Lutris (`~/Games/`)
- Heroic (`~/Games/Heroic/`)
- Flatpak variants
- Custom paths from environment variables

#### Manual Configuration

- Add games manually with custom paths
- Override auto-detected settings
- Specify mod directory structure per game

#### Game Configuration File (YAML)

```yaml
games:
  skyrim-se:
    name: "Skyrim Special Edition"
    install_path: "~/.steam/steam/steamapps/common/Skyrim Special Edition"
    mod_path: "~/.steam/steam/steamapps/common/Skyrim Special Edition/Data"
    nexus_game_id: "skyrimspecialedition"
    link_method: symlink
    profiles:
      - default
      - survival-modpack
```

### 6. Dependency Management

- Automatic dependency resolution
- Fetch and install required mods automatically
- Warn on circular dependencies
- Show dependency tree before installation

### 7. Conflict Resolution

- Load order determines file priority (later wins)
- Visual indicator for conflicting files
- Ability to reorder mods to resolve conflicts
- Per-file override capability (future enhancement)

### 8. Update Management

#### Update Modes

| Mode                 | Description                                                     |
| -------------------- | --------------------------------------------------------------- |
| **Manual** (default) | User triggers update check and manually approves each update    |
| **Auto-update**      | Mods marked as "auto-update" are automatically updated on check |

#### Features

- User-triggered update check (not background/automatic)
- Display available updates with changelogs
- Batch update capability
- Per-mod update preferences:
  - **Auto-update**: Automatically apply updates when checking
  - **Notify only**: Show available update, require manual approval
  - **Pin version**: Never update this mod automatically
- Rollback capability: restore previous mod version if update causes issues

---

## User Interfaces

### TUI (Primary)

Built with a modern Go TUI framework (e.g., Bubble Tea).

#### Navigation

Configurable keybindings supporting both styles:

- **Vim-like**: `hjkl` navigation, `/` search, modal operations
- **Standard**: Arrow keys, Enter to select, Escape to cancel

#### Main Views

1. **Dashboard**: Overview of installed mods, updates available, recent activity
2. **Game Selector**: List of configured games
3. **Mod Browser**: Search and browse mods from sources
4. **Installed Mods**: Manage installed mods, load order
5. **Profiles**: Create, edit, switch, export/import profiles
6. **Settings**: Configure application preferences

#### Visual Elements

- Progress indicators for downloads
- Color-coded status (installed, update available, conflicts)
- Mod thumbnails where terminal supports (Kitty, iTerm2)

### CLI (Secondary)

Basic commands for scripting and quick operations:

```bash
# Search
lmm search "skyui" --game skyrim-se

# Install
lmm install <mod-id> --game skyrim-se --profile default

# Update
lmm update --game skyrim-se              # Check all, apply auto-updates
lmm update <mod-id> --game skyrim-se     # Update specific mod
lmm update --game skyrim-se --dry-run    # Show available updates without applying

# Configure mod update behavior
lmm mod set-update <mod-id> --auto       # Enable auto-update for mod
lmm mod set-update <mod-id> --notify     # Notify only (default)
lmm mod set-update <mod-id> --pin        # Pin to current version

# List
lmm list --game skyrim-se
lmm list --profiles --game skyrim-se

# Profile management
lmm profile switch <name> --game skyrim-se
lmm profile export <name> --output profile.yaml
lmm profile import profile.yaml --game skyrim-se

# Status
lmm status --game skyrim-se
```

---

## Data Architecture

### SQLite Database

Internal state and metadata:

- Mod cache metadata (source, ID, version, files)
- Installation records
- Download history
- Authentication tokens (encrypted)

Location: `~/.local/share/lmm/lmm.db`

### YAML Configuration Files

User-editable configuration:

```
~/.config/lmm/
├── config.yaml          # Global settings
├── games.yaml           # Game configurations
└── games/
    └── <game-slug>/
        ├── profiles/
        │   ├── default.yaml
        │   └── custom-profile.yaml
        └── overrides/    # Per-profile config files
```

### Cache Structure

```
~/.local/share/lmm/
├── lmm.db               # SQLite database
├── cache/
│   └── <game-slug>/
│       └── <mod-id>/
│           ├── metadata.json
│           └── files/
└── downloads/           # Temporary download location
```

---

## Technical Architecture

### Interface-Based Design

Core interfaces for extensibility:

```go
// ModSource represents a mod repository (NexusMods, CurseForge, etc.)
type ModSource interface {
    ID() string
    Name() string
    Authenticate(ctx context.Context) error
    Search(ctx context.Context, query SearchQuery) ([]Mod, error)
    GetMod(ctx context.Context, modID string) (*Mod, error)
    Download(ctx context.Context, mod *Mod, version string) (io.ReadCloser, error)
    CheckUpdates(ctx context.Context, mods []InstalledMod) ([]Update, error)
}

// Game represents a moddable game
type Game interface {
    ID() string
    Name() string
    InstallPath() string
    ModPath() string
    ValidateMod(mod *Mod) error
}

// Profile represents a mod configuration
type Profile interface {
    Name() string
    Mods() []ModReference
    LoadOrder() []string
    ConfigOverrides() map[string][]byte
}
```

### Error Handling

- Graceful degradation on network failures
- Automatic retry with exponential backoff for downloads
- Clear error messages with suggested actions
- Transaction-like mod operations (rollback on failure)

---

## MVP Scope

### Phase 1: Core Infrastructure

- [ ] Project structure and build system
- [ ] SQLite database schema and migrations
- [ ] YAML configuration parsing
- [ ] Central cache management
- [ ] Linking system (symlink, hardlink, copy)

### Phase 2: NexusMods Integration

- [ ] OAuth authentication flow
- [ ] GraphQL client implementation
- [ ] Mod search and metadata retrieval
- [ ] Download manager with retry logic

### Phase 3: Mod Management

- [ ] Install/uninstall operations
- [ ] Load order management
- [ ] Conflict detection
- [ ] Dependency resolution

### Phase 4: Profile System

- [ ] Profile creation and switching
- [ ] Per-profile configurations
- [ ] Export/import functionality

### Phase 5: TUI

- [ ] Main application shell
- [ ] Game selector view
- [ ] Mod browser view
- [ ] Installed mods view
- [ ] Profile management view
- [ ] Settings view

### Phase 6: CLI

- [ ] Basic command structure
- [ ] Search, install, update commands
- [ ] Profile commands
- [ ] Status and list commands

### Phase 7: Polish

- [ ] Game auto-detection
- [ ] Configurable keybindings
- [ ] Documentation
- [ ] Testing with reference games (generic, then Bethesda)

---

## Reference Games for Testing

### Generic/Simple (Initial Testing)

- Games with straightforward mod directories
- Single folder mod installations
- No complex load order requirements

### Bethesda Games (Advanced Testing)

- **Skyrim Special Edition**: Complex ecosystem, plugin load order, SKSE
- **Fallout 4**: Similar complexity, F4SE support
- Tests: Load order management, BSA handling, plugin conflicts

---

## Non-Goals (MVP)

- GUI interface (TUI and CLI only)
- Mod creation/authoring tools
- Steam Workshop direct integration (manual download only)
- Windows/macOS support
- Mod merging or patching
- Virtual filesystem (overlayfs) - may be future enhancement

---

## Success Metrics

1. Successfully install and manage mods for at least 3 different games
2. Profile export/import works correctly between systems
3. Dependency resolution handles common mod ecosystems
4. TUI is responsive and intuitive for both navigation styles
5. CLI supports basic scripting workflows

---

## Open Questions

1. Should we support mod categories/tags for organization within profiles?
2. How should we handle mods that require manual installation steps?
3. Should there be a "dry run" mode for operations?
4. Integration with mod organizers' virtual filesystem approach (MO2-style)?

---

## Revision History

| Version | Date       | Changes                                                                |
| ------- | ---------- | ---------------------------------------------------------------------- |
| 0.1     | 2026-01-22 | Initial PRD draft                                                      |
| 0.2     | 2026-01-22 | Added per-mod auto-update option, version pinning, rollback capability |
