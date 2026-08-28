package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SourceLocal is the source ID for mods imported from local files
const SourceLocal = "local"

// SourceMerged is the source ID for the synthetic, profile-scoped "mod"
// that tracks a game's merged compiled pak (#197 - Icarus's cross-mod
// table merge). Follows the SourceLocal precedent: a reserved sentinel
// string, not a real ModSource registration.
const SourceMerged = "lmm-merged"

// UpdatePolicy determines how a mod handles updates
type UpdatePolicy int

const (
	UpdateNotify UpdatePolicy = iota // Default: show available, require approval
	UpdateAuto                       // Automatically apply updates
	UpdatePinned                     // Never update
)

func (p UpdatePolicy) String() string {
	switch p {
	case UpdateAuto:
		return "auto"
	case UpdatePinned:
		return "pinned"
	default:
		return "notify"
	}
}

// MarshalText implements encoding.TextMarshaler.
func (p UpdatePolicy) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (p *UpdatePolicy) UnmarshalText(b []byte) error {
	switch string(b) {
	case "notify":
		*p = UpdateNotify
	case "auto":
		*p = UpdateAuto
	case "pinned":
		*p = UpdatePinned
	default:
		return fmt.Errorf("unknown update policy %q", b)
	}
	return nil
}

// ModFile represents a single file within a mod archive (after extraction)
type ModFile struct {
	Path     string `json:"path"` // Relative path within mod archive
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"` // SHA256
}

// DownloadableFile represents a file available for download from a mod source
type DownloadableFile struct {
	ID          string `json:"id"`                    // Source-specific file ID
	Name        string `json:"name"`                  // Display name
	FileName    string `json:"file_name"`             // Actual filename (e.g., "mod-1.0.zip")
	Version     string `json:"version"`               // File version
	Size        int64  `json:"size"`                  // Size in bytes
	IsPrimary   bool   `json:"is_primary"`            // Whether this is the primary/main file
	Category    string `json:"category,omitempty"`    // Category: "MAIN", "OPTIONAL", "UPDATE", etc.
	Description string `json:"description,omitempty"` // File description
	SHA256      string `json:"sha256,omitempty"`      // Expected SHA-256 of the download (hex); empty = source declares no checksum
}

// EffectiveInstalledVersion resolves the version string that describes what
// the selected files actually are: the primary selected file's Version when
// it carries one, else the first selected file with a non-empty Version,
// else modVersion (the mod-level version). Install-recording flows stamp
// this onto the mod before downloading so the DB row, the profile
// ModReference, and the cache directory key all describe the bytes on disk
// (issue #94) instead of the mod-level latest.
func EffectiveInstalledVersion(modVersion string, selected []*DownloadableFile) string {
	for _, f := range selected {
		if f != nil && f.IsPrimary && f.Version != "" {
			return f.Version
		}
	}
	for _, f := range selected {
		if f != nil && f.Version != "" {
			return f.Version
		}
	}
	return modVersion
}

// ModReference is a pointer to a mod (used in profiles, dependencies)
type ModReference struct {
	SourceID string   `yaml:"source_id" json:"source_id"`                   // "nexusmods", "curseforge", etc.
	ModID    string   `yaml:"mod_id" json:"mod_id"`                         // Source-specific identifier
	Version  string   `yaml:"version" json:"version,omitempty"`             // The installed-version record (#94/#96): always stamped by installs, moved by updates, converged to by deploy. When Locked, also the lock's target.
	FileIDs  []string `yaml:"file_ids,omitempty" json:"file_ids,omitempty"` // Source-specific file IDs that were installed
	Locked   bool     `yaml:"locked,omitempty" json:"locked,omitempty"`     // #97 lock marker: lmm update refuses this mod; Version is the lock's target. Set/cleared only by lock/unlock; survives UpsertMod (in-place update) and export/import.
}

