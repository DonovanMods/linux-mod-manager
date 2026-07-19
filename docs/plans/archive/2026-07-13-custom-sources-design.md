# User-Defined Mod Sources — Design

**Date:** 2026-07-13
**Status:** Approved design, pending implementation plan
**Target version:** v1.6.0 (MINOR)

## Problem

Mod sources are hardcoded: `registerSources()` in `cmd/lmm/root.go` instantiates
NexusMods and CurseForge and registers them with the source registry. Users cannot
add their own sources without writing Go code.

Motivating example: a user maintains their own 7 Days to Die modlets in
`~/Projects/mods/7dtd/donovan-7d2d-modlets/` and wants that directory to be a
first-class mod source alongside NexusMods for the same game — searchable,
installable, and update-checked like any remote mod.

## Goals

- Users define custom sources declaratively in YAML config files — no code, no plugins.
- Three source types: **directory** (local mod folders), **manifest** (a published
  mod list file), and **api** (declarative REST API description).
- Sources may support a subset of operations; CLI and TUI degrade gracefully.
- Optional API-key auth reusing the existing token store / env-var machinery.
- Searching a game spans all its configured sources by default (aggregate search).
- Both CLI and TUI treat custom sources as first-class.

## Non-Goals (v1)

- Programmable plugins (external executables, embedded scripting).
- OAuth flows, POST/GraphQL APIs, HTML scraping in the `api` type.
- Dependency resolution for the `api` type.
- `lmm source import <url>` / community definition registry (may come later;
  nothing in this design precludes it).
- Interactive `lmm source add` wizard.
- Migrating built-in sources (NexusMods, CurseForge) to the declarative format.
- Global re-ranking of aggregate search results.

## Existing Architecture (relevant facts)

- `ModSource` interface (`internal/source/source.go`): ID, Name, AuthURL,
  ExchangeToken, Search, GetMod, GetDependencies, GetModFiles, GetDownloadURL,
  CheckUpdates.
- `source.Registry` is already generic (register/get/list by ID).
- The data model is already **per-mod source**: `Mod.SourceID`,
  `InstalledMod` (embeds Mod), and profile `ModReference.SourceID`. No schema
  migration is needed to mix sources within one game/profile.
- `Game.SourceIDs map[string]string` maps source ID → source-specific game ID
  (e.g. `"nexusmods" -> "skyrimspecialedition"`); it already supports multiple
  sources per game.
