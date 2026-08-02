# Icarus Merged-Pak Compilation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace per-mod `.exmodz` compilation for `deploy_mode: compile` games with a merged-ONLY model: every enabled `.exmodz` mod's row-level table diffs are applied sequentially, in profile load order, against the game's base tables into ONE profile-level `zzz_LMM_Merged_P.pak`, so multiple table-patching mods compose (field-level merge) instead of one mod's whole-table pak silently shadowing another's.

**Architecture:** A new `source.MergeCompiler` interface (replacing #196's `source.Compiler`) gives Icarus a `MergeCompile(sources []MergeSource, ...)` entry point that threads each mod's `.EXMOD` row-upserts through the SAME evolving table bytes — `ApplyRowPatch`'s existing shallow-merge semantics do the actual composing, unchanged. Ingest (download/import) drops per-mod pak generation entirely: it now only validates the `.exmodz` and retains its bytes in cache (zero deployment members). The merged pak itself is tracked as a synthetic, profile-scoped "mod" (`sourceID="lmm-merged"`, `modID="merged-pak"`) that reuses the EXISTING `Installer.Install`/`Uninstall`/cache/deployed-file machinery verbatim — no schema changes. A new `Service.syncMergedPak` (fingerprint-gated, cheap when nothing changed) is called from every mutation flow that can change the enabled-mod set, load order, mod version, or base pak; `lmm update`/`lmm verify` are the safety net that catches anything a hook call site misses, exactly as #196 relies on for base-pak drift today.

**Tech Stack:** Go stdlib only (`encoding/json` for the fingerprint marker, `crypto/md5` — already used via `md5File` — for exmodz checksums); no new dependencies. Builds on #196's staging/atomic-commit/reserved-marker cache primitives and #136/#175's `internal/unrealpak` reader/writer.

## Global Constraints

- Standard library only — no new third-party dependencies (matches `~/.claude/GO.md` and this repo's existing zero-new-deps discipline).
- Fail loud: a malformed `.exmodz`, an unresolvable base-pak table reference, or a merge failure must return an actionable error, never silently skip or produce a partial/incorrect merged pak.
- CLI and TUI parity through `internal/core` — no logic duplicated between `cmd/lmm` and `internal/tui`; both call the same `Service` methods.
- `--json` contract changes are additive-only: existing fields keep their exact names/types/omit-empty behavior; only new optional fields may be added.
- TDD: every task starts with a failing test before the implementation that makes it pass (`- [ ] Step: Write the failing test` / `- [ ] Step: Run it, confirm it fails` / `- [ ] Step: Implement` / `- [ ] Step: Run it, confirm it passes` / `- [ ] Step: Commit`).
- `gofmt`, `go vet`, `go test ./...`, and `trunk check` must be clean at the end of every task's commit.
- CHANGELOG discipline: this plan AMENDS the existing `[Unreleased] / Added` bullet for #196 in place (that bullet has never shipped in a tagged release — see Task 14) rather than adding a second, overlapping entry.
- Plain (non-`.exmodz`) `.pak` mods and non-`DeployCompile` games must be byte-for-byte unaffected by every task in this plan.
- Every new/changed function gets a doc comment stating the _why_, matching this repo's existing density (see `internal/core/updater.go`, `internal/storage/cache/cache.go` for the house style) — not the terser style used in this plan's own code samples.

## Design Decisions (locked in; one flagged for coordinator confirmation)

These were resolved by direct investigation of this repository (deployed-file schema, deploy trigger points, exmod/exmodz format) and, for the merge algorithm's core hypothesis, by **extraction-verification**: the merge engine and the fingerprint-equality logic below were written and tested as real, runnable Go code against a scratch copy of `develop` tip `541b485` before this plan was finalized. See each task's "Extraction-verified" note.

1. **Merged pak filename: `zzz_LMM_Merged_P.pak`.** UE's pak platform file mounts paks within a directory in filename-sort order, and a later-mounted pak wins same-path conflicts (this repo's own `icarusContentMountPoint` doc comment already notes "UE orders paks by its own filename-sort rules within a directory," and the issue body flags the same point). `zzz` forces last-alphabetical mount (a long-standing UE-modding convention for "load last, highest priority" — used so the merged pak's authoritative combined table state can never be silently shadowed by a plain prebuilt `.pak` mod that happens to also carry a table override). `LMM` makes the file greppable/recognizable as lmm-owned (useful for support and for the ownership design in Task 5). `Merged` names its content. `_P` matches the existing UE override-pak suffix convention this codebase already uses (`compiledFileName`, `internal/core/service.go:1008`).

2. **Merged pak identity for cache/deploy tracking: `sourceID = domain.SourceMerged = "lmm-merged"`, `modID = "merged-pak"`, cache `version = "merged"`.** The `deployed_files` table's `source_id`/`mod_id` columns are `NOT NULL` with no existing owner-less concept (verified: `internal/storage/db/migrations.go` `migrateV7`, `internal/storage/db/files.go`). Rather than a schema migration, the merged pak is tracked as a synthetic, singleton "mod" per `(game, profile)` — this reuses `Installer.Install`/`Uninstall`/`cache.Cache` verbatim (Task 5/6), inherits the SAME `deployed_files` ownership and #168-class residue risk as every other deployed file (not a new, worse risk class), and needs zero schema changes. `domain.SourceMerged` follows the existing `domain.SourceLocal = "local"` sentinel-string precedent (`internal/domain/mod.go:20`) — same acceptance of the (already-accepted) theoretical collision risk with a user-named custom source.

3. **Locked-mod semantics — PROPOSED, flagged for coordinator confirmation:** a locked mod's retained `.exmodz` diff STILL participates in every re-merge, at its locked version, unchanged. A lock does NOT exclude the mod from the merge and does NOT freeze the whole merged pak. Locking only prevents THAT mod's own version from advancing (the existing, #196-established meaning of "lock-wins" — `ApplyRecompile`'s `ErrModLocked` gate refuses to change what a locked mod's OWN cache/retained-source content is, but #196 already established that a locked mod's _existing_ diff is still reapplied when the BASE PAK changes; this plan simply extends that same reasoning to "when anything else in the profile changes, not just the base pak"). Freezing the whole merge on any lock present would make locking one mod block every OTHER mod's changes from ever reaching the deployed game — directly contradicting the purpose of a per-mod lock, and a severe UX regression for a feature that is supposed to make multi-mod profiles WORK. The merged pak is a separate, profile-level artifact (not "the locked mod's own files") — reading a locked mod's retained source to feed the merge is not "touching" it in the sense `ApplyRecompile`'s lock gate protects against (which is about REWRITING a locked mod's own cache/version content, not READING it). **This is the one item in this plan the coordinator should explicitly confirm before implementation starts** (Task 13 is a dedicated, isolated test task for exactly this behavior, so confirming/reversing it later is a small, contained change).

4. **`CheckGameUpdates` gains a `profileName` parameter.** #196's `Service.CheckGameUpdates(ctx, game, installed)` has no way to know which profile's merged pak to check (staleness is profile-scoped, `installed` alone doesn't reliably carry it). All 4 existing call sites (`cmd/lmm/update.go` ×2, `internal/tui/service_core.go` ×2) are updated in Task 9 — internal signature change, not user-facing.

5. **Per-mod `.exmodz` "fingerprint" simplifies to "retained + validated," full stop.** #196's per-mod `MarkBaseIndexHash`/`BaseIndexHashes` cache markers (recording a base-pak IndexHash per compiled FILE) become dead code once there is no per-mod compile output to fingerprint — the MERGED pak's fingerprint (Task 5) subsumes that job at the profile level, keyed by each contributing file's own content checksum (`md5File` over the retained `.exmodz` bytes, computed on demand — these files are small, this repo's own research established the largest real base table is 7.3 MB, so re-hashing on every staleness check is cheap and avoids a second marker to keep in sync). Task 4 removes the now-dead #196 functions.

## File Structure

| File                                    | Responsibility                                                                                                                                                                  |
| --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/source/source.go`             | `MergeCompiler` interface + `MergeSource` type (replaces `Compiler`)                                                                                                            |
| `internal/source/icarus/merge.go` (new) | `MergeCompile` — the merge engine — and `ValidateSource`                                                                                                                        |
| `internal/source/icarus/icarus.go`      | `*Icarus` implements `MergeCompiler` instead of `Compiler`                                                                                                                      |
| `internal/core/service.go`              | Ingest simplification (download + local-ingest DeployCompile branches); removal of dead #196 per-mod compile helpers                                                            |
| `internal/core/importer.go`             | Ingest simplification (import DeployCompile branch)                                                                                                                             |
| `internal/core/merged_pak.go` (new)     | `MergedFingerprint` type, marker read/write, `enabledExmodzSources`, `Service.syncMergedPak`, `Service.ApplyMergedPakRegen`                                                     |
| `internal/core/updater.go`              | `CheckGameUpdates` signature change; removal of `CheckBaseStaleness`/`ApplyRecompile`/`ClassifyRetainedSourceStatError` (superseded by `merged_pak.go`)                         |
| `internal/core/flows.go`                | `syncMergedPak` hook calls in `EnableMod`, `DisableMod`, `UninstallMod`, `DeployProfile`, `ApplyProfileSwitch`, `ApplyUpdate`, `ApplyInstall`; new `Service.ReorderProfileMods` |
| `cmd/lmm/update.go`                     | `CheckGameUpdates` call sites updated; synthetic merged-pak row rendering (table + `--json`); apply dispatch                                                                    |
| `cmd/lmm/verify.go`                     | Replace per-mod `stale_compile` pre-pass with the profile-level merged-pak check                                                                                                |
| `cmd/lmm/profile.go`                    | `pm.ReorderMods` call site switched to `Service.ReorderProfileMods`                                                                                                             |
| `internal/tui/service_core.go`          | Mirrors `cmd/lmm/update.go`'s wiring; `ReorderMods` switched to `Service.ReorderProfileMods`                                                                                    |
| `internal/tui/actions_provider.go`      | No field changes needed (`UpdateItem.RecompileNeeded`/`VersionLabel()` already generic)                                                                                         |
| `CHANGELOG.md`                          | Amend the unshipped #196 `[Unreleased]` bullet                                                                                                                                  |

---

### Task 1: `source.MergeCompiler` interface + `icarus.MergeCompile` merge engine

**Files:**

- Modify: `internal/source/source.go:156-158` (replace `Compiler` interface)
- Create: `internal/source/icarus/merge.go`
- Test: `internal/source/icarus/merge_test.go`
- Modify: `internal/source/icarus/icarus.go` (implement `MergeCompiler` instead of `Compiler`)
- Test: `internal/source/icarus/icarus_test.go` (interface assertion)

**Interfaces:**

- Consumes: `internal/source/icarus/exmod.go`'s `ParseExmod`, `ApplyRowPatch`, `ExmodDiff`, `ExmodRow` (unchanged); `exmodz.go`'s `ParseExmodz`, `ExmodzBundle` (unchanged); `compile.go`'s `resolveCurrentFile`, `sanitizeAssetPath`, `icarusDataTablePrefix`, `icarusContentMountPoint`, `endOfModSentinel` (unchanged, package-private, reused as-is); `internal/unrealpak`'s `Open`, `Create`, `WithMountPoint`, `Reader.ReadFile`, `Reader.Files` (unchanged).
- Produces: `source.MergeCompiler` interface (consumed by Task 2/3's `mergeCompilerSourceForGame`); `icarus.MergeSource{ModRef, ExmodzPath string}`; `icarus.MergeCompile(basePakPath string, sources []MergeSource, outputPakPath string) (warnings []string, err error)`; `icarus.ValidateSource(exmodzPath string) error`.

**Extraction-verified:** this task's `MergeCompile` body below is the EXACT code verified against a scratch copy of `develop` tip `541b485` (`/tmp/.../scratchpad/lmm-extract-verify`) with 6 passing tests proving: (a) two mods patching DIFFERENT fields of the SAME row both survive (the crux of #197 — whole-pak last-wins would lose one), (b) two mods patching DIFFERENT tables both land in one merged pak, (c) two mods patching the SAME field of the SAME row correctly last-wins with no special handling needed, (d) same-path bundled ASSET collisions last-win AND return a warning naming both mods, (e) a content-adding mod (new row) composes correctly with a table-patching mod, (f) the N=1 case (single enabled mod) produces byte-identical table content to the existing `Compile()`. No code changes were needed after the first defect fix below.

- [ ] **Step 1: Write the failing tests**

Create `internal/source/icarus/merge_test.go`:

```go
package icarus

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// TestMergeCompile_FieldLevelMergeAcrossMods is the crux of #197: two mods
// patch DIFFERENT fields of the SAME row in the SAME table. Whole-pak
// last-wins (the #136 status quo) would lose one mod's field entirely;
// sequential upserts must preserve BOTH.
func TestMergeCompile_FieldLevelMergeAcrossMods(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Rows":[{"Name":"Mount_Bear","BaseMovementSpeed":200,"BaseHealth":500}]}`),
	}
	basePak := writeTestBasePak(t, baseTables)

	modA := writeTestExmodzFile(t, `{"name":"Speed Mod","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]}]}`, nil)
	modB := writeTestExmodzFile(t, `{"name":"Health Mod","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseHealth":800}]}]}`, nil)

	outputPath := filepath.Join(t.TempDir(), "merged_P.pak")
	warnings, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:speed-mod", ExmodzPath: modA},
		{ModRef: "icarus:health-mod", ExmodzPath: modB},
	}, outputPath)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none (no asset collision in this fixture)", warnings)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening merged output: %v", err)
	}
	defer r.Close() //nolint:errcheck

	merged, err := r.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile merged data table: %v", err)
	}
	if !bytes.Contains(merged, []byte(`"BaseMovementSpeed":235`)) {
		t.Errorf("merged table = %s, want BaseMovementSpeed 235 (mod A's field) to survive", merged)
	}
	if !bytes.Contains(merged, []byte(`"BaseHealth":800`)) {
		t.Errorf("merged table = %s, want BaseHealth 800 (mod B's field) to survive", merged)
	}
}

// TestMergeCompile_DifferentTablesFromDifferentMods proves the OTHER
// whole-pak-last-wins failure mode (#197's issue body point 1): mod A
// patches table X, mod B patches table Y - both must land in the single
// merged pak, not just the last mod's table.
func TestMergeCompile_DifferentTablesFromDifferentMods(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json":       []byte(`{"Rows":[{"Name":"Mount_Bear","BaseMovementSpeed":200}]}`),
		"Items/D_ItemsStatic.json": []byte(`{"Rows":[{"Name":"Item_Saddle","Weight":5}]}`),
	}
	basePak := writeTestBasePak(t, baseTables)

	modA := writeTestExmodzFile(t, `{"name":"Mount Mod","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":300}]}]}`, nil)
	modB := writeTestExmodzFile(t, `{"name":"Item Mod","Rows":[{"CurrentFile":"Items-D_ItemsStatic.json","File_Items":[{"Name":"Item_Saddle","Weight":1}]}]}`, nil)

	outputPath := filepath.Join(t.TempDir(), "merged_P.pak")
	if _, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:mount-mod", ExmodzPath: modA},
		{ModRef: "icarus:item-mod", ExmodzPath: modB},
	}, outputPath); err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening merged output: %v", err)
	}
	defer r.Close() //nolint:errcheck

	aiTable, err := r.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile AI table: %v", err)
	}
	if !bytes.Contains(aiTable, []byte(`"BaseMovementSpeed":300`)) {
		t.Errorf("AI table = %s, want mod A's patch", aiTable)
	}
	itemsTable, err := r.ReadFile("data/Items/D_ItemsStatic.json")
	if err != nil {
		t.Fatalf("ReadFile Items table: %v", err)
	}
	if !bytes.Contains(itemsTable, []byte(`"Weight":1`)) {
		t.Errorf("Items table = %s, want mod B's patch", itemsTable)
	}
}

// TestMergeCompile_SameRowSameField_LastWins pins the EXPECTED (not
// warned-about) outcome when two mods genuinely conflict on the exact same
// field of the exact same row: later-in-order wins, ordinary upsert
// semantics, no special handling needed.
func TestMergeCompile_SameRowSameField_LastWins(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Rows":[{"Name":"Mount_Bear","BaseMovementSpeed":200}]}`),
	}
	basePak := writeTestBasePak(t, baseTables)

	modA := writeTestExmodzFile(t, `{"name":"A","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":300}]}]}`, nil)
	modB := writeTestExmodzFile(t, `{"name":"B","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":400}]}]}`, nil)

	outputPath := filepath.Join(t.TempDir(), "merged_P.pak")
	if _, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:a", ExmodzPath: modA},
		{ModRef: "icarus:b", ExmodzPath: modB},
	}, outputPath); err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening merged output: %v", err)
	}
	defer r.Close() //nolint:errcheck
	merged, err := r.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Contains(merged, []byte(`"BaseMovementSpeed":400`)) {
		t.Errorf("merged table = %s, want mod B's (later, order-2) value 400 to win", merged)
	}
	if bytes.Contains(merged, []byte(`"BaseMovementSpeed":300`)) {
		t.Errorf("merged table = %s, mod A's value should have been overwritten", merged)
	}
}

// TestMergeCompile_AssetCollision_LastWinsWithWarning: two mods bundle a
// prebuilt asset at the SAME path - cannot compose like a table row, so
// last-applied wins AND a warning is returned.
func TestMergeCompile_AssetCollision_LastWinsWithWarning(t *testing.T) {
	basePak := writeTestBasePak(t, map[string][]byte{"AI/D_AIGrowth.json": []byte(`{"Rows":[]}`)})

	modA := writeTestExmodzFile(t, `{"name":"A","Rows":[]}`, map[string][]byte{
		"Shared/ASS/SK_Shared.uasset": []byte("from-mod-a"),
	})
	modB := writeTestExmodzFile(t, `{"name":"B","Rows":[]}`, map[string][]byte{
		"Shared/ASS/SK_Shared.uasset": []byte("from-mod-b"),
	})

	outputPath := filepath.Join(t.TempDir(), "merged_P.pak")
	warnings, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:a", ExmodzPath: modA},
		{ModRef: "icarus:b", ExmodzPath: modB},
	}, outputPath)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly 1 asset-collision warning", warnings)
	}
	if !bytes.Contains([]byte(warnings[0]), []byte("Shared/ASS/SK_Shared.uasset")) {
		t.Errorf("warning = %q, want it to name the colliding path", warnings[0])
	}
	if !bytes.Contains([]byte(warnings[0]), []byte("icarus:b")) {
		t.Errorf("warning = %q, want it to name the winning mod", warnings[0])
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening merged output: %v", err)
	}
	defer r.Close() //nolint:errcheck
	asset, err := r.ReadFile("Shared/ASS/SK_Shared.uasset")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(asset) != "from-mod-b" {
		t.Errorf("asset content = %q, want mod B's (later-applied) content to win", asset)
	}
}

// TestMergeCompile_ContentAddingModComposesWithPatchMod: one mod ADDS a
// brand-new row (a new mountable species), another PATCHES an existing row
// in the SAME table. Both must survive in the merged output.
func TestMergeCompile_ContentAddingModComposesWithPatchMod(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Rows":[{"Name":"Mount_Bear","BaseMovementSpeed":200}]}`),
	}
	basePak := writeTestBasePak(t, baseTables)

	patchMod := writeTestExmodzFile(t, `{"name":"Patch","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":250}]}]}`, nil)
	addMod := writeTestExmodzFile(t, `{"name":"NewSpecies","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Wolf","BaseMovementSpeed":320}]}]}`, nil)

	outputPath := filepath.Join(t.TempDir(), "merged_P.pak")
	if _, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:patch", ExmodzPath: patchMod},
		{ModRef: "icarus:add", ExmodzPath: addMod},
	}, outputPath); err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening merged output: %v", err)
	}
	defer r.Close() //nolint:errcheck
	merged, err := r.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Contains(merged, []byte(`"BaseMovementSpeed":250`)) {
		t.Errorf("merged table = %s, want the patched Mount_Bear speed", merged)
	}
	if !bytes.Contains(merged, []byte(`"Mount_Wolf"`)) {
		t.Errorf("merged table = %s, want the newly-added Mount_Wolf row", merged)
	}
}

