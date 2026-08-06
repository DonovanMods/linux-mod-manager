package icarus

import (
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"
)

// entryClass classifies one mod-pak entry for pak→exmod conversion (#221).
type entryClass int

const (
	classOther entryClass = iota
	classTable
	classEmbeddedExmod
	classAsset
)

func (c entryClass) String() string {
	switch c {
	case classTable:
		return "table"
	case classEmbeddedExmod:
		return "embedded-exmod"
	case classAsset:
		return "asset"
	default:
		return "other"
	}
}

// hasPrefixFold reports whether s starts with prefix, ASCII-case-insensitively.
// UE virtual paths are case-insensitive (the game loads paks regardless of
// "Data/" vs "data/"), so a case-sensitive match silently misclassifies real
// mod paks - the spike's ground-truth audit caught a capital "Data/" mount
// segment doing exactly that (spike findings doc, constraint 4).
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// normalizeEntry joins a mod pak's mount point with one entry path and
// classifies the result. The data/ boundary floats between mount point and
// entry path in real mod paks, so classification always works on the JOINED
// path. For classTable the returned string is the base-table mount-relative
// path (what data.pak's Files() report, and what currentFileFor flattens);
// for classAsset/classEmbeddedExmod it is the Content-relative remainder;
// for classOther it is "". Prefix matching is case-insensitive, but the
// ORIGINAL case of the returned path is preserved - it must match the live
// base pak's actual entry casing for base.ReadFile lookups.
func normalizeEntry(mountPoint, entryPath string) (entryClass, string, error) {
	full := path.Join(mountPoint, entryPath) // Join cleans but keeps leading ../
	if !hasPrefixFold(full, icarusContentMountPoint) {
		return classOther, "", nil
	}
	rest := full[len(icarusContentMountPoint):] // slice, not TrimPrefix: preserve original case
	lower := strings.ToLower(rest)
	switch {
	case strings.HasSuffix(lower, ".exmod"):
		return classEmbeddedExmod, rest, nil
	case strings.HasSuffix(lower, ".uasset") || strings.HasSuffix(lower, ".uexp"):
		return classAsset, rest, nil
	case hasPrefixFold(rest, icarusDataTablePrefix) && strings.HasSuffix(lower, ".json"):
		tablePath := rest[len(icarusDataTablePrefix):] // slice: preserve original case
		if strings.Contains(tablePath, "-") {
			// The CurrentFile encoding replaces ALL '/' with '-' and is only
			// reversible because no real base-table path contains a hyphen
			// (matchMountPath, compile.go). A hyphen here would produce an
			// unresolvable CurrentFile.
			return classTable, "", fmt.Errorf("icarus: table path %q contains a hyphen: CurrentFile flattening would be ambiguous", tablePath)
		}
		return classTable, tablePath, nil
	default:
		return classOther, "", nil
	}
}

// currentFileFor flattens a base-table mount-relative path into the .EXMOD
// CurrentFile encoding (forward direction of matchMountPath's reversal).
func currentFileFor(tablePath string) string {
	return strings.ReplaceAll(tablePath, "/", "-")
}

// pakDataTable is the UE DataTable export shape {"RowStruct","Defaults","Rows"}.
type pakDataTable struct {
	rows  []map[string]any
	other map[string]any // every top-level key except Rows
}

func parsePakDataTable(data []byte) (*pakDataTable, error) {
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parsing data table: %w", err)
	}
	rawRows, ok := top["Rows"].([]any)
	if !ok {
		return nil, fmt.Errorf("data table has no Rows array")
	}
	t := &pakDataTable{other: top}
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

// diffTable derives the EXMOD-expressible difference between the CURRENT base
// table and a mod pak's (possibly stale) full-table snapshot - the Tier 2
// rebase (#221 design §1):
//
//   - row in pak, not in base      -> new row: emit all fields
//   - row in both, fields differ   -> emit Name + changed fields (whole-field;
//     this is the accepted rebase semantic - drift in author-touched rows
//     rides along BY DESIGN, user ruling 2026-08-05)
//   - row in base, not in pak      -> staleness: ignored (EXMOD has no delete)
//   - RowStruct mismatch           -> hard error (irreconcilable: the table's
//     schema changed under the pak; a field-level rebase is meaningless)
//   - Defaults/top-level changes, pak row missing a base field, duplicate
//     Names (first wins)           -> warnings; conversion proceeds
func diffTable(tableRef string, baseJSON, modJSON []byte) (items []ExmodFileItem, warnings []string, err error) {
	base, err := parsePakDataTable(baseJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("icarus: %s: base: %w", tableRef, err)
	}
	mod, err := parsePakDataTable(modJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("icarus: %s: pak table: %w", tableRef, err)
	}

	if !reflect.DeepEqual(base.other["RowStruct"], mod.other["RowStruct"]) {
		return nil, nil, fmt.Errorf("icarus: %s: RowStruct differs from current base (pak schema is irreconcilable)", tableRef)
	}

	// Non-Rows top-level keys: union of both sides, sorted for deterministic
	// warning order.
	otherKeys := make(map[string]bool)
	for key := range base.other {
		if key != "RowStruct" {
			otherKeys[key] = true
		}
	}
	for key := range mod.other {
		if key != "RowStruct" {
			otherKeys[key] = true
		}
	}
	sortedKeys := make([]string, 0, len(otherKeys))
	for key := range otherKeys {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)
	for _, key := range sortedKeys {
		if !reflect.DeepEqual(base.other[key], mod.other[key]) {
			warnings = append(warnings, fmt.Sprintf("%s: top-level key %q differs from base - inexpressible in exmod row semantics, ignored", tableRef, key))
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
			warnings = append(warnings, fmt.Sprintf("%s: duplicate row %q in pak table; first occurrence wins", tableRef, name))
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
			items = append(items, ExmodFileItem{Name: name, Fields: fields})
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
		// Collect removed fields, sort for deterministic warning order.
		removedFields := make([]string, 0)
		for k := range br {
			if k == "Name" {
				continue
			}
			if _, ok := mr[k]; !ok {
				removedFields = append(removedFields, k)
			}
		}
		sort.Strings(removedFields)
		for _, k := range removedFields {
			warnings = append(warnings, fmt.Sprintf("%s: row %q: field %q present in base but absent in pak (EXMOD cannot remove fields; base value kept)", tableRef, name, k))
		}
		if len(changed) > 0 {
			items = append(items, ExmodFileItem{Name: name, Fields: changed})
		}
	}
	// Base-only rows are staleness (the pak predates them) - deliberately
	// ignored, never deletions.
	return items, warnings, nil
}
