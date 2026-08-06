package icarus

import (
	"fmt"
	"path"
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
