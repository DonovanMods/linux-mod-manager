# Configuration Reference

lmm uses YAML configuration files under `~/.config/lmm/` (or the directory set with `--config`).

`link_method` and `deploy_mode` fields (in `config.yaml`, `games.yaml`, and profile files) are validated at load time: leaving one unset keeps its documented default, but a value that doesn't exactly match one of the listed options — a typo like `deploy_mode: compil` — is a load-time error naming the field, the offending value, and the valid options, not a silent fallback.

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

| Option         | Type   | Required | Description                                                           |
| -------------- | ------ | -------- | --------------------------------------------------------------------- |
| `name`         | string | yes      | Display name                                                          |
| `install_path` | string | yes      | Game installation directory (supports `~`)                            |
| `mod_path`     | string | yes      | Directory where mods are deployed (supports `~`)                      |
| `sources`      | map    | yes      | Source ID to game ID mapping (see below)                              |
| `link_method`  | string | no       | Override global link method: `symlink`, `hardlink`, `copy`            |
| `cache_path`   | string | no       | Per-game cache directory override                                     |
| `hooks`        | object | no       | Scripts to run around install/uninstall (see below)                   |
| `deploy_mode`  | string | no       | How to handle mod archives: `extract` (default), `copy`, or `compile` |

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

### Deploy Mode (games.yaml)

The `deploy_mode` option controls how downloaded mod archives are handled:

