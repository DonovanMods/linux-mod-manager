package domain

import "fmt"

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

// MarshalText implements encoding.TextMarshaler.
func (m LinkMethod) MarshalText() ([]byte, error) { return []byte(m.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (m *LinkMethod) UnmarshalText(b []byte) error {
	method, ok := ParseLinkMethod(string(b))
	if !ok {
		return fmt.Errorf("unknown link method %q", b)
	}
	*m = method
	return nil
}

// ValidLinkMethods lists ParseLinkMethod's recognized non-empty values, in
// the same order as the type's constants, for use in "unrecognized value"
// error messages — the single source of truth so those messages can't go
// stale the way a hand-written copy did (#172 review round 1).
const ValidLinkMethods = "symlink, hardlink, copy"

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
	ID                  string            `json:"id"`                    // Unique slug, e.g., "skyrim-se"
	Name                string            `json:"name"`                  // Display name
	InstallPath         string            `json:"install_path"`          // Game installation directory
	ModPath             string            `json:"mod_path"`              // Where mods should be deployed
	SourceIDs           map[string]string `json:"source_ids"`            // Map source to game ID, e.g., "nexusmods" -> "skyrimspecialedition"
	LinkMethod          LinkMethod        `json:"link_method"`           // How to deploy mods
	LinkMethodExplicit  bool              `json:"link_method_explicit"`  // True if LinkMethod was explicitly set in config
	CachePath           string            `json:"cache_path,omitempty"`  // Optional: custom cache path for this game's mods
	Hooks               GameHooks         `json:"hooks"`                 // Optional: hooks for install/uninstall operations
	DeployMode          DeployMode        `json:"deploy_mode"`           // How to handle downloaded files (extract vs copy)
	ConvertPaks         bool              `json:"convert_paks"`          // #221: convert prebuilt .pak mods into the merged pak (DeployCompile games; default true when omitted from games.yaml, must be set explicitly for direct Game literals)
	ConvertPaksExplicit bool              `json:"convert_paks_explicit"` // True if ConvertPaks was explicitly set in config (round-trip fidelity, like LinkMethodExplicit)
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

// MarshalText implements encoding.TextMarshaler.
func (m DeployMode) MarshalText() ([]byte, error) { return []byte(m.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (m *DeployMode) UnmarshalText(b []byte) error {
	mode, ok := ParseDeployMode(string(b))
	if !ok {
		return fmt.Errorf("unknown deploy mode %q", b)
	}
	*m = mode
	return nil
}

// ValidDeployModes is ValidLinkMethods' counterpart for ParseDeployMode.
const ValidDeployModes = "extract, copy, compile"

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
