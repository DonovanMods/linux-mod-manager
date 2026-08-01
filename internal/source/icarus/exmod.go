package icarus

import (
	"encoding/json"
	"fmt"
)

// ExmodDiff is the parsed .EXMOD manifest — a diff against the base game's
// JSON data tables, not a binary/compiled-asset diff (confirmed against a
// real sample; see docs/plans/2026-07-29-icarus-exmod-pak-research.md).
type ExmodDiff struct {
	Name        string
	Author      string
	Version     string
	Description string
	Rows        []ExmodRow
}

// ExmodRow targets one base data-table file (e.g. "AI-D_AIGrowth.json").
type ExmodRow struct {
	CurrentFile string
	FileItems   []ExmodFileItem
}

// ExmodFileItem upserts fields on the base row named Name — patching it if
// it already exists, adding it as a new row otherwise (see ApplyRowPatch).
// Fields holds every key from the source JSON except "Name" itself,
// generically — the real schema nests arbitrary game-data shapes here (see
// package doc comment), so this deliberately does not enumerate them.
type ExmodFileItem struct {
	Name   string
	Fields map[string]any
}

func ParseExmod(data []byte) (*ExmodDiff, error) {
	var raw struct {
		Name        string `json:"name"`
		Author      string `json:"author"`
		Version     string `json:"version"`
		Description string `json:"description"`
		Rows        []struct {
			CurrentFile string           `json:"CurrentFile"`
			FileItems   []map[string]any `json:"File_Items"`
		} `json:"Rows"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("icarus: parsing .EXMOD: %w", err)
	}

	diff := &ExmodDiff{Name: raw.Name, Author: raw.Author, Version: raw.Version, Description: raw.Description}
	for _, r := range raw.Rows {
		row := ExmodRow{CurrentFile: r.CurrentFile}
		for _, item := range r.FileItems {
			name, _ := item["Name"].(string)
			if name == "" {
				return nil, fmt.Errorf("icarus: .EXMOD row in %s: File_Items entry missing Name", r.CurrentFile)
			}
			fields := make(map[string]any, len(item)-1)
			for k, v := range item {
				if k == "Name" {
					continue
				}
				fields[k] = v
			}
			row.FileItems = append(row.FileItems, ExmodFileItem{Name: name, Fields: fields})
		}
		diff.Rows = append(diff.Rows, row)
	}
	return diff, nil
}

// ApplyRowPatch applies row's File_Items to baseJSON, a real Icarus
// DataTable JSON export — the standard Unreal Engine shape
// {"RowStruct": "...", "Defaults": {...}, "Rows": [{"Name": "...", ...fields}, ...]},
// confirmed against a real installed data.pak (task-7-report.md); not the
// flat {name: {fields}} map this function originally assumed, which never
// matched real game data and was only ever exercised against synthetic
// fixtures.
//
// Each File_Item is an upsert, not a strict patch: if its Name matches an
// existing entry in Rows, that row's fields are shallow-merged with the
// item's fields (item fields win, everything else on the row survives
// untouched); if no row has that Name, the item is appended to Rows
// verbatim as a brand-new row. This matches what real .EXMOD content
// actually does — most rows patch existing base stats, but a
// content-adding mod (e.g. a new mountable species) introduces rows the
// base game doesn't have yet, and erroring on that (the original
// patch-only design) made every such mod uncompilable. All other top-level
// keys on the base document (RowStruct, Defaults, and anything else) pass
// through re-serialization unchanged, since only doc["Rows"] is ever
// modified. Output is deterministic: encoding/json sorts map keys.
func ApplyRowPatch(baseJSON []byte, row ExmodRow) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(baseJSON, &doc); err != nil {
		return nil, fmt.Errorf("icarus: parsing base data table %s: %w", row.CurrentFile, err)
	}
	rawRows, ok := doc["Rows"]
	if !ok {
		return nil, fmt.Errorf("icarus: base data table %s: no top-level %q array", row.CurrentFile, "Rows")
	}
	rowsSlice, ok := rawRows.([]any)
	if !ok {
		return nil, fmt.Errorf("icarus: base data table %s: %q is not an array", row.CurrentFile, "Rows")
	}
	rows := make([]map[string]any, len(rowsSlice))
	for i, r := range rowsSlice {
		m, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("icarus: base data table %s: Rows[%d] is not an object", row.CurrentFile, i)
		}
		rows[i] = m
	}

	byName := make(map[string]int, len(rows))
	for i, r := range rows {
		if name, ok := r["Name"].(string); ok {
			byName[name] = i
		}
	}

	for _, item := range row.FileItems {
		if idx, ok := byName[item.Name]; ok {
			target := rows[idx]
			for k, v := range item.Fields {
				target[k] = v
			}
			continue
		}
		newRow := make(map[string]any, len(item.Fields)+1)
		newRow["Name"] = item.Name
		for k, v := range item.Fields {
			newRow[k] = v
		}
		rows = append(rows, newRow)
		byName[item.Name] = len(rows) - 1
	}

	newRows := make([]any, len(rows))
	for i, r := range rows {
		newRows[i] = r
	}
	doc["Rows"] = newRows

	return json.Marshal(doc)
}
