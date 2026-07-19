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

# Pin to current version
lmm mod set-update 12345 --game skyrim-se --pin
```

### Terminal UI

Browse your configured game, installed mods, and profiles interactively, search mod sources, and inspect the source registry:

```bash
lmm tui                     # real data, read-only
lmm tui --theme amber       # themes: wizardry (default), amber, dos, green
lmm tui --prototype         # demo mode with static fake data
```

Keys: `tab`/`h`/`l` cycle screens, `1`–`5` jump, `3` jumps to Search (any
entry path focuses the input immediately; `5` opens Sources), `↑↓`/`j`/`k`
move, `enter` open/select, `/` focus search from anywhere, type query,
`enter` to search, `esc` unfocus (clears focus; afterward `s` cycles sources,
number keys switch screens), `n`/`p` next/previous page, `?` help, `q` quit.

The Search screen defaults to **All sources**, mirroring the CLI: the typed
query runs concurrently against every source configured for the game. Press
`s` to cycle to a single source and back to "All sources". While "All
sources" is selected, results carry a source column, and if any source
failed, a one-line warning (e.g. `⚠ 1 source unavailable: my-repo: ...`)
appears above the results — the sources that succeeded are still shown.
Results mark already-installed mods; selecting a result shows a detail
panel.

The **Sources** screen (key `5`) lists every source registered with lmm —
built-in and custom — with the same ID/TYPE/AUTH/CAPABILITIES columns as
`lmm source list`. It only shows sources that actually registered: a custom
source whose definition file failed to load (bad YAML, ID collision, etc.)
has no row here — check `lmm source list` for those.

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

In addition to built-in mod sources (NexusMods, CurseForge), lmm lets you declare custom sources in YAML files instead of writing code. Three types are fully implemented: `directory` (a local folder of mods), `manifest` (a JSON/YAML mod list you publish, over `https://` or as a local file), and `api` (a GET+JSON REST API described declaratively) — all three work from `search`/`install`/`update` like any built-in source (within each type's capabilities), and `manifest`/`api` sources also support optional API-key authentication. Because `lmm search` queries every source configured for a game concurrently by default (see [Search](#search)), a game mapping several of these alongside NexusMods/CurseForge surfaces results from all of them in one query.

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
| `type`      | string  | yes      | Source type: `directory`, `manifest`, or `api`. All three are fully supported, each within its own capabilities (see the sections below; `api` in particular can be install-by-ID-only if its definition omits a `search` endpoint). |
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

### Manifest Sources

A `manifest` source treats a JSON or YAML document you publish — an `https://` URL, or a local file path — as a full mod list: search, install, within-source dependency resolution, and update checks all work against it, the same as a built-in source.

```yaml
id: my-repo
name: My Mod Repo
type: manifest
manifest:
  url: https://example.com/mods.yaml   # https:// URL, or a local path (~ expanded)
  refresh: 15m                         # optional cache TTL for remote URLs (default 15m)
```

- **Remote URLs** (`https://...`) are fetched on demand and cached in memory for `refresh` — a Go duration string like `30s`, `15m`, or `2h` (default `15m` when omitted). **Local file paths** are read fresh on every operation instead of being cached, so edits show up immediately.
- Fetch/parse problems (unreachable URL, malformed document, unsupported `version`) surface as an operation error naming the source and the manifest URL, at the point something actually uses the source. This is different from a broken *definition* file, which is caught at load time and skipped with a warning before lmm ever starts (see above).
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
    game_ids: [skyrimspecialedition]         # matched against this source's mapped `sources:` value
    url: https://example.com/mods/cool-mod   # optional web page
    updated_at: 2026-07-01T00:00:00Z         # optional, RFC 3339
    dependencies: [other-mod]                # optional, IDs of other mods in this manifest
    files:
      - id: main
        name: Main File
        filename: cool-mod-1.2.0.zip
        version: 1.2.0
        size: 123456
        url: https://example.com/files/cool-mod-1.2.0.zip
        sha256: <hex digest>                 # optional; verified on download if present
        primary: true
