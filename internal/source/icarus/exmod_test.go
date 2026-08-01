package icarus

import (
	"encoding/json"
	"strings"
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

// TestParseExmod_FileItemWithoutName_Errors pins the loud-error path for a
// File_Items entry with no Name — unchanged by the #175 ApplyRowPatch fix,
// but previously untested directly.
func TestParseExmod_FileItemWithoutName_Errors(t *testing.T) {
	manifest := `{"name":"X","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"BaseMovementSpeed":235}]}]}`
	_, err := ParseExmod([]byte(manifest))
	if err == nil {
		t.Fatal("expected an error for a File_Item with no Name, got nil")
	}
	if !strings.Contains(err.Error(), "AI-D_AIGrowth.json") {
		t.Errorf("error %q should name the offending row", err)
	}
}

// realBaseTable is a stand-in for a real Icarus DataTable JSON export — the
// standard Unreal Engine shape confirmed against a live data.pak
// (task-7-report.md): {"RowStruct", "Defaults", "Rows": [{"Name", ...}]},
// not the flat {name: {fields}} map ApplyRowPatch originally (and
// incorrectly) assumed.
const realBaseTable = `{
	"RowStruct": "/Script/Icarus.AIGrowth",
	"Defaults": {"Health": "None"},
	"Rows": [
		{"Name": "Mount_Bear", "BaseMovementSpeed": 200, "BaseSwimSpeed": 150, "Untouched": "keep-me"},
		{"Name": "Other_Row", "BaseMovementSpeed": 999}
	]
}`

func decodeRows(t *testing.T, patched []byte) []map[string]any {
	t.Helper()
	var doc struct {
		Rows []map[string]any `json:"Rows"`
	}
	if err := json.Unmarshal(patched, &doc); err != nil {
		t.Fatalf("unmarshaling patched result: %v", err)
	}
	return doc.Rows
}

func findRow(t *testing.T, rows []map[string]any, name string) map[string]any {
	t.Helper()
	for _, r := range rows {
		if r["Name"] == name {
			return r
		}
	}
	t.Fatalf("no row named %q in %+v", name, rows)
	return nil
}

func TestApplyRowPatch_PatchesExistingRow(t *testing.T) {
	row := ExmodRow{
		CurrentFile: "AI-D_AIGrowth.json",
		FileItems: []ExmodFileItem{
			{Name: "Mount_Bear", Fields: map[string]any{"BaseMovementSpeed": float64(235)}},
		},
	}

	got, err := ApplyRowPatch([]byte(realBaseTable), row)
	if err != nil {
		t.Fatalf("ApplyRowPatch: %v", err)
	}

	rows := decodeRows(t, got)
	if len(rows) != 2 {
		t.Fatalf("Rows = %d entries, want 2 (no rows added)", len(rows))
	}
	mountBear := findRow(t, rows, "Mount_Bear")
	if mountBear["BaseMovementSpeed"] != float64(235) {
		t.Errorf("BaseMovementSpeed not patched: %v", mountBear["BaseMovementSpeed"])
	}
	if mountBear["Untouched"] != "keep-me" {
		t.Errorf("unrelated field was clobbered: %v", mountBear["Untouched"])
	}
	other := findRow(t, rows, "Other_Row")
	if other["BaseMovementSpeed"] != float64(999) {
		t.Errorf("unrelated row was modified: %v", other)
	}
}

// TestApplyRowPatch_AddsNewRow pins the #175 fix: a File_Item whose Name has
// no match in the base table's Rows is appended as a brand-new row instead
// of erroring — real content-adding mods like Bear_Mount need this (see
// task-7-report.md's "Mount_Bear does not exist in the live install"
// finding).
func TestApplyRowPatch_AddsNewRow(t *testing.T) {
	row := ExmodRow{
		CurrentFile: "AI-D_AIGrowth.json",
		FileItems: []ExmodFileItem{
			{Name: "Juvenile_Bear", Fields: map[string]any{"BaseMovementSpeed": float64(144)}},
		},
	}

	got, err := ApplyRowPatch([]byte(realBaseTable), row)
	if err != nil {
		t.Fatalf("ApplyRowPatch: %v", err)
	}

	rows := decodeRows(t, got)
	if len(rows) != 3 {
		t.Fatalf("Rows = %d entries, want 3 (2 original + 1 added)", len(rows))
	}
	added := findRow(t, rows, "Juvenile_Bear")
	if added["BaseMovementSpeed"] != float64(144) {
		t.Errorf("added row BaseMovementSpeed = %v, want 144", added["BaseMovementSpeed"])
	}
	if findRow(t, rows, "Mount_Bear")["BaseMovementSpeed"] != float64(200) {
		t.Error("existing row was modified by an unrelated add")
	}
}

func TestApplyRowPatch_MixedPatchAndAddInOneTable(t *testing.T) {
	row := ExmodRow{
		CurrentFile: "AI-D_AIGrowth.json",
		FileItems: []ExmodFileItem{
			{Name: "Mount_Bear", Fields: map[string]any{"BaseMovementSpeed": float64(235)}},
			{Name: "Juvenile_Bear", Fields: map[string]any{"BaseMovementSpeed": float64(144)}},
		},
	}

	got, err := ApplyRowPatch([]byte(realBaseTable), row)
	if err != nil {
		t.Fatalf("ApplyRowPatch: %v", err)
	}

	rows := decodeRows(t, got)
	if len(rows) != 3 {
		t.Fatalf("Rows = %d entries, want 3", len(rows))
	}
	if findRow(t, rows, "Mount_Bear")["BaseMovementSpeed"] != float64(235) {
		t.Error("existing row was not patched")
	}
	if findRow(t, rows, "Juvenile_Bear")["BaseMovementSpeed"] != float64(144) {
		t.Error("new row was not added")
	}
}

func TestApplyRowPatch_PreservesDefaultsAndRowStruct(t *testing.T) {
	row := ExmodRow{
		CurrentFile: "AI-D_AIGrowth.json",
		FileItems: []ExmodFileItem{
			{Name: "Mount_Bear", Fields: map[string]any{"BaseMovementSpeed": float64(235)}},
		},
	}

	got, err := ApplyRowPatch([]byte(realBaseTable), row)
	if err != nil {
		t.Fatalf("ApplyRowPatch: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}
	if doc["RowStruct"] != "/Script/Icarus.AIGrowth" {
		t.Errorf("RowStruct = %v, want preserved", doc["RowStruct"])
	}
	defaults, ok := doc["Defaults"].(map[string]any)
	if !ok || defaults["Health"] != "None" {
		t.Errorf("Defaults = %v, want preserved", doc["Defaults"])
	}
}

func TestApplyRowPatch_Errors(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		wantErr string
	}{
		{
			name:    "unparseable base JSON",
			base:    `not json`,
			wantErr: "AI-D_AIGrowth.json",
		},
		{
			name:    "missing Rows key",
			base:    `{"RowStruct": "/Script/Icarus.AIGrowth", "Defaults": {}}`,
			wantErr: "AI-D_AIGrowth.json",
		},
		{
			name:    "Rows present but not an array",
			base:    `{"Rows": "not-an-array"}`,
			wantErr: "AI-D_AIGrowth.json",
		},
		{
			name:    "Rows entry not an object",
			base:    `{"Rows": ["not-an-object"]}`,
			wantErr: "AI-D_AIGrowth.json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := ExmodRow{
				CurrentFile: "AI-D_AIGrowth.json",
				FileItems:   []ExmodFileItem{{Name: "X", Fields: map[string]any{}}},
			}
			_, err := ApplyRowPatch([]byte(tt.base), row)
			if err == nil {
				t.Fatalf("ApplyRowPatch(%s): expected an error, got nil", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err, tt.wantErr)
			}
		})
	}
}
