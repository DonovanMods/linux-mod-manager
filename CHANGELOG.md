# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **CurseForge Support**: New mod source alongside NexusMods
  - Search, download, install, and update mods from CurseForge
  - `lmm auth login curseforge` to authenticate with API key
  - `CURSEFORGE_API_KEY` environment variable support
  - Dependency detection from CurseForge file metadata
  - Configure in games.yaml: `sources: { curseforge: "432" }` (numeric game IDs)

## [1.0.0] - 2026-01-29

### Added

- **Steam game auto-detection**: `lmm game detect` scans Steam libraries and offers to add known moddable games to `games.yaml`
  - Parses `libraryfolders.vdf` and `appmanifest_*.acf`
  - Known games map for Skyrim SE, Starfield, Fallout 4, Elden Ring, Witcher 3, and others
  - Prompts to add selected games (e.g. 1,2 or all or none); creates default profile per game
- **JSON output**: `--json` flag on `list`, `status`, and `search` for scriptable output
- **CLI polish**: Exit codes 0=success, 1=error, 2=user cancelled; `--no-color` and `NO_COLOR` env support; `ErrCancelled` for cancelled operations
- **Verify --fix**: `lmm verify --fix --game <id>` re-downloads missing cached mod files and updates checksums (skips local mods)
- **Documentation**: Man pages (`docs/man/man1/`), configuration reference (`docs/configuration.md`), CONTRIBUTING.md

### Changed

- Verify command `--fix` now implements re-download for missing files (was placeholder)

## [0.12.0] - 2026-01-29

### Added

- **Installation Hooks**: Run user-defined scripts before/after mod operations
  - Configure hooks per-game in `games.yaml` with optional per-profile overrides
  - Hook points: `install.before_all`, `install.before_each`, `install.after_each`, `install.after_all`
  - Same pattern for `uninstall.*` hooks
  - Environment variables: `LMM_GAME_ID`, `LMM_GAME_PATH`, `LMM_MOD_PATH`, `LMM_MOD_ID`, `LMM_MOD_NAME`, `LMM_MOD_VERSION`, `LMM_HOOK`
  - Contextual failure handling: `before_*` hooks abort (unless `--force`), `after_*` hooks warn only
  - `--no-hooks` global flag to disable all hooks at runtime
  - Configurable timeout via `hook_timeout` in `config.yaml` (default 60s)
- **Batch Install/Uninstall**: New `InstallBatch` and `UninstallBatch` methods in installer

### Changed

- All mod commands now support hooks: `install`, `uninstall`, `deploy`, `purge`, `update`, `update rollback`, `import`
- Commands now have `--force` flag to continue despite hook failures

## [0.11.0] - 2026-01-28

### Added

- **Automatic dependency installation**: `lmm install` now resolves and installs mod dependencies automatically
  - Shows install plan with dependencies in topological order
  - Warns about dependencies not available on source (external deps like SKSE)
  - `--no-deps` flag to skip dependency installation
  - `-y` flag auto-confirms dependency installation

## [0.10.0] - 2026-01-28

### Added

- **Local Mod Import**: `lmm import <archive-path>` imports mods from local archive files
  - Supports ZIP, 7z, and RAR archive formats
  - Auto-detects NexusMods naming pattern (ModName-ModID-Version.ext) for update linking
  - Extracts to mod cache and deploys to game directory
  - Flags: `--profile`, `--source`, `--id`, `--force`
  - Local mods tracked with source ID "local"
- **NexusMods Filename Parsing**: Automatically extracts mod ID and version from filenames like `SkyUI-12604-5-2SE.zip`
  - Strips trailing timestamps from version strings
  - Normalizes version format (replaces dashes with dots)
  - Falls back to UUID for mods without recognizable patterns

### Changed

- `lmm list` now shows "(local)" for mods imported from local files
- Update checker skips local mods (no remote source to check)

## [0.9.0] - 2026-01-28

### Added

- **Conflict Detection**: Warns when installing mods that overwrite files from existing mods
  - Tracks file ownership in database (per profile)
  - Shows conflicts before install with prompt to continue or cancel
  - `--force` flag to skip conflict prompts: `lmm install --force`
  - Database migration V7 adds `deployed_files` table
- **Mod Files Command**: `lmm mod files <mod-id>` lists all files deployed by a mod
  - Useful for debugging and understanding mod contents
  - Shows files tracked in database (mods installed with 0.9.0+)