```

`version: 1` is the only manifest version lmm understands today; any other value is rejected.

**`mods[]` fields:**

| Field          | Type     | Required | Description                                                                                                                                                                                            |
| -------------- | -------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `id`           | string   | **yes**  | Unique mod ID within this manifest; also its dependency-reference ID                                                                                                                                  |
| `name`         | string   | **yes**  | Display name                                                                                                                                                                                            |
| `version`      | string   | no       | Compared against installed versions for update checks                                                                                                                                                  |
| `author`       | string   | no       | —                                                                                                                                                                                                        |
| `summary`      | string   | no       | Shown in search results                                                                                                                                                                                 |
| `game_ids`     | []string | no       | Restricts the mod to specific games, matched against the value that game maps for this source under its `sources:` block in `games.yaml` (same convention as NexusMods/CurseForge IDs); omitted or empty matches every game that maps this source |
| `url`          | string   | no       | Web page for the mod (informational only)                                                                                                                                                              |
| `updated_at`   | string   | no       | RFC 3339 timestamp; an unparseable value is silently treated as unset rather than an error                                                                                                             |
| `dependencies` | []string | no       | Other mods' `id`s within this same manifest; resolved automatically like NexusMods dependencies                                                                                                        |
| `files`        | []object | no       | Downloadable files for this mod, see below                                                                                                                                                              |

**`files[]` fields:**

| Field      | Type    | Required | Description                                                                                                                 |
| ---------- | ------- | -------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `id`       | string  | **yes**  | File ID, used to request a download                                                                                         |
| `filename` | string  | **yes**  | Name given to the downloaded/cached file                                                                                    |
| `url`      | string  | **yes**  | Download URL (`https://` unless `allow_http: true`)                                                                         |
| `name`     | string  | no       | Display name                                                                                                                 |
| `version`  | string  | no       | —                                                                                                                             |
| `size`     | integer | no       | Size in bytes                                                                                                                |
| `sha256`   | string  | no       | Hex-encoded SHA-256 checksum; when present, lmm verifies it after download and **aborts the install if it doesn't match**   |
| `primary`  | boolean | no       | Marks the default file when a mod publishes more than one                                                                    |

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
  page_start: 1                  # optional; first page number the API expects (default 1)
  auth:                          # optional, same block as manifest sources
    api_key:
      in: header                 # "header" or "query"
      name: X-API-Key
  endpoints:                     # each endpoint is optional; an undefined one is a capability gap (see below)
    search:
      path: /mods?game={game_id}&q={query}&page={page}&limit={page_size}
      list: results               # required: dot-path to the results array
      total: pagination.total     # optional: dot-path to a total-count field
    get_mod:
      path: /mods/{mod_id}
    mod_files:
      path: /mods/{mod_id}/files
      list: files                 # required: dot-path to the files array
    download_url:
      path: /files/{file_id}/download
      field: url                  # required: dot-path to the URL string in the response
  mappings:
    mod:                          # domain field -> JSON dot-path
      id: id
      name: name
      version: latest_version
      author: author.name
      summary: description
      downloads: download_count
      updated_at: updated         # RFC 3339 expected; unparseable is left unset
      url: web_url
    file:                         # domain field -> JSON dot-path
      id: id
      name: title
      filename: file_name
      version: version
      size: size_bytes
