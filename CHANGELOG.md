# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.7.2] - 2026-01-27

### Fixed

- **Profile Sync FileIDs**: `lmm profile sync` now updates FileIDs for existing mods that are missing them

## [0.7.1] - 2026-01-27

### Fixed

- **Re-install Cleanup**: When re-installing a mod (e.g., to change file selection), old files are now properly removed from the game directory before installing new files
- **Cache Cleanup**: Old cache is deleted before downloading new files during re-install, ensuring a clean slate
- **Profile Export/Import FileIDs**: File IDs are now preserved when exporting and importing profiles
  - `lmm profile export` now includes FileIDs for each mod in the exported YAML
  - `lmm profile import` uses FileIDs from the imported profile to restore exact file selections
  - `lmm profile apply` and `lmm profile switch` also respect FileIDs for new installs and re-downloads

## [0.7.0] - 2026-01-26

### Added

- **Multi-File Install**: `lmm install` now supports selecting multiple files per mod
  - Select files like mods: `1,3,5` or `1-3` or `1,3-5`
  - Useful for mods with main file + optional patches or multiple optional files
  - `--file` flag accepts comma-separated file IDs for scripting
- **File ID Tracking**: Mods now track which specific file(s) were downloaded from the source
  - When re-downloading (cache missing), restores the exact file(s) the user originally installed
  - Supports multiple files per mod (e.g., main file + optional patches)
  - New database table `installed_mod_files` stores file IDs per mod
  - Falls back to primary file if stored IDs are no longer available on the source
- **Cache-Missing Re-download**: Mods that exist in the database but are missing from cache are now automatically re-downloaded
  - `lmm profile import` - Shows "cache missing" category separately, re-downloads as needed
  - `lmm profile apply` - Detects cache-missing mods when enabling, triggers download
  - `lmm profile switch` - Detects cache-missing mods, triggers download
  - `lmm redeploy` - Re-downloads from source instead of failing when cache is missing
  - Useful when cache directory changes, files are deleted, or profile is imported on a new machine

### Changed

- **Database Schema**: Migration V4 adds `installed_mod_files` table for tracking downloaded file IDs

## [0.6.9] - 2026-01-26

### Added

- **Cache-Missing Re-download** (superseded by 0.7.0): Initial implementation without file ID tracking

## [0.6.8] - 2026-01-25

### Added

- **GitHub Releases**: Automated release builds via GitHub Actions and GoReleaser
  - Creates Linux amd64 and arm64 binaries on tag push
  - Archives include README, LICENSE, and CHANGELOG
  - Checksums provided for verification
- **Go Install Support**: `go install github.com/DonovanMods/linux-mod-manager/cmd/lmm@latest`

### Changed

- **Module Path**: Changed from `lmm` to `github.com/DonovanMods/linux-mod-manager` to enable go install

## [0.6.7] - 2026-01-24

### Added

- **Status Default Game**: `lmm status` marks the default game with `(default)` in the game list

## [0.6.6] - 2026-01-24

### Added

- **Enhanced Status Output**: More detailed game information in `lmm status`
  - Shows installed mod count per game and total across all games
  - Shows link method with indicator for per-game overrides
  - Shows cache path with indicator for per-game vs global default
  - Shows source mappings in verbose mode
  - Summary view (`lmm status -v`) shows game ID, path, and link method columns
  - `*` suffix indicates per-game overrides in summary view
- **Cache Path in List**: `lmm list -v` shows cache path in verbose header

## [0.6.5] - 2026-01-24

### Added

- **Configurable Cache Path**: Set `cache_path` in `config.yaml` to store downloaded mods in a custom location
  - Supports `~` expansion for home directory paths
  - Useful for storing mods on a separate drive or faster storage
  - Defaults to `~/.local/share/lmm/cache/` if not set
- **Per-Game Cache Path**: Override the global cache path for individual games in `games.yaml`
  - Set `cache_path` per game to store that game's mods in a custom location
  - Priority: per-game `cache_path` > global `cache_path` > default

## [0.6.4] - 2026-01-24

### Added

- **Search Command**: Shows `[installed]` marker for mods already in your profile
  - Matches the existing behavior in the install command
  - Optional `--profile` flag to check against a specific profile

## [0.6.3] - 2026-01-24

### Added

- **Deployment Method Tracking**: `lmm list` now shows a DEPLOY column indicating how each mod was deployed (symlink, hardlink, or copy)
  - Link method is saved per-mod when installing, updating, or redeploying
  - Helps identify which mods use which deployment strategy

### Changed

- Database schema V3: Added `link_method` column to track deployment method per mod

## [0.6.2] - 2026-01-24

### Fixed

- **Per-Game Link Method**: Install, update, and redeploy commands now correctly use per-game `link_method` from `games.yaml`
  - If a game specifies `link_method` in `games.yaml`, that method is used
  - If not specified, falls back to global `default_link_method` from `config.yaml`
  - Different games can now use different deployment methods (symlink, hardlink, copy)
  - Affects: `lmm install`, `lmm update`, `lmm update rollback`, and `lmm redeploy`

## [0.6.1] - 2026-01-24

### Added

- **Redeploy Command**: `lmm redeploy` to re-deploy mods from cache to game directory
  - Re-deploy all enabled mods: `lmm redeploy`
  - Re-deploy specific mod: `lmm redeploy <mod-id>`
  - `--method` flag to try different link methods (symlink, hardlink, copy)
  - Useful when changing deployment methods or refreshing mod files

## [0.6.0] - 2026-01-24

### Added

- **Profile as Source of Truth**: Profiles now fully track mod state
  - Installing a mod automatically adds it to the current profile
  - Uninstalling a mod removes it from the current profile
- **Profile Sync Command**: `lmm profile sync` updates profile YAML to match installed mods
  - Use when profile gets out of sync or migrating from pre-profile installs
- **Profile Apply Command**: `lmm profile apply` makes system match profile
  - Installs missing mods, enables/disables as needed
  - Use after manually editing a profile YAML
- **Enhanced Profile Switch**: Switching profiles now installs missing mods
  - Shows preview of changes (disable/enable/install)
  - Prompts for confirmation before making changes
- **Enhanced Profile Import**: Importing profiles can install missing mods
  - `--force` flag to overwrite existing profiles
  - `--no-install` flag to skip installing missing mods
  - Shows preview of which mods need to be downloaded

### Changed

- Profile switch now shows detailed diff before switching
- Profile import shows summary of installed vs missing mods

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

[Unreleased]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.2...HEAD
[0.7.2]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.1...v0.7.2
[0.7.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.9...v0.7.0
[0.6.9]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.8...v0.6.9
[0.6.8]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.7...v0.6.8
[0.6.7]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.6...v0.6.7
[0.6.6]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.5...v0.6.6
[0.6.5]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.4...v0.6.5
[0.6.4]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.3...v0.6.4
[0.6.3]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.2...v0.6.3
[0.6.2]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.5.3...v0.6.0
[0.5.3]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.5.0...v0.5.2
[0.5.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/DonovanMods/linux-mod-manager/releases/tag/v0.1.0
