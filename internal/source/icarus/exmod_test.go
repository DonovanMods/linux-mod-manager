package icarus

import (
	"encoding/json"
	"testing"
)

const sampleExmod = `{
  "name": "Bear Mount",
  "author": "Jimk72",
  "version": "3.3",
  "description": "Allows raising cubs",
  "Rows": [
    {
      "CurrentFile": "AI-D_AIGrowth.json",
      "File_Items": [
        {"Name": "Mount_Bear", "BaseMovementSpeed": 235, "BaseSwimSpeed": 300}
      ]
    }
  ]
}`

func TestParseExmod(t *testing.T) {
	diff, err := ParseExmod([]byte(sampleExmod))
	if err != nil {
		t.Fatalf("ParseExmod: %v", err)
	}
	if diff.Name != "Bear Mount" || diff.Version != "3.3" {
		t.Errorf("Name/Version = %q/%q, want Bear Mount/3.3", diff.Name, diff.Version)
	}
	if len(diff.Rows) != 1 || diff.Rows[0].CurrentFile != "AI-D_AIGrowth.json" {
		t.Fatalf("Rows = %+v", diff.Rows)
	}
	if len(diff.Rows[0].FileItems) != 1 || diff.Rows[0].FileItems[0].Name != "Mount_Bear" {
		t.Fatalf("FileItems = %+v", diff.Rows[0].FileItems)
	}
	if diff.Rows[0].FileItems[0].Fields["BaseMovementSpeed"] != float64(235) {
		t.Errorf("BaseMovementSpeed = %v, want 235", diff.Rows[0].FileItems[0].Fields["BaseMovementSpeed"])
	}
}

func TestApplyRowPatch_OverwritesNamedRowFieldsOnly(t *testing.T) {
	base := []byte(`{
		"Mount_Bear": {"BaseMovementSpeed": 200, "BaseSwimSpeed": 150, "Untouched": "keep-me"},
		"Other_Row": {"BaseMovementSpeed": 999}
	}`)
	row := ExmodRow{
		CurrentFile: "AI-D_AIGrowth.json",
		FileItems: []ExmodFileItem{
			{Name: "Mount_Bear", Fields: map[string]any{"BaseMovementSpeed": float64(235)}},
		},
	}

	got, err := ApplyRowPatch(base, row)
	if err != nil {
		t.Fatalf("ApplyRowPatch: %v", err)
	}

	var result map[string]map[string]any
	if err := json.Unmarshal(got, &result); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}
	if result["Mount_Bear"]["BaseMovementSpeed"] != float64(235) {
		t.Errorf("BaseMovementSpeed not patched: %v", result["Mount_Bear"]["BaseMovementSpeed"])
	}
	if result["Mount_Bear"]["Untouched"] != "keep-me" {
		t.Errorf("unrelated field was clobbered: %v", result["Mount_Bear"]["Untouched"])
	}
	if result["Other_Row"]["BaseMovementSpeed"] != float64(999) {
		t.Errorf("unrelated row was modified: %v", result["Other_Row"])
	}
}

func TestApplyRowPatch_UnknownRowName_Errors(t *testing.T) {
	base := []byte(`{"Mount_Bear": {}}`)
	row := ExmodRow{FileItems: []ExmodFileItem{{Name: "Does_Not_Exist", Fields: map[string]any{"X": 1}}}}

	if _, err := ApplyRowPatch(base, row); err == nil {
		t.Error("expected error for unknown row name (no silent fallback), got nil")
	}
}