- `lmm search` currently queries a single source (`--source`, defaulting to the
  game's first configured source).
- Local import machinery exists (`internal/core/importer.go`): filename
  version extraction, mod-name detection, cache copy helpers, `domain.SourceLocal`.
- API keys resolve env-var-first, then the DB token store
  (`getSourceAPIKey` in `cmd/lmm/root.go`).

## Design

### 1. Definition files & loading

Custom sources live one-per-file in `~/.config/lmm/sources/*.yaml`.

Common fields:

```yaml
id: my-repo          # required; [a-z0-9-]+; unique across built-ins and other files
name: My Mod Repo    # required display name
type: manifest       # required: manifest | api | directory
allow_http: false    # optional; permit http:// URLs (default false)
```

Exactly one type-specific block (`manifest:`, `api:`, or `directory:`) must be
present and must match `type`.

**Loader:** `internal/storage/config/sources.go` reads the directory, validates
each file, and returns `([]SourceDefinition, []LoadError)`. Validation covers:
required fields, ID format, ID collisions (against already-registered sources and
sibling files), block/type agreement, https-only URLs unless `allow_http: true`.

**Registration:** `registerSources()` in `cmd/lmm/root.go` loads definitions after
the built-ins, constructs the matching implementation from
`internal/source/custom`, and registers it. A definition that fails validation
**warns on stderr and is skipped** — a broken file never bricks the CLI/TUI.
Load errors are retained for display by `lmm source list`.

**No DB changes.** Definitions are pure config; auth reuses the existing token store.

### 2. `type: directory` — local mod directories

```yaml
id: donovan-mods
name: Donovan's 7D2D Modlets
type: directory
directory:
  path: ~/Projects/mods/7dtd/donovan-7d2d-modlets
```

- **Scanning:** each subdirectory is a mod, and each `.zip`/`.jar` archive is a
  mod (a source cannot know a game's deploy mode, so it scans both; deploy-mode
  handling happens at install time). Scans are lazy per operation — local reads
  are cheap enough that no cache is needed, and edits appear without restarting
  lmm.
- **Metadata resolution (priority order):**
  1. Well-known metadata files, behind a small internal reader interface.
     v1 ships a `ModInfo.xml` reader (7D2D: name, display name, version,
     description, author). Future formats (e.g. `fabric.mod.json`) are additive.
  2. Fallback: directory/file name + the existing version-pattern extraction
     (promoted from `importer.go` to a shared location).
- **Mod identity:** the mod ID is the directory name (or archive base name).
  Stable across versions and human-readable (e.g. mod ID `BiggerBackpack`,
  installed via the usual `--source donovan-mods`). Renaming a directory creates a
  new mod identity — accepted trade-off, documented.
- **Install flow:** `GetModFiles` returns one synthetic `DownloadableFile` per
  mod whose URL is the local path. The download step recognizes local paths and
  copies the directory (streaming, reusing importer copy helpers) into the
  standard cache layout `<game>/<source-id>-<mod-id>/<version>/`. Deploy,
  enable/disable, uninstall, and profiles use the normal linker flow unchanged.
- **Updates:** `CheckUpdates` compares the installed version with the current
  metadata-file version using `domain.IsNewerVersion`. Bumping a modlet's
  `ModInfo.xml` version makes `lmm update` flag it like a remote update.
- **Game association:** a directory source has no game-ID namespace; it matches
  any game whose `sources:` map includes its ID (mapped value accepted, unused).
- **Auth:** none. `AuthURL` returns `""`; `ExchangeToken` returns `ErrNotSupported`.
- **Search:** client-side (see §5 semantics shared with manifest type).

### 3. `type: manifest` — published mod-list file

```yaml
id: my-repo
name: My Mod Repo
type: manifest
manifest:
  url: https://example.com/mods.yaml   # https URL or local path (~ expanded)
  refresh: 15m                         # optional cache TTL (default 15m)
```

The manifest document is an lmm-defined format (JSON or YAML):

```yaml
version: 1
mods:
  - id: cool-mod
    name: Cool Mod
    version: 1.2.0
    author: someone
    summary: Makes things cooler
    game_ids: [skyrimspecialedition]   # matched against the game's mapped value;
                                       # empty/omitted = matches all games
    url: https://example.com/mods/cool-mod   # optional web page
    updated_at: 2026-07-01T00:00:00Z         # optional, RFC 3339
    dependencies: [other-mod]                # optional, IDs within this source
    files:
      - id: main
        name: Main File
        filename: cool-mod-1.2.0.zip
        version: 1.2.0
        size: 123456
        url: https://example.com/files/cool-mod-1.2.0.zip
        sha256: <hex digest>           # optional; verified on download if present
        primary: true
```

- Fetched on demand; in-memory cache with TTL; local paths read directly each
  time (cheap).
- All `ModSource` operations are implemented client-side over the manifest:
  search (see §5), `GetMod` by ID, `GetModFiles`/`GetDownloadURL` straight from
  the document, dependencies resolve to `ModReference{SourceID: <this source>}`,
  `CheckUpdates` compares versions via `domain.IsNewerVersion`.
- `sha256` plugs into the existing checksum verification (P1 feature).
- Manifest parse errors fail the operation with a clear error naming the
  manifest URL; they are not load-time errors (the definition itself is valid).
- Optional API-key auth (§6) applies to both the manifest fetch and file
  downloads (same header/query on both).

### 4. `type: api` — declarative REST description

```yaml
id: esoui
name: ESOUI
type: api
api:
  base_url: https://api.example.com
  page_start: 1                  # first page number the API expects (default 1)
  auth:                          # optional
    api_key:
      in: header                 # header | query
      name: X-API-Key
  endpoints:                     # each endpoint is optional; absence = capability gap
    search:
      path: /mods?game={game_id}&q={query}&page={page}&limit={page_size}
      list: results              # dot-path to results array
      total: pagination.total    # optional dot-path to total count
    get_mod:
      path: /mods/{mod_id}
    mod_files:
      path: /mods/{mod_id}/files
      list: files
    download_url:
      path: /files/{file_id}/download
      field: url                 # dot-path to the URL string in the response
  mappings:
    mod:                         # JSON dot-paths -> domain.Mod fields
      id: id
      name: name
      version: latest_version
      author: author.name
      summary: description
      downloads: download_count
      updated_at: updated        # RFC 3339 expected; unparseable -> zero value
      url: web_url
    file:                        # JSON dot-paths -> domain.DownloadableFile
      id: id
      name: title
      filename: file_name
      version: version
      size: size_bytes
```

Rules and guardrails (v1):

- GET requests returning JSON only.
- Placeholders: `{game_id}` (from `Game.SourceIDs[source]`), `{query}`, `{page}`
  (internal 0-based page + `page_start`), `{page_size}`, `{offset}` (internal
  0-based page × page_size, independent of `page_start`, for offset-based APIs),
  `{mod_id}`, `{file_id}`. All values URL-escaped on substitution.
- Dot-paths traverse JSON objects; numbers/strings coerced to the target field
  type; missing paths yield zero values (except `mod.id`, `mod.name`, and
  `file.id`, which are required — a response missing them is an error).
- `mappings.mod.id` etc. are validated at load time for required keys.
- Undefined endpoint ⇒ that capability is unsupported (§7). A definition with
  only `get_mod` + `mod_files` + `download_url` is a valid install-by-ID source.
- `GetDependencies` always returns `ErrNotSupported` in v1.
- `CheckUpdates` is implemented generically via `get_mod` + version comparison
  (shared helper with the other types); without `get_mod` it is unsupported.

### 5. Aggregate search

`Service.SearchMods` gains an all-sources mode:

- No `--source` given ⇒ query every source in the game's `sources:` map
  **concurrently** (errgroup, shared context/timeout).
- Results are merged and tagged by source; sort = name-match relevance first,
  then downloads (same heuristic as today).
- Per-source failures become warnings in the result (one flaky API must not
  hide local modlets); only all-sources-failed is an error.
- Pagination is per-source: "page 2" requests page 2 from each source and
  merges. No global re-ranking in v1.
- Sources without search capability are skipped silently in aggregate mode.
- CLI: results gain a source column; `--source` still narrows to one source.
- TUI: search rows get a source badge; the source picker gains an
  "All sources" option, which becomes the default.

Client-side search semantics (directory + manifest types): case-insensitive
substring match on name and summary, game filtering (per-type rules above),
name-matches ranked before summary-matches, then alphabetical; paginated
locally honoring `SearchQuery.Page`/`PageSize`.

### 6. Authentication

- Custom sources support **API key or none**. OAuth is out of scope.
- Key resolution order (matches existing pattern): `LMM_<ID>_API_KEY` env var
  (ID uppercased, dashes → underscores), then the DB token store.
- `lmm auth login <source-id>` is extended to prompt for and store an API key
  for any custom source that declares `auth`.
- Keys are attached per the definition (`header` or `query`) and never logged.
- `lmm source list` shows auth state (not needed / configured / missing).

### 7. Capabilities & graceful degradation

New sentinel in `internal/source`:

```go
var ErrNotSupported = errors.New("operation not supported by this source")
```

Optional interface:

```go
type Capabilities struct {
    Search, Dependencies, Updates, Auth bool
}

type CapabilityReporter interface {
    Capabilities() Capabilities
}
```

- Sources not implementing `CapabilityReporter` are assumed fully capable
  (built-ins unchanged).
- Custom sources return `ErrNotSupported` (wrapped, with source ID) from
  operations they cannot perform.
- CLI: `errors.Is(err, source.ErrNotSupported)` ⇒ clean one-line notice, not a
  stack of wrapped errors.
- TUI: unsupported actions are hidden or disabled for the selected mod's source.
- Core: auto-dependency resolution and update checking treat "not supported" as
  an empty result plus a notice; never a hard failure.

### 8. Management commands

- **`lmm source list`** — every registered source plus failed definitions:
  ID, name, type (built-in/manifest/api/directory), auth state, capabilities,
  load error if any.
- **`lmm source validate <file>`** — full validation with actionable messages;
  `--probe` additionally performs a live smoke test (manifest fetch+parse, or a
  search/get_mod call for api types).
- **TUI:** read-only Sources screen mirroring `source list` (fits the existing
  views pattern; mutations remain CLI-side in v1, consistent with the Phase 5
  TUI roadmap).

### 9. Error handling & security

- Definition errors: warn-and-skip at startup; full detail via
  `source list` / `source validate`.
- Operational errors: fail fast, wrapped with source ID and action context
  per GO.md (`fmt.Errorf("source %q: searching: %w", id, err)`).
- HTTPS enforced for `api.base_url`, remote manifest URLs, and manifest file
  URLs; per-definition `allow_http: true` opt-out; local paths exempt.
- All placeholder substitutions URL-escaped; API keys never logged.
- Manifest `sha256` feeds existing checksum verification; directory-source
  copies are local and skip it.
- Directory source: configured path must exist and be a directory; symlinked
  mod directories are followed; scan results never traverse upward out of the
  configured path.

### 10. Package layout

```
internal/source/
├── source.go            # + ErrNotSupported, Capabilities, CapabilityReporter
├── custom/
│   ├── definition.go    # SourceDefinition types + validation
│   ├── directory.go     # directory source
│   ├── manifest.go      # manifest source + manifest document types
│   ├── api.go           # declarative REST source
│   ├── mapping.go       # dot-path resolution, template substitution
│   └── metadata/        # well-known metadata readers
│       ├── reader.go    # reader interface + registry
│       └── modinfo.go   # 7D2D ModInfo.xml
internal/storage/config/
└── sources.go           # loading ~/.config/lmm/sources/*.yaml
cmd/lmm/
└── source.go            # `lmm source list|validate`
```

(Exact file split may flex during implementation; package boundaries hold.)

### 11. Testing strategy

TDD throughout, per repo convention:

| Area | Approach |
| ---- | -------- |
| Definition loader/validator | Table-driven tests with bad-YAML fixtures (missing fields, bad IDs, collisions, http URLs) |
| Directory source | `t.TempDir()` fixture trees; `ModInfo.xml` parsing cases; copy-mode vs extract-mode scans |
| Manifest source | `httptest` server + local-file manifests; TTL cache behavior; checksum propagation |
| API source | `httptest` servers per endpoint shape: pagination variants (page_start 0/1, offset), auth header/query, malformed JSON, missing required fields |
| Aggregate search | Mock sources: merging, ordering, per-source failure isolation, unsupported-search skipping |
| End-to-end | Install from a directory source through real cache + linker in temp dirs |
| CLI | `source list` / `source validate` output including error display |

### 12. Delivery

- MINOR version bump → **v1.6.0**; README and CHANGELOG updated (three source
  types, manifest format spec, new commands, aggregate search).
- Work tracked under a GitHub issue (to be created/linked before implementation).
- Implementation phases:
  1. **Foundation** — `ErrNotSupported` + capabilities, definition
     loader/validator, `lmm source list|validate`
  2. **Directory source** — modlets use case end-to-end
     (scan → metadata → install → update)
  3. **Manifest source** — format spec, client-side ops, checksum wiring
  4. **API source** — templates, mappings, pagination, auth
  5. **Aggregate search** — core + CLI + TUI badge/picker

Each phase lands independently valuable and releasable.