```

**Placeholders** — every `{placeholder}` in an endpoint's `path` is substituted with a URL-escaped value before the request is made; a placeholder with no value for that request is left in the URL as-is:

| Placeholder   | Value                                                                        | Used by                                          |
| ------------- | ----------------------------------------------------------------------------- | ------------------------------------------------- |
| `{game_id}`   | The current game's ID for this source (from the search query, the mod being fetched/installed, or an installed mod during update checks) | `search`, `get_mod`, `mod_files`, `download_url` |
| `{query}`     | The search text                                                               | `search`                                          |
| `{page}`      | The internal 0-based page number, plus `page_start` (default `1`)             | `search`                                          |
| `{page_size}` | The requested page size (defaults to 20 when unspecified or ≤ 0)              | `search`                                          |
| `{offset}`    | The internal 0-based page × `page_size` — independent of `page_start`, for offset-paginated APIs | `search`                                          |
| `{mod_id}`    | The mod ID                                                                    | `get_mod`, `mod_files`, `download_url`            |
| `{file_id}`   | The file ID                                                                   | `download_url`                                    |

**`mappings.mod` keys** (`id` and `name` are required; every other key is optional and left at its zero value when unmapped or the path doesn't resolve):

| Key           | Required | Domain field                                                |
| ------------- | -------- | ------------------------------------------------------------- |
| `id`          | **yes**  | Mod ID                                                         |
| `name`        | **yes**  | Display name                                                    |
| `version`     | no       | Compared against installed versions for update checks            |
| `author`      | no       | —                                                                  |
| `summary`     | no       | Shown in search results                                             |
| `description` | no       | Falls back to `summary` when unmapped or empty                       |
| `downloads`   | no       | Download count                                                        |
| `updated_at`  | no       | RFC 3339 timestamp; unparseable is silently left unset                 |
| `url`         | no       | Web page for the mod                                                    |
| `picture_url` | no       | Main image URL                                                           |

**`mappings.file` keys** (`id` is required only when `mod_files` is defined):

| Key        | Required                     | Domain field              |
| ---------- | ------------------------------ | --------------------------- |
| `id`       | **yes** (when `mod_files` set)  | File ID, used to request a download |
| `name`     | no                               | Display name                          |
| `filename` | no                               | Name given to the downloaded/cached file |
| `version`  | no                               | —                                          |
| `size`     | no                               | Size in bytes                               |

Unknown keys anywhere in `mappings.mod` or `mappings.file` fail validation at load time (typo detection) instead of silently mapping to nothing.

**Capability gaps** — an endpoint you don't define makes the corresponding operation report "not supported" instead of failing at load time:

- no `search` → searching is unsupported (a valid install-by-ID-only source; probe one with `lmm source validate --probe --id <mod-id>`, see below)
- no `get_mod` → fetching a single mod is unsupported, and so are update checks (`api` sources check for updates by calling `get_mod` on each installed mod and comparing versions)
- no `mod_files` → listing a mod's files is unsupported
- no `download_url` → resolving a download URL is unsupported
- dependency resolution (`GetDependencies`) is **always** unsupported for `api` sources — there is no dependency endpoint in v1

`lmm source list`'s `CAPABILITIES` column reflects exactly this: a definition with only `get_mod` shows `updates`; adding `search` adds `search` to that list; `auth` appears only when the definition declares an `auth` block.

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
      in: header      # "header" or "query"
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
my-repo       My Mod Repo             manifest   no    search,deps,updates,auth
esoui         ESOUI                   api        no    search,updates,auth
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

A `directory` source now shows up with real capabilities in `lmm source list` (`search,updates`, `auth=n/a`), and it will show as an `error` row if the configured path is missing or not a directory. A `manifest` source shows `search,deps,updates` (plus `auth` if the definition declares one, with the `AUTH` column reporting `yes`/`no` once a key is or isn't configured). An `api` source shows only the capabilities its defined endpoints provide — `updates` alone for a `get_mod`-only definition, `search,updates` once a `search` endpoint is added, plus `auth` if the definition declares one — and never `deps` (dependency resolution isn't supported for `api` sources). Any type will show as an `error` row if construction fails (e.g. a directory source's path doesn't exist). A definition whose `id` collides with an already-registered source (a built-in, or another definition) also produces an `error` row (`id already in use`); the source that was already registered keeps its original row and type unchanged.

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
| `lmm search <query>`                   | Search all configured sources concurrently           |
| `lmm search <query> --source ID`       | Search a single source instead of all configured ones |
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
| `lmm source validate --probe <file>`   | Also live-smoke-test the definition (scan/fetch/API call) |
| `lmm source validate --probe --id <mod-id> <file>` | Probe an `api` definition that has no `search` endpoint |

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
