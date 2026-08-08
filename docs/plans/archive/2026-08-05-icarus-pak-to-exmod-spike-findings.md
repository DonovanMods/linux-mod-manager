# Icarus PAK → EXMOD Conversion Spike — Findings

**Date:** 2026-08-05 **Spike issue:** #220 **Branch:** `spike/pak-to-exmod`
**Design doc:** [2026-08-05-icarus-pak-to-exmod-spike-design.md](2026-08-05-icarus-pak-to-exmod-spike-design.md)
**Prototype:** `spike/pakconvert/` (throwaway; never merged)

## Verdict: PARTIAL GO

The design rubric's strict GO bar — semantic equivalence on ≥3 of 4 sampled
dual-form mods with every residual explained — was **not met by blind
diff-derivation**: of 11 dual-form mods, 2 PASS outright, 1 is genuinely
explained (a pure asset-only pak), and **8 DIVERGED** (final round-3
harness numbers; an audit-then-fixed normalization blind spot had briefly
made this look like 6). The failures decompose cleanly into (a) one
**mechanical normalization gap** (case-sensitivity — found by audit, fixed
in the harness, which flipped Eye Colors Expanded! into an ordinary
processed-but-drifted mod), (b) one **conversion-robustness gap** (a pak
whose table sits at `Content/` root with no recoverable directory
structure is structurally unconvertible by this approach), and (c) three
**inherent expressiveness gaps** of diffing a stale whole-table snapshot
against a live base — and 5 of the 8 DIVERGED paks embed the author's
original `data.EXMOD`, which sidesteps (c) entirely. The pipeline
seam is proven end-to-end (`ValidateSource` + `MergeCompile` accept a
synthesized `.exmodz` with zero changes), asset pass-through is empirically
viable (0 Oodle entries, all assets `.uasset`/`.uexp`), and `MergeCompile`
output was verified row-by-row. **Recommendation: proceed with a feature
scoped in tiers** — Tier 1 (high fidelity): convert paks that embed a
`data.EXMOD` by extracting it verbatim; Tier 2 (best effort, warn):
diff-derived conversion for the remainder, with the drift hazards below
surfaced to the user. Blind diff-derivation as a silent default is NO-GO.

## Evidence Summary

Ground truth = convert the mod's pak, apply our diff and the author's own
exmodz each to the **live** base tables via the real `icarus.ApplyRowPatch`,
deep-compare results. Final (round-3 harness: case-insensitive
normalization + unverifiable-guard on the benign branch; commit a02840d):

| Mod                          | Verdict   | Residuals | Breakdown                                                  | Notes                                                                                                                                  |
| ---------------------------- | --------- | --------- | ---------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| Cry's Lvl 120 Cap - 25%      | PASS      | 0         | —                                                          | clean conversion (tables + `data/`-mounted assets)                                                                                     |
| Cry's Lvl 120 Cap - 50%      | PASS      | 0         | —                                                          | clean conversion (tables + `data/`-mounted assets)                                                                                     |
| Turret Variants              | EXPLAINED | 139       | all table-absent-from-pak (verified; `Census["other"]==0`) | genuine: pure asset-only pak; exmodz variant is broader                                                                                |
| Floofs QOL                   | DIVERGED  | 1804      | 13 diverged / 1791 stale-pak-change                        | expressiveness gaps (embedded `.EXMOD` present)                                                                                        |
| Dextermod: Tactical Backpack | DIVERGED  | 59        | 7 / 52                                                     | expressiveness gaps (embedded `.EXMOD` present)                                                                                        |
| Dyls123's Horse Cart         | DIVERGED  | 1849      | 136 / 1713                                                 | expressiveness gaps (embedded `.EXMOD` present)                                                                                        |
| Dextermod: Stronger HVAC     | DIVERGED  | 132       | 73 / 59                                                    | expressiveness gaps (embedded `.EXMOD` present)                                                                                        |
| Intreeg's 4XP                | DIVERGED  | 27        | 26 / 1                                                     | expressiveness gaps (embedded `.EXMOD` present)                                                                                        |
| Eye Colors Expanded!         | DIVERGED  | 25        | 2 / 23                                                     | capital-`Data/` mount (audit-caught, harness-fixed); now genuinely processed (50 stale rows) — ordinary stale-drift divergence         |
| Intreeg's More Resources     | DIVERGED  | 21        | 21 / 0                                                     | **structurally unconvertible**: table at `Content/` root, no recoverable directory structure; unverifiable-guard refuses a benign call |
| Extraction 5 Seconds         | DIVERGED  | 7         | 7 / 0                                                      | expressiveness gaps (no embedded `.EXMOD`)                                                                                             |

