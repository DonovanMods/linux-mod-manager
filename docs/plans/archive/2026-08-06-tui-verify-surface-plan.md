# TUI Verify/Health Surface (#224) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract `doVerify`'s ~700-line orchestration into a core verify engine (Local/Full tiers, findings + ordered events, fix mode), make the CLI a byte-identical renderer over it, and give the TUI a screen-7 context-view host whose home content is a Verify/Health surface with a dashboard health signal, full-check action (`c`), and batch-fix action (`F`).

**Architecture:** `internal/core/verify.go` gains `Service.Verify(ctx, game, profile, opts, progress)` emitting typed `VerifyEvent`s in exactly today's production order; `cmd/lmm/verify.go` becomes a renderer (goldens recorded FIRST prove byte-identity); `internal/tui` gains `ScreenHealth` (digit 7) as a pluggable context-view host with the health view as home content, `DataProvider.Health` (Local tier, rides `loadData` like Conflicts), and `ActionProvider.RunHealthCheck` (full/fix).

**Tech Stack:** Go 1.25, Bubble Tea/bubbles/lipgloss, Cobra, modernc.org/sqlite (`:memory:` tests), testify.

## Global Constraints

- Branch `feat/tui-verify-surface` from `develop`; PR `--base develop`; merge-commit; issue refs `(#224)` in every commit subject.
- NO version bump; CHANGELOG entries under `[Unreleased]`.
- `gofmt -l cmd internal` clean, `go vet ./...` clean, and the FULL suite green before EVERY commit. Never pipe `go test` into another command in a `&&` chain; never commit red.
- **Byte-identical CLI verify output is a hard requirement.** Task 1's goldens are recorded from the PRE-refactor code and are never edited afterward; any later golden-test failure is a defect in the extraction, not a golden to regenerate.
- `internal/core` must NEVER import `internal/source/icarus` (alias types through `internal/source` — existing precedent).
- The `--json` contract (`verifyJSONOutput`/`verifyFileJSON` shapes, statuses, note text, row order) is frozen — no additions, no reorderings.
- `cmd/lmm/tui.go` `Long` help and the README TUI section MUST be updated in this branch (recurring staleness trap: grep for `1-6`, "conflicts)", and capability lists). `tui.go`'s Long is embedded in the committed man pages — run `make man` in the docs task.
- TUI merge gate: the USER's interactive smoke test. Never auto-merge.
- Existing verify tests (`cmd/lmm/verify_test.go`, `verify_converge_test.go`, `verify_convert_test.go`, and every other test touching verify) stay untouched and green through the entire branch — they are part of the byte-identity proof.

---

### Task 1: Golden capture harness (pre-refactor safety net)

**Files:**

- Create: `cmd/lmm/verify_golden_test.go`
- Create: `cmd/lmm/testdata/verify_golden/` (generated `.golden` files, committed)

**Interfaces:**

