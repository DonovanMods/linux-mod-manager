package core

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ParsedFilename contains extracted info from a NexusMods-style filename
type ParsedFilename struct {
	ModID    string // NexusMods mod ID
	Version  string // Mod version (normalized)
	BaseName string // Mod name portion before the ID
}

// nexusPattern matches NexusMods filename format: Name-ModID-Version.ext
// Example: SkyUI-12604-5-2SE.zip -> groups: SkyUI, 12604, 5-2SE
// The mod ID is a multi-digit number (2+ digits to distinguish from version components)
// We use a non-greedy match for the name to capture up to the mod ID.
var nexusPattern = regexp.MustCompile(`^(.+?)-(\d{2,})-([^.]+)\.[a-zA-Z0-9]+$`)

// timestampSuffix matches trailing timestamps (10+ digits at end of version)
var timestampSuffix = regexp.MustCompile(`-\d{10,}$`)

// ParseNexusModsFilename attempts to extract mod ID and version from a
// NexusMods-style filename like "SkyUI-12604-5-2SE.zip".
// Returns nil if the filename doesn't match the expected pattern.
func ParseNexusModsFilename(filename string) *ParsedFilename {
	// Get just the filename without path
	filename = filepath.Base(filename)

	matches := nexusPattern.FindStringSubmatch(filename)
	if matches == nil {
		return nil
	}

	baseName := matches[1]
	modID := matches[2]
	version := matches[3]

	// Strip trailing timestamp from version (e.g., -1703618069)
	version = timestampSuffix.ReplaceAllString(version, "")

	// Normalize version: replace dashes with dots
	version = strings.ReplaceAll(version, "-", ".")

	return &ParsedFilename{
		ModID:    modID,
		Version:  version,
		BaseName: baseName,
	}
}

// DetectModName determines a display name for an imported mod. game
// answers modNameFromMembers' "is this a BepInEx game" question for the
// bare plugins/config/core-style loader-structure shapes (#450) - nil for a
// caller with no game in hand.
// It checks for a single top-level directory in the extracted content,
// falling back to the archive basename if not found.
//
// The rule itself lives in modNameFromMembers (#314): PlanImportArchive has
// to answer the same question from an archive LISTING, before anything is
// extracted, so this reads the tree and hands the entries to the one
// implementation both sides share.
func DetectModName(game *domain.Game, extractedPath, archiveFilename string) string {
	// If no extracted path provided, use archive basename
	if extractedPath == "" {
		return stripExtension(archiveFilename)
	}

	// The whole tree, as the listing would give it: the rule looks beneath
	// a loader's own top-level directory (#450), not only at the top.
	var members []archiveMember
	err := filepath.WalkDir(extractedPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == extractedPath {
			return nil
		}
		rel, err := filepath.Rel(extractedPath, path)
		if err != nil {
			return err
		}
		members = append(members, archiveMember{Path: rel, Dir: d.IsDir()})
		return nil
	})
	if err != nil {
		return stripExtension(archiveFilename)
	}
	return modNameFromMembers(game, members, archiveFilename)
}

// stripExtension removes the file extension from a filename
func stripExtension(filename string) string {
	filename = filepath.Base(filename)
	ext := filepath.Ext(filename)
	return strings.TrimSuffix(filename, ext)
}
