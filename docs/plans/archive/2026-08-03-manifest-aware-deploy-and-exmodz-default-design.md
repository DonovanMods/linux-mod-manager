# Manifest-Aware Deploy & Exmodz-Default Selection — Design

**Date:** 2026-08-03
**Status:** Approved design, pre-implementation
**Issues:** [#210](https://github.com/DonovanMods/linux-mod-manager/issues/210) (bug: manifest-aware deploy) and [#211](https://github.com/DonovanMods/linux-mod-manager/issues/211) (feature: exmodz default) — two story branches, #210 first.

## Background / Evidence

Investigated from a live report (Icarus, 4 mods, `lmm -g icarus deploy`):

- The merged pak itself was verified correct at the byte level (mount point,
  entry paths byte-matching author-built pak conventions, diff values applied,
  fingerprint BaseIndexHash matching the installed `data.pak`).
- The real defects are upstream of the merge:

**Defect 1 (bug):** `installer.Install` deploys `cache.ListFiles(...)` — the
union of every regular file in a cache version dir — and never consults the
completion-marker member manifests (`cache.MarkFileCompleteWithMembers` /
`cache.FileManifests`, #144). Pre-#197 exmodz downloads compiled a per-mod
`<stem>_P.pak` into the cache; the post-#197 retain-only path seeds staging
from the existing entry (`prepareStaging`, `internal/core/service.go:778-796`)
and never removes it. Result: a mixed entry like
`icarus-dLs3nvmWj5uOPnXxezGe/1.4/` holds `LargerResourceStacks_P.pak`
(claimed by **no** manifest) next to `.lmm-source-exmodz` (marker records
zero members), yet deploy still symlinks the stale pak into the game dir —
double-applying tables alongside `zzz_LMM_Merged_P.pak`. The conflict scanner
(`internal/core/conflicts.go:99`) has the same blind spot (phantom conflicts).

**Defect 2 (feature gap):** the Icarus source emits two `DownloadableFile`s
(IDs `"pak"` / `"exmodz"`) but sets `IsPrimary` only when exactly one exists
(`internal/source/icarus/icarus.go:172-174`). With both present, every
non-interactive selection path — TUI install (no file chooser exists), `--yes`,
batch/dependency install, profile apply — falls through `selectDeployFiles`
to `files[0]` = **pak**. The mergeable variant silently loses everywhere, and
the CLI chooser rows (`(PAK, 0 B)` / `(EXMODZ, 0 B)`, no default mark) give
the user no basis to choose. The CLI multi-select also permits installing
_both_ variants, recreating the double-apply by explicit choice.

## Part 1 — Manifest-aware deploy (bug; ship first)

### Resolver

New helper in `internal/core` (beside the installer):

```
deployableFiles(cache, gameID, sourceID, modID, version) ([]string, error)
```

1. Read `FileManifests` for the version dir.
2. **All markers `Recorded=true` AND the entry holds at least one retained
   source (`.lmm-source-*`)** → deployable set = union of recorded members,
   intersected with files actually on disk (`ListFiles`). Unclaimed files
   are excluded.
3. **Otherwise** (any marker `Recorded=false`, no markers, or no retained
   source) → fall back to the full `ListFiles` union (legacy behavior,
   unchanged).

**Amended during implementation (user ruling 2026-08-03, Task 3 breaker):**
the original rule narrowed on "all markers recorded" alone, which is
indistinguishable from #144's protected legacy shapes — an entry holding
content no recorded manifest attributes (unverifiable file IDs, `lmm
import`-populated entries) must keep the union
(`TestInstaller_ReplaceForUpdate_SameCacheDir_FallsBackToUnionWithoutProvenance`).
The retained source is the discriminator: it is the validate+retain model's
signature, present in every real #210 entry, and the same signal `verify`'s
`hasRetainedSource` carve-out already trusts. When attribution is complete
the narrowed set equals the union anyway, so the gate only changes behavior
for entries with unattributed content and no retained source — exactly the
#144 shapes. `PruneUnclaimed` (Part 1's prune-on-commit) carries the same
retained-source gate so a commit can never delete legacy content.

Rationale for the any-legacy-marker fallback: a bare marker means "unknown
provenance — never none" (`cache.FileManifest.Recorded` contract); dropping
its members would break legitimately deployed legacy files.

### Call-site rule: deploy-direction vs removal-direction

- **Deploy-direction sites use the resolver:** `Installer.Install`, the _new_
  side of `Replace`/`ReplaceForUpdate`, `IsDeployed`/deploy-state checks, and
  the conflict scanner.
- **Removal-direction sites keep the `ListFiles` union:** `Uninstall`,
  undeploy, the _old_ side of Replace. Cleanup must remove anything that
  might ever have been linked.

This asymmetry makes a redeploy cycle self-heal an affected game dir: the
union side unlinks the stale pak; the resolver side never re-creates it.

### Prune-on-commit (cache hygiene)

When a download commit completes and _all_ markers in the entry are
`Recorded`, delete non-reserved files claimed by no manifest (the
seeded-staging leftovers). Skip pruning entirely if any legacy bare marker is
present.

### Verify

**Amended during planning (2026-08-03):** verify needs no change. Its
`hasRetainedSource` carve-out (`cmd/lmm/verify.go:269`) already skips the
count-mismatch check for compile-mode retained-only entries — including the
mixed stale-pak shape — before `ListFiles` is consulted, and its per-file
checksum loop keys off DB rows, not directory listings. Replacing the
carve-out with the resolver would have _introduced_ a false FILE COUNT
MISMATCH for healed retained-only entries (deployable set empty vs
expected downloads > 0). The implementation plan adds no verify task.

### Acceptance criteria

- A cache entry shaped like the live report (recorded zero-member manifest +
  unclaimed stale pak) deploys **nothing** of its own; a deploy cycle removes
  previously-linked stale symlinks from the game dir.
- Legacy bare-marker entries deploy exactly as today.
- Conflict scan no longer reports unclaimed files.
- Prune-on-commit removes unclaimed files only when provenance is fully
  recorded.

## Part 2 — Exmodz default + variant exclusivity (feature)

### Source-level primary

`icarus.GetModFiles`:

- Both variants present → set `IsPrimary` on **exmodz** (single-file behavior
  unchanged: sole file is primary).
- Fill `Description`: `"mergeable EXMOD — recommended"` / `"prebuilt PAK"` so
  the CLI chooser rows are distinguishable.

Every selection path already honors `IsPrimary` (CLI default mark + `--yes`,
TUI plan, batch/dependency, profile apply, `selectDeployFiles`), so no core
plumbing is needed — CLI/TUI parity for free. Escape hatch preserved:
`--file pak` or explicit chooser selection still installs the pak.

### Mixed-variant rejection (core seam)

For a source implementing `MergeCompiler`, a file selection that mixes an
exmodz file (`isExmodzFile`) with any other file is rejected:

> pak and exmodz are alternate forms of the same mod — select one

Enforced at the core resolution seam (`resolveTargetFiles`, covering
`--file pak,exmodz` and batch paths) and surfaced in the CLI chooser as a
re-prompt rather than a hard exit. Per the #197 lesson, both entry points get
CLI-layer integration tests, not just core unit tests.

### Acceptance criteria

- With both variants, TUI/`--yes`/batch/profile-apply install exmodz; CLI
  chooser shows exmodz as `<- default` with descriptive labels.
- `--file pak` still installs the pak alone.
- `--file pak,exmodz` and chooser `1,2` are rejected with the message above.

## Testing (TDD)

- **Bug:** failing test first reproducing the live cache shape; legacy
  bare-marker union preservation; replace-cycle stale-symlink removal;
  prune-on-commit unit tests; conflict-scan test.
- **Feature:** icarus source table tests (both→exmodz primary,
  single→unchanged); CLI chooser default + rejection integration tests; TUI
  plan test asserting `plan.Files == [exmodz]`.

## Rollout

Two GitHub issues; two story branches off `develop` (`fix/…` then `feat/…`);
separate PRs `--base develop`; CHANGELOG entries under `[Unreleased]`; no
per-story version bump (bug = PATCH-class, feature = MINOR-class for the
release batch). Issues do not auto-close on develop merges — close manually.

## Decisions log

- Exmodz default, pak selectable (user-approved; "exmodz-only/hide pak" and
  "label-only" rejected).
- Two issues, bug first (user-approved; combined story rejected).
- Preference implemented source-level via `IsPrimary`, not core game-aware
  plumbing (YAGNI: icarus source is only used with compile-mode games).
- Deploy-direction/removal-direction asymmetry is deliberate (self-healing).
