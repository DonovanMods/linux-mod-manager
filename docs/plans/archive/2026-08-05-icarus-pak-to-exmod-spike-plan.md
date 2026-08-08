# Icarus PAK → EXMOD Conversion Spike Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove (or refute) that lmm can internally convert a prebuilt Icarus PAK mod into a synthesized `.exmodz` that flows through the real `ParseExmodz` → `MergeCompile` pipeline, validated against author-shipped exmodz ground truth.

**Architecture:** A throwaway in-module package `spike/pakconvert/` (spike branch only, never merged) with four parts: a corpus fetcher CLI (public Firestore catalog, no auth), a pure converter chain (path normalization → row differ → EXMOD emitter), env-gated integration tests (ground truth, pipeline seam, asset probe), and JSON reports feeding a findings doc. Design doc: `docs/plans/2026-08-05-icarus-pak-to-exmod-spike-design.md`.

**Tech Stack:** Go 1.25.6, stdlib only (archive/zip, encoding/json, net/http), plus in-repo `internal/unrealpak` and `internal/source/icarus`.

## Global Constraints

- Module `github.com/DonovanMods/linux-mod-manager`, Go 1.25.6. `spike/pakconvert` imports `internal/...` freely (same module root).
- ALL new code lives under `spike/pakconvert/`. `internal/` and `cmd/` are READ-ONLY — zero production changes.
- Branch `spike/pak-to-exmod`. Commit after each task with prefix `spike:`. Plan/design docs are gitignored (`docs/plans/*`) — force-add with `git add -f` when a task says to commit docs.
- Run `gofmt -l spike/` (expect no output) and `go vet ./spike/...` before every commit.
- NEVER pipe `go test` inside a `&&` chain. Run `go test` as its own command and read its real output.
- Integration tests MUST skip cleanly when `LMM_SPIKE_ICARUS_DIR` / `LMM_SPIKE_CORPUS_DIR` are unset, so plain `go test ./...` stays green everywhere.
- Corpus mod files and generated reports are copyrighted / derived content: they live OUTSIDE the repo (user-supplied dir) and are NEVER committed.
- Four upstream values are unexported and MUST be duplicated as literals with a `// duplicated from internal/source/icarus (unexported)` comment: `"EndOfMod"`, `"../../../Icarus/Content/"`, `"data/"`, and the `-`↔`/` CurrentFile flattening rule.
- `unrealpak.Create` opens its file eagerly and `Writer` has no abort: when writing a pak or any file that may fail mid-way, use the `defer os.Remove`-on-error pattern.
- Catalog access is the public Firestore REST API via `icarus.New(nil, "projectdaedalus-fb09f")` — no auth, no API keys.
- Wire schema for an emitted `.EXMOD` (from `ParseExmod`'s anonymous struct, `internal/source/icarus/exmod.go:35`): lowercase `name`/`author`/`version`/`description`; capitalized `Rows` / `CurrentFile` / `File_Items`; each `File_Items` entry is a FLAT object (`"Name"` + sibling fields); terminal row `{"CurrentFile":"EndOfMod"}` carries NO `File_Items` key. A non-sentinel row with empty `File_Items` is a hard error at merge time — never emit one.

---

### Task 1: Corpus fetcher CLI

**Files:**

- Create: `spike/pakconvert/corpus.go` (pure selection/manifest logic)
- Create: `spike/pakconvert/corpus_test.go`
- Create: `spike/pakconvert/cmd/fetchcorpus/main.go` (I/O shell)

**Interfaces:**

- Produces: `type CorpusMod struct { ID, Name, Version, Week, Pak, Exmodz string }` (Pak/Exmodz are corpus-relative file paths, `""` if absent); `type Manifest struct { Mods []CorpusMod }`; `type CensusEntry struct { ID, Name string; HasPak, HasExmodz bool }`; `func MatchTargets(mods []domain.Mod, substrings []string) []domain.Mod`; `func LoadManifest(dir string) (*Manifest, error)`; `func SaveJSON(path string, v any) error`. Tasks 6–8 consume `LoadManifest` + `CorpusMod`.

- [ ] **Step 1: Write the failing test**

`spike/pakconvert/corpus_test.go`:

```go
package pakconvert

import (
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

func TestMatchTargets(t *testing.T) {
	mods := []domain.Mod{
		{ID: "a1", Name: "FloofLevelCap"},
		{ID: "b2", Name: "Intreeg's 4XP"},
		{ID: "c3", Name: "Unrelated Mod"},
		{ID: "d4", Name: "larkwell care package"},
	}
	got := MatchTargets(mods, []string{"flooflevelcap", "intreeg", "larkwell", "turret variants"})
	if len(got) != 3 {
		t.Fatalf("MatchTargets returned %d mods, want 3", len(got))
	}
	wantIDs := map[string]bool{"a1": true, "b2": true, "d4": true}
	for _, m := range got {
		if !wantIDs[m.ID] {
			t.Errorf("unexpected mod selected: %s (%s)", m.ID, m.Name)
		}
	}
}

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Manifest{Mods: []CorpusMod{
		{ID: "a1", Name: "FloofLevelCap", Version: "1.2", Week: "w57",
			Pak: "a1/FloofLevelCap.pak", Exmodz: ""},
	}}
	if err := SaveJSON(filepath.Join(dir, "manifest.json"), want); err != nil {
		t.Fatalf("SaveJSON: %v", err)
	}
	got, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(got.Mods) != 1 || got.Mods[0] != want.Mods[0] {
		t.Fatalf("round trip mismatch: got %+v want %+v", got.Mods, want.Mods)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./spike/pakconvert/ -run 'TestMatchTargets|TestManifestRoundTrip' -v`
Expected: FAIL (compile error) with `undefined: MatchTargets`, `undefined: Manifest`

- [ ] **Step 3: Write minimal implementation**

`spike/pakconvert/corpus.go`:

```go
// Package pakconvert is a THROWAWAY spike (#220): converting prebuilt Icarus
// PAK mods into synthesized .exmodz form. Never merged into develop.
package pakconvert

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// CorpusMod is one downloaded mod in the local corpus. Pak/Exmodz are
// corpus-relative paths ("" when the catalog has no such file).
type CorpusMod struct {
	ID      string
	Name    string
	Version string
	Week    string
	Pak     string
	Exmodz  string
}

// Manifest indexes the corpus dir; written by cmd/fetchcorpus, read by the
// env-gated integration tests.
type Manifest struct {
	Mods []CorpusMod
}

// CensusEntry records pak/exmodz availability for one catalog mod.
type CensusEntry struct {
	ID        string
	Name      string
	HasPak    bool
	HasExmodz bool
}

// MatchTargets returns mods whose Name contains any of the given substrings,
// case-insensitively. Order follows the input mods slice.
func MatchTargets(mods []domain.Mod, substrings []string) []domain.Mod {
	var out []domain.Mod
	for _, m := range mods {
		lower := strings.ToLower(m.Name)
		for _, s := range substrings {
			if strings.Contains(lower, strings.ToLower(s)) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// SaveJSON writes v as indented JSON, creating parent dirs.
func SaveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating dir for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", path, err)
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadManifest reads <dir>/manifest.json.
func LoadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("reading corpus manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing corpus manifest: %w", err)
	}
	return &m, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./spike/pakconvert/ -run 'TestMatchTargets|TestManifestRoundTrip' -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Write the fetcher main (manual-run I/O shell, no unit test)**

`spike/pakconvert/cmd/fetchcorpus/main.go`:

```go
// fetchcorpus downloads a spike corpus of Icarus mods (pak + exmodz files)
// from the public Firestore catalog into -dir (OUTSIDE the repo; never
// committed). Manual usage:
//
//	go run ./spike/pakconvert/cmd/fetchcorpus -dir ~/lmm-spike-corpus [-dual 8] [-census-all]
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
	"github.com/DonovanMods/linux-mod-manager/spike/pakconvert"
)

// Named spike targets (design doc "Corpus" section), matched case-insensitively.
var targets = []string{"flooflevelcap", "intreeg", "larkwell", "turret"}

func main() {
	dir := flag.String("dir", "", "corpus directory (required, outside the repo)")
	dual := flag.Int("dual", 8, "number of dual-form (pak+exmodz) mods to fetch")
	censusAll := flag.Bool("census-all", false, "sweep GetModFiles for the WHOLE catalog census")
	flag.Parse()
	if *dir == "" {
		log.Fatal("-dir is required")
	}

	ctx := context.Background()
	src := icarus.New(nil, "projectdaedalus-fb09f") // public catalog, no auth

	// PageSize 2000 >> catalog size (~540) so one page holds everything.
	res, err := src.Search(ctx, source.SearchQuery{PageSize: 2000})
	if err != nil {
		log.Fatalf("catalog search: %v", err)
	}
	fmt.Printf("catalog: %d mods\n", len(res.Mods))

	selected := pakconvert.MatchTargets(res.Mods, targets)
	selectedIDs := map[string]bool{}
	for _, m := range selected {
		selectedIDs[m.ID] = true
	}

	// Deterministic scan order for the dual-form quota and census.
	byID := append([]domain.Mod(nil), res.Mods...)
	sort.Slice(byID, func(i, j int) bool { return byID[i].ID < byID[j].ID })

	var census []pakconvert.CensusEntry
	var manifest pakconvert.Manifest
	dualFound := 0
	for _, m := range byID {
		needDual := dualFound < *dual
		if !*censusAll && !needDual && !selectedIDs[m.ID] {
			continue
		}
		time.Sleep(200 * time.Millisecond) // be polite to the public API
		files, err := src.GetModFiles(ctx, &m)
		if err != nil {
			log.Printf("WARN GetModFiles %s (%s): %v", m.ID, m.Name, err)
			continue
		}
		var pakFile, exmodzFile *domain.DownloadableFile
		for i := range files {
			switch files[i].ID {
			case "pak":
				pakFile = &files[i]
			case "exmodz":
				exmodzFile = &files[i]
			}
		}
		census = append(census, pakconvert.CensusEntry{
			ID: m.ID, Name: m.Name,
			HasPak: pakFile != nil, HasExmodz: exmodzFile != nil,
		})

		isDual := pakFile != nil && exmodzFile != nil
		fetch := selectedIDs[m.ID] || (isDual && dualFound < *dual)
		if !fetch {
			continue
		}
		if isDual && !selectedIDs[m.ID] {
			dualFound++
		}
		cm := pakconvert.CorpusMod{ID: m.ID, Name: m.Name, Version: m.Version, Week: m.Category}
		if pakFile != nil {
			cm.Pak = download(ctx, src, &m, pakFile, *dir)
		}
		if exmodzFile != nil {
			cm.Exmodz = download(ctx, src, &m, exmodzFile, *dir)
		}
		manifest.Mods = append(manifest.Mods, cm)
		fmt.Printf("fetched %s (%s) pak=%q exmodz=%q\n", m.Name, m.ID, cm.Pak, cm.Exmodz)
	}

	if err := pakconvert.SaveJSON(filepath.Join(*dir, "manifest.json"), manifest); err != nil {
		log.Fatalf("writing manifest: %v", err)
	}
	if err := pakconvert.SaveJSON(filepath.Join(*dir, "catalog-census.json"), census); err != nil {
		log.Fatalf("writing census: %v", err)
	}
	fmt.Printf("done: %d mods fetched, %d census rows -> %s\n", len(manifest.Mods), len(census), *dir)
}

