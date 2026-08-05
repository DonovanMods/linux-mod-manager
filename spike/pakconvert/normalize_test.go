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
			// Real layout from the ground-truth audit (round-3 fix, #220):
			// Eye Colors Expanded!'s mount carries a capital "Data/" segment
			// — reports/55F4mIY6qi5RYsAY278Y-assetprobe.json, mount
			// "../../../Icarus/Content/Data/Inventory/". Case-sensitive
			// matching classified this ClassOther with no Finding, silently
			// hiding a real table from the converter. Case-insensitive
			// matching must recover ClassTable AND preserve the original
			// "Inventory/D_InventoryInfo.json" casing — the live base pak's
			// actual entry name — so base.ReadFile still resolves it.
			name:  "table with capital Data mount segment (audit variant, case-insensitive)",
			mount: "../../../Icarus/Content/Data/Inventory/", entry: "D_InventoryInfo.json",
			wantClass: ClassTable, wantPath: "Inventory/D_InventoryInfo.json",
		},
		{
			// Real layout from the ground-truth audit (round-3 fix, #220):
			// Intreeg's More Resources' mount is bare "Content/" with the
			// entry dropped directly at the Content root, no "Data/" segment
			// anywhere — reports/JfZ0dRNFJWrvi5gpVXFq-assetprobe.json, mount
			// "../../../Icarus/Content/", entry "D_ProcessorRecipes.json".
			// There is no "data" substring to match case-insensitively, so
			// this correctly (and deliberately) stays ClassOther even after
			// the case-insensitivity fix — the entry carries no subdirectory
			// information, so there is no path to recover the real base
			// table ("Crafting/D_ProcessorRecipes.json") from. The
			// groundtruth harness's Census["other"]>0 guard (round-3 fix,
			// groundtruth_test.go) is the safety net for exactly this case.
			name:  "bare Content json with no data segment stays Other (audit variant, unresolvable)",
			mount: "../../../Icarus/Content/", entry: "D_ProcessorRecipes.json",
			wantClass: ClassOther, wantPath: "",
		},
		{
			// Synthetic (not from the audit): confirms the mount-prefix half
			// of the case-insensitivity fix independently of the data/
			// prefix half above.
			name:  "table with all-caps mount prefix (synthetic, case-insensitive)",
			mount: "../../../ICARUS/CONTENT/", entry: "Data/Character/D_CharacterGrowth.json",
			wantClass: ClassTable, wantPath: "Character/D_CharacterGrowth.json",
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
