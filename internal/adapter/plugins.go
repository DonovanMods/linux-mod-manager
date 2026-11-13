package adapter

import "github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

// PluginOrderer is the optional capability of an adapter whose game loads
// plugin files (.esm/.esp/.esl) in an order the user controls, through an
// active-plugin list the game reads at start (#355, design
// docs/plans/2026-11-10-collections-design.md §5).
//
// It is PURE DATA: the answer is a rule table, and the adapter touches no
// disk to give it. Core does every side effect - it finds the profile's
// deployed plugins, reads their headers, sorts, renders and writes the
// list - and asks this capability only where the list lives, how it is
// spelled, and which header bits mean what.
//
// Nothing in core consumes it yet: the profile's plugin list, the sort and
// the write are a later story (P3, #616). It ships first so that story has
// tables to read and the tables have their own tests.
type PluginOrderer interface {
	// PluginRules returns g's plugin rules, or the zero PluginSpec when the
	// adapter does not recognise which game g is. The zero spec means "do
	// not manage this game's plugin list": an adapter never guesses which
	// game it is looking at.
	PluginRules(g *domain.Game) PluginSpec
}

// PluginRules returns the adapter's plugin rules for g, and whether it has
// any. An adapter without PluginOrderer, or one that answers the zero spec
// (it does not know which game g is), reports false.
func PluginRules(a GameAdapter, g *domain.Game) (PluginSpec, bool) {
	po, ok := a.(PluginOrderer)
	if !ok {
		return PluginSpec{}, false
	}
	spec := po.PluginRules(g)
	return spec, spec.ListKind != ListNone
}

// ListKind is how a game records which plugins are active and in what
// order.
type ListKind int

const (
	// ListNone is the zero value: no plugin list is managed.
	ListNone ListKind = iota
	// ListAsterisk is one file, ListFile, that holds every plugin in load
	// order; an active plugin's line starts with '*', an inactive one is
	// the bare name, and '#' starts a comment (Skyrim SE/AE/VR, Fallout 4
	// and 4 VR, Starfield).
	ListAsterisk
	// ListTextfile is the textfile-based standard (Skyrim LE): ListFile
	// lists the active plugins, and OrderFile, beside it, the full load
	// order of every installed plugin.
	ListTextfile
)

// String returns the list kind's diagnostic name.
func (k ListKind) String() string {
	switch k {
	case ListNone:
		return "none"
	case ListAsterisk:
		return "asterisk"
	case ListTextfile:
		return "textfile"
	default:
		return "unknown"
	}
}

// HeaderKind is the plugin file format whose header core reads for a
// plugin's flags and masters.
type HeaderKind int

const (
	// HeaderNone is the zero value: no header format is known.
	HeaderNone HeaderKind = iota
	// HeaderTES4 is a file that starts with a TES4 record with a 24-byte
	// record header (every game since Skyrim LE).
	HeaderTES4
)

// String returns the header kind's diagnostic name.
func (k HeaderKind) String() string {
	switch k {
	case HeaderNone:
		return "none"
	case HeaderTES4:
		return "tes4"
	default:
		return "unknown"
	}
}

// SpecRoot names the directory a RootedPath is relative to. Core resolves
// it; the adapter only names it.
type SpecRoot int

const (
	// SpecRootGame is the game's install_path.
	SpecRootGame SpecRoot = iota
	// SpecRootMyGames is <Documents>/My Games/<folder> inside the game's
	// Wine prefix, where <folder> is the AppDataFolder core chose for the
	// list file. For every game PluginSpec covers, the "My Games" folder
	// and the AppData folder have the same name.
	SpecRootMyGames
)

// String returns the root's diagnostic name.
func (r SpecRoot) String() string {
	switch r {
	case SpecRootGame:
		return "game"
	case SpecRootMyGames:
		return "my_games"
	default:
		return "unknown"
	}
}

