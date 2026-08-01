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

// ParseLinkMethod converts a string to LinkMethod. An empty string is not
// yet set and returns the default (symlink) with ok=true, so configs that
// never set link_method keep working unchanged. Any other unrecognized
// string returns ok=false so the caller can fail loud (naming the field,
// offending value, and owning game/profile) instead of silently defaulting
// (#172).
func ParseLinkMethod(s string) (method LinkMethod, ok bool) {
	switch s {
	case "", "symlink":
		return LinkSymlink, true
	case "hardlink":
		return LinkHardlink, true
	case "copy":
		return LinkCopy, true
	default:
		return LinkSymlink, false
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
	Hooks              GameHooks         // Optional: hooks for install/uninstall operations
	DeployMode         DeployMode        // How to handle downloaded files (extract vs copy)
}

// DeployMode determines how downloaded mod archives are handled
type DeployMode int

const (
	DeployExtract DeployMode = iota // Default: extract archives to mod path
	DeployCopy                      // Copy files as-is (for games like Hytale where .zip IS the mod)
	DeployCompile                   // Compile downloaded file into a new artifact before caching (Icarus .exmodz -> .pak)
)

func (m DeployMode) String() string {
	switch m {
	case DeployExtract:
		return "extract"
	case DeployCopy:
		return "copy"
	case DeployCompile:
		return "compile"
	default:
		return "extract"
	}
}

// ParseDeployMode converts a string to DeployMode. Mirrors ParseLinkMethod's
// fail-loud contract: empty keeps the default (extract) with ok=true; any
// other unrecognized string returns ok=false (#172).
func ParseDeployMode(s string) (mode DeployMode, ok bool) {
	switch s {
	case "", "extract":
		return DeployExtract, true
	case "copy":
		return DeployCopy, true
	case "compile":
		return DeployCompile, true
	default:
		return DeployExtract, false
	}
}
