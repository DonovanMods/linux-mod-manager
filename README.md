# lmm - Linux Mod Manager

A terminal-based mod manager for Linux that provides both TUI and CLI interfaces for searching, installing, updating, and managing game mods from various sources.

## Features

- **Dual Interface**: Interactive TUI for browsing and CLI for scripting
- **NexusMods Integration**: Search and install mods from NexusMods
- **Profile System**: Manage multiple mod configurations per game
- **Flexible Deployment**: Symlink, hardlink, or copy mods to game directories
- **Update Management**: Check for updates with configurable policies (notify, auto, pinned)
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

### Interactive Mode (TUI)

Launch the interactive terminal UI:

```bash
lmm
```

Navigate with arrow keys or vim bindings (j/k). Press `?` for help.

### CLI Mode

```bash
# Search for mods
lmm search skyui --game skyrim-se

# Install a mod
lmm install 12345 --game skyrim-se

# List installed mods
lmm list --game skyrim-se

# Check for updates
lmm update --game skyrim-se

# Show status
lmm status
```

## Configuration

Configuration files are stored in `~/.config/lmm/`:

### Main Config (`config.yaml`)

```yaml
default_link_method: symlink # symlink, hardlink, or copy
default_source: nexusmods
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

| Flag        | Short | Description             |
| ----------- | ----- | ----------------------- |
| `--game`    | `-g`  | Game ID to operate on   |
| `--verbose` | `-v`  | Enable verbose output   |
| `--config`  |       | Custom config directory |
| `--data`    |       | Custom data directory   |

### Commands

| Command                     | Description               |
| --------------------------- | ------------------------- |
| `lmm`                       | Launch interactive TUI    |
| `lmm search <query>`        | Search for mods           |
| `lmm install <mod-id>`      | Install a mod             |
| `lmm uninstall <mod-id>`    | Uninstall a mod           |
| `lmm update [mod-id]`       | Check for / apply updates |
| `lmm list`                  | List installed mods       |
| `lmm status`                | Show current status       |
| `lmm profile list`          | List profiles             |
| `lmm profile create <name>` | Create a profile          |
| `lmm profile switch <name>` | Switch to a profile       |
| `lmm profile delete <name>` | Delete a profile          |
| `lmm profile export <name>` | Export profile to YAML    |
| `lmm profile import <file>` | Import profile from YAML  |

## Architecture

```
cmd/lmm/                  # CLI entry point (Cobra)
internal/
├── domain/               # Core types (Mod, Profile, Game)
├── source/               # Mod source abstraction
│   └── nexusmods/        # NexusMods GraphQL client
├── storage/
│   ├── db/               # SQLite storage
│   ├── config/           # YAML configuration
│   └── cache/            # Mod file cache
├── linker/               # Deployment strategies
├── core/                 # Business logic
└── tui/                  # Bubble Tea interface
    └── views/            # TUI screens
```

## File Locations

| Type      | Path                        |
| --------- | --------------------------- |
| Config    | `~/.config/lmm/`            |
| Database  | `~/.local/share/lmm/lmm.db` |
| Mod Cache | `~/.local/share/lmm/cache/` |

## Roadmap

- [ ] NexusMods authentication and downloads
- [ ] Additional mod sources (CurseForge, ESOUI)
- [ ] Conflict detection and resolution
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

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) - TUI framework
- [Cobra](https://github.com/spf13/cobra) - CLI framework
- [NexusMods](https://www.nexusmods.com/) - Mod hosting platform
