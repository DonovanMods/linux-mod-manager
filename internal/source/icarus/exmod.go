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

// ExmodFileItem overrides fields on the base row named Name. Fields holds
// every key from the source JSON except "Name" itself, generically — the
// real schema nests arbitrary game-data shapes here (see package doc
// comment), so this deliberately does not enumerate them.
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

// ApplyRowPatch merges row's named-row field overrides into baseJSON (a base
// game data-table file keyed by row name, e.g. {"Mount_Bear": {...}, ...})
// and returns the patched document. Fails loudly (no silent fallback, repo
// precedent #95) if a targeted row name doesn't exist in the base — that
// means either the base version is stale relative to the mod, or the exmod
// targets a file this function was called with by mistake.
func ApplyRowPatch(baseJSON []byte, row ExmodRow) ([]byte, error) {
	var doc map[string]map[string]any
	if err := json.Unmarshal(baseJSON, &doc); err != nil {
		return nil, fmt.Errorf("icarus: parsing base data table %s: %w", row.CurrentFile, err)
	}
	for _, item := range row.FileItems {
		target, ok := doc[item.Name]
		if !ok {
			return nil, fmt.Errorf("icarus: %s: row %q not found in base data table", row.CurrentFile, item.Name)
		}
		for k, v := range item.Fields {
			target[k] = v
		}
		doc[item.Name] = target
	}
	return json.Marshal(doc)
}