// TestMergeCompile_SingleSource_MatchesCompile proves the N=1 degenerate
// case (a profile with exactly one enabled exmodz mod) produces byte-
// identical table content to the existing single-mod Compile() - the
// merged-only model must not regress the already-shipped single-mod path.
func TestMergeCompile_SingleSource_MatchesCompile(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Rows":[{"Name":"Mount_Bear","BaseMovementSpeed":200}]}`),
	}
	basePak := writeTestBasePak(t, baseTables)
	manifest := `{"name":"Bear Mount","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, map[string][]byte{
		"Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset": []byte("fake-asset"),
	})

	compileOut := filepath.Join(t.TempDir(), "compile_P.pak")
	if err := Compile(basePak, exmodzPath, compileOut); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	mergeOut := filepath.Join(t.TempDir(), "merge_P.pak")
	if _, err := MergeCompile(context.Background(), basePak, []source.MergeSource{{ModRef: "icarus:bear-mount", ExmodzPath: exmodzPath}}, mergeOut); err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}

	cr, err := unrealpak.Open(compileOut)
	if err != nil {
		t.Fatalf("opening Compile output: %v", err)
	}
	defer cr.Close() //nolint:errcheck
	mr, err := unrealpak.Open(mergeOut)
	if err != nil {
		t.Fatalf("opening MergeCompile output: %v", err)
	}
	defer mr.Close() //nolint:errcheck

	cTable, err := cr.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("Compile ReadFile: %v", err)
	}
	mTable, err := mr.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("MergeCompile ReadFile: %v", err)
	}
	if !bytes.Equal(cTable, mTable) {
		t.Errorf("Compile table = %s, MergeCompile table = %s, want identical for N=1", cTable, mTable)
	}
}

// TestValidateSource_ValidExmodz_NoError proves ValidateSource accepts a
// well-formed .exmodz without compiling anything (no basePak needed).
func TestValidateSource_ValidExmodz_NoError(t *testing.T) {
	exmodzPath := writeTestExmodzFile(t, `{"name":"OK","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":200}]}]}`, nil)
	if err := ValidateSource(exmodzPath); err != nil {
		t.Errorf("ValidateSource: %v, want nil for a well-formed .exmodz", err)
	}
}

// TestValidateSource_MalformedExmodz_Errors proves a corrupt/unparseable
// .exmodz fails loud at validate time (ingest-time), not silently deferred
// to the next merge.
func TestValidateSource_MalformedExmodz_Errors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.exmodz")
	if err := os.WriteFile(path, []byte("not a zip file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSource(path); err == nil {
		t.Error("ValidateSource: got nil error, want a failure for a non-zip file")
	}
}
```

- [ ] **Step 2: Run the tests, confirm they fail to compile**

