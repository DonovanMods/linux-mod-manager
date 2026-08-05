package pakconvert

import (
	"fmt"
	"path"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// Report is ConvertPak's full account of what it saw and did — the raw
// material for the spike findings doc.
type Report struct {
	PakPath        string
	MountPoint     string
	Census         map[string]int        // EntryClass.String() -> count
	Tables         map[string]*TableDiff // tablePath -> diff vs live base
	EmbeddedExmods map[string][]byte     // Content-relative path -> raw bytes
	Findings       []Finding
	StaleRows      int
}

// sanitizeAssetPath reimplements (minimally) the discipline of icarus's
// unexported sanitizeAssetPath (compile.go:196): pak entry names are
// untrusted input. Backslashes normalize to '/', then NUL, absolute paths
// (POSIX and drive-letter), and any path escaping its root are rejected.
func sanitizeAssetPath(raw string) (string, error) {
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("asset path contains NUL byte")
	}
	p := strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("asset path %q is absolute", raw)
	}
	if len(p) >= 2 && p[1] == ':' {
		return "", fmt.Errorf("asset path %q is drive-absolute", raw)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("asset path %q escapes its root", raw)
	}
	return clean, nil
}

// ConvertPak converts one prebuilt mod pak into a synthesized .exmodz by
// diffing its full-table snapshots against the LIVE base data.pak (never
// adopting the pak's tables wholesale — they are stale snapshots, see the
// design doc's hazards). Assets pass through; embedded .EXMODs are captured
// for ground-truth comparison, not used for conversion.
func ConvertPak(pakPath, basePakPath string, meta Meta) ([]byte, *Report, error) {
	mod, err := unrealpak.Open(pakPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening mod pak: %w", err)
	}
	defer mod.Close()
	base, err := unrealpak.Open(basePakPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening base pak: %w", err)
	}
	defer base.Close()

	report := &Report{
		PakPath:        pakPath,
		MountPoint:     mod.MountPoint(),
		Census:         map[string]int{},
		Tables:         map[string]*TableDiff{},
		EmbeddedExmods: map[string][]byte{},
	}
	assets := map[string][]byte{}
	var tables []TableEntry

	for _, entry := range mod.Files() {
		class, rel, nerr := NormalizeEntry(mod.MountPoint(), entry.Path)
		report.Census[class.String()]++
		if nerr != nil {
			report.Findings = append(report.Findings, Finding{Kind: "hyphen-path",
				Table: entry.Path, Detail: nerr.Error()})
			continue
		}
		if class == ClassOther {
			continue
		}
		data, rerr := mod.ReadFile(entry.Path)
		if rerr != nil {
			// Oodle or other unsupported compression: record, keep going.
			report.Findings = append(report.Findings, Finding{Kind: "unreadable-entry",
				Table: entry.Path, Detail: rerr.Error()})
			continue
		}
		switch class {
		case ClassEmbeddedExmod:
			report.EmbeddedExmods[rel] = data
		case ClassAsset:
			safe, serr := sanitizeAssetPath(rel)
			if serr != nil {
				report.Findings = append(report.Findings, Finding{Kind: "unsafe-asset-path",
					Table: entry.Path, Detail: serr.Error()})
				continue
			}
			assets[safe] = data
		case ClassTable:
			baseData, berr := base.ReadFile(rel)
			if berr != nil {
				report.Findings = append(report.Findings, Finding{Kind: "table-not-in-base",
					Table: rel, Detail: berr.Error()})
				continue
			}
			td, derr := DiffTable(baseData, data)
			if derr != nil {
				return nil, nil, fmt.Errorf("diffing %s: %w", rel, derr)
			}
			for i := range td.Findings {
				td.Findings[i].Table = rel
			}
			report.Findings = append(report.Findings, td.Findings...)
			report.StaleRows += td.StaleBaseOnlyRows
			report.Tables[rel] = td
			if len(td.Items) > 0 {
				tables = append(tables, TableEntry{CurrentFile: CurrentFileFor(rel), Items: td.Items})
			}
		}
	}

	exmod, err := WriteExmod(meta, tables)
	if err != nil {
		return nil, nil, fmt.Errorf("emitting .EXMOD: %w", err)
	}
	exmodName := strings.ReplaceAll(meta.Name, "/", "_")
	if exmodName == "" {
		exmodName = "converted"
	}
	exmodz, err := WriteExmodz(exmodName, exmod, assets)
	if err != nil {
		return nil, nil, fmt.Errorf("emitting .exmodz: %w", err)
	}
	return exmodz, report, nil
}
