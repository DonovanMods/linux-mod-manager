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
