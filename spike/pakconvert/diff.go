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