// download fetches one file and returns its corpus-relative path ("" on failure).
func download(ctx context.Context, src *icarus.Icarus, m *domain.Mod, f *domain.DownloadableFile, dir string) string {
	url, err := src.GetDownloadURL(ctx, m, f.ID)
	if err != nil {
		log.Printf("WARN GetDownloadURL %s/%s: %v", m.ID, f.ID, err)
		return ""
	}
	rel := filepath.Join(m.ID, f.FileName)
	dest := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		log.Printf("WARN mkdir %s: %v", dest, err)
		return ""
	}
	resp, err := http.Get(url)
	if err != nil {
		log.Printf("WARN GET %s: %v", url, err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("WARN GET %s: status %s", url, resp.Status)
		return ""
	}
	out, err := os.Create(dest)
	if err != nil {
		log.Printf("WARN create %s: %v", dest, err)
		return ""
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		os.Remove(dest)
		log.Printf("WARN saving %s: %v", dest, err)
		return ""
	}
	return rel
}
```

- [ ] **Step 6: Build + full-package test, then commit**

Run: `go build ./spike/...`
Expected: no output.
Run: `go test ./spike/pakconvert/ -v`
Expected: PASS.
Run: `gofmt -l spike/` (expect no output), then `go vet ./spike/...` (expect no output).

```bash
git add spike/pakconvert/corpus.go spike/pakconvert/corpus_test.go spike/pakconvert/cmd/fetchcorpus/main.go
git commit -m "spike: corpus fetcher for pak-to-exmod spike (#220)"
```

**Note for the human operator (not a step):** the actual fetch is a manual run — `go run ./spike/pakconvert/cmd/fetchcorpus -dir "$HOME/lmm-spike-corpus"` — performed before Task 6. Tasks 2–5 are fully offline.

---

### Task 2: Path normalization and entry classification

**Files:**

- Create: `spike/pakconvert/normalize.go`
- Create: `spike/pakconvert/normalize_test.go`

**Interfaces:**

- Produces: `type EntryClass int` with consts `ClassTable`, `ClassEmbeddedExmod`, `ClassAsset`, `ClassOther` and a `String()` method; `func NormalizeEntry(mountPoint, entryPath string) (EntryClass, string, error)` — for `ClassTable` the string is the base-table mount-relative path (e.g. `"Experience/D_ExperienceEvents.json"`); for `ClassAsset`/`ClassEmbeddedExmod` it is the Content-relative remainder; for `ClassOther` it is `""`. `func CurrentFileFor(tablePath string) string`. Task 5 consumes all of these.

- [ ] **Step 1: Write the failing test**

`spike/pakconvert/normalize_test.go`:

```go
package pakconvert

import (
	"strings"
	"testing"
)

func TestNormalizeEntry(t *testing.T) {
	tests := []struct {
		name      string
		mount     string
		entry     string
		wantClass EntryClass
		wantPath  string
		wantErr   string
	}{
		{
			// Real layout: Intreegs4XP (pak-divergence-report §1)
			name:  "table with data prefix in entry",
			mount: "../../../Icarus/Content/", entry: "data/Experience/D_ExperienceEvents.json",
			wantClass: ClassTable, wantPath: "Experience/D_ExperienceEvents.json",
		},
		{
			// Real layout: FloofLevelCap — data/ boundary floats into the mount point
			name:  "table with data prefix in mount",
			mount: "../../../Icarus/Content/data/Character/", entry: "D_CharacterGrowth.json",
			wantClass: ClassTable, wantPath: "Character/D_CharacterGrowth.json",
		},
		{
			// Real layout: Intreegs4XP ships its source .EXMOD inside the pak
			name:  "embedded exmod",
			mount: "../../../Icarus/Content/", entry: "data.EXMOD",
			wantClass: ClassEmbeddedExmod, wantPath: "data.EXMOD",
		},
		{
			name:  "uasset asset",
			mount: "../../../Icarus/Content/", entry: "Larkwell/ITM/SK_Item.uasset",
			wantClass: ClassAsset, wantPath: "Larkwell/ITM/SK_Item.uasset",
		},
		{
			name:  "uexp asset case-insensitive",
			mount: "../../../Icarus/Content/", entry: "Larkwell/ITM/SK_Item.UEXP",
			wantClass: ClassAsset, wantPath: "Larkwell/ITM/SK_Item.UEXP",
		},
		{
			name:  "json outside data dir is Other",
			mount: "../../../Icarus/Content/", entry: "Config/whatever.json",
			wantClass: ClassOther, wantPath: "",
		},
		{
			name:  "entry outside Content mount is Other",
			mount: "../../../", entry: "Engine/Config/Base.ini",
			wantClass: ClassOther, wantPath: "",
		},
		{
			// '-' in a table path breaks the CurrentFile '/'-to-'-' flattening
			name:  "hyphen in table path is an error",
			mount: "../../../Icarus/Content/", entry: "data/AI/D_Some-Table.json",
			wantErr: "hyphen",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			class, p, err := NormalizeEntry(tt.mount, tt.entry)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeEntry: %v", err)
			}
			if class != tt.wantClass || p != tt.wantPath {
				t.Errorf("got (%v, %q), want (%v, %q)", class, p, tt.wantClass, tt.wantPath)
			}
		})
	}
}

