package domain

import "fmt"

// LinkMethod determines how mods are deployed to game directories
type LinkMethod int

// LinkSymlink, LinkHardlink, and LinkCopy are LinkMethod's three deploy
// strategies, in the order ParseLinkMethod/ValidLinkMethods list them:
// symlink is the default (space efficient), hardlink stays transparent to
// games that stat their own files, copy is the maximum-compatibility
// fallback for filesystems that support neither (e.g. across a bind mount).
const (
	LinkSymlink  LinkMethod = iota // Default: symlink (space efficient)
	LinkHardlink                   // Hardlink (transparent to games)
	LinkCopy                       // Copy (maximum compatibility)
)

// String returns the method's wire/config name ("symlink", "hardlink", or
// "copy"; "unknown" for an out-of-range value).
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
//
// NOTE: yaml.v3 also honours TextMarshaler — a struct that yaml-marshals this
// type will now emit the name, not the int. The storage/config DTOs use
// plain strings; keep it that way.
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
	// Adapter is the game adapter that answers "what does this GAME do
	// with mod content" (#353): the games.yaml `adapter:` value, verbatim.
	// Empty means the generic-files identity, which is every game lmm
	// managed before the seam existed - so an untouched games.yaml
	// round-trips byte-identically and every existing document keeps the
	// shape it had.
	//
	// It is the CONFIGURED value, not a resolved one. `deploy_mode:
	// compile` with no adapter derives the icarus adapter, but that
	// derivation needs the adapter registry (only core has one) and must
	// never reach the file lmm writes back - so it lives in core's
	// resolver, and this field stays what the user typed.
	//
	// It sits beside Loader deliberately: the two optional per-game
	// declarations, both omitempty, both absent from every document a game
	// that declares neither produces.
	Adapter string `json:"adapter,omitempty"`
	// Loader is #359's optional per-game mod-loader declaration
	// (games.yaml's `loader:` block, loader.go). nil for the overwhelming
	// majority of games, which need no loader at all - so the member is
	// absent from every document a loaderless game produces, and every
	// golden recorded before this field existed is byte-identical.
	Loader *GameLoader `json:"loader,omitempty"`
}

// DeployMode determines how downloaded mod archives are handled
type DeployMode int

// DeployExtract, DeployCopy, and DeployCompile are DeployMode's three
// handling strategies for a downloaded file: extract is the default (unpack
// an archive to the mod path), copy places the download as-is (for games
// like Hytale where the .zip IS the mod), compile runs it through a
// game-specific compiler into a new artifact before caching (Icarus
// .exmodz -> .pak, #196/#221).
const (
	DeployExtract DeployMode = iota // Default: extract archives to mod path
	DeployCopy                      // Copy files as-is (for games like Hytale where .zip IS the mod)
	DeployCompile                   // Compile downloaded file into a new artifact before caching (Icarus .exmodz -> .pak)
)

// String returns the mode's wire/config name ("extract", "copy", or
// "compile"). An out-of-range value also returns "extract" (the default),
// not an error - MarshalText's doc comment explains why.
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
//
// NOTE: yaml.v3 also honours TextMarshaler — a struct that yaml-marshals this
// type will now emit the name, not the int. The storage/config DTOs use
// plain strings; keep it that way.
//
// Out-of-range values marshal as the default name ("extract"), not an error
// — this is String()'s pre-existing behaviour, preserved on purpose (unlike
// LinkMethod/VerifyTier, which reject).
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