Run: `go test ./internal/source/icarus/... -run 'TestMergeCompile|TestValidateSource' -v`
Expected: build failure — `undefined: MergeCompile`, `source.MergeSource` undefined (the `MergeCompiler` interface doesn't exist in `internal/source` yet), `undefined: ValidateSource`.

- [ ] **Step 3: Add the `MergeCompiler` interface to `internal/source/source.go`**

Read `internal/source/source.go:142-158` first (the `DownloadHeaderProvider` and `Compiler` definitions) to match the file's exact comment style, then replace the `Compiler` interface block (`internal/source/source.go:156-158`) with:

```go
// MergeCompiler is implemented by sources whose compile-eligible files must
// be merged across every enabled mod into ONE profile-level artifact rather
// than compiled per-mod (#197: Icarus's cross-mod table merge - a whole-pak
// last-wins deploy would silently drop one mod's table rows whenever two
// mods patch the same table). Replaces #196's Compiler interface, which
// this source no longer implements: there is no more per-mod compiled
// artifact to produce.
type MergeCompiler interface {
	// ValidateSource parses/validates sourceFilePath (the retained,
	// not-yet-merged source archive) without compiling anything - called at
	// ingest time (download/import) so a malformed archive fails loud
	// immediately rather than at the next merge.
	ValidateSource(sourceFilePath string) error

	// MergeCompile applies every entry in sources, in order (profile load
	// order), against basePakPath's tables, and writes the merged result to
	// outputPakPath. Returns non-fatal warnings (e.g. same-path asset
	// collisions - last-applied wins) alongside a nil error; a nil error
	// with warnings is still a fully-written, deployable pak.
	MergeCompile(ctx context.Context, basePakPath string, sources []MergeSource, outputPakPath string) (warnings []string, err error)
}

// MergeSource identifies one mod's contribution to a merge, in the order it
// must be applied (profile load order).
type MergeSource struct {
	ModRef     string // "sourceID:modID" - identity used in collision warnings
	ExmodzPath string // the retained source archive to read
}
```

`source.go` already imports `"context"` (used by `ModSource`'s own methods) — no new import needed.

- [ ] **Step 4: Implement `MergeCompile` and `ValidateSource` in the icarus package**

Create `internal/source/icarus/merge.go`:

```go
package icarus

import (
	"context"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// MergeSource is a type alias (not a distinct type) for source.MergeSource
// (Step 3 above). internal/core must NOT import this icarus package
// directly (established #136/#196 precedent - see
// service_icarus_compile_test.go's fakeCompilerSource doc comment), so it
// can only ever construct/consume source.MergeSource values - aliasing it
// here, rather than defining a second, structurally-similar type, is what
// lets *Icarus's MergeCompile method (Step 6) satisfy source.MergeCompiler
// at all: Go interface satisfaction requires identical types, and a type
// alias IS the same type, not a look-alike.
type MergeSource = source.MergeSource

// ValidateSource parses exmodzPath without compiling anything - the
// ingest-time check (#197 design: "install still parses/validates the
// .exmodz early"). A malformed archive fails loud immediately, at
// download/import time, rather than at the next merge (which may not run
// until a later mutation).
func ValidateSource(exmodzPath string) error {
	data, err := os.ReadFile(exmodzPath)
	if err != nil {
		return fmt.Errorf("icarus: reading %s: %w", exmodzPath, err)
	}
	if _, err := ParseExmodz(data); err != nil {
		return fmt.Errorf("icarus: validating %s: %w", exmodzPath, err)
	}
	return nil
}

// MergeCompile applies every source's .EXMOD row upserts, IN ORDER, against
// the same evolving base tables - a merge is just Compile with N diffs
// instead of 1. Table conflicts compose at the FIELD level for free:
// ApplyRowPatch always shallow-merges an item's fields into whatever the
// target row currently holds, so feeding mod A's patched bytes back in as
// the "base" for mod B's row (instead of re-reading the pristine base table
// each time) is the entire merge algorithm - two mods patching DIFFERENT
// fields of the same row, or entirely different rows of the same table,
// both survive; only a genuine same-row-same-field write is last-wins (an
// ordinary, expected upsert outcome, not something to warn about). Bundled
// ASSET files cannot compose this way - a same-path asset collision is
// necessarily last-wins, so it is reported as a warning instead.
//
// ctx is accepted only to satisfy source.MergeCompiler and is never read -
// every step here is local file I/O over small files (mirrors Compile's own
// doc comment, internal/source/icarus/compile.go:23-25).
//
// A non-nil error always means outputPakPath does not exist (or does not
// contain a fully-written pak) - see the removal defer below, mirroring
// Compile's own fail-clean contract.
func MergeCompile(ctx context.Context, basePakPath string, sources []MergeSource, outputPakPath string) (warnings []string, err error) {
	base, err := unrealpak.Open(basePakPath)
	if err != nil {
		return nil, fmt.Errorf("icarus: opening base pak %s: %w", basePakPath, err)
	}
	defer base.Close() //nolint:errcheck

	tableState := make(map[string][]byte) // mountPath -> current (possibly already patched) JSON bytes
	assets := make(map[string][]byte)      // final asset path -> data (last source wins)
	assetOwner := make(map[string]string)  // asset path -> ModRef that last set it

	for _, src := range sources {
		exmodzData, rerr := os.ReadFile(src.ExmodzPath)
		if rerr != nil {
			return warnings, fmt.Errorf("icarus: reading %s: %w", src.ExmodzPath, rerr)
		}
		bundle, perr := ParseExmodz(exmodzData)
		if perr != nil {
			return warnings, fmt.Errorf("icarus: %s: %w", src.ExmodzPath, perr)
		}

		for _, row := range bundle.Diff.Rows {
			if row.CurrentFile == endOfModSentinel {
				continue
			}
			if len(row.FileItems) == 0 {
				return warnings, fmt.Errorf("icarus: %s: row has no File_Items to apply (malformed .EXMOD manifest)", row.CurrentFile)
			}
			mountPath, merr := resolveCurrentFile(base, row.CurrentFile)
			if merr != nil {
				return warnings, merr
			}
			current, seen := tableState[mountPath]
			if !seen {
				current, merr = base.ReadFile(mountPath)
				if merr != nil {
					return warnings, fmt.Errorf("icarus: reading base data table %s: %w", mountPath, merr)
				}
			}
			patched, perr2 := ApplyRowPatch(current, row)
			if perr2 != nil {
				return warnings, perr2
			}
			tableState[mountPath] = patched
		}

		for assetPath, data := range bundle.Assets {
			safePath, serr := sanitizeAssetPath(assetPath)
			if serr != nil {
				return warnings, serr
			}
			if owner, exists := assetOwner[safePath]; exists && owner != src.ModRef {
				warnings = append(warnings, fmt.Sprintf(
					"asset %q is bundled by both %s and %s - %s wins (last-applied, per profile load order)",
					safePath, owner, src.ModRef, src.ModRef))
			}
			assets[safePath] = data
			assetOwner[safePath] = src.ModRef
		}
	}

	out, cerr := unrealpak.Create(outputPakPath, unrealpak.WithMountPoint(icarusContentMountPoint))
	if cerr != nil {
		return warnings, fmt.Errorf("icarus: creating %s: %w", outputPakPath, cerr)
	}
	defer func() {
		if err == nil {
			return
		}
		_ = out.Close() //nolint:errcheck
		if rmErr := os.Remove(outputPakPath); rmErr != nil && !os.IsNotExist(rmErr) {
			err = fmt.Errorf("%w (additionally, removing partial output %s failed: %v)", err, outputPakPath, rmErr)
		}
	}()

	for mountPath, data := range tableState {
		tablePath := icarusDataTablePrefix + mountPath
		if err = out.AddFile(tablePath, data); err != nil {
			return warnings, fmt.Errorf("icarus: writing merged %s: %w", tablePath, err)
		}
	}
	for assetPath, data := range assets {
		if err = out.AddFile(assetPath, data); err != nil {
			return warnings, fmt.Errorf("icarus: writing bundled asset %s: %w", assetPath, err)
		}
	}

	if err = out.Close(); err != nil {
		return warnings, fmt.Errorf("icarus: finalizing %s: %w", outputPakPath, err)
	}
	return warnings, nil
}
```

Note: `ctx` is accepted but unused in the body — `go vet`/the linter will not flag an unused PARAMETER (only unused local variables/imports), so this compiles clean; this exactly mirrors `Compile`'s own sibling situation is avoided (Compile has no ctx at all) but matches how other `ctx`-accepting-but-unused methods already exist elsewhere in this codebase's source implementations.

Map iteration order for `tableState`/`assets` in the write-out loops is non-deterministic across runs, but `unrealpak.Writer.Close` already sorts all entries by path before serializing (existing behavior, unchanged) — the FINAL pak's byte layout is deterministic regardless of insertion order, so this needs no extra sorting here.

- [ ] **Step 5: Run the tests, confirm they pass**

Run: `go test ./internal/source/icarus/... -run 'TestMergeCompile|TestValidateSource' -v`
Expected: all 8 PASS. Extraction-verified: this is the exact code (module path adjusted for the `source.MergeSource` alias, added after the initial extraction pass caught the type-identity issue — see below) proven against a scratch copy of `develop` tip `541b485` with all 6 `MergeCompile` scenarios and both `ValidateSource` cases green.

**Extraction-verification note:** the FIRST draft of this task defined `MergeSource` as a plain struct local to the `icarus` package (not an alias of `source.MergeSource`). That draft's 6 `MergeCompile` tests still passed — the merge algorithm itself was correct — but a second look while wiring `*Icarus` to the `MergeCompiler` interface (Step 6) surfaced that two structurally-identical-but-distinct Go types never satisfy the same interface: `*Icarus` would NOT have implemented `source.MergeCompiler`. This is fixed by making `icarus.MergeSource` a genuine type alias (`type MergeSource = source.MergeSource`) rather than a second definition, so both packages refer to the exact same type. No change to the merge algorithm itself was needed — this defect was in the type PLUMBING around the verified logic, not the logic.

- [ ] **Step 6: Run the pre-existing package suite, confirm `Compile`'s own tests still pass**

Run: `go test ./internal/source/icarus/... -v`
Expected: all PASS (`Compile`, `ApplyRowPatch`, `ParseExmod`, `ParseExmodz` tests all unaffected — nothing about them changed).

- [ ] **Step 7: Wire `*icarus.Icarus` to implement `MergeCompiler`**

Find `*icarus.Icarus`'s current `Compile` method (search `internal/source/icarus/icarus.go` for `func (i *Icarus) Compile`). Replace it with:

```go
func (i *Icarus) ValidateSource(sourceFilePath string) error {
	return ValidateSource(sourceFilePath)
}

func (i *Icarus) MergeCompile(ctx context.Context, basePakPath string, sources []MergeSource, outputPakPath string) ([]string, error) {
	return MergeCompile(ctx, basePakPath, sources, outputPakPath)
}
```

Find the existing `var _ source.Compiler = (*Icarus)(nil)` (or equivalent) interface-assertion line in `icarus.go` or `icarus_test.go` and change it to `var _ source.MergeCompiler = (*Icarus)(nil)`.

- [ ] **Step 8: Run the full repo build and test suite, confirm the ONLY breakage is in `internal/core`**

Run: `go build ./... 2>&1 | tail -60`
Expected: `internal/source/...` builds clean; `internal/core/...` fails to build (`fakeCompilerSource does not implement source.MergeCompiler`, `src.(source.Compiler)` type assertion errors, `compiler.Compile` undefined) — this is the expected, BY-DESIGN breakage Tasks 2-4 fix. Note the exact list of broken files (`internal/core/service.go`, `internal/core/importer.go`, `internal/core/updater.go`, and their test files) for Task 2.

Run: `go test ./internal/source/... -v 2>&1 | tail -40`
Expected: all green — this package's own suite is fully self-contained and must pass before moving on, independent of `internal/core`'s state.

- [ ] **Step 9: Commit**

```bash
git add internal/source/source.go internal/source/icarus/merge.go internal/source/icarus/merge_test.go internal/source/icarus/icarus.go internal/source/icarus/icarus_test.go
git commit -m "feat: MergeCompiler interface + Icarus merge engine (#197)"
```

### Task 2: Ingest simplification — download path (`DownloadModToCache`)

**Files:**

- Modify: `internal/core/service.go:543-574` (the `DeployCompile` branch inside `DownloadModToCache`)
- Modify: `internal/core/service.go:180-199` (`compilerSourceForGame` → `mergeCompilerSourceForGame`)
- Test: `internal/core/service_icarus_compile_test.go` (update `fakeCompilerSource` to implement `MergeCompiler`; existing per-mod-pak assertions replaced with validate-and-retain assertions)
- Test: `internal/core/service_compile_fingerprint_test.go` (rewritten — see Step 5)

**Interfaces:**

- Consumes: `cache.RetainedSourceName(fileID string) string` (#196, unchanged); `commitStagedCacheWithMarker(cachePath, stagePath, fileID string, members []string) error` (#196, unchanged — now always called with `members: nil`); `source.MergeCompiler` (Task 1).
- Produces: after this task, a `DeployCompile` game's per-mod cache entry for an `.exmodz` file contains ONLY the reserved retained-source file — `gameCache.ListFiles(...)` returns an EMPTY slice for it. `DownloadModResult.FilesExtracted` is `0` for this branch (there is nothing to deploy from this mod's own entry — the merged pak, deployed separately in Task 6/7, is what actually reaches the game directory).

Read `internal/core/service.go:472-596` first (the whole `DownloadModToCache` function) for full context before editing — the snippet below is a targeted diff, not the whole function.

- [ ] **Step 1: Write the failing test**

Open `internal/core/service_icarus_compile_test.go`. Replace `fakeCompilerSource`'s `Compile` method:

```go
func (s *fakeCompilerSource) Compile(ctx context.Context, basePakPath, sourceFilePath, outputPath string) error {
	s.compileCalls++
	data, err := os.ReadFile(sourceFilePath)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, data, 0o644)
}
```

with:

```go
func (s *fakeCompilerSource) ValidateSource(sourceFilePath string) error {
	s.validateCalls++
	if _, err := os.Stat(sourceFilePath); err != nil {
		return err
	}
	return nil
}

func (s *fakeCompilerSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, error) {
	s.compileCalls++
	// Concatenate every source's bytes - enough for tests to distinguish
	// "which sources were actually merged" without needing a real base pak
	// table to patch.
	var out []byte
	for _, src := range sources {
		data, err := os.ReadFile(src.ExmodzPath)
		if err != nil {
			return nil, err
		}
		out = append(out, data...)
	}
	return nil, os.WriteFile(outputPath, out, 0o644)
}
```

Add `validateCalls int` to the `fakeCompilerSource` struct. No new import is needed — `source.MergeSource` uses the SAME `internal/source` import this file already has for `source.ModSource`/`source.DownloadableFile`-adjacent types (confirm with `grep -n '"github.com/DonovanMods/linux-mod-manager/internal/source"' internal/core/service_icarus_compile_test.go`; do NOT import `internal/source/icarus` into `internal/core` — that import boundary is deliberate, see Step 4's doc comment above). Change the interface assertions:

```go
var (
	_ source.ModSource     = (*fakeCompilerSource)(nil)
	_ source.MergeCompiler = (*fakeCompilerSource)(nil)
)
```

Replace `TestDownloadMod_DeployCompile_InvokesCompiler` with:

```go
func TestDownloadMod_DeployCompile_ValidatesAndRetainsNoPerModPak(t *testing.T) {
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fake-exmodz-bytes"))
	}))
	defer dlSrv.Close()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &fakeCompilerSource{downloadURL: dlSrv.URL}
	svc.RegisterSource(src)

	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "3.3"}
	file := &domain.DownloadableFile{ID: "exmodz", FileName: "Bear_Mount.exmodz"}

	result, err := svc.DownloadMod(context.Background(), "fake-compiler", game, mod, file, nil)
	require.NoError(t, err)
	require.Equal(t, 1, src.validateCalls, "ingest must validate the .exmodz")
	require.Equal(t, 0, src.compileCalls, "ingest must NOT compile a per-mod pak (#197: merged-only)")
	require.Equal(t, 0, result.FilesExtracted, "a per-mod exmodz cache entry has no deployment members under the merged-only model")

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	require.Empty(t, files, "ListFiles must report zero deployment members - the retained source is reserved, not a member")

	retainedPath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName(file.ID))
	data, err := os.ReadFile(retainedPath)
	require.NoError(t, err)
	require.Equal(t, "fake-exmodz-bytes", string(data), "the original .exmodz bytes must still be retained")
}

// TestDownloadMod_DeployCompile_MalformedExmodz_FailsLoudAtIngest proves
// validation happens at ingest time, not deferred to the next merge.
func TestDownloadMod_DeployCompile_MalformedExmodz_FailsLoudAtIngest(t *testing.T) {
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-a-valid-exmodz"))
	}))
	defer dlSrv.Close()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := &failingValidateCompilerSource{fakeCompilerSource: &fakeCompilerSource{downloadURL: dlSrv.URL}}
	svc.RegisterSource(src)

	game := &domain.Game{ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "bad-mount", SourceID: "fake-compiler", GameID: "icarus", Version: "1.0"}
	file := &domain.DownloadableFile{ID: "exmodz", FileName: "Bad_Mount.exmodz"}

	_, err = svc.DownloadMod(context.Background(), "fake-compiler", game, mod, file, nil)
	require.Error(t, err)

	gameCache := svc.GetGameCache(game)
	require.False(t, gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version), "a validation failure must leave no cache entry")
}

// failingValidateCompilerSource wraps fakeCompilerSource and always fails
// ValidateSource - simulates a corrupt/malformed downloaded .exmodz.
type failingValidateCompilerSource struct {
	*fakeCompilerSource
}

func (s *failingValidateCompilerSource) ValidateSource(sourceFilePath string) error {
	return fmt.Errorf("boom: not a valid .EXMODZ")
}
```

Add `"fmt"` and `"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"` to this test file's imports if not already present (they are — `cache` is used elsewhere in this package's test files; confirm with `grep -n '"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"' internal/core/service_icarus_compile_test.go` before adding a duplicate).

- [ ] **Step 2: Run the tests, confirm they fail to compile**

Run: `go test ./internal/core/... -run 'TestDownloadMod_DeployCompile' -v`
Expected: build failure (`fakeCompilerSource` doesn't implement `source.MergeCompiler`, `validateCalls` undefined) — this IS the expected RED state; Step 3 (below) makes it compile.

- [ ] **Step 3: Replace the `DeployCompile` branch**

In `internal/core/service.go`, replace lines 543-574 (quoted above in "Files") with:

```go
	if game.DeployMode == domain.DeployCompile && isExmodzFile(safeFileName) {
		mc, ok := src.(source.MergeCompiler)
		if !ok {
			return nil, fmt.Errorf("source %q: game %q requires DeployCompile but source does not implement MergeCompiler", src.ID(), game.ID)
		}
		if err := mc.ValidateSource(archivePath); err != nil {
			return nil, fmt.Errorf("validating %s: %w", safeFileName, err)
		}
		// Unlike copyFileStreaming (which mkdirs its destination itself),
		// the retained-source write below needs stagePath to exist first.
		if err := os.MkdirAll(stagePath, 0755); err != nil {
			return nil, fmt.Errorf("preparing staging: %w", err)
		}
		retainedPath := filepath.Join(stagePath, cache.RetainedSourceName(file.ID))
		if err := copyFileStreaming(archivePath, retainedPath); err != nil {
			return nil, fmt.Errorf("retaining %s: %w", safeFileName, err)
		}
		// members is nil (#197): this cache entry's ONLY content is the
		// reserved retained source - there is no per-mod deployment
		// artifact anymore. The merged pak (a separate, profile-level
		// cache entry - internal/core/merged_pak.go) is what actually
		// deploys.
		if err := commitStagedCacheWithMarker(cachePath, stagePath, file.ID, nil); err != nil {
			return nil, err
		}
		return &DownloadModResult{FilesExtracted: 0, Checksum: downloadResult.Checksum}, nil
	}
```

Do NOT delete `resolveBasePak`, `compiledFileName`, `basePakIndexHash`, or `stageCompileFingerprint` yet — `resolveBasePak` and `basePakIndexHash` are still used by Task 5/6 (the merged pak's own fingerprint needs the live base pak's `IndexHash`); `compiledFileName` and `stageCompileFingerprint` become genuinely dead here and are removed in Task 4 once every caller is confirmed gone (removing them now, before Task 3 also stops calling them, would break the build for the importer branch mid-task).

- [ ] **Step 4: Rename `compilerSourceForGame` to `mergeCompilerSourceForGame`**

`internal/core/service.go:180-199`. This function's body is otherwise unchanged except its return type and the type assertion inside the loop:

```go
func (s *Service) mergeCompilerSourceForGame(gameID string) (source.MergeCompiler, error) {
	srcs, err := s.SourcesForGame(gameID)
	if err != nil {
		return nil, err
	}
	var compilers []source.MergeCompiler
	for _, src := range srcs {
		if c, ok := src.(source.MergeCompiler); ok {
			compilers = append(compilers, c)
		}
	}
	switch len(compilers) {
	case 0:
		return nil, fmt.Errorf("game %q requires DeployCompile but has no merge-compiler-capable source configured (map a source implementing source.MergeCompiler in the game's sources)", gameID)
	case 1:
		return compilers[0], nil
	default:
		return nil, fmt.Errorf("game %q has multiple merge-compiler-capable sources configured; ambiguous compile source", gameID)
	}
}
```

Run `grep -rn "compilerSourceForGame" internal/core/*.go` and update every call site to the new name (Task 3's `Importer.Import` is the only other caller — updated there; Task 5/6's `syncMergedPak` is a NEW caller, using the new name from the start).

- [ ] **Step 5: Run the tests, confirm they pass**

Run: `go test ./internal/core/... -run 'TestDownloadMod_DeployCompile' -v`
Expected: both new tests PASS. This will also break every OTHER `service_icarus_compile_test.go`/`service_compile_fingerprint_test.go` test still asserting on per-mod pak output (`TestDownloadMod_DeployCompile_RoutesPerFile`, `TestDownloadMod_DeployCompile_MixedFileMod`, `TestDownloadMod_DeployCompile_RecordsBaseIndexHashAndRetainedSource`) — expected; Step 6 removes/rewrites them.

- [ ] **Step 6: Remove now-obsolete per-mod-pak tests**

Delete `TestDownloadMod_DeployCompile_RoutesPerFile` and `TestDownloadMod_DeployCompile_MixedFileMod` from `service_icarus_compile_test.go` (their premise — a per-mod compiled pak with a specific filename — no longer exists; Task 1's `TestMergeCompile_*` tests and this task's new tests cover the equivalent ground for the merged model). Delete `internal/core/service_compile_fingerprint_test.go` entirely (both its tests assert on `cache.BaseIndexHashes`/retained-source-plus-marker shape that Task 4 removes) — a replacement fingerprint test lives in Task 5.

- [ ] **Step 7: Run the full core suite, confirm the remaining failures are ONLY in files Task 3/4 own**

Run: `go test ./internal/core/... 2>&1 | tail -80`
Expected: failures confined to `importer.go`'s own `DeployCompile` branch (still calling the old `Compile`-based flow) and `updater.go`'s `CheckBaseStaleness`/`ApplyRecompile` (still referencing the removed `MarkBaseIndexHash`-based per-mod fingerprint) — both fixed in Tasks 3-4/6/9. Confirm nothing in `internal/tui` or `cmd/lmm` is affected yet (their turn is Tasks 10/12).

- [ ] **Step 8: Commit**

```bash
git add internal/core/service.go internal/core/service_icarus_compile_test.go
git rm internal/core/service_compile_fingerprint_test.go
git commit -m "feat: download path ingests .exmodz as validate+retain, no per-mod pak (#197)"
```

### Task 3: Ingest simplification — import path (`Importer.Import`)

**Files:**

- Modify: `internal/core/importer.go:40-48` (`resolveCompiler` field type)
- Modify: `internal/core/importer.go:65` (`Service.NewImporter`)
- Modify: `internal/core/importer.go:110-184` (the `DeployCompile` branch inside `Import`)
- Test: `internal/core/service_import_compile_test.go` (rewrite the compile-branch fakes/assertions)

**Interfaces:**

- Consumes: `source.MergeCompiler` (Task 1); `mergeCompilerSourceForGame` (Task 2, Step 4); `cache.RetainedSourceName` (#196).
- Produces: after this task, an imported `.exmodz`'s cache entry contains ONLY the reserved retained-source file — `result.FilesExtracted` is `0` for this branch, matching Task 2's download-path behavior exactly.

- [ ] **Step 1: Write the failing test**

Open `internal/core/service_import_compile_test.go`. Update `fakeCompilerSource` there the same way as Task 2 Step 1 (this file has its own copy per the existing `#173`-era test structure — confirm with `grep -n "type fakeCompilerSource" internal/core/*.go`; if Task 2 already made it shared/exported across test files in this package, skip re-declaring and just import the one type). Replace `TestImportMod_DeployCompile_ExmodzCompiles` and `TestImportMod_DeployCompile_RecordsBaseIndexHashAndRetainedSource` with:

```go
func TestImportMod_DeployCompile_ValidatesAndRetainsNoPerModPak(t *testing.T) {
	svc, src, game := newImportCompileTestGame(t)

	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "Bear_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("fake-exmodz-bytes"), 0o644))

	importer := svc.NewImporter(game)
	result, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, src.validateCalls)
	require.Equal(t, 0, src.compileCalls, "import must NOT compile a per-mod pak (#197: merged-only)")
	require.Equal(t, 0, result.FilesExtracted)

	gameCache := svc.GetGameCache(game)
	files, err := gameCache.ListFiles(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
	require.NoError(t, err)
	require.Empty(t, files)

	// Import has no real DownloadableFile.ID (see the field's own doc
	// comment) - it keys the retained source by the ARCHIVE'S OWN filename
	// instead, exactly as the #196-era destName-keying did.
	retainedPath := gameCache.GetFilePath(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version, cache.RetainedSourceName("Bear_Mount.exmodz"))
	data, err := os.ReadFile(retainedPath)
	require.NoError(t, err)
	require.Equal(t, "fake-exmodz-bytes", string(data))
}

func TestImportMod_DeployCompile_MalformedExmodz_FailsLoud(t *testing.T) {
	svc, src, game := newImportCompileTestGame(t)
	_ = src // validation failure is injected by wrapping, not by this fake

	failing := &failingValidateCompilerSource{fakeCompilerSource: &fakeCompilerSource{}}
	// Re-register under the same source ID so the importer resolves the
	// failing wrapper instead of the passing fake newImportCompileTestGame
	// already registered.
	svc.RegisterSource(failing)

	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "Bad_Mount.exmodz")
	require.NoError(t, os.WriteFile(archivePath, []byte("not-a-valid-exmodz"), 0o644))

	importer := svc.NewImporter(game)
	_, err := importer.Import(context.Background(), archivePath, game, core.ImportOptions{})
	require.Error(t, err)
}
```

`newImportCompileTestGame` already exists in this file (from #173/#196) and registers `fakeCompilerSource{}` under source ID `"fake-compiler"` with `game.SourceIDs = {"fake-compiler": ...}` — since `svc.RegisterSource` overwrites by ID (confirm: `grep -n "func.*RegisterSource" internal/core/service.go` shows it stores into a map keyed by `src.ID()`, and `failingValidateCompilerSource` embeds `*fakeCompilerSource{}` whose `ID()` returns `"fake-compiler"` too), the second `RegisterSource` call replaces the passing fake for that source ID within this one test — no `game.SourceIDs` change needed.

- [ ] **Step 2: Run the tests, confirm they fail to compile**

Run: `go test ./internal/core/... -run 'TestImportMod_DeployCompile' -v`
Expected: build failure (`validateCalls` undefined on this file's copy of `fakeCompilerSource` until it's updated identically to Task 2 Step 1).

- [ ] **Step 3: Replace the `DeployCompile` branch**

`internal/core/importer.go:40-48` — change the field:

```go
	// resolveMergeCompiler resolves the MergeCompiler-capable source mapped
	// to a DeployCompile game's registry entry (#197), consulted only when
	// importing a ".exmodz" archive for such a game — Import has no
	// per-archive source pinned the way DownloadModToCache does, so it must
	// look up the game's configured sources instead. nil when the Importer
	// was built via the standalone NewImporter (no Service context):
	// importing an .exmodz through such an Importer fails loud rather than
	// silently caching an unvalidated archive.
	resolveMergeCompiler func(gameID string) (source.MergeCompiler, error)
```

`internal/core/importer.go:65` — update the assignment:

```go
	imp.resolveMergeCompiler = s.mergeCompilerSourceForGame
```

`internal/core/importer.go:110-184` — replace the entire `if game.DeployMode == domain.DeployCompile && isExmodzFile(filename) {` block body with:

```go
	if game.DeployMode == domain.DeployCompile && isExmodzFile(filename) {
		// Validate mode (#197): Import has no real source file ID the way a
		// download does (DownloadableFile.ID is resolved later, outside
		// Import, only when --id was given), so the retained source is
		// keyed by the archive's own filename instead - stable across
		// re-imports of the same name, and the ONLY identity Import ever
		// has for this content.
		if i.resolveMergeCompiler == nil {
			return nil, fmt.Errorf("game %q requires DeployCompile to import %q, but this Importer was constructed without service context (via core.NewImporter, not Service.NewImporter) and has no compiler resolver to consult - import via the service-backed importer instead", game.ID, filename)
		}
		mc, err := i.resolveMergeCompiler(game.ID)
		if err != nil {
			return nil, err
		}
		if err := mc.ValidateSource(archivePath); err != nil {
			return nil, fmt.Errorf("validating %s: %w", filename, err)
		}

		modName = strings.TrimSuffix(filename, filepath.Ext(filename))
		if version != "" && version != "unknown" {
			if idx := strings.LastIndex(modName, version); idx > 0 {
				modName = strings.TrimRight(modName[:idx], "-_ ")
			}
		}

		cacheMod := &domain.Mod{ID: modID, SourceID: sourceID, Version: version, GameID: game.ID}
		cachePath, stagePath, err := prepareUnseededStaging(i.cache, game, cacheMod)
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(stagePath) //nolint:errcheck

		if err := os.MkdirAll(stagePath, 0755); err != nil {
			return nil, fmt.Errorf("preparing cache staging: %w", err)
		}
		retainedPath := filepath.Join(stagePath, cache.RetainedSourceName(filename))
		if err := copyFileStreaming(archivePath, retainedPath); err != nil {
			return nil, fmt.Errorf("retaining %s: %w", filename, err)
		}
		if err := commitStagedCache(cachePath, stagePath); err != nil {
			return nil, err
		}
		fileCount = 0
	} else if game.DeployMode == domain.DeployCopy {
```

(The `} else if game.DeployMode == domain.DeployCopy {` on the last line is the pre-existing next branch — reattach it exactly as it already reads at the current line 185; do not duplicate it.) Add `"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"` to `importer.go`'s import block if not already present (`grep -n '"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"' internal/core/importer.go` — it likely already is, since `i.cache *cache.Cache` is an existing field type).

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `go test ./internal/core/... -run 'TestImportMod_DeployCompile' -v`
Expected: both new tests PASS.

- [ ] **Step 5: Remove now-obsolete per-mod-pak import tests**

Delete `TestImportMod_DeployCompile_RoutesPerFile`, `TestImportMod_DeployCompile_CompileFailureLeavesNoPartialArtifact`, `TestImportMod_DeployCompile_ReimportSurvivesStagingFailure` from `service_import_compile_test.go` — each asserts on the removed per-mod-pak-compile shape (destName-as-`_P.pak`, a `raceCompilerSource`/`failingCompilerSource` simulating a COMPILE failure, which no longer exists as a per-mod step here — validation failure is now the only failure mode Import's own branch can produce, already covered by Step 1's new test). Keep `TestImportMod_DeployCompile_ZipPassthroughUnaffected`, `TestImportMod_DeployCompile_NoCompilerSourceFailsLoud` (rename its fake-lookup assertion target from `resolveCompiler`/`compilerSourceForGame` wording to `resolveMergeCompiler`/`mergeCompilerSourceForGame` if the test's own comment or error-string assertion names it), `TestImportMod_DeployCompile_MissingBasePakFailsLoud` — **wait**, re-read this last one: Import's `DeployCompile` branch after this task NO LONGER calls `resolveBasePak` at all (validation doesn't need a base pak — only the eventual MERGE does). Delete `TestImportMod_DeployCompile_MissingBasePakFailsLoud` too; there is nothing base-pak-related left to fail on at import time. `TestImportMod_DeployCompile_StandaloneImporterFailsLoud` stays, renaming its assertion string check (`"without service context"`/`"core.NewImporter"`) — unaffected wording, still correct.

- [ ] **Step 6: Run the full core suite, confirm remaining failures are confined to `updater.go`**

Run: `go test ./internal/core/... 2>&1 | tail -80`
Expected: `internal/core/service.go` and `internal/core/importer.go` compile and their own tests pass; remaining failures are in `updater.go`'s `CheckBaseStaleness`/`ApplyRecompile` (still referencing removed per-mod machinery) — fixed in Task 4/6/9.

- [ ] **Step 7: Commit**

```bash
git add internal/core/importer.go internal/core/service_import_compile_test.go
git commit -m "feat: import path ingests .exmodz as validate+retain, no per-mod pak (#197)"
```

### Task 4: Remove dead #196 per-mod compile machinery

**Files:**

- Modify: `internal/storage/cache/cache.go:253-312` (remove `baseIndexHashPrefix`/`MarkBaseIndexHash`/`BaseIndexHashes`)
- Modify: `internal/storage/cache/cache_test.go` (remove their tests)
- Modify: `internal/core/service.go:1008-1054` (remove `compiledFileName`, `stageCompileFingerprint`; KEEP `resolveBasePak` and `basePakIndexHash` — Task 5 reuses both)

**Interfaces:**

- Consumes: nothing new.
- Produces: nothing new — this is pure removal. `resolveBasePak(game *domain.Game) (string, error)` and `basePakIndexHash(basePakPath string) (string, error)` remain exactly as-is (Task 5's `syncMergedPak` calls both).

- [ ] **Step 1: Confirm nothing outside this task's own scope still calls the functions being removed**

Run: `grep -rn "MarkBaseIndexHash\|BaseIndexHashes\|compiledFileName\|stageCompileFingerprint" --include='*.go' .`
Expected, after Tasks 2/3 landed: matches ONLY inside `internal/storage/cache/cache.go`/`cache_test.go` (definitions) and `internal/core/service.go` (definitions) — zero call sites left in `internal/core/updater.go` (Task 6/9 will have already stopped calling them if done in order; if this task runs before Task 6/9 in a different execution order, `updater.go`'s `CheckBaseStaleness`/`ApplyRecompile` will still reference `BaseIndexHashes` — in that case, do Task 6 first, or accept that `go build` fails here until Task 6 lands, which is fine since this whole plan's tasks are meant to run in the numbered order).

- [ ] **Step 2: Remove `baseIndexHashPrefix`/`MarkBaseIndexHash`/`BaseIndexHashes` from `cache.go`**

Delete lines 253-312 (quoted in full in this task's "Files" section context above) verbatim — from the `// baseIndexHashPrefix names...` comment through the `BaseIndexHashes` function's closing `}`. The following `retainedSourcePrefix`/`RetainedSourceName` block (lines 314-329) is untouched and now becomes the section immediately following whatever preceded line 253 (`HasFileIDs`, unaffected).

- [ ] **Step 3: Remove their tests from `cache_test.go`**

Run: `grep -n "^func TestCache_BaseIndexHashes\|^func TestCache_MarkBaseIndexHash" internal/storage/cache/cache_test.go` and delete `TestCache_BaseIndexHashes_RoundTrip`, `TestCache_BaseIndexHashes_ExcludedFromContentEnumerators`, `TestCache_MarkBaseIndexHash_UnverifiableIDs` in full. Leave `TestCache_RetainedSourceName_IsReservedAndExcludedFromContent` and `TestCache_RetainedSourceName_UniquePerFileID` — unaffected.

- [ ] **Step 4: Remove `compiledFileName`/`stageCompileFingerprint` from `service.go`**

`internal/core/service.go:1008-1017` (`compiledFileName`) and `:1036-1054` (`stageCompileFingerprint`) — delete both functions in full (their doc comments too). `basePakIndexHash` (currently between them) stays. After deletion, `resolveBasePak` and `basePakIndexHash` should be adjacent (or separated only by whatever other unrelated function already sat between `resolveBasePak` and `compiledFileName`).

- [ ] **Step 5: Run the full build**

Run: `go build ./... 2>&1 | tail -60`
Expected (if run in plan order, after Task 6/9): clean build. If any `unused` warnings appear for imports that only `compiledFileName`/`stageCompileFingerprint` needed, remove them (`go vet`/`gofmt` will not catch unused imports, but `go build` will fail loudly on them — check `internal/core/service.go`'s import block against what's still referenced).

- [ ] **Step 6: Run the full test suite**

Run: `go test ./... 2>&1 | tail -60`
Expected: green, assuming Task 5/6/9 have landed first to remove the LAST remaining callers (`updater.go`).

- [ ] **Step 7: Commit**

```bash
git add internal/storage/cache/cache.go internal/storage/cache/cache_test.go internal/core/service.go
git commit -m "chore: remove dead #196 per-mod compile fingerprint machinery (#197)"
```

**Note on task ordering:** this task is listed 4th for narrative clarity (finish the ingest-side cleanup before moving to the new merge/deploy machinery), but its Step 5/6 depend on Task 6/9 already having stopped calling `BaseIndexHashes`/`ApplyRecompile`. If executing tasks strictly in order, expect `go build` to fail between Task 4 and Task 6/9 landing — that is fine; Task 4's own commit still only touches the files listed above, and the build goes green once Task 9 lands. Subagent-driven execution should sequence Tasks 5→6→7→8→9 before circling back to finish Task 4's Step 5-7, or simply do Task 4 last, after Task 9 — both orderings produce the identical final diff.

### Task 5: `MergedFingerprint` type + cache marker path + `enabledExmodzSources`

**Files:**

- Modify: `internal/domain/mod.go` (add `SourceMerged` constant)
- Modify: `internal/storage/cache/cache.go` (add `MergeFingerprintPath`)
- Test: `internal/storage/cache/cache_test.go`
- Create: `internal/core/merged_pak.go`
- Test: `internal/core/merged_pak_test.go`

**Interfaces:**

- Consumes: `cache.ReservedPrefix` (#196, unchanged); `Service.GetInstalledModsInProfileOrder(gameID, profileName string) ([]domain.InstalledMod, error)` (existing, unchanged); `cache.RetainedSourceName(fileID string) string` (#196, unchanged); `md5File(path string) (string, error)` (#196, unchanged); `resolveBasePak`/`basePakIndexHash` (#196, unchanged — kept by Task 4).
- Produces: `domain.SourceMerged = "lmm-merged"`; `cache.MergeFingerprintPath(versionDir string) string`; `core.MergedFingerprint{BaseIndexHash string, Mods []MergedFingerprintEntry}`; `core.MergedFingerprintEntry{SourceID, ModID, Version, Checksum string}`; `core.marshalMergedFingerprint(f MergedFingerprint) ([]byte, error)`; `core.mergedFingerprintsEqual(a, b MergedFingerprint) (bool, error)`; `core.mergedPakModID = "merged-pak"`, `core.mergedPakVersion = "merged"`, `core.mergedPakFileName = "zzz_LMM_Merged_P.pak"`; `Service.enabledExmodzSources(game *domain.Game, profileName string) ([]source.MergeSource, error)`. All consumed by Task 6.

**Extraction-verified:** `MergedFingerprint`/`marshalMergedFingerprint`/`mergedFingerprintsEqual` below are the EXACT code verified against the scratch copy of `develop` tip `541b485`, with 7 tests proving determinism and that every documented regeneration trigger (base pak change, mod enabled/disabled, load-order swap, version bump) produces an unequal comparison. **One real defect was caught and fixed during verification:** `encoding/json` marshals a nil slice as `null` but an empty slice as `[]` — two different byte sequences for what must count as the same "zero contributing mods" state. Without the normalization in `marshalMergedFingerprint` below, a profile with zero enabled exmodz mods could spuriously flip between "stale"/"not stale" depending on which code path happened to build each side's slice. The fix (normalize `nil` to `[]MergedFingerprintEntry{}` before marshaling) is included below, not a follow-up.

- [ ] **Step 1: Write the failing tests**

`internal/domain/mod.go` — no test needed for a bare constant; add it directly in Step 4 below.

Create `internal/storage/cache/cache_test.go` additions (append to the existing file, do not create a new one):

```go
func TestCache_MergeFingerprintPath_IsReserved(t *testing.T) {
	path := cache.MergeFingerprintPath("/some/version/dir")
	if !strings.HasPrefix(filepath.Base(path), cache.ReservedPrefix) {
		t.Errorf("MergeFingerprintPath = %q, want a reserved-prefixed basename", path)
	}
}

func TestCache_MergeFingerprintPath_ExcludedFromContent(t *testing.T) {
	c := cache.New(t.TempDir())
	require.NoError(t, c.Store("g", "lmm-merged", "merged-pak", "merged", "zzz_LMM_Merged_P.pak", []byte("pak-bytes")))
	versionDir := c.ModPath("g", "lmm-merged", "merged-pak", "merged")
	require.NoError(t, os.WriteFile(cache.MergeFingerprintPath(versionDir), []byte(`{"BaseIndexHash":"abc"}`), 0o644))

	files, err := c.ListFiles("g", "lmm-merged", "merged-pak", "merged")
	require.NoError(t, err)
	assert.Equal(t, []string{"zzz_LMM_Merged_P.pak"}, files, "the fingerprint marker must never be listed as deployable content")
}
```

Create `internal/core/merged_pak_test.go`:

```go
package core_test

import "testing"

// These tests exercise unexported core package internals (marshalMergedFingerprint,
// mergedFingerprintsEqual) and therefore live in package core, not core_test -
// see merged_pak_internal_test.go.
var _ = testing.T{}
```

Create `internal/core/merged_pak_internal_test.go` (white-box, `package core` — matches this package's existing precedent of a mixed black-box/white-box test split, e.g. `service_download_local_test.go` is `package core` while most other test files here are `package core_test`):

```go
package core

import "testing"

func TestMergedFingerprint_Deterministic(t *testing.T) {
	f := MergedFingerprint{
		BaseIndexHash: "abc123",
		Mods: []MergedFingerprintEntry{
			{SourceID: "icarus", ModID: "bear-mount", Version: "1.0", Checksum: "deadbeef"},
			{SourceID: "icarus", ModID: "wolf-mount", Version: "2.0", Checksum: "cafef00d"},
		},
	}
	b1, err := marshalMergedFingerprint(f)
	if err != nil {
		t.Fatalf("marshal 1: %v", err)
	}
	b2, err := marshalMergedFingerprint(f)
	if err != nil {
		t.Fatalf("marshal 2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Errorf("marshal not deterministic: %q vs %q", b1, b2)
	}
}

func TestMergedFingerprintsEqual_IdenticalInputs(t *testing.T) {
	f := MergedFingerprint{
		BaseIndexHash: "abc123",
		Mods:          []MergedFingerprintEntry{{SourceID: "icarus", ModID: "bear-mount", Version: "1.0", Checksum: "deadbeef"}},
	}
	eq, err := mergedFingerprintsEqual(f, f)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if !eq {
		t.Errorf("identical fingerprints compared unequal")
	}
}

func TestMergedFingerprintsEqual_BaseHashChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc123", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"}}}
	b := a
	b.BaseIndexHash = "def456"
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("base pak change (regeneration trigger) must compare unequal")
	}
}

func TestMergedFingerprintsEqual_ModSetChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"}}}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"},
		{SourceID: "icarus", ModID: "m2", Version: "1.0", Checksum: "y"},
	}}
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("enabling a mod (regeneration trigger) must compare unequal")
	}
}

func TestMergedFingerprintsEqual_LoadOrderChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"},
		{SourceID: "icarus", ModID: "m2", Version: "1.0", Checksum: "y"},
	}}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m2", Version: "1.0", Checksum: "y"},
		{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"},
	}}
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("a load-order swap (regeneration trigger) must compare unequal, got equal")
	}
}

func TestMergedFingerprintsEqual_VersionChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"}}}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "2.0", Checksum: "x2"}}}
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("a mod version bump (regeneration trigger) must compare unequal")
	}
}

func TestMergedFingerprintsEqual_EmptyModsBothSides(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: nil}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{}}
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if !eq {
		t.Errorf("nil vs empty Mods slice must still compare equal (both marshal to the same JSON array shape)")
	}
}
```

Delete the placeholder `internal/core/merged_pak_test.go` stub above once `merged_pak_internal_test.go` exists — it was only there to document the black-box/white-box split decision; the REAL black-box tests for `enabledExmodzSources` are added in Step 5 below, in this same (deleted-then-recreated) file.

- [ ] **Step 2: Run the tests, confirm they fail to compile**

Run: `go test ./internal/storage/cache/... ./internal/core/... -run 'TestCache_MergeFingerprintPath|TestMergedFingerprint' -v`
Expected: build failure — `undefined: cache.MergeFingerprintPath`, `undefined: MergedFingerprint`, etc.

- [ ] **Step 3: Add `cache.MergeFingerprintPath`**

`internal/storage/cache/cache.go`, right after the existing `RetainedSourceName` function (end of that #196 block):

```go
// mergeFingerprintMarkerName names the single JSON fingerprint marker a
// merged-pak cache entry carries (#197): what base pak and which
// (source, mod, version, exmodz-checksum) tuples, in order, the pak was
// last built from - so a later staleness check can compare without
// re-deriving the merge. Reserved (ReservedPrefix) so ListFiles/Size/deploy
// skip it like every other lmm bookkeeping entry.
const mergeFingerprintMarkerName = ReservedPrefix + "merge-fingerprint"

// MergeFingerprintPath returns the reserved on-disk path for versionDir's
// merge-fingerprint marker. Pure naming, like RetainedSourceName - callers
// (internal/core, which owns the MergedFingerprint type and its JSON
// encoding) read/write the actual bytes with ordinary file I/O.
func MergeFingerprintPath(versionDir string) string {
	return filepath.Join(versionDir, mergeFingerprintMarkerName)
}
```

- [ ] **Step 4: Add `domain.SourceMerged`**

`internal/domain/mod.go`, right after the existing `SourceLocal` constant:

```go
// SourceMerged is the source ID for the synthetic, profile-scoped "mod"
// that tracks a game's merged compiled pak (#197 - Icarus's cross-mod
// table merge). Follows the SourceLocal precedent: a reserved sentinel
// string, not a real ModSource registration.
const SourceMerged = "lmm-merged"
```

- [ ] **Step 5: Implement `MergedFingerprint` and `enabledExmodzSources`**

Create `internal/core/merged_pak.go`:

```go
package core

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
)

// mergedPakModID/mergedPakVersion/mergedPakFileName identify the merged pak
// as a synthetic, singleton "mod" per (game, profile) - domain.SourceMerged
// is the matching sourceID. This reuses Installer.Install/Uninstall and
// cache.Cache verbatim (#197 design decision 2) rather than a parallel
// deploy/tracking mechanism: zero schema changes, and the SAME
// deployed_files ownership (and #168-class residue risk) as every other
// deployed file.
const (
	mergedPakModID = "merged-pak"
	// mergedPakVersion is fixed ("merged", not a real upstream version) -
	// there is exactly one merged pak per (game, profile) at any time, and
	// every regeneration REPLACES it outright (mirrors #166's directory-
	// source "replace, don't overlay" precedent) rather than versioning it.
	mergedPakVersion = "merged"
	// mergedPakFileName sorts LAST among files UE mounts from a profile's
	// mods directory: paks mount in filename-sort order and a later mount
	// wins same-path conflicts (this repo's own icarusContentMountPoint doc
	// comment, and #197's issue body, both note this) - "zzz" is a
	// long-standing UE-modding convention for "load last, highest
	// priority", so the merged pak's authoritative combined table state can
	// never be silently shadowed by a plain prebuilt .pak mod that happens
	// to also carry a table override. "LMM" makes the file greppable as
	// lmm-owned; "_P" matches this codebase's existing override-pak suffix
	// convention (compiledFileName).
	mergedPakFileName = "zzz_LMM_Merged_P.pak"
)

// MergedFingerprint captures everything a merged pak was built from (#197):
// the base pak's IndexHash plus an ORDERED list of every contributing
// exmodz file's identity and content checksum. Order matters - it's the
// profile's load order, which is also merge-application order - so two
// fingerprints with the same entries in a DIFFERENT order must compare
// unequal (a load-order change is a documented regeneration trigger).
type MergedFingerprint struct {
	BaseIndexHash string
	Mods          []MergedFingerprintEntry
}

// MergedFingerprintEntry identifies one contributing file within a
// MergedFingerprint.
type MergedFingerprintEntry struct {
	SourceID string
	ModID    string
	Version  string
	Checksum string // MD5 of the retained .exmodz bytes (md5File)
}

// marshalMergedFingerprint renders f deterministically: encoding/json
// marshals struct fields in declaration order (not sorted) and preserves
// slice order exactly, so the same MergedFingerprint value always produces
// byte-identical output - the property mergedFingerprintsEqual depends on.
//
// A nil Mods is normalized to an empty (non-nil) slice first: encoding/json
// marshals a nil slice as `null` but an empty slice as `[]` - two DIFFERENT
// byte sequences for what must count as the same "zero contributing mods"
// state (e.g. a freshly-built "current" fingerprint via `var mods []T`
// compared against a previously-stored marker written some other way).
// Caught by extraction-verification (a scratch test comparing the two
// literally failed before this normalization was added) - without it, a
// profile with zero enabled exmodz mods could spuriously flip between
// "stale"/"not stale" depending on which code path happened to build each
// side's slice.
func marshalMergedFingerprint(f MergedFingerprint) ([]byte, error) {
	if f.Mods == nil {
		f.Mods = []MergedFingerprintEntry{}
	}
	return json.Marshal(f)
}

// mergedFingerprintsEqual reports whether a and b describe the same merge
// inputs, by comparing their marshaled bytes - exactly what "compare
// against the stored marker" needs, since the marker itself IS the
// marshaled form.
func mergedFingerprintsEqual(a, b MergedFingerprint) (bool, error) {
	aBytes, err := marshalMergedFingerprint(a)
	if err != nil {
		return false, err
	}
	bBytes, err := marshalMergedFingerprint(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(aBytes, bBytes), nil
}

// enabledExmodzSources returns every enabled mod's retained .exmodz files
// for game+profileName, in PROFILE LOAD ORDER (the merge-application order,
// #197 design) - the exact input MergeCompile needs. Only files that were
// actually retained (cache.RetainedSourceName present in the mod's cache
// entry) count: a mod's plain .pak files, or a mod whose ingest never got
// far enough to retain anything, contribute nothing. A mod's OWN FileIDs
// are walked (not the whole cache directory) because a download-compiled
// entry's retained-source name is keyed by a real DownloadableFile.ID,
// while an import-compiled entry's is keyed by its own archive filename
// (see Task 2/3's ingest branches) - FileIDs is the one list that already
// carries whichever identity applies, for either origin.
func (s *Service) enabledExmodzSources(game *domain.Game, profileName string) ([]source.MergeSource, error) {
	mods, err := s.GetInstalledModsInProfileOrder(game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile mods: %w", err)
	}

	gameCache := s.GetGameCache(game)
	var sources []source.MergeSource
	for _, mod := range mods {
		if !mod.Enabled {
			continue
		}
		for _, fileID := range mod.FileIDs {
			retainedPath := gameCache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, cache.RetainedSourceName(fileID))
			if _, statErr := os.Stat(retainedPath); statErr != nil {
				continue // not a retained exmodz file (a plain .pak's fileID, or nothing ingested)
			}
			sources = append(sources, source.MergeSource{
				ModRef:     mod.SourceID + ":" + mod.ID,
				ExmodzPath: retainedPath,
			})
		}
	}
	return sources, nil
}
```

Add `"os"` to the import block (used by `os.Stat`).

- [ ] **Step 6: Run the tests, confirm they pass**

Run: `go test ./internal/storage/cache/... ./internal/core/... -run 'TestCache_MergeFingerprintPath|TestMergedFingerprint' -v`
Expected: all PASS.

- [ ] **Step 7: Write and run the `enabledExmodzSources` black-box test**

Replace the placeholder `internal/core/merged_pak_test.go` (from Step 1) with:

```go
package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// TestEnabledExmodzSources_OrderMatchesProfileLoadOrderAndSkipsDisabled
// proves enabledExmodzSources returns retained exmodz files in PROFILE
// LOAD ORDER (merge-application order), skips disabled mods entirely, and
// skips a mod's fileIDs that have no retained source (a plain .pak).
func TestEnabledExmodzSources_OrderMatchesProfileLoadOrderAndSkipsDisabled(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "icarus", ModPath: t.TempDir(), DeployMode: domain.DeployCompile}
	require.NoError(t, svc.AddGame(game))

	gameCache := svc.GetGameCache(game)

	seedMod := func(sourceID, modID, version string, fileIDs []string, enabled bool) {
		for _, fileID := range fileIDs {
			require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, cache.RetainedSourceName(fileID), []byte("exmodz-"+modID+"-"+fileID)))
		}
		require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
			Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: modID, Version: version, GameID: game.ID},
			ProfileName:  "default",
			Enabled:      enabled,
			FileIDs:      fileIDs,
			UpdatePolicy: domain.UpdateNotify,
		}))
	}

	// mixedMod has one exmodz fileID and one plain-pak fileID (no retained
	// source for the latter) - only the exmodz one should be included.
	seedMod("icarus", "second-mod", "1.0", []string{"exmodz-file", "pak-file"}, true)
	seedMod("icarus", "first-mod", "1.0", []string{"exmodz-file"}, true)
	seedMod("icarus", "disabled-mod", "1.0", []string{"exmodz-file"}, false)

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)
	// Profile load order: first-mod, then second-mod (disabled-mod
	// intentionally omitted - membership in Profile.Mods, not just an
	// Enabled DB row, is what GetInstalledModsInProfileOrder requires).
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "icarus", ModID: "first-mod", Version: "1.0", FileIDs: []string{"exmodz-file"}}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "icarus", ModID: "second-mod", Version: "1.0", FileIDs: []string{"exmodz-file", "pak-file"}}))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "icarus", ModID: "disabled-mod", Version: "1.0", FileIDs: []string{"exmodz-file"}}))

	sources, err := svc.EnabledExmodzSourcesForTest(game, "default")
	require.NoError(t, err)
	require.Len(t, sources, 2, "disabled-mod excluded; second-mod's plain-pak fileID excluded")
	require.Equal(t, "icarus:first-mod", sources[0].ModRef)
	require.Equal(t, "icarus:second-mod", sources[1].ModRef)

	data, err := os.ReadFile(sources[0].ExmodzPath)
	require.NoError(t, err)
	require.Equal(t, "exmodz-first-mod-exmodz-file", string(data))
}

var _ = context.Background // keep context import if a future case needs ctx
var _ = filepath.Join       // keep filepath import if a future case needs it
```

**`enabledExmodzSources` is unexported** — this black-box test needs an exported test seam. Add ONE tiny exported wrapper to `internal/core/merged_pak.go`, directly below `enabledExmodzSources`:

```go
// EnabledExmodzSourcesForTest exposes enabledExmodzSources to external
// (core_test package) tests - the method itself stays unexported since it
// is an internal implementation detail of syncMergedPak (Task 6), not part
// of Service's public API.
func (s *Service) EnabledExmodzSourcesForTest(game *domain.Game, profileName string) ([]source.MergeSource, error) {
	return s.enabledExmodzSources(game, profileName)
}
```

(This mirrors an existing pattern already used elsewhere in this codebase for white-box-only helpers that still need black-box test coverage — if a `*ForTest`-style export doesn't already appear anywhere via `grep -rn "ForTest" internal/core/*.go`, prefer instead moving JUST this one test into a new `internal/core/merged_pak_internal_test.go` `package core` file, calling `s.enabledExmodzSources` directly with no exported wrapper at all — simpler, and avoids adding test-only surface to `Service`. Either approach is correct; the internal-test-file route is preferred if there's no existing `*ForTest` precedent to stay consistent with.)

Remove the unused `var _ = context.Background` / `var _ = filepath.Join` lines above if the final test file doesn't need those imports at all (it doesn't, in the version shown — they were placeholders for exactly this "which route did you take" branch; delete `"context"` and `"path/filepath"` from the import block too if going the internal-test-file route, since neither `context` nor `filepath` is used in that case).

Run: `go test ./internal/core/... -run 'TestEnabledExmodzSources' -v`
Expected: PASS.

- [ ] **Step 8: Run the full build and suite**

Run: `go build ./... 2>&1 | tail -40`
Expected: same confined failures as Task 1 Step 8 noted (`internal/core/importer.go`... wait, Task 2/3 already fixed those — expected failures now are ONLY in `internal/core/updater.go`, fixed in Task 6/9).

Run: `go test ./internal/storage/cache/... ./internal/core/... -v 2>&1 | tail -80` (skip the rest of the repo until `updater.go` is fixed in Task 6).

- [ ] **Step 9: Commit**

```bash
git add internal/domain/mod.go internal/storage/cache/cache.go internal/storage/cache/cache_test.go internal/core/merged_pak.go internal/core/merged_pak_test.go internal/core/merged_pak_internal_test.go
git commit -m "feat: MergedFingerprint type + enabledExmodzSources (#197)"
```

### Task 6: `Service.syncMergedPak` — regenerate-if-stale engine

**Files:**

- Modify: `internal/core/merged_pak.go`
- Test: `internal/core/merged_pak_test.go`

**Interfaces:**

- Consumes: `enabledExmodzSources` (Task 5); `MergedFingerprint`/`marshalMergedFingerprint`/`mergedFingerprintsEqual` (Task 5); `mergeCompilerSourceForGame` (Task 2); `resolveBasePak`/`basePakIndexHash` (#196, kept by Task 4); `md5File` (#196); `cache.MergeFingerprintPath` (Task 5); `prepareUnseededStaging`/`commitStagedCache` (#196, unchanged); `s.GetInstallerForProfile` (existing); `Installer.Install`/`Uninstall` (existing, unchanged).
- Produces: `Service.syncMergedPak(ctx context.Context, game *domain.Game, profileName string) (warnings []string, err error)` — consumed by Task 7/8's hook call sites and Task 9's `ApplyMergedPakRegen`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/core/merged_pak_test.go`:

```go
// newMergedPakTestGame builds a DeployCompile game with a registered merge
// compiler and an installed base pak - shared setup for syncMergedPak
// tests. Returns the service, game, and the base pak's own path (so a test
// can rewrite it to simulate a base-pak refresh).
func newMergedPakTestGame(t *testing.T) (*core.Service, *domain.Game, string) {
	t.Helper()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	svc := newFlowsTestService(t)
	src := &fakeCompilerSource{}
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	return svc, game, basePak
}

// seedEnabledExmodzMod installs an ENABLED mod with a retained exmodz file,
// via svc.SaveInstalledMod + profile UpsertMod (matching the real ingest
// shape Task 2/3 produce - a cache entry with a retained source and no
// deployment members).
func seedEnabledExmodzMod(t *testing.T, svc *core.Service, game *domain.Game, sourceID, modID, version, fileID string, exmodzContent []byte) {
	t.Helper()
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, sourceID, modID, version, cache.RetainedSourceName(fileID), exmodzContent))
	require.NoError(t, svc.SaveInstalledMod(&domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: sourceID, Name: modID, Version: version, GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		FileIDs:      []string{fileID},
		UpdatePolicy: domain.UpdateNotify,
	}))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: sourceID, ModID: modID, Version: version, FileIDs: []string{fileID}}))
}

// TestSyncMergedPak_GeneratesAndDeploys is the happy path: one enabled
// exmodz mod, no merged pak yet - syncMergedPak must generate one and
// deploy it into the game directory.
func TestSyncMergedPak_GeneratesAndDeploys(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-exmodz-bytes"))

	warnings, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.Empty(t, warnings)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-exmodz-bytes", string(data), "fakeCompilerSource's MergeCompile concatenates source bytes - see its own definition")
}

// TestSyncMergedPak_NoOpWhenUnchanged proves the fingerprint gate actually
// gates: calling syncMergedPak twice with nothing changed must not
// recompile (fakeCompilerSource.compileCalls stays at 1).
func TestSyncMergedPak_NoOpWhenUnchanged(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-exmodz-bytes"))

	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	src, ok := svc.GetSourceForTest("fake-compiler").(*fakeCompilerSource)
	require.True(t, ok)
	require.Equal(t, 1, src.compileCalls)

	_, err = svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.Equal(t, 1, src.compileCalls, "an unchanged fingerprint must not trigger a second merge")
}

// TestSyncMergedPak_RegeneratesOnModEnable proves enabling a SECOND mod
// (the mod-set changing) triggers regeneration.
func TestSyncMergedPak_RegeneratesOnModEnable(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	warnings, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.Empty(t, warnings)

	src, ok := svc.GetSourceForTest("fake-compiler").(*fakeCompilerSource)
	require.True(t, ok)
	require.Equal(t, 2, src.compileCalls, "a mod-set change must trigger a second merge")

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-byteswolf-bytes", string(data), "the merged pak must now reflect BOTH mods")
}

// TestSyncMergedPak_ZeroEnabledMods_UninstallsExistingPak proves the
// uninstall-to-zero case: disabling the LAST enabled exmodz mod must
// remove any previously-deployed merged pak from the game directory.
func TestSyncMergedPak_ZeroEnabledMods_UninstallsExistingPak(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "precondition: the merged pak must exist before disabling")

	require.NoError(t, svc.SetModEnabled("fake-compiler", "bear-mount", game.ID, "default", false))

	_, err = svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	_, err = os.Stat(deployedPath)
	require.True(t, os.IsNotExist(err), "disabling the last exmodz mod must remove the deployed merged pak")
}

// TestSyncMergedPak_RegeneratesOnBaseHashChange proves a base-pak refresh
// (the "Friday problem", generalized from #196 to the merged model) still
// triggers regeneration.
func TestSyncMergedPak_RegeneratesOnBaseHashChange(t *testing.T) {
	svc, game, basePak := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	// Rewrite the base pak with different content - a new IndexHash.
	writeFakeBasePakWithTable(t, basePak, map[string][]byte{"AI/D_Other.json": []byte(`{"Rows":[{"Name":"x","V":1}]}`)})

	_, err = svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	src, ok := svc.GetSourceForTest("fake-compiler").(*fakeCompilerSource)
	require.True(t, ok)
	require.Equal(t, 2, src.compileCalls, "a base pak change must trigger a second merge")
}

// TestSyncMergedPak_NonCompileGame_NoOp: a DeployExtract/DeployCopy game has
// no merged-pak concept at all - syncMergedPak must no-op unconditionally
// (cheap enough to call from every mutation flow regardless of game type).
func TestSyncMergedPak_NonCompileGame_NoOp(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "skyrim-se", ModPath: t.TempDir(), DeployMode: domain.DeployExtract}
	require.NoError(t, svc.AddGame(game))

	warnings, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.Empty(t, warnings)
}

// TestSyncMergedPak_AssetCollisionWarningSurfaces proves MergeCompile's own
// warnings (Task 1) propagate all the way out of syncMergedPak.
func TestSyncMergedPak_AssetCollisionWarningSurfaces(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	src, ok := svc.GetSourceForTest("fake-compiler").(*fakeCompilerSource)
	require.True(t, ok)
	src.mergeWarnings = []string{"asset collision: fixture warning"}
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	warnings, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	require.Equal(t, []string{"asset collision: fixture warning"}, warnings)
}
```

This introduces THREE new small test seams the fake/harness must gain:

1. `fakeCompilerSource.mergeWarnings []string` — its `MergeCompile` returns this slice instead of always `nil`. Update the fake (already changed in Task 2 Step 1) in `internal/core/service_icarus_compile_test.go`:

```go
func (s *fakeCompilerSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, error) {
	s.compileCalls++
	var out []byte
	for _, src := range sources {
		data, err := os.ReadFile(src.ExmodzPath)
		if err != nil {
			return nil, err
		}
		out = append(out, data...)
	}
	return s.mergeWarnings, os.WriteFile(outputPath, out, 0o644)
}
```

Add `mergeWarnings []string` to the `fakeCompilerSource` struct.

2. `Service.SyncMergedPakForTest` — thin exported wrapper over `syncMergedPak`, same rationale/pattern as Task 5 Step 7's `EnabledExmodzSourcesForTest` (or, per that step's note, skip the wrapper and put these tests in the internal white-box test file instead — pick whichever route Task 5 used, for consistency, and apply it here too).

3. `Service.GetSourceForTest` — a tiny exported wrapper over `s.registry.Get`, needed because these tests must reach into the `fakeCompilerSource` to assert `compileCalls`/set `mergeWarnings`, and `Service` has no existing public accessor for a registered source by ID (`GetSource` DOES already exist — `internal/core/service.go:108`, `func (s *Service) GetSource(id string) (source.ModSource, error)` — **use that directly, no new wrapper needed**; the test snippets above should read `svc.GetSource("fake-compiler")` (handling the returned error) rather than a fictitious `GetSourceForTest` — this was written assuming no existing accessor; there IS one, use it and drop this wrapper entirely).

Fix the test snippets above: replace every `svc.GetSourceForTest("fake-compiler").(*fakeCompilerSource)` with:

```go
	srcRaw, err := svc.GetSource("fake-compiler")
	require.NoError(t, err)
	src, ok := srcRaw.(*fakeCompilerSource)
	require.True(t, ok)
```

(inline this 4-line block wherever `svc.GetSourceForTest(...)` appears above).

Also add `writeFakeBasePakWithTable` (a variant of the existing `writeFakeBasePak` helper that lets a test control table content, needed for `TestSyncMergedPak_RegeneratesOnBaseHashChange`) to `internal/core/service_icarus_compile_test.go` next to `writeFakeBasePak`:

```go
func writeFakeBasePakWithTable(t *testing.T, path string, tables map[string][]byte) {
	t.Helper()
	w, err := unrealpak.Create(path)
	require.NoError(t, err)
	for mountPath, data := range tables {
		require.NoError(t, w.AddFile(mountPath, data))
	}
	require.NoError(t, w.Close())
}
```

Add `"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"` to that file's imports if not already present.

- [ ] **Step 2: Run the tests, confirm they fail to compile**

Run: `go test ./internal/core/... -run 'TestSyncMergedPak' -v`
Expected: build failure — `undefined: (*core.Service).SyncMergedPakForTest` (or the internal-test-file equivalent), `mergeWarnings` undefined on `fakeCompilerSource`.

- [ ] **Step 3: Implement `syncMergedPak`**

Append to `internal/core/merged_pak.go`:

```go
// syncMergedPak regenerates game+profileName's merged pak if its recorded
// fingerprint no longer matches the CURRENT enabled-mod set/order/versions/
// base pak (#197). Cheap when nothing changed: the fast path is one
// directory read (enabledExmodzSources), one base-pak footer read
// (basePakIndexHash - never the pak's full content), and N small MD5s
// (md5File over each retained .exmodz - real files here are small, see
// #175's own research on real base-table sizes), then a byte comparison.
// Safe to call unconditionally from ANY mutation flow regardless of game
// type - it no-ops immediately for a non-DeployCompile game.
//
// Zero enabled exmodz sources uninstalls any existing merged pak instead of
// generating an empty one (#197 design decision 2's "uninstall-to-zero"
// requirement) - Installer.Uninstall on the synthetic merged-pak mod is
// idempotent when there is nothing deployed (linker.Undeploy tolerates an
// already-absent path, matching every other uninstall in this codebase),
// so calling it unconditionally here is safe even when no pak was ever
// generated.
func (s *Service) syncMergedPak(ctx context.Context, game *domain.Game, profileName string) (warnings []string, err error) {
	if game.DeployMode != domain.DeployCompile {
		return nil, nil
	}

	sources, err := s.enabledExmodzSources(game, profileName)
	if err != nil {
		return nil, fmt.Errorf("listing enabled exmodz mods: %w", err)
	}

	gameCache := s.GetGameCache(game)
	syntheticMod := &domain.Mod{ID: mergedPakModID, SourceID: domain.SourceMerged, Version: mergedPakVersion, GameID: game.ID}

	installer, err := s.GetInstallerForProfile(game, profileName)
	if err != nil {
		return nil, err
	}

	if len(sources) == 0 {
		if uerr := installer.Uninstall(ctx, game, syntheticMod, profileName); uerr != nil {
			return nil, fmt.Errorf("removing merged pak: %w", uerr)
		}
		if derr := gameCache.Delete(game.ID, domain.SourceMerged, mergedPakModID, mergedPakVersion); derr != nil {
			return nil, fmt.Errorf("clearing merged pak cache entry: %w", derr)
		}
		return nil, nil
	}

	basePakPath, err := resolveBasePak(game)
	if err != nil {
		return nil, err
	}
	liveHash, err := basePakIndexHash(basePakPath)
	if err != nil {
		return nil, fmt.Errorf("reading base pak for merge fingerprint: %w", err)
	}

	current := MergedFingerprint{BaseIndexHash: liveHash}
	for _, src := range sources {
		sum, herr := md5File(src.ExmodzPath)
		if herr != nil {
			return nil, fmt.Errorf("hashing %s: %w", src.ExmodzPath, herr)
		}
		sourceID, modID, _ := strings.Cut(src.ModRef, ":")
		current.Mods = append(current.Mods, MergedFingerprintEntry{
			SourceID: sourceID, ModID: modID, Checksum: sum,
		})
	}
	// Version is not carried on source.MergeSource (it only needs ModRef +
	// ExmodzPath for the merge itself) - resolved separately here so
	// enabledExmodzSources' own signature stays minimal. Re-fetching the
	// installed mods once more is cheap (small profiles) and keeps
	// enabledExmodzSources' contract focused on ONE job.
	mods, err := s.GetInstalledModsInProfileOrder(game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile mods: %w", err)
	}
	versionByRef := make(map[string]string, len(mods))
	for _, m := range mods {
		versionByRef[m.SourceID+":"+m.ID] = m.Version
	}
	for i, src := range sources {
		current.Mods[i].Version = versionByRef[src.ModRef]
	}

	cachePath := gameCache.ModPath(game.ID, domain.SourceMerged, mergedPakModID, mergedPakVersion)
	if stored, ok := readMergedFingerprint(cachePath); ok {
		if eq, eqErr := mergedFingerprintsEqual(current, stored); eqErr == nil && eq {
			return nil, nil // fast path: nothing changed
		}
	}

	mc, err := s.mergeCompilerSourceForGame(game.ID)
	if err != nil {
		return nil, err
	}

	stagePath := cachePath + ".staging"
	if err := os.RemoveAll(stagePath); err != nil {
		return nil, fmt.Errorf("clearing merged pak staging: %w", err)
	}
	if err := os.MkdirAll(stagePath, 0755); err != nil {
		return nil, fmt.Errorf("preparing merged pak staging: %w", err)
	}
	defer os.RemoveAll(stagePath) //nolint:errcheck

	outputPath := filepath.Join(stagePath, mergedPakFileName)
	mergeWarnings, err := mc.MergeCompile(ctx, basePakPath, sources, outputPath)
	if err != nil {
		return nil, fmt.Errorf("merging %d exmodz mod(s): %w", len(sources), err)
	}
	warnings = mergeWarnings

	fingerprintBytes, err := marshalMergedFingerprint(current)
	if err != nil {
		return warnings, fmt.Errorf("encoding merge fingerprint: %w", err)
	}
	if err := os.WriteFile(cache.MergeFingerprintPath(stagePath), fingerprintBytes, 0644); err != nil {
		return warnings, fmt.Errorf("writing merge fingerprint: %w", err)
	}

	if err := commitStagedCache(cachePath, stagePath); err != nil {
		return warnings, err
	}

	if err := installer.Install(ctx, game, syntheticMod, profileName); err != nil {
		return warnings, fmt.Errorf("deploying merged pak: %w", err)
	}
	return warnings, nil
}

// readMergedFingerprint reads and decodes cachePath's stored merge
// fingerprint marker, if any. ok is false when no cache entry/marker
// exists yet (first-ever merge for this profile) or the marker is
// unreadable/corrupt - both degrade to "regenerate", never a crash or a
// false "unchanged".
func readMergedFingerprint(cachePath string) (fp MergedFingerprint, ok bool) {
	data, err := os.ReadFile(cache.MergeFingerprintPath(cachePath))
	if err != nil {
		return MergedFingerprint{}, false
	}
	if err := json.Unmarshal(data, &fp); err != nil {
		return MergedFingerprint{}, false
	}
	return fp, true
}
```

Add `"context"`, `"os"`, `"path/filepath"`, `"strings"` to `merged_pak.go`'s import block (`json` and `bytes` are already there from Task 5).

- [ ] **Step 4: Add the `SyncMergedPakForTest` wrapper (or internal test file — match Task 5's choice)**

If Task 5 used the exported-wrapper route:

```go
// SyncMergedPakForTest exposes syncMergedPak to external (core_test
// package) tests - see enabledExmodzSources/EnabledExmodzSourcesForTest's
// identical rationale.
func (s *Service) SyncMergedPakForTest(ctx context.Context, game *domain.Game, profileName string) ([]string, error) {
	return s.syncMergedPak(ctx, game, profileName)
}
```

If Task 5 used the internal-white-box-test-file route instead, move this task's new tests into that same `internal/core/merged_pak_internal_test.go` file and call `s.syncMergedPak(...)` directly — no wrapper.

- [ ] **Step 5: Run the tests, confirm they pass**

Run: `go test ./internal/core/... -run 'TestSyncMergedPak' -v`
Expected: all 7 PASS.

- [ ] **Step 6: Run the full build**

Run: `go build ./... 2>&1 | tail -40`
Expected: `internal/core/updater.go` is now the ONLY remaining broken file (`CheckBaseStaleness`/`ApplyRecompile` still reference removed `BaseIndexHashes`) — fixed in Task 9.

- [ ] **Step 7: Commit**

```bash
git add internal/core/merged_pak.go internal/core/merged_pak_test.go internal/core/service_icarus_compile_test.go
git commit -m "feat: Service.syncMergedPak - regenerate-if-stale engine (#197)"
```

### Task 7: Wire `syncMergedPak` into `EnableMod`/`DisableMod`/`UninstallMod`/`DeployProfile`

**Files:**

- Modify: `internal/core/flows.go` (4 call sites, listed below)
- Test: `internal/core/flows_test.go` (or a new `internal/core/merged_pak_hooks_test.go` — either is fine; this plan uses a new file to keep the diff to `flows.go` reviewable independently of a large pre-existing test file)

**Interfaces:**

- Consumes: `Service.syncMergedPak` (Task 6).
- Produces: nothing new — this task is pure wiring.

Every insertion below follows the IDENTICAL shape: call `syncMergedPak`, fold its warnings into the function's own existing diagnostics field, fold a hard error into the SAME non-fatal-Notes convention each function already uses for its OTHER best-effort side effects (never let a merged-pak sync failure turn an otherwise-successful enable/disable/uninstall/deploy into a hard error — the mod-level operation already succeeded; the merged pak catching up is a courtesy, and `lmm update`/`lmm verify` are the safety net if it doesn't).

- [ ] **Step 1: Write the failing tests**

Create `internal/core/merged_pak_hooks_test.go`:

```go
package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/require"
)

// TestEnableMod_SyncsMergedPak proves enabling an exmodz mod deploys the
// merged pak without a separate `lmm update` step.
func TestEnableMod_SyncsMergedPak(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	require.NoError(t, svc.SetModEnabled("fake-compiler", "bear-mount", game.ID, "default", false))

	_, err := svc.EnableMod(context.Background(), game, "default", "fake-compiler", "bear-mount")
	require.NoError(t, err)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "EnableMod must sync the merged pak, not just this mod's own (empty) cache entry")
}

// TestDisableMod_SyncsMergedPak_RemovesWhenLastModDisabled proves disabling
// the LAST enabled exmodz mod removes the merged pak.
func TestDisableMod_SyncsMergedPak_RemovesWhenLastModDisabled(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err)

	_, err = svc.DisableMod(context.Background(), game, "default", "fake-compiler", "bear-mount")
	require.NoError(t, err)

	_, err = os.Stat(deployedPath)
	require.True(t, os.IsNotExist(err), "DisableMod must sync the merged pak, removing it once the last exmodz mod is disabled")
}

// TestUninstallMod_SyncsMergedPak_RemovesWhenLastModUninstalled mirrors
// the disable case for a full uninstall.
func TestUninstallMod_SyncsMergedPak_RemovesWhenLastModUninstalled(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")

	_, err = svc.UninstallMod(context.Background(), game, "default", "fake-compiler", "bear-mount", core.UninstallOptions{})
	require.NoError(t, err)

	_, err = os.Stat(deployedPath)
	require.True(t, os.IsNotExist(err), "UninstallMod must sync the merged pak")
}

// TestDeployProfile_SyncsMergedPak proves a full `lmm deploy` also
// generates the merged pak (the pre-existing per-mod loop deploys zero
// files for an exmodz mod's own cache entry - Tasks 2/3 - so without this
// hook a fresh deploy would silently produce no merged pak at all).
func TestDeployProfile_SyncsMergedPak(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))

	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-bytes", string(data))
}
```

Add `"github.com/DonovanMods/linux-mod-manager/internal/core"` to the imports (needed for `core.UninstallOptions{}`/`core.DeployOptions{}`).

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `go test ./internal/core/... -run 'TestEnableMod_SyncsMergedPak|TestDisableMod_SyncsMergedPak|TestUninstallMod_SyncsMergedPak|TestDeployProfile_SyncsMergedPak' -v`
Expected: FAIL (not build failure this time — `EnableMod`/etc. already compile and run, they just don't call `syncMergedPak` yet, so no merged pak is ever deployed).

- [ ] **Step 3: Wire `EnableMod`**

`internal/core/flows.go`, `EnableMod` (ends around line 93-94 with `result.Changed = true` then `return result, nil`). Insert immediately before the final `return result, nil`:

```go
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: could not sync merged pak: %v", syncErr))
	} else {
		for _, w := range syncWarnings {
			result.Notes = append(result.Notes, "Warning: "+w)
		}
	}

	result.Changed = true
	return result, nil