func TestCurrentFileFor(t *testing.T) {
	got := CurrentFileFor("Audio/MusicConditions/D_MusicLocationConditions.json")
	want := "Audio-MusicConditions-D_MusicLocationConditions.json"
	if got != want {
		t.Fatalf("CurrentFileFor = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./spike/pakconvert/ -run 'TestNormalizeEntry|TestCurrentFileFor' -v`
Expected: FAIL (compile error) with `undefined: EntryClass`, `undefined: NormalizeEntry`

- [ ] **Step 3: Write minimal implementation**

`spike/pakconvert/normalize.go`:

```go
package pakconvert

import (
	"fmt"
	"path"
	"strings"
)

// Duplicated from internal/source/icarus (unexported there):
// icarusContentMountPoint (compile.go:128) and icarusDataTablePrefix
// (compile.go:138). The '-'<->'/' CurrentFile flattening rule is
// matchMountPath (compile.go:169).
const (
	contentMount = "../../../Icarus/Content/"
	dataPrefix   = "data/"
)

// EntryClass classifies one mod-pak entry for conversion.
type EntryClass int

const (
	ClassOther EntryClass = iota
	ClassTable
	ClassEmbeddedExmod
	ClassAsset
)

func (c EntryClass) String() string {
	switch c {
	case ClassTable:
		return "table"
	case ClassEmbeddedExmod:
		return "embedded-exmod"
	case ClassAsset:
		return "asset"
	default:
		return "other"
	}
}

// NormalizeEntry joins a pak's mount point with one entry path and classifies
// the result. The data/ boundary floats between mount point and entry path in
// real mod paks (pak-divergence-report §1), so classification always works on
// the JOINED path. For ClassTable the returned string is the base-table
// mount-relative path (what unrealpak data.pak Files() report, and what
// CurrentFileFor flattens); for ClassAsset and ClassEmbeddedExmod it is the
// Content-relative remainder; for ClassOther it is "".
func NormalizeEntry(mountPoint, entryPath string) (EntryClass, string, error) {
	full := path.Join(mountPoint, entryPath) // Join cleans but keeps leading ../
	if !strings.HasPrefix(full, contentMount) {
		return ClassOther, "", nil
	}
	rest := strings.TrimPrefix(full, contentMount)
	lower := strings.ToLower(rest)
	switch {
	case strings.HasSuffix(lower, ".exmod"):
		return ClassEmbeddedExmod, rest, nil
	case strings.HasSuffix(lower, ".uasset") || strings.HasSuffix(lower, ".uexp"):
		return ClassAsset, rest, nil
	case strings.HasPrefix(rest, dataPrefix) && strings.HasSuffix(lower, ".json"):
		tablePath := strings.TrimPrefix(rest, dataPrefix)
		if strings.Contains(tablePath, "-") {
			// The CurrentFile encoding replaces ALL '/' with '-' and is only
			// reversible because no real base-table path contains a hyphen
			// (icarus compile.go:147). A hyphen here would produce an
			// unresolvable CurrentFile.
			return ClassTable, "", fmt.Errorf("table path %q contains a hyphen: CurrentFile flattening would be ambiguous", tablePath)
		}
		return ClassTable, tablePath, nil
	default:
		return ClassOther, "", nil
	}
}

// CurrentFileFor flattens a base-table mount-relative path into the .EXMOD
// CurrentFile encoding (forward direction of icarus matchMountPath).
func CurrentFileFor(tablePath string) string {
	return strings.ReplaceAll(tablePath, "/", "-")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./spike/pakconvert/ -run 'TestNormalizeEntry|TestCurrentFileFor' -v`
Expected: PASS (all subtests)

- [ ] **Step 5: Commit**

Run: `gofmt -l spike/` (expect no output), then `go vet ./spike/...` (expect no output).

```bash
git add spike/pakconvert/normalize.go spike/pakconvert/normalize_test.go
git commit -m "spike: pak entry normalization and classification (#220)"
```

---

### Task 3: Row differ

**Files:**

- Create: `spike/pakconvert/diff.go`
- Create: `spike/pakconvert/diff_test.go`

**Interfaces:**

- Consumes: nothing from earlier tasks (pure JSON in/out).
- Produces: `type Item struct { Name string; Fields map[string]any }`; `type Finding struct { Kind, Table, Row, Detail string }`; `type TableDiff struct { Items []Item; StaleBaseOnlyRows int; Findings []Finding }`; `func DiffTable(baseJSON, modJSON []byte) (*TableDiff, error)`. Finding.Table is left `""` here — Task 5's ConvertPak fills it. Finding Kinds emitted by this task: `"defaults-changed"`, `"rowstruct-changed"`, `"top-level-changed"`, `"duplicate-row-name"`, `"field-removed"`. Tasks 4–6 consume `Item`; Tasks 5–6 consume `TableDiff`/`Finding`.

- [ ] **Step 1: Write the failing test**

`spike/pakconvert/diff_test.go`:

```go
package pakconvert

import (
	"testing"
)

const baseTable = `{
	"RowStruct": "/Script/Icarus.FakeRow",
	"Defaults": {"Speed": 1},
	"Rows": [
		{"Name": "Alpha", "Speed": 10, "Nested": {"A": 1, "B": 2}},
		{"Name": "Beta", "Speed": 20},
		{"Name": "Gamma", "Speed": 30}
	]
}`

func mustDiff(t *testing.T, base, mod string) *TableDiff {
	t.Helper()
	d, err := DiffTable([]byte(base), []byte(mod))
	if err != nil {
		t.Fatalf("DiffTable: %v", err)
	}
	return d
}

func TestDiffTableIdentical(t *testing.T) {
	d := mustDiff(t, baseTable, baseTable)
	if len(d.Items) != 0 || d.StaleBaseOnlyRows != 0 || len(d.Findings) != 0 {
		t.Fatalf("identical tables should produce empty diff, got %+v", d)
	}
}

func TestDiffTableChangedField(t *testing.T) {
	mod := `{
		"RowStruct": "/Script/Icarus.FakeRow",
		"Defaults": {"Speed": 1},
		"Rows": [
			{"Name": "Alpha", "Speed": 99, "Nested": {"A": 1, "B": 2}},
			{"Name": "Beta", "Speed": 20},
			{"Name": "Gamma", "Speed": 30}
		]
	}`
	d := mustDiff(t, baseTable, mod)
	if len(d.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", d.Items)
	}
	it := d.Items[0]
	if it.Name != "Alpha" || len(it.Fields) != 1 || it.Fields["Speed"] != float64(99) {
		t.Fatalf("want Alpha{Speed:99} only, got %+v", it)
	}
}

func TestDiffTableNestedChangeIsWholeField(t *testing.T) {
	mod := `{
		"RowStruct": "/Script/Icarus.FakeRow",
		"Defaults": {"Speed": 1},
		"Rows": [
			{"Name": "Alpha", "Speed": 10, "Nested": {"A": 1, "B": 3}},
			{"Name": "Beta", "Speed": 20},
			{"Name": "Gamma", "Speed": 30}
		]
	}`
	d := mustDiff(t, baseTable, mod)
	if len(d.Items) != 1 || d.Items[0].Name != "Alpha" {
		t.Fatalf("want 1 Alpha item, got %+v", d.Items)
	}
	nested, ok := d.Items[0].Fields["Nested"].(map[string]any)
	if !ok || nested["A"] != float64(1) || nested["B"] != float64(3) {
		t.Fatalf("nested change must be emitted as the WHOLE field, got %+v", d.Items[0].Fields)
	}
}

func TestDiffTableNewRow(t *testing.T) {
	mod := `{
		"RowStruct": "/Script/Icarus.FakeRow",
		"Defaults": {"Speed": 1},
		"Rows": [
			{"Name": "Alpha", "Speed": 10, "Nested": {"A": 1, "B": 2}},
			{"Name": "Beta", "Speed": 20},
			{"Name": "Gamma", "Speed": 30},
			{"Name": "Delta", "Speed": 40}
		]
	}`
	d := mustDiff(t, baseTable, mod)
	if len(d.Items) != 1 || d.Items[0].Name != "Delta" || d.Items[0].Fields["Speed"] != float64(40) {
		t.Fatalf("want new-row Delta{Speed:40}, got %+v", d.Items)
	}
}

func TestDiffTableStaleBaseOnlyRowsIgnored(t *testing.T) {
	// Mod table is a stale snapshot missing Beta and Gamma: NOT a deletion
	// (EXMOD cannot express deletes) — counted, not emitted.
	mod := `{
		"RowStruct": "/Script/Icarus.FakeRow",
		"Defaults": {"Speed": 1},
		"Rows": [{"Name": "Alpha", "Speed": 10, "Nested": {"A": 1, "B": 2}}]
	}`
	d := mustDiff(t, baseTable, mod)
	if len(d.Items) != 0 {
		t.Fatalf("stale snapshot must emit no items, got %+v", d.Items)
	}
	if d.StaleBaseOnlyRows != 2 {
		t.Fatalf("want StaleBaseOnlyRows=2, got %d", d.StaleBaseOnlyRows)
	}
}

func TestDiffTableFindings(t *testing.T) {
	mod := `{
		"RowStruct": "/Script/Icarus.OtherRow",
		"Defaults": {"Speed": 2},
		"Extra": true,
		"Rows": [
			{"Name": "Alpha", "Nested": {"A": 1, "B": 2}},
			{"Name": "Alpha", "Speed": 10},
			{"Name": "Beta", "Speed": 20},
			{"Name": "Gamma", "Speed": 30}
		]
	}`
	d := mustDiff(t, baseTable, mod)
	kinds := map[string]int{}
	for _, f := range d.Findings {
		kinds[f.Kind]++
	}
	for _, want := range []string{"rowstruct-changed", "defaults-changed", "top-level-changed", "duplicate-row-name", "field-removed"} {
		if kinds[want] == 0 {
			t.Errorf("missing finding kind %q in %+v", want, d.Findings)
		}
	}
}

func TestDiffTableMalformed(t *testing.T) {
	if _, err := DiffTable([]byte(`{"Rows": "nope"}`), []byte(baseTable)); err == nil {
		t.Fatal("want error for malformed base table")
	}
	if _, err := DiffTable([]byte(baseTable), []byte(`not json`)); err == nil {
		t.Fatal("want error for malformed mod table")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./spike/pakconvert/ -run 'TestDiffTable' -v`
Expected: FAIL (compile error) with `undefined: TableDiff`, `undefined: DiffTable`

- [ ] **Step 3: Write minimal implementation**

`spike/pakconvert/diff.go`:

```go
package pakconvert

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// Item is one upsert the converter derived: a row Name plus the changed (or,
// for new rows, all) top-level fields. Mirrors icarus.ExmodFileItem.
type Item struct {
	Name   string
	Fields map[string]any
}

// Finding records something the conversion observed but cannot (or must not)
// express as an EXMOD upsert. Table is filled by ConvertPak (Task 5).
type Finding struct {
	Kind   string
	Table  string
	Row    string
	Detail string
}

// TableDiff is the result of diffing one mod-pak table against the live base.
type TableDiff struct {
	Items             []Item
	StaleBaseOnlyRows int
	Findings          []Finding
}

// dataTable is the UE DataTable export shape ({"RowStruct","Defaults","Rows"}).
type dataTable struct {
	rows  []map[string]any
	other map[string]any // every top-level key except Rows
}

func parseDataTable(data []byte) (*dataTable, error) {
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parsing data table: %w", err)
	}
	rawRows, ok := top["Rows"].([]any)
	if !ok {
		return nil, fmt.Errorf("data table has no Rows array")
	}
	t := &dataTable{other: top}
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

// DiffTable derives the EXMOD-expressible difference between the LIVE base
// table and a mod pak's (possibly stale) full-table snapshot.
//
// Rules (design doc "Converter" §3):
//   - row in mod, not in base       -> new row, emit all fields
//   - row in both, fields differ    -> emit Name + changed fields (whole-field)
//   - row in base, not in mod       -> staleness, counted and IGNORED
//   - mod row missing a base field  -> finding "field-removed" (inexpressible)
//   - Defaults/RowStruct/other top-level changes -> findings (inexpressible)
//   - duplicate Name in mod         -> finding, first occurrence wins
func DiffTable(baseJSON, modJSON []byte) (*TableDiff, error) {
	base, err := parseDataTable(baseJSON)
	if err != nil {
		return nil, fmt.Errorf("base: %w", err)
	}
	mod, err := parseDataTable(modJSON)
	if err != nil {
		return nil, fmt.Errorf("mod: %w", err)
	}

	d := &TableDiff{}

	for _, key := range []string{"RowStruct", "Defaults"} {
		if !reflect.DeepEqual(base.other[key], mod.other[key]) {
			kind := "rowstruct-changed"
			if key == "Defaults" {
				kind = "defaults-changed"
			}
			d.Findings = append(d.Findings, Finding{Kind: kind,
				Detail: fmt.Sprintf("%s differs from base (EXMOD cannot express this)", key)})
		}
	}
	for key, v := range mod.other {
		if key == "RowStruct" || key == "Defaults" {
			continue
		}
		if !reflect.DeepEqual(base.other[key], v) {
			d.Findings = append(d.Findings, Finding{Kind: "top-level-changed",
				Detail: fmt.Sprintf("top-level key %q differs from base", key)})
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
			d.Findings = append(d.Findings, Finding{Kind: "duplicate-row-name", Row: name,
				Detail: "duplicate row Name in mod table; first occurrence wins"})
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
			d.Items = append(d.Items, Item{Name: name, Fields: fields})
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
				d.Findings = append(d.Findings, Finding{Kind: "field-removed", Row: name,
					Detail: fmt.Sprintf("field %q present in base but absent in mod row (EXMOD cannot remove fields)", k)})
			}
		}
		if len(changed) > 0 {
			d.Items = append(d.Items, Item{Name: name, Fields: changed})
		}
	}

	for name := range baseByName {
		if !seen[name] {
			d.StaleBaseOnlyRows++
		}
	}
	return d, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./spike/pakconvert/ -run 'TestDiffTable' -v`
Expected: PASS (7 tests)

- [ ] **Step 5: Commit**

Run: `gofmt -l spike/` (expect no output), then `go vet ./spike/...` (expect no output).

```bash
git add spike/pakconvert/diff.go spike/pakconvert/diff_test.go
git commit -m "spike: name-keyed row differ against live base tables (#220)"
```

---

### Task 4: EXMOD emitter + synthesized exmodz

**Files:**

- Create: `spike/pakconvert/emit.go`
- Create: `spike/pakconvert/emit_test.go`

**Interfaces:**

- Consumes: `Item` (Task 3).
- Produces: `type Meta struct { Name, Author, Version, Description string }`; `type TableEntry struct { CurrentFile string; Items []Item }`; `func WriteExmod(meta Meta, tables []TableEntry) ([]byte, error)`; `func WriteExmodz(exmodName string, exmod []byte, assets map[string][]byte) ([]byte, error)` (assets keyed by Content-relative path, zipped under `<exmodName>/<path>`). Task 5 consumes all of these.

- [ ] **Step 1: Write the failing test (round-trips through the REAL parsers)**

`spike/pakconvert/emit_test.go`:

```go
package pakconvert

import (
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
)

func TestWriteExmodRoundTrip(t *testing.T) {
	meta := Meta{Name: "Converted Mod", Author: "spike", Version: "1.0", Description: "pak-to-exmod spike output"}
	tables := []TableEntry{
		{CurrentFile: "Experience-D_ExperienceEvents.json", Items: []Item{
			{Name: "Kill_Deer", Fields: map[string]any{"Experience": float64(400)}},
			{Name: "New_Event", Fields: map[string]any{"Experience": float64(10), "Repeatable": true}},
		}},
		{CurrentFile: "Character-D_CharacterGrowth.json", Items: []Item{
			{Name: "MaxLevel", Fields: map[string]any{"Level": float64(99)}},
		}},
		{CurrentFile: "Empty-D_Nothing.json", Items: nil}, // must be skipped entirely
	}
	data, err := WriteExmod(meta, tables)
	if err != nil {
		t.Fatalf("WriteExmod: %v", err)
	}

	diff, err := icarus.ParseExmod(data) // the REAL parser is the round-trip oracle
	if err != nil {
		t.Fatalf("icarus.ParseExmod rejected our output: %v\n%s", err, data)
	}
	if diff.Name != meta.Name || diff.Author != meta.Author || diff.Version != meta.Version {
		t.Errorf("metadata mismatch: %+v", diff)
	}
	// ParseExmod does NOT consume the sentinel (that happens at compile/merge
	// time), so it comes back as a parsed row: 2 tables + sentinel = 3.
	// The empty table must have been skipped entirely.
	if len(diff.Rows) != 3 {
		t.Fatalf("want 3 parsed rows (2 tables + sentinel; empty table skipped), got %d: %+v", len(diff.Rows), diff.Rows)
	}
	if diff.Rows[0].CurrentFile != "Experience-D_ExperienceEvents.json" || len(diff.Rows[0].FileItems) != 2 {
		t.Errorf("row 0 mismatch: %+v", diff.Rows[0])
	}
	if diff.Rows[0].FileItems[1].Fields["Repeatable"] != true {
		t.Errorf("flat File_Items fields lost: %+v", diff.Rows[0].FileItems[1])
	}
	last := diff.Rows[len(diff.Rows)-1]
	if last.CurrentFile != "EndOfMod" || len(last.FileItems) != 0 {
		t.Errorf("last row must be the bare EndOfMod sentinel, got %+v", last)
	}
	// Every non-sentinel row must carry items (empty ones are a merge-time
	// hard error upstream).
	for _, r := range diff.Rows {
		if r.CurrentFile != "EndOfMod" && len(r.FileItems) == 0 {
			t.Errorf("emitted a non-sentinel row with no File_Items (merge-time hard error): %+v", r)
		}
	}
	if !strings.Contains(string(data), `"CurrentFile": "EndOfMod"`) &&
		!strings.Contains(string(data), `"CurrentFile":"EndOfMod"`) {
		t.Error("terminal EndOfMod sentinel row missing from emitted JSON")
	}
}

func TestWriteExmodEmptyItemName(t *testing.T) {
	_, err := WriteExmod(Meta{Name: "x"}, []TableEntry{
		{CurrentFile: "A-B.json", Items: []Item{{Name: "", Fields: map[string]any{"F": 1}}}},
	})
	if err == nil {
		t.Fatal("want error for empty item Name")
	}
}

func TestWriteExmodzRoundTrip(t *testing.T) {
	exmod, err := WriteExmod(Meta{Name: "Bundle"}, []TableEntry{
		{CurrentFile: "AI-D_AIGrowth.json", Items: []Item{{Name: "R1", Fields: map[string]any{"V": float64(1)}}}},
	})
	if err != nil {
		t.Fatalf("WriteExmod: %v", err)
	}
	assets := map[string][]byte{
		"ITM/SK_Saddle.uasset": []byte("uasset-bytes"),
		"ITM/SK_Saddle.uexp":   []byte("uexp-bytes"),
	}
	zipData, err := WriteExmodz("Bundle", exmod, assets)
	if err != nil {
		t.Fatalf("WriteExmodz: %v", err)
	}

	bundle, err := icarus.ParseExmodz(zipData) // REAL parser as oracle
	if err != nil {
		t.Fatalf("icarus.ParseExmodz rejected our zip: %v", err)
	}
	if bundle.Diff == nil || bundle.Diff.Name != "Bundle" {
		t.Fatalf("manifest not parsed: %+v", bundle.Diff)
	}
	if len(bundle.Assets) != 2 {
		t.Fatalf("want 2 assets, got %v", bundle.Assets)
	}
	if string(bundle.Assets["Bundle/ITM/SK_Saddle.uasset"]) != "uasset-bytes" {
		t.Errorf("asset content/path mismatch: keys %v", bundle.Assets)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./spike/pakconvert/ -run 'TestWriteExmod' -v`
Expected: FAIL (compile error) with `undefined: Meta`, `undefined: WriteExmod`

- [ ] **Step 3: Write minimal implementation**

`spike/pakconvert/emit.go`:

```go
package pakconvert

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// endOfModSentinel is duplicated from internal/source/icarus (unexported
// const, compile.go:146). A terminal row {"CurrentFile":"EndOfMod"} with no
// File_Items key matches what real author-built .EXMODs ship.
const endOfModSentinel = "EndOfMod"

// Meta is the synthesized .EXMOD's metadata block.
type Meta struct {
	Name        string
	Author      string
	Version     string
	Description string
}

// TableEntry is one table's derived upserts, keyed by its flattened
// CurrentFile encoding (see CurrentFileFor).
type TableEntry struct {
	CurrentFile string
	Items       []Item
}

// exmodDoc mirrors ParseExmod's anonymous wire struct
// (internal/source/icarus/exmod.go:35): lowercase metadata keys, capitalized
// structural keys. icarus.ExmodDiff itself is tag-free and CANNOT be used to
// emit — json.Marshal on it produces the wrong keys.
type exmodDoc struct {
	Name        string           `json:"name"`
	Author      string           `json:"author"`
	Version     string           `json:"version"`
	Description string           `json:"description"`
	Rows        []map[string]any `json:"Rows"`
}

// WriteExmod emits a .EXMOD JSON document readable by icarus.ParseExmod.
// Tables with zero items are skipped (a non-sentinel row with empty
// File_Items is a merge-time hard error upstream).
func WriteExmod(meta Meta, tables []TableEntry) ([]byte, error) {
	doc := exmodDoc{Name: meta.Name, Author: meta.Author, Version: meta.Version, Description: meta.Description}
	for _, te := range tables {
		if len(te.Items) == 0 {
			continue
		}
		items := make([]map[string]any, 0, len(te.Items))
		for _, it := range te.Items {
			if it.Name == "" {
				return nil, fmt.Errorf("table %s: item with empty Name", te.CurrentFile)
			}
			flat := make(map[string]any, len(it.Fields)+1)
			flat["Name"] = it.Name
			for k, v := range it.Fields {
				if k == "Name" {
					continue // Name is positional, never a payload field
				}
				flat[k] = v
			}
			items = append(items, flat)
		}
		doc.Rows = append(doc.Rows, map[string]any{
			"CurrentFile": te.CurrentFile,
			"File_Items":  items,
		})
	}
	// Terminal sentinel row, File_Items key ABSENT (matches real manifests).
	doc.Rows = append(doc.Rows, map[string]any{"CurrentFile": endOfModSentinel})
	return json.MarshalIndent(doc, "", "  ")
}

// WriteExmodz zips a synthesized .exmodz: the manifest at
// "Extracted Mods/<exmodName>.EXMOD" plus assets under "<exmodName>/<path>",
// mirroring the real bundle layout (icarus exmodz_test.go fixture).
func WriteExmodz(exmodName string, exmod []byte, assets map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.Create("Extracted Mods/" + exmodName + ".EXMOD")
	if err != nil {
		return nil, fmt.Errorf("creating manifest entry: %w", err)
	}
	if _, err := w.Write(exmod); err != nil {
		return nil, fmt.Errorf("writing manifest entry: %w", err)
	}

	paths := make([]string, 0, len(assets))
	for p := range assets {
		paths = append(paths, p)
	}
	sort.Strings(paths) // deterministic zip layout
	for _, p := range paths {
		w, err := zw.Create(exmodName + "/" + p)
		if err != nil {
			return nil, fmt.Errorf("creating asset entry %s: %w", p, err)
		}
		if _, err := w.Write(assets[p]); err != nil {
			return nil, fmt.Errorf("writing asset entry %s: %w", p, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finalizing exmodz zip: %w", err)
	}
	return buf.Bytes(), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./spike/pakconvert/ -run 'TestWriteExmod' -v`
Expected: PASS (3 tests). If `ParseExmod` rejects the output, the wire schema in emit.go is wrong — fix emit.go, never touch internal/.

- [ ] **Step 5: Commit**

Run: `gofmt -l spike/` (expect no output), then `go vet ./spike/...` (expect no output).

```bash
git add spike/pakconvert/emit.go spike/pakconvert/emit_test.go
git commit -m "spike: EXMOD/exmodz emitter round-tripping through real parsers (#220)"
```

---

### Task 5: Converter orchestration (ConvertPak)

**Files:**

- Create: `spike/pakconvert/convert.go`
- Create: `spike/pakconvert/convert_test.go`

**Interfaces:**

- Consumes: `NormalizeEntry`, `CurrentFileFor`, `EntryClass` consts (Task 2); `DiffTable`, `TableDiff`, `Finding`, `Item` (Task 3); `Meta`, `TableEntry`, `WriteExmod`, `WriteExmodz` (Task 4); `unrealpak.Open/Reader`, `unrealpak.Create/AddFile/WithMountPoint` (fixtures).
- Produces: `type Report struct { PakPath, MountPoint string; Census map[string]int; Tables map[string]*TableDiff; EmbeddedExmods map[string][]byte; Findings []Finding; StaleRows int }`; `func ConvertPak(pakPath, basePakPath string, meta Meta) ([]byte, *Report, error)`. Report finding kinds added by this task: `"table-not-in-base"`, `"unreadable-entry"`, `"hyphen-path"`, `"unsafe-asset-path"`. Tasks 6–7 consume `ConvertPak`; Task 6 consumes `Report.EmbeddedExmods`.

- [ ] **Step 1: Write the failing test (fully offline, synthetic paks)**

`spike/pakconvert/convert_test.go`:

```go
package pakconvert

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// writePak builds a synthetic pak fixture. Mirrors icarus's own test style:
// unrealpak.Writer output is readable by unrealpak.Reader.
func writePak(t *testing.T, path, mount string, entries map[string][]byte) {
	t.Helper()
	var opts []unrealpak.Option
	if mount != "" {
		opts = append(opts, unrealpak.WithMountPoint(mount))
	}
	w, err := unrealpak.Create(path, opts...)
	if err != nil {
		t.Fatalf("Create %s: %v", path, err)
	}
	for p, data := range entries {
		if err := w.AddFile(p, data); err != nil {
			t.Fatalf("AddFile %s: %v", p, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close %s: %v", path, err)
	}
}

const fixtureBaseTable = `{
	"RowStruct": "/Script/Icarus.FakeRow",
	"Defaults": {},
	"Rows": [
		{"Name": "Alpha", "Speed": 10},
		{"Name": "Beta", "Speed": 20}
	]
}`

// The mod pak carries a full-table snapshot: Alpha changed, Delta added,
// Beta absent (stale snapshot, must be ignored).
const fixtureModTable = `{
	"RowStruct": "/Script/Icarus.FakeRow",
	"Defaults": {},
	"Rows": [
		{"Name": "Alpha", "Speed": 99},
		{"Name": "Delta", "Speed": 40}
	]
}`

func TestConvertPak(t *testing.T) {
	dir := t.TempDir()
	basePak := filepath.Join(dir, "data.pak")
	modPak := filepath.Join(dir, "mod.pak")

	// Base data.pak: default mount, tables at bare mount-relative paths
	// (matching the real data.pak layout, e.g. "Test/D_Fake.json").
	writePak(t, basePak, "", map[string][]byte{
		"Test/D_Fake.json": []byte(fixtureBaseTable),
	})
	// Mod pak: real prebuilt layout — Content mount + data/-prefixed table,
	// plus an asset and an embedded .EXMOD.
	writePak(t, modPak, "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Fake.json":  []byte(fixtureModTable),
		"Mod/ITM/SK_Hat.uasset":  []byte("asset-bytes"),
		"data.EXMOD":             []byte(`{"Rows":[]}`),
		"README.txt":             []byte("junk"),
	})

	meta := Meta{Name: "TestMod", Author: "spike", Version: "1.0"}
	exmodz, report, err := ConvertPak(modPak, basePak, meta)
	if err != nil {
		t.Fatalf("ConvertPak: %v", err)
	}

	// Census: one of each class.
	for class, want := range map[string]int{"table": 1, "asset": 1, "embedded-exmod": 1, "other": 1} {
		if report.Census[class] != want {
			t.Errorf("census[%s] = %d, want %d (census: %v)", class, report.Census[class], want, report.Census)
		}
	}
	if report.StaleRows != 1 { // Beta missing from mod snapshot
		t.Errorf("StaleRows = %d, want 1", report.StaleRows)
	}
	if _, ok := report.EmbeddedExmods["data.EXMOD"]; !ok {
		t.Errorf("embedded .EXMOD not captured: %v", report.EmbeddedExmods)
	}
	td, ok := report.Tables["Test/D_Fake.json"]
	if !ok {
		t.Fatalf("table diff missing: %v", report.Tables)
	}
	if len(td.Items) != 2 {
		t.Fatalf("want 2 items (Alpha changed, Delta new), got %+v", td.Items)
	}

	// The synthesized bundle must parse with the REAL parser and contain
	// exactly the derived upserts + the passed-through asset.
	bundle, err := icarus.ParseExmodz(exmodz)
	if err != nil {
		t.Fatalf("icarus.ParseExmodz rejected ConvertPak output: %v", err)
	}
	// 1 table row + the EndOfMod sentinel (ParseExmod keeps the sentinel).
	if len(bundle.Diff.Rows) != 2 || bundle.Diff.Rows[0].CurrentFile != "Test-D_Fake.json" {
		t.Fatalf("diff rows mismatch: %+v", bundle.Diff.Rows)
	}
	items := bundle.Diff.Rows[0].FileItems
	if len(items) != 2 || items[0].Name != "Alpha" || items[0].Fields["Speed"] != float64(99) ||
		items[1].Name != "Delta" || items[1].Fields["Speed"] != float64(40) {
		t.Fatalf("upserts mismatch: %+v", items)
	}
	if string(bundle.Assets["TestMod/Mod/ITM/SK_Hat.uasset"]) != "asset-bytes" {
		t.Errorf("asset not passed through: keys %v", bundle.Assets)
	}
}

func TestConvertPakTableNotInBase(t *testing.T) {
	dir := t.TempDir()
	basePak := filepath.Join(dir, "data.pak")
	modPak := filepath.Join(dir, "mod.pak")
	writePak(t, basePak, "", map[string][]byte{
		"Test/D_Fake.json": []byte(fixtureBaseTable),
	})
	writePak(t, modPak, "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Unknown.json": []byte(fixtureModTable),
	})
	_, report, err := ConvertPak(modPak, basePak, Meta{Name: "X"})
	if err != nil {
		t.Fatalf("ConvertPak: %v", err)
	}
	found := false
	for _, f := range report.Findings {
		if f.Kind == "table-not-in-base" && f.Table == "Test/D_Unknown.json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want table-not-in-base finding, got %+v", report.Findings)
	}
	if len(report.Tables) != 0 {
		t.Errorf("unknown table must not be diffed: %v", report.Tables)
	}
}

func TestSanitizeAssetPath(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: `Mod\ITM\SK_Hat.uasset`, want: "Mod/ITM/SK_Hat.uasset"},
		{in: "Mod/./ITM/SK_Hat.uasset", want: "Mod/ITM/SK_Hat.uasset"},
		{in: "../escape.uasset", wantErr: true},
		{in: "/abs/path.uasset", wantErr: true},
		{in: "C:/abs/path.uasset", wantErr: true},
		{in: "nul\x00byte.uasset", wantErr: true},
	}
	for _, tt := range tests {
		got, err := sanitizeAssetPath(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("sanitizeAssetPath(%q): want error, got %q", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("sanitizeAssetPath(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestConvertPakMissingFiles(t *testing.T) {
	if _, _, err := ConvertPak(filepath.Join(t.TempDir(), "nope.pak"), os.DevNull, Meta{}); err == nil {
		t.Fatal("want error for missing pak")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./spike/pakconvert/ -run 'TestConvertPak|TestSanitizeAssetPath' -v`
Expected: FAIL (compile error) with `undefined: ConvertPak`, `undefined: sanitizeAssetPath`

- [ ] **Step 3: Write minimal implementation**

`spike/pakconvert/convert.go`:

```go
package pakconvert

import (
	"fmt"
	"path"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// Report is ConvertPak's full account of what it saw and did — the raw
// material for the spike findings doc.
type Report struct {
	PakPath        string
	MountPoint     string
	Census         map[string]int        // EntryClass.String() -> count
	Tables         map[string]*TableDiff // tablePath -> diff vs live base
	EmbeddedExmods map[string][]byte     // Content-relative path -> raw bytes
	Findings       []Finding
	StaleRows      int
}

// sanitizeAssetPath reimplements (minimally) the discipline of icarus's
// unexported sanitizeAssetPath (compile.go:196): pak entry names are
// untrusted input. Backslashes normalize to '/', then NUL, absolute paths
// (POSIX and drive-letter), and any path escaping its root are rejected.
func sanitizeAssetPath(raw string) (string, error) {
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("asset path contains NUL byte")
	}
	p := strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("asset path %q is absolute", raw)
	}
	if len(p) >= 2 && p[1] == ':' {
		return "", fmt.Errorf("asset path %q is drive-absolute", raw)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("asset path %q escapes its root", raw)
	}
	return clean, nil
}

// ConvertPak converts one prebuilt mod pak into a synthesized .exmodz by
// diffing its full-table snapshots against the LIVE base data.pak (never
// adopting the pak's tables wholesale — they are stale snapshots, see the
// design doc's hazards). Assets pass through; embedded .EXMODs are captured
// for ground-truth comparison, not used for conversion.
func ConvertPak(pakPath, basePakPath string, meta Meta) ([]byte, *Report, error) {
	mod, err := unrealpak.Open(pakPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening mod pak: %w", err)
	}
	defer mod.Close()
	base, err := unrealpak.Open(basePakPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening base pak: %w", err)
	}
	defer base.Close()

	report := &Report{
		PakPath:        pakPath,
		MountPoint:     mod.MountPoint(),
		Census:         map[string]int{},
		Tables:         map[string]*TableDiff{},
		EmbeddedExmods: map[string][]byte{},
	}
	assets := map[string][]byte{}
	var tables []TableEntry

	for _, entry := range mod.Files() {
		class, rel, nerr := NormalizeEntry(mod.MountPoint(), entry.Path)
		report.Census[class.String()]++
		if nerr != nil {
			report.Findings = append(report.Findings, Finding{Kind: "hyphen-path",
				Table: entry.Path, Detail: nerr.Error()})
			continue
		}
		if class == ClassOther {
			continue
		}
		data, rerr := mod.ReadFile(entry.Path)
		if rerr != nil {
			// Oodle or other unsupported compression: record, keep going.
			report.Findings = append(report.Findings, Finding{Kind: "unreadable-entry",
				Table: entry.Path, Detail: rerr.Error()})
			continue
		}
		switch class {
		case ClassEmbeddedExmod:
			report.EmbeddedExmods[rel] = data
		case ClassAsset:
			safe, serr := sanitizeAssetPath(rel)
			if serr != nil {
				report.Findings = append(report.Findings, Finding{Kind: "unsafe-asset-path",
					Table: entry.Path, Detail: serr.Error()})
				continue
			}
			assets[safe] = data
		case ClassTable:
			baseData, berr := base.ReadFile(rel)
			if berr != nil {
				report.Findings = append(report.Findings, Finding{Kind: "table-not-in-base",
					Table: rel, Detail: berr.Error()})
				continue
			}
			td, derr := DiffTable(baseData, data)
			if derr != nil {
				return nil, nil, fmt.Errorf("diffing %s: %w", rel, derr)
			}
			for i := range td.Findings {
				td.Findings[i].Table = rel
			}
			report.Findings = append(report.Findings, td.Findings...)
			report.StaleRows += td.StaleBaseOnlyRows
			report.Tables[rel] = td
			if len(td.Items) > 0 {
				tables = append(tables, TableEntry{CurrentFile: CurrentFileFor(rel), Items: td.Items})
			}
		}
	}

	exmod, err := WriteExmod(meta, tables)
	if err != nil {
		return nil, nil, fmt.Errorf("emitting .EXMOD: %w", err)
	}
	exmodName := strings.ReplaceAll(meta.Name, "/", "_")
	if exmodName == "" {
		exmodName = "converted"
	}
	exmodz, err := WriteExmodz(exmodName, exmod, assets)
	if err != nil {
		return nil, nil, fmt.Errorf("emitting .exmodz: %w", err)
	}
	return exmodz, report, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./spike/pakconvert/ -run 'TestConvertPak|TestSanitizeAssetPath' -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Run the whole spike package + commit**

Run: `go test ./spike/pakconvert/ -v`
Expected: PASS (all tests, Tasks 1–5).
Run: `gofmt -l spike/` (expect no output), then `go vet ./spike/...` (expect no output).

```bash
git add spike/pakconvert/convert.go spike/pakconvert/convert_test.go
git commit -m "spike: ConvertPak orchestration - pak to synthesized exmodz (#220)"
```

---

### Task 6: Ground-truth harness (env-gated integration)

**Files:**

- Create: `spike/pakconvert/reports.go`
- Create: `spike/pakconvert/groundtruth_test.go`

**Interfaces:**

- Consumes: `LoadManifest`, `CorpusMod`, `SaveJSON` (Task 1); `ConvertPak`, `Report` (Task 5); `icarus.ParseExmodz`, `icarus.ParseExmod`, `icarus.ApplyRowPatch`; `unrealpak.Open`.
- Produces: `func spikeEnv(t *testing.T) (icarusDir, corpusDir string)` (t.Skip when unset — Tasks 7–8 reuse it); `type GroundTruthReport struct { ModID, ModName, Verdict string; Residuals []Residual; EmbeddedMatch string }`; `type Residual struct { Table, Row, Class, Detail string }`. Verdict values: `"PASS"`, `"EXPLAINED"`, `"DIVERGED"`. Residual Class values: `"stale-pak-change"`, `"exmodz-newer-than-pak"`, `"diverged"`.

- [ ] **Step 1: Write reports.go (trivial, no separate test — exercised by this task's test)**

`spike/pakconvert/reports.go`:

```go
package pakconvert

// GroundTruthReport is one dual-form mod's conversion-vs-author comparison,
// written to <corpus>/reports/<id>-groundtruth.json.
type GroundTruthReport struct {
	ModID         string
	ModName       string
	Verdict       string // "PASS" | "EXPLAINED" | "DIVERGED"
	Residuals     []Residual
	EmbeddedMatch string // "" (no embedded .EXMOD) | "match" | "mismatch: <detail>"
	StaleRows     int
	Findings      []Finding
}

// Residual is one row where our conversion and the author's exmodz produce
// different final table states, with its classification.
type Residual struct {
	Table  string
	Row    string
	Class  string // "stale-pak-change" | "exmodz-newer-than-pak" | "diverged"
	Detail string
}
```

- [ ] **Step 2: Write the failing/skipping integration test**

`spike/pakconvert/groundtruth_test.go`:

```go
package pakconvert

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// spikeEnv gates the corpus-driven integration tests. Plain `go test ./...`
// must stay green on any machine, so absence of the env vars is a SKIP.
func spikeEnv(t *testing.T) (icarusDir, corpusDir string) {
	t.Helper()
	icarusDir = os.Getenv("LMM_SPIKE_ICARUS_DIR")
	corpusDir = os.Getenv("LMM_SPIKE_CORPUS_DIR")
	if icarusDir == "" || corpusDir == "" {
		t.Skip("set LMM_SPIKE_ICARUS_DIR and LMM_SPIKE_CORPUS_DIR to run spike integration tests")
	}
	return icarusDir, corpusDir
}

func basePakPath(icarusDir string) string {
	// Mirrors core.resolveBasePak (unexported, service.go:1040).
	return filepath.Join(icarusDir, "Icarus", "Content", "Data", "data.pak")
}

// applyItems applies EXMOD rows to base tables, returning tablePath -> final
// decoded table. Uses the REAL icarus.ApplyRowPatch.
func applyItems(t *testing.T, base *unrealpak.Reader, rows []icarus.ExmodRow) map[string]map[string]any {
	t.Helper()
	state := map[string][]byte{}
	for _, row := range rows {
		if row.CurrentFile == "EndOfMod" { // duplicated: unexported upstream const
			continue
		}
		// Reverse of CurrentFileFor — same rule as icarus matchMountPath.
		tablePath := replaceAllDashes(row.CurrentFile)
		cur, ok := state[tablePath]
		if !ok {
			data, err := base.ReadFile(tablePath)
			if err != nil {
				t.Fatalf("base table %s (from CurrentFile %s): %v", tablePath, row.CurrentFile, err)
			}
			cur = data
		}
		next, err := icarus.ApplyRowPatch(cur, row)
		if err != nil {
			t.Fatalf("ApplyRowPatch %s: %v", row.CurrentFile, err)
		}
		state[tablePath] = next
	}
	out := map[string]map[string]any{}
	for p, data := range state {
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("decoding patched %s: %v", p, err)
		}
		out[p] = decoded
	}
	return out
}

// replaceAllDashes reverses CurrentFileFor — same rule as icarus
// matchMountPath (unexported upstream).
func replaceAllDashes(currentFile string) string {
	return strings.ReplaceAll(currentFile, "-", "/")
}

// rowsByName extracts Rows keyed by Name from a decoded table.
func rowsByName(table map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	rows, _ := table["Rows"].([]any)
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			if n, ok := m["Name"].(string); ok {
				out[n] = m
			}
		}
	}
	return out
}

