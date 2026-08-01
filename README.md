# lmm - Linux Mod Manager

A terminal-based mod manager for Linux that provides a CLI interface for searching, installing, updating, and managing game mods from various sources.

## Features

- **Multi-Source Support**: Search, download, install mods from NexusMods and CurseForge
- **Profile System**: Manage multiple mod configurations per game
- **Update Management**: Check for updates with configurable policies (auto, notify, pinned)
- **Version Locking**: Lock a mod's profile entry to an exact version, independent of update policy — see [Locking mods to a version](#locking-mods-to-a-version)
- **Rollback Support**: Revert to previous mod versions when updates cause issues
- **Flexible Deployment**: Symlink, hardlink, or copy mods to game directories
- **Dependency Resolution**: Automatically fetches and installs mod dependencies
- **Infinite-Scroll Search**: Browse a continuously loading result list with clean cancel support
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

**Source auto-detection:** Commands automatically use the mod source configured for your game. If a game has multiple sources, you will be prompted to choose (or use `-y` to auto-select, or `--source` to specify explicitly) — **except `search`**, which queries every configured source concurrently by default instead of prompting (see [Search](#search) below).

```bash
# Search for mods (all configured sources by default)
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

# Mute update checks for this mod (does not hold a version — see Locking below)
lmm mod set-update 12345 --game skyrim-se --pin
```

`--pin` is a check-mute, not a version freeze: it stops `lmm update` from asking
the source about the mod at all, but the mod is free to be reinstalled,
rolled back, or otherwise moved to a different version by anything other than
an update check. If what you actually want is "this profile deploys exactly
version X, and nothing changes that", lock it instead (below). `--pin`
remains the only freeze available on sources that cannot resolve versions
(e.g. plain `directory` sources), since locking requires that capability.

### Locking mods to a version

`lmm mod lock <mod-id> [version]` locks the mod's entry in the current
profile to an exact version. With no version argument it locks at the
version currently recorded for the mod; with a version argument, that
version is resolved and validated against the source before the lock is
written — an unresolvable version is refused instead of writing a lock that
can never be satisfied. Locking requires a source that can resolve versions
(NexusMods, CurseForge, `manifest`/`api` sources with `mod_files`); a
version-less source is refused with a pointer to `lmm mod set-update --pin`
instead.

```bash
# Lock at the currently installed version
lmm mod lock 12345 --game skyrim-se

# Lock at a specific version
lmm mod lock 12345 1.2.3 --game skyrim-se

# Clear the lock (recorded version is left untouched)
lmm mod unlock 12345 --game skyrim-se
```

Locking is a metadata write, not a deploy: if the locked version differs
from what's currently installed, the command says so and the game directory
doesn't change until the next `lmm profile apply` (or `lmm deploy`), which
converges the mod to the locked version — downgrades included. `lmm mod
unlock` clears only the lock marker; the mod's recorded version is left
exactly as-is, since that's the record, not the lock.

In the TUI, `L` on the Installed Mods screen opens an async version picker
for the selected mod (fetched from the source); picking a version confirms
and locks/moves the lock immediately, and a locked mod's picker gains a
trailing "unlock" entry. The row's flags column shows `lck` for a locked mod
(it outranks `pin` when both apply — the lock is what the UI names).

**Lock vs. pin, in one line**: pinning mutes a mod's update _notifications_;
a lock is a lockfile entry that pins a _build_.

|                                        | `--pin` (update policy)                 | lock                                         |
| -------------------------------------- | --------------------------------------- | -------------------------------------------- |
| Statement about                        | "stop asking the source about this mod" | "this profile deploys exactly version X"     |
| Enforced at                            | check time only                         | deploy time (converges, downgrades included) |
| Scope                                  | per-install (SQLite)                    | per-profile (profile YAML)                   |
| Travels with `profile export`/`import` | no                                      | yes — imports reproduce the exact build      |
| Works on version-less sources          | yes                                     | no — refused with a capability error         |

The two are orthogonal — a mod can be locked, pinned, both, or neither — and
wherever they'd conflict, the lock wins and the output names it:

- **Locked, any other policy ("locked but informed")**: `lmm update` still
  checks the mod and reports a newer version if one exists, but deploy/apply
  still converges to the locked version.
- **Locked + `auto`**: auto-update skips the mod instead of applying an
  update to it, reported as a distinct "N locked mod(s) skipped by
  auto-update" line (`lmm update --all` skips it the same way).
- **Locked + `pinned` ("locked and silent")**: the mod isn't checked at all,
  same as any other pinned mod.
- **`lmm update <locked-mod-id>` (explicit single-mod update)**: refused —
  "locked at v*X*; move the lock (`lmm mod lock <id> <version>`) or unlock
  first."
- **`lmm install` of a locked mod at any other version** (an explicit
  `--version`, or a plain reinstall that would land on a newer latest — TUI
  install included): refused with the same remedies before anything
  downloads or deploys. Installing at exactly the locked version
  (reinstall/repair) still works and keeps the lock.
- **`lmm mod edit` of a locked mod**: refused before anything is written —
  `--version` (other than the locked version itself) with the same
  move-the-lock/unlock remedies, and `--source`/`--source-id` re-linking
  with the unlock remedy alone, since a re-link would replace the locked
  profile entry with a fresh, unlocked one and moving the lock can't help.
  Metadata-only edits (`--name`/`--author`) still work.

Lock state shows up alongside version info wherever it's installed: `lmm
list -v`'s `LOCKED` column (the locked version, or `-`), `lmm mod show`'s
Installed section, and `lmm update`'s table, where a locked mod's `POLICY`
cell gets a `[locked@<version>]` suffix. `--json` output for `list` and `mod
show` carries the same information additively (`locked`, `locked_version`),
and bulk `lmm update --json` marks a locked mod's `updates[]` entry with
`"locked": true` (omitted when unlocked); single-mod `lmm update --json`
instead reports a refused apply as `status: "skipped", reason: "locked"`, and
`lmm update rollback` of a locked mod is refused the same way — before its
"Rolling back..." header, with the same remedies and JSON document. `lmm verify` still reports a locked mod's version-record
mismatches, but `--fix` refuses to rewrite a locked mod's record (other,
unlocked mods in the same run are still fixed) — and when the installed
version hasn't yet converged to a lock's target, `verify` prints an
informational "lock pending convergence" note rather than treating it as
drift to repair.

### Terminal UI

Browse your configured game, installed mods, and profiles interactively, search mod sources, inspect the source registry, and manage mods in place — enable/disable, uninstall, deploy, reorder load order, resolve file conflicts, switch profiles, install from search results, check/apply updates (with changelogs and rollback), edit update policies, view a mod's deployed files, purge a profile, switch games, and create/delete/export/import profiles — with every mutating action behind a confirmation prompt:

```bash
lmm tui                     # real data
lmm tui --theme amber       # themes: wizardry (default), amber, dos, green
lmm tui --prototype         # demo mode with static fake data
```

Keys: `tab`/`h`/`l` cycle screens (landing on Search this way does not focus
the input), `1`–`6` jump directly (`3` focuses search immediately, like `/`;
`5` opens Sources, `6` opens Conflicts), `↑↓`/`j`/`k` move, `enter`
open/select (on Profiles, switch to the selected profile; selecting "Search
Archives" from the Dashboard menu also focuses search — explicit search
intent focuses, passive cycling doesn't), `/` focus search from anywhere,
type query, `enter` to search, `esc` unfocus (clears focus; afterward `s`
cycles sources, number keys switch screens), `n`/`p` skip a paneful of
results ahead/back (search results load continuously — there are no
pages to turn; `n`/`p` just move the selection by a screenful, refilling
the buffer as needed), `e`/`x`/`D` enable-disable/uninstall/deploy (see below), `i` install the
selected search result (Search, input blurred — see below), `u` check for
updates (Dashboard or Installed Mods — see below), `g` switch games (any
screen — see below), `?` help, `q` quit.

The Search screen defaults to **All sources**, mirroring the CLI: the typed
query runs concurrently against every source configured for the game. Press
`s` to cycle to a single source and back to "All sources". While "All
sources" is selected, results carry a source column, and if any source
failed, a one-line warning (e.g. `⚠ 1 source unavailable: my-repo: ...`)
appears above the results — the sources that succeeded are still shown.
Results mark already-installed mods; selecting a result shows a detail
panel. With the search input blurred, `i` installs the selected result: lmm
plans the install first ("Planning install…" on the status line), then opens
a confirmation panel with the version and size, source, file(s) that will
download, resolved dependencies (with their own download disclosure),
conflicting files if any, and a warning if a dependency is missing or
circular. Confirming streams download/extract/deploy progress into the
status line; a successful install re-runs the current search so the
result's "installed" marker updates right away.

The **Sources** screen (key `5`) lists the active game's configured
sources by default, with the same ID/TYPE/AUTH/CAPABILITIES columns as
`lmm source list`; press `a` to toggle to every source registered with lmm
— built-in and custom — marking which ones the active game uses. It only
shows sources that actually registered: a custom source whose definition
file failed to load (bad YAML, ID collision, etc.) has no row here — check
`lmm source list` for those.

On **Installed Mods**, `e` toggles the selected mod's enable/disable state
(the direction follows its current status: disabled mods enable, everything
else disables) and `x` uninstalls it — removing deployed files, cache, and
its profile entry, running uninstall hooks along the way. `D` deploys the
active profile (using its current enabled mods) from either Installed Mods
or the Dashboard. `f` opens a scrollable panel listing the selected mod's
deployed files (`f` again, or `esc`, closes it). `P` opens a notify/auto/pin
picker for the selected mod's update policy — picking one applies
immediately, no separate confirmation. `L` opens an async version picker
(fetched from the source) to lock the mod, or move an existing lock — a
locked mod's picker gains a trailing "unlock" entry — see [Locking mods to a
version](#locking-mods-to-a-version). `J`/`K` (also `ctrl+down`/`ctrl+up`)
swap the selected mod with its neighbor in load order and persist the new
order right away; the list itself renders in load order, and a hint reads
"order changed — deploy (`D`) to apply" until you redeploy. `<` rolls the
selected mod back to its previous version behind a confirmation prompt — a
mod with no previous version is refused on the status line instead. `X`, on
Dashboard or Installed Mods, purges the active profile (undeploying every
currently-deployed mod) behind a confirmation prompt; an empty profile
short-circuits with a one-line "no mods installed" message.

The **Conflicts** screen (key `6`) lists every game-directory file that two
or more enabled mods provide: the current load-order winner, every other
mod that also provides the file, and a "stale" marker when the deployed
copy no longer matches the winner (a reorder or update landed but hasn't
been redeployed yet). Selecting a row shows a resolution hint — reorder
(`J`/`K`) or disable the losing mod, then redeploy. `D` deploys the active
profile directly from this screen, same as Installed Mods/Dashboard. The
Dashboard's conflict count reflects this screen's real detection.

`u`, on Dashboard or Installed Mods, checks every checkable installed mod
for updates (pinned and local mods are skipped) — "Checking for updates…"
on the status line while it runs. Zero updates reports a one-line status;
one or more opens a confirmation panel listing each `<mod> <from> → <to>`,
and confirming applies all of them in sequence with per-update download
progress streamed into the status line — one mod failing doesn't stop the
rest, it's folded into the batch's warnings instead. While that panel is
open, `v` opens the changelog for the update it names — or, with several
updates pending, a "View changelog" picker naming each `<mod> <from> →
<to>` first — as a scrollable overlay. After a check, `v` also works
directly on an Installed Mods row: it shows that mod's changelog from the
most recent check (or says there's none for it). When a confirmed batch
finishes, an "update results" overlay lists exactly what happened, one
`✓ <mod> <from> → <to>` (or `✗ <mod>: <error>`) line per update, so the
applied set is never just a status-line count. The Dashboard's Updates count shows
`?` until a check has run this session, then reflects the real number (it
survives unrelated refreshes and only reverts to `?` after an update batch
is actually applied, since that's what makes the count stale).

On **Profiles**, `enter` on a profile other than the active one plans the
switch and shows a preview: mods to enable/disable, or "No mod changes; set
as default." when nothing would change. `enter` on the already-active
profile just reports "Already on profile ..." with no modal. If the plan
needs mods that aren't installed yet, the preview also discloses what it
will fetch (`Will download & install N mod(s):` plus one `↓` line per mod)
— confirming downloads and installs them as part of applying the switch,
streaming the same progress as an install. `c` opens an input for a new
profile name, validated inline against duplicates and invalid characters;
`d` deletes the selected profile behind a confirmation prompt, refusing the
active profile on the status line instead. `I` opens a "path to YAML" input
for profile import: lmm plans the import and shows a categorized preview
(new mods, already-installed mods, overwrite/cross-game warnings), then
confirming downloads and installs mods as needed, with an optional
immediate switch to the imported profile afterward. `E` opens a "path to
save" input, prefilled with a default filename, for profile export;
submitting refuses to overwrite an existing file.

`g`, on any screen, opens a picker of every game configured in
`games.yaml` with the active one marked; picking one rebinds the session
(data providers, active profile, sources) and reloads the current screen.

Every mutating action — enable/disable, uninstall, deploy, reorder, rollback,
purge, profile switch/create/delete/import, install, apply updates — opens a
confirmation panel describing what will change before it runs (reorder and
policy edits apply immediately instead, since the choice itself is the
confirmation): `y`/`enter` confirms, `n`/`esc` cancels, and only one action
can be in flight at a time. Install, apply-updates, and any switch
that downloads mods stream live progress into the status line while they
run; once an action finishes, a one-line status message reports the outcome
(including a warning count, if the flow reported any) and clears on your
next keypress. Quitting (`q`/`ctrl+c`) while an action is running cancels it
immediately but waits — "Finishing current step…" on the status line,
bounded to a few seconds — for that step to actually finish instead of
killing it mid-download. A source that can't perform a requested action, or
needs authentication, renders as a clean one-line message naming the right
CLI fallback (e.g. "run 'lmm update' from a shell instead", or `lmm auth
login <source>`) instead of a raw error. `--prototype` mode demos all of
these actions end to end against simulated data, including one canned
profile that always exercises the download-and-switch path.

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

  icarus:
    name: "Icarus"
    install_path: "/path/to/Steam/steamapps/common/Icarus"
    mod_path: "/path/to/Steam/steamapps/common/Icarus/Icarus/Content/Paks/mods"
    sources:
      icarus: "icarus"
    deploy_mode: compile
```

Steam auto-detection (`lmm game detect`) knows about Icarus (App ID `1149460`) and generates an equivalent entry for you, `install_path`/`mod_path` filled in from your actual Steam library — the YAML above is kept here as reference for what gets written, not something you need to type by hand.

### Deployment Methods

Mods can be deployed using three methods:

| Method     | Description                                                    |
| ---------- | -------------------------------------------------------------- |
| `symlink`  | Symbolic links to cached files (default, space efficient)      |
| `hardlink` | Hard links (transparent to games, requires same filesystem)    |
| `copy`     | Full file copies (maximum compatibility, uses more disk space) |

**Priority**: A profile-level `link_method` (in the profile's YAML) takes precedence over the per-game `link_method` in `games.yaml`, which takes precedence over `default_link_method` in `config.yaml`. If none is set, defaults to `symlink`. An explicit `--method` flag (e.g. `lmm deploy --method`) beats all three. See [Configuration reference](docs/configuration.md) for details, including an upgrade note for profiles saved before v1.14.1.

`lmm status -g <game>` shows the effective method for the active profile, marked `(per-profile)` or `(per-game)`; with no override anywhere the line appears only under `--verbose`, marked `(global default)`. In `--json` output, `link_method` reports the game-level resolution (game override or global default, unchanged for compatibility), while `effective_link_method` and `link_method_source` (`profile`, `game`, or `global`) report what a deploy into the active profile actually uses.

### Cache Path Priority

The mod cache location is determined by:

1. Per-game `cache_path` in `games.yaml` (if set)
2. Global `cache_path` in `config.yaml` (if set)
3. Default: `~/.local/share/lmm/cache/`

This allows you to store different games' mods on different drives (e.g., large games on HDD, frequently accessed games on SSD).

## Custom Sources

In addition to built-in mod sources (NexusMods, CurseForge), lmm lets you declare custom sources in YAML files instead of writing code. Three types are fully implemented: `directory` (a local folder of mods), `manifest` (a JSON/YAML mod list you publish, over `https://` or as a local file), and `api` (a GET+JSON REST API described declaratively) — all three work from `search`/`install`/`update` like any built-in source (within each type's capabilities), and `manifest`/`api` sources also support optional API-key authentication. Because `lmm search` queries every source configured for a game concurrently by default (see [Search](#search)), a game mapping several of these alongside NexusMods/CurseForge surfaces results from all of them in one query.

Custom source definitions are loaded from `~/.config/lmm/sources/*.yaml` (or `*.yml`). Each file must define exactly one source. Broken definition files are skipped with a warning — they never prevent lmm from starting.

### Source Definition Format

```yaml
id: donovan-mods # required; must match ^[a-z0-9-]+$ and be unique
name: Donovan's 7D2D Modlets # required display name
type: directory # required: directory (local folders) | manifest | api
allow_http: false # optional; permit http:// URLs (default false)

# Type-specific configuration (one block required, must match type)
directory:
  path: ~/Projects/mods/7dtd/donovan-7d2d-modlets
```

### Common Fields

| Field        | Type    | Required | Description                                                                                                                                                                                                                          |
| ------------ | ------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `id`         | string  | yes      | Unique source identifier; must contain only lowercase letters, numbers, and hyphens                                                                                                                                                  |
| `name`       | string  | yes      | Display name shown in source lists and commands                                                                                                                                                                                      |
| `type`       | string  | yes      | Source type: `directory`, `manifest`, or `api`. All three are fully supported, each within its own capabilities (see the sections below; `api` in particular can be install-by-ID-only if its definition omits a `search` endpoint). |
| `allow_http` | boolean | no       | If `true`, allow unencrypted http:// URLs (default `false`, HTTPS only)                                                                                                                                                              |

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

### Manifest Sources

A `manifest` source treats a JSON or YAML document you publish — an `https://` URL, or a local file path — as a full mod list: search, install, within-source dependency resolution, and update checks all work against it, the same as a built-in source.

```yaml
id: my-repo
name: My Mod Repo
type: manifest
manifest:
  url: https://example.com/mods.yaml # https:// URL, or a local path (~ expanded)
  refresh: 15m # optional cache TTL for remote URLs (default 15m)
```

- **Remote URLs** (`https://...`) are fetched on demand and cached in memory for `refresh` — a Go duration string like `30s`, `15m`, or `2h` (default `15m` when omitted). **Local file paths** are read fresh on every operation instead of being cached, so edits show up immediately.
- Fetch/parse problems (unreachable URL, malformed document, unsupported `version`) surface as an operation error naming the source and the manifest URL, at the point something actually uses the source. This is different from a broken _definition_ file, which is caught at load time and skipped with a warning before lmm ever starts (see above).
- `https://` is required for the manifest `url`, and for every file `url` inside the document, unless the definition sets `allow_http: true`; local paths are exempt.
- Remote manifest fetches are bounded by a 30-second timeout, so a hung server can't block other operations indefinitely.

The manifest document itself:

```yaml
version: 1
mods:
  - id: cool-mod
    name: Cool Mod
    version: 1.2.0
    author: someone
    summary: Makes things cooler
    game_ids: [skyrimspecialedition] # matched against this source's mapped `sources:` value
    url: https://example.com/mods/cool-mod # optional web page
    updated_at: 2026-07-01T00:00:00Z # optional, RFC 3339
    dependencies: [other-mod] # optional, IDs of other mods in this manifest
    files:
      - id: main
        name: Main File
        filename: cool-mod-1.2.0.zip
        version: 1.2.0
        size: 123456
        url: https://example.com/files/cool-mod-1.2.0.zip
        sha256: <hex digest> # optional; verified on download if present
        primary: true
```

`version: 1` is the only manifest version lmm understands today; any other value is rejected.

**`mods[]` fields:**

| Field          | Type     | Required | Description                                                                                                                                                                                                                                       |
| -------------- | -------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `id`           | string   | **yes**  | Unique mod ID within this manifest; also its dependency-reference ID                                                                                                                                                                              |
| `name`         | string   | **yes**  | Display name                                                                                                                                                                                                                                      |
| `version`      | string   | no       | Compared against installed versions for update checks                                                                                                                                                                                             |
| `author`       | string   | no       | —                                                                                                                                                                                                                                                 |
| `summary`      | string   | no       | Shown in search results                                                                                                                                                                                                                           |
| `game_ids`     | []string | no       | Restricts the mod to specific games, matched against the value that game maps for this source under its `sources:` block in `games.yaml` (same convention as NexusMods/CurseForge IDs); omitted or empty matches every game that maps this source |
| `url`          | string   | no       | Web page for the mod (informational only)                                                                                                                                                                                                         |
| `updated_at`   | string   | no       | RFC 3339 timestamp; an unparseable value is silently treated as unset rather than an error                                                                                                                                                        |
| `dependencies` | []string | no       | Other mods' `id`s within this same manifest; resolved automatically like NexusMods dependencies                                                                                                                                                   |
| `files`        | []object | no       | Downloadable files for this mod, see below                                                                                                                                                                                                        |

**`files[]` fields:**

| Field      | Type    | Required | Description                                                                                                               |
| ---------- | ------- | -------- | ------------------------------------------------------------------------------------------------------------------------- |
| `id`       | string  | **yes**  | File ID, used to request a download                                                                                       |
| `filename` | string  | **yes**  | Name given to the downloaded/cached file                                                                                  |
| `url`      | string  | **yes**  | Download URL (`https://` unless `allow_http: true`)                                                                       |
| `name`     | string  | no       | Display name                                                                                                              |
| `version`  | string  | no       | —                                                                                                                         |
| `size`     | integer | no       | Size in bytes                                                                                                             |
| `sha256`   | string  | no       | Hex-encoded SHA-256 checksum; when present, lmm verifies it after download and **aborts the install if it doesn't match** |
| `primary`  | boolean | no       | Marks the default file when a mod publishes more than one                                                                 |

To use a manifest source with a game, map it under that game's `sources:` block in `games.yaml`, the same as any built-in source — the mapped value should match the IDs used in the manifest's `game_ids` (unlike `directory` sources, this value is not ignored):

```yaml
games:
  skyrim-se:
    sources:
      nexusmods: skyrimspecialedition
      my-repo: skyrimspecialedition
```

### API Sources

An `api` source describes a GET+JSON REST API declaratively — endpoint URL templates plus JSON dot-path mappings — and lmm calls it directly: search, install, and update checks all work without writing a client. Every endpoint is optional; a definition with only enough endpoints to fetch and download a mod by a known ID (no `search`) is a valid "install-by-ID-only" source.

```yaml
id: esoui
name: ESOUI
type: api
api:
  base_url: https://api.example.com
  page_start: 1 # optional; first page number the API expects (default 1)
  auth: # optional, same block as manifest sources
    api_key:
      in: header # "header" or "query"
      name: X-API-Key
  endpoints: # each endpoint is optional; an undefined one is a capability gap (see below)
    search:
      path: /mods?game={game_id}&q={query}&page={page}&limit={page_size}
      list: results # required: dot-path to the results array
      total: pagination.total # optional: dot-path to a total-count field
    get_mod:
      path: /mods/{mod_id}
    mod_files:
      path: /mods/{mod_id}/files
      list: files # required: dot-path to the files array
    download_url:
      path: /files/{file_id}/download
      field: url # required: dot-path to the URL string in the response
  mappings:
    mod: # domain field -> JSON dot-path
      id: id
      name: name
      version: latest_version
      author: author.name
      summary: description
      downloads: download_count
      updated_at: updated # RFC 3339 expected; unparseable is left unset
      url: web_url
    file: # domain field -> JSON dot-path
      id: id
      name: title
      filename: file_name
      version: version
      size: size_bytes
```

**Placeholders** — every `{placeholder}` in an endpoint's `path` is substituted with a URL-escaped value before the request is made; a placeholder with no value for that request is left in the URL as-is:

| Placeholder   | Value                                                                                                                                    | Used by                                          |
| ------------- | ---------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------ |
| `{game_id}`   | The current game's ID for this source (from the search query, the mod being fetched/installed, or an installed mod during update checks) | `search`, `get_mod`, `mod_files`, `download_url` |
| `{query}`     | The search text                                                                                                                          | `search`                                         |
| `{page}`      | The internal 0-based page number, plus `page_start` (default `1`)                                                                        | `search`                                         |
| `{page_size}` | The requested page size (defaults to 20 when unspecified or ≤ 0)                                                                         | `search`                                         |
| `{offset}`    | The internal 0-based page × `page_size` — independent of `page_start`, for offset-paginated APIs                                         | `search`                                         |
| `{mod_id}`    | The mod ID                                                                                                                               | `get_mod`, `mod_files`, `download_url`           |
| `{file_id}`   | The file ID                                                                                                                              | `download_url`                                   |

**`mappings.mod` keys** (`id` and `name` are required; every other key is optional and left at its zero value when unmapped or the path doesn't resolve):

| Key           | Required | Domain field                                           |
| ------------- | -------- | ------------------------------------------------------ |
| `id`          | **yes**  | Mod ID                                                 |
| `name`        | **yes**  | Display name                                           |
| `version`     | no       | Compared against installed versions for update checks  |
| `author`      | no       | —                                                      |
| `summary`     | no       | Shown in search results                                |
| `description` | no       | Falls back to `summary` when unmapped or empty         |
| `downloads`   | no       | Download count                                         |
| `updated_at`  | no       | RFC 3339 timestamp; unparseable is silently left unset |
| `url`         | no       | Web page for the mod                                   |
| `picture_url` | no       | Main image URL                                         |

**`mappings.file` keys** (`id` is required only when `mod_files` is defined):

| Key        | Required                       | Domain field                             |
| ---------- | ------------------------------ | ---------------------------------------- |
| `id`       | **yes** (when `mod_files` set) | File ID, used to request a download      |
| `name`     | no                             | Display name                             |
| `filename` | no                             | Name given to the downloaded/cached file |
| `version`  | no                             | —                                        |
| `size`     | no                             | Size in bytes                            |

Unknown keys anywhere in `mappings.mod` or `mappings.file` fail validation at load time (typo detection) instead of silently mapping to nothing.

**Capability gaps** — an endpoint you don't define makes the corresponding operation report "not supported" instead of failing at load time:

- no `search` → searching is unsupported (a valid install-by-ID-only source; probe one with `lmm source validate --probe --id <mod-id>`, see below)
- no `get_mod` → fetching a single mod is unsupported, and so are update checks (`api` sources check for updates by calling `get_mod` on each installed mod and comparing versions)
- no `mod_files` → listing a mod's files is unsupported, and so is the `versions` capability (per-file version→file resolution, used by `install --version` and profile version convergence)
- no `download_url` → resolving a download URL is unsupported
- dependency resolution (`GetDependencies`) is **always** unsupported for `api` sources — there is no dependency endpoint in v1

`lmm source list`'s `CAPABILITIES` column reflects exactly this: a definition with only `get_mod` shows `updates`; adding `search` adds `search` to that list; `auth` appears only when the definition declares an `auth` block; `versions` appears once `mod_files` is defined. That `versions` flag only advertises the endpoint's presence, though — whether `install --version` can actually resolve a given mod depends on whether the files that mod's `mod_files` call returns carry version info, checked dynamically per call (see `install --version`'s own entry below).

**Guardrails:**

- Requests are `GET` only, and only JSON responses are understood — no POST, GraphQL, or scraping.
- `api.base_url` must be `https://` unless the definition sets `allow_http: true` (same rule as `manifest` sources).
- Every request is bounded by a 30-second timeout.
- Responses are capped at 10 MiB; a larger response fails the operation instead of being read into memory.

**Credentials** — `api` sources use the same `auth.api_key` block as `manifest` sources (see [Authentication](#authentication) below): the resolved key is attached to every API request per `in: header` / `in: query`. For downloads, both header- and query-mode keys are only sent when the URL returned by `download_url` shares scheme and host with `api.base_url` — an endpoint that hands back a third-party CDN URL never receives the source's key, in either form. If a download is redirected to a different scheme or host, a header-mode key is stripped before the redirect is followed (the same v1.8.0 machinery `manifest` sources use).

### Authentication

A custom source can require an API key, attached to every request as either a header or a query parameter. Today this is available to `manifest` and `api` sources (`directory` sources need no auth):

```yaml
manifest:
  url: https://example.com/mods.yaml
  auth:
    api_key:
      in: header # "header" or "query"
      name: X-API-Key # header name, or query parameter name, the key is sent as
```

- **Key resolution**, checked in order:
  1. The `LMM_<ID>_API_KEY` environment variable, with the source's `id` uppercased and `-` replaced by `_` (source `my-repo` → `LMM_MY_REPO_API_KEY`).
  2. A key saved with `lmm auth login <id>` — this works for any registered source whose definition declares `auth`, not just NexusMods/CurseForge, and stores the key in the same local token store.
- The resolved key is always attached to the manifest fetch itself (the request for the mod list document); for `api` sources, it's attached to every request built from an `endpoints.*.path` template (search, get_mod, mod_files, download_url).
- File downloads follow the same same-origin rule regardless of whether the key is `in: header` or `in: query`:
  - **Remote manifests** (`https://` URL): the key (as a header, or appended to the URL) is only sent to file downloads whose scheme and host match the manifest URL's — a manifest pointing files at a third-party CDN never receives the source's key, in either form.
  - **Local-file manifests**: the key is attached to every file download regardless of host, since a local manifest is user-authored and already trusted.
  - **`api` sources**: the key is only sent to a `download_url` response whose scheme and host match `api.base_url`'s — see [API Sources](#api-sources) above.
- If a file download is redirected to a different scheme or host, an `in: header` key is stripped before the redirect is followed — Go's HTTP client otherwise forwards custom headers across redirects even when it would strip `Authorization`/`Cookie`.
- Keys are never printed or logged; `lmm source list` only reports whether one is configured (`AUTH` column: `yes` / `no` / `n/a`), and `lmm auth status` masks stored keys to their first/last 3 characters (keys of 8 characters or fewer are fully masked). `lmm auth status` also lists any registered custom source whose definition declares `auth`, alongside the built-in nexusmods/curseforge rows, plus any stored token whose source is no longer registered (with a hint to remove it). `lmm auth logout <id>` removes a stored token even if the source's definition file has since been removed.

### Source Management Commands

List sources (built-in and custom). With a resolvable game (`-g`, or a default set via `lmm game set-default`), the list scopes to that game's configured sources by default:

```bash
lmm source list
```

Output:

```
ID            NAME                    TYPE       AUTH  CAPABILITIES                       ERROR
nexusmods     Nexus Mods              built-in   yes   search,deps,updates,auth,versions
donovan-mods  Donovan's 7D2D Modlets  directory  n/a   search,updates
```

Pass `--all` to see every registered source regardless of what the active game has configured, with an `IN USE` column marking which ones the active game maps:

```bash
lmm source list --all
```

Output:

```
ID            NAME                    TYPE       AUTH  CAPABILITIES                       IN USE  ERROR
nexusmods     Nexus Mods              built-in   yes   search,deps,updates,auth,versions  yes
curseforge    CurseForge              built-in   yes   search,deps,updates,auth,versions  no
donovan-mods  Donovan's 7D2D Modlets  directory  n/a   search,updates                     yes
my-repo       My Mod Repo             manifest   no    search,deps,updates,auth,versions  no
esoui         ESOUI                   api        no    search,updates,auth                no
```

With no game resolvable (no `-g`, no default game set), `--all` has no effect: the full registry is shown either way, with no `IN USE` column, exactly as when no game exists at all. Definitions that failed to load are always shown, in every view, as an `error` row. `--json` follows the same scoping; the `"in_use"` key is only ever present in the `--all`-with-game-resolvable combination.

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

Add `--probe` to also perform a live smoke test — a directory scan, a manifest fetch+parse, or an API call, depending on the definition's `type`:

```bash
lmm source validate --probe ~/.config/lmm/sources/my-source.yaml
```

For an `api` definition with no `search` endpoint (install-by-ID-only), pass `--id` with a known mod ID so `--probe` has something to call `get_mod` with. Captured against a local test definition (a `get_mod`-only `api` source pointed at a throwaway local server):

```
$ lmm source validate --probe --id 42 demo-api.yaml
demo-api.yaml: valid (api source "demo-api")
probe: ok — get_mod 42 returned "Cool Mod"
```

Without `--id` on a search-less `api` definition, `--probe` fails with a clear message instead of silently doing nothing:

```
Error: probe: this definition has no search endpoint; provide a known mod id with --id to probe get_mod
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

A `directory` source now shows up with real capabilities in `lmm source list` (`search,updates`, `auth=n/a`), and it will show as an `error` row if the configured path is missing or not a directory. A `manifest` source shows `search,deps,updates,versions` (plus `auth` if the definition declares one, with the `AUTH` column reporting `yes`/`no` once a key is or isn't configured). An `api` source shows only the capabilities its defined endpoints provide — `updates` alone for a `get_mod`-only definition, `search,updates` once a `search` endpoint is added, plus `auth` if the definition declares one, plus `versions` once a `mod_files` endpoint is defined — and never `deps` (dependency resolution isn't supported for `api` sources). Any type will show as an `error` row if construction fails (e.g. a directory source's path doesn't exist). A definition whose `id` collides with an already-registered source (a built-in, or another definition) also produces an `error` row (`id already in use`); the source that was already registered keeps its original row and type unchanged.

## CLI Reference

### Global Flags

| Flag         | Short | Description                                                                                                             |
| ------------ | ----- | ----------------------------------------------------------------------------------------------------------------------- |
| `--game`     | `-g`  | Game ID (optional if default set via `game set-default`)                                                                |
| `--verbose`  | `-v`  | Enable verbose output                                                                                                   |
| `--config`   |       | Custom config directory                                                                                                 |
| `--data`     |       | Custom data directory                                                                                                   |
| `--json`     |       | Output in JSON (list, status, search, update, conflicts, verify, mod show, source list); errors print `{"error":"..."}` |
| `--no-hooks` |       | Disable all hooks at runtime                                                                                            |
| `--no-color` |       | Disable colored output (respects NO_COLOR env)                                                                          |

### Commands

| Command                                            | Description                                                                                                                       |
| -------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `lmm search <query>`                               | Search all configured sources concurrently                                                                                        |
| `lmm search <query> --source ID`                   | Search a single source instead of all configured ones                                                                             |
| `lmm search <query> --category ID`                 | Filter by category (NexusMods and CurseForge)                                                                                     |
| `lmm search <query> --tag TAG`                     | Filter by tag (NexusMods only; repeat for multiple)                                                                               |
| `lmm install [query]`                              | Search and install a mod (query optional with `--id`)                                                                             |
| `lmm install --id <mod-id>`                        | Install by mod ID                                                                                                                 |
| `lmm install --id <mod-id> --file <file-id>`       | Install a specific file, skipping file selection                                                                                  |
| `lmm install --version <version>`                  | Install the exact-match version (archived files searched automatically)                                                           |
| `lmm install --show-archived`                      | Include archived/old files when selecting a file                                                                                  |
| `lmm install --no-deps`                            | Skip automatic dependency installation                                                                                            |
| `lmm install --source ID` / `-s`                   | Use a specific source (default: sole configured source; prompts when several are configured, `-y` picks the first alphabetically) |
| `lmm uninstall <mod-id>`                           | Uninstall a mod                                                                                                                   |
| `lmm uninstall <mod-id> --keep-cache`              | Uninstall but keep the cached mod files                                                                                           |
| `lmm import`                                       | Scan `mod_path` for untracked mods and import them (see [Import](#import) below)                                                  |
| `lmm import <archive-path>`                        | Import one local mod archive                                                                                                      |
| `lmm list`                                         | List installed mods                                                                                                               |
| `lmm list --profiles`                              | List profiles for the game                                                                                                        |
| `lmm status`                                       | Show current status                                                                                                               |
| `lmm update`                                       | Check for and apply auto-updates                                                                                                  |
| `lmm update <mod-id>`                              | Update a specific mod                                                                                                             |
| `lmm update --all`                                 | Apply all available updates                                                                                                       |
| `lmm update --dry-run`                             | Preview what would update                                                                                                         |
| `lmm update rollback <mod-id>`                     | Rollback to previous version                                                                                                      |
| `lmm verify`                                       | Verify cached mod files (see below)                                                                                               |
| `lmm verify --fix`                                 | Re-download missing files, populate missing checksums, repair version-record mismatches                                           |
| `lmm mod enable <mod-id>`                          | Enable a disabled mod                                                                                                             |
| `lmm mod disable <mod-id>`                         | Disable mod (keep in cache)                                                                                                       |
| `lmm mod set-update <mod-id> --auto`               | Enable auto-updates for mod                                                                                                       |
| `lmm mod set-update <mod-id> --notify`             | Notify only (default)                                                                                                             |
| `lmm mod set-update <mod-id> --pin`                | Mute update checks for mod (does not hold a version — see [Locking](#locking-mods-to-a-version))                                  |
| `lmm mod lock <mod-id> [version]`                  | Lock mod's profile entry to its current or a specific version                                                                     |
| `lmm mod unlock <mod-id>`                          | Clear a mod's lock (recorded version is left untouched)                                                                           |
| `lmm mod show <mod-id>`                            | Show mod details (description, image, etc.)                                                                                       |
| `lmm mod files <mod-id>`                           | List files deployed by mod                                                                                                        |
| `lmm mod edit <current-id>`                        | Edit mod details (name, version, author, source, ID)                                                                              |
| `lmm game set-default <game-id>`                   | Set the default game                                                                                                              |
| `lmm game show-default`                            | Show current default game                                                                                                         |
| `lmm game clear-default`                           | Clear the default game setting                                                                                                    |
| `lmm game add`                                     | Interactively add a new game configuration                                                                                        |
| `lmm game detect`                                  | Scan Steam libraries for known moddable games                                                                                     |
| `lmm auth login [source]`                          | Authenticate with a source (any source declaring auth; nexusmods/curseforge validated live)                                       |
| `lmm auth logout [source]`                         | Remove stored credentials                                                                                                         |
| `lmm auth status`                                  | Show authentication status                                                                                                        |
| `lmm profile list`                                 | List profiles                                                                                                                     |
| `lmm profile create <name>`                        | Create a profile                                                                                                                  |
| `lmm profile switch <name>`                        | Switch to a profile (installs missing mods)                                                                                       |
| `lmm profile delete <name>`                        | Delete a profile                                                                                                                  |
| `lmm profile export <name>`                        | Export profile to YAML                                                                                                            |
| `lmm profile import <file>`                        | Import profile from YAML                                                                                                          |
| `lmm profile import <file> --force`                | Import and overwrite existing                                                                                                     |
| `lmm profile reorder [mod-id ...]`                 | Show or set load order                                                                                                            |
| `lmm profile sync`                                 | Update profile to match installed mods                                                                                            |
| `lmm profile apply`                                | Install/enable mods to match profile                                                                                              |
| `lmm deploy`                                       | Deploy all enabled mods from cache                                                                                                |
| `lmm deploy <mod-id>`                              | Deploy specific mod from cache                                                                                                    |
| `lmm deploy --method hardlink`                     | Deploy using different link method                                                                                                |
| `lmm deploy --purge`                               | Purge then deploy all mods                                                                                                        |
| `lmm purge`                                        | Remove all mods from game directory                                                                                               |
| `lmm conflicts`                                    | Show file conflicts in current profile                                                                                            |
| `lmm source list`                                  | List built-in and user-defined mod sources                                                                                        |
| `lmm source validate <file>`                       | Validate a user-defined source definition                                                                                         |
| `lmm source validate --probe <file>`               | Also live-smoke-test the definition (scan/fetch/API call)                                                                         |
| `lmm source validate --probe --id <mod-id> <file>` | Probe an `api` definition that has no `search` endpoint                                                                           |

`lmm install --version <version>` resolves the exact version against the mod's full file list — archived/old files are searched automatically, no `--show-archived` needed — and the matching file(s) become the pool for `--file`/`-y`/the interactive prompt; when the mod has dependencies, `--version` and `--file` apply to the named mod only (`--file` picks from the version's matches when both are given, and the whole install aborts up front if either fails to resolve) — dependencies are unaffected, still installing at latest with their primary file auto-selected. An unknown version fails with an error listing the versions the source actually has (`version not found: version "..." (available: ...)`). A source whose files carry no version information fails with the standard "not supported" gap instead, same as any other missing capability — this is decided dynamically from the actual file data returned for that mod, not from the source's advertised `versions` capability flag (a source can declare `versions` support and still hit this gap for a mod whose files happen to lack version strings). Omitting `--version` installs the latest, unchanged.

**Version behavior in profiles**: a mod reference's `version:` field in a profile is the record of what that profile deploys, not just a display value — `lmm profile apply` and `profile switch` converge the installed mod to match it, downgrades included, healing a stale on-disk deployment back to the recorded version whenever it's still available upstream; `profile import` converges the same way: a mod already installed at a different version than the imported profile records is reinstalled at the profile's version as part of the import itself — so a lock carried by a shared profile takes effect without a second command. Hand-edit a profile's `version:` (or export/share/import the profile) to reproduce an exact build across machines. Sources whose files carry no version information (decided dynamically from the actual file data, not the source's advertised `versions` capability flag) keep the previous file-ID-based behavior instead.

### Exit Codes

| Code | Meaning                                                     |
| ---- | ----------------------------------------------------------- |
| `0`  | Success                                                     |
| `1`  | Error                                                       |
| `2`  | Cancelled by the user (e.g. declined a confirmation prompt) |

### Import

`lmm import` has two distinct modes, chosen by whether an archive path is given:

- **Scan mode** (`lmm import`, no arguments): scans the game's `mod_path` for files not yet tracked by lmm, tries to match each one by name against every search-capable source configured for the game (in ID-sorted order — e.g. `curseforge` before `nexusmods` when both are configured — stopping at the first source that returns a result; skip matching entirely with `--skip-match`), and imports whatever is left after confirmation. Useful for mods that were installed manually — e.g. mods whose source has disabled API downloads. `--dry-run` and `--skip-match` only apply to this mode. Every mod imported this way is marked as requiring manual download (since lmm did not fetch it itself); re-link it to a source with `lmm mod edit --source` to clear that once it can be checked for updates normally.
- **Archive mode** (`lmm import <archive-path>`): imports that one specific mod file, deploying it and adding it to the profile. Pass `--id` (with `--source`, or it defaults to the game's sole configured source, prompting interactively when several are configured) to fetch and attach source metadata as part of the import.

Either way, a mod that ends up unmatched to any remote source is imported as local — it deploys and installs normally, but `lmm update` has nothing to check it against and will never notify about it.

```bash
lmm import --game hytale                    # Scan mod_path for untracked mods
lmm import --game hytale --dry-run          # Preview what would be imported
lmm import ./my-mod.zip --game skyrim-se    # Import a specific archive
lmm import ./mod.zip --game skyrim-se --id 12345 --source curseforge
```

### Search

`lmm search <query>` queries every source configured for the game concurrently by default — there's no prompt to pick one first, even when several sources are mapped. Results carry a `SOURCE` column so you can tell which source found each mod:

```
$ lmm search bigger --game skyrim-se
ID                  NAME             AUTHOR   VERSION  SOURCE
--                  ----             ------   -------  ------
BiggerBackpack-2.1  Bigger Backpack  donovan  2.1      donovan-mods
```

If one source fails, its failure is reported as a warning on stderr and the other sources' results are still returned — a flaky manifest URL doesn't hide results from a source that responded:

```
warning: source my-repo: source "my-repo": reading manifest /opt/mods/my-repo.yaml: open /opt/mods/my-repo.yaml: no such file or directory
```

Only when **every** configured source fails does the command return an error, which names each source's failure:

```
Error: search failed: all 1 source(s) failed: source my-repo: source "my-repo": reading manifest /opt/mods/my-repo.yaml: open /opt/mods/my-repo.yaml: no such file or directory
```

Use `--source <id>` to search a single configured source instead of aggregating:

```bash
lmm search bigger --game skyrim-se --source donovan-mods
```

A source that doesn't support searching (e.g. an `api` source defined without a `search` endpoint — see [API Sources](#api-sources)) is silently skipped when aggregating, but targeting it directly with `--source` reports a clear notice instead of a generic error:

```
Error: source "demo-api" does not support searching; install by ID instead: lmm install --source demo-api --id <mod-id>
```

A game with no configured sources at all fails fast with a diagnostic instead of an empty result:

```
Error: no mod sources configured for Skyrim Special Edition; add sources with 'lmm game add' or edit games.yaml
```

`--json search` includes the same per-source failures as a `"warnings"` array alongside `"mods"`.

### Update check behavior

When you run `lmm update`, the tool checks each installed mod against the source (e.g. NexusMods). If some mods cannot be fetched (e.g. deleted, private, or network error), you still see **partial results** (any updates that were found), and a **warning** is printed to stderr describing which mods could not be checked.

### Verify output

`lmm verify` reports per file:

- **+ ModName (fileID) - OK** - Cache exists and checksum stored.
- **X ModName (fileID) - MISSING (version X not in cache)** - Cached files for that mod version are missing; use `--fix` to re-download.
- **? ModName (fileID) - NO CHECKSUM** - File was installed without a stored checksum (e.g. before checksum support or with `--skip-verify`).
- **! ModName - FILE COUNT MISMATCH** - The cache directory exists but is empty, when downloads were expected (per-mod, not per-file); not repaired by `--fix`.
- **? Unknown mod ID - SKIPPED** - A stored checksum row references a mod that's no longer installed; not repaired by `--fix`.

`lmm verify` also contacts each installed mod's source to check its recorded version against what the stored file ID(s) actually are upstream (issue #94: older installs could record the mod's "latest" version instead of the version of the file that was actually downloaded and deployed), reporting per mod:

- **X ModName - VERSION MISMATCH (recorded X, source reports Y)** - The recorded version doesn't match what the installed file ID(s) report upstream; use `--fix` to repair.
- **? ModName - VERSION UNVERIFIABLE** - None of the recorded file ID(s) are listed by the source anymore; not repaired by `--fix` (reinstall the mod instead).

A locked mod's VERSION MISMATCH is still reported, but `--fix` refuses to
rewrite a locked mod's record (other, unlocked mods in the same run are
still fixed) since the record is the lock's target, not drift to repair —
move the lock instead. Separately, when a locked mod's installed version
hasn't yet converged to the lock (see [Locking mods to a
version](#locking-mods-to-a-version)), `verify` prints an informational
"lock pending convergence" note rather than treating it as an issue.

Mods installed from a local source, mods requiring manual download, and mods with no recorded file IDs are skipped silently. `--fix` repairs a VERSION MISMATCH by re-keying the cache entry to the effective (source-reported) version, correcting the DB row and active profile record, and re-linking symlink deployments; if a cache entry already exists under the effective version the rename is skipped and a note is printed (also included as `note` in `--json` output) while the DB/profile are still corrected.

## Architecture

```text
cmd/lmm/                  # CLI entry point (Cobra)
internal/
├── domain/               # Core types (Mod, Profile, Game)
├── source/               # Mod source abstraction
│   ├── nexusmods/        # NexusMods API client
│   ├── curseforge/       # CurseForge API client
│   ├── custom/           # User-defined sources (directory, manifest, api)
│   ├── steam/            # Steam library scanning (for 'lmm game detect')
│   └── httpclient/       # Shared HTTP client (timeouts, size caps, redirects)
├── storage/
│   ├── db/               # SQLite storage
│   ├── config/           # YAML configuration
│   └── cache/            # Mod file cache
├── linker/               # Deployment strategies
├── core/                 # Business logic
│   ├── service.go        # Main orchestrator
│   ├── installer.go      # Install/uninstall
│   ├── updater.go        # Update checking
│   ├── downloader.go     # HTTP downloads
│   └── extractor.go      # Archive extraction
└── tui/                  # Bubble Tea application
    ├── prototype/        # --prototype demo mode (static fake data)
    └── theme/            # Color themes (wizardry, amber, dos, green)
```

## File Locations

| Type             | Path                                                                         |
| ---------------- | ---------------------------------------------------------------------------- |
| Config           | `~/.config/lmm/`                                                             |
| Custom Sources   | `~/.config/lmm/sources/*.yaml`                                               |
| Database         | `~/.local/share/lmm/lmm.db`                                                  |
| Mod Cache        | `~/.local/share/lmm/cache/` (default)                                        |
| Download Staging | `~/.local/share/lmm/downloads/` (in-flight downloads and archive extraction) |

The mod cache location can be customized via `cache_path` in `config.yaml`. Setting a per-game `cache_path` in `games.yaml` changes that game's on-disk layout too: the global cache is `cache/<game-id>/<source-id>-<mod-id>/<version>/`, but a game-scoped `cache_path` drops the `<game-id>` segment since the configured directory is already specific to that game (`<cache_path>/<source-id>-<mod-id>/<version>/`).

## Documentation

- **[Configuration reference](docs/configuration.md)** – All options for `config.yaml` and `games.yaml` (including hooks, link method, sources).
- **Man pages** – In [`docs/man/man1/`](docs/man/man1/), one page per command and subcommand, generated from the CLI's own `--help` text (`make man`; a drift test fails CI if the pages fall out of sync). View with `man -l docs/man/man1/lmm.1` or install to your man path.
- **[CHANGELOG.md](CHANGELOG.md)** – Release history and notable changes.
- **[CONTRIBUTING.md](CONTRIBUTING.md)** – How to build, test, and submit changes.

## Roadmap

- [x] NexusMods authentication and downloads
- [x] Update management with policies and rollback
- [x] Default game setting (avoid --game on every command)
- [x] Mod dependency detection from NexusMods
- [x] Conflict detection (file conflicts, circular dependency warnings)
- [x] Mod file verification (checksums, --fix re-download)
- [x] Automatic dependency installation (opt out with `--no-deps`)
- [x] Interactive TUI (Bubble Tea) - see the Terminal UI section above
- [x] CurseForge integration
- [x] Additional first-party built-in sources beyond NexusMods/CurseForge (Icarus)
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