// Mod represents a mod from any source
type Mod struct {
	ID           string         `json:"id"`
	SourceID     string         `json:"source_id"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Author       string         `json:"author"`
	Summary      string         `json:"summary"`
	Description  string         `json:"description"`
	GameID       string         `json:"game_id"`
	Category     string         `json:"category"`
	Downloads    int64          `json:"downloads"`
	Endorsements *int64         `json:"endorsements,omitempty"`
	PictureURL   string         `json:"picture_url"` // Main image URL (e.g. NexusMods picture_url)
	SourceURL    string         `json:"source_url"`  // Web page URL (e.g. CurseForge mod page)
	Files        []ModFile      `json:"files"`
	Dependencies []ModReference `json:"dependencies"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// InstalledMod tracks a mod installed in a profile
type InstalledMod struct {
	Mod
	ProfileName     string       `json:"profile_name"`
	UpdatePolicy    UpdatePolicy `json:"update_policy"`
	InstalledAt     time.Time    `json:"installed_at"`
	Enabled         bool         `json:"enabled"`                     // User intent: wants this mod active
	Deployed        bool         `json:"deployed"`                    // Current state: files are in game directory
	PreviousVersion string       `json:"previous_version,omitempty"`  // Version before last update (for rollback)
	PreviousFileIDs []string     `json:"previous_file_ids,omitempty"` // File IDs before last update (for rollback)
	LinkMethod      LinkMethod   `json:"link_method"`                 // How the mod was deployed (symlink, hardlink, copy)
	FileIDs         []string     `json:"file_ids,omitempty"`          // Source-specific file IDs that were downloaded
	ManualDownload  bool         `json:"manual_download"`             // True if mod requires manual download (CurseForge restricted, etc.)
	ConvertPaks     bool         `json:"convert_paks"`                // #221: pak-to-exmod conversion enabled (default true; only meaningful for DeployCompile games)
}

// Update represents an available update for an installed mod
type Update struct {
	InstalledMod       InstalledMod      `json:"installed_mod"`
	NewVersion         string            `json:"new_version"`
	Changelog          string            `json:"changelog,omitempty"`
	FileIDReplacements map[string]string `json:"file_id_replacements,omitempty"` // Old file ID -> new file ID when a file was superseded (e.g. NexusMods FileUpdates)
	// RecompileNeeded marks a DeployCompile mod whose deployed compile no
	// longer matches the game's live base data.pak (#196, "the Friday
	// problem": a weekly base-pak refresh silently reverts the tables a
	// compiled mod patches, with nothing before #196 to notice). NewVersion
	// equals InstalledMod.Version in this case - the mod itself hasn't
	// changed, only the base pak has - so callers must not treat NewVersion
	// as a real version bump when this is set.
	RecompileNeeded bool `json:"recompile_needed"`
	// RecompileReason qualifies RecompileNeeded with why a recompile/resync
	// is needed (#197 postsmoke UX fix): "base pak updated" when the
	// merged/compiled fingerprint no longer matches current inputs, "not
	// deployed" when the fingerprint still matches but the artifact is
	// missing from the game directory (#197 I5's wedge case). Empty when
	// RecompileNeeded is false.
	RecompileReason string `json:"recompile_reason,omitempty"`
}

// ModKey returns a unique lookup key for a mod: "sourceID:modID".
// Use this instead of ad-hoc string concatenation throughout the codebase.
func ModKey(sourceID, modID string) string {
	return sourceID + ":" + modID
}

// CompareVersions compares two version strings.
// Returns: -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2.
// Handles common prefixes (v/V) and pre-release suffixes (e.g. "1.0.0-beta").
func CompareVersions(v1, v2 string) int {
	parts1 := parseVersionParts(v1)
	parts2 := parseVersionParts(v2)

	maxLen := max(len(parts1), len(parts2))

	for i := range maxLen {
		var p1, p2 int
		if i < len(parts1) {
			p1 = parts1[i]
		}
		if i < len(parts2) {
			p2 = parts2[i]
		}
		if p1 < p2 {
			return -1
		}
		if p1 > p2 {
			return 1
		}
	}

	return 0
}

// IsNewerVersion returns true if newVersion is newer than currentVersion.
func IsNewerVersion(currentVersion, newVersion string) bool {
	return CompareVersions(currentVersion, newVersion) < 0
}

// parseVersionParts splits a version string into numeric parts.
// Strips v/V prefixes and extracts the leading numeric portion of each dotted segment.
func parseVersionParts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")

	parts := strings.Split(v, ".")
	result := make([]int, 0, len(parts))

	for _, part := range parts {
		// Find the end of the leading digit run
		end := 0
		for end < len(part) && part[end] >= '0' && part[end] <= '9' {
			end++
		}

		if end == 0 {
			result = append(result, 0)
		} else {
			n, _ := strconv.Atoi(part[:end])
			result = append(result, n)
		}
	}

	return result
}

// versionPattern matches version-like strings such as 1.2.3, v1.2.3, or
// 1.0.0-beta2. The optional suffix must start with a letter so compound
// strings like "1.20.1-15.3.0" parse as two versions, not one.
var versionPattern = regexp.MustCompile(`[vV]?(\d+\.\d+(?:\.\d+)?(?:\.\d+)?(?:[-+][a-zA-Z][\w.]*)?)`)

// ExtractVersionFromName extracts the last version-like pattern from a file or
// directory name (mod version typically follows the game version in names like
// "jei-1.20.1-15.3.0"). Returns "" when no version is present.
func ExtractVersionFromName(name string) string {
	matches := versionPattern.FindAllStringSubmatch(name, -1)
	if len(matches) > 0 {
		return matches[len(matches)-1][1]
	}
	return ""
}
