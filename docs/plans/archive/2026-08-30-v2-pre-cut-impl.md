# v2 Pre-cut Items Implementation Plan (Phase 3 addendum)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the two contract gaps the Phase 3 phase-end review left open before the v2.0.0 cut: every command honours the root `--json` flag (#309), and `lmm import <archive>` gets a real Plan/Apply pair so `--dry-run` previews instead of being rejected (#314).

**Architecture:** Both items extend the Phase 3 contract in place: new goldened core/app report types under the existing json/v2 emitter and Ruling 15 framing; `ImportArchive` becomes `PlanImportArchive` (side-effect-free, listing-based) + `ApplyImportArchive` (under `beginOp` + `checkPlanFresh`), with the listing/normalisation logic shared with the ingest so plan and apply cannot diverge.

**Tech Stack:** Go 1.27, encoding/json/v2, archive/zip + `7z l` listing, existing golden/AST-coverage harnesses.

**Spec:** docs/plans/2026-08-27-v2-core-refactor-design.md (§5 Phase 3) + docs/plans/archive/2026-08-29-v2-phase3-impl.md (Rulings 1–16 and the Outcome). Owner instruction 2026-08-30: "complete the optional pre-cut items as well".

## Global Constraints

- The Phase 3 output invariant holds: plain text byte-identical to `v2` a88d815 except the deltas RULED below (each recorded in the decisions log + CHANGELOG and re-pinned once). JSON shapes may still change (the v2.0.0 window is open) — goldens recorded/re-recorded ONCE.
- Ruling 15: under `--json` exactly one document on stdout, stderr empty except `--log-level`; never read stdin; `{"error","details"}` envelope on failure. Every new report type is json-tagged with a golden and passes the AST coverage + doc-comment ratchets; enums MarshalText; nil lists `[]`; not-applicable → omitted (`omitzero`).
- Ruling 16 in force: completing profile writes go through `completeProfileWrite`/`completeDBWrite`. Sanctioned `context.Background()` non-test sites = 2. cmd/lmm imports exactly app/core/domain/source. Core never writes stdout/stderr.
- `version` stays 1.30.1; no tag; `develop`/`main` untouched; story branches off `v2`, merged `--no-ff`, issues closed at the merge. Tests sandbox HOME AND XDG_*; `.envrc` exports real API keys (`t.Setenv`). `make check` clean, `make man` committed when help text changes.

## Rulings (binding)

