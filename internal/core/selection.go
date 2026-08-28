package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// ErrNoDownloadableFiles is FilterAndSortFiles/PrimaryFile/
// SelectFilesForVersion's sentinel for an empty file list - the mod's
// source offers nothing to install/deploy. This text is part of the CLI
// contract: cmd/lmm/profile.go's errNoDownloadableFiles and this package's
// former errNoDeployFiles were hand-kept byte-identical before the two
// copies were unified into this one sentinel.
var ErrNoDownloadableFiles = errors.New("no downloadable files")

// ErrStoredFilesUnavailable is SelectFilesForVersion's sentinel for a
// would-be primary-file fallback that isn't allowed (#95): the mod's stored
// file IDs no longer match anything the source currently offers, and
// silently substituting the primary file would deploy/install/switch-in a
// file the caller never asked for. Always wrapped with the missing IDs and
// a remediation hint; see selectDeployFiles and SelectFilesForVersion.
var ErrStoredFilesUnavailable = errors.New("stored file(s) no longer available upstream")

// FilterAndSortFiles is the file-selection policy's filter/sort step,
// shared by the CLI's doInstall default and PlanInstall's non-interactive
// default alike: unless showArchived, drops any file whose Category
// (case-insensitive) is ARCHIVED, OLD_VERSION, or DELETED, then
// stable-sorts the remainder by category priority - MAIN, then OPTIONAL,
// then UPDATE, then MISCELLANEOUS, then anything else (archived categories
// sort last, but they're already gone unless showArchived kept them).
func FilterAndSortFiles(files []domain.DownloadableFile, showArchived bool) []domain.DownloadableFile {
	var filtered []domain.DownloadableFile
	for _, f := range files {
		category := strings.ToUpper(f.Category)
		if !showArchived && (category == "ARCHIVED" || category == "OLD_VERSION" || category == "DELETED") {
			continue
		}
		filtered = append(filtered, f)
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		return installFileCategoryPriority(filtered[i].Category) < installFileCategoryPriority(filtered[j].Category)
	})

	return filtered
}

// installFileCategoryPriority is FilterAndSortFiles' and pickVersionMatch's
// sort key (lower sorts first).
func installFileCategoryPriority(category string) int {
	switch strings.ToUpper(category) {
	case "MAIN":
		return 0
	case "OPTIONAL":
		return 1
	case "UPDATE":
		return 2
	case "MISCELLANEOUS":
		return 3
	case "ARCHIVED", "OLD_VERSION", "DELETED":
		return 99
	default:
		return 50
	}
}

// PrimaryFile returns the primary file from files - the first with
// IsPrimary set, else the first file - or nil for an empty slice.
func PrimaryFile(files []domain.DownloadableFile) *domain.DownloadableFile {
	if len(files) == 0 {
		return nil
	}
	for i := range files {
		if files[i].IsPrimary {
			return &files[i]
		}
	}
	return &files[0]
}

// SelectFilesForVersion picks the file(s) to (re)download for a mod pinned
// to version (#96), with the recorded version made authoritative. version
// == "" (legacy refs) and version-less file lists (the #130 vacuous rule)
// fall through to the pre-#96, version-blind behavior unchanged: stored IDs
// found upstream win, and stored IDs found nowhere hard-fail via
// ErrStoredFilesUnavailable rather than silently falling back to the
// primary file - substituting a file the caller never asked for is exactly
// the silent-fallback bug #95 tracks. No stored IDs at all falls back to
// the primary file.
//
// Otherwise: stored IDs win only while their effective version agrees with
// the record; drift and gone-IDs heal by exact-match resolution to the SAME
// version (never latest - #95's rule extended); unresolvable targets are
// hard per-mod errors naming the version - the "gone upstream" #95 wording
// only when the stored IDs themselves match nothing at all upstream, versus
// a distinct ErrVersionNotFound wrap when at least one stored ID IS still
// present upstream but the recorded version isn't (the classic pre-#94
// mis-stamped row, which isn't a "gone" file - it's a wrong version record
// on a file that's still there).
func SelectFilesForVersion(files []domain.DownloadableFile, storedFileIDs []string, version string) ([]*domain.DownloadableFile, error) {
	if version == "" || !anyFileHasVersion(files) {
		selected, _, err := selectDeployFiles(files, storedFileIDs, false)
		return selected, err
	}
	var idSet map[string]bool
	if len(storedFileIDs) > 0 {
		idSet = make(map[string]bool, len(storedFileIDs))
		for _, id := range storedFileIDs {
			idSet[id] = true
		}
	}
	var found []*domain.DownloadableFile
	for i := range files {
		if idSet[files[i].ID] {
			found = append(found, &files[i])
		}
	}
	if len(found) > 0 && domain.EffectiveInstalledVersion(version, found) == version {
		return found, nil
	}
	var matches []*domain.DownloadableFile
	for i := range files {
		if files[i].Version == version {
			matches = append(matches, &files[i])
		}
	}
	if len(matches) == 0 {
		if len(storedFileIDs) > 0 {
			if len(found) > 0 {
				// At least one stored ID is still present upstream - the
				// files aren't gone, only the recorded version doesn't
				// match anything. Distinct from the #95 "gone" wording
				// below: this is a version-record problem, not a
				// missing-file problem, so it points at verify/update
				// instead of reinstall.
				return nil, fmt.Errorf("%w: installed file(s) (ID(s): %s) do not match recorded version %q, which is not available upstream - run 'lmm verify --fix' to correct the version record, or 'lmm update' to adopt the current version", ErrVersionNotFound, strings.Join(storedFileIDs, ", "), version)
			}
			return nil, fmt.Errorf("%w (file ID(s): %s; version %q not available) - reinstall the mod or run 'lmm update' to adopt the current version", ErrStoredFilesUnavailable, strings.Join(storedFileIDs, ", "), version)
		}
		return nil, fmt.Errorf("%w: version %q is not available upstream (available: %s) - edit the profile's version or reinstall", ErrVersionNotFound, version, strings.Join(availableVersions(files), ", "))
	}
	return pickVersionMatch(matches, idSet), nil
}

