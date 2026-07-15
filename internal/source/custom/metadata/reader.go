// Package metadata extracts mod metadata from well-known files inside a mod
// directory (e.g. 7 Days to Die's ModInfo.xml). Adding a format means adding
// one Reader implementation and listing it in readers.
package metadata

// Info is metadata extracted from a well-known mod metadata file. Zero-value
// fields mean the file did not provide them.
type Info struct {
	Name        string
	DisplayName string
	Version     string
	Summary     string
	Author      string
}

// Reader parses one well-known metadata file format.
type Reader interface {
	// Detect returns the metadata file path within modDir, or "" if absent.
	Detect(modDir string) string
	// Read parses the metadata file at path.
	Read(path string) (*Info, error)
}

// readers lists all known formats in priority order.
var readers = []Reader{ModInfoXML{}}

// Resolve extracts metadata from modDir using the first matching reader.
// Returns nil when no known metadata file exists or it cannot be parsed;
// callers fall back to name-based detection.
func Resolve(modDir string) *Info {
	for _, r := range readers {
		path := r.Detect(modDir)
		if path == "" {
			continue
		}
		info, err := r.Read(path)
		if err != nil {
			continue // malformed metadata falls back, it doesn't fail the scan
		}
		return info
	}
	return nil
}
