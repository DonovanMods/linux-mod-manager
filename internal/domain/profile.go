package domain

// Profile represents a collection of mods with a specific configuration
type Profile struct {
	Name       string            // Profile identifier
	GameID     string            // Which game this profile is for
	Mods       []ModReference    // Mods in load order (first = lowest priority)
	Overrides  map[string][]byte // Config file overrides (path -> content)
	LinkMethod LinkMethod        // Override game's default link method (optional)
	IsDefault  bool              // Is this the default profile for the game?
}

// ExportedProfile is the YAML-serializable format for sharing
type ExportedProfile struct {
	Name       string         `yaml:"name"`
	GameID     string         `yaml:"game_id"`
	Mods       []ModReference `yaml:"mods"`
	LinkMethod string         `yaml:"link_method,omitempty"`
}
