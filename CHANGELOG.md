# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.5.3] - 2026-01-24

### Added

- **Multi-Select Install**: Select multiple mods from search results using range syntax
  - `1,3,5` - Select specific mods by number
  - `1-3` or `1..3` - Select a range of mods
  - `1,3-5,8` - Mix both formats
  - Each mod is installed sequentially with automatic file selection

## [0.5.2] - 2026-01-24

### Added

- **Install Command**: Search results now show `[installed]` marker for mods already in your profile

## [0.5.1] - 2026-01-24

### Changed

- **List Command**: Mod count is now always displayed, not just in verbose mode

## [0.5.0] - 2026-01-24

### Added

- **Mod Enable/Disable**: New commands to enable or disable mods without uninstalling
  - `lmm mod enable <mod-id>` - Redeploy mod files from cache to game directory
  - `lmm mod disable <mod-id>` - Remove mod files from game directory, keep in cache
  - Disabled mods show as "no" in the ENABLED column of `lmm list`
  - Re-enabling a mod does not require re-downloading

## [0.4.0] - 2026-01-24

### Added

- **NexusMods Update Detection**: `CheckUpdates` now queries NexusMods API for current mod versions and compares against installed versions using semantic version parsing
- **Mod Dependency Fetching**: `GetDependencies` queries NexusMods GraphQL API for mod requirements, returning dependencies as `ModReference` entries

### Fixed

- **Uninstall Cleanup**: `lmm uninstall` now properly undeploys mod files from the game directory and cleans up the mod cache (unless `--keep-cache` is specified)

### Changed

- **NexusMods OAuth**: Clarified that NexusMods uses API key authentication, not OAuth. The `ExchangeToken` method now returns a clear error message directing users to use `SetAPIKey()` or the `NEXUSMODS_API_KEY` environment variable

## [0.3.0] - 2026-01-23

### Added

- **Update Management**: Complete update workflow with policies and rollback
  - `lmm update --game <game>` - Check all mods for updates, apply auto-updates
  - `lmm update <mod-id> --game <game>` - Update specific mod
  - `lmm update --dry-run` - Preview what would update
  - `lmm update --all` - Apply all available updates (not just auto-updates)
  - `lmm update rollback <mod-id>` - Rollback to previous version
  - `lmm mod set-update <mod-id> --auto|--notify|--pin` - Set per-mod update policy
- **Per-mod Update Policies**:
  - `notify` (default) - Show available updates, require manual approval
  - `auto` - Automatically apply updates when checking
  - `pinned` - Never update this mod automatically
- **Rollback Support**: Previous version preserved in cache after updates
- **Database Migration V2**: Added `previous_version` column for rollback tracking

### Changed

- **CLI**: `lmm update` now shows update policy column in output
- **CLI**: Auto-updates are applied immediately when checking for updates

## [0.2.0] - 2026-01-23

### Added

- **Mod Download**: Complete download pipeline for installing mods
  - `lmm install "query"` - Search for mods by name and install interactively
  - `lmm install --id <mod-id>` - Install directly by mod ID (for scripting)
  - `lmm install -y` - Auto-select first/primary options (no prompts)
  - Download progress bar with size tracking
  - Archive extraction: ZIP (native Go), 7z/RAR (via system `7z` command)
  - Mod file caching with version-aware storage
  - Automatic deployment to game directory via symlinks
- **NexusMods API**: File listing and download URL generation
  - `GetModFiles()` - List available download files for a mod
  - `GetDownloadURL()` - Get CDN download URL for a specific file
- **Domain Types**: `DownloadableFile` type for files available from mod sources
- **Core Components**:
  - `Downloader` - HTTP file download with progress tracking and atomic writes
  - `Extractor` - Archive extraction with zip-slip protection
- **Authentication**: NexusMods API key authentication
  - `lmm auth login` - Authenticate with NexusMods using personal API key
  - `lmm auth logout` - Remove stored credentials
  - `lmm auth status` - Show authentication status
  - Secure token storage in SQLite database
  - Support for `NEXUSMODS_API_KEY` environment variable
  - Automatic token loading on startup
- **CLI**: Helpful error messages when authentication is required

### Fixed

- **CLI**: NexusMods source now properly registered on startup (search was failing with "source not found")
- **CLI**: Removed downloads column from search output (GraphQL doesn't return this data)

### Changed

- **CLI**: `lmm install` now accepts search query instead of mod ID (use `--id` for direct ID)
- **NexusMods**: Search now uses GraphQL v2 API for proper server-side search (no auth required for basic searches)
- **NexusMods**: REST API v1 still used for mod details, files, and downloads (requires API key)

### Removed

- **TUI**: Removed terminal UI to focus on CLI functionality first (see BACKLOG.md)

## [0.1.0] - 2026-01-23

### Added

#### Core Infrastructure

- Domain types: `Mod`, `InstalledMod`, `Game`, `Profile`, `ModReference`
- SQLite database with migrations for mod metadata and auth tokens
- YAML configuration for games and profiles
- Mod file cache with version-aware storage

#### Mod Sources

- `ModSource` interface for abstracting mod repositories
- Source registry for managing multiple mod sources
- NexusMods REST API v1 client with mod fetching and browse functionality

#### Mod Management

- Service facade orchestrating all mod operations
- Installer with download, extract, and deploy functionality
- Updater with semantic version comparison
- Dependency resolver with cycle detection (topological sort)
- Profile manager with create, delete, switch, export, and import

#### Deployment

- Linker interface with three strategies:
  - Symlink (default) - symbolic links to cached files
  - Hardlink - hard links for same-filesystem deployments
  - Copy - full file copies for maximum compatibility

#### Terminal UI (TUI)

- Bubble Tea application shell with view routing
- Game selector view with navigation
- Mod browser with search input and results display
- Installed mods view with enable/disable and reorder
- Profile management view with create/delete/switch/export
- Settings view with cycling options
- Configurable keybindings (vim and standard modes)

#### Command Line Interface (CLI)

- Cobra command structure with global flags
- `lmm` - Launch interactive TUI (default)
- `lmm search <query>` - Search for mods
- `lmm install <mod-id>` - Install a mod
- `lmm uninstall <mod-id>` - Uninstall a mod
- `lmm update [mod-id]` - Check for updates
- `lmm list` - List installed mods
- `lmm status` - Show current status
- `lmm profile list|create|delete|switch|export|import` - Profile management

### Technical Details

- Pure Go implementation (no CGO required)
- ~2500 lines of Go code
- Comprehensive test coverage for core components
- MIT License

[Unreleased]: https://github.com/dyoung522/linux-mod-manager/compare/v0.5.3...HEAD
[0.5.3]: https://github.com/dyoung522/linux-mod-manager/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/dyoung522/linux-mod-manager/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/dyoung522/linux-mod-manager/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/dyoung522/linux-mod-manager/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/dyoung522/linux-mod-manager/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/dyoung522/linux-mod-manager/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/dyoung522/linux-mod-manager/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/dyoung522/linux-mod-manager/releases/tag/v0.1.0