Totals across 4,063 residuals: 3,639 `stale-pak-change` (machine-verified:
our value provably traces to the pak's stale snapshot while the author's
matches live), 139 `exmodz-newer-than-pak` (whole-table absence, all
Turret Variants, verified), 285 `diverged`. The classifier's branch-5
harness tripwire (pak touched a row but the converter emitted no patch)
fired **zero** times in 4,063 residuals — the row-level differ itself
dropped nothing it could see. The two normalization blind spots this
spike's own audit surfaced (capital `Data/`, bare `Content/`) were fixed
in the harness (case-insensitive `NormalizeEntry` preserving original
case, plus a `Census["other"]>0` guard that blocks the benign
classification whenever unclassified entries exist) and the corpus re-run
— the numbers above are post-fix.

**Pipeline seam (Task 7): PASS.** A synthesized "Floofs QOL" `.exmodz`
passed the real `icarus.ValidateSource`, merged via the real
`icarus.MergeCompile` alongside a genuine author exmodz ("Dextermod:
Enhanced Tactical Backpack") with **zero warnings**, and the output pak had
the correct mount point (`../../../Icarus/Content/`), `data/`-prefixed
tables, and every converted row/field verified present. Zero changes to
`internal/` were needed anywhere in the spike.

**Embedded-`.EXMOD` oracle:** all 5 paks carrying a `data.EXMOD` showed
`EmbeddedMatch: mismatch` against our derived diff — expected by design
(the embedded diff was authored against the build-time base; ours diffs
against today's live base). The embedded file is the author's original
field-scoped intent and is the highest-fidelity conversion source
available (it is exactly what `ParseExmod` consumes).

## Census

- **Catalog:** 539 mods total (live Firestore catalog at fetch time).
  Sampled census (fetcher stops at quota; full sweep not run): of 115
  scanned mods — 25 pak-only, 78 exmodz-only, 12 dual-form, 0 neither.
  (The archived 2026-08 research counted 41 dual-form across the full
  ~538; this sample is consistent with that order of magnitude.)
- **Corpus:** 14 mods fetched (~38 MB): named targets + dual-form quota.
  11 had a usable pak (3 entries had `Pak: ""` — one Larkwell pak URL
  404'd; `""` means unavailable, conflating catalog-absent with
  download-failed).
- **Convertibility:** all 11 corpus paks opened and read cleanly with
  `internal/unrealpak` — v11, unencrypted, **zero Oodle/unreadable
  entries**. Extension census: JSON tables in 9 paks, `.uasset`/`.uexp`
  assets in 3, embedded `.exmod` in 5. No unsafe entry paths, no
  hyphenated table paths, no duplicate row names anywhere in the corpus.

## Answers to the Six Spike Questions

### 1. Path normalization

_Can mount-point + entry-path pairs be normalized reliably?_ **Yes for
every structured layout observed — with case-insensitivity required and
one structurally unresolvable layout.** The floating `data/` boundary
(mount `.../Content/` + entry `data/X/Y.json` vs mount
`.../Content/data/X/` + entry `Y.json`) is handled correctly by joining
the pair before classifying. The corpus produced two variants the
archived research never saw: a **capital `Data/`** segment (Eye Colors
Expanded!) — initially misclassified, then fixed with case-insensitive
matching (original case preserved) and re-proven on the corpus — and a
table mounted **directly at `Content/` root with no `data/` segment at
all** (Intreeg's More Resources), which carries no recoverable directory
structure and is **structurally unconvertible** by path normalization
alone (a basename-fallback against base tables is a candidate future
mitigation; not attempted in this spike). Either way, "unclassifiable
JSON" must be a loud signal, never a silent skip — the spike harness now
enforces this via a `Census["other"]>0` guard.

### 2. Differ fidelity

_Does a Name-keyed row differ against the live base reproduce author
intent?_ **Only when the pak is fresh relative to the base.** The differ
mechanism itself is sound (branch-5 tripwire: 0 hits in 4,063 residuals;
the 2 PASS mods, whose paks target base tables that barely drift, convert
byte-equivalently). But because prebuilt tables are **stale whole-table
snapshots**, per-field diffing against today's base sweeps every
independently-drifted field into the diff alongside the author's real
change (dominant pattern: 3,639 residuals). Author intent per-row is
recoverable only from the embedded `.EXMOD` (when present) — not from the
snapshot alone.

### 3. Convertibility census

_What fraction of paks are convertible?_ **In this corpus: 11/11 readable,
9/11 table-bearing, 3/11 asset-bearing, 5/11 embed the author's
`data.EXMOD`.** No Oodle, no encryption, no format obstacles at all — the
reading layer is a non-issue. Convertibility is limited by semantics
(staleness/expressiveness), not by format.

### 4. Asset probe

_Does the inferred asset layout hold; do assets fit the exmodz filter?_
**Layout inference held for 9/11 paks and was violated by 2** (Cry's Lvl
120 Cap 25%/50%: the mount itself carries `data/Character/`, so their
`.uasset`/`.uexp` land under a `data/` prefix — assets do NOT always mount
"unprefixed under Content/"). All probed assets are `.uasset`/`.uexp`
(fit `ParseExmodz`'s filter), all stored/Zlib. Pass-through works when the
asset's **Content-relative joined path** is preserved as the exmodz asset
path — the Cry's pair, converted exactly that way, are the spike's two
PASS mods. Layout variance is handled by the same pair-normalization as
tables; it is a naming expectation broken, not a blocker.

### 5. Expressiveness gaps

_What can't EXMOD express, and how often does it occur?_ Corpus
frequencies: `field-removed` **795** (fields present in the live base row
but absent from the pak's stale row — schema drift, correctly ignored,
inexpressible either way), `defaults-changed` **8** (Defaults edits are
inexpressible in EXMOD), `rowstruct-changed` / `top-level-changed` /
`duplicate-row-name` / `hyphen-path` / `table-not-in-base` /
`unreadable-entry` / `unsafe-asset-path`: **0**. Two further
representation gaps surfaced that EXMOD-the-format can hold but
diff-derivation gets wrong: tool-auto-populated fields on new rows (e.g.
`EventDescription: NSLOCTEXT(...)` captured by whole-row new-row emission
but absent from the author's own exmod) and localized-text representation
(`INVTEXT("[DNT] ...")` raw structure vs the author tooling's resolved
string). Row deletions never appeared (and are inexpressible regardless).

### 6. Seam validation

_Does a synthesized `.exmodz` flow through the real pipeline unchanged?_
**Yes — proven.** Real `ValidateSource` + real `MergeCompile` + verified
merged output, zero pipeline modifications, zero warnings (§ Evidence
Summary). The ingest-time "synthesize a retained `.exmodz`" seam works
exactly as the design predicted.

## Expressiveness-Gap Frequencies

| Gap                                                       | Count                       | Production consequence                                                                                                                                                                                                                         |
| --------------------------------------------------------- | --------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Stale-drift sweep-in (`stale-pak-change` residuals)       | 3,639                       | The dominant hazard: a derived diff silently reverts base-game updates in every field that drifted since the pak was built. Must be mitigated (embedded-EXMOD preference, drift heuristics, or explicit user warning), never silently shipped. |
| Structurally unconvertible layout (bare-`Content/` mount) | 1 mod (21 residuals)        | A pak with no recoverable directory structure cannot be converted by normalization alone — must be detected and refused loudly (basename-fallback is a candidate future mitigation).                                                           |
| `field-removed` (schema drift, base-only fields)          | 795                         | Correctly ignorable (staleness), but confirms snapshots routinely predate schema changes.                                                                                                                                                      |
| Whole-table absence from pak vs exmodz (verified)         | 139                         | Pak and exmodz variants of the same mod can differ in **feature scope** — conversion fidelity vs the exmodz is not always achievable from the pak alone.                                                                                       |
| Tool-auto-populated new-row fields                        | ~26 rows (Intreeg's 4XP)    | Harmless-looking extra fields ride along on new rows; cosmetic in the cases inspected but unverified in general.                                                                                                                               |
| Localized-text representation (INVTEXT/NSLOCTEXT)         | spot-confirmed (Floofs QOL) | JSON-level conversion cannot reproduce resolved localized strings; affects display-name fields.                                                                                                                                                |
| `defaults-changed`                                        | 8                           | Rare but real; inexpressible in EXMOD — must be a loud per-mod warning when detected.                                                                                                                                                          |

## Constraints a Feature Design Must Honor

1. **Diff against the live base only; base-absent rows are staleness, not
   deletions** — confirmed empirically (795 field-removed, 3,494 stale
   rows) and EXMOD has no delete verb anyway.
2. **Derived diffs go stale too.** A diff synthesized at ingest freezes
   live-base values from that day; the next weekly base update recreates
   the exact Friday problem one level up. Either retain the **raw pak**
   and re-derive at merge/recompile time (the merged-pak pipeline already
   recompiles on base-staleness, #196), or re-derive the retained exmodz
   on the same trigger. The embedded-`data.EXMOD` path does not have this
   problem (field-scoped author intent, exactly like a published exmodz).
3. **Prefer the embedded `data.EXMOD` whenever the pak carries one**
   (5/11 corpus paks, including 5 of the 8 DIVERGED) — it converts the
   hardest cases exactly and needs zero inference. Verify embedded ≡
   published-exmodz on dual-form mods before trusting it blindly (not
   done in this spike).
4. **Mount + entry normalized as a pair, case-insensitively** (original
   case preserved) — the corpus contains capital-`Data/` mounts,
   bare-`Content/` mounts, and `data/`-prefixed asset mounts (Cry's).
   Case-insensitivity is proven necessary and sufficient for the
   capital-`Data/` case (harness-fixed and corpus-re-proven);
   bare-`Content/` paks are structurally unconvertible without a
   basename-fallback (future mitigation) and must be refused loudly.
   "Unclassifiable JSON entry" must be a loud finding; `ClassOther` as a
   silent third outcome briefly mislabeled 2 mods as EXPLAINED before the
   spike's own audit caught and fixed it.
5. **Four unexported upstream values must be exported** (or a conversion
   API added to `internal/source/icarus`) before production work:
   `endOfModSentinel`, `icarusContentMountPoint`, `icarusDataTablePrefix`,
   and the `-`↔`/` CurrentFile flatten rule. The spike duplicated all
   four as literals; production code must not.
6. **Fingerprint membership:** converted paks must contribute to
   `MergedFingerprint` (e.g. MD5 of the retained artifact — and if
   re-derivation lands, the raw pak's bytes) or pak-mod
   enable/disable/reorder/update leaves the merged pak invisibly stale.
7. **Alternate-form validation & #211:** `ValidateInstallFileSelection`'s
   "pak and exmodz are alternate forms — select one" and exmodz
   `IsPrimary` defaults need rethinking once a pak is itself mergeable;
   Turret Variants shows the two forms can differ in feature scope, so
   "alternate forms" is not always true.
8. **Double-apply prevention:** when a converted pak's diff enters the
   merge, the original `.pak` must simultaneously stop deploying — the
   manifest-aware `deployableFiles` resolver + `PruneUnclaimed` (#210) is
   the existing mechanism; the retained-source marker keeps the rest of
   the pipeline (membership, purge, deploy) working unchanged, as Task 7
   proved.
9. **Warning surface:** `defaults-changed` detection, drift-suspect
   conversions (large `changed` sets on old paks), and scope differences
   vs a known exmodz variant should surface through the existing
   `MergeCompile` → CLI/TUI warnings plumbing.
10. **Report/diagnostic hygiene** (spike lessons): keep finding `Table`
    fields consistently normalized (the spike mixed normalized and raw
    entry paths); don't overload availability semantics the way
    `CorpusMod.Pak == ""` conflated catalog-absent with download-failed.

## Open Product Questions

- Is conversion **opt-in per mod, default-on for pak-only mods, or a
  game-level setting**? What does the TUI/CLI selection look like now that
  "pak" can mean "mergeable via conversion"?
- What happens to **already-installed pak mods** on upgrade — auto-migrate
  into the merged pak (with the deploy-convergence machinery removing the
  old per-mod link), or only on reinstall?
- Tier-2 UX: is a drift-suspect conversion **blocked, warned, or shipped
  silently**? Where is the threshold (e.g. changed-fields count vs pak
  age/week delta)?
- Should `lmm` verify **embedded `data.EXMOD` ≡ published exmodz** on
  dual-form mods and prefer the published one when both exist?
- Does Tier 2 justify shipping at all for **asset-only paks** (nothing to
  diff — pass-through is effectively exact), making them Tier 1?
- Where does the differ live long-term — `internal/source/icarus`, or the
  future public unrealpak module (#170)?

## Raw Data

Corpus and generated evidence live **outside the repo** (copyrighted mod
content; never committed): `~/.cache/lmm-spike-corpus/` — downloaded mod
files under `<docID>/`, `manifest.json`, `catalog-census.json`, and
per-mod `reports/<id>-groundtruth.json` + `reports/<id>-assetprobe.json`
(regenerated by the round-3 harness re-run; asset-probe deltas confined to
Eye Colors' entry flipping `other`→`table` as expected).
The spike prototype and its tests are on branch `spike/pak-to-exmod`
(`spike/pakconvert/`; round-3 harness fix a02840d); the env-gated
ground-truth test intentionally fails on DIVERGED verdicts by design —
that failure is the recorded evidence, not a defect. SDD execution ledger:
`.superpowers/sdd/2026-08-05-icarus-pak-to-exmod-spike-plan/progress.md`.