func TestGroundTruthDualFormMods(t *testing.T) {
	icarusDir, corpusDir := spikeEnv(t)
	basePak := basePakPath(icarusDir)
	base, err := unrealpak.Open(basePak)
	if err != nil {
		t.Fatalf("opening live base pak: %v", err)
	}
	defer base.Close()

	manifest, err := LoadManifest(corpusDir)
	if err != nil {
		t.Fatalf("corpus manifest (run cmd/fetchcorpus first): %v", err)
	}

	diverged := 0
	for _, cm := range manifest.Mods {
		if cm.Pak == "" || cm.Exmodz == "" {
			continue // ground truth needs BOTH forms
		}
		cm := cm
		t.Run(cm.Name, func(t *testing.T) {
			pakPath := filepath.Join(corpusDir, cm.Pak)
			meta := Meta{Name: cm.Name, Author: "spike-converted", Version: cm.Version, Description: "converted from pak"}
			synthesized, report, err := ConvertPak(pakPath, basePak, meta)
			if err != nil {
				t.Fatalf("ConvertPak: %v", err)
			}
			ours, err := icarus.ParseExmodz(synthesized)
			if err != nil {
				t.Fatalf("ParseExmodz(ours): %v", err)
			}
			authorData, err := os.ReadFile(filepath.Join(corpusDir, cm.Exmodz))
			if err != nil {
				t.Fatalf("reading author exmodz: %v", err)
			}
			author, err := icarus.ParseExmodz(authorData)
			if err != nil {
				t.Fatalf("ParseExmodz(author): %v", err)
			}

			oursFinal := applyItems(t, base, ours.Diff.Rows)
			authorFinal := applyItems(t, base, author.Diff.Rows)

			// The stale pak snapshot, for residual classification.
			modPak, err := unrealpak.Open(pakPath)
			if err != nil {
				t.Fatalf("reopening mod pak: %v", err)
			}
			defer modPak.Close()

			gt := GroundTruthReport{ModID: cm.ID, ModName: cm.Name,
				StaleRows: report.StaleRows, Findings: report.Findings}
			tables := map[string]bool{}
			for p := range oursFinal {
				tables[p] = true
			}
			for p := range authorFinal {
				tables[p] = true
			}
			sorted := make([]string, 0, len(tables))
			for p := range tables {
				sorted = append(sorted, p)
			}
			sort.Strings(sorted)

			for _, tablePath := range sorted {
				liveData, err := base.ReadFile(tablePath)
				if err != nil {
					t.Fatalf("live base %s: %v", tablePath, err)
				}
				var live map[string]any
				if err := json.Unmarshal(liveData, &live); err != nil {
					t.Fatalf("decoding live %s: %v", tablePath, err)
				}
				liveRows := rowsByName(live)
				oursRows := rowsByName(oursFinal[tablePath])
				authorRows := rowsByName(authorFinal[tablePath])
				if oursFinal[tablePath] == nil {
					oursRows = liveRows // we didn't touch this table
				}
				if authorFinal[tablePath] == nil {
					authorRows = liveRows
				}

				// Stale pak snapshot rows for this table, if present in the pak.
				staleRows := map[string]map[string]any{}
				for _, e := range modPak.Files() {
					class, rel, nerr := NormalizeEntry(modPak.MountPoint(), e.Path)
					if nerr == nil && class == ClassTable && rel == tablePath {
						if data, rerr := modPak.ReadFile(e.Path); rerr == nil {
							var snap map[string]any
							if json.Unmarshal(data, &snap) == nil {
								staleRows = rowsByName(snap)
							}
						}
					}
				}

				names := map[string]bool{}
				for n := range oursRows {
					names[n] = true
				}
				for n := range authorRows {
					names[n] = true
				}
				for n := range names {
					o, a := oursRows[n], authorRows[n]
					if reflect.DeepEqual(o, a) {
						continue
					}
					res := Residual{Table: tablePath, Row: n, Class: "diverged",
						Detail: fmt.Sprintf("ours=%v author=%v", o, a)}
					l, s := liveRows[n], staleRows[n]
					switch {
					case reflect.DeepEqual(a, l) && reflect.DeepEqual(o, s):
						// Author's current exmodz no longer changes this row;
						// the (older) pak did. Pak is stale relative to exmodz.
						res.Class = "stale-pak-change"
					case reflect.DeepEqual(o, l) && a != nil:
						// We suppressed (pak matches live base); author's
						// exmodz is newer than the pak build.
						res.Class = "exmodz-newer-than-pak"
					}
					gt.Residuals = append(gt.Residuals, res)
				}
			}

			gt.Verdict = "PASS"
			for _, r := range gt.Residuals {
				if r.Class == "diverged" {
					gt.Verdict = "DIVERGED"
					break
				}
				gt.Verdict = "EXPLAINED"
			}

			// Embedded data.EXMOD: exact-oracle check of the differ itself.
			for _, raw := range report.EmbeddedExmods {
				embedded, perr := icarus.ParseExmod(raw)
				if perr != nil {
					gt.EmbeddedMatch = "mismatch: embedded .EXMOD unparseable: " + perr.Error()
					continue
				}
				gt.EmbeddedMatch = compareRowSets(embedded.Rows, ours.Diff.Rows)
			}

			if err := SaveJSON(filepath.Join(corpusDir, "reports", cm.ID+"-groundtruth.json"), gt); err != nil {
				t.Fatalf("writing report: %v", err)
			}
			t.Logf("%s: %s (%d residuals, %d stale rows)", cm.Name, gt.Verdict, len(gt.Residuals), gt.StaleRows)
			if gt.Verdict == "DIVERGED" {
				diverged++
				t.Errorf("DIVERGED: %+v", gt.Residuals)
			}
		})
	}
	if diverged > 0 {
		t.Errorf("%d dual-form mods DIVERGED — see <corpus>/reports/", diverged)
	}
}

