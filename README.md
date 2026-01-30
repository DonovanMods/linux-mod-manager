# lmm - Linux Mod Manager

A terminal-based mod manager for Linux that provides a CLI interface for searching, installing, updating, and managing game mods from various sources.

## Features

- **NexusMods Integration**: Search, download, install, and check updates from NexusMods
- **Profile System**: Manage multiple mod configurations per game
- **Update Management**: Check for updates with configurable policies (auto, notify, pinned)
- **Rollback Support**: Revert to previous mod versions when updates cause issues
- **Flexible Deployment**: Symlink, hardlink, or copy mods to game directories
- **Dependency Detection**: Fetches mod dependencies from NexusMods (automatic installation coming soon)
- **Pure Go**: No CGO required, easy cross-compilation

## Installation

### From GitHub Releases

Download the latest release for your architecture from the [Releases page](https://github.com/DonovanMods/linux-mod-manager/releases):

- `lmm_<version>_linux_amd64.tar.gz` - 64-bit x86
- `lmm_<version>_linux_arm64.tar.gz` - 64-bit ARM

Extract and install:

```bash
tar -xzf lmm_*.tar.gz
sudo mv lmm /usr/local/bin/
```

### With Go Install

Requires Go 1.21 or later.

```bash
go install github.com/DonovanMods/linux-mod-manager/cmd/lmm@latest
```

### From Source

```bash
git clone https://github.com/DonovanMods/linux-mod-manager.git
cd linux-mod-manager
go build -o lmm ./cmd/lmm
```

## Quick Start

### Authentication

NexusMods requires an API key for downloading mods. Get your personal API key from [NexusMods API settings](https://www.nexusmods.com/users/myaccount?tab=api) and authenticate:

```bash
lmm auth login
# Or set the environment variable
export NEXUSMODS_API_KEY="your-api-key"
```

### Set Default Game

Set a default game to avoid specifying `--game` for every command:

```bash
# Set default game
lmm game set-default skyrim-se

# Now all game commands work without --game
lmm search "skyui"
lmm install "skyui"
lmm list
lmm update
lmm uninstall 12345
lmm mod set-update 12345 --auto
lmm profile list

# Show current default
lmm game show-default

# Clear the default
lmm game clear-default
```

### Basic Usage

```bash
# Search for mods
lmm search "skyui" --game skyrim-se

# Install a mod (interactive selection)
lmm install "skyui" --game skyrim-se

# Install multiple mods (select with 1,3-5 or 1..3 syntax)
lmm install "stack" --game starrupture

# Install by mod ID (for scripting)
lmm install --id 12345 --game skyrim-se

# List installed mods
lmm list --game skyrim-se

# Check for updates (shows partial results and a warning if some mods can't be checked)
lmm update --game skyrim-se

# Update a specific mod
lmm update 12345 --game skyrim-se

# Rollback to previous version
lmm update rollback 12345 --game skyrim-se

# Show status
lmm status
```

### Update Policies

Control how each mod handles updates:

```bash
# Auto-update when checking
lmm mod set-update 12345 --game skyrim-se --auto

# Notify only (default)
lmm mod set-update 12345 --game skyrim-se --notify

# Pin to current version
lmm mod set-update 12345 --game skyrim-se --pin
```

## Configuration

Configuration files are stored in `~/.config/lmm/`:

### Main Config (`config.yaml`)

```yaml
default_link_method: symlink # Global default: symlink, hardlink, or copy
default_game: skyrim-se # Optional, set via 'lmm game set-default'
cache_path: ~/.local/share/lmm/cache # Optional, defaults to <data_dir>/cache
```

The `cache_path` setting allows you to store downloaded mod files in a custom location. This is useful if you want to:

- Store mods on a separate drive with more space
- Share cached mods between multiple installations
- Use a faster SSD for mod storage

Paths support `~` expansion for the home directory.

### Games (`games.yaml`)

```yaml
games:
  skyrim-se:
    name: "Skyrim Special Edition"
    install_path: "/path/to/skyrim"
    mod_path: "/path/to/skyrim/Data"
    sources:
      nexusmods: "skyrimspecialedition"
    # link_method: symlink  # Optional: override default_link_method for this game
    # cache_path: ~/skyrim-mods  # Optional: override global cache_path for this game

  starfield:
    name: "Starfield"
    install_path: "/path/to/starfield"
    mod_path: "/path/to/starfield/Data"
    sources:
      nexusmods: "starfield"
    link_method: copy # This game requires file copies instead of symlinks
    cache_path: /mnt/fast-ssd/starfield-mods # Store this game's mods on fast storage
```

### Deployment Methods

Mods can be deployed using three methods:

| Method     | Description                                                    |
| ---------- | -------------------------------------------------------------- |
| `symlink`  | Symbolic links to cached files (default, space efficient)      |
| `hardlink` | Hard links (transparent to games, requires same filesystem)    |
| `copy`     | Full file copies (maximum compatibility, uses more disk space) |

**Priority**: Per-game `link_method` in `games.yaml` takes precedence over `default_link_method` in `config.yaml`. If neither is set, defaults to `symlink`.

### Cache Path Priority

The mod cache location is determined by:

1. Per-game `cache_path` in `games.yaml` (if set)
2. Global `cache_path` in `config.yaml` (if set)
3. Default: `~/.local/share/lmm/cache/`

This allows you to store different games' mods on different drives (e.g., large games on HDD, frequently accessed games on SSD).

## CLI Reference

### Global Flags

| Flag         | Short | Description                                                                                                |
| ------------ | ----- | ---------------------------------------------------------------------------------------------------------- |
| `--game`     | `-g`  | Game ID (optional if default set via `game set-default`)                                                   |
| `--verbose`  | `-v`  | Enable verbose output                                                                                      |
| `--config`   |       | Custom config directory                                                                                    |
| `--data`     |       | Custom data directory                                                                                      |
| `--json`     |       | Output in JSON (list, status, search, update, conflicts, verify, mod show); errors print `{"error":"..."}` |
| `--no-hooks` |       | Disable all hooks at runtime                                                                               |
| `--no-color` |       | Disable colored output (respects NO_COLOR env)                                                             |

### Commands

| Command                                | Description                                 |
| -------------------------------------- | ------------------------------------------- |
| `lmm search <query>`                   | Search for mods                             |
| `lmm search <query> --category ID`     | Filter by NexusMods category                |
| `lmm search <query> --tag TAG`         | Filter by tag (repeat for multiple)         |
| `lmm install <query>`                  | Search and install a mod                    |
| `lmm install --id <mod-id>`            | Install by mod ID                           |
| `lmm uninstall <mod-id>`               | Uninstall a mod                             |
| `lmm list`                             | List installed mods                         |
| `lmm list --profiles`                  | List profiles for the game                  |
| `lmm status`                           | Show current status                         |
| `lmm update`                           | Check for and apply auto-updates            |
| `lmm update <mod-id>`                  | Update a specific mod                       |
| `lmm update --all`                     | Apply all available updates                 |
| `lmm update --dry-run`                 | Preview what would update                   |
| `lmm update rollback <mod-id>`         | Rollback to previous version                |
| `lmm verify`                           | Verify cached mod files (see below)         |
| `lmm verify --fix`                     | Re-download missing or corrupted files      |
| `lmm mod enable <mod-id>`              | Enable a disabled mod                       |
| `lmm mod disable <mod-id>`             | Disable mod (keep in cache)                 |
| `lmm mod set-update <mod-id> --auto`   | Enable auto-updates for mod                 |
| `lmm mod set-update <mod-id> --notify` | Notify only (default)                       |
| `lmm mod set-update <mod-id> --pin`    | Pin mod to current version                  |
| `lmm mod show <mod-id>`                | Show mod details (description, image, etc.) |
| `lmm mod files <mod-id>`               | List files deployed by mod                  |
| `lmm game set-default <game-id>`       | Set the default game                        |
| `lmm game show-default`                | Show current default game                   |
| `lmm game clear-default`               | Clear the default game setting              |
| `lmm auth login`                       | Authenticate with NexusMods                 |
| `lmm auth logout`                      | Remove stored credentials                   |
| `lmm auth status`                      | Show authentication status                  |
| `lmm profile list`                     | List profiles                               |
| `lmm profile create <name>`            | Create a profile                            |
| `lmm profile switch <name>`            | Switch to a profile (installs missing mods) |
| `lmm profile delete <name>`            | Delete a profile                            |
| `lmm profile export <name>`            | Export profile to YAML                      |
| `lmm profile import <file>`            | Import profile from YAML                    |
| `lmm profile import <file> --force`    | Import and overwrite existing               |
| `lmm profile reorder [mod-id ...]`     | Show or set load order                      |
| `lmm profile sync`                     | Update profile to match installed mods      |
| `lmm profile apply`                    | Install/enable mods to match profile        |
| `lmm deploy`                           | Deploy all enabled mods from cache          |
| `lmm deploy <mod-id>`                  | Deploy specific mod from cache              |
| `lmm deploy --method hardlink`         | Deploy using different link method          |
| `lmm deploy --purge`                   | Purge then deploy all mods                  |
| `lmm purge`                            | Remove all mods from game directory         |
| `lmm conflicts`                        | Show file conflicts in current profile      |

### Update check behavior

When you run `lmm update`, the tool checks each installed mod against the source (e.g. NexusMods). If some mods cannot be fetched (e.g. deleted, private, or network error), you still see **partial results** (any updates that were found), and a **warning** is printed to stderr describing which mods could not be checked.

### Verify output

`lmm verify` reports per file:

- **+ ModName (fileID) - OK** - Cache exists and checksum stored.
- **X ModName (fileID) - MISSING (version X not in cache)** - Cached files for that mod version are missing; use `--fix` to re-download.
- **? ModName (fileID) - NO CHECKSUM** - File was installed without a stored checksum (e.g. before checksum support or with `--skip-verify`).

## Architecture

```text
cmd/lmm/                  # CLI entry point (Cobra)
internal/
├── domain/               # Core types (Mod, Profile, Game)
├── source/               # Mod source abstraction
│   └── nexusmods/        # NexusMods API client
├── storage/
│   ├── db/               # SQLite storage
│   ├── config/           # YAML configuration
│   └── cache/            # Mod file cache
├── linker/               # Deployment strategies
└── core/                 # Business logic
    ├── service.go        # Main orchestrator
    ├── installer.go      # Install/uninstall
    ├── updater.go        # Update checking
    ├── downloader.go     # HTTP downloads
    └── extractor.go      # Archive extraction
```

## File Locations

| Type      | Path                                  |
| --------- | ------------------------------------- |
| Config    | `~/.config/lmm/`                      |
| Database  | `~/.local/share/lmm/lmm.db`           |
| Mod Cache | `~/.local/share/lmm/cache/` (default) |

The mod cache location can be customized via `cache_path` in `config.yaml`.

## Documentation

- **[Configuration reference](docs/configuration.md)** – All options for `config.yaml` and `games.yaml` (including hooks, link method, sources).
- **Man pages** – In `docs/man/man1/`: `lmm(1)`, `lmm-install(1)`, `lmm-list(1)`, `lmm-search(1)`, `lmm-status(1)`, `lmm-verify(1)`, `lmm-game(1)`, `lmm-profile(1)`, `lmm-update(1)`, `lmm-mod(1)`, `lmm-conflicts(1)`, `lmm-deploy(1)`, `lmm-purge(1)`. View with `man -l docs/man/man1/lmm.1` or install to your man path.
- **[CONTRIBUTING.md](CONTRIBUTING.md)** – How to build, test, and submit changes.

## Roadmap

- [x] NexusMods authentication and downloads
- [x] Update management with policies and rollback
- [x] Default game setting (avoid --game on every command)
- [x] Mod dependency detection from NexusMods
- [x] Conflict detection (file conflicts, circular dependency warnings)
- [x] Mod file verification (checksums, --fix re-download)
- [ ] Automatic dependency installation
- [ ] Interactive TUI (Bubble Tea) - see BACKLOG.md
- [ ] Additional mod sources (CurseForge, ESOUI)
- [ ] Game auto-detection beyond Steam (Lutris, Heroic, Flatpak)
- [ ] Backup and restore

## Development

```bash
# Run tests
go test ./...

# Format code
go fmt ./...

# Vet code
go vet ./...

# Build
go build -o lmm ./cmd/lmm
```

## License

MIT License - See [LICENSE](LICENSE) for details.

## Acknowledgments

- [Cobra](https://github.com/spf13/cobra) - CLI framework
- [NexusMods](https://www.nexusmods.com/) - Mod hosting platform