```

(replacing the existing bare `result.Changed = true` / `return result, nil` pair with this — the sync call runs BEFORE `result.Changed = true` is set here only because that's where the existing lines already sat; functionally the ordering relative to `Changed` doesn't matter.)

- [ ] **Step 4: Wire `DisableMod`**

`internal/core/flows.go`, `DisableMod`'s MAIN path (not the already-disabled self-heal early return above it) ends with `result.Changed = true` / `return result, nil` (around line 165-166). Apply the identical insertion:

```go
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: could not sync merged pak: %v", syncErr))
	} else {
		for _, w := range syncWarnings {
			result.Notes = append(result.Notes, "Warning: "+w)
		}
	}

	result.Changed = true
	return result, nil
```

Do NOT add this to the already-disabled self-heal branch (`if !mod.Enabled { ... }`, earlier in the function) — nothing about the enabled-mod-set changed there, so there is nothing to sync.

- [ ] **Step 5: Wire `UninstallMod`**

`internal/core/flows.go`, `UninstallMod` ends with `return result, nil` (around line 294, per this task's own investigation). Read the ~15 lines immediately before that return first (`grep -n "^func (s \*Service) UninstallMod" -A 260 internal/core/flows.go | tail -40`) to confirm what `result` variable is in scope and its exact type (`*UninstallResult`, `Notes []string`) before inserting — then insert the same 7-line block immediately before that final `return result, nil`:

```go
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: could not sync merged pak: %v", syncErr))
	} else {
		for _, w := range syncWarnings {
			result.Notes = append(result.Notes, "Warning: "+w)
		}
	}

	return result, nil