// compareRowSets reports whether two EXMOD row sets touch the same
// CurrentFiles with the same item Names (field-level detail goes to the
// ground-truth comparison; this is the embedded-oracle sanity check).
func compareRowSets(a, b []icarus.ExmodRow) string {
	key := func(rows []icarus.ExmodRow) map[string][]string {
		out := map[string][]string{}
		for _, r := range rows {
			if r.CurrentFile == "EndOfMod" {
				continue
			}
			var names []string
			for _, it := range r.FileItems {
				names = append(names, it.Name)
			}
			sort.Strings(names)
			out[r.CurrentFile] = names
		}
		return out
	}
	ka, kb := key(a), key(b)
	if reflect.DeepEqual(ka, kb) {
		return "match"
	}
	return fmt.Sprintf("mismatch: embedded=%v ours=%v", ka, kb)
}
```

- [ ] **Step 3: Verify skip-cleanliness and compilation**

Run: `go test ./spike/pakconvert/ -run TestGroundTruth -v`
Expected: `--- SKIP: TestGroundTruthDualFormMods` with the skip reason (env vars unset). Compilation itself is the test here.

- [ ] **Step 4: Manual gate — fetch corpus, then run for real**

Prerequisite (once, manual): `go run ./spike/pakconvert/cmd/fetchcorpus -dir "$HOME/lmm-spike-corpus"`

Run (as ONE command, not a chain):
`LMM_SPIKE_ICARUS_DIR="<your Icarus install root>" LMM_SPIKE_CORPUS_DIR="$HOME/lmm-spike-corpus" go test ./spike/pakconvert/ -run TestGroundTruth -v`
Expected: per-mod `PASS`/`EXPLAINED` verdict lines and reports under `$HOME/lmm-spike-corpus/reports/`. `DIVERGED` verdicts fail the test — each one is a spike finding to investigate (differ bug vs genuine inexpressibility), not something to paper over.

- [ ] **Step 5: Commit**

Run: `gofmt -l spike/` (expect no output), then `go vet ./spike/...` (expect no output).

```bash
git add spike/pakconvert/reports.go spike/pakconvert/groundtruth_test.go
git commit -m "spike: ground-truth harness vs dual-form author exmodz (#220)"
```

---

### Task 7: Pipeline seam check (env-gated integration)

**Files:**

- Create: `spike/pakconvert/pipeline_test.go`

**Interfaces:**

- Consumes: `spikeEnv`, `basePakPath` (Task 6); `LoadManifest` (Task 1); `ConvertPak` (Task 5); `icarus.ValidateSource`, `icarus.MergeCompile`, `icarus.MergeSource`; `unrealpak.Open`.
- Produces: nothing new — this is the seam-validation evidence for spike question 6.

- [ ] **Step 1: Write the integration test**

`spike/pakconvert/pipeline_test.go`:

```go
package pakconvert

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// TestPipelineSeam proves the design's Seam A: a synthesized .exmodz is
// indistinguishable to the real pipeline — icarus.ValidateSource accepts it
// and icarus.MergeCompile merges it alongside a genuine author exmodz with
// zero pipeline changes.
func TestPipelineSeam(t *testing.T) {
	icarusDir, corpusDir := spikeEnv(t)
	basePak := basePakPath(icarusDir)

	manifest, err := LoadManifest(corpusDir)
	if err != nil {
		t.Fatalf("corpus manifest (run cmd/fetchcorpus first): %v", err)
	}

	// Pick one mod with a pak to convert, and one DIFFERENT mod's genuine
	// author exmodz to merge alongside it.
	var convertMod, authorMod *CorpusMod
	for i := range manifest.Mods {
		m := &manifest.Mods[i]
		if convertMod == nil && m.Pak != "" {
			convertMod = m
			continue
		}
		if authorMod == nil && m.Exmodz != "" && (convertMod == nil || m.ID != convertMod.ID) {
			authorMod = m
		}
	}
	if convertMod == nil || authorMod == nil {
		t.Skip("corpus needs at least one pak mod and one other exmodz mod")
	}

	meta := Meta{Name: convertMod.Name, Author: "spike-converted", Version: convertMod.Version}
	synthesized, report, err := ConvertPak(filepath.Join(corpusDir, convertMod.Pak), basePak, meta)
	if err != nil {
		t.Fatalf("ConvertPak: %v", err)
	}

	dir := t.TempDir()
	synthPath := filepath.Join(dir, "converted.exmodz")
	if err := os.WriteFile(synthPath, synthesized, 0o644); err != nil {
		t.Fatalf("writing synthesized exmodz: %v", err)
	}

	// Gate 1: the ingest-time validation hook.
	if err := icarus.ValidateSource(synthPath); err != nil {
		t.Fatalf("icarus.ValidateSource rejected the synthesized exmodz: %v", err)
	}

	// Gate 2: the real merge, author exmodz first, converted mod second
	// (load-order semantics: later wins conflicting fields).
	outPak := filepath.Join(dir, "zzz_LMM_Merged_P.pak")
	sources := []icarus.MergeSource{
		{ModRef: "icarus:" + authorMod.ID, ExmodzPath: filepath.Join(corpusDir, authorMod.Exmodz)},
		{ModRef: "icarus:" + convertMod.ID + "-converted", ExmodzPath: synthPath},
	}
	warnings, err := icarus.MergeCompile(context.Background(), basePak, sources, outPak)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	for _, w := range warnings {
		// Asset collisions across the two mods are legitimate; anything else
		// is unexpected for this corpus pairing.
		if !strings.Contains(w, "is bundled by both") {
			t.Errorf("unexpected merge warning: %s", w)
		}
	}

	// Gate 3: output pak shape — mount point, data/-prefixed tables, and the
	// converted mod's changed rows actually present.
	out, err := unrealpak.Open(outPak)
	if err != nil {
		t.Fatalf("opening merged pak: %v", err)
	}
	defer out.Close()
	// Duplicated literal: icarusContentMountPoint (unexported upstream).
	if got := out.MountPoint(); got != "../../../Icarus/Content/" {
		t.Errorf("merged pak mount point = %q", got)
	}
	for tablePath, td := range report.Tables {
		if len(td.Items) == 0 {
			continue
		}
		// Duplicated literal: icarusDataTablePrefix "data/" (unexported upstream).
		data, err := out.ReadFile("data/" + tablePath)
		if err != nil {
			t.Errorf("converted table %s missing from merged pak: %v", tablePath, err)
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Errorf("merged table %s is not JSON: %v", tablePath, err)
			continue
		}
		merged := rowsByName(decoded)
		for _, item := range td.Items {
			row, ok := merged[item.Name]
			if !ok {
				t.Errorf("row %s missing from merged %s", item.Name, tablePath)
				continue
			}
			for field, want := range item.Fields {
				if got, ok := row[field]; !ok || !jsonEqual(got, want) {
					t.Errorf("merged %s row %s field %s = %v, want %v", tablePath, item.Name, field, got, want)
				}
			}
		}
	}
	t.Logf("seam validated: converted %q merged alongside %q, %d warnings", convertMod.Name, authorMod.Name, len(warnings))
}

