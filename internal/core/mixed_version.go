package core

import (
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// MixedVersionFilesCode is the "code" a MixedVersionSelectionError's
// --json details carry (#647), for a script that branches on the refusal
// rather than on its sentence.
const MixedVersionFilesCode = "mixed_version_files"

// MixedVersionSelectionError refuses an install selection whose files
// belong to more than one version of the mod (#647). An installed mod
// records one version, and its cache entry is that version's tree: files of
// two versions would leave a tree the record does not describe, which
// update and rollback then reason about wrongly. Nothing is downloaded or
// written; the remedy is to pick the files of one version.
//
// A file whose source reports no version belongs to no version: it joins a
// selection of any one version and never makes a selection mixed, so it is
// never named here either.
type MixedVersionSelectionError struct {
	// Versions lists each version the selection spans, in the order the
	// selection first names it, with the selected files of that version.
	// Always two or more.
	Versions []MixedVersion
}

// MixedVersion is one version a refused selection spans, and its files.
type MixedVersion struct {
	Version string             `json:"version"`
	Files   []MixedVersionFile `json:"files"`
}

// MixedVersionFile is one selected file, by its source id and file name
// (the display name when the source reports no file name).
type MixedVersionFile struct {
	ID       string `json:"id"`
	FileName string `json:"file_name,omitempty"`
}

// Error names each version and its files: "these files belong to versions
// 1.0 (a.zip) and 2.0 (b.zip); pick one version".
func (e *MixedVersionSelectionError) Error() string {
	parts := make([]string, len(e.Versions))
	for i, v := range e.Versions {
		names := make([]string, len(v.Files))
		for j, f := range v.Files {
			names[j] = f.FileName
			if names[j] == "" {
				names[j] = f.ID
			}
		}
		parts[i] = fmt.Sprintf("%s (%s)", v.Version, strings.Join(names, ", "))
	}
	list := parts[0]
	if n := len(parts); n > 1 {
		list = strings.Join(parts[:n-1], ", ") + " and " + parts[n-1]
	}
	return fmt.Sprintf("these files belong to versions %s; pick one version", list)
}

// Details implements the --json error envelope's extension point: the
// refusal's code and every version with its files.
func (e *MixedVersionSelectionError) Details() any {
	return mixedVersionSelectionDetails{Code: MixedVersionFilesCode, Versions: e.Versions}
}

// mixedVersionSelectionDetails is MixedVersionSelectionError's wire shape.
type mixedVersionSelectionDetails struct {
	Code     string         `json:"code"`
	Versions []MixedVersion `json:"versions"`
}

// mixedVersionSelection returns a *MixedVersionSelectionError when files
// span more than one non-empty version, else nil. The grouping follows the
// files' own order, so the message names versions as the selection did.
func mixedVersionSelection(files []domain.DownloadableFile) error {
	var versions []MixedVersion
	index := make(map[string]int)
	for _, f := range files {
		if f.Version == "" {
			continue
		}
		name := f.FileName
		if name == "" {
			name = f.Name
		}
		i, ok := index[f.Version]
		if !ok {
			i = len(versions)
			index[f.Version] = i
			versions = append(versions, MixedVersion{Version: f.Version})
		}
		versions[i].Files = append(versions[i].Files, MixedVersionFile{ID: f.ID, FileName: name})
	}
	if len(versions) < 2 {
		return nil
	}
	return &MixedVersionSelectionError{Versions: versions}
}
