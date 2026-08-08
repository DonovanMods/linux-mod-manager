# go-unrealpak Module + Icarus Mod-Authoring Skill — Design

**Date:** 2026-08-07
**Issues:** #170 (module extraction), plus one new issue for the lmm asset-path defect found while designing this
**Repos touched:** `linux-mod-manager` (module extraction, dependency swap, bug fix), new `go-unrealpak`, `Icarus-Mods` (skill + rebuilt artifacts)

---

## STATUS 2026-08-07 — delivered, with the skill deliberately descoped

**Read this before implementing anything below.** Two of the three layers shipped
as designed; the third was cut on evidence. Do not build §3 as written.

| Layer                                 | Outcome                                                                                                           |
| ------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| §2 `go-unrealpak` module + CLI (#170) | **Shipped.** v0.1.0 public and tagged; lmm swapped via PR #239, merged to `develop` as `9c7d108`; #170 closed.    |
| §4.2 lmm asset-path defect            | **Shipped early**, out of order, as hotfix v1.29.1 (#237, PR #238, `f495794`). In-game validated.                 |
| §3 the authoring skill                | **Descoped to the analysis procedure only** (§3.2). §3.3, §3.4, §3.5 and §3.6 were _not_ built and should not be. |

**Why the skill was cut.** Running §3.2's analysis against the real mods
answered the question the whole design existed to serve — and the answer was
that nothing needed rebuilding. All three mods resolve clean against build
`3.0.22.155681` while still declaring `week: "171"`.

The premise in §1 and the Goal above is wrong in one important way: **an `.EXMOD`
is a diff, rebased onto the live `data.pak` at every compile, so table patches do
not go stale.** The Friday problem afflicts prebuilt `.pak` files, not `.exmodz`.
`week` is metadata, and `modinfo.json` declares `"compatibility": "all"` anyway.
So §3.3 (rebuild `.EXMODZ`) and §3.4 (rebuild `.pak`) address a problem the
author does not have — the second only mattering at all if pak variants were ever
published, which `modinfo.json` does not do.

**What genuinely can rot, and what shipped to catch it:** bundled `.uasset`/
`.uexp` files are whole-file overrides frozen at build time and do **not** rebase
— if the game moves or renames the asset they override, the mod keeps installing
and deploying while doing nothing. Plus a patched row renamed upstream, or the
base value catching up (a silent no-op). All three are checked by
`Icarus-Mods/tools/check-mods.py`, with the domain rules recorded in
`Icarus-Mods/CLAUDE.md` (commit `b78efb7`). It exits non-zero only on a genuine
break, and was verified against deliberately broken fixtures so it can actually
fail rather than only ever printing `ok`.

**Not a skill.** `superpowers:writing-skills` is explicit that project-specific
conventions belong in the instructions file, not `.claude/skills/`. One repo, one
script — `CLAUDE.md` is the right home.

**Still true and worth keeping** from below: §3.1's rules table (all empirical,
and the wrapper rule is now lmm's shipped behavior), §3.2's analysis design, and
§2's entire CLI rationale.

---

## Goal

Let a future session maintain the author's own Icarus mods in the `Icarus-Mods`
repo: analyze each mod against the game's _current_ data, then re-create its
`.EXMODZ` and `.pak` artifacts from source.

Icarus ships a data update most weeks. A mod's built artifacts are a snapshot
against whichever `data.pak` existed when they were built — the "Friday
problem" from the author's side rather than the mod manager's. Nothing today
tells the author whether a mod still does what its README claims, and rebuilding
by hand means re-deriving a binary format that was falsified twice during #136
planning.

## Non-goals

- Authoring or recompiling UE assets (`.uasset`/`.uexp`). They are carried
  through verbatim; producing them is an Unreal Editor task.
- Replacing IMM (Icarus Mod Tools/Manager) or its `.EXMODZ` conventions. This
  matches the existing ecosystem format, it does not redefine it.
- Oodle decompression (see §2.3).
- Any change to lmm's merged-pak consumer pipeline beyond the §4.2 defect fix.

## Architecture

Three layers, each with one job.

```text
Icarus-Mods/.claude/skills/icarus-mod-build/   Icarus semantics: EXMOD schema,
  ├── SKILL.md                                 path rules, drift analysis,
  ├── scripts/                                 rebuild + verify procedures
  └── reference/
                    │ drives (binary work only)
                    ▼
github.com/DonovanMods/go-unrealpak            game-agnostic UE v11 pak
  ├── unrealpak/    (extracted package)         read/write + CLI
  └── cmd/unrealpak (CLI)
                    ▲
                    │ consumed as a dependency
linux-mod-manager   internal/unrealpak → module dep
```

The split is forced by #170's own constraint: the package is deliberately
game-agnostic with zero Icarus imports. Every Icarus-specific rule the author
workflow needs — the `.EXMOD` schema, `CurrentFile` flattening, the `data/`
table prefix, the `Icarus/Content/` mount, the asset-wrapper strip, patching
against `data.pak` — therefore cannot live in the module. It lives in the skill.

## 1. Source of truth in `Icarus-Mods`

Unchanged from today; the design only adds a second built artifact.

```text
<Mod>/<Mod>.EXMOD        # source: the hand-maintained diff manifest
<Mod>/**/*.uasset|.uexp  # source: prebuilt assets, under a <Mod>/ wrapper dir
<Mod>.EXMODZ             # built artifact
<Mod>_P.pak              # built artifact (new)
modinfo.json             # catalog entry; gains files.pak, week bumped
```

## 2. Layer 1 — `go-unrealpak` (#170)

### 2.1 Package

`internal/unrealpak` extracted as-is, history riding along via `git subtree
split`. Public API is what lmm already uses: `Open`, `Create`, `Reader`,
`Writer`, `WithMountPoint`. The byte-level format spec in
`docs/plans/archive/icarus-pak-format-findings.md` seeds the module's format
documentation — that document is the empirical record and should not be
paraphrased from memory.

### 2.2 CLI surface

```text
unrealpak info    <pak>                     version, footer size, mount point, entry
                                            count, compression methods, hash gates
unrealpak list    <pak> [--json]            mount-relative paths, size, compression
unrealpak cat     <pak> <path>              one entry's bytes to stdout
unrealpak extract <pak> <dir> [--filter G]  entries to dir at their mount-relative
                                            paths, plus a sidecar recording the mount
unrealpak build   <dir> <pak> --mount=M     pack dir; --mount defaults from sidecar
```

`extract` writes a sidecar (`.unrealpak.json`) holding the source mount point so
`build` can round-trip a pak without the caller having to remember it. `--mount`
given explicitly always wins.

### 2.3 Deliberate limits

- **Writes are stored (uncompressed) only.** Every mod pak in the #220 corpus is
  stored-uncompressed, and lmm's shipped writer already emits only this shape.
- **Reads handle stored and Zlib.** That covers all of `data.pak` (40 stored,
  258 Zlib — #175).
- **Oodle is out of scope, and says so.** Index reads never need it, so `info`,
  `list`, and `extract --filter` over non-Oodle entries all work against
  `pakchunk0*`. Reading an Oodle _payload_ fails with a named error identifying
  the entry. This answers #170's open "does Oodle belong in the module"
  checkbox: it is a caller concern. The author workflow never needs it, because
  mods carry prebuilt assets rather than extracting base ones.
- **No silent fallbacks.** Zero or ambiguous matches, unsupported shapes, and
  failed hash gates are loud errors with a non-zero exit (repo precedent #95).

### 2.4 Testing

- Port the existing `internal/unrealpak` tests unchanged; they are the
  regression gate for extraction.
- CLI: `extract` → `build` round-trip on a fixture pak, asserting the rebuilt
  index structures and entry set match; `list --json` golden output.
- Real-file tests gated behind an env var naming the Icarus install, following
  the repo's existing convention for install-dependent tests.
- lmm's own suite passing after the dependency swap is the integration gate.

## 3. Layer 2 — the skill (DESCOPED — only §3.2 shipped; see STATUS above)

Lives at `Icarus-Mods/.claude/skills/icarus-mod-build/`, so it is discovered
whenever a session works in the mods repo and is versioned with the mods it
builds.

### 3.1 The rules it encodes

Each is empirical. None is inferred from convention.

| Rule                                                                                                             | Evidence                                                                                                                                                                                                                                                                                 |
| ---------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Mount point is `../../../Icarus/Content/`; output is named `<Name>_P.pak`                                        | two real prebuilt mods (#178, pak-divergence-report)                                                                                                                                                                                                                                     |
| Patched tables land at `data/` + the table's mount-relative path                                                 | byte-matched against FloofLevelCap and Intreeg's 4XP                                                                                                                                                                                                                                     |
| `CurrentFile` ⇄ mount path by replacing `-` ⇄ `/`                                                                | no base table path contains a hyphen (verified across all 298)                                                                                                                                                                                                                           |
| Bundled assets land at their zip path **minus the leading `<Mod>/` wrapper**, which equals the manifest basename | dual-form ground truth: `Crys_Lvl120Cap_M25/data/Character/C_PlayerTalentGrowth.uasset` in the EXMODZ ⇒ `Icarus/Content/data/Character/C_PlayerTalentGrowth.uasset` in the shipped pak; holds for all 6 asset-bearing bundles available; now also lmm's shipped behavior (#237, v1.29.1) |
| `{"CurrentFile":"EndOfMod"}` is a terminator row: no `File_Items`, no table                                      | author manifests + #220 corpus                                                                                                                                                                                                                                                           |
| Rows upsert: matching `Name` shallow-merges fields, otherwise appends                                            | `ApplyRowPatch`; matches real mod behavior                                                                                                                                                                                                                                               |
| `.EXMOD` carries pass-through fields lmm's parser drops — `week`, `fileName`, `readmeURL`, `imageURL`, `Level2`  | the author's own three manifests                                                                                                                                                                                                                                                         |
| Base tables come from the installed `Icarus/Content/Data/data.pak`                                               | #175; offline and week-correct by construction                                                                                                                                                                                                                                           |
| UE virtual paths are case-insensitive; match case-insensitively, preserve original case                          | #220 constraint 4 (capital `Data/` mounts are real)                                                                                                                                                                                                                                      |

### 3.2 Procedure: analyze

Against the installed `data.pak`, per mod, report:

- **Install identity** — `Major.Minor.Patch.Changelist` from
  `Icarus/Config/version.json`, and the data changelist.
- **Per overridden field**, one of:
  - `applies` — base differs from your value; the patch does something
  - `no-op` — base already equals your value; the game caught up, drop it
  - `missing-row` — the row name is gone from the base table
  - `missing-table` — `CurrentFile` no longer resolves
  - `field-absent-in-base` — a field the base row does not have: intentional
    addition, or a typo that silently does nothing
- **Assets** — each bundled asset's stripped path checked against the real base
  asset paths (index reads across the pakchunks; no Oodle needed).

Asset checking can go further than path matching, because the base assets an
Icarus mod overrides turn out to be **readable, not Oodle** — `pakchunk0_s20`'s
`Data/Character/C_*Growth.uexp` decode with the stdlib. So the analysis can
compare a mod's curve against the stock one it replaces, not merely assert the
path exists. Worked example from #237's validation: `.uexp` curve keypairs sit at
byte offsets 165/169 and 192/196 as `(level, cumulative total)`, giving stock
1.5 talent / 0.5 solo / 3.5 blueprint per level against MegaPoints' 6 / 2 / 10.
Reading the _reference_ rather than the curve is what makes this discoverable:
`Character/D_CharacterGrowth.json`'s `Player` row names the three curve assets.
Treat the offsets as an example, not a spec — they were not verified beyond
these three assets, and a general curve reader is out of scope.

A mod may have no overridden fields at all: MegaPoints and MorePoints are
asset-only, with `Rows` consisting solely of `EndOfMod`. That is a valid shape,
not an empty report — for those mods the asset check _is_ the analysis, and the
report must say so rather than rendering an empty field table.

`no-op` and `field-absent-in-base` are the two the author cannot currently see
and the reason existence checks alone were rejected.

### 3.3 Procedure: rebuild `.EXMODZ`

Set `week` to the week the rebuild targets — it records which base the artifact
was built against, so it changes on every rebuild. `version` is the author's own
semantic version and changes only when the mod's _content_ changes, never for a
rebuild alone. Preserve every pass-through field, then zip
`Extracted Mods/<Name>.EXMOD` plus the `<Name>/` asset tree verbatim.
Entry order sorted and timestamps fixed, so an unchanged mod rebuilds to
identical bytes and `git status` stays honest.

### 3.4 Procedure: rebuild `.pak`

1. Stage a directory.
2. For each row: `unrealpak cat data.pak <resolved path>` → apply the row's
   upserts → write to `stage/data/<mount-relative path>`.
3. For each asset: copy `<Name>/<rest>` → `stage/<rest>` (the wrapper strip).
4. `unrealpak build stage <Name>_P.pak --mount=../../../Icarus/Content/`.

### 3.5 Procedure: verify

`unrealpak list` the output and assert: mount point exact, table entries under
`data/`, asset entries matching real base asset paths case-insensitively, and a
spot-read of one patched table showing the intended value. In-game confirmation
remains the final gate — every Icarus finding in #136 was settled that way, and
Icarus's shipping build writes no logs.

### 3.6 Bundled helper

The JSON and zip work — applying upserts to tables up to 7 MB, preserving
pass-through fields, deterministic zipping — is a bundled Python script with
unit tests, not something re-derived per run. Python because it needs no
toolchain beyond what the machine has, and because the JSON layer has no
overlap with the module's binary concerns.

**Accepted duplication:** upsert semantics then exist both here and in lmm's
`ApplyRowPatch`. It is roughly 30 lines of fully specified JSON logic, and the
two sides genuinely differ — lmm consumes published mods and merges N of them
against a shared evolving base, while this produces one mod's artifacts and must
preserve author-facing fields lmm discards. Unifying them would mean either
importing Go from a Python helper or making the mods repo depend on the mod
manager. Recorded as a conscious choice so a future reader does not "fix" it.

## 4. Layer 3 — `linux-mod-manager` changes

### 4.1 Dependency swap

Replace `internal/unrealpak` with the module. Mechanical; the existing suite is
the gate.

### 4.2 Asset-path defect (new issue)

`Compile` and `applyBundle` pass each `.EXMODZ` entry's zip path through
`sanitizeAssetPath` and write it into the pak unchanged. Because every real
`.EXMODZ` wraps its assets in a `<Mod>/` directory (verified against the
author's three mods, Cry's two, and All_Ammo_Turret), lmm emits
`Icarus/Content/<Mod>/data/Character/X.uasset` where the game expects
`Icarus/Content/data/Character/X.uasset`.

Consequence: **bundled assets from every asset-bearing `.EXMODZ` land at a path
the game ignores.** For a mod whose entire effect is assets — MegaPoints and
MorePoints both have `Rows` consisting only of `EndOfMod` — the mod installs,
deploys, and does nothing.

Fix: strip the leading wrapper segment before writing. Per repo directive this
starts with a failing test reproducing the wrong output path. Sequenced after
extraction so it is written against the module API, and it needs its own smoke
validation since it changes what lands in a deployed pak.

## 5. Build order

1. **#170** — extract the module, add the CLI, port the format docs, CI, semver
   tag; lmm swaps the dependency.
2. ~~**Asset-strip fix**~~ — **DONE, shipped as v1.29.1** (#237, PR #238, merged
   `f495794`, tagged, back-merged to develop `9715f05`). Pulled forward out of
   order because it was a shipped-behavior defect: bundled assets landed one
   directory too deep, so asset-bearing `.EXMODZ` mods deployed and did nothing.
   In-game validated — MegaPoints alone yields 4× stock talent points.
3. ~~**The skill** — rules, four procedures, helper script and its tests.~~
   **DESCOPED** — shipped as the analysis procedure only:
   `Icarus-Mods/tools/check-mods.py` + `Icarus-Mods/CLAUDE.md` (`b78efb7`).
   See the STATUS section at the top.
4. ~~**Validate** — rebuild all three mods against the current week...~~
   **NOT NEEDED.** The analysis showed all three mods already resolve clean
   against the current build, so there was nothing to rebuild and no
   `modinfo.json` change to make. Adding `files.pak` remains optional and
   unmotivated: nothing consumes a pak variant of these mods.

Step 1 shipped and closed #170. Steps 3 and 4 collapsed into "run the check,
confirm nothing is broken" — which is the outcome, not a shortcut.

## 6. Open items

- Module path: `github.com/DonovanMods/go-unrealpak` (#170's suggestion) — to
  confirm at extraction.
- Whether `modinfo.json` should ship a `pak` URL for all three mods or only
  those whose users need the non-lmm path.
- Whether the skill's analyze step should cache the pakchunk asset-path index
  between runs (33 index reads per invocation is fast but not free).