```

- [ ] **Step 6: Wire `DeployProfile`**

`internal/core/flows.go:1810-1816` — the existing profile-overrides step is the natural "runs once per DeployProfile call, after all mod files are on disk" anchor (this task's own investigation identified it as the ONLY existing whole-profile step in this function). Insert immediately AFTER that block, still BEFORE the `for _, w := range deferredWarnings { emit(w) }` loop:

```go
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		msg := fmt.Sprintf("syncing merged pak: %v", syncErr)
		result.Warnings = append(result.Warnings, msg)
		emit(DeployProgress{Phase: DeployWarning, Detail: msg})
	} else {
		for _, w := range syncWarnings {
			result.Warnings = append(result.Warnings, w)
			emit(DeployProgress{Phase: DeployWarning, Detail: w})
		}
	}
```

(`DeployResult.Warnings` and `DeployWarning`/`DeployProgress`/`emit` are all pre-existing in this function's scope — no new types needed.)

- [ ] **Step 7: Run the tests, confirm they pass**

Run: `go test ./internal/core/... -run 'TestEnableMod_SyncsMergedPak|TestDisableMod_SyncsMergedPak|TestUninstallMod_SyncsMergedPak|TestDeployProfile_SyncsMergedPak' -v`
Expected: all 4 PASS.

- [ ] **Step 8: Run the full core suite**

Run: `go test ./internal/core/... 2>&1 | tail -100`
Expected: all PASS except `updater.go`'s own tests (Task 9). Pay particular attention to any EXISTING `TestEnableMod_*`/`TestDisableMod_*`/`TestDeployProfile_*` test for a NON-DeployCompile game — `syncMergedPak`'s own `game.DeployMode != domain.DeployCompile` no-op guard (Task 6) must make this wiring invisible to every one of them; if any fails, the guard isn't firing correctly.

- [ ] **Step 9: Commit**

```bash
git add internal/core/flows.go internal/core/merged_pak_hooks_test.go
git commit -m "feat: sync merged pak on enable/disable/uninstall/deploy (#197)"
```

### Task 8: Wire `syncMergedPak` into `ApplyProfileSwitch`/`ApplyUpdate`/`ApplyInstall` + new `Service.ReorderProfileMods`

**Files:**

- Modify: `internal/core/flows.go` (`ApplyProfileSwitch`, `ApplyUpdate`, `ApplyInstall`)
- Modify: `internal/core/profile.go` (new `Service.ReorderProfileMods` — check this file for where `Service`-level profile wrappers already live, e.g. near `NewProfileManager`; if `Service` has no existing profile-wrapper methods in `profile.go`, add it to `internal/core/flows.go` instead, next to `EnableMod`/`DisableMod`)
- Modify: `cmd/lmm/profile.go:831` (call `Service.ReorderProfileMods` instead of `pm.ReorderMods` directly)
- Modify: `internal/tui/service_core.go:978` (same)
- Test: `internal/core/merged_pak_hooks_test.go` (append)

**Interfaces:**

- Consumes: `Service.syncMergedPak` (Task 6); `ProfileManager.ReorderMods(gameID, profileName string, mods []domain.ModReference) error` (existing, unchanged).
- Produces: `Service.ReorderProfileMods(gameID, profileName string, mods []domain.ModReference) error` — consumed by `cmd/lmm/profile.go` and `internal/tui/service_core.go`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/core/merged_pak_hooks_test.go`:

