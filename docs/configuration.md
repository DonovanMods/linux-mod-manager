# Configuration Reference

lmm uses YAML configuration files under `~/.config/lmm/` (or the directory set with `--config`).

## config.yaml

Global application settings. Optional; defaults apply if the file is missing.

| Option                | Type   | Default   | Description                                                       |
| --------------------- | ------ | --------- | ----------------------------------------------------------------- |
| `default_link_method` | string | `symlink` | How to deploy mods: `symlink`, `hardlink`, or `copy`              |
| `default_game`        | string | (empty)   | Game ID to use when `--game` is not specified                     |
| `keybindings`         | string | `vim`     | Reserved for future TUI: `vim` or `standard`                      |
| `cache_path`          | string | (empty)   | Override default mod cache directory (`~/.local/share/lmm/cache`) |
| `hook_timeout`        | int    | 60        | Timeout in seconds for hook scripts                               |

## games.yaml

Defines moddable games. Each game is keyed by a unique slug (e.g. `skyrim-se`).

### Game options

| Option         | Type   | Required | Description                                                         |
| -------------- | ------ | -------- | ------------------------------------------------------------------- |
| `name`         | string | yes      | Display name                                                        |
| `install_path` | string | yes      | Game installation directory (supports `~`)                          |
| `mod_path`     | string | yes      | Directory where mods are deployed (supports `~`)                    |
| `sources`      | map    | yes      | Source ID to game domain ID, e.g. `nexusmods: skyrimspecialedition` |
| `link_method`  | string | no       | Override global link method: `symlink`, `hardlink`, `copy`          |
| `cache_path`   | string | no       | Per-game cache directory override                                   |
| `hooks`        | object | no       | Scripts to run around install/uninstall (see below)                 |

### Hooks (games.yaml)

Under each game, optional `hooks`:

```yaml
hooks:
  install:
    before_all: "/path/to/script.sh" # Before any mod is installed
    before_each: "/path/to/script.sh" # Before each mod
    after_each: "/path/to/script.sh" # After each mod
    after_all: "/path/to/script.sh" # After all mods
  uninstall:
    before_all: "/path/to/script.sh"
    before_each: "/path/to/script.sh"
    after_each: "/path/to/script.sh"
    after_all: "/path/to/script.sh"
```

Scripts receive environment variables: `LMM_GAME_ID`, `LMM_GAME_PATH`, `LMM_MOD_PATH`, `LMM_MOD_ID`, `LMM_MOD_NAME`, `LMM_MOD_VERSION`, `LMM_HOOK`. Use `--no-hooks` to disable all hooks at runtime; `--force` to continue when a hook fails.

## Profile files

Profiles are stored under `~/.config/lmm/games/<game-id>/profiles/<name>.yaml`.

| Option        | Type   | Description                                                         |
| ------------- | ------ | ------------------------------------------------------------------- |
| `name`        | string | Profile name                                                        |
| `game_id`     | string | Game this profile belongs to                                        |
| `mods`        | list   | Mod references (source_id, mod_id, version, file_ids) in load order |
| `link_method` | string | Optional override                                                   |
| `is_default`  | bool   | Whether this is the default profile for the game                    |

## steam-games.yaml (optional)

Used by `lmm game detect` to know which Steam games are moddable. The app ships with a built-in list; you can add or override entries by creating:

**`~/.config/lmm/steam-games.yaml`**

Format: Steam App ID (string) as key, then `slug`, `name`, `nexus_id`, `mod_path` (relative to game install, empty for game root). Example:

```yaml
"489830":
  slug: skyrim-se
  name: Skyrim Special Edition
  nexus_id: skyrimspecialedition
  mod_path: Data
"1234567":
  slug: my-game
  name: My Game
  nexus_id: mygame
  mod_path: ""
```

Entries here are merged with the built-in list (overrides win). No rebuild needed to support more games.

## File locations

| Path                                            | Description                                            |
| ----------------------------------------------- | ------------------------------------------------------ |
| `~/.config/lmm/config.yaml`                     | Global config                                          |
| `~/.config/lmm/games.yaml`                      | Game definitions                                       |
| `~/.config/lmm/steam-games.yaml`                | Optional: Steam games for `game detect` (add/override) |
| `~/.config/lmm/games/<game-id>/profiles/*.yaml` | Per-game profiles                                      |
| `~/.local/share/lmm/lmm.db`                     | SQLite database (metadata, tokens)                     |
| `~/.local/share/lmm/cache/`                     | Mod file cache (or `cache_path` override)              |
