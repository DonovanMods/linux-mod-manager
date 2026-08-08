# Spike Design: Icarus PAK → EXMOD Internal Conversion

**Date:** 2026-08-05
**Status:** Approved design — spike not yet executed
**Type:** Spike (feasibility investigation; throwaway prototype + findings doc; no production code lands)
**Branch:** `spike/pak-to-exmod` (unmerged; per the plan-doc durability convention)

## Problem

Prebuilt PAK mods cannot participate in the profile-level merged pak
(`zzz_LMM_Merged_P.pak`). The merged pak deliberately sorts last so it wins
same-path conflicts — which means a pak mod's table override is silently
discarded whenever the merged pak touches the same table. Only ~41 of ~538
catalog mods ship an `.exmodz` alternative; the rest are pak-only and locked
out of the merge model entirely.

**Spike question:** can lmm internally convert a prebuilt PAK mod into an
EXMOD-equivalent form (a synthesized `.exmodz`) so pak mods participate in the
merged pak exactly like exmodz mods?

## What recon established (2026-08-05, from archived docs + current code)

- **Reading mod paks is solved.** `internal/unrealpak.Reader` enumerates and
  extracts arbitrary v11 paks (`Files()`, `ReadFile()`); proven against
  173k entries across 34 paks and against real third-party mod paks. Every
  prebuilt mod pak inspected declared an _empty_ CompressionMethods table
  (all entries stored) — no decompression needed; Zlib is covered anyway.
- **The injection seam is clean.** Merge membership is discovered solely by a
  retained-source cache marker (`enabledExmodzSources` →
  `cache.RetainedSourceName(fileID)`). If ingest retains a synthesized
  `.exmodz` for a pak, everything downstream (merge, fingerprint,
  manifest-aware deploy, purge) works unchanged.
- **No diff-derive code exists.** The entire pipeline is diff-_apply_
  (`ParseExmod`, `ApplyRowPatch`). The differ is the spike's genuinely new
  work.