```go
// TestApplyProfileSwitch_SyncsMergedPakForToProfile proves switching TO a
// profile with enabled exmodz mods deploys ITS merged pak (plan.To, not
// plan.From).
func TestApplyProfileSwitch_SyncsMergedPakForToProfile(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	pm := svc.NewProfileManager()
	_, err := pm.Create(game.ID, "other")
	require.NoError(t, err)

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	// Move the mod's profile membership to "other" too, so switching there
	// has something enabled to merge.
	require.NoError(t, pm.UpsertMod(game.ID, "other", domain.ModReference{SourceID: "fake-compiler", ModID: "bear-mount", Version: "1.0", FileIDs: []string{"exmodz-file"}}))
	mod, err := svc.GetInstalledMod("fake-compiler", "bear-mount", game.ID, "default")
	require.NoError(t, err)
	mod.ProfileName = "other"
	require.NoError(t, svc.SaveInstalledMod(mod))

	plan := &core.SwitchPlan{From: "default", To: "other"}
	_, err = svc.ApplyProfileSwitch(context.Background(), game, plan, nil)
	require.NoError(t, err)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	_, err = os.Stat(deployedPath)
	require.NoError(t, err, "ApplyProfileSwitch must sync the merged pak for the TO profile")
}

// TestReorderProfileMods_SyncsMergedPak proves a load-order change (a
// documented regeneration trigger) actually reaches the merged pak.
func TestReorderProfileMods_SyncsMergedPak(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-a", []byte("A"))
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-b", []byte("B"))
	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	before, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "AB", string(before))

	// Swap load order: wolf-mount now first.
	err = svc.ReorderProfileMods(game.ID, "default", []domain.ModReference{
		{SourceID: "fake-compiler", ModID: "wolf-mount", Version: "1.0", FileIDs: []string{"exmodz-b"}},
		{SourceID: "fake-compiler", ModID: "bear-mount", Version: "1.0", FileIDs: []string{"exmodz-a"}},
	})
	require.NoError(t, err)

	_, err = svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	after, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "BA", string(after), "reordering must be reflected in a subsequent sync (fingerprint changed)")
}
```

**`ApplyUpdate`/`ApplyInstall` are exercised indirectly, not with a dedicated new test each** — both already have extensive existing test coverage (`flows_update_test.go`, install-flow test files) that this task's Step 8 (full suite run) must keep green; adding a merged-pak-specific assertion to either would require standing up a much larger fixture (a real update/install flow, not just an enable/disable) for marginal additional confidence beyond what `TestSyncMergedPak_*` (Task 6) and `TestDeployProfile_SyncsMergedPak` (Task 7) already establish for the underlying `syncMergedPak` call itself — the wiring here is mechanically identical to Task 7's, so this task trusts that pattern and spends its test budget on the two NEW code shapes (`ApplyProfileSwitch`'s `plan.To` targeting, `ReorderProfileMods`'s new wrapper) instead.

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `go test ./internal/core/... -run 'TestApplyProfileSwitch_SyncsMergedPakForToProfile|TestReorderProfileMods_SyncsMergedPak' -v`
Expected: `TestApplyProfileSwitch_SyncsMergedPakForToProfile` FAILs (no sync wired yet); `TestReorderProfileMods_SyncsMergedPak` fails to COMPILE (`ReorderProfileMods` undefined).

- [ ] **Step 3: Wire `ApplyProfileSwitch`**

