# Feature Design: PAK→EXMOD Merge-Time Conversion (Icarus)

**Date:** 2026-08-05
**Status:** Approved design — implementation not started
**Builds on:** Spike #220 (PARTIAL GO) — `docs/plans/2026-08-05-icarus-pak-to-exmod-spike-findings.md`
on frozen branch `spike/pak-to-exmod`. Spike constraints 4 (case-insensitive
pair normalization) and 5 (the four upstream values) are binding inputs here.

## Problem and Semantics

Prebuilt PAK mods are frozen in time: their tables are whole-file snapshots
of whichever `data.pak` existed when the author built them. Deployed raw,
they are shadowed by `zzz_LMM_Merged_P.pak` wherever tables overlap, and
they silently revert base-game updates on every field of every row they
touch.

**The feature: rebase.** At merge time, decompile the pak, rebuild its
changes against the game's _current_ `data.pak`, and merge it with all
other deployed mods exactly as exmodz mods merge today.

- **Tier 1 (exact):** when the pak embeds a `data.EXMOD` manifest, use it —
  it is pure author intent, applied to the current base via the normal
  upsert path. (Covered 5 of 8 hard cases in the spike corpus.)
- **Tier 2 (derived):** otherwise, diff the pak's tables against the
  current base by row `Name`, emitting changed fields whole-field and new
  rows verbatim. Drift baked into author-touched rows is **accepted by
  design** (user ruling 2026-08-05): it is the rebase semantic, not an
  error. Base-only rows are staleness, never deletions.
