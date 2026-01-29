package domain

// HookExplicitFlags tracks which hooks were explicitly set in profile config
// This allows distinguishing between "not set" (inherit from game) and "set to empty" (disable)
type HookExplicitFlags struct {
	BeforeAll  bool
	BeforeEach bool
	AfterEach  bool
	AfterAll   bool
}

// GameHooksExplicit tracks which hooks were explicitly set
type GameHooksExplicit struct {
	Install   HookExplicitFlags
	Uninstall HookExplicitFlags
}

// Profile represents a collection of mods with a specific configuration
type Profile struct {
	Name          string            // Profile identifier
	GameID        string            // Which game this profile is for
	Mods          []ModReference    // Mods in load order (first = lowest priority)
	Overrides     map[string][]byte // Config file overrides (path -> content)
	LinkMethod    LinkMethod        // Override game's default link method (optional)
	IsDefault     bool              // Is this the default profile for the game?
	Hooks         GameHooks         // Profile-level hook overrides
	HooksExplicit GameHooksExplicit // Tracks which hooks were explicitly set
}

// ExportedProfile is the YAML-serializable format for sharing
type ExportedProfile struct {
	Name       string            `yaml:"name"`
	GameID     string            `yaml:"game_id"`
	Mods       []ModReference    `yaml:"mods"`
	LinkMethod string            `yaml:"link_method,omitempty"`
	Overrides  map[string]string `yaml:"overrides,omitempty"` // path (relative to game install) -> file content
}