// DetectedGame is a Steam game found on disk that lmm knows how to
// configure. Moved verbatim from internal/source/steam.DetectedGame (v2
// Phase 2 Task 21, Ruling 8): app.DetectGames wraps the Steam-specific scan
// and returns this domain type so core can consume detected games (via
// GameFromDetected) without importing a concrete source. steam keeps
// DetectedGame as a type alias for this.
type DetectedGame struct {
	SteamAppID  string `json:"steam_app_id"`          // Steam App ID
	Slug        string `json:"slug"`                  // lmm game ID (from known games list)
	Name        string `json:"name"`                  // Display name
	InstallPath string `json:"install_path"`          // Absolute path to game install (e.g. .../common/Skyrim Special Edition)
	ModPath     string `json:"mod_path"`              // Absolute path to mod directory (InstallPath + ModPath relative)
	NexusID     string `json:"nexus_id,omitempty"`    // NexusMods game domain ID. Optional: "" for games with no NexusMods presence (#177).
	DeployMode  string `json:"deploy_mode,omitempty"` // games.yaml's deploy_mode string, passed through from GameInfo.DeployMode. Optional: "" means the default (extract).
	// Adapter is games.yaml's `adapter:` value (#353), passed through from
	// the curated known-games entry. Only a CURATED entry can carry one -
	// nothing on disk says what a game does with mod content - so an
	// unknown candidate carries none, which reads as `generic-files`, the
	// same conservative default its empty ModPath is.
	//
	// omitempty: a game whose entry names no adapter carries no member at
	// all, so every detect document recorded before this field existed is
	// byte-identical.
	Adapter string            `json:"adapter,omitempty"`
	Sources map[string]string `json:"sources,omitempty"` // games.yaml's sources map, passed through from GameInfo.Sources. Optional: nil means "derive {nexusmods: NexusID}".
	// Known reports whether the Steam app id matched lmm's known-games
	// list (the embedded steam-games.yaml plus the user's override). A
	// known candidate carries that entry's curated slug, mod path, deploy
	// mode and sources; an unknown one carries only what the app manifest
	// itself said - the Steam name, the install path, a slug derived from
	// the name - with an EMPTY ModPath and no sources, because nothing on
	// disk says where that game keeps its mods (#206).
	//
	// omitzero, so today's known-only detection emits exactly the shape it
	// always did except for this one added key, and an unknown row is the
	// one that carries no "known" member at all. Absent therefore reads as
	// "not in the known-games list", which is also what a pre-#206 client
	// decoding this document would assume of every row it had never seen.
	Known bool `json:"known,omitzero"`
	// WorkshopItems is how many Steam Workshop items this app's
	// appworkshop manifest declares as installed (#269). Non-zero is what
	// made detection prefill `steamworkshop: <appid>` into Sources, so the
	// count is the evidence for that prefill rather than a separate claim -
	// a frontend can say "30 Workshop items already downloaded" next to
	// the row it is offering to add.
	//
	// omitzero: an app with no workshop manifest, or one whose manifest is
	// the empty stub Steam leaves for a workshop-capable app with nothing
	// subscribed, carries no member at all and gets no prefill.
	WorkshopItems int `json:"workshop_items,omitzero"`
	// Loader is the curated known-games entry's mod-loader declaration
	// (#416), carried through to the Game this candidate configures so a
	// BepInEx game detected and added in one pass arrives with the
	// declaration #359's precondition asks for - otherwise the very first
	// plugin install of a curated BepInEx game is refused and the flow is
	// worse than it was before the loader existed.
	//
	// Only a CURATED entry can carry one: nothing on disk says which loader
	// a game wants, so an uncurated candidate never gets a guess. The
	// catalog declares kind and (optionally) version; runtime and bootstrap
	// are facts about the INSTALLATION rather than about the game, which
	// `lmm game show` answers from disk.
	//
	// omitempty: a game that needs no loader - which is every entry in the
	// shipped catalog today - carries no member at all, so every detect
	// document recorded before this field existed is byte-identical.
	Loader *GameLoader `json:"loader,omitempty"`
}

// Listable reports whether a detect LISTING shows this candidate without
// being asked for the wider list (#368). Two rows qualify: one lmm has a
// curated known-games entry for, and one whose Steam Workshop manifest
// declares items already downloaded - a game with thirty subscribed items
// is moddable by observation, whatever the curated list says, and #269's
// whole Tier 1 exists to track exactly those. Hiding it behind
// `--include-unknown` / `?all=1` put the two games the feature was built
// for behind a flag nobody would guess.
//
// It is a method on the candidate rather than a filter inside either
// frontend so `lmm game detect`, GET /api/v1/games/detect and the web
// first-run list cannot disagree about which rows a user sees.
//
// A --no-workshop scan stamps no count and gets no prefill, so it lists
// exactly what it always did - which is what that flag asks for.
func (g DetectedGame) Listable() bool {
	return g.Known || g.WorkshopItems > 0
}

// Addable reports whether a detect SELECTION can configure this candidate
// with nothing more asked of the user (#368): a curated row carries its
// known-games entry's mod path and sources, and an uncurated row carries a
// source map only when detection prefilled one - today, #269's
// `steamworkshop: <appid>`.
//
// An uncurated row with no source at all is listed (under
// `--include-unknown`) but not addable here: `lmm game add
// --from-detected <app-id>` is the path that collects the source, and a
// detect prompt that silently wrote a game with no mod source would create
// an unusable games.yaml entry (core.GameSpec refuses one for the same
// reason).
func (g DetectedGame) Addable() bool {
	return g.Known || len(g.Sources) > 0
}

// WorkshopItem is one Steam Workshop item already installed on this machine
// by the Steam client (#269). It follows DetectedGame's precedent (Ruling
// 8): the type lives in domain so internal/core can consume a scan of the
// user's Steam libraries without importing the concrete
// internal/source/steamworkshop package that produces it.
//
// Every field is a fact read out of Steam's own appworkshop manifest; lmm
// never writes any of them. Path is the directory the Steam client owns and
// the game loads the item from - it is what lands in
// InstalledMod.ExternalPath at adopt time.
type WorkshopItem struct {
	// FileID is the Steam published-file id, which is also the mod id lmm
	// tracks the item under.
	FileID string `json:"file_id"`
	// Path is the absolute content directory Steam installed the item into
	// (<library>/steamapps/workshop/content/<appid>/<fileid>).
	Path string `json:"path"`
	// SizeOnDisk is the manifest's recorded size in bytes; 0 when the
	// manifest does not record one.
	SizeOnDisk int64 `json:"size_on_disk,omitzero"`
	// Manifest is Steam's content id for the installed revision - the
	// version identity lmm records and compares against the API's
	// hcontent_file. Absent on an older record, which is why TimeUpdated
	// exists as the secondary signal.
	Manifest string `json:"manifest,omitempty"`
	// TimeUpdated is the manifest's Unix timestamp for the installed
	// revision. It is what human-facing surfaces show as the item's
	// version - a 19-digit content id is not a version anybody can read.
	TimeUpdated int64 `json:"time_updated,omitzero"`
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