- **`extract`** (default): Archives are extracted to the mod path. Use for games where mods are loose files (e.g., Skyrim, Fallout).
- **`copy`**: Archives are copied as-is to the mod path without extraction. Use for games that expect mod files to remain as archives (e.g., Minecraft `.jar` files, some Unity games).
- **`compile`**: The downloaded file is compiled into a new artifact before caching (currently Icarus only: an `.exmodz` diff is applied to the game's base data tables to produce a deployable `_P.pak`). Only sources that implement compiling support this mode. The base data tables are read directly from the installed game's own `data.pak`, so a compile always matches the installed game version and needs no network access.

Example:

```yaml
games:
  minecraft:
    name: "Minecraft"
    install_path: "~/.minecraft"
    mod_path: "~/.minecraft/mods"
    deploy_mode: copy # Keep .jar files as-is
    sources:
      curseforge: "432"
```

## Profile files

Profiles are stored under `~/.config/lmm/games/<game-id>/profiles/<name>.yaml`.

| Option        | Type   | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| ------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `name`        | string | Profile name                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `game_id`     | string | Game this profile belongs to                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| `mods`        | list   | Mod references (source_id, mod_id, version, file_ids) in load order                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| `link_method` | string | Optional override (symlink, hardlink, copy). Wins over the game-level `link_method` (`games.yaml`) and the global `default_link_method` (`config.yaml`) for every deploy into this profile; only an explicit CLI `--method` flag beats it ([#81](https://github.com/DonovanMods/linux-mod-manager/issues/81)). **Upgrade note:** profiles saved before v1.14.1 may carry an unintended `link_method: symlink` line (a save bug wrote it into every profile); it now takes effect and will override a per-game `hardlink`/`copy` setup. If `lmm status <game>` shows an unexpected `(per-profile)` method, delete that line from the profile file. |
| `is_default`  | bool   | Whether this is the default profile for the game                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `hooks`       | object | Optional profile-level hook overrides (same structure as game hooks)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| `overrides`   | map    | Optional config overrides: path (relative to game install) → file content (INI tweaks, etc.). Applied on switch/deploy.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |

### Portable export format

`lmm profile export <name>` writes a portable YAML format suitable for sharing or backup. The same format is accepted by `lmm profile import <file>`.

Exported YAML includes:

- **name**, **game_id** – Profile identifier and game.
- **mods** – List of mod references in load order; each has `source_id`, `mod_id`, optional `version`, optional `file_ids`.
- **link_method** – Optional: symlink, hardlink, or copy. Preserved through export/import and honored at deploy time as the profile-level override (profile > game > global; see the Profile files table above).
- **overrides** – Optional map of relative paths (under game install) to file contents (e.g. INI tweaks). Applied when switching to the profile or deploying.

Import preserves load order, link method, and overrides; missing mods can be installed when you switch to or apply the profile.

## steam-games.yaml (optional)

Used by `lmm game detect` to know which Steam games are moddable. The app ships with a built-in list; you can add or override entries by creating:

**`~/.config/lmm/steam-games.yaml`**

Format: Steam App ID (string) as key, then `slug`, `name`, `mod_path` (relative to game install, empty for game root), optional `nexus_id` (omit for a game with no NexusMods presence), and two more optional fields, `deploy_mode` and `sources`, that pass straight through to the generated `games.yaml` entry's own `deploy_mode`/`sources` (omit both for the default `{nexusmods: <nexus_id>}` sources map and `extract` deploy mode every entry got before these existed). Example:

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
"7654321":
  slug: my-compile-game
  name: My Compile-Mode Game
  mod_path: Mods
  deploy_mode: compile
  sources:
    mysource: my-compile-game
```

Entries here are merged with the built-in list (overrides win). No rebuild needed to support more games.

## File locations

| Path                                            | Description                                                             |
| ----------------------------------------------- | ----------------------------------------------------------------------- |
| `~/.config/lmm/config.yaml`                     | Global config                                                           |
| `~/.config/lmm/games.yaml`                      | Game definitions                                                        |
| `~/.config/lmm/steam-games.yaml`                | Optional: Steam games for `game detect` (add/override)                  |
| `~/.config/lmm/sources/*.yaml`                  | Custom source definitions (see [Custom Sources](#custom-sources) below) |
| `~/.config/lmm/games/<game-id>/profiles/*.yaml` | Per-game profiles                                                       |
| `~/.local/share/lmm/lmm.db`                     | SQLite database (metadata, tokens)                                      |
| `~/.local/share/lmm/cache/`                     | Mod file cache (or `cache_path` override)                               |
| `~/.local/share/lmm/downloads/`                 | Staging area for in-flight downloads and archive extraction             |

## Custom Sources

In addition to the built-in sources below (NexusMods, CurseForge), lmm can load user-defined sources from `~/.config/lmm/sources/*.yaml` — `directory` (a local folder of mods), `manifest` (a JSON/YAML mod list), and `api` (a declarative REST API). This file only lists the built-in sources' `games.yaml` conventions; the custom-source YAML format, field reference, and authentication are documented in the README's **[Custom Sources](../README.md#custom-sources)** section.

## Mod Sources

lmm supports multiple mod sources. Each source uses its own game identifier:

### NexusMods

- **Source ID:** `nexusmods`
- **Game ID format:** Game domain slug (e.g., `skyrimspecialedition`, `minecraft`)
- **Auth:** API key from [NexusMods API settings](https://www.nexusmods.com/users/myaccount?tab=api)
- **Env var:** `NEXUSMODS_API_KEY`

### CurseForge

- **Source ID:** `curseforge`
- **Game ID format:** Numeric game ID (e.g., `432` for Minecraft, `1` for WoW)
- **Auth:** API key from [CurseForge Console](https://console.curseforge.com/)
- **Env var:** `CURSEFORGE_API_KEY`

### Example games.yaml with multiple sources

```yaml
games:
  minecraft:
    name: "Minecraft"
    install_path: "~/.minecraft"
    mod_path: "~/.minecraft/mods"
    sources:
      nexusmods: "minecraft"
      curseforge: "432" # or use slug: "minecraft"

  skyrim-se:
    name: "Skyrim Special Edition"
    install_path: "~/.steam/steam/steamapps/common/Skyrim Special Edition"
    mod_path: "~/.steam/steam/steamapps/common/Skyrim Special Edition/Data"
    sources:
      nexusmods: "skyrimspecialedition"
```

### Source Auto-Detection

When running commands like `search`, `install`, or `update`, lmm automatically detects which source to use:

1. **Single source:** If the game has only one source configured, it is used automatically.
2. **Multiple sources:** If the game has multiple sources, you are prompted to select one.
3. **Explicit override:** Use `--source <name>` to bypass auto-detection.
4. **Scripting mode:** Use `-y` (on install) to auto-select the first configured source without prompting.

Example prompt when multiple sources are configured:

```
Minecraft has multiple mod sources configured. Select one:
  [1] CurseForge
  [2] NexusMods
Enter choice (1-2):
```