// jsonEqual compares two decoded-JSON values via re-marshaling (normalizes
// map ordering and numeric formatting).
func jsonEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}
```

- [ ] **Step 2: Verify skip-cleanliness and compilation**

Run: `go test ./spike/pakconvert/ -run TestPipelineSeam -v`
Expected: `--- SKIP: TestPipelineSeam` (env vars unset).

- [ ] **Step 3: Run for real**

Run (one command):
`LMM_SPIKE_ICARUS_DIR="<your Icarus install root>" LMM_SPIKE_CORPUS_DIR="$HOME/lmm-spike-corpus" go test ./spike/pakconvert/ -run TestPipelineSeam -v`
Expected: PASS with the `seam validated:` log line. A failure here is a core spike finding (the seam is NOT clean) — record it, do not patch internal/.

- [ ] **Step 4: Commit**

Run: `gofmt -l spike/` (expect no output), then `go vet ./spike/...` (expect no output).

```bash
git add spike/pakconvert/pipeline_test.go
git commit -m "spike: pipeline seam check - synthesized exmodz through real MergeCompile (#220)"
```

---

### Task 8: Asset probe (env-gated, report-only)

**Files:**

- Create: `spike/pakconvert/assetprobe_test.go`

**Interfaces:**

- Consumes: `spikeEnv` (Task 6), `LoadManifest`/`SaveJSON` (Task 1), `NormalizeEntry` (Task 2), `unrealpak.Open`, `unrealpak.ErrUnsupportedFormat`.
- Produces: `type AssetProbeReport struct { ModID, ModName, MountPoint string; Entries []ProbeEntry; ExtensionCensus map[string]int; LayoutMatchesInference bool }`; `type ProbeEntry struct { Path string; Size int64; Class, TablePath, ReadError string; Readable bool }`. Report-only: this test NEVER fails on pak content — only on I/O plumbing errors.

- [ ] **Step 1: Write the probe test**

`spike/pakconvert/assetprobe_test.go`:

```go
package pakconvert

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// AssetProbeReport records the empirical dissection of one mod pak —
// answering spike question 4 (does the inferred asset layout hold?).
type AssetProbeReport struct {
	ModID                  string
	ModName                string
	MountPoint             string
	Entries                []ProbeEntry
	ExtensionCensus        map[string]int
	LayoutMatchesInference bool // all assets join under Icarus/Content/ WITHOUT a data/ prefix
}

