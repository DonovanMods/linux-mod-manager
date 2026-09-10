# Configuration Reference

lmm uses YAML configuration files under `$XDG_CONFIG_HOME/lmm/` — `~/.config/lmm/` by default — or the directory set with `--config`.

`link_method` and `deploy_mode` fields (in `config.yaml`, `games.yaml`, and profile files) are validated at load time: leaving one unset keeps its documented default, but a value that doesn't exactly match one of the listed options — a typo like `deploy_mode: compil` — is a load-time error naming the field, the offending value, and the valid options, not a silent fallback.

## config.yaml

Global application settings. Optional; defaults apply if the file is missing.

| Option                | Type   | Default   | Description                                                                                          |
| --------------------- | ------ | --------- | ---------------------------------------------------------------------------------------------------- |
| `default_link_method` | string | `symlink` | How to deploy mods: `symlink`, `hardlink`, or `copy`                                                 |
| `default_game`        | string | (empty)   | Game ID to use when `--game` is not specified                                                        |
| `keybindings`         | string | (unset)   | Ignored. Kept so existing config files that set it still parse (it was reserved for the removed TUI); lmm never writes it |
| `cache_path`          | string | (empty)   | Override default mod cache directory (`<data dir>/cache`)                                            |
| `hook_timeout`        | int    | 60        | Timeout in seconds for hook scripts                                                                  |
| `auto_snapshot`       | bool   | `false`   | Record a snapshot before every deploy, profile switch and update (see below)                         |
| `auto_snapshot_keep`  | int    | `10`      | How many AUTOMATIC snapshots to keep per game; `0` means unlimited (see below)                       |

### `auto_snapshot`

With `auto_snapshot: true`, lmm records a snapshot named
`auto-<op>-<timestamp>` before each deploy, profile switch and update — the
three operations that change what is in the game directory.

It is **off by default**. Creating a snapshot hashes the whole deployed
tree, which on a large install is real work to do on every deploy, and a
user who wants the safety net can say so once. An automatic snapshot that
FAILS is reported as a warning and the operation continues: a backup that
blocks the thing it is protecting is worse than no backup.

Automatic snapshots are listed by `lmm snapshot list` like any other, marked
`(auto)`, and are deleted the same way.

### `auto_snapshot_keep`

Automatic snapshots are **pruned**: each time lmm records one, the oldest
automatic snapshots beyond `auto_snapshot_keep` (default `10`) are deleted.
Set it to `0` for unlimited.

Only automatic snapshots are ever pruned — one you named with
`lmm snapshot create --name` is yours until you delete it — and the
originals store is never touched by a prune: it lives outside the
snapshot-name namespace entirely (`_originals/`), and it holds the only
copy of what it holds.

### What a snapshot does and does not contain

A snapshot never contains mod bytes. It records the profile (load order and
locks), the installed versions and per-mod settings, the deployed files with
their checksums, and the originals in force — a few kilobytes. The mod files
themselves are in the mod cache (`<data>/cache/...`).

So a restore needs, for each mod it puts back: the cached files for the
**recorded version** if they are still there, and otherwise a download of
that exact version from the mod's source. A version the source can no longer
serve — deleted, hidden, or superseded with the old file removed — is
reported as a refusal in the preview, before anything is touched. Clearing
the cache does not invalidate a snapshot, but it does make restoring it need
the network, and a source that has dropped a version can no longer supply
it at all.

The one thing a snapshot restore does NOT need from anywhere is the stock
game content lmm replaced: those bytes are in the originals store, which is
the only copy of them and is never pruned or deleted by lmm.

Removing the mod that replaced a file puts that file back on its own — an
uninstall, a purge, an update that drops a file the previous version
shipped, a rolled-back install, or the deploy-time convergence — and the
stored copy is dropped only once the original is back in place. `lmm
snapshot restore` remains the whole-state path, and the one that can put
back everything at once.


## games.yaml