- **Conflicts Command**: `lmm conflicts` shows all file conflicts in current profile
  - Lists each conflicting file path
  - Shows which mod owns the file vs which mods also want it
  - Helps identify and resolve mod conflicts

### Changed

- Installer now tracks deployed files per mod in database
- Uninstall now removes file tracking records

## [0.8.0] - 2026-01-28

### Added

- **Checksum Verification**: MD5 checksums are calculated during download and stored
  - Checksums computed during download using TeeReader (zero extra I/O)
  - Stored per-file in database for integrity verification
  - Displayed after each file download (truncated for readability)
  - `--skip-verify` flag on install to skip checksum storage
- **Verify Command**: `lmm verify` checks integrity of cached mod files
  - Verifies all cached mods: `lmm verify --game skyrim-se`
  - Verify specific mod: `lmm verify 12345 --game skyrim-se`
  - Shows OK, MISSING, or NO CHECKSUM status for each file
  - `--fix` flag placeholder for future re-download functionality

### Changed

- Database schema V6: Added `checksum` column to `installed_mod_files` table
- `DownloadMod` now returns `DownloadModResult` with checksum information

## [0.7.8] - 2026-01-28

### Added

- **Deployed State Tracking**: Separate `enabled` and `deployed` states for mods
  - `enabled` = user intent (wants this mod active)
  - `deployed` = current state (files are in game directory)
  - `lmm list` now shows both ENABLED and DEPLOYED columns
  - Purge sets `deployed=false` while preserving `enabled` state
  - Deploy sets `deployed=true` without changing `enabled` state
  - Allows purging and redeploying without losing user's enabled/disabled preferences
- **Deploy All Flag**: `lmm deploy --all` deploys all mods including disabled ones
  - Useful after a purge when you want to deploy everything
  - Without `--all`, only enabled mods are deployed

### Changed

- Database schema V5: Added `deployed` column to track deployment state separately from enabled state

## [0.7.7] - 2026-01-28

### Added

- **Purge Command**: `lmm purge` removes all deployed mods from a game directory
  - Resets the game directory back to its pre-modded state
  - Mod records are preserved by default; use `lmm deploy` to restore
  - `--uninstall` flag also removes mod records from database
  - `--yes` flag skips confirmation prompt
  - Useful when mods get out of sync or you want to start fresh
- **Deploy Purge Flag**: `lmm deploy --purge` clears all deployed mods before deploying
  - Ensures a clean slate before deploying mods
  - Useful for switching deployment methods or fixing sync issues
- **Renamed Command**: `lmm redeploy` is now `lmm deploy`

## [0.7.6] - 2026-01-28

### Fixed

- **Multi-File Install**: When installing multiple files from the same mod, only the first file was being downloaded and extracted. The remaining files were skipped because the cache directory already existed. Now all selected files are properly downloaded and extracted to the cache.

## [0.7.5] - 2026-01-27

### Added

- **UpsertMod API**: New `ProfileManager.UpsertMod()` method for atomic add-or-update operations
  - Updates mod in place if it exists (preserves load order position)
  - Appends to end if mod is new
  - Single save operation instead of two
  - Replaces error-prone `RemoveMod` + `AddMod` pattern

### Changed

- **Profile Operations**: Install, update, sync, switch, apply, and import now use `UpsertMod`
  - More reliable FileID updates during re-installs
  - Cleaner code with centralized profile modification logic

## [0.7.4] - 2026-01-27

### Fixed

- **FileIDs Persistence**: FileIDs were not being written to profile YAML files
  - Added `file_ids` field to profile config format
  - Profile save/load now properly handles FileIDs

## [0.7.3] - 2026-01-27

### Added

- **Auto-Sync FileIDs**: Profile YAML now automatically stays in sync with actual file selections
  - Install: Profile updated with downloaded FileIDs
  - Update: Preserves file selections, updates both DB and profile
  - Profile switch/import/apply: Auto-sync FileIDs after installing mods
  - Profile sync: Updates FileIDs for existing mods that are missing them

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

[Unreleased]: https://github.com/DonovanMods/linux-mod-manager/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.12.0...v1.0.0
[0.12.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.8...v0.8.0
[0.7.8]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.7...v0.7.8
[0.7.7]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.6...v0.7.7
[0.7.6]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.5...v0.7.6
[0.7.5]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.4...v0.7.5
[0.7.4]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.3...v0.7.4
[0.7.3]: https://github.com/DonovanMods/linux-mod-manager/compare/v0.7.2...v0.7.3
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
