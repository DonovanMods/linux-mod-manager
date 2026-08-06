package icarus

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
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
			entry:     "data/Experience/D_ExperienceEvents.json",
			wantClass: classTable, wantPath: "Experience/D_ExperienceEvents.json",
		},
		{
			// FloofLevelCap layout: data/ boundary inside the mount point.
			name: "table with data prefix in mount", mount: "../../../Icarus/Content/data/Character/",
			entry:     "D_CharacterGrowth.json",
			wantClass: classTable, wantPath: "Character/D_CharacterGrowth.json",
		},
		{
			// Eye Colors Expanded! layout: capital Data/ segment (spike round-3
			// audit) - classification must be case-insensitive but the returned
			// path must preserve ORIGINAL case for base.ReadFile lookups.
			name: "capital Data segment", mount: "../../../Icarus/Content/Data/",
			entry:     "Inventory/D_InventoryInfo.json",
			wantClass: classTable, wantPath: "Inventory/D_InventoryInfo.json",
		},
		{
			name: "embedded exmod", mount: "../../../Icarus/Content/",
			entry:     "data.EXMOD",
			wantClass: classEmbeddedExmod, wantPath: "data.EXMOD",
		},
		{
			name: "uasset asset", mount: "../../../Icarus/Content/",
			entry:     "Mods/Bear/SK_Saddle.uasset",
			wantClass: classAsset, wantPath: "Mods/Bear/SK_Saddle.uasset",
		},
		{
			name: "uexp asset", mount: "../../../Icarus/Content/",
			entry:     "Mods/Bear/SK_Saddle.uexp",
			wantClass: classAsset, wantPath: "Mods/Bear/SK_Saddle.uexp",
		},
		{
			// Intreeg's More Resources layout: bare Content/, no Icarus/ segment
			// - unmappable, classifies other (Task 4 turns json-others into a
			// whole-mod error).
			name: "bare content json is other", mount: "../../../Content/",
			entry:     "D_ProcessorRecipes.json",
			wantClass: classOther, wantPath: "",
		},
		{
			name: "json outside data dir is other", mount: "../../../Icarus/Content/",
			entry:     "Readme/notes.json",
			wantClass: classOther, wantPath: "",
		},
		{
			name: "hyphenated table path errors", mount: "../../../Icarus/Content/",
			entry:     "data/AI/D_AI-Growth.json",
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

	t.Run("multiple removed fields produce sorted deterministic warnings", func(t *testing.T) {
		// Use a custom base with one row having 3 fields to test removal ordering.
		baseMulti := []byte(`{
			"RowStruct": "/Script/Icarus.Growth",
			"Defaults": {"XP": 1},
			"Rows": [
				{"Name": "TestRow", "Speed": 5, "Level": 1, "XP": 10}
			]
		}`)
		// Mod removes Speed and XP, keeps only Name and Level.
		mod := []byte(`{
			"RowStruct": "/Script/Icarus.Growth",
			"Defaults": {"XP": 1},
			"Rows": [
				{"Name": "TestRow", "Level": 1}
			]
		}`)
		items, warnings, err := diffTable("Test/D_Growth.json", baseMulti, mod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// TestRow has Level unchanged; Speed and XP removed.
		// Should emit 0 items (Level unchanged, Speed/XP removed can't be expressed).
		// Should emit 2 warnings, in sorted order: Speed, then XP.
		if len(items) != 0 {
			t.Fatalf("want 0 items, got %+v", items)
		}
		if len(warnings) != 2 {
			t.Fatalf("want 2 warnings (Speed, XP removed), got %d: %v", len(warnings), warnings)
		}
		// Verify sorted order: Speed < XP alphabetically.
		if !strings.Contains(warnings[0], "Speed") {
			t.Fatalf("want first warning to mention Speed, got: %v", warnings[0])
		}
		if !strings.Contains(warnings[1], "XP") {
			t.Fatalf("want second warning to mention XP, got: %v", warnings[1])
		}
	})
}

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
		"data/Test/D_Growth.json":    []byte(modTable),
		"Mods/Thing/SK_Thing.uasset": {0x01, 0x02},
		"readme.txt":                 []byte("ignore me"),
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
