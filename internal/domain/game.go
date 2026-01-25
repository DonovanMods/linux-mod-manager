package domain

// LinkMethod determines how mods are deployed to game directories
type LinkMethod int

const (
	LinkSymlink  LinkMethod = iota // Default: symlink (space efficient)
	LinkHardlink                   // Hardlink (transparent to games)
	LinkCopy                       // Copy (maximum compatibility)
)

func (m LinkMethod) String() string {
	switch m {
	case LinkSymlink:
		return "symlink"
	case LinkHardlink:
		return "hardlink"
	case LinkCopy:
		return "copy"
	default:
		return "unknown"
	}
}

// ParseLinkMethod converts a string to LinkMethod
func ParseLinkMethod(s string) LinkMethod {
	switch s {
	case "hardlink":
		return LinkHardlink
	case "copy":
		return LinkCopy
	default:
		return LinkSymlink
	}
}

// Game represents a moddable game
type Game struct {
	ID                 string            // Unique slug, e.g., "skyrim-se"
	Name               string            // Display name
	InstallPath        string            // Game installation directory
	ModPath            string            // Where mods should be deployed
	SourceIDs          map[string]string // Map source to game ID, e.g., "nexusmods" -> "skyrimspecialedition"
	LinkMethod         LinkMethod        // How to deploy mods
	LinkMethodExplicit bool              // True if LinkMethod was explicitly set in config
	CachePath          string            // Optional: custom cache path for this game's mods
}
