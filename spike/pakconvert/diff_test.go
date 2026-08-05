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

func TestDiffTableRemovedTopLevelKey(t *testing.T) {
	// Mod table is missing a top-level key present in base (e.g., "Extra")
	// This should generate a top-level-changed finding.
	mod := `{
		"RowStruct": "/Script/Icarus.FakeRow",
		"Defaults": {"Speed": 1},
		"Rows": [
			{"Name": "Alpha", "Speed": 10, "Nested": {"A": 1, "B": 2}},
			{"Name": "Beta", "Speed": 20},
			{"Name": "Gamma", "Speed": 30}
		],
		"ExtraKey": "extra_value"
	}`
	baseWithExtra := `{
		"RowStruct": "/Script/Icarus.FakeRow",
		"Defaults": {"Speed": 1},
		"Rows": [
			{"Name": "Alpha", "Speed": 10, "Nested": {"A": 1, "B": 2}},
			{"Name": "Beta", "Speed": 20},
			{"Name": "Gamma", "Speed": 30}
		],
		"ExtraKey": "extra_value",
		"RemovedKey": "removed_value"
	}`
	d := mustDiff(t, baseWithExtra, mod)
	found := false
	for _, f := range d.Findings {
		if f.Kind == "top-level-changed" && f.Detail == "top-level key \"RemovedKey\" differs from base" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("want top-level-changed finding for removed key, got findings: %+v", d.Findings)
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
