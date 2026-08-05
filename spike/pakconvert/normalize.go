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

// NormalizeEntry joins a pak's mount point with one entry path and classifies
// the result. The data/ boundary floats between mount point and entry path in
// real mod paks (pak-divergence-report §1), so classification always works on
// the JOINED path. For ClassTable the returned string is the base-table
// mount-relative path (what unrealpak data.pak Files() report, and what
// CurrentFileFor flattens); for ClassAsset and ClassEmbeddedExmod it is the
// Content-relative remainder; for ClassOther it is "".
func NormalizeEntry(mountPoint, entryPath string) (EntryClass, string, error) {
	full := path.Join(mountPoint, entryPath) // Join cleans but keeps leading ../
	if !strings.HasPrefix(full, contentMount) {
		return ClassOther, "", nil
	}
	rest := strings.TrimPrefix(full, contentMount)
	lower := strings.ToLower(rest)
	switch {
	case strings.HasSuffix(lower, ".exmod"):
		return ClassEmbeddedExmod, rest, nil
	case strings.HasSuffix(lower, ".uasset") || strings.HasSuffix(lower, ".uexp"):
		return ClassAsset, rest, nil
	case strings.HasPrefix(rest, dataPrefix) && strings.HasSuffix(lower, ".json"):
		tablePath := strings.TrimPrefix(rest, dataPrefix)
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
