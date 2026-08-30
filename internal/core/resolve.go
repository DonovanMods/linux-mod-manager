package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// ErrVersionNotFound reports that version->file resolution ran against a
// source that does carry per-file version info, but no file matched the
// requested version exactly. Callers branch with errors.Is; the message
// names the requested version and the distinct versions that ARE available.
var ErrVersionNotFound = errors.New("version not found")

// ResolveVersionFiles selects the files whose Version exactly matches
// version, from a source's raw (unfiltered) file list - archived/old/deleted
// files are eligible by design, since a version pin usually targets one
// (#96). Matches are returned category-sorted (MAIN first, mirroring
// FilterAndSortFiles' ordering) so callers can apply their own
// sub-selection (--file, the primary heuristic).
//
// Degradation is dynamic rather than capability-driven: a list in which no
// file carries a non-empty Version cannot resolve any version, and returns
// source.ErrNotSupported wrapped with the sourceID - the same contract as a
// source that lacks the operation entirely (#130's vacuous-version
// precedent: no version info means nothing to compare, not a mismatch).
func ResolveVersionFiles(sourceID string, files []domain.DownloadableFile, version string) ([]domain.DownloadableFile, error) {
	if !anyFileHasVersion(files) {
		return nil, fmt.Errorf("source %q: version resolution: %w", sourceID, source.ErrNotSupported)
	}
	var matches []domain.DownloadableFile
	for _, f := range files {
		if f.Version == version {
			matches = append(matches, f)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: version %q (available: %s)", ErrVersionNotFound, version, strings.Join(availableVersions(files), ", "))
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return installFileCategoryPriority(matches[i].Category) < installFileCategoryPriority(matches[j].Category)
	})
	return matches, nil
}

// availableVersions returns the distinct non-empty versions in files, in
// first-seen order - display material for ErrVersionNotFound.
func availableVersions(files []domain.DownloadableFile) []string {
	seen := make(map[string]bool, len(files))
	var out []string
	for _, f := range files {
		if f.Version == "" || seen[f.Version] {
			continue
		}
		seen[f.Version] = true
		out = append(out, f.Version)
	}
	return out
}

// anyFileHasVersion reports whether at least one file carries version info -
// the gate between version-aware and legacy (FileIDs-only) behavior.
func anyFileHasVersion(files []domain.DownloadableFile) bool {
	for _, f := range files {
		if f.Version != "" {
			return true
		}
	}
	return false
}