- Consumes: existing test fixtures — `setupDoVerifyConvergeTest` (`verify_converge_test.go:43`), `setupDoVerifyEmptyProfileConvergeTest` (`verify_converge_test.go` #217 tests), the version-mismatch fixture `setupDoVerifyVersionTest` (`verify_test.go`), and the #221 convert fixtures in `verify_convert_test.go` — plus `captureStdout` (`auth_status_test.go:16`).
- Produces: `runVerifyGolden(t, name, fixture func(t) (*cobra.Command, *core.Service, *domain.Game), fix, json bool)` — the harness every later task re-runs unchanged; `-update` flag support via `go test ./cmd/lmm/ -run TestVerifyGolden -update`.

**Why first:** these snapshots ARE the spec for Task 7's renderer swap. They snapshot today's output through the real `doVerify` across: empty profile, convergence candidates, version mismatch (incl. locked refusal), needs-reingest/conversion statuses — each in plain, `--fix`, and `--json` modes (fix-mode runs use fresh fixtures per mode since `--fix` mutates).

- [ ] **Step 1: Write the harness + scenario table**

```go
package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

var updateGolden = flag.Bool("update", false, "rewrite verify golden files from current output")

// runVerifyGolden runs doVerify against a fresh fixture with the given
// output-mode globals and compares (or, with -update, records) the exact
// stdout transcript. Colors are already suppressed in tests (no TTY), so
// the transcript is stable. Each invocation builds its OWN fixture: fix
// mode mutates state, so scenarios can never share one.
func runVerifyGolden(t *testing.T, name string, fixture func(*testing.T) (*cobra.Command, *core.Service, *domain.Game), fix, json bool) {
	t.Helper()
	cmd, svc, game := fixture(t)
	oldFix, oldJSON := verifyFix, jsonOutput
	verifyFix, jsonOutput = fix, json
	t.Cleanup(func() { verifyFix, jsonOutput = oldFix, oldJSON })

	out := captureStdout(t, func() error { return doVerify(cmd, svc, game, nil) })

	path := filepath.Join("testdata", "verify_golden", name+".golden")
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(out), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden missing - run with -update on the PRE-refactor tree only")
	require.Equal(t, string(want), out, "verify output must be byte-identical to the pre-refactor golden")
}

func TestVerifyGolden(t *testing.T) {
	scenarios := []struct {
		name    string
		fixture func(*testing.T) (*cobra.Command, *core.Service, *domain.Game)
	}{
		{"empty_profile", func(t *testing.T) (*cobra.Command, *core.Service, *domain.Game) {
			cmd, svc, game, _ := setupDoVerifyEmptyProfileConvergeTest(t)
			return cmd, svc, game
		}},
		{"converge", setupDoVerifyConvergeTest},
		// Version-mismatch and convert-status fixtures: adapt the setup
		// helpers already in verify_test.go / verify_convert_test.go to this
		// signature with thin wrappers in this file (each returns cmd, svc,
		// game exactly like the two above).
	}
	for _, sc := range scenarios {
		for _, mode := range []struct {
			suffix    string
			fix, json bool
		}{{"plain", false, false}, {"fix", true, false}, {"json", false, true}, {"fix_json", true, true}} {
			t.Run(sc.name+"_"+mode.suffix, func(t *testing.T) {
				runVerifyGolden(t, sc.name+"_"+mode.suffix, sc.fixture, mode.fix, mode.json)
			})
		}
	}
}
```

- [ ] **Step 2: Add the version-mismatch and convert wrappers.** Open `cmd/lmm/verify_test.go` and `cmd/lmm/verify_convert_test.go`, find their setup helpers (`setupDoVerifyVersionTest` and the needs-reingest/conversion-failed fixtures), and add thin adapter funcs in `verify_golden_test.go` matching the harness signature. If a helper returns extra values, discard them; if one takes scenario parameters, bake the values its own tests use. Include a locked-mod version-mismatch scenario (the fixture for `--fix skipped: ... is locked` exists in `verify_test.go`'s lock tests).
- [ ] **Step 3: Record goldens** — run `go test ./cmd/lmm/ -run TestVerifyGolden -update` then `go test ./cmd/lmm/ -run TestVerifyGolden` and confirm PASS. Inspect each `.golden` by eye: every scenario must show real content (not just headers) — a golden that captured an unexpected error line is a broken fixture, fix it now.
- [ ] **Step 4: Full gates** — gofmt, vet, full suite (run `go test ./...` on its own).
- [ ] **Step 5: Commit** — `test: record pre-refactor verify output goldens (#224)`

---

### Task 2: Core verify types, skeleton, and the local per-file walk

**Files:**

- Create: `internal/core/verify.go`
- Create: `internal/core/verify_test.go`
- Modify: `cmd/lmm/verify.go` (delete `hasRetainedSource` + `sourceMappedMod`, re-point callers)
- Create: `internal/core/verify_helpers.go` (moved helpers)

**Interfaces:**

- Consumes: `Service.GetFilesWithChecksums(gameID, profile string) ([]db.FileChecksum, error)`, `Service.GetInstalledMod(sourceID, modID, gameID, profile string) (*domain.InstalledMod, error)`, `Service.GetGameCache(game *domain.Game) *cache.Cache`, `cache.RetainedSourceName`, `gameCache.Exists/ListFiles/FileManifests/GetFilePath`.
- Produces (frozen for ALL later tasks):

```go
type VerifyTier int

const (
	VerifyLocal VerifyTier = iota
	VerifyFull
)

type VerifyOptions struct {
	Tier      VerifyTier
	Fix       bool
	ModFilter string
}

type VerifyFinding struct {
	ModID, ModName, FileID, Status, Note string
}

type VerifyResult struct {
	Findings         []VerifyFinding
	Issues, Warnings int
	Checked          int  // feeds the CLI's "No files found for mod X" gate
	HasFiles         bool // false = the #217 empty-profile path ran
}

type VerifyEventKind int

const (
	VerifyEvBegin           VerifyEventKind = iota // HasFiles
	VerifyEvFinding                                // Finding + extras; row was appended to Findings
	VerifyEvRepairDetail                           // indented sub-line; Detail pre-formatted, Green tone flag
	VerifyEvSyncWarning                            // stderr-bound merged-pak sync warning (Detail)
	VerifyEvVerbose                                // verbose-gated diagnostic (Detail)
	VerifyEvProgress                               // Full-tier network tick (Index/Total/ModName)
)

type VerifyEvent struct {
	Kind     VerifyEventKind
	HasFiles bool
	Finding  VerifyFinding // valid for VerifyEvFinding
	// Main-line extras the CLI needs beyond the finding row itself:
	Recorded, Effective, Version string // version_mismatch / missing
	ExpectedCount                int    // file_count_mismatch
	Variant                      string // "" | "checksum_populated" (ok main line) | "fixed_green" (fixed_stale_deployment whole-line green - Task 6)
	// Sub-line / progress payload:
	Detail       string
	Green        bool
	Index, Total int
	ModName      string
}

func (s *Service) Verify(ctx context.Context, game *domain.Game, profile string, opts VerifyOptions, progress func(VerifyEvent)) (*VerifyResult, error)
```

Also produced: `internal/core/verify_helpers.go` with `hasRetainedSource(gameCache *cache.Cache, gameID, sourceID, modID, version string, fileIDs []string) bool` and `sourceMappedMod(game *domain.Game, mod *domain.Mod) *domain.Mod` — moved VERBATIM (doc comments included) from `cmd/lmm/verify.go:238` and `cmd/lmm/verify.go:977`, exported as `HasRetainedCompileSource` / kept-internal `sourceMappedMod`.

**Engine internal shape (all later engine tasks extend this):** `Verify` builds a `verifyRun{svc, game, profile, opts, emit func(VerifyEvent), result *VerifyResult}` and calls phase methods in today's order. `emit` is nil-safe (no-op progress). Every finding append goes through:

```go
func (r *verifyRun) finding(f VerifyFinding, extras VerifyEvent) {
	r.result.Findings = append(r.result.Findings, f)
	extras.Kind, extras.Finding = VerifyEvFinding, f
	r.emit(extras)
}
// and fix-resolution row rewrites through:
func (r *verifyRun) resolveLast(status, note string) {
	last := &r.result.Findings[len(r.result.Findings)-1]
	last.Status, last.Note = status, note
}
```

This task ports (from `cmd/lmm/verify.go`, keeping counting rules identical):

1. `GetFilesWithChecksums` + the `len(files) == 0` early path (emit `VerifyEvBegin{HasFiles:false}`, run nothing else this task — convergence lands in Task 6; until then the empty path returns an empty result).
2. The file-count pre-pass (lines 339-415) — statuses `skipped`/`file_count_mismatch`, `hasRetainedSource` carve-out, `reportedMismatch` dedup.
3. The per-file loop's NON-fix branches (lines 678-854 minus fix blocks and minus `PakNeedsReingest`, which is Task 3): unknown-mod `skipped`, `missing` (Version extra), `no_checksum`, `ok`, `Checked` increments.

- [ ] **Step 1: Write failing engine tests** in `internal/core/verify_test.go` (package `core_test`, in-memory service via the same builders `merged_pak_test.go` uses — `newFlowsTestService(t)` is internal, so use the exported test seams other `core_test` files use; check `pak_convert_e2e_test.go` for the external-service builder and mirror it):

```go
func TestVerify_LocalWalk_StatusesAndCounts(t *testing.T) {
	// Fixture: game + profile with (a) one mod fully cached+checksummed -> ok,
	// (b) one checksum row whose version dir is absent -> missing (issues=1),
	// (c) one row with empty checksum -> no_checksum (warnings=1),
	// (d) one checksum row for an uninstalled mod -> skipped (warnings=1).
	// Assert result.Findings order is exactly (file-count pass first, then
	// per-file rows in GetFilesWithChecksums order), Issues==1, Warnings==2,
	// Checked==row count, HasFiles==true, and that a progress collector saw
	// one VerifyEvBegin{HasFiles:true} followed by one VerifyEvFinding per row
	// with Version set on the missing row.
}

func TestVerify_EmptyProfile_HasFilesFalse(t *testing.T) {
	// No checksummed files: result.HasFiles==false, Findings empty,
	// Begin event carries HasFiles=false.
}

func TestVerify_ModFilter_LimitsRows(t *testing.T) { /* filter to one mod: other rows absent, Checked matches */ }
```

- [ ] **Step 2: Run to see them fail** — `go test ./internal/core/ -run TestVerify_` → FAIL (undefined types).
- [ ] **Step 3: Implement** `verify.go` types + skeleton + the three ported phases; move the two helpers to `verify_helpers.go` verbatim and update `cmd/lmm/verify.go` call sites (`hasRetainedSource(...)` → `core.HasRetainedCompileSource(...)`; `sourceMappedMod` stays needed by CLI paths until Task 7 — move it to core NOW and give the CLI a one-line wrapper `func sourceMappedMod(...)` delegating to an exported `core.SourceMappedMod` so nothing breaks and Task 7 just deletes the wrapper).
- [ ] **Step 4: Run engine tests to pass, then the FULL suite including `TestVerifyGolden`** (CLI behavior unchanged — goldens must still pass).
- [ ] **Step 5: Commit** — `feat(core): verify engine skeleton with local per-file walk (#224)`

---

### Task 3: Merged-pak checks, needs-reingest, lock-pending, and the Full-tier version pass

**Files:**

- Modify: `internal/core/verify.go`
- Modify: `internal/core/verify_test.go`

**Interfaces:**

- Consumes: Task 2's `verifyRun`/`finding`/`emit`; `Service.CheckMergedPakStaleness(game, profile)`, `Service.MergedPakOutcomes(game, profile)`, `Service.PakNeedsReingest(game, mod, fileID)`, `Service.GetModFiles(ctx, sourceID, mod)`, `Service.GetInstalledMods(gameID, profile)`, `config.LoadProfile(svc.ConfigDir(), gameID, profile)`, `domain.EffectiveInstalledVersion`, `core.SourceMappedMod`.
- Produces: the complete READ side of the engine — every status except the fix-mode resolutions and convergence. Emission order (must match `doVerify` exactly): file-count pass → merged-pak staleness (`stale_compile`) → conversion outcomes (`conversion_failed`) → per-mod version pass → per-file loop (with `needs_reingest` before the cache-existence check, `VerifyEvVerbose` on its check error).

Port details (source line refs into pre-refactor `cmd/lmm/verify.go`):

- Staleness (449-474): `skipped` on check error; `stale_compile` with `Note: staleUpd.RecompileReason`; `Checked++` unconditionally inside the DeployCompile branch.
- Conversion outcomes (484-514): name fallback `entry.ModID` when the installed map misses; `conversion_failed` with `Note: entry.FailReason`.
- Version pass (516-676) — **gated on `opts.Tier == VerifyFull`**: local-source/manual/no-fileIDs silent skip; `skipped` + note `"could not check version: %v"` on `GetModFiles` error; `version_unverifiable`; `version_mismatch` (extras `Recorded`, `Effective`) with `issues++` — the fix branch is Task 5's; the quiet-OK `Checked++` and the lock-pending-convergence informational row (`ok` + note, extras none) port here (lock state via `prof.FindRef` from the up-front `config.LoadProfile`). Emit `VerifyEvProgress{Index: i+1, Total: len(installedMods), ModName: mod.Name}` at the TOP of each mod's iteration (new — CLI ignores it; TUI status line consumes it), and honor `ctx.Err()` between mods: on cancellation return the partial result with the error.
- `needs_reingest` (696-753 minus the fix block): note text switches on `domain.SourceLocal`; check-error emits `VerifyEvVerbose` with EXACTLY the current text `fmt.Sprintf("could not check pak-reingest status for %s (%s): %v", mod.Name, f.FileID, nerr)` (the CLI adds its own ` (verbose)` prefix).

- [ ] **Step 1: Failing tests** — extend `verify_test.go`:

```go
func TestVerify_LocalTier_NeverTouchesNetwork(t *testing.T) {
	// Register a source whose GetModFiles fails the test if called
	// (t.Fatal via a callback), install a source-backed mod with FileIDs,
	// run Verify with Tier: VerifyLocal. No version rows, no skipped rows,
	// and the trap never fires. THE spec's no-network proof.
}
func TestVerify_FullTier_VersionStatuses(t *testing.T) {
	// Table: reachable+matched -> quiet ok (Checked++ only);
	// unreachable -> skipped w/ note; no matching fileID -> version_unverifiable;
	// effective != recorded -> version_mismatch with Recorded/Effective extras
	// and issues++; locked ref pending convergence -> ok + note row.
	// Assert a VerifyEvProgress tick per source-backed mod.
}
func TestVerify_CompileGameStatuses(t *testing.T) {
	// DeployCompile fixture (mirror merged_pak_test.go's): stale fingerprint
	// -> stale_compile row; a non-Converted outcome entry -> conversion_failed
	// row with FailReason note; a pre-#221 pak (deployable member, no
	// retained source) -> needs_reingest row with the redownload note, and
	// the SourceLocal variant note for a local mod.
}
func TestVerify_ContextCancelledMidVersionPass(t *testing.T) {
	// Cancel ctx from the first mod's GetModFiles trap; Verify returns
	// ctx.Err() with the partial result it had.
}
```

- [ ] **Step 2: Run to fail** — `go test ./internal/core/ -run 'TestVerify_(Local|Full|Compile|Context)'`.
- [ ] **Step 3: Implement the ports** per the line-ref map above.
- [ ] **Step 4: Engine tests green, full suite + goldens green.**
- [ ] **Step 5: Commit** — `feat(core): verify engine compile checks and Full-tier version pass (#224)`

---

### Task 4: Fix mode I — redownload repairs (missing / no-checksum / needs-reingest)

**Files:**

- Modify: `internal/core/verify.go`
- Create: `internal/core/verify_repair.go`
- Modify: `internal/core/verify_test.go`

**Interfaces:**

- Consumes: `Service.DownloadMod`, `Service.SaveFileChecksum`, Task 2/3's run plumbing.
- Produces: `func (r *verifyRun) redownloadModFile(ctx context.Context, mod *domain.InstalledMod, fileID string) (persisted bool, err error)` in `verify_repair.go` — `cmd/lmm/verify.go:1544`'s body moved verbatim minus the `cmd` parameter (takes `ctx` directly), using `core.SourceMappedMod`. Resolution semantics (all inside `opts.Fix && mod.SourceID != domain.SourceLocal` guards, matching today's sites):
  - `missing` → on error `RepairDetail{Detail: "Re-download failed: <err>", Green: false}` + last-row `Note=err`; on `persisted` → `RepairDetail{"Re-downloaded OK", Green: true}` + `resolveLast("ok", "")` + `issues--`; on not-persisted → `RepairDetail{"Re-downloaded, but no checksum was available to store - NO CHECKSUM remains"}` + `resolveLast("no_checksum", "re-downloaded, but no checksum was available to store")` + `issues--; warnings++`.
  - `no_checksum` fix path REPLACES the plain row emission (today's code appends different rows per outcome — port the exact branching from lines 804-846, including the `ok` + `Variant: "checksum_populated"` main-line emission on success).
  - `needs_reingest` → failure `RepairDetail{"Re-ingest failed: <err>"}` + note rewrite; success `RepairDetail{"Re-ingested for pak conversion", Green: true}` + `resolveLast("fixed_needs_reingest", "re-ingested with retained source for pak conversion")` + `warnings--`.

**Sub-line contract note (applies to Tasks 4-6):** every `VerifyEvRepairDetail.Detail` string is the EXACT text inside today's `fmt.Printf("  %s\n", ...)`-style calls, WITHOUT the two-space indent and without color — the CLI renderer adds indent + `colorGreen` when `Green`. The goldens are the arbiter.

- [ ] **Step 1: Failing tests** — fake source with controllable download outcomes (mirror the fake-source patterns in `service_icarus_compile_test.go`): table over {missing+success, missing+no-checksum, missing+error, no_checksum+success, needs_reingest+success, needs_reingest+error, any+SourceLocal (no repair attempted)}; assert final Findings statuses/notes, Issues/Warnings arithmetic, and the RepairDetail event sequence (Detail text + Green flags) per case.
- [ ] **Step 2: Run to fail.**
- [ ] **Step 3: Implement** (`verify_repair.go` + the three fix blocks in the walk).
- [ ] **Step 4: Green: engine, full suite, goldens.**
- [ ] **Step 5: Commit** — `feat(core): verify fix mode redownload repairs (#224)`

---

### Task 5: Fix mode II — version repair, siblings, locked refusals

**Files:**

- Modify: `internal/core/verify_repair.go` (port `repairModVersion`, `repairSiblingProfiles`, `fileIDsEqual`, `relinkDeployedRow`, `cacheDirExists` from `cmd/lmm/verify.go:998-1499`)
- Modify: `internal/core/verify.go` (the version-mismatch fix branch, lines 583-657)
- Modify: `internal/core/verify_test.go`

**Interfaces:**

- Consumes: `svc.NewProfileManager()`, `Service.SetModVersion/SetModDeployed/SetModLinkMethod/GetEffectiveLinkMethod/NewInstallerWithLinker/GetLinker`, `domain.ModReference`, profile `FindRef`.
- Produces: `func (r *verifyRun) repairModVersion(ctx context.Context, mod *domain.InstalledMod, effective string) (note string, siblingFailures int, err error)` and helpers — bodies moved with EVERY doc comment intact; each inline `fmt.Printf` becomes a `VerifyEvRepairDetail` emission whose Detail is the current text minus indent (e.g. `"Warning: could not repair profile %s: %v"`, `"Warning: %s is locked at v%s in profile %s; run ..."` — the full remedy strings preserved verbatim). The locked-primary refusal (line 598's sentence) becomes `RepairDetail{Detail: refusal}` + last-row `Note="locked"` set ONLY for the findings slice (JSON) — the CLI text mode prints the refusal sub-line; the row keeps `version_mismatch`. Success path emits `RepairDetail{Detail: fmt.Sprintf("Repaired: %s → %s", recorded, effective), Green: true}` then, when note != "", `RepairDetail{Detail: "Note: " + note}` — and `resolveLast("ok", note)` + `issues--`. Failure path: `RepairDetail{"Repair failed: <err>"}` + note rewrite `"repair failed: <err>[; <note>]"`. `warnings += siblingFailures` regardless of outcome.

**Ordering trap (spec-governs rule):** today the JSON-mode sibling/relink warnings do NOT print (jsonOutput-gated) but their text-mode prints happen DURING the repair, before the primary row's own resolution lines. Emission order must be: per-sibling/relink RepairDetail events as they occur (the CLI text renderer prints them; JSON ignores) → the final Repaired/failed detail → row rewrite. The goldens for the version-mismatch scenarios prove this ordering.

- [ ] **Step 1: Failing tests** — port shapes from the existing CLI version-repair tests (read `verify_test.go`'s repair suite for fixtures): {clean repair (+cache rename), blocked rename (note, no relink), locked primary (refusal event + note "locked" + issue stays), sibling repaired, sibling differs (decline + warning count), sibling locked (decline + warning count), relink failure (Deployed cleared, error path)}; assert event Detail strings verbatim against the current code's formats.
- [ ] **Step 2: Run to fail.**
- [ ] **Step 3: Implement the port.**
- [ ] **Step 4: Green: engine, full suite, goldens.**
- [ ] **Step 5: Commit** — `feat(core): verify fix mode version repair with sibling and lock semantics (#224)`

---

### Task 6: Convergence, merged-pak sync, and engine completion

**Files:**

- Modify: `internal/core/verify.go`
- Modify: `internal/core/verify_test.go`

**Interfaces:**

- Consumes: `Service.ConvergeDeployedFiles(ctx, game, profile, dryRun)`, `Service.SyncMergedPak(ctx, game, profile)`, `unwrapJoinedErrors` (move from `cmd/lmm/verify.go:990` into `verify_helpers.go`, unexported `unwrapJoined`).
- Produces: the COMPLETE engine. Final phase order after the per-file walk: (fix only) `SyncMergedPak` — error → `VerifyEvSyncWarning{Detail: fmt.Sprintf("could not sync merged pak: %v", err)}`; each returned warning → `VerifyEvSyncWarning{Detail: w}` — then the convergence pass (port of `reportConvergencePass`, `cmd/lmm/verify.go:917-961`): `fixed_stale_deployment` rows emit with `Green: true` semantics via the MAIN line (CLI renders the whole `Fixed: removed %s (%s)` line green — give the finding event `Variant: "fixed_green"`), `stale_deployment` + `warnings++`, per-item joined errors → `skipped` rows with `Note: "convergence: <err>"`. The #217 empty-profile path now runs Begin → convergence → done (Issues always 0 there).

- [ ] **Step 1: Failing tests** — {empty-profile with dangling link (dry-run row + warning; fix removes + `fixed_stale_deployment`), main-path convergence after repairs, sync warning events in fix mode (stub a compile game whose sync returns warnings), full-order integration test asserting the exact Findings sequence for a fixture combining file-count + stale_compile + conversion_failed + needs_reingest + missing + convergence rows}.
- [ ] **Step 2: Run to fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Green everywhere; goldens still untouched and green.**
- [ ] **Step 5: Commit** — `feat(core): complete verify engine with convergence and sync phases (#224)`

---

### Task 7: CLI renderer swap (byte-identity gate)

**Files:**

- Modify: `cmd/lmm/verify.go` — `doVerify` body replaced; DELETE `repairModVersion`, `repairSiblingProfiles`, `fileIDsEqual`, `relinkDeployedRow`, `redownloadModFile`, `cacheDirExists`, `reportConvergencePass`, `unwrapJoinedErrors`, the CLI `sourceMappedMod` wrapper, `hasRetainedSource` remnants — everything the engine absorbed.

**Interfaces:**

- Consumes: `core.VerifyOptions/VerifyEvent/VerifyResult`, `Service.Verify`.
- Produces: `doVerify(cmd, svc, game, args) error` — signature unchanged (tests call it directly). Shape:

```go
func doVerify(cmd *cobra.Command, svc *core.Service, game *domain.Game, args []string) error {
	profile, err := resolveProfile(svc, game.ID, verifyProfile)
	if err != nil {
		return err
	}
	var modFilter string
	if len(args) > 0 {
		modFilter = args[0]
	}
	opts := core.VerifyOptions{Tier: core.VerifyFull, Fix: verifyFix, ModFilter: modFilter}
	result, err := svc.Verify(cmd.Context(), game, profile, opts, func(ev core.VerifyEvent) {
		if jsonOutput {
			if ev.Kind == core.VerifyEvSyncWarning {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", ev.Detail) // stderr never corrupts the JSON document
			}
			return // rows come from result.Findings below
		}
		renderVerifyEvent(ev)
	})
	if err != nil {
		return err
	}
	if jsonOutput { /* map result.Findings -> verifyFileJSON rows, encode verifyJSONOutput exactly as today (empty-profile keeps Files: []verifyFileJSON{}) */ }
	/* text summary: the empty-profile variant when !result.HasFiles (0-issue line + short fix hint),
	   else blank line + "No files found for mod X" gate (result.Checked) + issue/warning summary or All-OK — moved verbatim */
}
```

`renderVerifyEvent` is one switch reproducing every current format string (the finding main lines keyed on `Finding.Status` + `Variant` + extras; `VerifyEvRepairDetail` → `fmt.Printf("  %s\n", ...)` with `colorGreen` when `Green`; `VerifyEvVerbose` → gated on `verbose`, prefixed ` (verbose)`; `VerifyEvBegin` → the two headers). Copy each format string FROM the deleted code, not from memory.

- [ ] **Step 1: Swap the implementation** (no new tests first here — the goldens and the whole existing verify suite ARE the failing-test discipline: they were green, the swap must keep them green).
- [ ] **Step 2: Run `go test ./cmd/lmm/ -run TestVerifyGolden -v`** — every scenario byte-identical. Any diff: fix the renderer/engine, NEVER the golden.
- [ ] **Step 3: Full suite + gofmt + vet.** Also `go build ./cmd/lmm` and eyeball `./lmm -g <any> verify` compiles/runs in a sandbox HOME if convenient.
- [ ] **Step 4: Verify dead code is gone** — `grep -n "repairModVersion\|redownloadModFile\|reportConvergencePass" cmd/lmm/` returns nothing.
- [ ] **Step 5: Commit** — `refactor(cli): verify renders the core engine's events, byte-identical (#224)`

---

### Task 8: TUI provider seams — HealthView, DataProvider.Health, ActionProvider.RunHealthCheck

**Files:**

- Modify: `internal/tui/service.go` (types + `DataProvider` method + prototype impl)
- Modify: `internal/tui/actions_provider.go` (`ActionProvider` method + docs)
- Modify: `internal/tui/service_core.go` (coreProvider impls)
- Create: `internal/tui/health_provider_test.go`

**Interfaces:**

- Consumes: `core.VerifyOptions/VerifyTier/VerifyEvent/VerifyFinding`, `Service.Verify` (Tasks 2-6).
- Produces (frozen for Tasks 9-12):

```go
// service.go
type HealthFinding struct {
	ModID, ModName, FileID, Status, Note string
}

type HealthView struct {
	Findings         []HealthFinding
	Issues, Warnings int
	Full             bool // true when produced by the Full (network) tier
}

// DataProvider gains:
//	// Health runs the LOCAL verify tier (disk/DB only - never the network;
//	// core.VerifyLocal) for the dashboard signal and the Health screen's
//	// initial content. Rides loadData like Conflicts.
	Health(ctx context.Context) (HealthView, error)

// actions_provider.go - ActionProvider gains:
//	// RunHealthCheck runs the verify engine on demand: full=true adds the
//	// network version pass ('c'); fix=true applies CLI --fix semantics
//	// behind the Health screen's confirmation ('F', always full). progress
//	// receives one line per VerifyEvProgress / RepairDetail / Finding event.
	RunHealthCheck(ctx context.Context, full, fix bool, progress func(ActionProgress)) (HealthView, error)
```

coreProvider implementations (service_core.go):

```go
func (p *coreProvider) Health(ctx context.Context) (HealthView, error) {
	game := p.currentGame()
	res, err := p.svc.Verify(ctx, game, p.currentProfile(), core.VerifyOptions{Tier: core.VerifyLocal}, nil)
	if err != nil {
		return HealthView{}, err
	}
	return healthView(res, false), nil
}

func (p *coreProvider) RunHealthCheck(ctx context.Context, full, fix bool, progress func(ActionProgress)) (HealthView, error) {
	opts := core.VerifyOptions{Tier: core.VerifyLocal, Fix: fix}
	if full {
		opts.Tier = core.VerifyFull
	}
	res, err := p.svc.Verify(ctx, p.currentGame(), p.currentProfile(), opts, func(ev core.VerifyEvent) {
		if progress == nil {
			return
		}
		switch ev.Kind {
		case core.VerifyEvProgress:
			progress(ActionProgress{Line: fmt.Sprintf("checking versions %d/%d: %s", ev.Index, ev.Total, ev.ModName)})
		case core.VerifyEvFinding:
			progress(ActionProgress{Line: fmt.Sprintf("%s: %s", ev.Finding.Status, ev.Finding.ModName)})
		case core.VerifyEvRepairDetail, core.VerifyEvSyncWarning:
			progress(ActionProgress{Line: ev.Detail})
		}
	})
	if err != nil {
		return HealthView{}, err
	}
	return healthView(res, full), nil
}

func healthView(res *core.VerifyResult, full bool) HealthView {
	v := HealthView{Issues: res.Issues, Warnings: res.Warnings, Full: full, Findings: make([]HealthFinding, 0, len(res.Findings))}
	for _, f := range res.Findings {
		if f.Status == "ok" && f.Note == "" {
			continue // quiet-ok rows carry no screen content; lock-pending (ok+note) stays
		}
		v.Findings = append(v.Findings, HealthFinding{ModID: f.ModID, ModName: f.ModName, FileID: f.FileID, Status: f.Status, Note: f.Note})
	}
	return v
}
```

prototypeProvider: `Health` returns a canned view (two findings: one `stale_deployment`, one `conversion_failed`, Warnings: 2) so `--prototype` demos the screen; `RunHealthCheck` returns the same view after `fakeProgressTicks(progress, "checking")`, with `fix=true` returning an emptied view (all findings resolved) — no real I/O ever.

- [ ] **Step 1: Failing provider tests** (`health_provider_test.go`, package `tui`, using the same real-service builders `service_core_convert_test.go` uses): {Health returns Local findings and NEVER hits the network (register the Task-3-style trap source and assert no trip), RunHealthCheck(full=true) includes version statuses + emits progress lines, RunHealthCheck(fix=true) resolves a fixable finding, ok-row filtering keeps lock-pending rows}.
- [ ] **Step 2: Run to fail.** — `go test ./internal/tui/ -run TestHealth`
- [ ] **Step 3: Implement all three providers.**
- [ ] **Step 4: Green + full suite.**
- [ ] **Step 5: Commit** — `feat(tui): health provider seams over the core verify engine (#224)`

---

### Task 9: ScreenHealth — context-view host + health home content

**Files:**

- Modify: `internal/tui/navigation.go` (ScreenHealth after ScreenConflicts, screens slice, String() "Health")
- Create: `internal/tui/contextview.go` (host)
- Modify: `internal/tui/keys.go` (`HealthScreen` binding, key "7")
- Modify: `internal/tui/app.go` (Model fields, screenView case, healthView render, itemCount, helpGroups entry, updateKey: digit 7 + esc-pop)
- Create: `internal/tui/health_screen_test.go`

**Interfaces:**

- Consumes: Task 8's `HealthView`; existing Model plumbing (`m.selected`, `clampLines`, `truncateLines`, theme styles, `conflictsListPane`/`conflictsDetailPane` as the layout reference).
- Produces:

```go
// contextview.go
// contextContent is a pluggable full-screen content view hosted by
// ScreenHealth (#224). #86's mod-details view will be the second
// implementation. One-deep stack by design (YAGNI): push replaces any
// pushed content; esc pops back to the pushing screen; with nothing
// pushed, ScreenHealth renders the health home view.
type contextContent interface {
	Title() string
	// Lines renders the body for the given content box; the host owns
	// chrome (panel, title, nav) and clamping.
	Lines(width, height int) []string
	// HandleKey lets pushed content consume a key before the outer
	// switch; handled=false falls through to global handling.
	HandleKey(msg tea.KeyMsg) (next contextContent, cmd tea.Cmd, handled bool)
	HelpGroup() helpGroup
}

// Model gains:
//	contextContent contextContent // nil = health home
//	contextReturn  Screen         // pop target
//	health         HealthView
//	healthAt       *time.Time    // when health was produced (m.now() at receipt)
//	healthErr      string        // last scan failure, "" when fine

func (m *Model) pushContext(c contextContent, from Screen)  // sets fields, jumps to ScreenHealth
func (m *Model) popContext() Screen                         // clears, returns pop target
```

Health home rendering (`app.go`): `healthHomeView()` mirrors `conflictsView()`'s two-pane shape — list rows `[status-glyph] STATUS  MOD (FILE)` tinted by status class (`missing`/`version_mismatch` danger; `ok` fine; everything else warning), detail pane shows the selected finding's ModName/FileID/Status/Note plus remedy copy per status (reuse the CLI's remedy phrasings: e.g. needs_reingest -> "run a fix (F) to re-ingest"). Header line: `last scan: local, 3m ago` / `full, just now` from `healthAt` (reuse `lastDeployLabel`'s relative-age helper) — or `no scan yet` when nil. Empty state: `no findings (local) — run a full check (c)`. Selection/scroll through `m.selected[ScreenHealth]` + `itemCount` returning `len(m.health.Findings)`.

Key routing in `updateKey`: digit 7 → `m.gotoScreen(ScreenHealth)`; on ScreenHealth with `m.contextContent != nil`, offer the key to `HandleKey` first and handle `esc` as pop (return to `contextReturn`); helpGroups gains a "Health" group (7, c, F — c/F bindings arrive in Tasks 11/12 but the group lands here with the 7 entry and grows).

- [ ] **Step 1: Failing model tests** (`health_screen_test.go`, teatest-free model-update style like `convert_test.go`): {digit 7 reaches ScreenHealth and tab-cycle includes it after Conflicts; healthHomeView renders findings + empty state + header age; a fake pushed content (test-local struct implementing contextContent) renders its lines and title, its HandleKey consumes a key, esc pops back to the pushing screen — THE spec-criterion-4 proof; popping with nothing pushed is a no-op}.
- [ ] **Step 2: Run to fail.**
- [ ] **Step 3: Implement** navigation/keys/host/render.
- [ ] **Step 4: Green + full suite (nav-cycle tests elsewhere may assert 6 screens — update ONLY counts/orders, never semantics).**
- [ ] **Step 5: Commit** — `feat(tui): ScreenHealth context-view host with health home content (#224)`

---

### Task 10: Dashboard health line + menu entry

**Files:**

- Modify: `internal/tui/service.go` (`Summary.HealthIssues`, `Summary.HealthWarnings` — int, -1 unknown sentinel, doc comment mirroring Conflicts')
- Modify: `internal/tui/app.go` (`loadData` fetch + sentinel handling, all four dashboard layout views, `dashboardMenu`)
- Modify: `internal/tui/app_test.go` or create `internal/tui/health_dashboard_test.go`

**Interfaces:**

- Consumes: `DataProvider.Health` (Task 8), Model.health fields (Task 9).
- Produces: `loadData` calls `m.provider.Health(m.ctx)` after Conflicts; on success `summary.HealthIssues, summary.HealthWarnings = view.Issues, view.Warnings` and `dataLoadedMsg` carries the view (Model stores it + stamps `healthAt`); on error the summary keeps the -1 sentinels and the msg carries `healthErr` (load itself still succeeds — a failed health scan must NOT fail the whole load, unlike Conflicts today: wrap in its own error capture, not the early-return pattern). Dashboard line in every layout's summary block, phrased: `Health: ?` (sentinel) / `Health: OK (local)` / `Health: 1 issue(s), 2 warning(s) (local)` — suffix `(full)` when `m.health.Full`. `dashboardMenu` gains `{title: "Verify Integrity", screen: ScreenHealth}` before the Conflicts entry.

- [ ] **Step 1: Failing tests**: {loadData populates health + summary counts; provider Health error → sentinels + healthErr set + the scan error surfaces on the status line (spec error posture: "health shows ?, error on the status line") + load still completes; each layout view contains the Health line (render string contains "Health:"); menu entry opens ScreenHealth via enter}.
- [ ] **Step 2: Run to fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Green + full suite.**
- [ ] **Step 5: Commit** — `feat(tui): dashboard health signal from the local verify tier (#224)`

---

### Task 11: `c` — full check action

**Files:**

- Modify: `internal/tui/keys.go` (`FullCheck` binding, key "c", help "full health check")
- Modify: `internal/tui/mutations.go` (`runFullHealthCheck`)
- Modify: `internal/tui/app.go` (updateKey case: ScreenHealth + no pushed content)
- Modify: `internal/tui/health_screen_test.go`

**Interfaces:**

- Consumes: `ActionProvider.RunHealthCheck(ctx, full=true, fix=false, progress)`, `buildAction` (actions.go:285 — the single-flight/progress/drain machinery every async action uses).
- Produces:

```go
// mutations.go
// runFullHealthCheck dispatches the Full-tier (network) verify pass behind
// the standard single-flight action machinery - no confirm modal (it
// mutates nothing; mirrors checkForUpdates' fetch-then-show shape), live
// progress on the status line, cancellable by quit-drain.
func (m Model) runFullHealthCheck() (Model, tea.Cmd)
```

Implementation shape: follow `checkForUpdates` (mutations.go:1387) — refuse when an action is in flight ("busy"); dispatch via `buildAction`-style command that calls `RunHealthCheck(ctx, true, false, progress)`; on done, store the returned view (`m.health`, `healthAt = m.now()`, `healthErr=""`), update `summary.HealthIssues/Warnings`, status line `full check: N issue(s), M warning(s)` or `full check: all OK`; on failure, status line error + keep the previous view. Key dispatch: `case key.Matches(msg, m.keys.FullCheck)` gated on `m.screen == ScreenHealth && m.contextContent == nil` (note: "c" is CreateProfile on ScreenProfiles — per-screen dispatch keeps both).

- [ ] **Step 1: Failing tests**: {c on ScreenHealth dispatches and lands the new view + status line; c while an action runs is refused; c on other screens does nothing; progress ticks reach the status line (drive actionProgressMsg)}.
- [ ] **Step 2: Run to fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Green + full suite.**
- [ ] **Step 5: Commit** — `feat(tui): full health check action on the Health screen (#224)`

---

### Task 12: `F` — batch fix behind confirmation

**Files:**

- Modify: `internal/tui/keys.go` (`FixHealth` binding, key "F", help "fix findings")
- Modify: `internal/tui/mutations.go` (`fixHealthPrompt` + category-count detail builder)
- Modify: `internal/tui/app.go` (updateKey case)
- Modify: `internal/tui/health_screen_test.go`

**Interfaces:**

- Consumes: `ActionProvider.RunHealthCheck(ctx, full=true, fix=true, progress)`, `promptAction`/`buildAction` confirm-modal machinery, `infoOverlay` (the update-results pattern, actions.go), `loadData` refresh path used by actionDoneMsg.
- Produces:

```go
// fixHealthPrompt opens the standard y/n confirmation summarizing what a
// fix pass will attempt - counts by category from the CURRENT view
// (m.health), detail lines capped by the modal's own "+N more" - then runs
// the engine in fix mode (always Full: --fix parity includes the version
// pass). Refused on the status line when the current view has nothing
// actionable. Completion shows per-item results in an info overlay and
// triggers the ordinary data reload so screen + dashboard refresh.
func (m Model) fixHealthPrompt() (Model, tea.Cmd)
```

Detail lines: one per status class present, e.g. `2 missing file(s) — re-download`, `1 version mismatch — re-key records (locked mods refused)`, `1 stale deployment — remove`, `1 pak needs re-ingest`; title `Fix N finding(s)?`. "Nothing actionable" = every finding's status is one that fix cannot touch (`skipped`, `version_unverifiable`, `file_count_mismatch`, `ok`) — refuse with `nothing fixable — run a full check (c) first` when the view is empty or unfixable. On done: overlay lists the returned view's `fixed_*` rows and remaining findings (reuse the update-results overlay builder shape at actions.go:509 changelogOverlay as the layout reference), store the view, and return the loadData refresh cmd (same as actionDoneMsg's reload) so the dashboard line updates.

- [ ] **Step 1: Failing tests**: {F with fixable findings opens a modal whose detail contains the category counts; confirm dispatches and the resolved view lands + overlay shows; F with empty/unfixable view refused on status line; cancel leaves state untouched; locked version_mismatch remains after fix (engine refusal surfaces in remaining findings)}.
- [ ] **Step 2: Run to fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Green + full suite.**
- [ ] **Step 5: Commit** — `feat(tui): batch fix action with confirmation on the Health screen (#224)`

---

### Task 13: Docs, help text, CHANGELOG, man pages

**Files:**

- Modify: `cmd/lmm/tui.go` (Long: capability list gains "verify integrity and fix findings (health)"; `1-6` → `1-7`; screen list gains health)
- Modify: `README.md` (TUI section: screen list, keybindings table if present, #224 capability)
- Modify: `CHANGELOG.md` (`[Unreleased]` → Added: TUI health screen + dashboard signal; core verify engine extraction noted as internal)
- Run: `make man` (tui.go Long is embedded in committed man pages)

**Interfaces:** none — documentation only, but grep-driven: `grep -rn "1-6\|dashboard, installed mods, search, profiles, sources, conflicts" cmd/ README.md docs/man/` must return only updated text afterward.

- [ ] **Step 1: Update tui.go Long** (screen list + keys paragraph + capability sentence), README, CHANGELOG.
- [ ] **Step 2: `make man`; confirm `git status` shows only expected man-page diffs.**
- [ ] **Step 3: Staleness grep sweep** — `grep -rn "not yet\|read-only\|aren't available\|use 'lmm" internal/tui cmd/lmm/tui.go` and fix any statement the new capability falsifies.
- [ ] **Step 4: Full gates (genman test enforces the man regen).**
- [ ] **Step 5: Commit** — `docs: TUI health surface help, README, CHANGELOG (#224)`

---

### Task 14: E2E sweep + branch finalization

**Files:**

- Create: `internal/tui/health_e2e_test.go`
- Modify (if gaps found): any

**Interfaces:** consumes everything above.

- [ ] **Step 1: Write the lifecycle e2e** (real service, compile-game fixture reusing `pak_convert_e2e_test.go`'s builders): a pre-#221 pak (needs_reingest) + a dangling game-dir symlink → `DataProvider.Health` shows both findings (local, no network) → `RunHealthCheck(full=true, fix=true)` resolves both (`fixed_needs_reingest`, `fixed_stale_deployment`) → a second `Health` call comes back clean → dashboard summary counts hit 0. This is spec success-criterion 3 end-to-end.
- [ ] **Step 2: Run to fail / fix / pass.**
- [ ] **Step 3: Full-suite + gofmt + vet + `go build -o lmm ./cmd/lmm` (smoke binary at repo root for the user).**
- [ ] **Step 4: Re-run `TestVerifyGolden` one last time and `git diff --stat develop` sanity review (no unrelated files).**
- [ ] **Step 5: Commit** — `test: health surface lifecycle e2e (#224)` — then PR `--base develop` with the smoke checklist: (1) dashboard Health line on live Icarus profile; (2) screen 7 findings vs `lmm verify` parity; (3) `c` full check (network) with progress; (4) `F` fix on a needs-reingest state, confirm merged pak regenerates and raw links clear; (5) esc/nav/help-panel behavior; (6) CLI `lmm verify` and `verify --fix` behave exactly as before.
