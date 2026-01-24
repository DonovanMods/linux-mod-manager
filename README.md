# lmm - Linux Mod Manager

A terminal-based mod manager for Linux that provides a CLI interface for searching, installing, updating, and managing game mods from various sources.

## Features

- **NexusMods Integration**: Search, download, and install mods from NexusMods
- **Profile System**: Manage multiple mod configurations per game
- **Update Management**: Check for updates with configurable policies (auto, notify, pinned)
- **Rollback Support**: Revert to previous mod versions when updates cause issues
- **Flexible Deployment**: Symlink, hardlink, or copy mods to game directories
- **Dependency Resolution**: Automatic handling of mod dependencies with cycle detection
- **Pure Go**: No CGO required, easy cross-compilation

## Installation

### From Source

Requires Go 1.21 or later.

```bash
git clone https://github.com/dyoung522/linux-mod-manager.git
cd linux-mod-manager
go build -o lmm ./cmd/lmm
```

Optionally, install to your PATH:

```bash
go install ./cmd/lmm
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

# Install by mod ID (for scripting)
lmm install --id 12345 --game skyrim-se

# List installed mods
lmm list --game skyrim-se

# Check for updates
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
default_link_method: symlink # symlink, hardlink, or copy
default_game: skyrim-se # optional, set via 'lmm game set-default'
```

### Games (`games.yaml`)

```yaml
games:
  skyrim-se:
    name: "Skyrim Special Edition"
    install_path: "/path/to/skyrim"
    mod_path: "/path/to/skyrim/Data"
    source_ids:
      nexusmods: "skyrimspecialedition"
```

## CLI Reference

### Global Flags

| Flag        | Short | Description                                              |
| ----------- | ----- | -------------------------------------------------------- |
| `--game`    | `-g`  | Game ID (optional if default set via `game set-default`) |
| `--verbose` | `-v`  | Enable verbose output                                    |
| `--config`  |       | Custom config directory                                  |
| `--data`    |       | Custom data directory                                    |

### Commands

| Command                                | Description                      |
| -------------------------------------- | -------------------------------- |
| `lmm search <query>`                   | Search for mods                  |
| `lmm install <query>`                  | Search and install a mod         |
| `lmm install --id <mod-id>`            | Install by mod ID                |
| `lmm uninstall <mod-id>`               | Uninstall a mod                  |
| `lmm list`                             | List installed mods              |
| `lmm status`                           | Show current status              |
| `lmm update`                           | Check for and apply auto-updates |
| `lmm update <mod-id>`                  | Update a specific mod            |
| `lmm update --all`                     | Apply all available updates      |
| `lmm update --dry-run`                 | Preview what would update        |
| `lmm update rollback <mod-id>`         | Rollback to previous version     |
| `lmm mod set-update <mod-id> --auto`   | Enable auto-updates for mod      |
| `lmm mod set-update <mod-id> --notify` | Notify only (default)            |
| `lmm mod set-update <mod-id> --pin`    | Pin mod to current version       |
| `lmm game set-default <game-id>`       | Set the default game             |
| `lmm game show-default`                | Show current default game        |
| `lmm game clear-default`               | Clear the default game setting   |
| `lmm auth login`                       | Authenticate with NexusMods      |
| `lmm auth logout`                      | Remove stored credentials        |
| `lmm auth status`                      | Show authentication status       |
| `lmm profile list`                     | List profiles                    |
| `lmm profile create <name>`            | Create a profile                 |
| `lmm profile switch <name>`            | Switch to a profile              |
| `lmm profile delete <name>`            | Delete a profile                 |
| `lmm profile export <name>`            | Export profile to YAML           |
| `lmm profile import <file>`            | Import profile from YAML         |

## Architecture

```
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

| Type      | Path                        |
| --------- | --------------------------- |
| Config    | `~/.config/lmm/`            |
| Database  | `~/.local/share/lmm/lmm.db` |
| Mod Cache | `~/.local/share/lmm/cache/` |

## Roadmap

- [x] NexusMods authentication and downloads
- [x] Update management with policies and rollback
- [x] Default game setting (avoid --game on every command)
- [ ] Interactive TUI (Bubble Tea) - see BACKLOG.md
- [ ] Additional mod sources (CurseForge, ESOUI)
- [ ] Conflict detection and resolution
- [ ] Game auto-detection (Steam, Lutris, Heroic)
- [ ] Mod file verification (checksums)
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
