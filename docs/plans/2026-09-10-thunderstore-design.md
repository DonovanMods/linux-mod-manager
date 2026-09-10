# Thunderstore as a built-in source — Design (#360)

**Date:** 2026-09-10 · **Status:** For review · **Issue:** #360 (epic #357, spike #267) · **Target:** v2.0.0

Authority: issue #360 plus its 2026-09-09 correction, over the spike's §1.4 and §5
(`docs/plans/2026-09-09-bepinex-spike.md`). Every number in §2 was **re-measured on
2026-09-10** against the live API — [Appendix A](#appendix-a--live-requests-made-while-writing-this-document)
lists the exact requests. No test in this design ever touches the network. Each numbered
section is a decision, not a menu; §7 holds the only genuine forks.

This is **Tier 3** of the BepInEx epic. Tiers 1–2 (#358, #359) are a prerequisite for the
*dependency routing* in §3.4 only; everything else here stands alone.

---

## 1. Source identity, game mapping, capabilities

**Source id `thunderstore`**, `Name() "Thunderstore"`, `TypeLabel() "built-in"`, in a new
package `internal/source/thunderstore`. Registered by one line in
`internal/app/sources.go`'s `builtinSourceFactories` and one entry in `builtinSourceIDs`,
exactly like `steamworkshop`. The factory takes `Paths` because the index lives under the
cache root:

```go
func(p Paths) source.ModSource {
    return thunderstore.New(thunderstore.Options{CacheDir: p.CacheDir})
},
```

`lmm source list`, `lmm auth`, the SPA's `SourcesMapEditor` and the setup sources card all
enumerate the registry, so all four work with no change.

**A game maps to it through `games.yaml`'s existing `sources:` block, and the per-source
game id IS the Thunderstore community slug**: `sources: {thunderstore: "lethal-company"}`.
That lands in `domain.Game.SourceIDs`, which `Service.SearchMods`, `Service.GetMod` and
`Updater.CheckUpdates` already translate through — no new config key, no schema change.

**How a game learns its slug — two paths, both existing:**

1. **Curated `steam-games.yaml` entries.** `steam.GameInfo` already carries a full
   `Sources map[string]string` (#177's Icarus precedent), so an entry gains
   `sources: {nexusmods: …, thunderstore: lethal-company}` and `lmm game detect` prefills
   it. T2 seeds the Linux-relevant communities as data, not code: Lethal Company (1966720),
   Valheim (892970), Risk of Rain 2 (632360), R.E.P.O. (3241660), Content Warning (2881650).
   A user adds their own in `~/.config/lmm/steam-games.yaml`, which `LoadKnownGames` already
   merges over the embedded list.
2. **`lmm game edit --source thunderstore=<slug>`** and `PUT /api/v1/games/{id}` (#326),
   which already REPLACE a game's source map. Nothing to build.

There is deliberately **no community auto-detection**: the slug is not derivable from a
Steam app id, and a wrong guess silently indexes the wrong 34 MB.

**Capabilities** — `source.Capabilities{Search: true, Dependencies: true, Updates: true,
Auth: false, Versions: true}`.

- `Auth: false` is the headline: Thunderstore needs **no key at all** — search, metadata,
  downloads. It is the first built-in with no credential, so it implements no
  `EnvKeyProvider` and `app.authState` reports `AuthNone`.
- `Versions: true`: every package carries a complete `versions[]` whose `version_number` is
  a strict `x.y.z` (verified across all 50,707 lethal-company packages), so
  `core.ResolveVersionFiles` resolves an exact version to a file. `Dependencies: true`: §3.4.
- **No** `Fetcher`, `LocalFileServer` or `BatchModDescriber` — a `download_url` is a plain
  public HTTPS URL and the local index answers every id in one read. It **does** implement
  `source.ExactFileSizer` (`file_size` is exact) and `source.RefreshingUpdateChecker` (§3.5).
- One **new** optional interface, `source.LocalIndexSource` (§2.6), is the only addition to
  `internal/source/source.go`.

---

## 2. The local index

### 2.1 Why there is one

`GET /c/<community>/api/v1/package/` is the **only** public listing endpoint and it is
unpaginated: `?page=2` is ignored and returns the whole document byte-for-byte (measured).
The endpoint that *does* paginate, `/api/experimental/frontend/c/<community>/packages/`,
returns **403**. There is no per-query search, so "search Thunderstore" must mean "search a
local copy" — a new capability for lmm, whose every other source searches remotely.

### 2.2 Measured cost — the numbers this design is sized on

Re-measured 2026-09-10 (Appendix A). `repo` is typical; `lethal-company` is the largest
community on the site.

| | `repo` | `lethal-company` |
| --- | --- | --- |
| On the wire (gzip) | 3.76 MB | 34.6 MB |
| Decoded JSON | 33.5 MB | 329 MB |
| Packages / versions | 6,356 / 25,318 | 50,707 / ~200k |
| **Streaming decode → disk** | **134 ms** | **1.23 s** |
| **Peak Go heap during that** | **8.2 MB** | **25.9 MB** |
| `index.json` (search index) | 1.19 MB | 8.62 MB |
| `packages.jsonl` (detail store) | 19.1 MB | 220 MB |
| Index load into memory | 2 ms | 17 ms (87 ms incl. case folding) |
| Resident after load | 11 MB | 42 MB |
| One search over the loaded index | <1 ms | 1.5–3.6 ms |
| Conditional GET when unchanged | **304, 0 bytes** | **304, 0 bytes** |

The two facts that decide the design: a **streaming** decode never holds the 329 MB
document (peak heap 26 MB), and the searchable projection is **2.6 %** of the decoded size.

### 2.3 Layout on disk

```text
<CacheDir>/_thunderstore/<community>/
    index.json        the searchable projection + an offset table
    packages.jsonl    one full package record per line, byte-addressed by index.json
    watermark.json    {"last_modified": "...", "fetched_at": ..., "packages": N, "schema": 1}
```

The `_`-prefixed root is `steamworkshop`'s convention and for its reason: `_` is unreachable
as a game slug (`core.DeriveGameID` never emits one), so this tree cannot collide with the
game-scoped mod cache sharing the root. `<community>` is validated against
`^[a-z0-9][a-z0-9-]{0,63}$` before it is joined onto a path — the slug comes from
games.yaml, which the user edits.

**`index.json`** is a JSON array of fixed-shape rows, not objects (the field names would be
a third of the file):

```text
[full_name, description, categories[], date_updated, latest_version, is_deprecated, offset, length]
```

**`packages.jsonl`** holds the lean record: `full_name`, `date_updated`, `categories`,
`is_deprecated`, the latest description and website, and **every** version as
`[version_number, file_size, date_created, dependencies[]]`. `download_url`, `icon` and the
per-version `name`/`full_name` are **not stored** — they are pure functions of
`owner`/`name`/`version`, which is 30 % of the file saved for a `fmt.Sprintf`. Versions are
**not** capped: a cap of 10 saves 33 % (measured) and costs the ability to roll back to
anything older, and 220 MB for the largest community on the site — beside a mod cache
measured in gigabytes — is the right trade.

### 2.4 Fetch, refresh, and the watermark

One `GET`, `Accept-Encoding: gzip`, through the shared `httpclient` transport with the
`steamworkshop`-style retry/backoff `RoundTripper` (429/5xx, jitter, a process-lifetime
circuit breaker) — the house pattern, lifted, not re-invented.

- **Cold** (no `index.json`, a `watermark.json` schema mismatch, or either file missing or
  short): fetch, stream-decode, write. §2.7 covers what the user sees.
- **Warm, within TTL**: no request at all. **TTL = 6 hours**, `steamworkshop`'s
  `positiveTTL` and for its reason: a package the user is about to install did not change in
  the last six hours, and if it did, the next `lmm update` catches it.
- **Warm, past TTL**: conditional `GET` with `If-Modified-Since: <watermark.last_modified>`.
  **304 → zero bytes**, stamp `fetched_at`, done. 200 → re-stream, rewrite both files.
- **`--refresh`**: skip the TTL, still send `If-Modified-Since` — a 304 is the correct answer
  to "is it current?" and costs nothing.

The rewrite is **write-then-rename**, both files, `steamworkshop`'s `metaCache.put`
discipline; `watermark.json` is renamed **last**, so a torn refresh reads as cold rather
than as valid-but-stale. A failed refresh over a **usable index is not an error** — it
serves the stale one and surfaces a `core.SearchReport` warning, the channel
`searchAllSources` already uses for a per-source failure; a failed refresh with **no** index
is.

### 2.5 Search over it

Loaded **lazily**, on the first `Search` in a process, and cached in the `Source` for the
process's life (a `sync.Mutex`; `lmm serve` holds one per community it has been asked
about). Load builds one lowercased haystack per row — `full_name \x00 description \x00
categories` — the 87 ms / 42 MB worst case above. Matching: every whitespace-separated term
must hit, `strings.Contains`, case-folded. Scoring, summed per term —
exact `full_name` or `name` **100** · `name` prefix **40** · `name` substring **20** ·
`owner` substring **10** · an exact category **8** · description substring **3** · and a
**deprecated** package is scaled ×0.25 and never ranks above a non-deprecated hit. Ties
break on `date_updated` descending, then `full_name` ascending, so paging is stable; an
**empty query** browses by `date_updated` descending.

`SearchQuery.Category` filters Thunderstore's `categories` (exact, case-folded — "Mods",
"BepInEx", "Client-side") and `SearchQuery.Tags` filters the same field, ANDed; both apply
before ranking, so `TotalCount` counts filtered hits.

**Pagination is exact and deep** — Thunderstore is the first source that knows its own
total: `SearchResult.TotalCount` is the true hit count, `Page`/`PageSize` slice the ranked
slice, and a page past the end is an empty `Mods` with the same `TotalCount`, not an error.
`PageSize` defaults to 20, capped at 100, matching `steamworkshop`. **No inverted index**: a
full scan of the largest community is 1.5–3.6 ms, so anything cleverer is a data structure
to keep correct for a 3 ms saving.

### 2.6 The seam: `source.LocalIndexSource`

The one addition to `internal/source/source.go`, in the established optional-capability shape
(core type-asserts; nothing in core imports the concrete package):

```go
// IndexStatus describes a source's local search index for one game.
type IndexStatus struct {
    GameID    string // the source's own game id (a Thunderstore community slug)
    Present   bool
    Packages  int
    FetchedAt time.Time
    Bytes     int64 // on-disk footprint
    Stale     bool  // present, but past its TTL
}

// IndexProgressFunc reports one tick while an index is being built. Same
// serialization contract as source.FetchProgressFunc.
type IndexProgressFunc func(phase, detail string, bytes int64)

// LocalIndexSource is implemented by sources that answer Search from a
// locally cached index rather than a remote query.
type LocalIndexSource interface {
    IndexStatus(ctx context.Context, sourceGameID string) (IndexStatus, error)
    RefreshIndex(ctx context.Context, sourceGameID string, force bool, progress IndexProgressFunc) (IndexStatus, error)
}
```

Core gains two methods in a new `internal/core/source_index.go`:
`SourceIndexStatus(ctx, sourceID, gameID) (*IndexStatus, error)` — **nil** for a source that
is not a `LocalIndexSource`, which is how a frontend hides the surface — and
`RefreshSourceIndex(ctx, sourceID, gameID, force, sink) (*IndexReport, error)`, gated by
`beginOp` (it writes under the cache root) and forwarding ticks into the sink as
`StepEvent`s (`IndexReport` is §4.3's `--json` document). `Service.SearchMods` is otherwise
**unchanged**: the source refreshes itself on TTL inside `Search`, and the explicit method
exists for the one-time cold case and the manual refresh button, not for the hot path.

### 2.7 First search, and what the user sees

`Search` on a cold index fetches it — correctness must not depend on a frontend remembering
to prime it. What the frontends add is the **readout**:

- **CLI.** `lmm search` calls `SourceIndexStatus` first, and when `Present` is false prints
  one line to **stderr** before searching — so `--json` on stdout stays a single document
  (the `lmm update --json` invariant): `Building the Thunderstore index for lethal-company
  (one-time, ~35 MB)…`, then `Indexed 50,707 packages in 4.1s.` A `Stale` index prints
  nothing; its refresh is a 304.
- **SPA.** The setup sources card shows the index row (community, package count, "updated
  2 h ago", footprint) with a **Refresh index** button. Cold, the search page renders its
  existing loading state and the request takes a few seconds once.

### 2.8 Footprint and pruning

`_thunderstore/` is `lmm cache` territory. **`lmm cache prune` removes any
`_thunderstore/<community>/` no game currently maps** — the index caches a public document
and is always rebuildable, so a community the user stopped playing has no claim on 220 MB —
and prunes a **mapped** community only past 30 days of `fetched_at`. `lmm cache info` lists
each index with its size; `--all` removes them regardless, as everywhere else.

---

## 3. The package model → `domain.Mod`

### 3.1 Identity

**`domain.Mod.ID` is the package's `full_name`** — `RugbugRedfern-Skinwalkers`. Verified
across all 50,707 lethal-company packages: `owner` and `name` are strictly `[A-Za-z0-9_]+`
and `full_name` is always exactly `owner + "-" + name`, so the split on the **first** `-` is
unambiguous; version numbers are strictly `x.y.z`, so `Namespace-Name-Version` splits on the
**last two** just as unambiguously. Two exported helpers carry that, with tests pinning the
invariants: `SplitPackage(full) (ns, name, ok)` and `SplitDependency(dep) (ns, name, version,
ok)`. Identity is **not** the `uuid4`: `full_name` is what the user types, what a dependency
string names, what the URL contains, and what survives a re-upload.

### 3.2 Field mapping

`ID` ← `full_name`; `SourceID` ← `"thunderstore"`; `GameID` ← the community slug (core has
already translated it); `Name` ← `name` with `_` → space; `Author` ← `owner`; `Version` ←
`versions[0].version_number` (Thunderstore orders newest-first); `Description` ←
`versions[0].description`, raw, per the #86 precedent; `URL` ← `package_url`; `ImageURL` ←
derived, `https://gcdn.thunderstore.io/live/repository/icons/<full_name>-<ver>.png`;
`UpdatedAt`/`CreatedAt` ← `date_updated`/`date_created` (RFC 3339); `Categories` ←
`categories`; `Dependencies` ← §3.4.

`is_deprecated` gets no new `domain.Mod` field: it rides in `Categories` as a synthetic
`"Deprecated"` entry, which every existing renderer already prints, and drives §2.5's
ranking penalty.

### 3.3 `GetModFiles` / `GetDownloadURL`

A Thunderstore package version **is** one file — there is no file list, no primary-file
choice, no optional-vs-main. `GetModFiles` returns one `domain.DownloadableFile` **per
version**, newest first, straight out of `packages.jsonl` with no network call at all:

```go
domain.DownloadableFile{
    ID:        version_number,             // the file id IS the version
    Name:      "<Name> <version>",
    FileName:  "<ns>-<name>-<ver>.zip",
    Version:   version_number,
    Size:      file_size,                  // exact — see ExactFileSizer
    IsPrimary: i == 0,                     // the newest version
    Category:  "MAIN",
    SHA256:    "",                         // Thunderstore publishes no checksum
}
```

That shape makes `core.ResolveVersionFiles` (#96), `lmm mod files`, the rollback flow and
the SPA's versions table work with **zero** source-specific handling — a version *is* a
file, which is what those flows already assume. `GetDownloadURL` is pure string
construction, `https://thunderstore.io/package/download/<ns>/<name>/<fileID>/`, validating
`fileID` against the package's known versions first so a caller cannot synthesise a URL for
a version that does not exist. No token, no expiry, no second round trip.

### 3.4 Dependencies, and the loader

`GetDependencies` parses `versions[<installed>].dependencies[]` — the *installed* version's,
not the latest's — and returns `[]domain.ModReference`:

```go
domain.ModReference{SourceID: "thunderstore", ModID: ns + "-" + name, Version: version}
```

Thunderstore's clients treat the pinned version as a **minimum**; lmm records it as
`ModReference.Version` — already "the installed-version record" — and lets the existing
resolver and lock machinery own it. `core.DependencyResolver` needs **no change**: these are
ordinary `SourceID`/`ModID` refs in the same source.

**The loader entry is routed out.** A dependency whose `name` is `BepInExPack` or begins
`BepInExPack_` — in **any** namespace — is the BepInEx framework, not a mod:

- `BepInEx-BepInExPack` (40,526 of lethal-company's packages), `bbepis-BepInExPack` (455),
  `denikson-BepInExPack_Valheim` (38), `BepInEx-BepInExPack_H3VR` (33), and six others.
  The namespace varies; the **name** is the reliable signal. 41,045 of 50,707 packages
  declare one.
- Everything else in the `BepInEx` namespace stays an ordinary mod —
  `BepInEx-MonoMod_Loader`, `CatsArmy-BepInEx_GUI` and friends are real packages.

Those entries are **excluded from the returned `ModReference`s** and surfaced instead as the
Tier-2 loader precondition (#359): `GetDependencies` drops them, and the source exposes
`LoaderRequirement(mod) (kind, version, ok)` returning `("bepinex", "5.4.2100", true)` for
core's precondition to consume. **Tiers 1–2 are not a build prerequisite for this unit** —
until #359 lands the requirement is dropped with a plan warning naming the pack, strictly
better than resolving BepInExPack as a mod and deploying a framework into the mod path. A
dependency naming a package **not in this community's index** (it is listed elsewhere) is a
plan warning, not a failure: the resolver's `missing dependency` path says exactly that.

### 3.5 `CheckUpdates`

`date_updated` makes this **entirely local**: one index read, zero requests.
`CheckUpdatesRefreshing` refreshes the index (TTL, or forced by `--refresh`), then compares
each installed mod's recorded `Version` against the package's newest `version_number`.
Different → a `domain.Update` carrying the new version, its `date_created` and its
`file_size`. A mod whose package has left the index is reported unavailable, not as an
error — `ModDescription`'s rule, applied by hand since there is no batch call to make.

Comparison is **string equality, not semver ordering**: Thunderstore's `versions[0]` is
authoritative about what "newest" means (it is publish order), and a re-published older
version is something lmm should offer, not hide behind a `>` test.
`UpdateProgressReporter` is implemented too, so `lmm update`'s progress line behaves as it
does everywhere else even though the check is microseconds.

---

## 4. Frontends

Both stay thin: every rule above lives in `internal/source/thunderstore` or
`internal/core/source_index.go`, with its own tests.

### 4.1 CLI

| Surface | Change |
| --- | --- |
| `lmm search` | The cold-index stderr line (§2.7). New `--refresh` flag: "rebuild a locally cached source index before searching". |
| `lmm search --json` | Unchanged document; the notice goes to **stderr**. |
| `lmm install` / `mod show` / `mod files` / `update rollback` / `lmm auth` | **Nothing.** A version-is-a-file source needs no special casing, and `Auth: false` means `lmm auth status` prints `n/a` and the login path never offers it. |
| `lmm update --refresh` | Already exists; now also means "re-fetch the Thunderstore index". Help generalised from "Steam Workshop caches for hours" to "bypass cached source metadata and locally cached source indexes". |
| `lmm cache info` / `prune` | One row per cached index; §2.8's rules. |
| `lmm source index` | **New, one command**: `lmm source index [--source thunderstore] [--refresh] [--json]` — show or rebuild the index for the active game's sources. The surface a user reaches for when a brand-new package will not show up. |

`make man` regenerates for `--refresh` on `search` and for `lmm source index`.

### 4.2 `lmm serve`

| Surface | Change |
| --- | --- |
| Search page | **Nothing structural** — it is already source-scoped and paginated (#331), and Thunderstore is the first source that gives it an exact `TotalCount`. |
| Setup → Sources card (`setupsources.js`) | A per-index row: community, package count, relative "updated", footprint, and a **Refresh index** button. |
| `GET /api/v1/sources/{id}/index?game=…` | **New**, read-only: `core.IndexStatus` as JSON, or `404` when the source keeps no index. |
| `POST /api/v1/sources/{id}/index` | **New**, settings-class single-step write (the `api_mod_settings.go` shape — no plan, no job): body `{"refresh": true}`, returns the `IndexReport`. Synchronous; the worst measured case is ~5 s including the download. CSRF-guarded like every other mutation. |
| Everything else | Unchanged. |

**No new job kind, no new SSE frames**: a job exists to make a long mutation cancellable and
resumable across a page reload, and a five-second idempotent cache rebuild is neither.

### 4.3 Typed errors and JSON

Two new typed errors in `internal/source/thunderstore`, both classified in
`internal/core/errors.go` so a frontend that cannot import the source package still branches
on them (`core.IsIndexUnavailable`, `core.IsCommunityNotConfigured`):

- `ErrIndexUnavailable` — no index on disk and the fetch failed. `Details()` carries the
  community and the underlying reason. HTTP 502.
- `ErrCommunityNotConfigured` — the game maps `thunderstore` to an empty or malformed slug.
  `Details()` carries the game id and the offending value, so the SPA can deep-link the
  sources editor. HTTP 400. The source does **not** implement `GameIdentifierIgnorer`: an
  empty mapping is a misconfiguration here, not a legitimate state.

**JSON is additive only.** Two new documents, each with its own golden and its own
`-update-*` flag:

- `core.IndexStatus` — `{"source","game","present","packages","fetched_at","bytes","stale"}`
  — and `core.IndexReport` — `{"source","game","status","changed","packages","bytes",
  "duration_ms","warnings"}`: goldens `internal/core/testdata/json/index_status.json` and
  `index_report.json`, re-recorded through the existing **`-update-json-goldens`**.
- `cmd/lmm/testdata/json_golden/source_index.json` for `lmm source index --json`,
  re-recorded through a **new** `-update-source-index` flag (per-command flags, per CLAUDE.md).
- `internal/serve` adds no wire type of its own — both routes return the core documents
  verbatim — so `TestJSONWireContractCoverage` stays green with no new serve golden.

No existing document changes shape: `domain.Mod`, `domain.DownloadableFile`,
`core.SearchReport` and `core.ModDetail` are populated by §3's mapping, not extended.

---

## 5. Testing

**No test makes a live request.** A `TestNoTestReachesTheProductionAPI` ratchet, copied from
`steamworkshop`'s, greps the package's `_test.go` files for `thunderstore.io`;
`Options.BaseURL` is required by every test constructor.

| Layer | Approach |
| --- | --- |
| Fixtures | `internal/source/thunderstore/testdata/` — **`community_small.json`**, a **hand-built** index of ~12 packages covering every shape: one version, five versions, deprecated, a `BepInEx-BepInExPack-5.4.2100` dependency, a `denikson-BepInExPack_Valheim-…` one, a `BepInEx-MonoMod_Loader-1.0.0` one (an ordinary mod in the loader's namespace), an unresolvable dependency, an `_` in a name, a Unicode description. Plus **`package_detail.json`**, one `/api/experimental/package/…` response. Hand-written, committed with a header naming this design; no live capture is checked in. |
| Index build | `httptest` serving `community_small.json` with `Last-Modified` and gzip: package count, `index.json` offsets actually addressing the right `packages.jsonl` lines, watermark contents, byte-for-byte idempotence of a second build. |
| Conditional refresh | The same server answering `304` on a matching `If-Modified-Since`: assert **no rewrite** (mtimes and bytes unchanged), `fetched_at` advanced, and that a changed `200` **does** rewrite. `Options.Now` injects the clock, so the TTL is asserted without spending it. A truncated `packages.jsonl` or a missing watermark is treated as cold, not served. |
| Path safety | Table test over community slugs — `../../etc`, `a/b`, `""`, 300 chars, uppercase — every one refused before any path is joined. |
| Search | Table-driven over the fixture: ranking order, the deprecated penalty, multi-term AND, category and tag filters, empty-query browse order, exact `TotalCount`, page past the end, `PageSize` clamping, and tie-break stability across two pages. |
| Identity + mapping | Tables for `SplitPackage`/`SplitDependency` (`A_B-C_D-1.2.3` and every malformed shape); `GetMod`/`GetModFiles`/`GetDownloadURL`/`GetDependencies` against the fixture, with a golden for the resulting `domain.Mod`. Loader routing: the three loader-shaped dependencies are excluded and reported via `LoaderRequirement`; `BepInEx-MonoMod_Loader` is **not**. |
| Updates + backoff | `CheckUpdatesRefreshing` over up-to-date / behind / package-gone / `refresh=true`; and `transport_internal_test.go`'s shape lifted — 429 with `Retry-After`, 5xx, the circuit breaker, a cancelled context cutting a backoff short. |
| Core + serve | `SourceIndexStatus`/`RefreshSourceIndex` against a fake `LocalIndexSource`, the nil-for-a-non-indexed-source path, goldens for both JSON documents; then `httptest` against a seeded `Service` for both routes, the CSRF refusal, the 404 for a non-indexed source and the 400 for an unconfigured community. **No E2E is added** — the card's new row displays an existing document and its button drives an ordinary POST. |

Sandboxing is not negotiable: `HOME` **and** every `XDG_*` var redirected, `t.TempDir()` for
the cache root, nothing touching a real Steam library or game directory. Every exported
identifier in the new package carries a doc comment, `steamworkshop`'s own convention.

---

## 6. Implementation plan

Three units, sequential — each a `git merge --no-ff` onto `dyoung522/v2-thunderstore`,
closing #360's tracking with a comment naming the merge commit.

### T1 — "Thunderstore: the community index, fetched, cached and searched (#360)" — **L**

`internal/source/thunderstore` from nothing: `Options`/`New`/identity/capabilities, the retry
transport, the streaming index builder, the watermark and conditional refresh, the
path-safety gate, the resident search index and its ranking, `Search` onto
`SearchQuery`/`SearchResult`, and `source.LocalIndexSource`; registration in
`internal/app/sources.go`. Everything in §5's first five rows. *The whole risk of this
design lives here*: nothing downstream is worth building until a cold `lethal-company`
index builds in a second and a half.

### T2 — "Thunderstore: packages, versions, dependencies and update checks (#360)" — **M**

`GetMod`, `GetModFiles`, `GetDownloadURL`, `GetDependencies` with the loader routing,
`ExactFileSizer`, `CheckUpdatesRefreshing`/`UpdateProgressReporter`, `SplitPackage`/
`SplitDependency`, `LoaderRequirement`, and the curated `steam-games.yaml` entries. This is
where `lmm install thunderstore:…` first works end to end.

### T3 — "Thunderstore: the index surface in both frontends (#360)" — **M**

`internal/core/source_index.go` (`SourceIndexStatus`, `RefreshSourceIndex`, `IndexStatus`,
`IndexReport`, the two error classifiers), the JSON goldens and their flags, `lmm source
index` + `lmm search --refresh` + `make man`, `lmm cache info`/`prune` integration, the two
`/api/v1` routes, and the setup sources card row. **No parallelism**: T2 reads the index T1
writes and T3 renders what T2 produces, so three worktrees would spend more on conflicts in
one small package than the wall-clock saves.

---

## 7. Open questions

Only two things here have no clearly better answer.

**Q1 — Does `lmm search` with no `--source` include Thunderstore on a cold index?**
`searchAllSources` fans out to every configured source, so a cold index turns a 300 ms
multi-source search into a 5-second one, once, without the user having asked for
Thunderstore specifically. *Recommendation: **yes, include it**, with §2.7's stderr line
naming what is happening.* Silently omitting a configured source is the worse surprise — the
user concludes the package is not on Thunderstore. The alternative (skip a cold index in the
fan-out, include it once warm) makes results depend on invisible cache state, which is
harder to explain than a one-time wait.

**Q2 — Should `lmm serve` build a cold index synchronously on the search request?** Measured
worst case ~5 s (1.3 s transfer + 1.2 s decode + write) inside a plain `GET /api/v1/search`.
*Recommendation: **yes, synchronously**, revisited only if it proves slow on a spinning
disk.* A job kind for it would cost a plan kind, a job kind, an SSE contract and a plan
renderer to save four seconds that happen once per community; the setup card's manual
**Refresh index** button covers the user who would rather pay it deliberately.

Everything else in this document is a decision.

---

## Appendix A — live requests made while writing this document

Read-only `GET`s against Thunderstore's public API on **2026-09-10**, no credential (none
exists). Recorded because the design is sized on them; **no test may repeat any** (§5).

1. `HEAD /c/repo/api/v1/package/` — `Last-Modified`, `Cache-Control: max-age=30`, gzip, `content-length: 3,761,768`.
2. `GET /api/experimental/package/RugbugRedfern/Skinwalkers/` — the package-detail shape (`latest`, `community_listings[]`, `dependencies: ["BepInEx-BepInExPack-5.4.2100"]`).
3. `GET /api/experimental/community/?page_size=3` — cursor pagination, and `page_size` **ignored**.
4. `GET /c/repo/api/v1/package/` — the typical-community measurements (6,356 packages, 25,318 versions, 33.5 MB decoded).
5. The same with `If-Modified-Since` — **304, 0 bytes**.
6. The same with `?page=2` — returns the **whole** index unchanged: genuinely unpaginated.
7. `GET /c/lethal-company/api/v1/package/` — the worst case (34.6 MB wire, 329 MB decoded, 50,707 packages), and the corpus every identity/dependency invariant in §3 was verified against.

§2.2's decode/size/search timings came from a throwaway Go program in `/var/tmp` reading the
two saved responses off disk. Neither response, nor any part of one, is committed — §5's
fixtures are hand-built.

---

## Approved with notes (coordinator, 2026-09-10)

Approved as written. The two open questions are answered as recommended:

- **Q1 — yes.** An unscoped `lmm search` includes a cold Thunderstore index, with §2.7's one
  stderr line naming what is happening (and the equivalent line in the web search page's
  progress area); results never depend on invisible cache state.
- **Q2 — yes, synchronously**, inside `GET /api/v1/search`, with two conditions: the build is
  cancellable through the request context (a closed tab must not leave a half-written index —
  §2.4's write-then-rename already guarantees the on-disk half), and it runs as a READ under
  the Service's query/mutation contract (it must never take the mutation slot or the
  cross-process lock).
- **Decision 18 (loader routing)** depends on #359's `LoaderRequirement`. T2 consumes whatever
  #359 has merged by then; if #359 has not merged, T2 defines the seam exactly as #359's spec
  names it and #359 rebases onto it. Either way the two units share ONE definition.
- **Decision 4 (`Auth: false`)** must not break the auth surfaces: `lmm auth status` and the
  setup card list the source with "no credential needed", not as unauthenticated.
- Units T1 → T2 → T3 run sequentially as designed; each is its own story issue under #360.