- **R-A1 `profile list --json`** → new `core.ProfileListing{GameID string; Profiles []ProfileSummary}` from a new query `Service.ListProfiles(ctx, gameID)` (`ProfileSummary` already exists: name/mod_count/is_default). The plain table (NAME/MODS/DEFAULT, "No profiles found.") is rebuilt from the listing byte-identically. `ProfileNames` stays.
- **R-A2 `auth status --json`** → new `app.AuthStatusReport{Sources []AuthSourceStatus; Orphaned []OrphanedToken}` with `AuthSourceStatus{ID, Name string; Authenticated bool; Via string ("stored"|"env", omitempty); EnvVar string omitempty; KeyMasked string omitempty}` and `OrphanedToken{ID, Reason string}`; lives in `internal/app` (it owns env-key resolution); plain output rebuilt from it byte-identically.
- **R-A3 `profile export --json`** → the JSON form of `domain.ExportedProfile` via a new core query `Service.ExportProfile(ctx, gameID, name) (*domain.ExportedProfile, error)` carrying exactly what the YAML export carries (hooks included); plain path still prints the YAML bytes. Fix the type's doc comment ("emitted by `lmm profile export --json`").
- **R-A4 `source validate <file> --json`** → new `app.SourceValidationReport{Path, ID, Type string; Valid bool; Errors, Warnings []string; Probe *SourceProbeResult omitzero}` (probe sub-result shape at the implementer's discretion, goldened). If today the command returns a non-zero error on an invalid file, keep that: under `--json` the error is wrapped in a typed error whose `Details() any` is the report (the `ConflictError` pattern) so the envelope carries it; a valid file emits the report document.
- **R-A5 `game show-default --json`** → new `core.DefaultGame{Set bool; ID string omitzero; Name string omitzero}` via `Service.DefaultGame(ctx)`. **Ruling 17 (recorded plain-text delta):** the plain output moves from stderr (an accident of `cmd.Println`) to stdout; bytes unchanged. Record in the decisions log + CHANGELOG; re-pin the capture once.
- **R-A6** README: the `--json` exceptions paragraph is deleted (every command honours the flag); command tables gain the five rows; CHANGELOG bullet (#309, Ruling 17). Framing tests for all five (one document, empty stderr, `-v --json`).
- **R-B1 `ImportArchivePlan`** (`internal/core/import_archive.go`): `{Archive string; Mod domain.Mod (the RESOLVED identity — source/id/name/version; a minted local UUID is minted here, once, and carried); LinkedSource string; AutoDetected bool; Files []string (game-dir-relative deployable paths, sorted); Conflicts []Conflict; MergedArtifact *MergedArtifactEffect omitzero; Hooks []string; EntryPreExists bool; archive fingerprint (size+mtime) and the installed snapshot, both json:"-"}`. Golden `import_archive_plan`.
- **R-B2 `PlanImportArchive(ctx, game, profileName, archivePath, opts)`** is side-effect-free on managed state (DB, profiles, cache, game tree) and leaves NOTHING on disk: members are LISTED, not ingested — `archive/zip` for zip; `7z l -slt` (parsed) for 7z/rar, falling back to extraction into a temp dir under the staging root that is removed before return only if listing is impossible (state which in the report). The member→deployable-path normalisation (EXMODZ wrapper strip, reserved-name rejection, `DetectModName`-style top-level folding, merge-source classification) is refactored into pure functions SHARED with `Importer.Import` so plan and ingest cannot diverge. Metadata enrichment (`GetMod`/`resolveImportedFile`) is a read and runs in the plan so the plan's identity/version is final. Conflicts come from `db.CheckFileConflicts(ctx, game, profile, files)` on the plan's path list (an `Installer.GetConflicts` twin taking paths).
- **R-B3 `ApplyImportArchive(ctx, game, profileName, plan, opts)`**: `beginOp` → `checkPlanFresh` (installed snapshot) AND archive fingerprint unchanged (else `ErrStalePlan`) → ingest into the cache under the plan's identity (one identity ever — the accept re-run of Ruling 7 no longer mints a second UUID) → conflict gate: recompute conflicts from the ingested files; if they differ from `plan.Conflicts` → `ErrStalePlan`; if non-empty and `!opts.AcceptConflicts && !opts.Force` → discard the entry this call created and return `*ConflictError` (as today) → hooks → deploy → persist (Ruling 16 helpers) → merged-pak sync. `ImportArchive` stays exported as the documented convenience `Plan` then `Apply` (Ruling 10 note in its doc comment).
- **R-B4 cmd `import <archive>`**: `--dry-run` prints the plan (plain: the existing import readout vocabulary for a preview — "Would import:" header per the Phase 2 dry-run convention; `--json`: the plan document); the Phase 3 close-wave rejection is removed (help, man, README table, CHANGELOG). Non-dry-run: plan → if `plan.Conflicts` non-empty and no `--force`, prompt from the PLAN (plain) / envelope with `details.conflicts` (`--json`, unchanged behaviour), then Apply with `AcceptConflicts` — the Ruling 7 accept re-run and its duplicated readout block disappear. **Ruling 18 (recorded plain-text delta):** the import readout prints once and the ID printed is the ID persisted; re-pin the capture once and record the old/new bytes.
- **R-B5 tests:** plan is side-effect-free (full snapshot of DB/profile/cache/tree/staging root before and after `PlanImportArchive` — identical); property equality for EVERY fixture archive in the repo's testdata: `plan.Files` == the cache's `ListFiles` after `ApplyImportArchive`, and `plan.Conflicts` == the gate's set (7z/rar cases `t.Skip` when `7z` is absent); stale plan on archive change and on profile change; conflict refusal leaves no cache entry; `--dry-run` cmd captures (plain + JSON) for a plain archive, a conflicting one, and a compile-game merge source (merged-artifact effect).

## Pre-flight

| Pair | Shared surface | Finding |
|---|---|---|
| Task A ↔ Task B | README command tables, CHANGELOG `[Unreleased]`, decisions log | Both append; conflict-prone but trivial — Task A merges first, Task B merges `v2` into its branch (worker resolves) before its whole-branch review |
| Task B ↔ Ruling 7/Unit P | `importReadoutRerunLines` capture + Ruling 7 row | Superseded by Ruling 18 — Task B updates the row, does not delete history |
| Task A ↔ Unit Q M5 | `domain.ExportedProfile` doc ("no command emits it today") | Task A now makes `profile export --json` the emitter — doc updated (R-A3) |

---

### Task A: every command honours `--json` (#309) — R-A1…R-A6

**Files:** `internal/core/queries.go` (+`ProfileListing`, `ListProfiles`, `DefaultGame`), `internal/core/profile.go` or a new `export.go` (`ExportProfile` → `*domain.ExportedProfile`), `internal/app/auth_status.go` (new), `internal/app/source_validate.go` (new), `cmd/lmm/profile.go` (`doProfileList`, `doProfileExport`), `cmd/lmm/auth.go` (`doAuthStatus`), `cmd/lmm/source.go` (validate), `cmd/lmm/game.go` (`runGameShowDefault` → `doGameShowDefault(ctx, …)` + stdout), goldens under `internal/{core,app,domain}/testdata/json` + `cmd/lmm/testdata/json_golden`, README, CHANGELOG, decisions log (Ruling 17), `docs/man` if help text changes.

- [ ] RED per command: the framing test (`runJSONCommand` → one document decoding into the declared type with unknown members rejected; empty stderr) and the plain capture byte-identical (except R-A5's stream move). GREEN. One commit per command + one for docs.

### Task B: `PlanImportArchive` / `ApplyImportArchive` (#314) — R-B1…R-B5

**Files:** `internal/core/import_archive.go` (Plan type, `PlanImportArchive`, `ApplyImportArchive`, `ImportArchive` = Plan+Apply), `internal/core/importer.go` + new `internal/core/archive_listing.go` (listing + shared pure normalisation), `internal/core/installer.go` (`conflictsForPaths`), `internal/core/testdata/json/import_archive_plan.golden`, `cmd/lmm/import.go` (`doImportArchive` plan-first; `--dry-run` preview; rejection removed), `cmd/lmm/testdata/{json_golden,import_dry_run_golden}`, README/CHANGELOG/decisions log (Ruling 18; Ruling 7 row superseded; #314 closes the interim), `docs/man`.

- [ ] RED: `TestPlanImportArchive_IsSideEffectFree`, `TestPlanImportArchive_FilesMatchIngest` (every fixture archive), `TestApplyImportArchive_StaleArchive`/`_StaleProfile`, `TestDoImportArchive_DryRun_*` (plain + JSON + no mutation). GREEN. Commit per step (listing helpers → plan → apply → cmd → docs).

---

## Outcome (2026-08-30)

Both pre-cut items landed on `v2` ahead of the v2.0.0 cut (owner instruction 2026-08-30):

| Task | Issue | Merge | Delivered |
|---|---|---|---|
| A | #309 | 5e58e08 | every command honours the root `--json` flag: `profile list` → `core.ProfileListing`, `profile export` → the `domain.ExportedProfile` JSON document, `auth status` → `app.AuthStatusReport`, `source validate` → `app.SourceValidationReport` (envelope `details` on failure), `game show-default` → `core.DefaultGame`; **Ruling 17**: show-default's plain output moves stderr→stdout (bytes unchanged; config-only fallback preserved); README's exceptions list deleted |
| B | #314 | 9589ba2 | `ImportArchive` split into a side-effect-free, listing-based `PlanImportArchive` (shared normalisation with the ingest; `Warnings` carried on the plan) and `ApplyImportArchive` under `beginOp`+`checkPlanFresh`+archive fingerprint; `import <archive> --dry-run` previews (plain + `ImportArchivePlan` JSON); the close-wave rejection removed; **Ruling 18**: plan-first import — readout printed once, one minted UUID (the persisted one), the Ruling 7 accept re-run retired, `import failed:`/`extracting archive:` prefixes dropped from the five plan-time errors (all pinned) |

Also closed en route: #310's residue (the refused-conflict cache orphan) fell out of the plan-first restructure. Whole-branch reviews (opus) + scoped re-reviews gated both merges; Task B's twin matrix (14 scenarios vs 5e58e08) showed only the ruled deltas. Full reports retained in the git-ignored SDD workspace `.superpowers/sdd/2026-08-30-v2-pre-cut-impl/` until the v2.0.0 cut.
