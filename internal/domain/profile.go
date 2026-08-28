package domain

// HookExplicitFlags tracks which hooks were explicitly set in profile config
// This allows distinguishing between "not set" (inherit from game) and "set to empty" (disable)
type HookExplicitFlags struct {
	BeforeAll  bool `json:"before_all"`
	BeforeEach bool `json:"before_each"`
	AfterEach  bool `json:"after_each"`
	AfterAll   bool `json:"after_all"`
}

// GameHooksExplicit tracks which hooks were explicitly set
type GameHooksExplicit struct {
	Install   HookExplicitFlags `json:"install"`
	Uninstall HookExplicitFlags `json:"uninstall"`
}

// Profile represents a collection of mods with a specific configuration
type Profile struct {
	Name       string            `json:"name"`        // Profile identifier
	GameID     string            `json:"game_id"`     // Which game this profile is for
	Mods       []ModReference    `json:"mods"`        // Mods in load order (first = lowest priority)
	Overrides  map[string][]byte `json:"-"`           // Config file overrides (path -> content)
	LinkMethod LinkMethod        `json:"link_method"` // Override game's default link method (optional)
	// LinkMethodExplicit distinguishes "not set" (inherit from game/global) from an
	// explicit "symlink", which is LinkMethod's zero value. Mirrors Game.LinkMethodExplicit.
	LinkMethodExplicit bool              `json:"link_method_explicit"`
	IsDefault          bool              `json:"is_default"`     // Is this the default profile for the game?
	Hooks              GameHooks         `json:"hooks"`          // Profile-level hook overrides
	HooksExplicit      GameHooksExplicit `json:"hooks_explicit"` // Tracks which hooks were explicitly set
}

// FindRef returns a pointer into p.Mods for the reference matching
// sourceID+modID (the same identity ModKey keys on), scanned in load order,
// or nil when the profile does not list it. Nil-receiver-safe, mirroring
// the tolerant "_ = " profile-load precedent (coreProvider.Overview,
// ApplyUpdate's lock gate) so a caller that already handled a failed
// config.LoadProfile as "profile stays nil" can call FindRef unconditionally
// instead of guarding it separately.
//
// Consolidates what used to be five near-identical "scan profile.Mods for
// one ref" loops (#97 review) spread across internal/core/flows.go,
// cmd/lmm/update.go, cmd/lmm/list.go, and cmd/lmm/mod.go, some of which used
// inconsistent key-separator conventions ("|" vs ":") despite all meaning
// the same domain.ModKey identity.
func (p *Profile) FindRef(sourceID, modID string) *ModReference {
	if p == nil {
		return nil
	}
	for i := range p.Mods {
		if p.Mods[i].SourceID == sourceID && p.Mods[i].ModID == modID {
			return &p.Mods[i]
		}
	}
	return nil
}

// ExportedProfile is the YAML-serializable format for sharing
type ExportedProfile struct {
	Name       string            `yaml:"name" json:"name"`
	GameID     string            `yaml:"game_id" json:"game_id"`
	Mods       []ModReference    `yaml:"mods" json:"mods"`
	LinkMethod string            `yaml:"link_method,omitempty" json:"link_method,omitempty"`
	Overrides  map[string]string `yaml:"overrides,omitempty" json:"overrides,omitempty"` // path (relative to game install) -> file content
}
