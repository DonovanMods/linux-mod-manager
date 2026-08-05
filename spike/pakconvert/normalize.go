package pakconvert

import (
	"fmt"
	"path"
	"strings"
)

// Duplicated from internal/source/icarus (unexported):
// icarusContentMountPoint (compile.go:128) and icarusDataTablePrefix
// (compile.go:138). The '-'<->'/' CurrentFile flattening rule is
// matchMountPath (compile.go:169).
const (
	contentMount = "../../../Icarus/Content/"
	dataPrefix   = "data/"
)

// EntryClass classifies one mod-pak entry for conversion.
type EntryClass int

const (
	ClassOther EntryClass = iota
	ClassTable
	ClassEmbeddedExmod
	ClassAsset
)

func (c EntryClass) String() string {
	switch c {
	case ClassTable:
		return "table"
	case ClassEmbeddedExmod:
		return "embedded-exmod"
	case ClassAsset:
		return "asset"
	default:
		return "other"
	}
}

// hasPrefixFold reports whether s starts with prefix, ASCII-case-insensitively
// (UE virtual paths are case-insensitive on disk — the game loads paks fine
// regardless of "Data/" vs "data/" — so a case-sensitive match here silently
// misclassifies real corpus paks into ClassOther with no Finding to explain
// why: see the ground-truth audit for Eye Colors Expanded!, whose mount
// contains a capital "Data/" segment).
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// NormalizeEntry joins a pak's mount point with one entry path and classifies
// the result. The data/ boundary floats between mount point and entry path in
// real mod paks (pak-divergence-report §1), so classification always works on
// the JOINED path. For ClassTable the returned string is the base-table
// mount-relative path (what unrealpak data.pak Files() report, and what
// CurrentFileFor flattens); for ClassAsset and ClassEmbeddedExmod it is the
// Content-relative remainder; for ClassOther it is "". Prefix matching against
// contentMount and dataPrefix is case-insensitive (real corpus paks vary the
// casing of the "Data/" mount segment), but the ORIGINAL case of the returned
// path is preserved — it must match the live base pak's actual entry casing
// for base.ReadFile lookups to succeed.
func NormalizeEntry(mountPoint, entryPath string) (EntryClass, string, error) {
	full := path.Join(mountPoint, entryPath) // Join cleans but keeps leading ../
	if !hasPrefixFold(full, contentMount) {
		return ClassOther, "", nil
	}
	rest := full[len(contentMount):] // slice, not TrimPrefix, to preserve rest's original case
	lower := strings.ToLower(rest)
	switch {
	case strings.HasSuffix(lower, ".exmod"):
		return ClassEmbeddedExmod, rest, nil
	case strings.HasSuffix(lower, ".uasset") || strings.HasSuffix(lower, ".uexp"):
		return ClassAsset, rest, nil
	case hasPrefixFold(rest, dataPrefix) && strings.HasSuffix(lower, ".json"):
		tablePath := rest[len(dataPrefix):] // slice, not TrimPrefix, to preserve tablePath's original case
		if strings.Contains(tablePath, "-") {
			// The CurrentFile encoding replaces ALL '/' with '-' and is only
			// reversible because no real base-table path contains a hyphen
			// (icarus compile.go:147). A hyphen here would produce an
			// unresolvable CurrentFile.
			return ClassTable, "", fmt.Errorf("table path %q contains a hyphen: CurrentFile flattening would be ambiguous", tablePath)
		}
		return ClassTable, tablePath, nil
	default:
		return ClassOther, "", nil
	}
}

// CurrentFileFor flattens a base-table mount-relative path into the .EXMOD
// CurrentFile encoding (forward direction of icarus matchMountPath).
func CurrentFileFor(tablePath string) string {
	return strings.ReplaceAll(tablePath, "/", "-")
}