- **Failure is per-mod:** an irreconcilable pak produces a clear error,
  skips the merge, and falls back to raw deploy (today's behavior). Other
  mods continue. No partial conversion of a mod.

Load-order upserts, asset whole-file last-wins with warning, merged-pak
regeneration triggers — all downstream behavior unchanged.

## Architecture (Approach A — merge-time conversion from retained raw pak)

Chosen over ingest-time synthesis because the derived diff goes stale on
every weekly base update: the raw pak is the durable source; derivation
happens where the current base is already open.

### Ingest seam

For a convert-eligible pak (compile-mode game, conversion not opted out),
ingest retains the raw `.pak` under `cache.RetainedSourceName(fileID)` —
the same marker that drives merge membership today. The spike proved this
seam requires zero downstream schema changes. Both ingest paths (download
`service.go` and import `importer.go`) get the same widened predicate:
compile-eligible = exmodz OR convert-eligible pak.

The pak/exmodz mutual-exclusion validation and the exmodz-`IsPrimary`
default (#211) are unchanged — exmodz remains the preferred form when the
author ships one.

### Merge seam

`source.MergeSource.ExmodzPath` is renamed `SourcePath` (internal
interface; update the icarus alias and all call sites). `MergeCompile`
distinguishes source kind by file extension:

- `.exmodz` → existing path, unchanged.
- `.pak` → in-memory conversion: open pak → normalize entries
  (case-insensitive mount+entry pair, original case preserved) → Tier 1
  embedded-`data.EXMOD` if present, else Tier 2 table diff against the
  already-open current base state → feed the resulting rows through the
  same `ApplyRowPatch` sequence; assets pass through the existing
  sanitizer and last-wins map.
- Conversion failure → the mod contributes nothing; a structured warning
  (mod ref + reason) is returned; remaining sources continue. Note: this
  is a _warning_ at the MergeCompile contract level (non-empty output pak
  still valid) but surfaced as a per-mod **error** in UX.

No synthesized `.exmodz` artifact is written to disk.

### Converter code

Promoted into `internal/source/icarus` (new files, e.g. `convert.go` /
`normalize.go` sibling logic), rewritten to production standard using the
spike package as the reference implementation — not copied wholesale.
Spike lessons that are binding: `hasPrefixFold`-style case-insensitive
prefix matching with original-case preservation; hyphen-in-path guard;
reuse of `sanitizeAssetPath`; the four formerly-duplicated values
(`endOfModSentinel`, mount point, `data/` prefix, `-`↔`/` flatten) are now
intra-package — no exports needed (irrelevant to #170's timeline).
The spike branch itself stays frozen; `spike/pakconvert` is never merged.

## Irreconcilable Criteria (whole-mod failure)

- Pak unreadable: encrypted index, unsupported version, unsupported
  compression (Oodle), corrupt structure.
- Unresolvable entry structure: table entries whose mount+path pair cannot
  be mapped into the base's directory layout (e.g. bare-`Content/` mounts
  with no subdirectory information — the "Intreeg's More Resources" case).
- A pak table absent from the current base (`table-not-in-base`).
- Hyphen-ambiguous table paths (CurrentFile flattening would be lossy).
- `RowStruct` mismatch between the pak's table and the current base's.

Per-row / per-field drift is never an error. `Defaults`-only differences:
warning, table still converts (inexpressible in EXMOD row semantics;
visible in output, accepted as minor loss).

## Overrides and Fallback

- **Per-game config:** `convert_paks: true|false` in `games.yaml`
  (default `true` for compile-mode games; irrelevant otherwise).
- **Per-mod opt-out:** persisted in the DB; CLI `lmm mod convert <mod>
on|off` (naming may be refined at plan time to match existing `mod`
  subcommand conventions) + TUI toggle (CLI/TUI parity is a standing
  repo requirement).
- Merge-membership discovery honors both; opted-out and conversion-failed
  mods deploy raw through the existing manifest/convergence machinery,
  exactly as pak mods deploy today.
- Conversion failures surface with reasons in `deploy`, `update`,
  `verify`, and `status` (human + JSON; JSON-contract additions are MINOR
  per precedent).

## Staleness, Fingerprint, Migration

- `MergedFingerprint` already includes base `IndexHash` and per-mod
  retained-source checksums; a retained pak's MD5 slots in identically.
  Weekly base updates therefore regenerate the merged pak and re-derive
  every converted pak against the new base automatically. The
  Friday-recompile machinery (#196) composes untouched.
- Per-mod convert flags participate in staleness: toggling a mod's
  conversion changes merge membership and must regenerate the merged pak.
- **Migration is lazy:** existing installed pak mods have no retained
  source in cache. Deploy/verify convergence detects convert-eligible pak
  mods lacking the marker and re-ingests from the cached archive (the
  #166 stale re-ingest path). No big-bang migration step. The same path
  heals a mod that is opted in after having been ingested without the
  marker.

## Testing

- Converter unit tests in `internal/source/icarus`, adapted from the spike
  suite: normalization variants (incl. capital-`Data/`, bare-`Content/`,
  hyphen guard), differ rules (changed/new/stale/duplicate/inexpressible),
  Tier 1 embedded-EXMOD extraction, irreconcilable classifications —
  synthetic fixture paks built with `unrealpak.Create` (mod files are
  copyrighted; nothing from the corpus is committed).
- MergeCompile-level tests: pak + exmodz mixed sources, load-order wins,
  failure-skip-continue, warning surfacing.
- **CLI-seam integration tests for every mutation entry point** (install,
  import, deploy, verify --fix, convert toggle) — the merged-pak epic's
  hard lesson; core-level tests alone are insufficient.
- TUI smoke test gate before merge; live in-game validation on the real
  install before release.

## Versioning / Scope

- Ships as **MINOR** (new functionality; JSON additions).
- Single story branch `feat/pak-to-exmod-convert` off `develop`, PR
  `--base develop`; no version bump on the story PR.
- Out of scope: non-Icarus games, Oodle support, #170 module extraction,
  derivation caching (Approach C — add later only if conversion time is
  measured to matter), TUI conversion-report views beyond status surfacing.

## Success Criteria

- A pak-only mod (no exmodz form) installs, converts, and its table
  changes are present in `zzz_LMM_Merged_P.pak` rebased onto the current
  base, alongside exmodz mods, honoring load order.
- Base update → next sync re-derives automatically; no stale table wins.
- An irreconcilable pak yields a clear per-mod error, deploys raw, and
  does not block other mods.
- Opt-outs honored at global and per-mod level from both CLI and TUI.
- All existing exmodz behavior byte-identical when no pak mods are present.
