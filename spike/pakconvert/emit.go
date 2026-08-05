package pakconvert

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// endOfModSentinel is duplicated from internal/source/icarus (unexported
// const, compile.go:146). A terminal row {"CurrentFile":"EndOfMod"} with no
// File_Items key matches what real author-built .EXMODs ship.
const endOfModSentinel = "EndOfMod"

// Meta is the synthesized .EXMOD's metadata block.
type Meta struct {
	Name        string
	Author      string
	Version     string
	Description string
}

// TableEntry is one table's derived upserts, keyed by its flattened
// CurrentFile encoding (see CurrentFileFor).
type TableEntry struct {
	CurrentFile string
	Items       []Item
}

// exmodDoc mirrors ParseExmod's anonymous wire struct
// (internal/source/icarus/exmod.go:35): lowercase metadata keys, capitalized
// structural keys. icarus.ExmodDiff itself is tag-free and CANNOT be used to
// emit — json.Marshal on it produces the wrong keys.
type exmodDoc struct {
	Name        string           `json:"name"`
	Author      string           `json:"author"`
	Version     string           `json:"version"`
	Description string           `json:"description"`
	Rows        []map[string]any `json:"Rows"`
}

// WriteExmod emits a .EXMOD JSON document readable by icarus.ParseExmod.
// Tables with zero items are skipped (a non-sentinel row with empty
// File_Items is a merge-time hard error upstream).
func WriteExmod(meta Meta, tables []TableEntry) ([]byte, error) {
	doc := exmodDoc{Name: meta.Name, Author: meta.Author, Version: meta.Version, Description: meta.Description}
	for _, te := range tables {
		if len(te.Items) == 0 {
			continue
		}
		items := make([]map[string]any, 0, len(te.Items))
		for _, it := range te.Items {
			if it.Name == "" {
				return nil, fmt.Errorf("table %s: item with empty Name", te.CurrentFile)
			}
			flat := make(map[string]any, len(it.Fields)+1)
			flat["Name"] = it.Name
			for k, v := range it.Fields {
				if k == "Name" {
					continue // Name is positional, never a payload field
				}
				flat[k] = v
			}
			items = append(items, flat)
		}
		doc.Rows = append(doc.Rows, map[string]any{
			"CurrentFile": te.CurrentFile,
			"File_Items":  items,
		})
	}
	// Terminal sentinel row, File_Items key ABSENT (matches real manifests).
	doc.Rows = append(doc.Rows, map[string]any{"CurrentFile": endOfModSentinel})
	return json.MarshalIndent(doc, "", "  ")
}

// WriteExmodz zips a synthesized .exmodz: the manifest at
// "Extracted Mods/<exmodName>.EXMOD" plus assets under "<exmodName>/<path>",
// mirroring the real bundle layout (icarus exmodz_test.go fixture).
func WriteExmodz(exmodName string, exmod []byte, assets map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.Create("Extracted Mods/" + exmodName + ".EXMOD")
	if err != nil {
		return nil, fmt.Errorf("creating manifest entry: %w", err)
	}
	if _, err := w.Write(exmod); err != nil {
		return nil, fmt.Errorf("writing manifest entry: %w", err)
	}

	paths := make([]string, 0, len(assets))
	for p := range assets {
		paths = append(paths, p)
	}
	sort.Strings(paths) // deterministic zip layout
	for _, p := range paths {
		w, err := zw.Create(exmodName + "/" + p)
		if err != nil {
			return nil, fmt.Errorf("creating asset entry %s: %w", p, err)
		}
		if _, err := w.Write(assets[p]); err != nil {
			return nil, fmt.Errorf("writing asset entry %s: %w", p, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finalizing exmodz zip: %w", err)
	}
	return buf.Bytes(), nil
}