// RootedPath is a path relative to a named root: slash-separated, never
// absolute.
type RootedPath struct {
	// Root is the directory Path is relative to.
	Root SpecRoot
	// Path is the slash-separated path under Root.
	Path string
}

// AppDataFolder is one candidate for the folder under the prefix's
// AppData/Local that holds the list file.
type AppDataFolder struct {
	// Name is the folder's name ("Skyrim Special Edition GOG").
	Name string
	// When is a file in the game's install directory whose presence
	// selects this folder ("Galaxy64.dll" for a GOG install). Empty is the
	// default folder, chosen when no other candidate's file is present.
	// Candidates are listed in the order to test them, the default last.
	When string
}

// PluginSpec is one game's plugin rules. Every value in it is a fact about
// the game, and each table that fills one cites where the fact came from.
type PluginSpec struct {
	// ListKind is how the game records the active plugins and their order.
	ListKind ListKind
	// AppDataFolders are the candidate folders under AppData/Local that
	// hold ListFile, in the order core tests them.
	AppDataFolders []AppDataFolder
	// ListFile is the active-plugin list's file name ("Plugins.txt"),
	// encoded in Windows-1252. The games read the capitalised name even
	// where their launchers write a lower-case one.
	ListFile string
	// OrderFile is ListTextfile's full load order ("loadorder.txt"), beside
	// ListFile and encoded in UTF-8 without a BOM. Empty for ListAsterisk.
	OrderFile string

	// Extensions are the plugin file extensions, lower case with the dot.
	Extensions []string
	// HeaderKind is the plugin file format.
	HeaderKind HeaderKind
	// MasterFlag is the header flag bit that marks a master.
	MasterFlag uint32
	// LightFlag is the header flag bit that marks a light (small) plugin,
	// or 0 for a game without light plugins.
	LightFlag uint32
	// MediumFlag is the header flag bit that marks a medium plugin, or 0
	// for a game without them.
	MediumFlag uint32
	// UpdateFlag is the header flag bit that stops a light-implying
	// extension from making a plugin light (Starfield), or 0.
	UpdateFlag uint32
	// BlueprintFlag is the header flag bit that marks a blueprint plugin
	// (Starfield), or 0. The game removes blueprint plugins from ListFile
	// itself and orders them by file time, so lmm never lists them.
	BlueprintFlag uint32
	// ExtImpliesMaster are the extensions that make a plugin a master
	// whatever its header says.
	ExtImpliesMaster []string
	// ExtImpliesLight are the extensions that make a plugin light whatever
	// its header says (unless UpdateFlag is set, where UpdateFlag is
	// non-zero).
	ExtImpliesLight []string
	// UnlistedPrefixes are plugin name prefixes the game strips from
	// ListFile itself (Starfield's "BlueprintShips-"), compared
	// case-insensitively. lmm never lists them.
	UnlistedPrefixes []string

	// Implicit are the plugins the game always loads first, in this order,
	// active or not; they are never written to ListFile.
	Implicit []string
	// ImplicitlyActive are plugins the game always loads, without a fixed
	// position (Skyrim LE's Update.esm loads after the other masters
	// unless the list places it). They may be written to the list to give
	// them a position.
	ImplicitlyActive []string
	// CCCFiles are the Creation Club files that list more Implicit
	// plugins, one name per line, after the fixed ones. They are in
	// precedence order: the FIRST that exists is read and the rest are
	// ignored, even when it is empty.
	CCCFiles []RootedPath
	// ExtraPluginDirs are directories, beside the game's mod_path, that
	// the game also loads plugins from.
	ExtraPluginDirs []RootedPath

	// MaxFull is the most active full plugins the game loads. One slot is
	// given up for each of the light and medium plugin classes that has
	// an active plugin, so 253 full plugins fit when both are in use.
	MaxFull int
	// MaxLight is the most active light plugins, or 0 for a game without
	// them.
	MaxLight int
	// MaxMedium is the most active medium plugins, or 0 for a game without
	// them.
	MaxMedium int
}