// ProbeEntry is one pak entry's classification + readability.
type ProbeEntry struct {
	Path      string
	Size      int64
	Class     string
	TablePath string
	Readable  bool
	ReadError string // "" | "oodle-or-unsupported" | raw error text
}

// TestAssetProbe dissects EVERY corpus pak (the asset-heavy ones are the
// point, but census data on all of them is free) and writes
// <corpus>/reports/<id>-assetprobe.json. Report-only: pak content never
// fails the test.
func TestAssetProbe(t *testing.T) {
	_, corpusDir := spikeEnv(t)
	manifest, err := LoadManifest(corpusDir)
	if err != nil {
		t.Fatalf("corpus manifest (run cmd/fetchcorpus first): %v", err)
	}
	for _, cm := range manifest.Mods {
		if cm.Pak == "" {
			continue
		}
		cm := cm
		t.Run(cm.Name, func(t *testing.T) {
			r, err := unrealpak.Open(filepath.Join(corpusDir, cm.Pak))
			if err != nil {
				// Unreadable pak IS a census result, not a test failure.
				report := AssetProbeReport{ModID: cm.ID, ModName: cm.Name,
					ExtensionCensus: map[string]int{"UNOPENABLE: " + err.Error(): 1}}
				if serr := SaveJSON(filepath.Join(corpusDir, "reports", cm.ID+"-assetprobe.json"), report); serr != nil {
					t.Fatalf("writing report: %v", serr)
				}
				t.Logf("%s: pak unopenable: %v", cm.Name, err)
				return
			}
			defer r.Close()

			report := AssetProbeReport{
				ModID: cm.ID, ModName: cm.Name, MountPoint: r.MountPoint(),
				ExtensionCensus:        map[string]int{},
				LayoutMatchesInference: true,
			}
			for _, e := range r.Files() {
				pe := ProbeEntry{Path: e.Path, Size: e.Size}
				ext := strings.ToLower(path.Ext(e.Path))
				if ext == "" {
					ext = "(none)"
				}
				report.ExtensionCensus[ext]++

				class, rel, nerr := NormalizeEntry(r.MountPoint(), e.Path)
				pe.Class = class.String()
				if nerr != nil {
					pe.Class = "hyphen-error"
				} else if class == ClassTable {
					pe.TablePath = rel
				}

				if _, rerr := r.ReadFile(e.Path); rerr != nil {
					pe.Readable = false
					if errors.Is(rerr, unrealpak.ErrUnsupportedFormat) {
						pe.ReadError = "oodle-or-unsupported"
					} else {
						pe.ReadError = rerr.Error()
					}
				} else {
					pe.Readable = true
				}

				if class == ClassAsset {
					// Inference under probe (pak-divergence-report §2): assets
					// join under Icarus/Content/ with NO data/ prefix.
					full := path.Join(r.MountPoint(), e.Path)
					if !strings.HasPrefix(full, "../../../Icarus/Content/") ||
						strings.HasPrefix(strings.TrimPrefix(full, "../../../Icarus/Content/"), "data/") {
						report.LayoutMatchesInference = false
					}
				}
				report.Entries = append(report.Entries, pe)
			}
			if err := SaveJSON(filepath.Join(corpusDir, "reports", cm.ID+"-assetprobe.json"), report); err != nil {
				t.Fatalf("writing report: %v", err)
			}
			t.Logf("%s: %d entries, mount %q, extensions %v, layoutMatchesInference=%v",
				cm.Name, len(report.Entries), report.MountPoint, report.ExtensionCensus, report.LayoutMatchesInference)
		})
	}
}
```

- [ ] **Step 2: Verify skip-cleanliness and compilation**

Run: `go test ./spike/pakconvert/ -run TestAssetProbe -v`
Expected: `--- SKIP: TestAssetProbe` (env vars unset).

- [ ] **Step 3: Run for real**

Run (one command):
`LMM_SPIKE_ICARUS_DIR="<your Icarus install root>" LMM_SPIKE_CORPUS_DIR="$HOME/lmm-spike-corpus" go test ./spike/pakconvert/ -run TestAssetProbe -v`
Expected: PASS with per-mod log lines; reports under `$HOME/lmm-spike-corpus/reports/`. Pay attention to the Larkwell / Turret Variants lines — their `LayoutMatchesInference` and extension census answer spike question 4.

- [ ] **Step 4: Full offline suite sanity + commit**

Run: `go test ./spike/pakconvert/ -v`
Expected: unit tests PASS, the three integration tests SKIP (no env vars in this invocation).
Run: `go test ./...`
Expected: whole repo green (spike package included, integration skipped).
Run: `gofmt -l spike/` (expect no output), then `go vet ./spike/...` (expect no output).

```bash
git add spike/pakconvert/assetprobe_test.go
git commit -m "spike: asset-bearing pak probe, report-only (#220)"
```

---

### Task 9: Findings doc + wrap-up

**Files:**

- Create: `docs/plans/<today's date>-icarus-pak-to-exmod-spike-findings.md` (gitignored — force-add)
- Read: every `$LMM_SPIKE_CORPUS_DIR/reports/*.json`, `$LMM_SPIKE_CORPUS_DIR/catalog-census.json`, and the design doc `docs/plans/2026-08-05-icarus-pak-to-exmod-spike-design.md`