// pickVersionMatch narrows matches - every file carrying the version being
// resolved - down to the one(s) to actually use: the stored-ID subset when
// any stored ID is among them (so a mod installed from an OPTIONAL/extra
// file of that version keeps that file rather than being silently moved to
// the main one), else the version's primary file, else its best file by
// installFileCategoryPriority (#144 item 5 - a version whose files are e.g.
// {MISCELLANEOUS, OPTIONAL} with no primary picks the OPTIONAL, not
// whichever the source happened to list first; ties keep first-listed, so
// category-less listings behave exactly as before). matches must be
// non-empty. idSet may be nil (reads as all-false), which simply skips the
// stored-ID preference.
//
// Shared by SelectFilesForVersion (#96) and selectUpdateDeployFiles (#143)
// specifically so the two cannot drift: both answer the same question -
// "given the files for THIS version, which ones does this mod use" - and
// only differ in how they decide the version and what to do when it isn't
// offered at all.
func pickVersionMatch(matches []*domain.DownloadableFile, idSet map[string]bool) []*domain.DownloadableFile {
	var stored []*domain.DownloadableFile
	for _, m := range matches {
		if idSet[m.ID] {
			stored = append(stored, m)
		}
	}
	if len(stored) > 0 {
		return stored
	}
	for _, m := range matches {
		if m.IsPrimary {
			return []*domain.DownloadableFile{m}
		}
	}
	best := 0
	for i := 1; i < len(matches); i++ {
		if installFileCategoryPriority(matches[i].Category) < installFileCategoryPriority(matches[best].Category) {
			best = i
		}
	}
	return []*domain.DownloadableFile{matches[best]}
}

// selectDeployFiles picks the file(s) to (re)download for a cache-miss mod:
// the files matching storedFileIDs if any are found, else - only when
// allowFallback is true - the primary file, reporting whether it had to
// fall back.
//
// allowFallback is a per-call-site policy (#95): the update path (ApplyUpdate)
// passes true because falling back to the NEW version's primary file is
// correct semantics there - a source that prunes old file IDs after a
// version bump (CurseForge routinely does) should resolve to the current
// primary file, not an error. Every other caller (deploy, switch, import,
// SelectFilesForVersion, and the nil-storedFileIDs install paths, where the
// branch is unreachable) passes false: silently deploying/installing a file
// the caller never asked for is exactly the silent-fallback bug #95 tracks.
// With allowFallback false, a would-be fallback instead returns
// ErrStoredFilesUnavailable, wrapped with the missing IDs and a remediation
// hint.
func selectDeployFiles(files []domain.DownloadableFile, storedFileIDs []string, allowFallback bool) ([]*domain.DownloadableFile, bool, error) {
	if len(files) == 0 {
		return nil, false, ErrNoDownloadableFiles
	}
	if len(storedFileIDs) > 0 {
		idSet := make(map[string]bool, len(storedFileIDs))
		for _, id := range storedFileIDs {
			idSet[id] = true
		}
		var found []*domain.DownloadableFile
		for i := range files {
			if idSet[files[i].ID] {
				found = append(found, &files[i])
			}
		}
		if len(found) > 0 {
			return found, false, nil
		}
		if !allowFallback {
			return nil, false, fmt.Errorf("%w (file ID(s): %s) - reinstall the mod or run 'lmm update' to adopt the current version", ErrStoredFilesUnavailable, strings.Join(storedFileIDs, ", "))
		}
		return []*domain.DownloadableFile{PrimaryFile(files)}, true, nil
	}
	return []*domain.DownloadableFile{PrimaryFile(files)}, false, nil
}
