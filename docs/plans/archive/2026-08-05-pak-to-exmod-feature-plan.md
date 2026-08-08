# Icarus PAK→EXMOD Merge-Time Conversion Implementation Plan (#221)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pak-only Icarus mods participate in `zzz_LMM_Merged_P.pak` by in-memory conversion at merge time — rebased onto the _current_ `data.pak` — with per-mod error-and-skip falling back to raw deploy.

**Architecture:** Ingest retains the raw `.pak` (like exmodz) but also records a deployable member, so raw-deploy is the default state; `MergeCompile` dispatches per source kind and converts paks in-memory (Tier 1: embedded `data.EXMOD`; Tier 2: table diff vs the already-open current base); `syncMergedPak` learns which mods failed, records outcomes in the fingerprint, and reconciles each pak mod's cache manifest (converted → members=nil → merged-pak claims it; failed/opted-out → members=[pak] → raw deploys). Weekly base updates re-derive automatically via the existing fingerprint.

**Tech Stack:** Go 1.25.6, stdlib + in-repo `internal/unrealpak`, `modernc.org/sqlite`, cobra, bubbletea. No new dependencies.

## Global Constraints

- Branch `feat/pak-to-exmod-convert` off `develop`; PR with explicit `--base develop`. NO version bump; CHANGELOG entries under `[Unreleased]`.
- Conventional commits: `feat:` / `refactor:` / `test:` / `docs:`, referencing `(#221)`.
- Before EVERY commit: `gofmt -l .` (expect no output beyond pre-existing), `go vet ./...`, full `go test ./...` green. NEVER pipe `go test` inside a `&&` chain — run it as its own command and read its real output.
- `internal/core` must NEVER import `internal/source/icarus` (alias pattern via `source.MergeSource` — see internal/source/icarus/merge.go:12-21).
- The spike branch `spike/pak-to-exmod` is a frozen, read-only reference: consult with `git show spike/pak-to-exmod:<path>`, never checkout, never merge. Port logic; do not copy files wholesale. The spike's `WriteExmod`/`WriteExmodz` wire-schema emitters are NOT ported (conversion is in-memory; no synthesized artifact on disk).
- Existing exmodz behavior must remain byte-identical when no pak mods participate (exmodz error policy stays FATAL; warnings text unchanged).
- Rebase semantics are BY DESIGN (user ruling 2026-08-05): drift baked into author-touched rows is not an error. Irreconcilable paks fail whole-mod: error + skip + raw deploy; other mods continue.
- Test fixtures are synthetic paks built with `unrealpak.Create` in `t.TempDir()` — nothing from the mod corpus is ever committed (copyrighted).
- JSON output-contract additions are MINOR (established precedent) — additive fields only, `omitempty` where a field is conditional.
- SQLite tests use `:memory:`; filesystem tests use `t.TempDir()`.

---

### Task 1: Rename MergeSource.ExmodzPath → SourcePath (+ membership function rename)

The field is about to hold `.pak` paths too; the name must stop lying before any behavior changes. Pure mechanical refactor — the compiler is the test.

**Files:**

- Modify: `internal/source/source.go:168-173`
- Modify: `internal/source/icarus/merge.go` (3 sites: 71, 73, 77 + doc comments)
- Modify: `internal/core/merged_pak.go` (sites 113-137 incl. function rename, 143, 294, 296, 313, 315)
- Modify (tests, mechanical): `cmd/lmm/install_compile_test.go:62`, `internal/core/merged_pak_import_flow_test.go:74`, `internal/core/merged_pak_test.go:69`, `internal/core/service_icarus_compile_test.go:88`, `internal/tui/service_core_recompile_test.go:65`, `internal/source/icarus/merge_test.go` (11 sites), plus any `EnabledExmodzSourcesForTest` callers in `internal/core/*_test.go`

**Interfaces:**

- Consumes: nothing.
- Produces: `source.MergeSource{ModRef string; SourcePath string}`; `Service.enabledMergeSources` (was `enabledExmodzSources`); `Service.EnabledMergeSourcesForTest` (was `EnabledExmodzSourcesForTest`). Every later task uses these names.

- [ ] **Step 1: Rename the struct field**

In `internal/source/source.go`, change the `MergeSource` declaration to:

```go
// MergeSource identifies one mod's contribution to a merge, in the order it
// must be applied (profile load order).
type MergeSource struct {
	ModRef     string // "sourceID:modID" - identity used in collision warnings
	SourcePath string // the retained source archive to read (.exmodz, or a raw .pak eligible for conversion - #221)
}
```

- [ ] **Step 2: Chase compile errors**

Run: `go build ./...`
Expected: FAIL with `unknown field ExmodzPath` / `undefined: ... ExmodzPath` in the files listed above.

Fix every site mechanically (`ExmodzPath:` → `SourcePath:`, `.ExmodzPath` → `.SourcePath`). Enumerate with:

Run: `grep -rn 'ExmodzPath' --include='*.go' .`
Expected after fixing: no output.

- [ ] **Step 3: Rename the membership function**

In `internal/core/merged_pak.go`: rename `enabledExmodzSources` → `enabledMergeSources` and `EnabledExmodzSourcesForTest` → `EnabledMergeSourcesForTest`. Update the doc comments: the function returns "every enabled mod's retained merge-source files" (drop "exmodz" specificity; the retained-marker semantics text stays). Update the caller in `currentMergedFingerprint` (merged_pak.go:294) and its error string `"listing enabled exmodz mods"` → `"listing enabled merge sources"`. Fix test callers:

Run: `grep -rn 'EnabledExmodzSourcesForTest\|enabledExmodzSources' --include='*.go' .`
Expected after fixing: no output.

- [ ] **Step 4: Full verification**

Run: `gofmt -l .` → expected: no output.
Run: `go vet ./...` → expected: no output.
Run: `go test ./...`
Expected: PASS everywhere (behavior-preserving rename).

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor: rename MergeSource.ExmodzPath to SourcePath (#221)"
```

---

### Task 2: Converter core — pak entry normalization (internal/source/icarus)

Port the spike's classification logic (case-insensitive, original-case-preserving, hyphen guard) into the icarus package as unexported production code. Reference: `git show spike/pak-to-exmod:spike/pakconvert/normalize.go`.

**Files:**

- Create: `internal/source/icarus/pakconvert.go`
- Create: `internal/source/icarus/pakconvert_test.go`

**Interfaces:**

- Consumes: existing unexported consts `icarusContentMountPoint` (compile.go:131), `icarusDataTablePrefix` (compile.go:140).
- Produces (unexported, same package): `type entryClass int` with `classOther, classTable, classEmbeddedExmod, classAsset` and `String() string`; `func hasPrefixFold(s, prefix string) bool`; `func normalizeEntry(mountPoint, entryPath string) (entryClass, string, error)`; `func currentFileFor(tablePath string) string`. Tasks 3-4 consume all of these.

- [ ] **Step 1: Write the failing test**

`internal/source/icarus/pakconvert_test.go`:

```go
package icarus

import (
	"strings"
	"testing"
)

func TestNormalizeEntry(t *testing.T) {
	tests := []struct {
		name      string
		mount     string
		entry     string
		wantClass entryClass
		wantPath  string
		wantErr   string
	}{
		{
			// Intreegs4XP layout: data/ boundary in the entry path.
			name: "table with data prefix in entry", mount: "../../../Icarus/Content/",
			entry: "data/Experience/D_ExperienceEvents.json",
			wantClass: classTable, wantPath: "Experience/D_ExperienceEvents.json",
		},
		{
			// FloofLevelCap layout: data/ boundary inside the mount point.
			name: "table with data prefix in mount", mount: "../../../Icarus/Content/data/Character/",
			entry: "D_CharacterGrowth.json",
			wantClass: classTable, wantPath: "Character/D_CharacterGrowth.json",
		},
		{
			// Eye Colors Expanded! layout: capital Data/ segment (spike round-3
			// audit) - classification must be case-insensitive but the returned
			// path must preserve ORIGINAL case for base.ReadFile lookups.
			name: "capital Data segment", mount: "../../../Icarus/Content/Data/",
			entry: "Inventory/D_InventoryInfo.json",
			wantClass: classTable, wantPath: "Inventory/D_InventoryInfo.json",
		},
		{
			name: "embedded exmod", mount: "../../../Icarus/Content/",
			entry: "data.EXMOD",
			wantClass: classEmbeddedExmod, wantPath: "data.EXMOD",
		},
		{
			name: "uasset asset", mount: "../../../Icarus/Content/",
			entry: "Mods/Bear/SK_Saddle.uasset",
			wantClass: classAsset, wantPath: "Mods/Bear/SK_Saddle.uasset",
		},
		{
			name: "uexp asset", mount: "../../../Icarus/Content/",
			entry: "Mods/Bear/SK_Saddle.uexp",
			wantClass: classAsset, wantPath: "Mods/Bear/SK_Saddle.uexp",
		},
		{
			// Intreeg's More Resources layout: bare Content/, no Icarus/ segment
			// - unmappable, classifies other (Task 4 turns json-others into a
			// whole-mod error).
			name: "bare content json is other", mount: "../../../Content/",
			entry: "D_ProcessorRecipes.json",
			wantClass: classOther, wantPath: "",
		},
		{
			name: "json outside data dir is other", mount: "../../../Icarus/Content/",
			entry: "Readme/notes.json",
			wantClass: classOther, wantPath: "",
		},
		{
			name: "hyphenated table path errors", mount: "../../../Icarus/Content/",
			entry: "data/AI/D_AI-Growth.json",
			wantClass: classTable, wantErr: "hyphen",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			class, rel, err := normalizeEntry(tt.mount, tt.entry)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if class != tt.wantClass || rel != tt.wantPath {
				t.Fatalf("got (%v, %q), want (%v, %q)", class, rel, tt.wantClass, tt.wantPath)
			}
		})
	}
}

