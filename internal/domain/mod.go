package domain

import "time"

// UpdateProgressFunc is called during update checks with (current 1-based index, total count, mod name).
// Set via context when running "lmm -v update" to get per-mod progress.
type UpdateProgressFunc func(n, total int, modName string)

type updateProgressKey struct{}

// UpdateProgressContextKey is the context key for UpdateProgressFunc. Attach with context.WithValue.
var UpdateProgressContextKey = &updateProgressKey{}

// SourceLocal is the source ID for mods imported from local files
const SourceLocal = "local"

// UpdatePolicy determines how a mod handles updates
type UpdatePolicy int

const (
	UpdateNotify UpdatePolicy = iota // Default: show available, require approval
	UpdateAuto                       // Automatically apply updates
	UpdatePinned                     // Never update
)

// ModFile represents a single file within a mod archive (after extraction)
type ModFile struct {
	Path     string // Relative path within mod archive
	Size     int64
	Checksum string // SHA256
}

// DownloadableFile represents a file available for download from a mod source
type DownloadableFile struct {
	ID          string // Source-specific file ID
	Name        string // Display name
	FileName    string // Actual filename (e.g., "mod-1.0.zip")
	Version     string // File version
	Size        int64  // Size in bytes
	IsPrimary   bool   // Whether this is the primary/main file
	Category    string // Category: "MAIN", "OPTIONAL", "UPDATE", etc.
	Description string // File description
}

// ModReference is a pointer to a mod (used in profiles, dependencies)
type ModReference struct {
	SourceID string   `yaml:"source_id"`          // "nexusmods", "curseforge", etc.
	ModID    string   `yaml:"mod_id"`             // Source-specific identifier
	Version  string   `yaml:"version"`            // Empty string means "latest"
	FileIDs  []string `yaml:"file_ids,omitempty"` // Source-specific file IDs that were installed
}

// Mod represents a mod from any source
type Mod struct {
	ID           string
	SourceID     string
	Name         string
	Version      string
	Author       string
	Summary      string
	Description  string
	GameID       string
	Category     string
	Downloads    int64
	Endorsements int64
	PictureURL   string // Main image URL (e.g. NexusMods picture_url)
	SourceURL    string // Web page URL (e.g. CurseForge mod page)
	Files        []ModFile
	Dependencies []ModReference
	UpdatedAt    time.Time
}

// InstalledMod tracks a mod installed in a profile
type InstalledMod struct {
	Mod
	ProfileName     string
	UpdatePolicy    UpdatePolicy
	InstalledAt     time.Time
	Enabled         bool       // User intent: wants this mod active
	Deployed        bool       // Current state: files are in game directory
	PreviousVersion string     // Version before last update (for rollback)
	LinkMethod      LinkMethod // How the mod was deployed (symlink, hardlink, copy)
	FileIDs         []string   // Source-specific file IDs that were downloaded
	ManualDownload  bool       // True if mod requires manual download (CurseForge restricted, etc.)
}

// Update represents an available update for an installed mod
type Update struct {
	InstalledMod       InstalledMod
	NewVersion         string
	Changelog          string
	FileIDReplacements map[string]string // Old file ID -> new file ID when a file was superseded (e.g. NexusMods FileUpdates)
}
