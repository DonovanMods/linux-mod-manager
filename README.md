# lmm - Linux Mod Manager

A terminal-based mod manager for Linux that provides a CLI interface for searching, installing, updating, and managing game mods from various sources.

## Features

- **Multi-Source Support**: Search, download, install mods from NexusMods and CurseForge
- **Profile System**: Manage multiple mod configurations per game
- **Update Management**: Check for updates with configurable policies (auto, notify, pinned)
- **Rollback Support**: Revert to previous mod versions when updates cause issues
- **Flexible Deployment**: Symlink, hardlink, or copy mods to game directories
- **Dependency Resolution**: Automatically fetches and installs mod dependencies
- **Paginated Search**: Browse results page-by-page with clean cancel support
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

Mod sources require API keys for downloading mods.

#### NexusMods

Get your personal API key from [NexusMods API settings](https://www.nexusmods.com/users/myaccount?tab=api):

```bash
lmm auth login nexusmods
# Or set the environment variable
export NEXUSMODS_API_KEY="your-api-key"
```

#### CurseForge

Get your API key from [CurseForge Console](https://console.curseforge.com/):

```bash
lmm auth login curseforge
# Or set the environment variable
export CURSEFORGE_API_KEY="your-api-key"
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

**Source auto-detection:** Commands automatically use the mod source configured for your game. If a game has multiple sources, you will be prompted to choose (or use `-y` to auto-select, or `--source` to specify explicitly).

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

### Terminal UI

Browse your configured game, installed mods, and profiles interactively, and search mod sources:

```bash
lmm tui                     # real data, read-only
lmm tui --theme amber       # themes: wizardry (default), amber, dos, green
lmm tui --prototype         # demo mode with static fake data
```

Keys: `tab`/`h`/`l` cycle screens, `1`–`4` jump (`3` alone jumps to Search without
focusing input), `↑↓`/`j`/`k` move, `enter` open/select,
`/` searches from anywhere (jumps to the Search screen and focuses the input;
type query, `enter` to search, `esc` returns to navigation),
`n`/`p` next/previous page, `s` cycle sources, `?` help, `q` quit.
Results mark already-installed mods; selecting a result shows a detail panel.
Browsing and searching are read-only — install/update/deploy actions from the TUI arrive in a later release.

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

## Custom Sources

In addition to built-in mod sources (NexusMods, CurseForge), lmm lets you declare custom sources in YAML files instead of writing code. The `directory` type is fully implemented — point it at a local folder of mods and use it from `search`/`install` like any built-in source. `manifest` and `api` definitions parse and validate today, but their source types (fetching mods over HTTP) ship in later releases.

Custom source definitions are loaded from `~/.config/lmm/sources/*.yaml` (or `*.yml`). Each file must define exactly one source. Broken definition files are skipped with a warning — they never prevent lmm from starting.

### Source Definition Format

```yaml
id: donovan-mods              # required; must match ^[a-z0-9-]+$ and be unique
name: Donovan's 7D2D Modlets  # required display name
type: directory               # required: directory (local folders) | manifest | api
allow_http: false             # optional; permit http:// URLs (default false)

# Type-specific configuration (one block required, must match type)
directory:
  path: ~/Projects/mods/7dtd/donovan-7d2d-modlets
```

### Common Fields

| Field       | Type    | Required | Description                                                                         |
| ----------- | ------- | -------- | ----------------------------------------------------------------------------------- |
| `id`        | string  | yes      | Unique source identifier; must contain only lowercase letters, numbers, and hyphens |
| `name`      | string  | yes      | Display name shown in source lists and commands                                     |
| `type`      | string  | yes      | Source type: `directory`, `manifest`, or `api`. All three parse and validate today; `directory` support (search/install) is implemented now, `manifest`/`api` ship in later releases. |
| `allow_http` | boolean | no       | If `true`, allow unencrypted http:// URLs (default `false`, HTTPS only)            |

### Directory Sources

A `directory` source scans a local folder every time it's queried — no indexing, no caching of the listing — so edits to the folder show up immediately without restarting lmm. Each entry directly under the configured path becomes one mod:

- A **subdirectory** is a mod whose contents are used as-is.
- A **`.zip` or `.jar` file** is a mod whose archive is extracted like any downloaded mod.
- Anything else (loose files, `README.md`, `LICENSE.md`, other subfolders that aren't mods, etc.) is ignored by the scan but still shows up as a listed entry if it happens to be a directory — see the note on metadata fallback below.
- **Dot-prefixed entries** (`.git`, `.DS_Store`, dotfiles, ...) are always ignored, whether they're a directory or a file.

Because every non-hidden subdirectory becomes an entry, point `directory.path` at a folder dedicated to mods (as in the example below) rather than something like a repository root that also holds unrelated project files — those would otherwise show up as listed (if harmless) entries in `search`/`lmm source list` output.

```yaml
id: donovan-mods
name: Donovan's 7D2D Modlets
type: directory
directory:
  path: ~/Projects/mods/7dtd/donovan-7d2d-modlets
```

**Metadata resolution** — for each subdirectory, lmm resolves name/version/summary/author in this order:

1. **`ModInfo.xml`** (7 Days to Die's mod metadata format), if present. Both layouts are supported:
   - **V2**: fields directly under `<xml>` — `<xml><Name value="..."/><Version value="..."/>...</xml>`
   - **V1**: fields nested in `<ModInfo>` — `<xml><ModInfo><Name value="..."/>...</ModInfo></xml>`
2. **Dirname parsing**, if no `ModInfo.xml` (or it fails to parse): the directory name is split into a name and version, e.g. `PlainMod-0.5` → name `PlainMod`, version `0.5`. If no version-like suffix is found, the whole name is used as-is and the version is empty.

Archive files (`.zip`/`.jar`) get the same metadata resolution: lmm looks for `ModInfo.xml` inside the archive (at its root or exactly one directory deep, e.g. `donovan-aio.zip` containing `donovan-aio/ModInfo.xml`) before falling back to dirname-style parsing on the filename.

**The mod ID is the directory (or archive) name, verbatim.** There is no separate ID field — `BiggerBackpack/` is mod `BiggerBackpack`. This means **renaming the directory creates a new mod identity**: lmm has no way to know `BiggerBackpack/` and `Bigger-Backpack/` are the same mod, so a rename shows up as the old mod disappearing (update checks silently stop finding it) and a new, unrelated mod appearing. Keep directory names stable once you've installed from them.

Directory sources support search, file listing, downloads (via local copy, no network), and update checks. They do not support dependency resolution (`GetDependencies` returns "not supported") since there's no manifest to declare dependencies from.

To use a directory source with a specific game, map it under that game's `sources:` block in `games.yaml` (the value is ignored — directory sources apply to any game that maps them — but the key must be present):

```yaml
games:
  7daystodie:
    sources:
      nexusmods: 7daystodie
      donovan-mods: "" # directory sources ignore this value
```

### Source Management Commands

List all sources (built-in and custom):

```bash
lmm source list
```

Output:

```
ID            NAME                    TYPE       AUTH  CAPABILITIES              ERROR
nexusmods     Nexus Mods              built-in   yes   search,deps,updates,auth
curseforge    CurseForge              built-in   yes   search,deps,updates,auth
donovan-mods  Donovan's 7D2D Modlets  directory  n/a   search,updates
```

Validate a source definition file before use:

```bash
lmm source validate ~/.config/lmm/sources/my-source.yaml
```

On success:
```
~/.config/lmm/sources/my-source.yaml: valid (directory source "my-source")
```

On error (exits with code 1):
```
Error: invalid definition: id "my-bad-source!" must match ^[a-z0-9-]+$
```

### Adding a Custom Source

1. Create `~/.config/lmm/sources/` if it doesn't exist:
   ```bash
   mkdir -p ~/.config/lmm/sources
   ```

2. Create a YAML file with your source definition:
   ```bash
   cat > ~/.config/lmm/sources/my-mods.yaml <<'EOF'
   id: my-local-mods
   name: My Local Mods
   type: directory
   directory:
     path: ~/projects/mods
   EOF
   ```

3. Validate the definition:
   ```bash
   lmm source validate ~/.config/lmm/sources/my-mods.yaml
   ```

4. Map it under the game(s) that should use it in `games.yaml` (see [Directory Sources](#directory-sources) above):
   ```yaml
   games:
     skyrim-se:
       sources:
         nexusmods: skyrimspecialedition
         my-local-mods: ""
   ```

5. Search and install from it like any built-in source:
   ```bash
   lmm search bigger -g skyrim-se --source my-local-mods
   lmm install --source my-local-mods --id BiggerBackpack -g skyrim-se
   ```

A `directory` source now shows up with real capabilities in `lmm source list` (`search,updates`, `auth=n/a`), and it will show as an `error` row if the configured path is missing or not a directory. A definition whose `id` collides with an already-registered source (a built-in, or another definition) also produces an `error` row (`id already in use`); the source that was already registered keeps its original row and type unchanged.

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

| Command                                | Description                                          |
| -------------------------------------- | ---------------------------------------------------- |
| `lmm search <query>`                   | Search for mods                                      |
| `lmm search <query> --category ID`     | Filter by NexusMods category                         |
| `lmm search <query> --tag TAG`         | Filter by tag (repeat for multiple)                  |
| `lmm install <query>`                  | Search and install a mod                             |
| `lmm install --id <mod-id>`            | Install by mod ID                                    |
| `lmm uninstall <mod-id>`               | Uninstall a mod                                      |
| `lmm list`                             | List installed mods                                  |
| `lmm list --profiles`                  | List profiles for the game                           |
| `lmm status`                           | Show current status                                  |
| `lmm update`                           | Check for and apply auto-updates                     |
| `lmm update <mod-id>`                  | Update a specific mod                                |
| `lmm update --all`                     | Apply all available updates                          |
| `lmm update --dry-run`                 | Preview what would update                            |
| `lmm update rollback <mod-id>`         | Rollback to previous version                         |
| `lmm verify`                           | Verify cached mod files (see below)                  |
| `lmm verify --fix`                     | Re-download missing or corrupted files               |
| `lmm mod enable <mod-id>`              | Enable a disabled mod                                |
| `lmm mod disable <mod-id>`             | Disable mod (keep in cache)                          |
| `lmm mod set-update <mod-id> --auto`   | Enable auto-updates for mod                          |
| `lmm mod set-update <mod-id> --notify` | Notify only (default)                                |
| `lmm mod set-update <mod-id> --pin`    | Pin mod to current version                           |
| `lmm mod show <mod-id>`                | Show mod details (description, image, etc.)          |
| `lmm mod files <mod-id>`               | List files deployed by mod                           |
| `lmm game set-default <game-id>`       | Set the default game                                 |
| `lmm game show-default`                | Show current default game                            |
| `lmm game clear-default`               | Clear the default game setting                       |
| `lmm auth login [source]`              | Authenticate with a source (nexusmods or curseforge) |
| `lmm auth logout`                      | Remove stored credentials                            |
| `lmm auth status`                      | Show authentication status                           |
| `lmm profile list`                     | List profiles                                        |
| `lmm profile create <name>`            | Create a profile                                     |
| `lmm profile switch <name>`            | Switch to a profile (installs missing mods)          |
| `lmm profile delete <name>`            | Delete a profile                                     |
| `lmm profile export <name>`            | Export profile to YAML                               |
| `lmm profile import <file>`            | Import profile from YAML                             |
| `lmm profile import <file> --force`    | Import and overwrite existing                        |
| `lmm profile reorder [mod-id ...]`     | Show or set load order                               |
| `lmm profile sync`                     | Update profile to match installed mods               |
| `lmm profile apply`                    | Install/enable mods to match profile                 |
| `lmm deploy`                           | Deploy all enabled mods from cache                   |
| `lmm deploy <mod-id>`                  | Deploy specific mod from cache                       |
| `lmm deploy --method hardlink`         | Deploy using different link method                   |
| `lmm deploy --purge`                   | Purge then deploy all mods                           |
| `lmm purge`                            | Remove all mods from game directory                  |
| `lmm conflicts`                        | Show file conflicts in current profile               |
| `lmm source list`                      | List built-in and user-defined mod sources           |
| `lmm source validate <file>`           | Validate a user-defined source definition           |

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
│   ├── nexusmods/        # NexusMods API client
│   └── curseforge/       # CurseForge API client
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

| Type           | Path                                   |
| -------------- | --------------------------------------- |
| Config         | `~/.config/lmm/`                       |
| Custom Sources | `~/.config/lmm/sources/*.yaml`         |
| Database       | `~/.local/share/lmm/lmm.db`            |
| Mod Cache      | `~/.local/share/lmm/cache/` (default)  |

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
- [x] CurseForge integration
- [ ] Additional mod sources (ESOUI)
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
- [CurseForge](https://www.curseforge.com/) - Mod hosting platform
