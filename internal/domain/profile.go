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

	// Collection is the profile's collection identity (#621): set on a
	// profile imported from a collection document, and on an author's own
	// profile once it is exported as one, so a re-export keeps the
	// collection's id and its origin's credit. nil for every other profile,
	// and absent from every document such a profile produces.
	Collection *CollectionInfo `json:"collection,omitempty"`
	// External lists the collection items lmm cannot fetch (#621): a tool
	// or file the curator says to get by hand. Listed, never downloaded;
	// kept so a re-export stays complete. Absent when empty.
	External []CollectionExternalItem `json:"external,omitempty"`

	// Plugins is the profile's plugin list (#616): the load order and rules
	// for the .esm/.esp/.esl files of a game whose adapter manages its
	// plugins. Absent from every document of a profile without one.
	Plugins ProfilePlugins `json:"plugins,omitzero"`

	// LoadOrders are the profile's file load orders (#620): per load-order
	// id, the files of a game whose adapter orders them (Cyberpunk 2077's
	// archives). Absent from every document of a profile without one.
	LoadOrders FileLoadOrders `json:"load_orders,omitempty"`

	// CollectionBaseline is what the profile's collection last applied
	// beyond its mod entries (#613): the base a collection update merges
	// the profile's plugin list and overrides against, so it can tell the
	// user's edit from the collection's own change. This machine's record,
	// like ModReference.FromCollection: the profile file keeps it, no
	// export, document or JSON carries it. nil for every other profile.
	CollectionBaseline *CollectionBaseline `json:"-"`
}

// CollectionBaseline is a collection's last applied plugin list and
// overrides (Profile.CollectionBaseline, #613). An empty one is a record
// too - the collection applied nothing beyond its entries - and differs
// from none (a nil *CollectionBaseline), which a collection update merges
// against as if empty but reads every shared difference as a conflict.
type CollectionBaseline struct {
	// Plugins is the collection's plugin list, as it last applied it.
	Plugins ProfilePlugins
	// Overrides maps each override path the collection last wrote to the
	// sha256 of its content (lower-case hex).
	Overrides map[string]string
}

// IsZero reports whether b records nothing.
func (b *CollectionBaseline) IsZero() bool {
	return b == nil || (b.Plugins.IsZero() && len(b.Overrides) == 0)
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
// one ref" loops (#97 review) spread across several internal/core files,
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

// ExportedProfile is the JSON shape emitted by `lmm profile export --json`
// (core.Service.ExportProfile, #309); the plain path still writes the YAML
// document. Before #309 nothing emitted this type - its only production
// role was as the decode intermediate inside config.ImportProfile - so a
// reader looking for the emitter before this addendum would have found
// none (unit Q review, M5). The YAML file `lmm profile export` writes is a
// SEPARATE DTO, internal/storage/config's own exportedProfileYAML,
// assembled from *Profile via config.ExportProfileValue: the hook pair
// needs the profile file's *string-pointer encoding to keep "unset (inherit
// from the game)" and "explicitly disabled" distinguishable (#296), which no
// yaml tag on a GameHooks/GameHooksExplicit pair can express, so this type
// carries no yaml tags at all - internal/storage/config owns that encoding
// (parseProfileHooks/serializeProfileHooks) instead of yaml-marshalling this
// type.
type ExportedProfile struct {
	Name       string            `json:"name"`
	GameID     string            `json:"game_id"`
	Mods       []ModReference    `json:"mods"`
	LinkMethod string            `json:"link_method,omitempty"`
	Overrides  map[string]string `json:"overrides,omitempty"` // path (relative to game install) -> file content

	// Hooks/HooksExplicit carry the profile's own hook overrides through an
	// export/import round trip (#296): before this they were silently
	// dropped, so a shared profile lost every `hooks:` override it had.
	// HooksExplicit is what distinguishes an explicitly-disabled hook (set,
	// empty) from an unset one (inherit) - see Profile's own pair.
	Hooks         GameHooks         `json:"hooks"`
	HooksExplicit GameHooksExplicit `json:"hooks_explicit"`

	// Collection and External are the profile's own (#621), carried so a
	// snapshot restores them; absent for a profile without them.
	Collection *CollectionInfo          `json:"collection,omitempty"`
	External   []CollectionExternalItem `json:"external,omitempty"`

	// Plugins is the profile's plugin list (#616), carried so a snapshot
	// restores it; absent for a profile without one.
	Plugins ProfilePlugins `json:"plugins,omitzero"`

	// LoadOrders are the profile's file load orders (#620), carried so a
	// snapshot restores them; absent for a profile without one.
	LoadOrders FileLoadOrders `json:"load_orders,omitempty"`
}