- **Ground truth exists.** ~41 dual-form mods (pak + author's own exmodz);
  additionally some paks embed the original `.EXMOD` inside the pak
  (e.g. Intreegs4XP's `data.EXMOD` entry) — an exact, zero-inference check.
- **Known hazards:**
  - Prebuilt tables are _stale whole-table snapshots_ (measured: 167 rows in
    a mod pak's table vs 374 in live base). A converter must diff against the
    live base `data.pak` and treat base-rows-absent-from-the-pak as
    staleness, never deletions (EXMOD has no delete verb).
  - The `data/` boundary floats between mount point and entry path per mod
    (Intreegs4XP: mount `.../Content/` + entry `data/Experience/…`;
    FloofLevelCap: mount `.../Content/data/Character/` + entry
    `D_CharacterGrowth.json`). Normalization must treat mount+entry as a pair.
  - Asset-bearing prebuilt paks were never dissected; their layout is
    inferred only.
  - EXMOD cannot express: row deletions, `Defaults`/`RowStruct` changes,
    sub-field-level nested partials (shallow row-field merge only).
  - Pak entry names are untrusted; same sanitation discipline as
    `sanitizeAssetPath` applies.

## Spike Questions

1. **Path normalization** — can mount-point + entry-path pairs be normalized
   reliably across real mod paks to base-table mount-relative paths?
2. **Differ fidelity** — does a `Name`-keyed row differ against the _live_
   base tables reproduce author intent? (Ground truth: dual-form mods +
   embedded `data.EXMOD` where present.)
3. **Convertibility census** — what fraction of corpus paks are convertible
   (tables-only) vs asset-bearing vs unreadable (encrypted/odd version/Oodle)?
4. **Asset probe** — does the inferred asset layout hold in 1–2 real
   asset-heavy paks? Could assets pass through as exmodz-style assets
   (`.uasset`/`.uexp`)? _Probe + report only; no asset conversion prototype
   unless it turns out trivial._
5. **Expressiveness gaps** — how often do inexpressible changes
   (`Defaults` edits, apparent deletions, nested partials) occur in practice?
6. **Seam validation** — does a synthesized `.exmodz` flow through the real
   `ParseExmodz` → `MergeCompile` pipeline with zero pipeline changes?

## Deliverable

- Throwaway prototype (converter + validation harness) on `spike/pak-to-exmod`.
- Findings doc `docs/plans/<date>-icarus-pak-to-exmod-spike-findings.md`
  (dated when written) with a **go / partial-go / no-go** recommendation and, on go/partial-go,
  the seams + constraints a feature design would need.
- No production code lands. The spike branch is never PR'd into develop.

## Harness Design (Approach A — throwaway in-module package)

New `spike/pakconvert/` package inside the repo module (so it imports
`internal/unrealpak` and `internal/source/icarus` directly), driven by
explicit integration tests that **skip** unless env vars provide:

- `LMM_SPIKE_ICARUS_DIR` — real Icarus install (for live `data.pak`)
- `LMM_SPIKE_CORPUS_DIR` — local corpus of downloaded mod files

Four parts:

### 1. Converter (the new work)

`pak → synthesized .exmodz`:

1. Open pak with `unrealpak.Open`; refuse encrypted/unsupported politely.
2. Normalize each entry: join mount point + entry path, strip
   `../../../Icarus/Content/` + `data/` (wherever the boundary falls) →
   base-table mount-relative path. Non-JSON entries → classified for the
   census (assets, `data.EXMOD`, junk).
3. For each JSON table: load the corresponding live base table
   (`Reader.ReadFile` on installed `data.pak`); index both `Rows` by `Name`.
   - Row in pak, not in base → new row: emit verbatim (upsert append).
   - Row in both, fields differ → emit `Name` + changed top-level fields
     (whole-field replacement; nested structures replaced whole).
   - Row in base, not in pak → **ignore** (staleness; EXMOD can't delete).
   - `Defaults`/`RowStruct`/other top-level differences → recorded as
     findings, not converted.
   - Table path present in pak but absent from base → finding (census).
4. Emit `.EXMOD` JSON (`CurrentFile` = mount path with `/`→`-`;
   `EndOfMod` sentinel; metadata from the mod's catalog identity) and zip as
   `Extracted Mods/<name>.EXMOD` → synthesized `.exmodz`.
5. All pak entry names treated as untrusted input (same discipline as
   `sanitizeAssetPath`).

### 2. Validator (ground truth + pipeline)

- **Semantic equivalence:** apply the converted diff and the author's own
  exmodz each to the live base tables (via the real `ApplyRowPatch`);
  compare resulting tables row-by-row. Residual differences must be
  individually classified (staleness noise vs real divergence).
- **Exact check:** where the pak embeds `data.EXMOD`, compare our derived
  diff against it directly.
- **Pipeline check:** run the real `ParseExmodz` on the synthesized file,
  then the real `MergeCompile` against the live base pak with the synthesized
  exmodz alongside a genuine exmodz mod; assert clean compile and sensible
  output (spot-check merged tables).

### 3. Asset probe (report only)

Dissect 1–2 asset-heavy paks (candidates: Larkwell Care Package, Turret
Variants): mount points, entry paths, compression, whether the inferred
"assets unprefixed under `Icarus/Content/`" model holds, and whether their
assets would fit the exmodz asset filter (`.uasset`/`.uexp`).

### 4. Corpus census

Sweep every corpus pak: version/encryption/compression, tables vs assets vs
mixed, normalization success, differ outcome. Stats feed the findings doc.

## Corpus

Downloaded fresh via lmm against the live catalog into a git-ignored local
dir (mod files are copyrighted — **never committed**):

- FloofLevelCap + Intreegs4XP (previously dissected pure-JSON paks).
- ~5–8 of the 41 dual-form mods (ground truth).
- 1–2 asset-heavy paks (probe).
- Broader pak sweep if cheap, for census stats.

## Go / No-Go Rubric

- **Go:** semantic equivalence on at least 3 of 4 sampled dual-form mods,
  with every residual difference individually explained (staleness noise is
  acceptable; unexplained divergence is not); clean `MergeCompile`; seam
  validated end-to-end.
- **Partial go:** table conversion sound but assets blocked → recommend a
  feature scoped to table-only paks, assets as follow-up.
- **No-go:** differ cannot reproduce author intent, or format variance
  across paks is unmanageable.

Either way the findings doc records the evidence, the census numbers, and —
on go/partial-go — the open product questions a feature design must answer
(pak/exmodz mutual-exclusion validation, `IsPrimary` defaults, fingerprint
membership for converted paks, double-apply prevention via the
manifest-aware deploy resolver).

## Out of Scope

- Any production code, CLI/TUI surface, DB/schema changes.
- Asset conversion implementation (probe only, unless trivially free).
- Product decisions (selection UX, defaults) — recorded as open questions.
- Oodle support, encrypted paks, non-Icarus games.

## Logistics

- GitHub `Spike:` issue tracks the work.
- Branch `spike/pak-to-exmod` holds spike code + findings; never merged.
  This design doc + findings doc force-added there for durability.
- Execution: subagent-driven (standing default), empirically — real files
  first, conclusions second (the Icarus epic's core lesson).
