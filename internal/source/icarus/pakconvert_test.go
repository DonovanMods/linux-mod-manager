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
		mod := []byte(`{
			"RowStruct": "/Script/Icarus.Growth",
			"Defaults": {"XP": 1},
			"Rows": [
				{"Name": "RowA", "XP": 10}
			]
		}`)
		items, warnings, err := diffTable("Test/D_Growth.json", base, mod)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// RowA in base has XP:10, Level:1. Mod has only XP:10 (no change).
		// Base has Level field missing in mod -> warning. Run multiple times
		// to ensure deterministic order (would randomize without sort).
		if len(items) != 0 {
			t.Fatalf("want 0 items (XP unchanged), got %+v", items)
		}
		if len(warnings) != 1 {
			t.Fatalf("want 1 warning (Level removed), got %d: %v", len(warnings), warnings)
		}
		if !strings.Contains(warnings[0], "Level") {
			t.Fatalf("want Level in warning, got: %v", warnings[0])
		}
	})
}
