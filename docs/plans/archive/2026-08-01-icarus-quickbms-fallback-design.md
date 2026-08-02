# Icarus: QuickBMS Auto-Extraction Fallback — Design

**Issue:** [#174](https://github.com/DonovanMods/linux-mod-manager/issues/174) · **Epic:** #136 (branch `epic/icarus-136`; PR #171 merged there) · **Status:** design approved by user 2026-08-01; implementation gated on the Task-1 spike.

## Problem

The compile pipeline's base-table chain (`data_dump_path` local dir → hosted community dump → loud failure) is at the mercy of the hosted dump repo's freshness (currently Week 236 vs the installed Week 243). Users with QuickBMS available should never hit that wall: the installed `data.pak` itself is always week-correct base truth — it just needs Oodle-capable extraction, which QuickBMS (the dump-repo maintainer's own tool) provides.

## Decisions (user, 2026-08-01)

1. **Auto-run + announce** when needed; `auto_extract: false` opts out. No interactive prompt.
2. **`.bms` script ships embedded** (go:embed), pending the spike's license check; if embedding is legally murky, fall back to download-on-demand from the canonical source with caching.
3. This is lmm's **first sanctioned external-binary invocation** — optional, runtime-detected, announced; never a hard dependency.
4. Lives on the **epic branch** (`epic/icarus-136`) as its own story PR; the epic merges to develop as one PR when complete.
5. QuickBMS is **not yet installed** on the reference machine — the spike performs a user-local (non-root) build first and records the recommended permanent install route.

## Fallback chain

Base-table acquisition (inside the icarus source's provider, before `Compile` writes anything):

1. `data_dump_path` local dir — explicit user override, highest priority.
2. **Cached QuickBMS extraction for this exact build** (new) — `<dataDir>/icarus/extracted/<build>/`, populated by a previous auto-run; cheap disk check.
3. Hosted community dump (existing).
4. **QuickBMS auto-run** (new) — binary via `exec.LookPath("quickbms")` or `quickbms_path` config; announce the exact command and reason; extract the installed `data.pak` into the per-build cache dir; normalize layout; proceed.
5. All failed → one error enumerating every source tried, why each failed, and the remedies (set `data_dump_path`, install QuickBMS, wait for the dump repo).

**Every source passes the same `validateDump` byte-compare gate** (40 stored tables vs the installed pak). QuickBMS output is week-correct by construction (it reads the installed pak); the gate proves the extraction wasn't mangled. Ordering rationale: hosted dump first keeps the zero-dependency path primary per the user's "if/when the files are out of date" framing; the per-build cache (step 2) makes extraction a once-per-game-update cost.

## Component

`internal/source/icarus/quickbms.go` — one file, four responsibilities:

- **Detection:** `quickbms_path` config override, else `exec.LookPath`. Absence is a normal chain-miss, not an error.
- **Invocation:** embedded UE4 `.bms` script written to a temp file; QuickBMS executed with `exec.CommandContext` (timeout), stdout/stderr captured; non-zero exit → wrapped, actionable error carrying the tail of the tool's output.
- **Normalization:** map QuickBMS's output layout (pinned by the spike) into the dump-tree shape `loadLocalDump` already consumes.
- **Cache:** per-build directory under the data dir (`SetDataDir`'s directory — previously reserved, now used); stale builds' caches are ignored (validation would reject them anyway) and may be pruned opportunistically.

`Compile`'s exported signature is unchanged; the chain slots into the existing provider logic.

## Config (games.yaml, beside `data_dump_path`)

- `auto_extract: true` (default) — set `false` to disable the auto-run.
- `quickbms_path: /path/to/quickbms` (optional) — for non-PATH installs (including the spike's user-local build).

No CLI/TUI surface: pipeline-internal, announced through existing progress/logging. (CLI/TUI parity holds trivially — shared core path.)

## Error handling

Fail-loud throughout (repo precedent #95): extraction errors, validation failures, and missing tools each produce specific, remedial messages; the chain-exhausted error names all attempts. Partial extraction output is removed on failure (same hygiene as `Compile`'s partial-pak cleanup). Never silently fall back _across weeks_ — a stale-but-validating source is impossible by construction of the gate.

## Testing

- **Hermetic CI:** a stub `quickbms` executable on `$PATH` (test-written script emitting a known tree) covers detection, invocation, normalization, validation pass/fail, cache reuse, `auto_extract: false`, missing-binary, non-zero-exit, and timeout legs. No network, no real QuickBMS, no real game files.
- **Real validation:** spike + post-plan manual steps on the reference machine (extract real `data.pak`, byte-compare, then an end-to-end `Compile` of the real `Bear_Mount.EXMODZ` — which this feature finally unblocks).

## Task-1 spike (the gate)

On the reference machine: user-local QuickBMS build (no root); obtain the ecosystem UE4 `.bms` script; extract the real `data.pak`; verify Oodle tables decompress (byte-compare the 40 stored tables against `unrealpak` reads, spot-check JSON validity of previously unreachable tables like `D_ItemsStatic.json`); pin the exact invocation, output layout, and runtime; check the script's license for embedding. **If Linux QuickBMS cannot decompress Icarus's Oodle tables, stop — the design's premise is falsified and the feature is rethought before any product code.** Also record the recommended permanent install route for the user (AUR vs upstream).

## Out of scope

Extraction for any other game; any non-QuickBMS extractor; dump self-hosting; prompting UIs; making QuickBMS a required dependency.
