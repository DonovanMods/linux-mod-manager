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