func TestCurrentFileFor(t *testing.T) {
	got := currentFileFor("Audio/MusicConditions/D_MusicLocationConditions.json")
	want := "Audio-MusicConditions-D_MusicLocationConditions.json"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/icarus/ -run 'TestNormalizeEntry|TestCurrentFileFor' -v`
Expected: FAIL — `undefined: normalizeEntry` (compile error).

- [ ] **Step 3: Write the implementation**

`internal/source/icarus/pakconvert.go`:

```go
package icarus

import (
	"fmt"
	"path"
	"strings"
)

// entryClass classifies one mod-pak entry for pak→exmod conversion (#221).
type entryClass int

const (
	classOther entryClass = iota
	classTable
	classEmbeddedExmod
	classAsset
)

func (c entryClass) String() string {
	switch c {
	case classTable:
		return "table"
	case classEmbeddedExmod:
		return "embedded-exmod"
	case classAsset:
		return "asset"
	default:
		return "other"
	}
}

// hasPrefixFold reports whether s starts with prefix, ASCII-case-insensitively.
// UE virtual paths are case-insensitive (the game loads paks regardless of
// "Data/" vs "data/"), so a case-sensitive match silently misclassifies real
// mod paks - the spike's ground-truth audit caught a capital "Data/" mount
// segment doing exactly that (spike findings doc, constraint 4).
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// normalizeEntry joins a mod pak's mount point with one entry path and
// classifies the result. The data/ boundary floats between mount point and
// entry path in real mod paks, so classification always works on the JOINED
// path. For classTable the returned string is the base-table mount-relative
// path (what data.pak's Files() report, and what currentFileFor flattens);
// for classAsset/classEmbeddedExmod it is the Content-relative remainder;
// for classOther it is "". Prefix matching is case-insensitive, but the
// ORIGINAL case of the returned path is preserved - it must match the live
// base pak's actual entry casing for base.ReadFile lookups.
func normalizeEntry(mountPoint, entryPath string) (entryClass, string, error) {
	full := path.Join(mountPoint, entryPath) // Join cleans but keeps leading ../
	if !hasPrefixFold(full, icarusContentMountPoint) {
		return classOther, "", nil
	}
	rest := full[len(icarusContentMountPoint):] // slice, not TrimPrefix: preserve original case
	lower := strings.ToLower(rest)
	switch {
	case strings.HasSuffix(lower, ".exmod"):
		return classEmbeddedExmod, rest, nil
	case strings.HasSuffix(lower, ".uasset") || strings.HasSuffix(lower, ".uexp"):
		return classAsset, rest, nil
	case hasPrefixFold(rest, icarusDataTablePrefix) && strings.HasSuffix(lower, ".json"):
		tablePath := rest[len(icarusDataTablePrefix):] // slice: preserve original case
		if strings.Contains(tablePath, "-") {
			// The CurrentFile encoding replaces ALL '/' with '-' and is only
			// reversible because no real base-table path contains a hyphen
			// (matchMountPath, compile.go). A hyphen here would produce an
			// unresolvable CurrentFile.
			return classTable, "", fmt.Errorf("icarus: table path %q contains a hyphen: CurrentFile flattening would be ambiguous", tablePath)
		}
		return classTable, tablePath, nil
	default:
		return classOther, "", nil
	}
}

// currentFileFor flattens a base-table mount-relative path into the .EXMOD
// CurrentFile encoding (forward direction of matchMountPath's reversal).
func currentFileFor(tablePath string) string {
	return strings.ReplaceAll(tablePath, "/", "-")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/icarus/ -run 'TestNormalizeEntry|TestCurrentFileFor' -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Full suite + commit**

Run: `gofmt -l .` → no output. Run: `go vet ./...` → no output.
Run: `go test ./...`
Expected: PASS.

```bash
git add internal/source/icarus/pakconvert.go internal/source/icarus/pakconvert_test.go
git commit -m "feat: pak entry normalization for pak-to-exmod conversion (#221)"
```

---

### Task 3: Converter core — table differ producing ExmodFileItem upserts

Diff a pak's (stale, whole-file) table snapshot against the CURRENT base table, emitting `ExmodFileItem` upserts directly — no wire-schema JSON round-trip. Reference: `git show spike/pak-to-exmod:spike/pakconvert/diff.go`, with feature-grade policy changes: RowStruct mismatch is now a hard error (irreconcilable, per design §4); Defaults/top-level/field-removed/duplicate-name are warnings.

**Files:**

- Modify: `internal/source/icarus/pakconvert.go` (append)
- Modify: `internal/source/icarus/pakconvert_test.go` (append)

**Interfaces:**

- Consumes: `ExmodFileItem{Name string; Fields map[string]any}` (exmod.go:29-33).
- Produces (unexported): `func diffTable(tableRef string, baseJSON, modJSON []byte) (items []ExmodFileItem, warnings []string, err error)`. Task 4 consumes it. `tableRef` is used only to prefix warning/error text.

- [ ] **Step 1: Write the failing test**

Append to `internal/source/icarus/pakconvert_test.go`:

```go
func TestDiffTable(t *testing.T) {
	base := []byte(`{
		"RowStruct": "/Script/Icarus.Growth",
		"Defaults": {"XP": 1},
		"Rows": [
			{"Name": "RowA", "XP": 10, "Level": 1},
			{"Name": "RowB", "XP": 20, "Level": 2},
			{"Name": "BaseOnly", "XP": 30}
		]
	}`)

	t.Run("changed field emits whole field, new row emits all fields, base-only ignored", func(t *testing.T) {
		mod := []byte(`{
			"RowStruct": "/Script/Icarus.Growth",
			"Defaults": {"XP": 1},
			"Rows": [
				{"Name": "RowA", "XP": 99, "Level": 1},
				{"Name": "NewRow", "XP": 5, "Nested": {"Value": "x"}}
			]
		}`)
		items, warnings, err := diffTable("Test/D_Growth.json", base, mod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(warnings) != 0 {
			t.Fatalf("unexpected warnings: %v", warnings)
		}
		if len(items) != 2 {
			t.Fatalf("want 2 items, got %d: %+v", len(items), items)
		}
		if items[0].Name != "RowA" || len(items[0].Fields) != 1 || items[0].Fields["XP"] != float64(99) {
			t.Fatalf("RowA item wrong: %+v", items[0])
		}
		if items[1].Name != "NewRow" || len(items[1].Fields) != 2 {
			t.Fatalf("NewRow item wrong: %+v", items[1])
		}
	})

	t.Run("identical row emits nothing", func(t *testing.T) {
		mod := []byte(`{
			"RowStruct": "/Script/Icarus.Growth",
			"Defaults": {"XP": 1},
			"Rows": [{"Name": "RowA", "XP": 10, "Level": 1}]
		}`)
		items, warnings, err := diffTable("Test/D_Growth.json", base, mod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(items) != 0 || len(warnings) != 0 {
			t.Fatalf("want no items/warnings, got %+v / %v", items, warnings)
		}
	})

	t.Run("rowstruct mismatch is a hard error", func(t *testing.T) {
		mod := []byte(`{
			"RowStruct": "/Script/Icarus.SomethingElse",
			"Defaults": {"XP": 1},
			"Rows": [{"Name": "RowA", "XP": 10, "Level": 1}]
		}`)
		_, _, err := diffTable("Test/D_Growth.json", base, mod)
		if err == nil || !strings.Contains(err.Error(), "RowStruct") {
			t.Fatalf("want RowStruct error, got %v", err)
		}
	})

	t.Run("defaults and field-removed and duplicate are warnings", func(t *testing.T) {
		mod := []byte(`{
			"RowStruct": "/Script/Icarus.Growth",
			"Defaults": {"XP": 2},
			"Rows": [
				{"Name": "RowA", "XP": 10},
				{"Name": "RowA", "XP": 11, "Level": 1}
			]
		}`)
		items, warnings, err := diffTable("Test/D_Growth.json", base, mod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// RowA first occurrence: XP unchanged (10), Level field REMOVED vs
		// base -> warning, no item (nothing changed that EXMOD can express).
		if len(items) != 0 {
			t.Fatalf("want 0 items, got %+v", items)
		}
		var haveDefaults, haveRemoved, haveDup bool
		for _, w := range warnings {
			if strings.Contains(w, "Defaults") {
				haveDefaults = true
			}
			if strings.Contains(w, "cannot remove fields") {
				haveRemoved = true
			}
			if strings.Contains(w, "duplicate row") {
				haveDup = true
			}
		}
		if !haveDefaults || !haveRemoved || !haveDup {
			t.Fatalf("missing expected warnings: %v", warnings)
		}
	})

	t.Run("malformed table errors", func(t *testing.T) {
		_, _, err := diffTable("Test/D_Growth.json", base, []byte(`{"NoRows": true}`))
		if err == nil {
			t.Fatal("want error for table without Rows")
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/icarus/ -run TestDiffTable -v`
Expected: FAIL — `undefined: diffTable`.

- [ ] **Step 3: Write the implementation**

Append to `internal/source/icarus/pakconvert.go` (add `"encoding/json"`, `"reflect"`, `"sort"` to imports):

```go
// pakDataTable is the UE DataTable export shape {"RowStruct","Defaults","Rows"}.
type pakDataTable struct {
	rows  []map[string]any
	other map[string]any // every top-level key except Rows
}

func parsePakDataTable(data []byte) (*pakDataTable, error) {
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parsing data table: %w", err)
	}
	rawRows, ok := top["Rows"].([]any)
	if !ok {
		return nil, fmt.Errorf("data table has no Rows array")
	}
	t := &pakDataTable{other: top}
	delete(top, "Rows")
	for i, r := range rawRows {
		row, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Rows[%d] is not an object", i)
		}
		if _, ok := row["Name"].(string); !ok {
			return nil, fmt.Errorf("Rows[%d] has no string Name", i)
		}
		t.rows = append(t.rows, row)
	}
	return t, nil
}

// diffTable derives the EXMOD-expressible difference between the CURRENT base
// table and a mod pak's (possibly stale) full-table snapshot - the Tier 2
// rebase (#221 design §1):
//
//   - row in pak, not in base      -> new row: emit all fields
//   - row in both, fields differ   -> emit Name + changed fields (whole-field;
//     this is the accepted rebase semantic - drift in author-touched rows
//     rides along BY DESIGN, user ruling 2026-08-05)
//   - row in base, not in pak      -> staleness: ignored (EXMOD has no delete)
//   - RowStruct mismatch           -> hard error (irreconcilable: the table's
//     schema changed under the pak; a field-level rebase is meaningless)
//   - Defaults/top-level changes, pak row missing a base field, duplicate
//     Names (first wins)           -> warnings; conversion proceeds
func diffTable(tableRef string, baseJSON, modJSON []byte) (items []ExmodFileItem, warnings []string, err error) {
	base, err := parsePakDataTable(baseJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("icarus: %s: base: %w", tableRef, err)
	}
	mod, err := parsePakDataTable(modJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("icarus: %s: pak table: %w", tableRef, err)
	}

	if !reflect.DeepEqual(base.other["RowStruct"], mod.other["RowStruct"]) {
		return nil, nil, fmt.Errorf("icarus: %s: RowStruct differs from current base (pak schema is irreconcilable)", tableRef)
	}

	// Non-Rows top-level keys: union of both sides, sorted for deterministic
	// warning order.
	otherKeys := make(map[string]bool)
	for key := range base.other {
		if key != "RowStruct" {
			otherKeys[key] = true
		}
	}
	for key := range mod.other {
		if key != "RowStruct" {
			otherKeys[key] = true
		}
	}
	sortedKeys := make([]string, 0, len(otherKeys))
	for key := range otherKeys {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)
	for _, key := range sortedKeys {
		if !reflect.DeepEqual(base.other[key], mod.other[key]) {
			warnings = append(warnings, fmt.Sprintf("%s: top-level key %q (e.g. Defaults) differs from base - EXMOD cannot express this; base value kept", tableRef, key))
		}
	}

	baseByName := make(map[string]map[string]any, len(base.rows))
	for _, r := range base.rows {
		baseByName[r["Name"].(string)] = r
	}

	seen := make(map[string]bool, len(mod.rows))
	for _, mr := range mod.rows {
		name := mr["Name"].(string)
		if seen[name] {
			warnings = append(warnings, fmt.Sprintf("%s: duplicate row %q in pak table; first occurrence wins", tableRef, name))
			continue
		}
		seen[name] = true

		br, inBase := baseByName[name]
		if !inBase {
			fields := make(map[string]any, len(mr)-1)
			for k, v := range mr {
				if k != "Name" {
					fields[k] = v
				}
			}
			items = append(items, ExmodFileItem{Name: name, Fields: fields})
			continue
		}
		changed := map[string]any{}
		for k, v := range mr {
			if k == "Name" {
				continue
			}
			if bv, ok := br[k]; !ok || !reflect.DeepEqual(bv, v) {
				changed[k] = v
			}
		}
		for k := range br {
			if k == "Name" {
				continue
			}
			if _, ok := mr[k]; !ok {
				warnings = append(warnings, fmt.Sprintf("%s: row %q: field %q present in base but absent in pak (EXMOD cannot remove fields; base value kept)", tableRef, name, k))
			}
		}
		if len(changed) > 0 {
			items = append(items, ExmodFileItem{Name: name, Fields: changed})
		}
	}
	// Base-only rows are staleness (the pak predates them) - deliberately
	// ignored, never deletions.
	return items, warnings, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/icarus/ -run TestDiffTable -v`
Expected: PASS (all 5 subtests).

- [ ] **Step 5: Full suite + commit**

Run: `gofmt -l .` → no output. Run: `go vet ./...` → no output.
Run: `go test ./...`
Expected: PASS.

```bash
git add internal/source/icarus/pakconvert.go internal/source/icarus/pakconvert_test.go
git commit -m "feat: table differ deriving exmod upserts from pak snapshots (#221)"
```

---

### Task 4: convertPakToBundle — Tier 1 / Tier 2 orchestration

Convert one pak into an in-memory `*ExmodzBundle` (the exact shape `MergeCompile`'s loops already consume). Tier 1: exactly one embedded `.EXMOD` → `ParseExmod` its rows verbatim (pure author intent, rebased by the normal upsert path). Tier 2: diff every table snapshot. Whole-mod failure on: unreadable pak, unreadable/hyphenated/unmapped-json entries, table absent from current base, RowStruct mismatch, multiple embedded manifests.

**Files:**

- Modify: `internal/source/icarus/pakconvert.go` (append)
- Modify: `internal/source/icarus/pakconvert_test.go` (append)

**Interfaces:**

- Consumes: `normalizeEntry`, `currentFileFor`, `diffTable` (Tasks 2-3); `ExmodzBundle{Diff *ExmodDiff; Assets map[string][]byte}` (exmodz.go:13-16); `ExmodDiff`/`ExmodRow`/`ExmodFileItem` (exmod.go:11-33); `ParseExmod(data []byte) (*ExmodDiff, error)` (exmod.go:35); `unrealpak.Open/Reader`.
- Produces (unexported): `func convertPakToBundle(pakPath string, base *unrealpak.Reader) (*ExmodzBundle, []string, error)`. Task 5 consumes it. Asset map keys are Content-relative paths, fed through the merge loop's existing `sanitizeAssetPath` gate unchanged.

- [ ] **Step 1: Write the failing test**

Append to `internal/source/icarus/pakconvert_test.go` (add imports `"os"`, `"path/filepath"`, and `"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"`):

```go
// buildTestPak writes a synthetic pak with the given mount point and entries.
func buildTestPak(t *testing.T, dir, name, mount string, entries map[string][]byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	w, err := unrealpak.Create(p, unrealpak.WithMountPoint(mount))
	if err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	for path, data := range entries {
		if err := w.AddFile(path, data); err != nil {
			t.Fatalf("adding %s: %v", path, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing %s: %v", name, err)
	}
	return p
}

// testBaseTable is a minimal real-shape UE DataTable.
const testBaseTable = `{"RowStruct":"/Script/Icarus.Growth","Defaults":{},"Rows":[{"Name":"RowA","XP":10},{"Name":"RowB","XP":20}]}`

func openTestBase(t *testing.T, dir string) *unrealpak.Reader {
	t.Helper()
	// Base data.pak entries are mount-relative WITHOUT a data/ prefix
	// (matchMountPath resolves "Test/D_Growth.json" against Files()).
	basePath := buildTestPak(t, dir, "data.pak", "../../../Icarus/Content/Data/", map[string][]byte{
		"Test/D_Growth.json": []byte(testBaseTable),
	})
	base, err := unrealpak.Open(basePath)
	if err != nil {
		t.Fatalf("opening base: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	return base
}

func TestConvertPakToBundleTier2(t *testing.T) {
	dir := t.TempDir()
	base := openTestBase(t, dir)
	modTable := `{"RowStruct":"/Script/Icarus.Growth","Defaults":{},"Rows":[{"Name":"RowA","XP":99},{"Name":"RowNew","XP":5}]}`
	pak := buildTestPak(t, dir, "mod.pak", "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Growth.json":     []byte(modTable),
		"Mods/Thing/SK_Thing.uasset":  {0x01, 0x02},
		"readme.txt":                  []byte("ignore me"),
	})

	bundle, warnings, err := convertPakToBundle(pak, base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(bundle.Diff.Rows) != 1 {
		t.Fatalf("want 1 row, got %+v", bundle.Diff.Rows)
	}
	row := bundle.Diff.Rows[0]
	if row.CurrentFile != "Test-D_Growth.json" {
		t.Fatalf("CurrentFile = %q", row.CurrentFile)
	}
	if len(row.FileItems) != 2 {
		t.Fatalf("want 2 items (changed RowA + new RowNew), got %+v", row.FileItems)
	}
	if _, ok := bundle.Assets["Mods/Thing/SK_Thing.uasset"]; !ok {
		t.Fatalf("asset missing: %+v", bundle.Assets)
	}
}

func TestConvertPakToBundleTier1EmbeddedExmod(t *testing.T) {
	dir := t.TempDir()
	base := openTestBase(t, dir)
	embedded := `{"Rows":[{"CurrentFile":"Test-D_Growth.json","File_Items":[{"Name":"RowA","XP":42}]},{"CurrentFile":"EndOfMod"}]}`
	// The pak ALSO carries a stale table snapshot - Tier 1 must ignore it in
	// favor of the embedded manifest (exact author intent).
	staleTable := `{"RowStruct":"/Script/Icarus.Growth","Defaults":{},"Rows":[{"Name":"RowA","XP":42},{"Name":"Ancient","XP":1}]}`
	pak := buildTestPak(t, dir, "mod.pak", "../../../Icarus/Content/", map[string][]byte{
		"data.EXMOD":              []byte(embedded),
		"data/Test/D_Growth.json": []byte(staleTable),
	})

	bundle, _, err := convertPakToBundle(pak, base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 2 rows: the real one + the EndOfMod sentinel ParseExmod preserves.
	if len(bundle.Diff.Rows) != 2 {
		t.Fatalf("want embedded manifest's 2 rows, got %+v", bundle.Diff.Rows)
	}
	if bundle.Diff.Rows[0].FileItems[0].Fields["XP"] != float64(42) {
		t.Fatalf("embedded row not used: %+v", bundle.Diff.Rows[0])
	}
}

func TestConvertPakToBundleIrreconcilable(t *testing.T) {
	dir := t.TempDir()
	base := openTestBase(t, dir)

	t.Run("table not in current base", func(t *testing.T) {
		pak := buildTestPak(t, dir, "gone.pak", "../../../Icarus/Content/", map[string][]byte{
			"data/Removed/D_Gone.json": []byte(testBaseTable),
		})
		_, _, err := convertPakToBundle(pak, base)
		if err == nil || !strings.Contains(err.Error(), "not present in current base") {
			t.Fatalf("want table-not-in-base error, got %v", err)
		}
	})

	t.Run("unmappable json entry", func(t *testing.T) {
		pak := buildTestPak(t, dir, "bare.pak", "../../../Content/", map[string][]byte{
			"D_ProcessorRecipes.json": []byte(testBaseTable),
		})
		_, _, err := convertPakToBundle(pak, base)
		if err == nil || !strings.Contains(err.Error(), "unresolvable") {
			t.Fatalf("want unresolvable-layout error, got %v", err)
		}
	})

	t.Run("multiple embedded manifests", func(t *testing.T) {
		pak := buildTestPak(t, dir, "multi.pak", "../../../Icarus/Content/", map[string][]byte{
			"a.EXMOD": []byte(`{"Rows":[]}`),
			"b.EXMOD": []byte(`{"Rows":[]}`),
		})
		_, _, err := convertPakToBundle(pak, base)
		if err == nil || !strings.Contains(err.Error(), "multiple embedded") {
			t.Fatalf("want multiple-embedded error, got %v", err)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/icarus/ -run TestConvertPakToBundle -v`
Expected: FAIL — `undefined: convertPakToBundle`.

- [ ] **Step 3: Write the implementation**

Append to `internal/source/icarus/pakconvert.go` (add `"sort"` already present; add `"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"` to imports):

```go
// convertPakToBundle converts one prebuilt mod pak into the in-memory
// ExmodzBundle shape MergeCompile's loops already consume - the pak→exmod
// REBASE (#221): rebuild the pak's changes against the CURRENT base.
//
// Tier 1: when the pak embeds exactly one *.EXMOD manifest, its rows are
// used verbatim (pure author intent; the merge loop's resolveCurrentFile +
// ApplyRowPatch rebase them onto the current base). Tier 2: otherwise every
// table snapshot is diffed against the current base via diffTable.
//
// A non-nil error means the WHOLE mod is irreconcilable (design §4): the
// caller must skip it (it falls back to raw deploy) - never partially
// convert. Warnings are non-fatal observations (inexpressible details).
func convertPakToBundle(pakPath string, base *unrealpak.Reader) (*ExmodzBundle, []string, error) {
	mod, err := unrealpak.Open(pakPath)
	if err != nil {
		return nil, nil, fmt.Errorf("icarus: opening pak %s: %w", pakPath, err)
	}
	defer mod.Close() //nolint:errcheck

	type tableSnapshot struct {
		rel  string
		data []byte
	}
	var (
		warnings  []string
		tables    []tableSnapshot
		embedded  [][]byte
		assets    = map[string][]byte{}
	)

	for _, entry := range mod.Files() {
		class, rel, nerr := normalizeEntry(mod.MountPoint(), entry.Path)
		if nerr != nil {
			return nil, nil, nerr // hyphen-ambiguous table path: irreconcilable
		}
		if class == classOther {
			if strings.HasSuffix(strings.ToLower(entry.Path), ".json") {
				// A JSON entry we cannot place under Icarus/Content/data is
				// almost certainly a table with an unresolvable layout (e.g.
				// a bare Content/ mount with no directory structure) - a
				// silent skip would drop the mod's actual content.
				return nil, nil, fmt.Errorf("icarus: pak layout unresolvable: entry %q (mount %q) does not map into Icarus/Content/data", entry.Path, mod.MountPoint())
			}
			continue // readme, images, etc.
		}
		data, rerr := mod.ReadFile(entry.Path)
		if rerr != nil {
			if class == classAsset {
				warnings = append(warnings, fmt.Sprintf("asset %q unreadable (%v) - skipped", entry.Path, rerr))
				continue
			}
			// Unreadable table or embedded manifest: the mod's content is
			// unrecoverable - irreconcilable.
			return nil, nil, fmt.Errorf("icarus: reading pak entry %q: %w", entry.Path, rerr)
		}
		switch class {
		case classEmbeddedExmod:
			embedded = append(embedded, data)
		case classAsset:
			assets[rel] = data
		case classTable:
			tables = append(tables, tableSnapshot{rel: rel, data: data})
		}
	}

	if len(embedded) > 1 {
		return nil, nil, fmt.Errorf("icarus: pak %s carries multiple embedded .EXMOD manifests - ambiguous", pakPath)
	}

	if len(embedded) == 1 {
		diff, perr := ParseExmod(embedded[0])
		if perr != nil {
			return nil, nil, fmt.Errorf("icarus: embedded .EXMOD in %s: %w", pakPath, perr)
		}
		return &ExmodzBundle{Diff: diff, Assets: assets}, warnings, nil
	}

	// Tier 2: diff-derive. Deterministic row order: sort snapshots by path.
	sort.Slice(tables, func(i, j int) bool { return tables[i].rel < tables[j].rel })
	diff := &ExmodDiff{}
	for _, snap := range tables {
		baseData, berr := base.ReadFile(snap.rel)
		if berr != nil {
			return nil, nil, fmt.Errorf("icarus: pak table %q not present in current base: %w", snap.rel, berr)
		}
		items, tblWarnings, derr := diffTable(snap.rel, baseData, snap.data)
		if derr != nil {
			return nil, nil, derr
		}
		warnings = append(warnings, tblWarnings...)
		if len(items) == 0 {
			continue // nothing expressible changed; never emit an empty row
		}
		diff.Rows = append(diff.Rows, ExmodRow{CurrentFile: currentFileFor(snap.rel), FileItems: items})
	}
	return &ExmodzBundle{Diff: diff, Assets: assets}, warnings, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/icarus/ -run TestConvertPakToBundle -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Full suite + commit**

Run: `gofmt -l .` → no output. Run: `go vet ./...` → no output.
Run: `go test ./...`
Expected: PASS.

```bash
git add internal/source/icarus/pakconvert.go internal/source/icarus/pakconvert_test.go
git commit -m "feat: convertPakToBundle with embedded-EXMOD tier and diff tier (#221)"
```

---

### Task 5: MergeCompile pak dispatch, per-mod failure policy, MergeFailure interface change

`MergeCompile` gains a per-source kind dispatch and a structured `failed` return so core can reconcile manifests. Exmodz error policy stays FATAL and byte-identical; pak failures are per-mod (warning + skip + report). Pak application is transactional per mod (scratch state, committed only on success) so a failing pak cannot half-pollute the merge.

**Files:**

- Modify: `internal/source/source.go` (MergeCompiler interface, new MergeFailure type, MergeSource.Kind field)
- Modify: `internal/source/icarus/merge.go` (dispatch + apply refactor)
- Modify: `internal/source/icarus/icarus.go:50-51` (method wrapper signature)
- Modify: `internal/core/merged_pak.go:236-240` (call site — minimal adaptation; full use in Task 8)
- Modify (test doubles, mechanical signature updates): `cmd/lmm/install_compile_test.go`, `internal/core/flows_variant_exclusivity_test.go`, `internal/core/merged_pak_import_flow_test.go`, `internal/core/service_icarus_compile_test.go`, `internal/core/service_test.go`, `internal/tui/service_core_recompile_test.go`
- Test: `internal/source/icarus/merge_test.go` (new cases; existing cases updated for signature)

**Interfaces:**

- Consumes: `convertPakToBundle` (Task 4).
- Produces:
  - `source.MergeSource{ModRef, SourcePath string; Kind string}` with consts `source.MergeSourceExmodz = "exmodz"`, `source.MergeSourcePak = "pak"` (empty Kind = exmodz, back-compat).
  - `source.MergeFailure{ModRef string; Reason string}`.
  - Interface + package func + method signature: `MergeCompile(ctx context.Context, basePakPath string, sources []MergeSource, outputPakPath string) (warnings []string, failed []MergeFailure, err error)`.
  - `icarus.ValidateSource` widened: `.pak` files validate via `unrealpak.Open` + non-empty `Files()`.
    Tasks 8-9 consume all of these.

- [ ] **Step 1: Write the failing test**

Append to `internal/source/icarus/merge_test.go` (it already builds synthetic base paks and exmodz files — reuse its existing helpers; the new test builds a pak with Task 4's `buildTestPak` helper from `pakconvert_test.go`, same package):

```go
func TestMergeCompilePakSource(t *testing.T) {
	dir := t.TempDir()
	basePath := buildTestPak(t, dir, "data.pak", "../../../Icarus/Content/Data/", map[string][]byte{
		"Test/D_Growth.json": []byte(testBaseTable),
	})
	modTable := `{"RowStruct":"/Script/Icarus.Growth","Defaults":{},"Rows":[{"Name":"RowA","XP":99}]}`
	pakPath := buildTestPak(t, dir, "mod.pak", "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Growth.json": []byte(modTable),
	})
	out := filepath.Join(dir, "merged.pak")

	warnings, failed, err := MergeCompile(context.Background(), basePath, []MergeSource{
		{ModRef: "icarus:pakmod", SourcePath: pakPath, Kind: source.MergeSourcePak},
	}, out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(failed) != 0 || len(warnings) != 0 {
		t.Fatalf("unexpected failed/warnings: %v / %v", failed, warnings)
	}
	merged, err := unrealpak.Open(out)
	if err != nil {
		t.Fatalf("opening merged: %v", err)
	}
	defer merged.Close()
	data, err := merged.ReadFile("data/Test/D_Growth.json")
	if err != nil {
		t.Fatalf("merged table missing: %v", err)
	}
	if !strings.Contains(string(data), `"XP":99`) {
		t.Fatalf("converted change not applied: %s", data)
	}
}

func TestMergeCompilePakFailureSkipsModOnly(t *testing.T) {
	dir := t.TempDir()
	basePath := buildTestPak(t, dir, "data.pak", "../../../Icarus/Content/Data/", map[string][]byte{
		"Test/D_Growth.json": []byte(testBaseTable),
	})
	// Irreconcilable pak: its table does not exist in the current base.
	badPak := buildTestPak(t, dir, "bad.pak", "../../../Icarus/Content/", map[string][]byte{
		"data/Removed/D_Gone.json": []byte(testBaseTable),
	})
	goodTable := `{"RowStruct":"/Script/Icarus.Growth","Defaults":{},"Rows":[{"Name":"RowB","XP":77}]}`
	goodPak := buildTestPak(t, dir, "good.pak", "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Growth.json": []byte(goodTable),
	})
	out := filepath.Join(dir, "merged.pak")

	warnings, failed, err := MergeCompile(context.Background(), basePath, []MergeSource{
		{ModRef: "icarus:bad", SourcePath: badPak, Kind: source.MergeSourcePak},
		{ModRef: "icarus:good", SourcePath: goodPak, Kind: source.MergeSourcePak},
	}, out)
	if err != nil {
		t.Fatalf("per-mod failure must not be fatal: %v", err)
	}
	if len(failed) != 1 || failed[0].ModRef != "icarus:bad" {
		t.Fatalf("want icarus:bad failed, got %+v", failed)
	}
	var haveWarning bool
	for _, w := range warnings {
		if strings.Contains(w, "icarus:bad") && strings.Contains(w, "deploying raw") {
			haveWarning = true
		}
	}
	if !haveWarning {
		t.Fatalf("want a deploying-raw warning for icarus:bad, got %v", warnings)
	}
	merged, err := unrealpak.Open(out)
	if err != nil {
		t.Fatalf("opening merged: %v", err)
	}
	defer merged.Close()
	data, err := merged.ReadFile("data/Test/D_Growth.json")
	if err != nil {
		t.Fatalf("good mod's table missing: %v", err)
	}
	if !strings.Contains(string(data), `"XP":77`) {
		t.Fatalf("good mod's change not applied: %s", data)
	}
	if _, err := merged.ReadFile("data/Removed/D_Gone.json"); err == nil {
		t.Fatal("failed mod's table must not be in the merged pak")
	}
}

func TestValidateSourcePak(t *testing.T) {
	dir := t.TempDir()
	pakPath := buildTestPak(t, dir, "ok.pak", "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Growth.json": []byte(testBaseTable),
	})
	if err := ValidateSource(pakPath); err != nil {
		t.Fatalf("valid pak rejected: %v", err)
	}
	badPath := filepath.Join(dir, "not-a.pak")
	if err := os.WriteFile(badPath, []byte("junk"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSource(badPath); err == nil {
		t.Fatal("junk .pak must fail validation")
	}
}
```

(Add imports as needed: `"context"`, `"os"`, `"path/filepath"`, `"strings"`, `"github.com/DonovanMods/linux-mod-manager/internal/source"`, `"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"` — some already present in merge_test.go.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/icarus/ -run 'TestMergeCompilePak|TestValidateSourcePak' -v`
Expected: FAIL — `undefined: source.MergeSourcePak` / wrong result count (compile errors).

- [ ] **Step 3: Update source.go**

In `internal/source/source.go`, extend `MergeSource`, add `MergeFailure`, and change the interface:

```go
// Merge-source kinds (#221). An empty Kind means MergeSourceExmodz - every
// pre-#221 constructor built exmodz-only sources and never set a kind.
const (
	MergeSourceExmodz = "exmodz"
	MergeSourcePak    = "pak"
)

// MergeSource identifies one mod's contribution to a merge, in the order it
// must be applied (profile load order).
type MergeSource struct {
	ModRef     string // "sourceID:modID" - identity used in collision warnings
	SourcePath string // the retained source archive to read (.exmodz, or a raw .pak eligible for conversion - #221)
	Kind       string // MergeSourceExmodz (default when empty) or MergeSourcePak
}

// MergeFailure records one source that could not participate in a merge
// (#221: an irreconcilable pak). The merge itself still succeeds - the
// failed mod is skipped and falls back to raw deploy; core uses this list
// to reconcile cache manifests and record outcomes in the fingerprint.
type MergeFailure struct {
	ModRef string
	Reason string
}
```

Change the `MergeCompiler` interface method (keep the doc comment, append to it):

```go
	// MergeCompile applies every entry in sources, in order (profile load
	// order), against basePakPath's tables, and writes the merged result to
	// outputPakPath. Returns non-fatal warnings (e.g. same-path asset
	// collisions - last-applied wins) alongside a nil error; a nil error
	// with warnings is still a fully-written, deployable pak. Pak-kind
	// sources that cannot be converted are skipped per-mod and reported in
	// failed (#221) - only exmodz-source errors and I/O failures are fatal.
	MergeCompile(ctx context.Context, basePakPath string, sources []MergeSource, outputPakPath string) (warnings []string, failed []MergeFailure, err error)
```

- [ ] **Step 4: Rework icarus MergeCompile**

In `internal/source/icarus/merge.go`, replace the function body's per-source region (lines 70-118) and the signature. The apply logic is extracted so both kinds share it, with pak sources applying to SCRATCH state committed only on success:

```go
func MergeCompile(ctx context.Context, basePakPath string, sources []MergeSource, outputPakPath string) (warnings []string, failed []source.MergeFailure, err error) {
	base, err := unrealpak.Open(basePakPath)
	if err != nil {
		return nil, nil, fmt.Errorf("icarus: opening base pak %s: %w", basePakPath, err)
	}
	defer base.Close() //nolint:errcheck

	tableState := make(map[string][]byte) // mountPath -> current (possibly already patched) JSON bytes
	assets := make(map[string][]byte)     // final asset path -> data (last source wins)
	assetOwner := make(map[string]string) // asset path -> ModRef that last set it

	for _, src := range sources {
		if src.Kind == source.MergeSourcePak {
			bundle, convWarnings, cerr := convertPakToBundle(src.SourcePath, base)
			if cerr != nil {
				failed = append(failed, source.MergeFailure{ModRef: src.ModRef, Reason: cerr.Error()})
				warnings = append(warnings, fmt.Sprintf("mod %s: pak conversion failed: %v - deploying raw", src.ModRef, cerr))
				continue
			}
			for _, w := range convWarnings {
				warnings = append(warnings, fmt.Sprintf("mod %s: %s", src.ModRef, w))
			}
			// Apply on scratch copies: a Tier 1 row can still fail
			// resolveCurrentFile against the CURRENT base (the manifest may
			// reference a since-removed table), and a half-applied mod must
			// not pollute the merge.
			scratch := make(map[string][]byte, len(tableState))
			for k, v := range tableState {
				scratch[k] = v
			}
			applyWarnings, aerr := applyBundle(base, scratch, assets, assetOwner, bundle, src.ModRef)
			if aerr != nil {
				failed = append(failed, source.MergeFailure{ModRef: src.ModRef, Reason: aerr.Error()})
				warnings = append(warnings, fmt.Sprintf("mod %s: pak conversion failed: %v - deploying raw", src.ModRef, aerr))
				continue
			}
			warnings = append(warnings, applyWarnings...)
			tableState = scratch
			continue
		}

		// Exmodz source: policy unchanged - any error is fatal (#197).
		exmodzData, rerr := os.ReadFile(src.SourcePath)
		if rerr != nil {
			return warnings, failed, fmt.Errorf("icarus: reading %s: %w", src.SourcePath, rerr)
		}
		bundle, perr := ParseExmodz(exmodzData)
		if perr != nil {
			return warnings, failed, fmt.Errorf("icarus: %s: %w", src.SourcePath, perr)
		}
		applyWarnings, aerr := applyBundle(base, tableState, assets, assetOwner, bundle, src.ModRef)
		if aerr != nil {
			return warnings, failed, aerr
		}
		warnings = append(warnings, applyWarnings...)
	}
	// ... (writer section below unchanged except signature returns)
```

Extract the two loops (rows + assets, formerly merge.go:80-117) verbatim into:

```go
// applyBundle applies one bundle's row upserts and assets to the merge
// state. Asset collisions across mods warn (last-applied wins); any other
// error is returned to the caller, which decides fatality by source kind.
func applyBundle(base *unrealpak.Reader, tableState map[string][]byte, assets map[string][]byte, assetOwner map[string]string, bundle *ExmodzBundle, modRef string) (warnings []string, err error) {
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
			var rerr error
			current, rerr = base.ReadFile(mountPath)
			if rerr != nil {
				return warnings, fmt.Errorf("icarus: reading base data table %s: %w", mountPath, rerr)
			}
		}
		patched, perr := ApplyRowPatch(current, row)
		if perr != nil {
			return warnings, perr
		}
		tableState[mountPath] = patched
	}

	for assetPath, data := range bundle.Assets {
		safePath, serr := sanitizeAssetPath(assetPath)
		if serr != nil {
			return warnings, serr
		}
		if owner, exists := assetOwner[safePath]; exists && owner != modRef {
			warnings = append(warnings, fmt.Sprintf(
				"asset %q is bundled by both %s and %s - %s wins (last-applied, per profile load order)",
				safePath, owner, modRef, modRef))
		}
		assets[safePath] = data
		assetOwner[safePath] = modRef
	}
	return warnings, nil
}
```

The writer section (merge.go:120-149) is unchanged except every `return warnings, ...` becomes `return warnings, failed, ...`. NOTE the known asset-scratch caveat: a pak mod that fails AFTER contributing assets could leave assets behind — but `applyBundle` applies ALL rows before ANY assets, and pak failures inside `applyBundle` can only occur in the rows loop or the sanitize call, so at most the failing mod's own earlier assets are at risk; to keep it strictly transactional, pak sources buffer assets too: pass fresh `scratchAssets := map[string][]byte{}` / `scratchOwner` copies the same way as `scratch` (copy both maps, commit both on success). Implement it that way — copy all three maps for the pak branch.

Widen `ValidateSource` (merge.go:28-37):

```go
// ValidateSource parses sourceFilePath without compiling anything - the
// ingest-time check. .exmodz archives fully parse (#197); .pak files (#221)
// open + enumerate only - full conversion is checked at merge time BY
// DESIGN (the result depends on the current base pak, which changes weekly).
func ValidateSource(sourceFilePath string) error {
	if strings.HasSuffix(strings.ToLower(sourceFilePath), ".pak") {
		r, err := unrealpak.Open(sourceFilePath)
		if err != nil {
			return fmt.Errorf("icarus: validating %s: %w", sourceFilePath, err)
		}
		defer r.Close() //nolint:errcheck
		if len(r.Files()) == 0 {
			return fmt.Errorf("icarus: validating %s: pak contains no entries", sourceFilePath)
		}
		return nil
	}
	data, err := os.ReadFile(sourceFilePath)
	if err != nil {
		return fmt.Errorf("icarus: reading %s: %w", sourceFilePath, err)
	}
	if _, err := ParseExmodz(data); err != nil {
		return fmt.Errorf("icarus: validating %s: %w", sourceFilePath, err)
	}
	return nil
}
```

(Add `"strings"` and the `source` import to merge.go's imports if missing.)

Update the method wrapper in `internal/source/icarus/icarus.go`:

```go
func (s *Icarus) MergeCompile(ctx context.Context, basePakPath string, sources []MergeSource, outputPakPath string) ([]string, []source.MergeFailure, error) {
	return MergeCompile(ctx, basePakPath, sources, outputPakPath)
}
```

- [ ] **Step 5: Adapt call site and test doubles**

`internal/core/merged_pak.go:236` (temporary until Task 8 uses `failed`):

```go
	mergeWarnings, mergeFailed, err := mc.MergeCompile(ctx, basePakPath, sources, outputPath)
	if err != nil {
		return nil, fmt.Errorf("merging %d merge source(s): %w", len(sources), err)
	}
	warnings = mergeWarnings
	_ = mergeFailed // consumed in the fingerprint/manifest reconcile (#221 Task 8)
```

Update every fake MergeCompiler in the 6 test files listed above: signature gains `[]source.MergeFailure` (return `nil` for it). Compile-error-driven:

Run: `go build ./...` then `go vet ./...`
Expected after fixes: no output.

Existing `merge_test.go` call sites (`MergeCompile(...)` two-value returns at lines 29, 73, 116, 154, 198, 240 vicinity) gain the middle `failed` return — assert `len(failed) == 0` in each existing happy-path test.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/source/icarus/ -v`
Expected: PASS — all existing merge/compile tests (byte-identical exmodz behavior) plus the three new tests.

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat: MergeCompile pak dispatch with per-mod failure policy (#221)"
```

---

### Task 6: Per-game convert_paks config

`convert_paks: true|false` in games.yaml, default true. Follows the LinkMethod/LinkMethodExplicit precedent exactly (the only pattern that survives the selective write-back in `saveGamesLocked` — recon: any field not copied there is silently dropped on SaveGame).

**Files:**

- Modify: `internal/domain/game.go:50-62` (two fields)
- Modify: `internal/storage/config/games.go` (GameConfig, loadGamesLocked, saveGamesLocked)
- Modify: `cmd/lmm/game_list.go` (JSON + human surface)
- Test: `internal/storage/config/games_test.go` (append), `cmd/lmm/game_list_test.go` if present (append) else fold assertion into config test

**Interfaces:**

- Consumes: nothing new.
- Produces: `domain.Game.ConvertPaks bool` (effective value, default true), `domain.Game.ConvertPaksExplicit bool`. Tasks 8-9-11 consume `game.ConvertPaks`.

- [ ] **Step 1: Write the failing test**

Append to `internal/storage/config/games_test.go` (mirror the file's existing Load/Save round-trip test style — it writes YAML to a temp config dir and calls LoadGames/SaveGame):

```go
func TestConvertPaksDefaultAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	yaml := `games:
    icarus:
        name: Icarus
        install_path: /tmp/icarus
        mod_path: /tmp/icarus/mods
        sources:
            icarus: icarus
        deploy_mode: compile
    explicit-off:
        name: Off
        install_path: /tmp/off
        mod_path: /tmp/off/mods
        sources:
            icarus: icarus
        deploy_mode: compile
        convert_paks: false
`
	writeGamesYAML(t, dir, yaml) // use the file's existing helper for writing games.yaml; if named differently, reuse that helper

	games, err := LoadGamesFrom(dir) // use the file's existing load-from-dir seam
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !games["icarus"].ConvertPaks || games["icarus"].ConvertPaksExplicit {
		t.Fatalf("absent convert_paks must default true/implicit, got %+v", games["icarus"])
	}
	if games["explicit-off"].ConvertPaks || !games["explicit-off"].ConvertPaksExplicit {
		t.Fatalf("explicit false must load false/explicit, got %+v", games["explicit-off"])
	}

	// Round-trip: saving the explicit-off game must preserve convert_paks: false.
	g := games["explicit-off"]
	if err := SaveGameTo(dir, &g); err != nil { // the file's existing save seam
		t.Fatalf("save: %v", err)
	}
	reloaded, err := LoadGamesFrom(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded["explicit-off"].ConvertPaks {
		t.Fatal("convert_paks: false lost on save round-trip")
	}
}
```

NOTE: `writeGamesYAML` / `LoadGamesFrom` / `SaveGameTo` stand for the test file's EXISTING helpers and public seams — read `internal/storage/config/games_test.go` first and use its actual names (the package's public API is `LoadGames`/`SaveGame` with a config-dir parameter or env override; mirror whichever the sibling tests use). The assertions above are the requirement; the harness comes from the file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/config/ -run TestConvertPaks -v`
Expected: FAIL — `unknown field ConvertPaks` (compile error).

- [ ] **Step 3: Implement**

`internal/domain/game.go` — append to the `Game` struct:

```go
	ConvertPaks         bool // #221: convert prebuilt .pak mods into the merged pak (DeployCompile games; default true)
	ConvertPaksExplicit bool // True if ConvertPaks was explicitly set in config (round-trip fidelity, like LinkMethodExplicit)
```

`internal/storage/config/games.go`:

```go
// In GameConfig:
	ConvertPaks *bool `yaml:"convert_paks,omitempty"`
```

In `loadGamesLocked`'s per-game block (beside the ParseDeployMode handling):

```go
		convertPaks := true // default: paks convert (only meaningful for DeployCompile games)
		convertExplicit := false
		if gc.ConvertPaks != nil {
			convertPaks = *gc.ConvertPaks
			convertExplicit = true
		}
```

and in the `domain.Game` literal: `ConvertPaks: convertPaks, ConvertPaksExplicit: convertExplicit,`.

In `saveGamesLocked`'s selective write-back (beside the LinkMethodExplicit block):

```go
		if game.ConvertPaksExplicit {
			v := game.ConvertPaks
			gc.ConvertPaks = &v
		}
```

`cmd/lmm/game_list.go`: add to the JSON struct `ConvertPaks *bool \`json:"convert_paks,omitempty"\``populated only when`g.DeployMode == domain.DeployCompile` (`v := g.ConvertPaks; row.ConvertPaks = &v`), and in the human output print `convert_paks: true|false`on the same detail line as`deploy_mode` for compile-mode games.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/storage/config/ -run TestConvertPaks -v` → PASS.
Run: `go test ./...` → PASS.
Run: `gofmt -l .` → no output. Run: `go vet ./...` → no output.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: per-game convert_paks config with explicit round-trip (#221)"
```

---

### Task 7: Per-mod convert flag in SQLite

`convert_paks INTEGER DEFAULT 1` on installed_mods, following the migrateV8/UpdateModPolicy patterns exactly. The column is EXCLUDED from SaveInstalledMod's INSERT column list entirely — the schema default governs on first insert and the ON CONFLICT update can't touch it, so a user-set flag survives reinstall (stronger than update_policy's approach, no callers need changing).

**Files:**

- Modify: `internal/storage/db/migrations.go` (append migrateV12 to the slice + function)
- Modify: `internal/storage/db/mods.go` (SELECT/Scan in GetInstalledMods + GetInstalledMod; new setter; SaveInstalledMod doc note)
- Modify: `internal/domain/mod.go:107-120` (InstalledMod field)
- Modify: `internal/core/service.go:1286-1309` region (pass-through)
- Test: `internal/storage/db/mods_test.go` (append; `:memory:` per existing pattern)

**Interfaces:**

- Consumes: nothing new.
- Produces: `domain.InstalledMod.ConvertPaks bool`; `(*DB).SetModConvertPaks(sourceID, modID, gameID, profileName string, convert bool) error`; `(*Service).SetModConvertPaks(...)` same params. Tasks 8-10-12 consume these.

- [ ] **Step 1: Write the failing test**

Append to `internal/storage/db/mods_test.go` (reuse the file's existing in-memory DB constructor and InstalledMod fixture helpers — read the sibling tests for exact names):

```go
func TestSetModConvertPaks(t *testing.T) {
	d := newTestDB(t) // the file's existing :memory: constructor helper
	mod := &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m1", SourceID: "icarus", GameID: "icarus", Name: "M", Version: "1.0"},
		ProfileName:  "default",
		Enabled:      true,
	}
	if err := d.SaveInstalledMod(mod); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := d.GetInstalledMod("icarus", "m1", "icarus", "default")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.ConvertPaks {
		t.Fatal("convert_paks must default to true (schema DEFAULT 1)")
	}

	if err := d.SetModConvertPaks("icarus", "m1", "icarus", "default", false); err != nil {
		t.Fatalf("set off: %v", err)
	}
	got, err = d.GetInstalledMod("icarus", "m1", "icarus", "default")
	if err != nil {
		t.Fatalf("get 2: %v", err)
	}
	if got.ConvertPaks {
		t.Fatal("convert_paks not persisted off")
	}

	// Reinstall (SaveInstalledMod upsert) must NOT reset the user's flag.
	if err := d.SaveInstalledMod(mod); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	got, err = d.GetInstalledMod("icarus", "m1", "icarus", "default")
	if err != nil {
		t.Fatalf("get 3: %v", err)
	}
	if got.ConvertPaks {
		t.Fatal("reinstall reset convert_paks - the column must stay out of the upsert")
	}

	// Unknown mod -> domain.ErrModNotFound (targeted-setter contract).
	if err := d.SetModConvertPaks("icarus", "nope", "icarus", "default", true); err != domain.ErrModNotFound {
		t.Fatalf("want ErrModNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/storage/db/ -run TestSetModConvertPaks -v`
Expected: FAIL — `undefined: (*DB).SetModConvertPaks` / `unknown field ConvertPaks`.

- [ ] **Step 3: Implement**

`internal/storage/db/migrations.go` — append `migrateV12` to the `migrations` slice and add:

```go
func migrateV12(d *DB) error {
	// #221: per-mod pak-to-exmod conversion opt-out. Default 1 = convert
	// (paks join the merged pak); 0 = deploy raw. Deliberately excluded
	// from SaveInstalledMod's upsert so reinstall preserves the user's
	// choice - changes go through SetModConvertPaks only.
	_, err := d.Exec(`ALTER TABLE installed_mods ADD COLUMN convert_paks INTEGER DEFAULT 1`)
	return err
}
```

`internal/domain/mod.go` — append to InstalledMod:

```go
	ConvertPaks bool // #221: pak-to-exmod conversion enabled (default true; only meaningful for DeployCompile games)
```

`internal/storage/db/mods.go`:

- `GetInstalledMods` SELECT gains `, convert_paks` at the end of the column list; Scan gains `&mod.ConvertPaks` in matching position. Same for `GetInstalledMod`.
- `SaveInstalledMod`: INSERT list UNCHANGED (column absent → schema default 1). Extend the doc comment: "convert_paks is likewise never written here - the schema default covers first insert, and SetModConvertPaks is the only writer, so reinstall can't reset it."
- New setter (exact `UpdateModPolicy` shape, mods.go:193-214):

```go
// SetModConvertPaks sets the per-mod pak-conversion flag (#221).
func (d *DB) SetModConvertPaks(sourceID, modID, gameID, profileName string, convert bool) error {
	result, err := d.Exec(`
		UPDATE installed_mods SET convert_paks = ?
		WHERE source_id = ? AND mod_id = ? AND game_id = ? AND profile_name = ?
	`, convert, sourceID, modID, gameID, profileName)
	if err != nil {
		return fmt.Errorf("updating mod convert_paks: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		// A driver error here must not be read as "0 rows affected" -
		// that would misreport a real DB failure as domain.ErrModNotFound.
		return fmt.Errorf("updating mod convert_paks: checking rows affected: %w", err)
	}
	if rows == 0 {
		return domain.ErrModNotFound
	}

	return nil
}
```

`internal/core/service.go` — beside SetModUpdatePolicy/SetModEnabled:

```go
// SetModConvertPaks toggles per-mod pak-to-exmod conversion (#221). A local
// DB write; the caller re-syncs the merged pak to apply the change.
func (s *Service) SetModConvertPaks(sourceID, modID, gameID, profileName string, convert bool) error {
	return s.db.SetModConvertPaks(sourceID, modID, gameID, profileName, convert)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/storage/db/ -run TestSetModConvertPaks -v` → PASS.
Run: `go test ./...` → PASS (migration runs everywhere the suite opens a DB).
Run: `gofmt -l .` → no output. Run: `go vet ./...` → no output.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: per-mod convert_paks column, setter, and service seam (#221)"
```

---

### Task 8: Merge membership, outcome-aware fingerprint, manifest reconcile

The core wiring: pak retained-sources join `enabledMergeSources` (honoring both opt-outs, carrying Kind), the fingerprint records conversion OUTCOMES without them affecting input equality (retry only when inputs change), and `syncMergedPak` reconciles each pak mod's cache manifest to the merge outcome — converted → members=nil (merged pak claims it; raw link undeployed), failed/opted-out → members=[pak] (raw deploys). Public `SyncMergedPak` signature is UNCHANGED (`[]string, error`) so its ~13 callers stay untouched.

**Files:**

- Modify: `internal/core/merged_pak.go` (enabledMergeSources, MergedFingerprintEntry, equality, currentMergedFingerprint, syncMergedPak, new reconcile helpers)
- Test: `internal/core/merged_pak_test.go` (append), plus a flow test appended to `internal/core/service_icarus_compile_test.go`

**Interfaces:**

- Consumes: `source.MergeSourcePak/MergeSourceExmodz`, `source.MergeFailure` (Task 5); `domain.InstalledMod.ConvertPaks` (Task 7); `domain.Game.ConvertPaks` (Task 6); `cache.MarkFileCompleteWithMembers`, `cache.FileManifests`, `cache.RetainedSourceName`, `gameCache.ListFiles`.
- Produces: `MergedFingerprintEntry{SourceID, ModID, Version, Checksum, Kind string; Converted bool; FailReason string}`; `func mergeSourceKind(fileID string) string`; stored fingerprints now carry per-mod outcomes (Task 11's verify reads them). `readMergedFingerprint` already exported enough for verify via `Service` helpers — add `(*Service).MergedPakOutcomes(game, profileName) ([]MergedFingerprintEntry, bool)` for Tasks 11-12.

- [ ] **Step 1: Write the failing pure-logic tests**

Append to `internal/core/merged_pak_test.go`:

```go
func TestMergeSourceKind(t *testing.T) {
	tests := map[string]string{
		"pak":         source.MergeSourcePak,
		"MyMod.PAK":   source.MergeSourcePak,
		"exmodz":      source.MergeSourceExmodz,
		"MyMod.exmodz": source.MergeSourceExmodz,
		"weird.zip":   source.MergeSourceExmodz, // unknown retained kind: today's behavior
	}
	for fileID, want := range tests {
		if got := mergeSourceKind(fileID); got != want {
			t.Errorf("mergeSourceKind(%q) = %q, want %q", fileID, got, want)
		}
	}
}

func TestFingerprintEqualityIgnoresOutcomes(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "h", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m", Version: "1", Checksum: "c", Kind: source.MergeSourcePak, Converted: true},
	}}
	b := MergedFingerprint{BaseIndexHash: "h", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m", Version: "1", Checksum: "c", Kind: source.MergeSourcePak, Converted: false, FailReason: "x"},
	}}
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("outcome fields must not affect input equality (retry only when inputs change)")
	}

	c := b
	c.Mods = []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m", Version: "2", Checksum: "c", Kind: source.MergeSourcePak}}
	eq, err = mergedFingerprintsEqual(a, c)
	if err != nil {
		t.Fatal(err)
	}
	if eq {
		t.Fatal("input fields (Version) must affect equality")
	}
}

func TestReadOldFingerprintMarkerCompat(t *testing.T) {
	// A pre-#221 marker has no Kind/Converted/FailReason - it must read
	// fine and compare EQUAL to a current computation with the same inputs
	// (Kind "" vs "exmodz" must not force a spurious regen).
	dir := t.TempDir()
	old := []byte(`{"BaseIndexHash":"h","Mods":[{"SourceID":"icarus","ModID":"m","Version":"1","Checksum":"c"}]}`)
	if err := os.WriteFile(cache.MergeFingerprintPath(dir), old, 0644); err != nil {
		t.Fatal(err)
	}
	stored, ok := readMergedFingerprint(dir)
	if !ok {
		t.Fatal("old marker unreadable")
	}
	current := MergedFingerprint{BaseIndexHash: "h", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m", Version: "1", Checksum: "c", Kind: source.MergeSourceExmodz, Converted: true},
	}}
	eq, err := mergedFingerprintsEqual(current, stored)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("pre-#221 marker must not trigger a regen for unchanged exmodz inputs")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/ -run 'TestMergeSourceKind|TestFingerprintEquality|TestReadOldFingerprintMarker' -v`
Expected: FAIL — `undefined: mergeSourceKind` / `unknown field Kind`.

- [ ] **Step 3: Implement membership + fingerprint**

In `internal/core/merged_pak.go`:

```go
// mergeSourceKind classifies a retained-source fileID (#221). Download-path
// icarus fileIDs are literally "pak"/"exmodz"; import-path fileIDs are the
// archive's own filename. Unknown kinds default to exmodz - the only kind
// that existed before #221.
func mergeSourceKind(fileID string) string {
	lower := strings.ToLower(fileID)
	if lower == "pak" || strings.HasSuffix(lower, ".pak") {
		return source.MergeSourcePak
	}
	return source.MergeSourceExmodz
}
```

`enabledMergeSources` loop body — after the retained-path stat succeeds:

```go
			kind := mergeSourceKind(fileID)
			if kind == source.MergeSourcePak && (!game.ConvertPaks || !mod.ConvertPaks) {
				continue // opted out (game- or mod-level): stays raw-deployed (#221)
			}
			sources = append(sources, source.MergeSource{
				ModRef:     mod.SourceID + ":" + mod.ID,
				SourcePath: retainedPath,
				Kind:       kind,
			})
```

`MergedFingerprintEntry` gains fields (input fields first, then outcomes):

```go
type MergedFingerprintEntry struct {
	SourceID string
	ModID    string
	Version  string
	Checksum string // MD5 of the retained source bytes (md5File)
	Kind     string `json:",omitempty"` // source.MergeSourcePak for retained paks; empty/exmodz otherwise (#221)

	// Outcome fields (#221): recorded AFTER the merge, ignored by input
	// equality - a failed conversion retries only when an INPUT changes
	// (pak bytes, base pak, membership), not on every sync.
	Converted  bool   `json:",omitempty"`
	FailReason string `json:",omitempty"`
}

// fingerprintInputs strips outcome fields and normalizes Kind so equality
// judges inputs only. Kind "" and "exmodz" are the same input (pre-#221
// markers wrote no Kind).
func fingerprintInputs(f MergedFingerprint) MergedFingerprint {
	out := MergedFingerprint{BaseIndexHash: f.BaseIndexHash, Mods: make([]MergedFingerprintEntry, len(f.Mods))}
	for i, m := range f.Mods {
		kind := m.Kind
		if kind == "" {
			kind = source.MergeSourceExmodz
		}
		out.Mods[i] = MergedFingerprintEntry{SourceID: m.SourceID, ModID: m.ModID, Version: m.Version, Checksum: m.Checksum, Kind: kind}
	}
	return out
}
```

`mergedFingerprintsEqual` compares `fingerprintInputs(a)` vs `fingerprintInputs(b)` (marshal both projections, compare bytes — keep the existing nil-normalization inside marshalMergedFingerprint).

`currentMergedFingerprint`'s entry construction gains `Kind: src.Kind, Converted: true` (predicted; flipped post-merge for failures).

- [ ] **Step 4: Implement outcome recording + manifest reconcile in syncMergedPak**

After the `mc.MergeCompile` call (Task 5 left `mergeFailed` unused):

```go
	failedByRef := make(map[string]string, len(mergeFailed))
	for _, f := range mergeFailed {
		failedByRef[f.ModRef] = f.Reason
	}
	for i := range current.Mods {
		ref := current.Mods[i].SourceID + ":" + current.Mods[i].ModID
		if reason, bad := failedByRef[ref]; bad {
			current.Mods[i].Converted = false
			current.Mods[i].FailReason = reason
		}
	}
```

(then fingerprint marshal/write/commit/Install as today), and after the successful `installer.Install`:

```go
	reconWarnings, rerr := s.reconcilePakManifests(ctx, game, profileName, installer, failedByRef)
	if rerr != nil {
		return warnings, fmt.Errorf("reconciling pak manifests: %w", rerr)
	}
	warnings = append(warnings, reconWarnings...)
```

Also call `s.reconcilePakManifests(ctx, game, profileName, installer, nil)` in the zero-sources early-return branch BEFORE returning (an all-opted-out profile must still flip previously-converted paks back to raw), ignoring reconcile warnings there is NOT allowed — append and return them.

The reconcile helper:

```go
// reconcilePakManifests aligns every enabled pak mod's cache manifest with
// the merge outcome (#221): a mod whose pak CONVERTED has members=nil (the
// merged pak claims its content; the raw copy must not deploy - flipping it
// undeploys any raw link), while a failed or opted-out mod has its raw pak
// as the sole member (raw deploy, today's behavior). Flips are followed by
// the matching installer action so the game dir converges immediately: the
// first-install transient (raw deployed, then first sync converts) is
// healed here, not left for the next verify.
func (s *Service) reconcilePakManifests(ctx context.Context, game *domain.Game, profileName string, installer *Installer, failedByRef map[string]string) (warnings []string, err error) {
	mods, err := s.GetInstalledModsInProfileOrder(game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile mods: %w", err)
	}
	gameCache := s.GetGameCache(game)
	for i := range mods {
		mod := &mods[i]
		if !mod.Enabled {
			continue
		}
		for _, fileID := range mod.FileIDs {
			if mergeSourceKind(fileID) != source.MergeSourcePak {
				continue
			}
			versionDir := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
			retained := filepath.Join(versionDir, cache.RetainedSourceName(fileID))
			if _, statErr := os.Stat(retained); statErr != nil {
				continue // nothing retained (legacy ingest): Task 11's needs_reingest covers it
			}
			ref := mod.SourceID + ":" + mod.ID
			_, failed := failedByRef[ref]
			participating := game.ConvertPaks && mod.ConvertPaks && !failed

			manifests, merr := gameCache.FileManifests(game.ID, mod.SourceID, mod.ID, mod.Version)
			if merr != nil {
				return warnings, merr
			}
			var currentMembers []string
			recorded := false
			for _, m := range manifests {
				if m.FileID == fileID {
					currentMembers = m.Members
					recorded = m.Recorded
				}
			}

			if participating {
				if recorded && len(currentMembers) == 0 {
					continue // already converged
				}
				if werr := cache.MarkFileCompleteWithMembers(versionDir, fileID, nil); werr != nil {
					return warnings, fmt.Errorf("flipping %s to merged-claimed: %w", ref, werr)
				}
				// The raw copy is now unclaimed: undeploy this mod's files
				// (idempotent; the merged pak carries its content now).
				if uerr := installer.Uninstall(ctx, game, &mod.Mod, profileName); uerr != nil {
					return warnings, fmt.Errorf("undeploying raw pak for %s: %w", ref, uerr)
				}
			} else {
				members, lerr := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
				if lerr != nil {
					return warnings, lerr
				}
				if recorded && len(currentMembers) == len(members) && len(members) > 0 {
					continue // already converged (raw)
				}
				if werr := cache.MarkFileCompleteWithMembers(versionDir, fileID, members); werr != nil {
					return warnings, fmt.Errorf("flipping %s to raw-deploy: %w", ref, werr)
				}
				if ierr := installer.Install(ctx, game, &mod.Mod, profileName); ierr != nil {
					return warnings, fmt.Errorf("deploying raw pak for %s: %w", ref, ierr)
				}
			}
		}
	}
	return warnings, nil
}
```

NOTE on `cache.FileManifests`: its return type carries `FileID`, `Recorded`, `Members` (see internal/storage/cache/cache.go:167-201). Confirm the field names against the actual struct and adjust the loop accordingly — the semantics above are the requirement. Likewise the `installer *Installer` parameter: use whatever concrete type `s.GetInstallerForProfile` returns (syncMergedPak already holds it at merged_pak.go:177) — pass that value through; do not construct a new installer.

Add the outcomes accessor for later tasks:

```go
// MergedPakOutcomes returns the stored merge fingerprint's per-mod entries
// (with #221 conversion outcomes), if a merged pak exists for game+profile.
func (s *Service) MergedPakOutcomes(game *domain.Game, profileName string) ([]MergedFingerprintEntry, bool) {
	gameCache := s.GetGameCache(game)
	cachePath := gameCache.ModPath(game.ID, domain.SourceMerged, mergedPakModID, mergedPakVersion)
	fp, ok := readMergedFingerprint(cachePath)
	if !ok {
		return nil, false
	}
	return fp.Mods, true
}
```

- [ ] **Step 5: Flow-level test**

Append to `internal/core/service_icarus_compile_test.go`, mirroring its existing service/fake-MergeCompiler harness (read the file first; reuse its constructors). The fake MergeCompiler for this test returns `failed = []source.MergeFailure{{ModRef: "icarus:badmod", Reason: "boom"}}` when a source list contains that ref. Scenario and required assertions:

```go
// TestSyncMergedPakReconcilesPakManifests:
// 1. Install (via the harness) two pak-kind mods: goodmod, badmod - each
//    with a retained pak (cache.RetainedSourceName(fileID)) AND a deployable
//    pak copy recorded as the manifest's sole member (Task 9's ingest state).
// 2. Run svc.SyncMergedPak.
// 3. Assert: goodmod's manifest members are now EMPTY (converted; merged pak
//    claims it) - via gameCache.FileManifests.
// 4. Assert: badmod's manifest still lists its pak (raw fallback).
// 5. Assert: the stored fingerprint (readMergedFingerprint or
//    svc.MergedPakOutcomes) has goodmod Converted=true, badmod
//    Converted=false with FailReason "boom".
// 6. Assert: returned warnings contain "badmod" and "deploying raw".
// 7. Toggle: svc.SetModConvertPaks(..., "goodmod", ..., false); re-run
//    SyncMergedPak; assert goodmod's manifest lists its pak again (raw) and
//    the new fingerprint omits goodmod (membership changed -> regen).
```

Write it as real code against the harness's actual helper names.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/core/ -run 'TestMergeSourceKind|TestFingerprintEquality|TestReadOldFingerprintMarker|TestSyncMergedPakReconciles' -v`
Expected: PASS.
Run: `go test ./...` → PASS. `gofmt -l .` / `go vet ./...` → no output.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat: pak merge membership, outcome fingerprint, manifest reconcile (#221)"
```

---

### Task 9: Ingest — retain paks with a deployable member (download + import)

Convert-eligible paks now take the compile-ingest branch: validate, retain the raw pak, AND keep a deployable copy as the manifest's sole member (raw-deploy default until the first sync converts it — Task 8 flips the manifest). Exmodz ingest is byte-identical.

**Files:**

- Modify: `internal/core/service.go` (download branch ~577-628; new predicate beside isExmodzFile ~1016)
- Modify: `internal/core/importer.go` (import branch ~122-166; MarkImportedFileComplete member logic ~314-328)
- Modify: `internal/core/flows.go:4198` (progress classification includes eligible paks)
- Test: `internal/core/service_icarus_compile_test.go` (append), `internal/core/service_import_compile_test.go` (append)

**Interfaces:**

- Consumes: `icarus.ValidateSource` pak widening (Task 5, via the MergeCompiler interface); `game.ConvertPaks` (Task 6).
- Produces: `func isConvertEligiblePakFile(game *domain.Game, fileName string) bool`. Task 11's verify consumes it (export decision there; keep unexported here, verify goes through a Service helper added in Task 11).

- [ ] **Step 1: Write the failing test**

Append to `internal/core/service_icarus_compile_test.go` (reuse its download-flow harness; the existing exmodz ingest test there is the template — read it first):

```go
// TestDownloadPakRetainsAndDeploysRaw:
// Using the harness's fake MergeCompiler source and a fake .pak download
// (file.ID = "pak", FileName = "CoolMod.pak") into a DeployCompile game
// with ConvertPaks: true:
//   - assert ValidateSource was called with the archive path
//   - assert the cache entry contains BOTH cache.RetainedSourceName("pak")
//     AND the deployable copy "CoolMod.pak"
//   - assert the entry's manifest for "pak" is Recorded with exactly
//     ["CoolMod.pak"] as members (raw-deploy default)
//   - assert DownloadModResult.FilesExtracted == 1
// And with game.ConvertPaks = false:
//   - assert the pak takes the LEGACY path (no retained source, normal
//     copy/extract members) - byte-identical to pre-#221 behavior.
```

Write as real code against the harness. For the import side, append the mirrored test to `internal/core/service_import_compile_test.go` (its exmodz import test is the template): import "CoolMod.pak" → retained under `RetainedSourceName("CoolMod.pak")`, deployable copy present, manifest members `["CoolMod.pak"]`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/core/ -run 'TestDownloadPakRetains|TestImportPakRetains' -v`
Expected: FAIL (pak currently falls through to the legacy branch: no retained source).

- [ ] **Step 3: Implement**

`internal/core/service.go` — beside `isExmodzFile`:

```go
// isConvertEligiblePakFile reports whether fileName is a prebuilt .pak that
// should enter the merge-convert pipeline (#221): DeployCompile game with
// convert_paks enabled. The per-MOD opt-out is consulted at merge-membership
// time (enabledMergeSources), not here - ingest state is identical either
// way (retained + raw-deployable), only participation differs.
func isConvertEligiblePakFile(game *domain.Game, fileName string) bool {
	return game.DeployMode == domain.DeployCompile && game.ConvertPaks &&
		strings.HasSuffix(strings.ToLower(fileName), ".pak")
}
```

Download branch (service.go:577) — widen and add the pak member:

```go
	if game.DeployMode == domain.DeployCompile && (isExmodzFile(safeFileName) || isConvertEligiblePakFile(game, safeFileName)) {
		mc, ok := src.(source.MergeCompiler)
		if !ok {
			return nil, fmt.Errorf("source %q: game %q requires DeployCompile but source does not implement MergeCompiler", src.ID(), game.ID)
		}
		if err := mc.ValidateSource(archivePath); err != nil {
			return nil, fmt.Errorf("validating %s: %w", safeFileName, err)
		}
		if err := os.MkdirAll(stagePath, 0755); err != nil {
			return nil, fmt.Errorf("preparing staging: %w", err)
		}
		retainedPath := filepath.Join(stagePath, cache.RetainedSourceName(file.ID))
		if err := copyFileStreaming(archivePath, retainedPath); err != nil {
			return nil, fmt.Errorf("retaining %s: %w", safeFileName, err)
		}
		// exmodz: members nil (#197) - the merged pak is the only artifact.
		// pak (#221): ALSO keep a deployable copy as the sole member, so the
		// default state is raw-deploy (today's behavior); the first
		// successful merge flips the manifest to nil (syncMergedPak's
		// reconcile) and the merged pak takes over.
		var members []string
		if isConvertEligiblePakFile(game, safeFileName) && !isExmodzFile(safeFileName) {
			deployablePath := filepath.Join(stagePath, safeFileName)
			if err := copyFileStreaming(archivePath, deployablePath); err != nil {
				return nil, fmt.Errorf("staging deployable pak %s: %w", safeFileName, err)
			}
			members = []string{safeFileName}
		}
		if err := commitStagedCacheWithMarker(cachePath, stagePath, file.ID, members); err != nil {
			return nil, err
		}
		return &DownloadModResult{FilesExtracted: len(members), Checksum: downloadResult.Checksum}, nil
	}
```

`internal/core/importer.go` (import branch at 122): widen the condition the same way (`isExmodzFile(filename) || isConvertEligiblePakFile(game, filename)`); after the retained copy, for the pak case also `copyFileStreaming(archivePath, filepath.Join(stagePath, filename))` and set `fileCount = 1`. Then read `Service.MarkImportedFileComplete` (importer.go:314-328) and make its member computation kind-aware: exmodz retained → `members = nil` (unchanged); pak retained → `members = []string{filename}`. The existing implementation derives members from the entry listing — keep its shape, just ensure the pak's deployable copy is listed and the exmodz case stays nil.

`internal/core/flows.go:4198` — widen the progress classification:

```go
		if game.DeployMode == domain.DeployCompile && (isExmodzFile(file.FileName) || isConvertEligiblePakFile(game, file.FileName)) {
			compiledFiles = append(compiledFiles, file)
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/core/ -run 'TestDownloadPakRetains|TestImportPakRetains' -v` → PASS.
Run: `go test ./...` → PASS (exmodz flows byte-identical — the existing compile-ingest tests are the regression net).
Run: `gofmt -l .` / `go vet ./...` → no output.

- [ ] **Step 5: End-to-end no-double-apply test**

Append to `internal/core/service_icarus_compile_test.go` the transient-heal scenario (the design's key safety property):

```go
// TestPakInstallThenSyncNeverDoubleApplies:
// 1. Ingest a pak mod (Task 9 state: raw member recorded).
// 2. Deploy it (installer.Install) - raw link present in game.ModPath.
// 3. Run SyncMergedPak with a fake MergeCompiler that SUCCEEDS.
// 4. Assert: the raw pak link is GONE from game.ModPath (reconcile
//    undeployed it) and zzz_LMM_Merged_P.pak is deployed.
// 5. Re-run SyncMergedPak: fast path, nothing changes, raw link still gone.
```

Write as real code with the harness. Run it, expect PASS.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat: pak ingest retains source with raw-deploy default (#221)"
```

---

### Task 10: CLI — `lmm mod convert`, list/show surfaces

Per-mod toggle following the set-update/lock pattern; convert state surfaces in `lmm list` (verbose column + JSON) and `lmm mod show` (JSON + human). JSON additions are MINOR-precedent additive fields.

**Files:**

- Modify: `cmd/lmm/mod.go` (new command + init wiring + doModConvert; modShowInstalled)
- Modify: `cmd/lmm/list.go` (listModJSON + verbose column)
- Test: `cmd/lmm/mod_convert_test.go` (create; mirror `cmd/lmm/mod_lock_test.go`'s harness)

**Interfaces:**

- Consumes: `Service.SetModConvertPaks` (Task 7), `InstalledMod.ConvertPaks`.
- Produces: `lmm mod convert <mod-id> <on|off>`; `listModJSON.ConvertPaks *bool json:"convert_paks,omitempty"`; `modShowInstalled.ConvertPaks *bool json:"convert_paks,omitempty"`. Task 13 documents them; Task 12 mirrors in TUI.

- [ ] **Step 1: Write the failing test**

Create `cmd/lmm/mod_convert_test.go`, mirroring the service/DB harness used by `mod_lock_test.go` (read it first; reuse its game/profile/mod fixture builders):

```go
// TestModConvertCommand:
// - seed an installed mod in a DeployCompile game
// - run: lmm mod convert <id> off  (via the harness's command executor)
//   -> assert output contains "conversion: off" and the DB flag is false
// - run: lmm mod convert <id> on -> flag true again
// - run: lmm mod convert <id> sideways -> error "on|off"
// - non-compile game -> output includes a note that conversion only
//   affects merge-compile games (still persists)
// TestListShowsConvert: verbose list row for a convert-off mod shows "off"
// in the CONVERT column; --json includes "convert_paks": false. For a
// non-compile game the JSON field is ABSENT (omitempty nil).
```

Write as real code with the harness.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/lmm/ -run 'TestModConvert|TestListShowsConvert' -v`
Expected: FAIL — unknown command "convert".

- [ ] **Step 3: Implement the command**

`cmd/lmm/mod.go`:

```go
var modConvertCmd = &cobra.Command{
	Use:   "convert <mod-id> <on|off>",
	Short: "Enable or disable pak-to-exmod conversion for a mod",
	Long: `Control whether a prebuilt .pak mod is converted into the profile's
merged pak (rebased onto the game's current data.pak) or deployed raw.

Only meaningful for merge-compile games (deploy_mode: compile) whose game
config has convert_paks enabled (the default). Conversion is on by default
for every mod; turn it off to keep a specific mod's prebuilt pak deployed
as-is.

This is a metadata write, not a deploy: run 'lmm deploy' (or any merge-pak
mutation) afterward to re-sync the merged pak.

Examples:
  lmm mod convert 12345 off --game icarus
  lmm mod convert 12345 on --game icarus`,
	Args: cobra.ExactArgs(2),
	RunE: runModConvert,
}

func runModConvert(cmd *cobra.Command, args []string) error {
	var convert bool
	switch strings.ToLower(args[1]) {
	case "on":
		convert = true
	case "off":
		convert = false
	default:
		return fmt.Errorf("second argument must be on|off, got %q", args[1])
	}
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doModConvert(service, game, args[0], convert)
	})
}

func doModConvert(service *core.Service, game *domain.Game, modID string, convert bool) error {
	var err error
	modSource, err = resolveSource(service, game, modSource, false)
	if err != nil {
		return err
	}
	profileName, err := resolveProfile(service, game.ID, modProfile)
	if err != nil {
		return err
	}
	mod, err := service.GetInstalledMod(modSource, modID, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}
	if err := service.SetModConvertPaks(modSource, modID, game.ID, profileName, convert); err != nil {
		return fmt.Errorf("setting pak conversion for %s: %w", mod.Name, err)
	}
	state := "on"
	if !convert {
		state = "off"
	}
	fmt.Printf("%s %s pak conversion: %s\n", colorGreen("✓"), mod.Name, state)
	if game.DeployMode != domain.DeployCompile {
		fmt.Println("  note: this game is not merge-compile (deploy_mode: compile); the flag has no effect until it is")
	} else {
		fmt.Println("  run 'lmm deploy' to re-sync the merged pak")
	}
	return nil
}
```

Wire in `init()` (mod.go:151-161): `modCmd.AddCommand(modConvertCmd)`.

`cmd/lmm/list.go`: add to `listModJSON`:

```go
	ConvertPaks *bool `json:"convert_paks,omitempty"` // #221: pak-to-exmod conversion; present only for merge-compile games
```

Populate in the JSON loop only when `game.DeployMode == domain.DeployCompile` (`v := m.ConvertPaks; row.ConvertPaks = &v`). Verbose human table: append `CONVERT` to the header/separator/row triple (list.go:186-189, 203-220); cell value `on`/`off` for compile-mode games, `-` otherwise.

`cmd/lmm/mod.go` modShowInstalled (mod.go:574-580): add `ConvertPaks *bool \`json:"convert_paks,omitempty"\``+ populate for compile games + a human output line`Pak conversion: on|off` beside the update-policy line.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/lmm/ -run 'TestModConvert|TestListShowsConvert' -v` → PASS.
Run: `go test ./...` → PASS. `gofmt -l .` / `go vet ./...` → no output.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: lmm mod convert command and convert state in list/show (#221)"
```

---

### Task 11: verify statuses (`conversion_failed`, `needs_reingest`) + lazy migration + deploy surfacing test

Verify reports conversion outcomes (from the stored fingerprint) and detects convert-eligible pak mods whose cache entry predates #221 (no retained source); `--fix` re-ingests via the existing `redownloadModFile` path — the widened ingest predicate (Task 9) heals the entry, and the sync at verify.go:683 picks it up. Also: the retain-only FILE COUNT MISMATCH carve-out becomes members-aware, and a deploy-flow test proves conversion failures surface to the user.

**Files:**

- Modify: `cmd/lmm/verify.go` (two new status strings; per-mod outcome rows; needs_reingest detection; carve-out fix; Long help enum text at verify.go:150)
- Modify: `internal/core/service.go` or `internal/core/merged_pak.go` (small helper: `(*Service).PakNeedsReingest(game, mod, fileID) bool` — verify must not reimplement kind/retained logic)
- Test: `cmd/lmm/verify_convert_test.go` (create; mirror the harness of existing verify CLI-seam tests), plus a deploy-output assertion appended to an existing deploy CLI-seam test file
- Test: update `cmd/lmm/verify.go`'s `hasRetainedSource` carve-out coverage in the same file

**Interfaces:**

- Consumes: `Service.MergedPakOutcomes` (Task 8), `mergeSourceKind` semantics via the new helper, `redownloadModFile` (verify.go:1355).
- Produces: verify JSON `Status` values `"conversion_failed"` (Note = FailReason) and `"needs_reingest"` (Note explains the fix); helper `(*Service).PakNeedsReingest(game *domain.Game, mod *domain.InstalledMod, fileID string) (bool, error)`.

- [ ] **Step 1: Write the failing test**

Create `cmd/lmm/verify_convert_test.go` mirroring the existing verify harness (read a sibling like the stale_compile test first):

```go
// TestVerifyReportsConversionFailed:
// - seed a compile-game profile with a pak mod; write a merged-pak cache
//   entry whose fingerprint marker has the mod's entry Converted=false,
//   FailReason="table X not present in current base"
// - run lmm verify --json
// - assert a row: {"mod_id": ..., "status": "conversion_failed",
//   "note": "table X not present in current base"} and Warnings count +1
// - human output contains "CONVERSION FAILED" and "deploying raw"
//
// TestVerifyNeedsReingest:
// - seed a compile-game (ConvertPaks true) pak mod whose cache entry has
//   the deployable pak but NO retained source (pre-#221 ingest state)
// - run lmm verify --json -> row status "needs_reingest"
// - run lmm verify --fix with the harness's fake source download available
//   -> redownload path runs; afterward the cache entry HAS the retained
//   source and the manifest records the pak member (Task 9 ingest state)
// - a mod with ConvertPaks=false must NOT be flagged needs_reingest
//
// TestVerifyFileCountCarveOutMembersAware:
// - a pak entry (retained + 1 member) with a stray extra file must STILL
//   report file_count_mismatch (the old retain-only carve-out suppressed
//   any entry with a retained source; now it suppresses only entries whose
//   manifests record zero members).
```

Write as real code with the harness.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/lmm/ -run 'TestVerifyReportsConversionFailed|TestVerifyNeedsReingest|TestVerifyFileCountCarveOut' -v`
Expected: FAIL (statuses don't exist; carve-out suppresses).

- [ ] **Step 3: Implement**

Core helper (in `internal/core/merged_pak.go`, beside mergeSourceKind):

```go
// PakNeedsReingest reports whether mod's fileID is a convert-eligible pak
// whose cache entry predates #221 pak retention (deployable pak present,
// no retained source) - the lazy-migration detector (#221 design §6).
func (s *Service) PakNeedsReingest(game *domain.Game, mod *domain.InstalledMod, fileID string) (bool, error) {
	if game.DeployMode != domain.DeployCompile || !game.ConvertPaks || !mod.ConvertPaks {
		return false, nil
	}
	if mergeSourceKind(fileID) != source.MergeSourcePak {
		return false, nil
	}
	gameCache := s.GetGameCache(game)
	versionDir := gameCache.ModPath(game.ID, mod.SourceID, mod.ID, mod.Version)
	if _, err := os.Stat(filepath.Join(versionDir, cache.RetainedSourceName(fileID))); err == nil {
		return false, nil // already retained
	} else if !os.IsNotExist(err) {
		return false, err
	}
	// Only flag entries that actually exist (an entirely-missing cache
	// entry is the MISSING status's business, not ours).
	files, err := gameCache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	if err != nil || len(files) == 0 {
		return false, err
	}
	return true, nil
}
```

`cmd/lmm/verify.go`:

- In the merged-pak block region (verify.go:361-394 vicinity), after the staleness row: read `svc.MergedPakOutcomes(game, profile)`; for each entry with `Converted == false`, emit a JSON row `{ModID, ModName (resolve via the installed-mods map the command already holds), Status: "conversion_failed", Note: entry.FailReason}` and a human line:
  `fmt.Printf("  %s %s - CONVERSION FAILED (%s) - deploying raw; fix the mod or run 'lmm mod convert %s off' to silence\n", warnMark, name, entry.FailReason, entry.ModID)` counting it as a warning, not an issue. (`warnMark` = whatever warning-color helper verify.go's existing warning lines use — read the stale_compile block and match it exactly; do not invent a new color helper.)
- In the per-file loop, before the cache checks: `if need, nerr := svc.PakNeedsReingest(game, &mod, fileID); nerr == nil && need { ... }` → row Status `"needs_reingest"`, Note `"pak predates conversion support - run 'lmm verify --fix' to re-ingest"`; under `verifyFix && mod.SourceID != domain.SourceLocal` call the existing `redownloadModFile(cmd, svc, game, profile, &mod, fileID)`; for local/imported mods the Note says `"re-import the archive to enable conversion"`.
- Carve-out (verify.go:188-201 + its use): change `hasRetainedSource(...)`'s suppression condition to also require the entry's manifests to record ZERO members (retain-only entries): read `gameCache.FileManifests(...)`; if any manifest records members, do NOT suppress the file-count check.
- Long help text (verify.go:150) and the `verifyFileJSON.Status` doc comment (verify.go:33-39) gain both new strings.

`cmd/lmm/status.go` (design §5 requires status surfacing too): in `statusProfileJSON` (status.go:305-310) add `ConversionFailures int \`json:"conversion_failures,omitempty"\``; populate from `svc.MergedPakOutcomes(game, profile)`(count of entries with`Converted == false`) for DeployCompile games; in the human `showGameStatus`path print` pak conversion failures: N (see 'lmm verify')` only when N > 0. Cover with one assertion appended to the existing status CLI-seam test (seed a fingerprint marker with one failed entry, assert the JSON field and human line).

- [ ] **Step 4: Deploy surfacing test**

Append to an existing deploy CLI-seam test (or the compile-flow test file if deploy has none): run the deploy flow with a fake MergeCompiler returning a failure for one mod; assert stderr/output contains `pak conversion failed` and `deploying raw` (the warning threads through the existing sync-warning plumbing — this test pins that no phase swallows it).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/lmm/ -run 'TestVerify|TestDeploy' -v` → PASS (new + existing).
Run: `go test ./...` → PASS. `gofmt -l .` / `go vet ./...` → no output.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat: verify conversion outcomes, needs_reingest migration, deploy surfacing (#221)"
```

---

### Task 12: TUI — convert toggle, flag indicator, parity

Key choice: **"m" (toggle pak merge)** — verified free in DefaultKeyMap (bound lowercase keys today: q,l,h,k,j,n,p,s,y,e,x,i,u,f,c,d,g,a,v; "m" is unbound; lowercase fits the single-screen non-destructive-toggle convention, keys.go:70-76). Confirmation-free direct toggle (the Policy-picker "choice IS the confirmation" precedent; it's a reversible metadata write).

**Files:**

- Modify: `internal/tui/actions_provider.go` (interface + prototypeProvider)
- Modify: `internal/tui/service_core.go` (coreProvider.SetConvertPaks; Overview populates ModItem)
- Modify: `internal/tui/service.go` (ModItem fields)
- Modify: `internal/tui/keys.go` (ConvertToggle binding)
- Modify: `internal/tui/app.go` (dispatch, help pane, modFlags)
- Modify: `internal/tui/mutations.go` (handler)
- Test: `internal/tui/service_core_convert_test.go` (create; mirror service_core_recompile_test.go's harness)

**Interfaces:**

- Consumes: `Service.SetModConvertPaks` (Task 7), `InstalledMod.ConvertPaks`.
- Produces: `ActionProvider.SetConvertPaks(ctx context.Context, item ModItem, enabled bool) (ActionOutcome, error)`; `ModItem.ConvertPaks bool` + `ModItem.CompileGame bool` (so the view knows whether the flag is meaningful); key "m"; modFlags 3-char indicator `raw` (precedence: lck > pin > raw).

- [ ] **Step 1: Write the failing test**

Create `internal/tui/service_core_convert_test.go` mirroring the recompile test's coreProvider harness:

```go
// TestCoreProviderSetConvertPaks:
// - seed an installed mod in a DeployCompile game via the harness
// - p.SetConvertPaks(ctx, item, false) -> ActionOutcome.Message contains
//   "pak conversion: off"; the DB flag reads back false
// - Overview() items now carry ConvertPaks == false and CompileGame == true
```

Write as real code with the harness.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run TestCoreProviderSetConvertPaks -v`
Expected: FAIL — `SetConvertPaks` undefined.

- [ ] **Step 3: Implement**

`internal/tui/service.go` ModItem (beside Locked):

```go
	// ConvertPaks reports the #221 per-mod pak-to-exmod flag (Overview
	// only, like UpdatePolicy). Meaningful only when CompileGame is true.
	ConvertPaks bool
	// CompileGame is true when the current game's DeployMode is compile -
	// gates the "m" toggle and the raw flag column.
	CompileGame bool
```

`internal/tui/actions_provider.go`: add to the ActionProvider interface (beside SetUpdatePolicy):

```go
	// SetConvertPaks toggles #221 pak-to-exmod conversion for item. A local
	// DB write like SetUpdatePolicy - no network, no hooks; the next merge
	// sync applies it.
	SetConvertPaks(ctx context.Context, item ModItem, enabled bool) (ActionOutcome, error)
```

prototypeProvider gets a stub mirroring its other mutators (record + message).

`internal/tui/service_core.go` (SetUpdatePolicy template, service_core.go:1594-1606):

```go
func (p *coreProvider) SetConvertPaks(_ context.Context, item ModItem, enabled bool) (ActionOutcome, error) {
	if err := p.svc.SetModConvertPaks(item.Source, item.ID, p.currentGame().ID, p.currentProfile(), enabled); err != nil {
		return ActionOutcome{}, fmt.Errorf("setting pak conversion for %s: %w", item.Name, err)
	}
	state := "on"
	if !enabled {
		state = "off"
	}
	return ActionOutcome{Message: fmt.Sprintf("%s pak conversion: %s (deploy to apply)", item.Name, state)}, nil
}
```

Overview (service_core.go:157-166 item literal): `ConvertPaks: m.ConvertPaks, CompileGame: game.DeployMode == domain.DeployCompile,` (use the in-scope game/mod variables per the surrounding code).

`internal/tui/keys.go`: field + binding:

```go
	// ConvertToggle toggles #221 pak-to-exmod conversion for the selected
	// mod on the Installed Mods screen. Lowercase per the single-screen
	// non-destructive-toggle convention (see ToggleEnable's "e").
	ConvertToggle key.Binding
```

```go
		ConvertToggle: key.NewBinding(
			key.WithKeys("m"),
			key.WithHelp("m", "toggle pak merge"),
		),
```

`internal/tui/mutations.go` (toggleSelectedModEnable's guard shape, confirmation-free like the policy picker's directness):

```go
// toggleSelectedModConvert flips #221 pak conversion for the selected mod.
// Direct action, no confirm modal: it is a reversible metadata write (the
// policy picker precedent - the keypress IS the confirmation).
func (m *Model) toggleSelectedModConvert() (tea.Model, tea.Cmd) {
	if m.screen != ScreenInstalledMods || m.actions == nil {
		return m, nil
	}
	item, ok := m.selectedMod()
	if !ok {
		return m, nil
	}
	if !item.CompileGame {
		return m.withStatus("pak conversion applies only to merge-compile games")
	}
	return m.runAction(func(ctx context.Context) (ActionOutcome, error) {
		return m.actions.SetConvertPaks(ctx, item, !item.ConvertPaks)
	})
}
```

(Adapt `withStatus`/`runAction` to the file's ACTUAL helper names — read the sibling handlers; `toggleSelectedModEnable` and `resolvePolicyChoice` show the real async/refresh pattern. The behavioral spec: guard screen+provider, no-op message for non-compile games, invoke provider, refresh the Overview so the flag column updates.)

`internal/tui/app.go`: dispatch `case key.Matches(msg, m.keys.ConvertToggle): return m.toggleSelectedModConvert()` in the installed-mods key block (app.go:938-953); help-pane entry in the installedMods group (app.go:2065-2090): `{"m", "toggle pak merge"}` matching the group's row shape; `modFlags` (app.go:2292-2319): in the 3-char slot switch, after lck/pin: `case item.CompileGame && !item.ConvertPaks: flag = "raw"`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -run TestCoreProviderSetConvertPaks -v` → PASS.
Run: `go test ./...` → PASS (prototypeProvider + interface satisfaction everywhere).
Run: `gofmt -l .` / `go vet ./...` → no output.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "feat: TUI pak-conversion toggle with raw flag indicator (#221)"
```

---

### Task 13: Documentation — README, CHANGELOG, man pages

**Files:**

- Modify: `README.md` (sections per the line map below)
- Modify: `CHANGELOG.md` (`[Unreleased]` at line 8)
- Modify: `docs/man/` via `make man` (the new `lmm mod convert` command changes generated pages; the genman test compares committed vs generated — regenerate NOW, no version bump involved)

**Interfaces:** consumes everything shipped; produces the user-facing contract.

- [ ] **Step 1: CHANGELOG**

Under `## [Unreleased]` (CHANGELOG.md:8), add:

```markdown
### Added

- Icarus: prebuilt `.pak` mods are now converted into the merged pak at
  merge time - rebased onto the game's current `data.pak` - instead of
  deploying raw and being shadowed (#221). Paks that embed a `data.EXMOD`
  manifest convert exactly; others are diff-derived. Irreconcilable paks
  produce a per-mod error and fall back to raw deploy.
- `lmm mod convert <mod-id> <on|off>` and a per-game `convert_paks`
  games.yaml setting control conversion; the TUI toggles it with `m`.
- `lmm verify` reports `conversion_failed` and `needs_reingest` statuses;
  `--fix` re-ingests pre-existing pak mods into the conversion pipeline.
- JSON additions: `convert_paks` in `lmm list --json`, `lmm mod show
--json`, and `lmm game list --json`.
```

- [ ] **Step 2: README**

Using the recon line map (verify actual current positions before editing):

- `### Games (games.yaml)` (~403-436): add `convert_paks: true` (commented as default) to the Icarus example block; extend the Merge precedence paragraph (~436) with two sentences: prebuilt paks are converted and rebased onto the current base at merge time (embedded `data.EXMOD` exact, else diff-derived; failures fall back to raw deploy with a warning), and `convert_paks: false` / `lmm mod convert ... off` keep specific paks raw.
- After `### Locking mods to a version` (~243): new sibling section `### Pak conversion (Icarus)` — ~15 lines covering: why (frozen-in-time paks, shadowing), rebase semantics (drift in author-touched rows is expected), the three control levels (game config, per-mod toggle, automatic default), error-and-skip behavior, `needs_reingest` migration.
- `### Terminal UI` key table: add the `m` row.
- `### Commands` table (~906-916): row `| lmm mod convert <mod-id> <on\|off> | Toggle pak-to-exmod conversion |`.
- `### Verify output` (~1022-1049): rows for `conversion_failed`, `needs_reingest`.

- [ ] **Step 3: man pages**

Run: `make man`
Expected: regenerated pages include `lmm-mod-convert`; `git status` shows docs/man changes.

Run: `go test ./cmd/lmm/ -run TestGenMan -v` (or the genman test's actual name — grep `genman_test.go`)
Expected: PASS.

- [ ] **Step 4: Full suite + commit**

Run: `go test ./...` → PASS. `gofmt -l .` / `go vet ./...` → no output.

```bash
git add README.md CHANGELOG.md docs/man
git commit -m "docs: pak-to-exmod conversion feature documentation (#221)"
```

---

### Task 14: End-to-end sweep and cross-cutting regression tests

The scenarios that span multiple tasks' seams — plus the final full verification.

**Files:**

- Test: `internal/core/pak_convert_e2e_test.go` (create; reuse the compile-flow harness)
- Modify: none (any failure here is a defect in Tasks 1-13 — fix loops go through the owning task's files)

- [ ] **Step 1: Write the e2e tests**

```go
// TestPakConvertEndToEnd (core-level, fake MergeCompiler recording its
// inputs):
// 1. Ingest one exmodz mod and one pak mod; SyncMergedPak.
//    - assert MergeCompile received BOTH sources in profile order, pak with
//      Kind=MergeSourcePak
//    - assert pak manifest flipped to nil; raw link gone; merged deployed
// 2. Toggle the pak mod off (SetModConvertPaks false); SyncMergedPak.
//    - assert MergeCompile received ONLY the exmodz source
//    - assert pak manifest lists the pak; raw deployed again
// 3. Toggle back on; SyncMergedPak; converted state again.
// 4. Disable the pak mod entirely (SetModEnabled false); SyncMergedPak.
//    - assert it contributes nothing and reconcile skipped it (disabled
//      mods are not deployed at all).
//
// TestNoPakModsByteIdentical: a profile with only exmodz mods produces the
// same fingerprint marker JSON shape as pre-#221 for its inputs (Kind
// exmodz entries; equality with a pre-#221-style marker true) and
// MergeCompile receives sources with Kind set - the existing exmodz e2e
// tests passing unchanged is the primary regression net; this test pins
// the marker compat explicitly.
```

Write as real code with the harness.

- [ ] **Step 2: Run the new tests**

Run: `go test ./internal/core/ -run 'TestPakConvertEndToEnd|TestNoPakModsByteIdentical' -v`
Expected: PASS (if not, the defect belongs to an earlier task's code — fix there, not here).

- [ ] **Step 3: Full verification sweep**

Run: `gofmt -l .` → no output.
Run: `go vet ./...` → no output.
Run: `go test ./...`
Expected: PASS, no skips beyond pre-existing.
Run: `go build -o lmm ./cmd/lmm`
Expected: builds; spot-check `./lmm mod convert --help` renders the Long text.

- [ ] **Step 4: Commit**

```bash
git add internal/core/pak_convert_e2e_test.go
git commit -m "test: end-to-end pak conversion lifecycle coverage (#221)"
```

---

## Execution Notes

- Tasks are strictly sequential; each builds on the previous task's interfaces. No parallel implementers.
- Test-harness helper names in Tasks 6-12 marked "read the file first" are deliberate: the harnesses exist (recon-verified files/lines) and their exact constructor names are the implementer's first read, not an invention license. The ASSERTIONS specified are the requirement.
- The spike branch is consulted read-only: `git show spike/pak-to-exmod:spike/pakconvert/<file>`.
- In-game validation on the user's real Icarus install is a RELEASE gate, not a task: after the final review, the user smoke-tests (install a real pak-only mod, verify merged behavior in-game) before the PR merges. Build `./lmm` for them.
- Failures discovered mid-plan that trace to a design gap stop the line (BLOCKED) rather than being patched around.