Defines moddable games. Each game is keyed by a unique slug (e.g. `skyrim-se`).

### Game options

| Option         | Type   | Required | Description                                                           |
| -------------- | ------ | -------- | --------------------------------------------------------------------- |
| `name`         | string | yes      | Display name                                                          |
| `install_path` | string | yes      | Game installation directory (supports `~`)                            |
| `mod_path`     | string | yes      | Directory where mods are deployed (supports `~`; see below)           |
| `sources`      | map    | yes      | Source ID to game ID mapping (see below)                              |
| `link_method`  | string | no       | Override global link method: `symlink`, `hardlink`, `copy`            |
| `cache_path`   | string | no       | Per-game cache directory override                                     |
| `hooks`        | object | no       | Scripts to run around install/uninstall (see below)                   |
| `deploy_mode`  | string | no       | How to handle mod archives: `extract` (default), `copy`, or `compile` |

#### `mod_path` and relative values

`mod_path` may be **absolute, or relative to `install_path` — everywhere**.
A relative value — `mod_path: Data` — is resolved against that game's
`install_path`, never against the directory you happen to run `lmm` from,
so the entry means the same thing from every shell. `~` is expanded first,
so `~/mods` is an absolute path, not a relative one.

The same rule applies on every path that *writes* a game, not just to a
hand-written file: `lmm game add`, `lmm game add --from-detected`,
`lmm game detect`, `POST /api/v1/games` and the web UI's add-game form all
accept `Data` and store the resolved absolute path, so the file lmm writes
reads back identically from any working directory.

A relative `mod_path` needs an `install_path` to resolve against, so an
entry that carries one without the other is refused — when `games.yaml` is
read, naming the game and the field, and at the prompt/form/API, where the
refusal names `install_path`, the value that is actually missing. The
alternative is the CWD-relative behaviour this rule exists to end, applied
silently.

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
- **`compile`**: The downloaded `.exmodz` is validated and retained (currently Icarus only); at deploy time, every enabled compile-mode mod's changes are merged — in profile load order — into one profile-level `zzz_LMM_Merged_P.pak` built against the installed game's own base tables. Only sources that implement compiling support this mode. The base data tables are read directly from the installed game's own `data.pak`, so a compile always matches the installed game version and needs no network access. When a mod publishes both a prebuilt pak and an `.exmodz`, lmm installs the `.exmodz` by default; the pak remains selectable explicitly (CLI chooser or `--file pak`), and installing both together is rejected as they are alternate forms of the same mod.