**Interfaces:**

- Consumes: all reports from Tasks 6–8. Produces the spike's durable deliverable. No new Go code.

- [ ] **Step 1: Aggregate the numbers**

Read `catalog-census.json` and every `reports/*.json`. Compute: total catalog mods; pak-only / exmodz-only / dual-form counts; per-verdict counts (PASS / EXPLAINED / DIVERGED); total residuals by class; finding-kind frequencies (`defaults-changed`, `field-removed`, `table-not-in-base`, `hyphen-path`, `unreadable-entry`, `duplicate-row-name`, `top-level-changed`, `unsafe-asset-path`); asset-probe layout confirmations; stale-row totals.

- [ ] **Step 2: Write the findings doc**

Structure (real section headings — fill every one; a section with nothing to report says so explicitly):

```markdown
# Icarus PAK → EXMOD Conversion Spike — Findings

**Date:** <today> **Spike issue:** #220 **Branch:** spike/pak-to-exmod
**Design doc:** 2026-08-05-icarus-pak-to-exmod-spike-design.md

## Verdict: GO | PARTIAL GO | NO-GO

<One paragraph. Apply the design rubric verbatim: GO requires semantic
equivalence on ≥3 of 4 sampled dual-form mods with every residual explained,
clean MergeCompile, and the seam validated end-to-end.>

## Evidence Summary

<Ground-truth verdict table: mod | verdict | residuals | classification.
MergeCompile/ValidateSource results. Embedded-.EXMOD oracle outcomes.>

## Census

<Catalog: N mods, X pak-only, Y dual-form, Z exmodz-only. Corpus: fetched
counts. Convertibility: readable/unreadable, tables/assets/mixed.>

## Answers to the Six Spike Questions

### 1. Path normalization ### 2. Differ fidelity

### 3. Convertibility census ### 4. Asset probe

### 5. Expressiveness gaps ### 6. Seam validation

<Each: the design doc's question restated in one line, then the empirical
answer with numbers from the reports.>

## Expressiveness-Gap Frequencies

<How often Defaults-changed / field-removed / hyphen / table-not-in-base
actually occurred, and what that means for a production converter.>

## Constraints a Feature Design Must Honor

<From the evidence. At minimum address: stale-snapshot diffing (live base,
absent-rows-are-staleness); mount+entry normalized as a pair; the four
unexported upstream values (endOfModSentinel, icarusContentMountPoint,
icarusDataTablePrefix, the '-'/'/' flatten) that should be EXPORTED (or a
conversion API added to internal/source/icarus) before production work;
fingerprint membership for converted paks; pak/exmodz alternate-form
validation and #211 IsPrimary; double-apply prevention (deployableFiles +
PruneUnclaimed).>

## Open Product Questions

<Selection UX, defaults, migration of already-installed pak mods, etc. —
questions, not answers.>

## Raw Data

<Corpus dir layout note + reminder reports are NOT committed.>
```

- [ ] **Step 3: Self-check the verdict against the rubric**

Re-read the design doc's "Go / No-Go Rubric" section and confirm the verdict paragraph cites the actual numbers (e.g. "4/6 PASS, 2/6 EXPLAINED, 0 DIVERGED"). If any DIVERGED verdict remains unexplained, the verdict CANNOT be GO — say so honestly.

- [ ] **Step 4: Commit (force-add, docs are gitignored) and update the spike issue**

```bash
git add -f docs/plans/*-icarus-pak-to-exmod-spike-findings.md
git commit -m "spike: findings and go/no-go recommendation (#220)"
git push origin spike/pak-to-exmod
```

Then post the summary to the issue (fill the verdict line from the doc):

```bash
gh issue comment 220 --body "Spike complete on \`spike/pak-to-exmod\`. Verdict: <GO|PARTIAL GO|NO-GO> — <one-sentence why>. Full findings: docs/plans/<date>-icarus-pak-to-exmod-spike-findings.md on the spike branch. Key numbers: <dual-form verdicts>, <census summary>, <seam result>."
```

- [ ] **Step 5: Final verification sweep**

Run: `go test ./...`
Expected: green everywhere (integration tests skip without env vars).
Run: `git status`
Expected: clean tree; corpus dir and reports remain OUTSIDE the repo; nothing under `spike/` or `docs/plans/` left uncommitted on the spike branch.

---

## Execution Notes

- Tasks 1–5 are fully offline and machine-independent. Tasks 6–8 need the user's real Icarus install + a fetched corpus; coordinate the one-time `fetchcorpus` run before starting Task 6.
- Task order is strict for 1→5 (each consumes the previous task's interfaces). Tasks 6/7/8 are independent of each other once 5 is done — parallelizable across workers IF each runs the env-gated tests on the same machine/corpus.
- Every DIVERGED ground-truth verdict, seam failure, or layout-inference refutation is spike EVIDENCE, not a bug to silently fix around: record it in the findings doc even when a differ fix also lands.
