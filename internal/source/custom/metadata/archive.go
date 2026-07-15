package metadata

import (
	"archive/zip"
	"io"
	"strings"
)

// modInfoFileName is the exact (case-sensitive) name Detect and
// ResolveArchive look for; case-insensitive matching is tracked separately
// (see issue #52).
const modInfoFileName = "ModInfo.xml"

// maxModInfoSize is the maximum allowed size (in bytes) for a ModInfo.xml entry
// when extracted from an archive. Real ModInfo.xml files are typically a few KB;
// this limit prevents decompression bomb attacks where a small compressed entry
// expands to gigabytes during extraction.
const maxModInfoSize = 1 << 20 // 1 MiB

// ResolveArchive extracts metadata from a .zip or .jar archive whose
// ModInfo.xml lives at the archive root or exactly one directory deep - the
// common "wrapper folder" layout used by 7 Days to Die mods (e.g. a
// donovan-aio.zip containing donovan-aio/ModInfo.xml). Returns nil when the
// archive can't be opened, has no ModInfo.xml, or the metadata is malformed;
// callers fall back to filename-based detection, mirroring Resolve.
func ResolveArchive(archivePath string) *Info {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil
	}
	defer r.Close()

	target := findModInfoEntry(r.File)
	if target == nil {
		return nil
	}

	rc, err := target.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, maxModInfoSize+1))
	if err != nil {
		return nil
	}

	// Reject entries larger than the cap to prevent decompression bombs
	if len(data) > maxModInfoSize {
		return nil
	}

	info, err := parseModInfo(data)
	if err != nil {
		return nil // malformed metadata falls back, it doesn't fail the scan
	}
	return info
}

// findModInfoEntry locates ModInfo.xml at the archive root or exactly one
// directory deep. A root-level match always wins over a nested one;
// otherwise the first one-deep match in archive order is used.
func findModInfoEntry(files []*zip.File) *zip.File {
	var nested *zip.File
	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}
		parts := strings.Split(f.Name, "/")
		switch len(parts) {
		case 1:
			if parts[0] == modInfoFileName {
				return f
			}
		case 2:
			if nested == nil && parts[1] == modInfoFileName {
				nested = f
			}
		}
	}
	return nested
}