**Merge precedence**: with more than one `compile`-mode mod installed, the merge applies each mod's changes in the profile's load order (the `mods` list's order - see [Profile files](#profile-files) below, and the same order `lmm list` displays) against the same evolving base tables, so a mod later in the load order is applied later. Table-row conflicts compose at the _field_ level: an upsert, not a whole-row overwrite, so two mods patching different fields of the same row - or different rows entirely - both survive; only a genuine same-row-same-field write is last-wins, which is an expected outcome of ordinary upserts, not something that gets a warning. Bundled asset files can't compose that way - a same-path asset collision between two mods is necessarily whole-file last-wins, and is reported as a warning (installing or updating a colliding mod prints it). Either way, the mod at the bottom of the load order has final say, and reordering the profile (`lmm profile reorder`) regenerates the merged pak immediately, so the new precedence takes effect right away.

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

Profiles are stored under `<config>/games/<game-id>/profiles/<name>.yaml`.

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

**`<config>/steam-games.yaml`**

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

### Fields

| Field         | Required | Meaning                                                                                                                                                        |
| ------------- | -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| _(the key)_   | yes      | The Steam App ID, **quoted** so it stays a string. It is on the store page URL (`store.steampowered.com/app/<id>/`).                                            |
| `slug`        | yes      | The lmm game id the entry creates. Lowercase alphanumerics in dash-separated runs, and unique across the whole list.                                            |
| `name`        | yes      | Display name. Use the title exactly as the Steam store page writes it.                                                                                         |
| `mod_path`    | yes      | The mod folder, **relative to the game's install directory** — `""` means the install root. Detection joins it to the install path it found.                    |
| `nexus_id`    | no       | The game's NexusMods domain: the path segment in `nexusmods.com/<domain>`. Omit it for a game with no NexusMods page.                                           |
| `deploy_mode` | no       | `extract` (the default), `copy` or `compile`.                                                                                                                    |
| `sources`     | no       | A full source id → per-source game id map, for a game whose sources are not just NexusMods. Omitting it means `{nexusmods: <nexus_id>}`.                        |

An entry must name at least one source — `nexus_id`, a `sources` map, or
both. One that names neither produces a game lmm can add and then cannot
install anything for.

### Contributing an entry to the built-in list

The shipped list is `internal/source/steam/data/steam-games.yaml`. It is
curated from public documentation, one game at a time, and the bar is that
the facts are **verifiable**, not that the game is popular:

1. **Find the app id** on the game's Steam store page URL.
2. **Find the mod folder the community documents**, and check it is inside
   the game's install directory. A game whose mods live in your home
   directory — `%APPDATA%`, `~/Documents`, a Proton prefix — cannot be
   curated, because `mod_path` is install-relative. Leave it detect-only
   rather than inventing a path.
3. **Find the source ids.** The NexusMods domain is in the URL of the
   game's Nexus page. `steamworkshop` takes the Steam app id as its game
   id. Only source ids lmm registers are accepted.
4. **Write the entry with a comment above it** carrying the URL each fact
   came from — the game's Nexus page, its modding wiki, the mod loader's
   own install instructions. Every existing entry has one; it is what makes
   a wrong path fixable by the next person instead of re-researched.
5. **Add a row to the story test** in
   `internal/source/steam/curated_games_test.go`, which pins the entry
   field by field.

`TestKnownGamesListIsWellFormed` (in `internal/app`) then checks the whole
list on every build: quoted numeric app id, well-formed unique slug,
relative `mod_path`, a `deploy_mode` the domain parses, and every source id
one that lmm actually registers.

A game with no real modding ecosystem — or one whose mods do not live under
the install directory — stays **detect-only**: `lmm game detect
--include-unknown` still lists it with its app id, and `lmm game add` can
still configure it by hand. That is the honest answer, and it is preferred
over a guessed path.

## File locations

`<config>` is `$XDG_CONFIG_HOME/lmm` (default `~/.config/lmm`); `<data>` is `$XDG_DATA_HOME/lmm` (default `~/.local/share/lmm`).

**Precedence** (#297): `--config`/`--data` win outright; then an `XDG_CONFIG_HOME`/`XDG_DATA_HOME` set to an **absolute** path, whether or not that directory exists yet; then — only when the variable is unset or set to a relative path, which the XDG spec requires be ignored — the legacy `~/.config/lmm` / `~/.local/share/lmm`. Setting an XDG variable is treated as an explicit instruction, so lmm never writes into the legacy directory behind your back; move your data (or point `--data`/`--config` at it) when you adopt the XDG location.

| Path                                       | Description                                                             |
| ------------------------------------------ | ----------------------------------------------------------------------- |
| `<config>/config.yaml`                     | Global config                                                           |
| `<config>/games.yaml`                      | Game definitions                                                        |
| `<config>/steam-games.yaml`                | Optional: Steam games for `game detect` (add/override)                  |
| `<config>/sources/*.yaml`                  | Custom source definitions (see [Custom Sources](#custom-sources) below) |
| `<config>/games/<game-id>/profiles/*.yaml` | Per-game profiles                                                       |
| `<data>/lmm.db`                            | SQLite database (metadata, tokens)                                      |
| `<data>/.oplock`                           | Advisory mutation lock (#317) — see below                               |
| `<data>/cache/`                            | Mod file cache (or `cache_path` override)                               |
| `<data>/downloads/`                        | Staging area for in-flight downloads and archive extraction             |
| `<data>/key`                               | Token-encryption key (`0600`, created on first `auth login`)            |
| `<data>/snapshots/<game-id>/<name>.json`   | One snapshot's record (`lmm snapshot`)                                  |
| `<data>/snapshots/<game-id>/_originals/manifest.json` | Manifest of the files lmm has replaced for that game          |
| `<data>/snapshots/<game-id>/_originals/files/` | The replaced files themselves, split by root (`mod_path`/`install_path`) |

## Custom Sources

In addition to the built-in sources below (NexusMods, CurseForge), lmm can load user-defined sources from `<config>/sources/*.yaml` — `directory` (a local folder of mods), `manifest` (a JSON/YAML mod list), and `api` (a declarative REST API). This file only lists the built-in sources' `games.yaml` conventions; the custom-source YAML format, field reference, and authentication are documented in the README's **[Custom Sources](../README.md#custom-sources)** section.

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

## The mutation lock

Every lmm **mutation** — a CLI command or a `lmm serve` job — takes an advisory
`flock` on `<data>/.oplock` while it runs, so two lmm processes pointed at the same
data directory cannot interleave their work (#317). **Every** mutation, not only the
ones that touch the game directory: storing or removing an API key, adding or editing
a game, profile changes and saving a source definition all take it too, so
`lmm auth login` typed during a long `serve` deploy is refused rather than queued.
A second mutation waits up to two seconds and then refuses, naming the holder:

```text
another lmm operation is in progress (pid 4242, since 2026-09-09T12:00:00Z)
```

Under `--json` the same refusal carries `pid` and `started_at` in the error
envelope's `details`. **Reads never take the lock**, so listing, status and every
`GET /api/v1` route keep working while a mutation is in flight.

The lock lives in the open file descriptor, not in the file's contents, so the
kernel drops it the moment the process exits — a killed or crashed lmm never leaves
a stale lock to clear by hand, and the file itself can be deleted safely when no lmm
is running. Two installations (different `--data` directories) have separate lock
files and never contend.

**Two different waits, and how to tell them apart.** The refusal above is about a
*mutation* and comes from a command that had already started. A message at STARTUP —
`lmm: waiting for another lmm process to finish with the database (re-encrypting
stored credentials, up to 30s)…` — is a different thing: it is the one-time
credential re-encryption (#79) waiting for the database itself, before any mutation
lock is involved. The first says "another lmm is doing something right now"; the
second says "another lmm still has the database open while this one upgrades it".

## The verify memo

`lmm verify` always looks at the disk for real. The web UI's Health card, which
re-hydrates on every route change, job completion and profile switch, does not: core
keeps the last verify answer per game, profile and tier, and re-uses it while a cheap
fingerprint of what a run inspects is unchanged (#336). That fingerprint is the
profile's mods and their locks, the installed rows, the recorded file checksums a run
compares against, and a **stat-only** walk of the deployed tree — each file's path,
size and modification time.

A re-used answer keeps the `checked_at` of the run that produced it and carries
`cached: true`, which the card renders as "Unchanged since …". Every lmm mutation
drops the memo, and the card's **Re-verify** (`GET /api/v1/health?force=1`) forces a
real run. The recorded checksums are in the fingerprint for the cross-process case:
`lmm verify --fix` run from a terminal backfills them without touching the deployed
tree, so a running `lmm serve` has nothing else to notice, and its Health card would
otherwise keep reporting a warning that was repaired minutes ago.

**The limit**, stated plainly: size and modification time are not content. A deployed
file rewritten to the same length with its timestamp preserved — a restore from
backup, a tool that copies mtimes — fingerprints identically, and the memoised answer
stands until something else changes or a run is forced. The memo also cannot see
anything the full tier reads over the network, nor changes inside the cache
directory. Run `lmm verify` (or press Re-verify) whenever you want a guaranteed fresh
answer; neither ever uses the memo.
