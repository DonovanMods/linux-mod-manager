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