`internal/core/flows.go`, `ApplyProfileSwitch` ends with `return result, nil` (around line 2644, per this task's own investigation — no existing trailing whole-profile step, unlike `DeployProfile`). Insert immediately before it:

```go
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, plan.To); syncErr != nil {
		result.Notes = append(result.Notes, fmt.Sprintf("Warning: could not sync merged pak: %v", syncErr))
	} else {
		for _, w := range syncWarnings {
			result.Notes = append(result.Notes, "Warning: "+w)
		}
	}

	return result, nil
```

(`plan.To`, not `plan.From` — the switch deploys INTO `plan.To`, per this function's own doc comment already read during investigation; `SwitchResult.Notes []string` is the pre-existing field this function already uses for its other non-fatal diagnostics.)

- [ ] **Step 4: Wire `ApplyUpdate`**

`internal/core/flows.go`, `ApplyUpdate` ends with `return result, nil` (around line 4468). Insert immediately before it:

```go
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("syncing merged pak: %v", syncErr))
	} else {
		result.Warnings = append(result.Warnings, syncWarnings...)
	}

	return result, nil
```

(`UpdateApplyResult.Warnings []string` — #196's own field, matching how `ApplyRecompile`, Task 9 below, already reports diagnostics.)

- [ ] **Step 5: Wire `ApplyInstall`**

`internal/core/flows.go`, `ApplyInstall` ends with `return result, nil` (around line 3650). `ApplyInstall` takes `plan *InstallPlan` and `opts InstallOptions` — confirm `opts.ProfileName` (or `plan`'s own profile field — read `internal/core/flows.go`'s `InstallPlan`/`InstallOptions` struct definitions first, `grep -n "type InstallPlan struct\|type InstallOptions struct" -A 15 internal/core/flows.go`) is the correct profile name to pass; using whichever field the function's OWN body already reads for its per-mod `installer.Install(ctx, game, ..., profileName)` calls keeps this consistent. Insert immediately before the final `return result, nil`:

```go
	if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("syncing merged pak: %v", syncErr))
	} else {
		result.Warnings = append(result.Warnings, syncWarnings...)
	}

	return result, nil
```

(`InstallResult.Warnings []string` — confirmed to exist during this task's own investigation, `internal/core/flows.go:3144` area.)

- [ ] **Step 6: Add `Service.ReorderProfileMods`**

Add to `internal/core/profile.go` (near `NewProfileManager`/other `Service`-level profile wrappers — if none exist there, add to `flows.go` next to `EnableMod`):

```go
// ReorderProfileMods persists mods as gameID/profileName's new load order
// (via ProfileManager.ReorderMods) and syncs the merged pak (#197: a
// load-order change is a documented regeneration trigger, since profile
// load order IS merge-application order - see enabledExmodzSources). The
// single seam cmd/lmm and internal/tui both call, replacing their
// previous direct pm.ReorderMods(...) calls (CLI+TUI parity).
//
// A sync failure is non-fatal and returned as part of the SAME error only
// if the reorder itself also failed; a reorder that succeeded but whose
// merged-pak sync failed still returns nil - the reorder took effect, and
// `lmm update`/`lmm verify` are the safety net for a merged pak that
// didn't catch up. Callers wanting to surface a sync warning distinctly
// can call Service.syncMergedPak's own exported test seam directly in a
// follow-up if this proves too quiet in practice; kept simple here to
// match ReorderMods' own existing bare-error signature rather than
// inventing a new result type for one warning slice.
func (s *Service) ReorderProfileMods(gameID, profileName string, mods []domain.ModReference) error {
	pm := NewProfileManager(s.configDir, s.db)
	if err := pm.ReorderMods(gameID, profileName, mods); err != nil {
		return err
	}
	game, ok := s.games[gameID]
	if !ok {
		return nil // an unknown game has no merged pak to sync either
	}
	_, _ = s.syncMergedPak(context.Background(), game, profileName) //nolint:errcheck // best-effort, see doc comment
	return nil
}
```

Add `"context"` to `profile.go`'s import block if not already present.

- [ ] **Step 7: Update the CLI and TUI call sites**

`cmd/lmm/profile.go:831` — change `pm.ReorderMods(game.ID, profileName, newRefs)` to `service.ReorderProfileMods(game.ID, profileName, newRefs)` (read the surrounding ~10 lines first to confirm the local variable name for the `*core.Service` in scope — likely `service`, matching every other command in this package).

`internal/tui/service_core.go:978` — change `pm.ReorderMods(game.ID, profileName, mods)` to `p.svc.ReorderProfileMods(game.ID, profileName, mods)` (matching this file's own `p.svc` receiver convention used throughout `coreProvider`'s other methods).

- [ ] **Step 8: Run the tests, confirm they pass**

Run: `go test ./internal/core/... -run 'TestApplyProfileSwitch_SyncsMergedPakForToProfile|TestReorderProfileMods_SyncsMergedPak' -v`
Expected: both PASS.

- [ ] **Step 9: Run the full build and suite**

Run: `go build ./... 2>&1 | tail -60`
Expected: `internal/core/updater.go` remains the only broken file (Task 9). `cmd/lmm` and `internal/tui` build clean (Step 7's call-site swap is a drop-in replacement — `Service.ReorderProfileMods` has the identical `(gameID, profileName string, mods []domain.ModReference) error` signature `pm.ReorderMods` had, just via `service`/`p.svc` instead of a locally-constructed `pm`).

Run: `go test ./internal/core/... ./cmd/lmm/... ./internal/tui/... 2>&1 | tail -100`
Expected: `internal/core` all green except `updater.go`'s own tests; `cmd/lmm`/`internal/tui` fully green (their `ReorderMods` tests exercise the exact same underlying `pm.ReorderMods` call, now one hop further through `Service`).

- [ ] **Step 10: Commit**

```bash
git add internal/core/flows.go internal/core/profile.go internal/core/merged_pak_hooks_test.go cmd/lmm/profile.go internal/tui/service_core.go
git commit -m "feat: sync merged pak on profile switch/update/install/reorder (#197)"
```

### Task 9: `CheckGameUpdates` gains `profileName` + merged-pak staleness check + `ApplyMergedPakRegen`

**Files:**

- Modify: `internal/core/merged_pak.go` (extract `currentMergedFingerprint`; add `CheckMergedPakStaleness`; add `ApplyMergedPakRegen`)
- Modify: `internal/core/updater.go` (remove `CheckBaseStaleness`, `ApplyRecompile`, `ClassifyRetainedSourceStatError`; change `CheckGameUpdates` signature)
- Modify: `internal/domain/mod.go` (no change — `Update.RecompileNeeded` already exists from #196, reused as-is)
- Test: `internal/core/service_base_staleness_test.go` (rewritten — see Step 6)
- Test: `internal/core/service_apply_recompile_test.go` (deleted — see Step 6)
- Test: `internal/core/updater_test.go` (call-site signature update)

**Interfaces:**

- Consumes: `enabledExmodzSources`/`resolveBasePak`/`basePakIndexHash`/`md5File`/`readMergedFingerprint`/`mergedFingerprintsEqual` (Task 5/6).
- Produces: `Service.CheckGameUpdates(ctx context.Context, game *domain.Game, profileName string, installed []domain.InstalledMod) ([]domain.Update, error)` (SIGNATURE CHANGE — `profileName` inserted as the 3rd parameter); `Service.CheckMergedPakStaleness(game *domain.Game, profileName string) (*domain.Update, error)` (nil, nil when not stale or not applicable); `Service.ApplyMergedPakRegen(ctx context.Context, game *domain.Game, profileName string, progress func(DeployProgress)) (*UpdateApplyResult, error)`.

- [ ] **Step 1: Write the failing tests**

Delete `internal/core/service_apply_recompile_test.go` and `internal/core/service_base_staleness_test.go` entirely (both test `CheckBaseStaleness`/`ApplyRecompile`, which this task removes — their premise, per-mod fingerprinting, no longer exists).

Create `internal/core/merged_pak_staleness_test.go`:

```go
package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestCheckMergedPakStaleness_NotStaleWhenUnchanged(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	upd, err := svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.Nil(t, upd, "an up-to-date merged pak must not be reported stale")
}

func TestCheckMergedPakStaleness_StaleAfterModEnable(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	upd, err := svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.NotNil(t, upd)
	require.True(t, upd.RecompileNeeded)
	require.Equal(t, upd.InstalledMod.Version, upd.NewVersion, "a staleness row has no real version change")
}

func TestCheckMergedPakStaleness_NilWhenNoMergedPakEverGenerated(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	upd, err := svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.Nil(t, upd, "zero enabled exmodz mods means nothing to report - not an error, not a staleness row")
}

func TestCheckMergedPakStaleness_NonCompileGame_Nil(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "skyrim-se", ModPath: t.TempDir(), DeployMode: domain.DeployExtract}
	require.NoError(t, svc.AddGame(game))
	upd, err := svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.Nil(t, upd)
}

// TestApplyMergedPakRegen_Regenerates proves the apply-side wiring: given
// a stale merged pak, applying regenerates and redeploys it.
func TestApplyMergedPakRegen_Regenerates(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	result, err := svc.ApplyMergedPakRegen(context.Background(), game, "default", nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-byteswolf-bytes", string(data))
}

// TestApplyMergedPakRegen_LockedModDiffStillParticipates is the dedicated
// coordinator-flagged design-decision test - see Task 13 for the FULL
// suite; this is the minimal smoke case proving a LOCKED mod's retained
// exmodz is not excluded from a merge triggered by an UNLOCKED mod's
// change.
func TestApplyMergedPakRegen_LockedModDiffStillParticipates(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("locked-bear-bytes"))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(game.ID, "default", "fake-compiler", "bear-mount", ""))

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	_, err := svc.ApplyMergedPakRegen(context.Background(), game, "default", nil)
	require.NoError(t, err, "a locked mod elsewhere in the profile must not block the merge")

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "locked-bear-bytes", "the locked mod's diff must still be included in the merge")
	require.Contains(t, string(data), "wolf-bytes")
}
```

Update `internal/core/updater_test.go`'s existing `CheckUpdates`/`CheckGameUpdates` call sites (search `grep -n "CheckGameUpdates(" internal/core/updater_test.go`) to pass a `profileName` argument — every existing test seeds mods under `"default"`, so pass `"default"` as the new 3rd positional argument.

- [ ] **Step 2: Run the tests, confirm they fail to compile**

Run: `go test ./internal/core/... -run 'TestCheckMergedPakStaleness|TestApplyMergedPakRegen' -v`
Expected: build failure — `undefined: (*core.Service).CheckMergedPakStaleness`, `undefined: (*core.Service).ApplyMergedPakRegen`.

- [ ] **Step 3: Extract `currentMergedFingerprint` from `syncMergedPak`**

In `internal/core/merged_pak.go`, refactor `syncMergedPak`'s fingerprint-building block (everything from `basePakPath, err := resolveBasePak(game)` through the `current.Mods[i].Version = ...` loop) into a new shared function, so `CheckMergedPakStaleness` (Step 4) doesn't duplicate it:

```go
// currentMergedFingerprint computes what game+profileName's merged pak
// SHOULD look like right now: the live base pak's IndexHash plus every
// currently-enabled exmodz mod's identity/version/content checksum, in
// profile load order. Returns a nil sources/zero-value fingerprint (not an
// error) when there is nothing to merge - callers distinguish "nothing to
// do" from "failed to compute" via the returned slice's length, exactly
// like syncMergedPak's own zero-sources branch does.
func (s *Service) currentMergedFingerprint(game *domain.Game, profileName string) (MergedFingerprint, []source.MergeSource, error) {
	sources, err := s.enabledExmodzSources(game, profileName)
	if err != nil {
		return MergedFingerprint{}, nil, fmt.Errorf("listing enabled exmodz mods: %w", err)
	}
	if len(sources) == 0 {
		return MergedFingerprint{}, sources, nil
	}

	basePakPath, err := resolveBasePak(game)
	if err != nil {
		return MergedFingerprint{}, sources, err
	}
	liveHash, err := basePakIndexHash(basePakPath)
	if err != nil {
		return MergedFingerprint{}, sources, fmt.Errorf("reading base pak for merge fingerprint: %w", err)
	}

	current := MergedFingerprint{BaseIndexHash: liveHash}
	for _, src := range sources {
		sum, herr := md5File(src.ExmodzPath)
		if herr != nil {
			return MergedFingerprint{}, sources, fmt.Errorf("hashing %s: %w", src.ExmodzPath, herr)
		}
		sourceID, modID, _ := strings.Cut(src.ModRef, ":")
		current.Mods = append(current.Mods, MergedFingerprintEntry{SourceID: sourceID, ModID: modID, Checksum: sum})
	}

	mods, err := s.GetInstalledModsInProfileOrder(game.ID, profileName)
	if err != nil {
		return MergedFingerprint{}, sources, fmt.Errorf("loading profile mods: %w", err)
	}
	versionByRef := make(map[string]string, len(mods))
	for _, m := range mods {
		versionByRef[m.SourceID+":"+m.ID] = m.Version
	}
	for i, src := range sources {
		current.Mods[i].Version = versionByRef[src.ModRef]
	}

	return current, sources, nil
}
```

Now simplify `syncMergedPak` (Task 6) to call this instead of repeating the block — replace everything from `basePakPath, err := resolveBasePak(game)` through the `current.Mods[i].Version = ...` loop with:

```go
	current, sources, err := s.currentMergedFingerprint(game, profileName)
	if err != nil {
		return nil, err
	}
```

(`sources` here SHADOWS the outer `sources` variable `syncMergedPak` already computed via its own earlier `enabledExmodzSources` call for the zero-check — since `currentMergedFingerprint` recomputes it internally anyway, DELETE `syncMergedPak`'s own earlier `sources, err := s.enabledExmodzSources(...)` call and its zero-length check, replacing BOTH with a single call to `currentMergedFingerprint` right after the `game.DeployMode != domain.DeployCompile` guard, THEN branch on `len(sources) == 0` for the uninstall-to-zero path. Re-read the resulting full function once assembled to confirm there is exactly ONE call to `enabledExmodzSources`, indirectly via `currentMergedFingerprint`, not two.) `basePakPath` is still needed later (the `mc.MergeCompile(ctx, basePakPath, ...)` call) — keep a separate `basePakPath, err := resolveBasePak(game)` call in `syncMergedPak` after the zero-check (cheap — it's just an `os.Stat`, unlike `basePakIndexHash` which `currentMergedFingerprint` already paid for).

- [ ] **Step 4: Implement `CheckMergedPakStaleness`**

Append to `internal/core/merged_pak.go`:

```go
// CheckMergedPakStaleness reports whether game+profileName's merged pak no
// longer matches the current enabled-mod set/order/versions/base pak
// (#197, generalizing #196's per-mod CheckBaseStaleness to the merged
// model). Returns nil, nil - not an error - when the merged pak is
// up to date, when there is nothing to merge (zero enabled exmodz mods),
// or when game is not a DeployCompile game.
func (s *Service) CheckMergedPakStaleness(game *domain.Game, profileName string) (*domain.Update, error) {
	if game.DeployMode != domain.DeployCompile {
		return nil, nil
	}

	current, sources, err := s.currentMergedFingerprint(game, profileName)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, nil
	}

	gameCache := s.GetGameCache(game)
	cachePath := gameCache.ModPath(game.ID, domain.SourceMerged, mergedPakModID, mergedPakVersion)
	stored, ok := readMergedFingerprint(cachePath)
	if ok {
		if eq, eqErr := mergedFingerprintsEqual(current, stored); eqErr == nil && eq {
			return nil, nil
		}
	}

	return &domain.Update{
		InstalledMod: domain.InstalledMod{
			Mod: domain.Mod{
				ID: mergedPakModID, SourceID: domain.SourceMerged,
				Name: "Icarus Merged Pak", Version: mergedPakVersion, GameID: game.ID,
			},
		},
		NewVersion:      mergedPakVersion,
		RecompileNeeded: true,
	}, nil
}
```

- [ ] **Step 5: Implement `ApplyMergedPakRegen`**

Append to `internal/core/merged_pak.go`:

```go
// ApplyMergedPakRegen regenerates game+profileName's merged pak (#197 -
// replaces #196's per-mod ApplyRecompile). No lock gate: a locked mod's
// retained exmodz still participates in every re-merge (design decision 3
// - locking pins THAT mod's own version, it does not freeze the whole
// merged pak or exclude the mod's diff; reading a locked mod's retained
// source to feed the merge is not "touching" it in the sense a lock
// protects against).
func (s *Service) ApplyMergedPakRegen(ctx context.Context, game *domain.Game, profileName string, progress func(DeployProgress)) (*UpdateApplyResult, error) {
	result := &UpdateApplyResult{}
	warnings, err := s.syncMergedPak(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	result.Warnings = warnings
	result.Applied = []string{mergedPakFileName}
	if progress != nil {
		progress(DeployProgress{Phase: UpdateDownloadDone})
	}
	return result, nil
}
```

- [ ] **Step 6: Remove `CheckBaseStaleness`/`ApplyRecompile`/`ClassifyRetainedSourceStatError` from `updater.go`**

Delete these three functions and their doc comments in full from `internal/core/updater.go` (search `grep -n "^func (s \*Service) CheckBaseStaleness\|^func ClassifyRetainedSourceStatError\|^func (s \*Service) ApplyRecompile" internal/core/updater.go` for exact current line ranges — each runs to its own closing `}` before the next function/EOF). Remove now-unused imports this leaves behind (`"io/fs"` was added in #196's review-fix round specifically for `ClassifyRetainedSourceStatError` — check `grep -n '"io/fs"' internal/core/updater.go` and remove it if nothing else in this file uses `fs.` anymore).

- [ ] **Step 7: Change `CheckGameUpdates`'s signature**

`internal/core/updater.go`'s `CheckGameUpdates` — add `profileName string` as the 3rd parameter and replace its `CheckBaseStaleness` call with `CheckMergedPakStaleness`:

```go
func (s *Service) CheckGameUpdates(ctx context.Context, game *domain.Game, profileName string, installed []domain.InstalledMod) ([]domain.Update, error) {
	updates, checkErr := s.NewUpdater().CheckUpdates(ctx, game, installed)

	staleUpd, staleErr := s.CheckMergedPakStaleness(game, profileName)
	if staleErr != nil && checkErr == nil {
		checkErr = staleErr
	}

	if staleUpd != nil {
		reported := false
		for _, u := range updates {
			if u.InstalledMod.SourceID == staleUpd.InstalledMod.SourceID && u.InstalledMod.ID == staleUpd.InstalledMod.ID {
				reported = true
				break
			}
		}
		if !reported {
			updates = append(updates, *staleUpd)
		}
	}

	return updates, checkErr
}
```

(The "already reported" de-dup check that #196's version needed — a mod with BOTH a real update and staleness only reporting the real update — is now moot for the SAME reason it moots itself here too: `staleUpd`'s identity is always the SYNTHETIC merged-pak row, which by construction can never collide with a REAL installed mod's `(SourceID, ID)` pair from `updates`, so the loop above is a defensive no-op today, not load-bearing — kept for clarity/future-proofing rather than removed, since a future change adding a second staleness source could reintroduce exactly this collision.)

- [ ] **Step 8: Run the tests, confirm they pass**

Run: `go test ./internal/core/... -run 'TestCheckMergedPakStaleness|TestApplyMergedPakRegen' -v`
Expected: all 7 PASS, including the locked-mod smoke test.

- [ ] **Step 9: Run the full build and suite**

Run: `go build ./... 2>&1 | tail -80`
Expected: `internal/core` builds clean. `cmd/lmm` and `internal/tui` now fail to build (their `CheckGameUpdates(ctx, game, installed)` call sites are missing the new `profileName` argument) — fixed in Task 10/12.

Run: `go test ./internal/core/... 2>&1 | tail -100`
Expected: all green.

- [ ] **Step 10: Commit**

```bash
git add internal/core/merged_pak.go internal/core/updater.go internal/core/updater_test.go internal/core/merged_pak_staleness_test.go
git rm internal/core/service_apply_recompile_test.go internal/core/service_base_staleness_test.go
git commit -m "feat: CheckMergedPakStaleness + ApplyMergedPakRegen, retire per-mod #196 staleness (#197)"
```

### Task 10: CLI wiring — `cmd/lmm/update.go`

**Files:**

- Modify: `cmd/lmm/update.go` (both `CheckGameUpdates` call sites; `applyRecompile` → calls `ApplyMergedPakRegen`; rendering text unchanged — it already speaks generically about "recompile"/"base pak updated," which reads correctly for the merged pak too)
- Test: `cmd/lmm/update_recompile_test.go` (rewritten fixture — see Step 1)

**Interfaces:**

- Consumes: `Service.CheckGameUpdates(ctx, game, profileName, installed)` (Task 9, new signature); `Service.ApplyMergedPakRegen` (Task 9).
- Produces: nothing new — the existing `updateModJSON.RecompileNeeded`/`Reason` fields (#196) and `singleUpdateJSON` `"recompiled"`/`"recompile_available"` statuses (#196) are REUSED as-is for the merged-pak row; no JSON contract change.

The bulk table's `"[recompile]"` POLICY marker, the single-mod `"Recompiling %s (base pak updated)..."` text, and the `--json` `recompile_needed`/`reason: "stale_compile"` fields were all written generically in #196 (they never say "this specific mod" in a way that stops making sense for a profile-level row) — this task changes ONLY the two `CheckGameUpdates` call sites' argument list and the apply-dispatch target function name; no rendering code changes.

- [ ] **Step 1: Write the failing test**

Rewrite `cmd/lmm/update_recompile_test.go`'s `setupDoUpdateRecompileTest` helper (the shared fixture for all 4 existing tests in that file) to seed a MERGED-PAK-eligible mod instead of a #196-era per-mod-compiled one:

```go
// setupDoUpdateRecompileTest builds a DeployCompile game with a registered
// merge-compiler-capable source and an ENABLED exmodz mod, deliberately
// leaving the merged pak un-generated (or stale, per staleAfterSync) so
// `lmm update` reports/applies a #197 merge-needed row end to end through
// the CLI. linkMethod is LinkCopy so a successful regen+redeploy is
// provable from the on-disk deployed bytes (a symlink would trivially
// reflect an in-place cache swap on its own).
func setupDoUpdateRecompileTest(t *testing.T) (*core.Service, *domain.Game, *compilerInstallSource, string) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	compiler := &compilerInstallSource{fakeInstallSource: newFakeInstallSource("fake-compiler")}
	svc.RegisterSource(compiler)

	game := &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))

	oldSource, oldProfile, oldAll, oldDryRun, oldForce := updateSource, updateProfile, updateAll, updateDryRun, updateForce
	oldVerbose, oldNoColor, oldNoHooks := verbose, noColor, noHooks
	updateSource = "fake-compiler"
	updateProfile = ""
	updateAll = false
	updateDryRun = false
	updateForce = false
	verbose = false
	noColor = true
	noHooks = false
	t.Cleanup(func() {
		updateSource, updateProfile, updateAll, updateDryRun, updateForce = oldSource, oldProfile, oldAll, oldDryRun, oldForce
		verbose, noColor, noHooks = oldVerbose, oldNoColor, oldNoHooks
	})

	const modID, version, fileID = "bear-mount", "3.3", "exmodz-file-id"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, cache.RetainedSourceName(fileID), []byte("retained-exmodz-bytes")))

	im := &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Bear Mount", Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{fileID},
	}
	require.NoError(t, svc.SaveInstalledMod(im))

	pm := svc.NewProfileManager()
	_, cerr := pm.Create(game.ID, "default")
	require.NoError(t, cerr)
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: version, FileIDs: []string{fileID}}))

	return svc, game, compiler, filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
}
```

`compilerInstallSource` (defined in `cmd/lmm/install_compile_test.go`, #173/#196-era) needs its `Compile` method replaced with `ValidateSource`/`MergeCompile`, matching Task 2 Step 1's `fakeCompilerSource` change exactly — apply the identical transformation there too (same struct shape: `*fakeInstallSource` embedded, `compileCalls`/`validateCalls int` fields, `var _ source.MergeCompiler = (*compilerInstallSource)(nil)`).

The 4 existing tests in `cmd/lmm/update_recompile_test.go` (`TestDoUpdate_JSON_ReportsRecompileNeeded`, `TestApplySingleUpdate_Recompile_AppliesAndRedeploys`, `TestApplySingleUpdate_Recompile_JSON`, `TestApplySingleUpdate_Recompile_LockedRefuses`) need NO other changes — they already only reference `row.RecompileNeeded`/`row.Reason`/deployed-file content, which stay semantically valid (now describing the merged pak instead of a per-mod compile). **Exception:** `TestApplySingleUpdate_Recompile_LockedRefuses` currently locks the SAME mod whose staleness row it expects to be refused — under #197, the merged pak is a SEPARATE synthetic mod (`sourceID="lmm-merged"`, `modID="merged-pak"`), and design decision 3 says a locked CONTRIBUTING mod does NOT block the merge. Delete this test — the correct #197 replacement (proving a lock does NOT block, the opposite assertion) lives in Task 13's dedicated locked-mod suite; keeping a test here that asserts the OLD, now-wrong behavior would be actively misleading.

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `go test ./cmd/lmm/... -run 'TestDoUpdate_JSON_ReportsRecompileNeeded|TestApplySingleUpdate_Recompile' -v`
Expected: build failure (`compilerInstallSource` doesn't implement `source.MergeCompiler` until Step 1's transformation is applied there too) and/or `service.CheckGameUpdates` argument-count mismatch until Step 3 lands.

- [ ] **Step 3: Update the two `CheckGameUpdates` call sites**

`cmd/lmm/update.go:319`:

```go
	updates, checkErr := service.CheckGameUpdates(ctx, game, profileName, installed)
```

`cmd/lmm/update.go:601`:

```go
	updates, err := service.CheckGameUpdates(ctx, game, profileName, []domain.InstalledMod{*mod})
```

(`profileName` is already in scope at both call sites — `doUpdate`'s own resolved profile, and `applySingleUpdate`'s parameter of the same name, respectively; confirm by reading the ~15 lines above each call site before editing.)

- [ ] **Step 4: Retarget `applyRecompile`**

`cmd/lmm/update.go`'s `applyRecompile` function — change its final line from `service.ApplyRecompile(ctx, game, profileName, mod, progress)` to `service.ApplyMergedPakRegen(ctx, game, profileName, progress)`, and drop the now-unused `mod domain.InstalledMod` parameter (it was only ever passed through to `ApplyRecompile`, which no longer exists):

```go
// applyRecompile applies a #197 merged-pak staleness row via
// Service.ApplyMergedPakRegen, printing from its progress events the same
// way applyUpdate does for its own (UpdateWarning/UpdateNote are the only
// phases ApplyMergedPakRegen emits - it runs no hooks and downloads
// nothing worth a progress bar).
func applyRecompile(ctx context.Context, service *core.Service, game *domain.Game, profileName string) error {
	progress := func(p core.DeployProgress) {
		switch p.Phase {
		case core.UpdateWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		case core.UpdateNote:
			if verbose && !jsonOutput {
				fmt.Printf("  %s\n", p.Detail)
			}
		}
	}

	_, err := service.ApplyMergedPakRegen(ctx, game, profileName, progress)
	return err
}
```

Update `applyUpdate`'s own call site (`return applyRecompile(ctx, service, game, upd.InstalledMod, profileName)`) to drop the now-removed argument: `return applyRecompile(ctx, service, game, profileName)`.

- [ ] **Step 5: Run the tests, confirm they pass**

Run: `go test ./cmd/lmm/... -run 'TestDoUpdate_JSON_ReportsRecompileNeeded|TestApplySingleUpdate_Recompile' -v`
Expected: all 3 remaining tests PASS (the 4th, `LockedRefuses`, was deleted in Step 1).

- [ ] **Step 6: Run the full `cmd/lmm` suite**

Run: `go test ./cmd/lmm/... 2>&1 | tail -80`
Expected: green. `go build ./... 2>&1 | tail -40` — `internal/tui` remains the only broken package (Task 12).

- [ ] **Step 7: Commit**

```bash
git add cmd/lmm/update.go cmd/lmm/update_recompile_test.go cmd/lmm/install_compile_test.go
git commit -m "feat: cmd/lmm/update.go targets merged-pak regen instead of per-mod recompile (#197)"
```

### Task 11: CLI wiring — `cmd/lmm/verify.go`

**Files:**

- Modify: `cmd/lmm/verify.go:308-340` (replace the per-mod `CheckBaseStaleness` pre-pass with a profile-level `CheckMergedPakStaleness` check)
- Test: `cmd/lmm/verify_recompile_test.go` (rewritten fixture)

**Interfaces:**

- Consumes: `Service.CheckMergedPakStaleness(game, profile)` (Task 9).
- Produces: nothing new — `verifyFileJSON.Status == "stale_compile"` (#196) is REUSED for the merged-pak row.

- [ ] **Step 1: Write the failing test**

Rewrite `cmd/lmm/verify_recompile_test.go`'s two tests to reuse Task 10's rewritten `setupDoUpdateRecompileTest` fixture (same file convention already established — `verify_recompile_test.go` already calls into `update_recompile_test.go`'s helper today, per its own existing structure) with NO source changes needed to the test bodies themselves — `TestDoVerify_StaleCompile_ReportedAsWarning` and `TestDoVerify_StaleCompile_JSON` already just assert on the `"RECOMPILE NEEDED"`/`"stale_compile"` text and `verifyFileJSON` shape, which is unchanged. Confirm by reading `cmd/lmm/verify_recompile_test.go` in full before touching `verify.go` — if it compiles and passes unmodified once Task 10's fixture change lands, skip straight to Step 3; if `svc.SaveFileChecksum("fake-compiler", "bear-mount", game.ID, "default", "exmodz-file-id", "deadbeef")` (a line in the existing test, seeding a checksum so `doVerify`'s OTHER pre-existing checks stay quiet) still makes sense against Task 10's rewritten fixture's exact fileID/modID naming, no change is needed there either.

- [ ] **Step 2: Run the tests, confirm they still describe the intended behavior**

Run: `go test ./cmd/lmm/... -run 'TestDoVerify_StaleCompile' -v`
Expected (before Step 3's `verify.go` change lands): these tests currently call into `doVerify`, which still calls the now-removed `svc.CheckBaseStaleness` — build failure. This confirms Step 3 is required, not skippable.

- [ ] **Step 3: Replace the staleness pre-pass**

`cmd/lmm/verify.go:308-340` (quoted above) — replace the whole `if game.DeployMode == domain.DeployCompile { ... }` block with:

```go
	// Merged-pak staleness check (#197, generalizing #196's per-mod
	// version): for a DeployCompile game, compare the profile's merged
	// pak's recorded fingerprint against the game's CURRENT enabled-mod
	// set/order/versions/base pak. Entirely local/offline. modFilter has no
	// effect here - the merged pak is profile-scoped, not per-mod, so
	// `lmm verify <mod-id>` still checks it (a single mod's own version
	// mismatch and the profile's overall merge staleness are independent
	// facts).
	if game.DeployMode == domain.DeployCompile {
		staleUpd, serr := svc.CheckMergedPakStaleness(game, profile)
		if serr != nil {
			if jsonOutput {
				jsonFiles = append(jsonFiles, verifyFileJSON{Status: "skipped", Note: fmt.Sprintf("could not check merged pak staleness: %v", serr)})
			} else {
				fmt.Printf("%s could not check merged pak staleness: %v\n", colorYellow("?"), serr)
			}
			warnings++
		}
		checked++
		if staleUpd != nil {
			if jsonOutput {
				jsonFiles = append(jsonFiles, verifyFileJSON{ModID: staleUpd.InstalledMod.ID, ModName: staleUpd.InstalledMod.Name, Status: "stale_compile"})
			} else {
				fmt.Printf("%s %s - RECOMPILE NEEDED (base pak updated - run 'lmm update' to fix)\n", colorYellow("?"), staleUpd.InstalledMod.Name)
			}
			warnings++
		}
	}
```

`modFilter`/`installedMods` are no longer read by this block (the check is profile-scoped, not per-mod) — this is intentional per the doc comment above, not a bug; do not restore the old per-mod filtering loop.

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `go test ./cmd/lmm/... -run 'TestDoVerify_StaleCompile' -v`
Expected: both PASS.

- [ ] **Step 5: Run the full `cmd/lmm` suite**

Run: `go test ./cmd/lmm/... 2>&1 | tail -80`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add cmd/lmm/verify.go cmd/lmm/verify_recompile_test.go
git commit -m "feat: lmm verify checks merged-pak staleness at the profile level (#197)"
```

### Task 12: TUI wiring — `internal/tui/service_core.go`

**Files:**

- Modify: `internal/tui/service_core.go` (`coreProvider.CheckUpdates`, `coreProvider.ApplyUpdate`)
- Test: `internal/tui/service_core_recompile_test.go` (rewritten fixture)

**Interfaces:**

- Consumes: `Service.CheckGameUpdates(ctx, game, profileName, installed)` (Task 9, new signature); `Service.ApplyMergedPakRegen` (Task 9).
- Produces: nothing new — `UpdateItem.RecompileNeeded`/`VersionLabel()` (#196) are REUSED as-is.

**Real defect caught while planning this task:** #196's `coreProvider.ApplyUpdate` calls `p.svc.GetInstalledMod(u.Source, u.ID, game.ID, profile)` FIRST, unconditionally, to look up the real `InstalledMod` behind `u` — then re-checks via `CheckGameUpdates` for just that one mod. Under #197 a `RecompileNeeded` row's `u.Source`/`u.ID` are the SYNTHETIC merged-pak identity (`domain.SourceMerged`/`mergedPakModID`), which has NO real `InstalledMod` DB row — `GetInstalledMod` would return an error and abort the whole apply BEFORE ever reaching the dispatch that would have routed it correctly. Fixed below by checking `u.RecompileNeeded` FIRST (already known — `UpdateItem` carries it from the last `CheckUpdates` call, no re-check needed) and short-circuiting straight to `ApplyMergedPakRegen`, mirroring Task 10's identical simplification of `cmd/lmm`'s own `applyRecompile` (which also stopped needing a `mod` parameter).

- [ ] **Step 1: Write the failing test**

Rewrite `internal/tui/service_core_recompile_test.go`'s `newRecompileActionsFixture` to seed a merged-pak-eligible mod (mirroring Task 10's `setupDoUpdateRecompileTest` rewrite exactly — same fixture shape, TUI-layer construction):

```go
func newRecompileActionsFixture(t *testing.T) (tui.ActionProvider, *recompileFakeSource, string) {
	t.Helper()

	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	w, err := unrealpak.Create(basePak)
	require.NoError(t, err)
	require.NoError(t, w.AddFile("Data/D_Fixture.json", []byte(`{"fixture":true}`)))
	require.NoError(t, w.Close())

	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	compiler := &recompileFakeSource{}
	svc.RegisterSource(compiler)

	game := &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))

	pm := svc.NewProfileManager()
	_, err = pm.Create(game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(game.ID, "default"))

	const modID, version, fileID = "bear-mount", "3.3", "exmodz-file-id"
	gameCache := svc.GetGameCache(game)
	require.NoError(t, gameCache.Store(game.ID, "fake-compiler", modID, version, cache.RetainedSourceName(fileID), []byte("retained-exmodz-bytes")))

	im := &domain.InstalledMod{
		Mod:          domain.Mod{ID: modID, SourceID: "fake-compiler", Name: "Bear Mount", Version: version, GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{fileID},
	}
	require.NoError(t, svc.SaveInstalledMod(im))
	require.NoError(t, pm.UpsertMod(game.ID, "default", domain.ModReference{SourceID: "fake-compiler", ModID: modID, Version: version, FileIDs: []string{fileID}}))

	return tui.NewCoreActions(svc, game, "default"), compiler, filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
}
```

`recompileFakeSource` (defined in this same test file) needs its `Compile` method replaced with `ValidateSource`/`MergeCompile`, matching Task 2 Step 1's transformation exactly (same pattern, third occurrence of this identical fake-rewrite across `internal/core`/`cmd/lmm`/`internal/tui`'s own compiler fakes). The two existing tests in this file (`TestCoreProviderActions_CheckUpdates_ReportsRecompileNeeded`, `TestCoreProviderActions_ApplyUpdate_Recompile_AppliesAndRedeploys`) need no other changes — they already only assert on `u.RecompileNeeded`/`u.VersionLabel()`/deployed-file content.

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `go test ./internal/tui/... -run 'TestCoreProviderActions_CheckUpdates_ReportsRecompileNeeded|TestCoreProviderActions_ApplyUpdate_Recompile' -v`
Expected: build failure (`recompileFakeSource` doesn't implement `source.MergeCompiler`; `p.svc.CheckGameUpdates` argument-count mismatch).

- [ ] **Step 3: Update `CheckUpdates`**

`internal/tui/service_core.go`'s `CheckUpdates` — change the `CheckGameUpdates` call:

```go
	updates, checkErr := p.svc.CheckGameUpdates(ctx, game, profile, installed)
```

(`profile` is already in scope — `p.currentProfile()`'s result, assigned two lines above the existing call.)

- [ ] **Step 4: Fix `ApplyUpdate`'s dispatch order**

`internal/tui/service_core.go`'s `ApplyUpdate` — replace the WHOLE function body with:

```go
func (p *coreProvider) ApplyUpdate(ctx context.Context, u UpdateItem, progress func(ActionProgress)) (ActionOutcome, error) {
	game := p.currentGame()
	profile := p.currentProfile()

	adapter := deployProgressAdapter(progress, func(p core.DeployProgress) (ActionProgress, bool) {
		return updateProgressLine(u.Name, p)
	})

	// #197: a RecompileNeeded row's Source/ID are the SYNTHETIC merged-pak
	// identity (domain.SourceMerged/"merged-pak"), which has no real
	// InstalledMod DB row - GetInstalledMod below would error for it. u
	// already carries everything needed (RecompileNeeded is set by the
	// last CheckUpdates call), so this branches BEFORE the GetInstalledMod/
	// re-check path that only makes sense for a real installed mod.
	if u.RecompileNeeded {
		result, err := p.svc.ApplyMergedPakRegen(ctx, game, profile, adapter)
		if err != nil {
			return ActionOutcome{}, mapUpdateNetworkError(fmt.Sprintf("regenerating merged pak for %s", u.Name), u.Source, err)
		}
		return ActionOutcome{
			Message:  fmt.Sprintf("Regenerated %q (base pak or mod set updated)", u.Name),
			Warnings: mergeDiagnostics(result.Warnings, result.Notes),
		}, nil
	}

	mod, err := p.svc.GetInstalledMod(u.Source, u.ID, game.ID, profile)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("getting installed mod %s: %w", u.Name, err)
	}

	updates, err := p.svc.CheckGameUpdates(ctx, game, profile, []domain.InstalledMod{*mod})
	if err != nil {
		return ActionOutcome{}, mapUpdateNetworkError(fmt.Sprintf("checking update for %s", u.Name), u.Source, err)
	}
	if len(updates) == 0 {
		return ActionOutcome{Message: notCheckedMessage(u.Name, *mod)}, nil
	}
	upd := updates[0]

	opts := core.UpdateOptions{
		Hooks:       p.resolvedHooks(game, profile),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(game),
		Force:       false,
	}

	result, err := p.svc.ApplyUpdate(ctx, game, profile, upd, opts, adapter)
	if err != nil {
		return ActionOutcome{}, mapUpdateNetworkError(fmt.Sprintf("updating %s", u.Name), u.Source, err)
	}
	return ActionOutcome{
		Message:  fmt.Sprintf("Updated %q to %s", u.Name, upd.NewVersion),
		Warnings: mergeDiagnostics(result.Warnings, result.Notes),
	}, nil
}
```

(This is the pre-existing function's REAL-update path, verbatim, just moved after the new early `RecompileNeeded` branch instead of running a doomed `GetInstalledMod` call first — `upd.RecompileNeeded`'s OLD re-check-based dispatch, further down in the pre-#197 body, is now unreachable dead code once the early branch exists, since a REAL update's `updates[0]` from `CheckGameUpdates` is never itself a synthetic merged-pak row when `u.RecompileNeeded` was already false going in; delete the old inner `if upd.RecompileNeeded { ... }` block entirely rather than leaving unreachable code behind.)

- [ ] **Step 5: Run the tests, confirm they pass**

Run: `go test ./internal/tui/... -run 'TestCoreProviderActions_CheckUpdates_ReportsRecompileNeeded|TestCoreProviderActions_ApplyUpdate_Recompile' -v`
Expected: both PASS.

- [ ] **Step 6: Run the full `internal/tui` suite, then the full repo**

Run: `go test ./internal/tui/... 2>&1 | tail -100`
Expected: green — in particular, confirm every PRE-EXISTING `TestCoreProviderActions_ApplyUpdate_*` test (a REAL version update, `RecompileNeeded` false) still passes unchanged through the reordered function; the early branch must be a true no-op for them.

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./... 2>&1 | tail -60`
Expected: the ENTIRE repo builds and passes now — this is the first point since Task 2 where every package is simultaneously green.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/service_core.go internal/tui/service_core_recompile_test.go
git commit -m "feat: TUI targets merged-pak regen instead of per-mod recompile (#197)"
```

### Task 13: Locked-mod semantics — dedicated test suite

**Files:**

- Test: `internal/core/merged_pak_locked_test.go` (new)

**Interfaces:**

- Consumes: `Service.syncMergedPak`, `Service.CheckMergedPakStaleness`, `Service.ApplyMergedPakRegen` (Tasks 6/9); `ProfileManager.SetModLock` (existing, unchanged).
- Produces: nothing new — this task is proof, not implementation. **This is the design decision flagged for coordinator confirmation (plan header, Design Decisions item 3).** If the coordinator reverses the decision (a lock SHOULD freeze the whole merge, or exclude the locked mod's diff), every test in this file gets its assertion inverted and `ApplyMergedPakRegen`/`syncMergedPak` gain a lock-gate check mirroring #196's `ApplyRecompile`'s `ErrModLocked` pattern — a small, contained change, which is exactly why this is broken out as its own task rather than folded into Task 6/9.

- [ ] **Step 1: Write the tests**

Create `internal/core/merged_pak_locked_test.go`:

```go
package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLockedMod_DiffStillParticipatesInMerge: a locked mod's retained
// exmodz contributes to the merge exactly like an unlocked one - locking
// pins THAT mod's own VERSION, it does not exclude its diff or freeze the
// merged pak (design decision 3, flagged for coordinator confirmation).
func TestLockedMod_DiffStillParticipatesInMerge(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(game.ID, "default", "fake-compiler", "bear-mount", ""))

	warnings, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err, "a locked mod must not block the merge")
	require.Empty(t, warnings)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-bytes", string(data), "the locked mod's own diff must be included")
}

// TestLockedMod_DoesNotBlockAnotherModsChangeFromReachingTheMerge: enabling
// a SECOND, unlocked mod alongside a locked one must still trigger
// regeneration and include BOTH mods' diffs.
func TestLockedMod_DoesNotBlockAnotherModsChangeFromReachingTheMerge(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(game.ID, "default", "fake-compiler", "bear-mount", ""))
	_, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err)

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "wolf-mount", "1.0", "exmodz-file", []byte("wolf-bytes"))

	warnings, err := svc.SyncMergedPakForTest(context.Background(), game, "default")
	require.NoError(t, err, "a lock on one mod must never block ANOTHER mod's change from reaching the merged pak")
	require.Empty(t, warnings)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	data, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Equal(t, "bear-byteswolf-bytes", string(data), "both mods' diffs must be present - the lock excluded neither")
}

// TestLockedMod_CheckMergedPakStaleness_NotBlockedByLock proves the CHECK
// side (not just apply) also treats a locked mod normally.
func TestLockedMod_CheckMergedPakStaleness_NotBlockedByLock(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(game.ID, "default", "fake-compiler", "bear-mount", ""))

	upd, err := svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.NotNil(t, upd, "a never-yet-generated merged pak is stale regardless of a lock elsewhere in the profile")

	_, err = svc.ApplyMergedPakRegen(context.Background(), game, "default", nil)
	require.NoError(t, err)

	upd, err = svc.CheckMergedPakStaleness(game, "default")
	require.NoError(t, err)
	require.Nil(t, upd, "after applying, the locked mod's presence must not cause a spurious permanent-stale state")
}

// TestLockedMod_ApplyMergedPakRegen_NeverErrorsForALock proves
// ApplyMergedPakRegen has NO lock-gate at all (unlike #196's ApplyRecompile,
// which refused a locked MOD's own recompile) - it is a profile-level
// operation, and design decision 3 explicitly rejects "freeze the whole
// merge on any lock present."
func TestLockedMod_ApplyMergedPakRegen_NeverErrorsForALock(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "bear-mount", "1.0", "exmodz-file", []byte("bear-bytes"))
	pm := svc.NewProfileManager()
	require.NoError(t, pm.SetModLock(game.ID, "default", "fake-compiler", "bear-mount", ""))

	_, err := svc.ApplyMergedPakRegen(context.Background(), game, "default", nil)
	require.NoError(t, err, "ApplyMergedPakRegen must never refuse due to a lock - #196's ErrModLocked gate does not apply here")
}
```

- [ ] **Step 2: Run the tests**

Run: `go test ./internal/core/... -run 'TestLockedMod' -v`
Expected: all 4 PASS, WITHOUT any change to `merged_pak.go` — this task is pure verification that Task 6/9's implementation already has the intended (no lock-gate) behavior, since design decision 3 was already the plan going into Task 6/9's own writing. If any of these 4 fail, STOP and re-examine `syncMergedPak`/`ApplyMergedPakRegen`/`CheckMergedPakStaleness` for an accidental lock-gate check that shouldn't be there (there should be NONE — `grep -n "Locked\|ErrModLocked" internal/core/merged_pak.go` should return zero matches).

- [ ] **Step 3: Commit**

```bash
git add internal/core/merged_pak_locked_test.go
git commit -m "test: pin locked-mod-does-not-block-merge semantics (#197, coordinator-flagged design decision)"
```

### Task 14: CHANGELOG amendment + final full-repo verification

**Files:**

- Modify: `CHANGELOG.md` (amend the #196 `[Unreleased]` bullet in place)

**Interfaces:**

- Consumes: nothing.
- Produces: nothing — this is the plan's closing task.

- [ ] **Step 1: Amend the CHANGELOG bullet**

`CHANGELOG.md`'s `[Unreleased] / Added` section currently reads (in full, current text):

```
- Compiled mods (`deploy_mode: compile`, e.g. Icarus) now recover automatically when the game's base `data.pak` changes underneath them — "the Friday problem": a weekly base-pak refresh used to silently revert a compiled mod's patched tables, with nothing to notice. Compiling now records the base pak's footer fingerprint and retains a copy of the original `.exmodz` beside the compiled `_P.pak`, both invisible to deployment. `lmm update` (CLI and TUI) checks every compiled mod's fingerprint against the game's current base pak and reports a same-version "recompile needed" row (additive `--json` field `recompile_needed`/`reason`) alongside normal version updates; applying it recompiles in place from the retained `.exmodz` (falling back to a re-download when possible) and redeploys — pinned mods recompile normally, locked mods are refused with the same loud lock warning a real update gets. Pre-existing compiled installs without the new fingerprint are left alone rather than guessed at (indistinguishable from a plain prebuilt `.pak`); they pick up fingerprinting on their next real recompile. `lmm verify` gains a matching "RECOMPILE NEEDED" warning row (`stale_compile`) (#196)
```

Replace it (same list position, same `#196` reference retained alongside the new `#197` — this bullet now describes work spanning both issues, since #197 supersedes #196's per-mod behavior before either ever shipped) with:

```
- Compiled mods (`deploy_mode: compile`, e.g. Icarus) with more than one enabled `.exmodz` mod now compose correctly instead of silently shadowing each other: every enabled mod's table-row diffs are applied sequentially, in profile load order, into ONE merged `zzz_LMM_Merged_P.pak` per profile (named to mount last, so it always wins over a plain prebuilt `.pak`'s own table override) — two mods patching different fields of the same row, or entirely different rows of the same table, both survive; only a genuine same-field conflict is last-wins, and a bundled-asset path collision (which can't compose) is last-wins with a loud warning. This also fixes "the Friday problem" (a weekly base-pak refresh silently reverting a mod's patched tables, with nothing to notice): the merge regenerates whenever the enabled-mod set, load order, a mod's version, or the base pak itself changes. `lmm update` (CLI and TUI) reports a "recompile needed" row for the profile's merged pak (additive `--json` field `recompile_needed`/`reason`) alongside normal version updates; applying it regenerates and redeploys — pinned mods' diffs recompile normally, and a LOCKED mod's diff still participates in every merge (a lock pins that mod's own version, not the profile's merged pak). Installing/importing a `.exmodz` now only validates and retains it (a per-mod compiled pak is no longer generated or deployed); a plain prebuilt `.pak` mod, and every non-`deploy_mode: compile` game, is completely unaffected. `lmm verify` gains a matching "RECOMPILE NEEDED" warning row (`stale_compile`) for the profile's merged pak (#136, #175, #196, #197)
```

- [ ] **Step 2: Run `make man` if any CLI `--help`/`Long` text changed**

Task 10/11 did not change any cobra `Long`/`Short` help text (only internal call sites and rendering logic that was already generic) — confirm with `git diff --stat` against `cmd/lmm/update.go`/`cmd/lmm/verify.go`'s `Long:` string literals specifically; if genuinely unchanged, `make man` is a no-op and `go test ./cmd/lmm/... -run TestGenManTree_MatchesCommittedPages` stays green without regenerating. If ANY `Long`/`Short` text drifted during Task 10/11 (e.g. if an implementer added a `#197`-specific clarifying line while there), run `make man` and commit the regenerated `docs/man/` pages alongside this task's CHANGELOG commit.

- [ ] **Step 3: Full-repo verification**

Run, in order, stopping at the first failure:

```bash
gofmt -l . && echo "gofmt clean"
go vet ./... && echo "vet clean"
go build ./... && echo "build clean"
go test ./... 2>&1 | tail -60
trunk check --no-fix $(git diff --name-only c0ca7af..HEAD -- '*.go' | tr '\n' ' ') 2>&1 | tail -80
```

(`c0ca7af` was `develop`'s tip immediately before #196's own branch point — replace with whatever this plan's actual base commit is if it differs by the time implementation starts; the intent is "every `.go` file this whole plan touched, from Task 1 through Task 13".)

Expected: `gofmt`/`go vet`/`go build` all clean; `go test ./...` fully green across every package (`internal/source/icarus`, `internal/storage/cache`, `internal/core`, `cmd/lmm`, `internal/tui`, plus every OTHER untouched package unaffected); `trunk check` reports zero NEW issues (pre-existing issue counts from before this plan started are fine, per this repo's own established convention throughout #172/#173/#189/#190/#196's own review cycles).

- [ ] **Step 4: Manual smoke check (documented, not automatable in this plan)**

This plan cannot execute a real Icarus install (no live game install in CI/this environment) — note explicitly in the implementation report whether a manual smoke test against a real Icarus install was performed (install 2+ real `.exmodz` mods that patch the SAME table, confirm the deployed `zzz_LMM_Merged_P.pak` actually contains both mods' changes in-game) or whether this plan's automated test suite (Task 1's `MergeCompile` tests, Task 6's `syncMergedPak` tests, Task 7/8's hook tests) is the sole verification. Matches this repo's own established precedent (#136/#190's smoke-test call-outs) of being explicit about what WAS and WASN'T verified against the real game, never silently claiming parity with reality that wasn't checked.

- [ ] **Step 5: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: amend #196 CHANGELOG entry for merged-pak compilation (#197)"
```

---

## Self-Review

**1. Spec coverage** (against the task's "APPROVED DESIGN" paragraph, point by point):

- "merged-ONLY model... ONE merged pak per profile, generated at deploy time by applying all enabled mods' retained .EXMOD diffs sequentially" → Task 1 (merge engine), Task 6 (deploy-time generation), Task 2/3 (per-mod paks no longer generated). ✅
- "profile load order = upsert order; sequential upserts give field-level merge" → Task 1's `enabledExmodzSources`-fed, profile-load-order-preserving `[]source.MergeSource`; Task 1's `TestMergeCompile_FieldLevelMergeAcrossMods`/`TestMergeCompile_SameRowSameField_LastWins` extraction-verified. ✅
- "same-path ASSET collisions = last-wins with a loud warning" → Task 1's asset-collision branch + `TestMergeCompile_AssetCollision_LastWinsWithWarning`, propagated through Task 6/9's `warnings` plumbing to CLI/TUI. ✅
- "Per-mod \_P.pak artifacts are no longer generated or deployed" → Task 2/3. ✅
- "install still parses/validates the .exmodz early + retains source + fingerprint in cache" → Task 2/3's `ValidateSource` + `cache.RetainedSourceName` retention; "fingerprint" clarified as the MERGED-level fingerprint (Task 5/6), not a per-mod one (Design Decision 5). ✅
- "Merged pak name sorts LAST in mods/... pick exact name, justify" → Design Decision 1 + Task 5's `mergedPakFileName`. ✅
- "Regeneration triggers: mod set/enable/disable/load order/mod version/base pak change" → Task 6's `syncMergedPak` fingerprint (all 5 dimensions, each with its own extraction-verified test in Task 5); Task 7/8's 8 hook call sites. ✅
- "update shows re-merge rows" → Task 9/10/12. ✅
- "verify checks the merged artifact" → Task 11. ✅
- "locked mods: decide + justify semantics... propose, flag for coordinator" → Design Decision 3 + Task 13. ✅
- "Provenance: the merged pak is a PROFILE-level artifact — design its deployed-file ownership/tracking so uninstall-to-zero removes it and #168-class stale-link hygiene is not worsened" → Design Decision 2 + Task 5 (synthetic mod identity) + Task 6's zero-sources uninstall branch + `TestSyncMergedPak_ZeroEnabledMods_UninstallsExistingPak`/`TestDisableMod_.../TestUninstallMod_...` (Task 7). ✅
- "Plain-pak mods and non-compile games byte-unchanged" → Task 2/3's branches are additive (only the `isExmodzFile` branch changes; the `DeployCopy`/extract branches are untouched); Task 6/9's `game.DeployMode != domain.DeployCompile` guards; explicitly tested (`TestSyncMergedPak_NonCompileGame_NoOp`, `TestCheckMergedPakStaleness_NonCompileGame_Nil`). ✅

**2. Placeholder scan:** every code block in every task is complete, runnable Go (or a literal shell command) — no `TODO`/`...`/"add appropriate handling" appears in any Step's implementation code. Where a step says "read X first" or "confirm via grep", that grep/read is itself the concrete instruction (find the exact current line range before editing), not a stand-in for missing content.

**3. Type consistency:** `source.MergeSource`/`icarus.MergeSource` (Task 1's alias fix, extraction-verified), `MergedFingerprint`/`MergedFingerprintEntry` (Task 5, used identically in Task 6/9/13), `Service.syncMergedPak`/`CheckMergedPakStaleness`/`ApplyMergedPakRegen`/`enabledExmodzSources`/`currentMergedFingerprint` (introduced Task 5/6/9, consumed identically throughout Task 7/8/9/10/11/12/13 with no signature drift), `mergedPakModID`/`mergedPakVersion`/`mergedPakFileName` (Task 5, referenced by exact name in Task 6/9/10/11/12/13's tests) — all consistent across every task that references them.
