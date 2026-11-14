package source

import (
	"context"
	"errors"
	"io/fs"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// This file is the collection half of the source seam that a source with
// revisioned, recipe-carrying collections implements (#607, design
// docs/plans/2026-11-10-collections-design.md §2): NexusMods today. A
// collection there is two things - the LIST of pinned files, which the
// source's API returns (GameCollectionResolver), and the RECIPE for
// installing them, an archive whose manifest says how
// (CollectionManifestFetcher). Core owns the download and the extraction;
// the source supplies the URL and the parse, and core never imports a
// concrete source. The Workshop's CollectionResolver is unchanged.

// CollectionItemKind says what one collection list item is.
type CollectionItemKind string

const (
	// CollectionItemModFile is a file of a mod on the source itself.
	CollectionItemModFile CollectionItemKind = "mod_file"
	// CollectionItemExternal is something the curator says to fetch
	// elsewhere: a URL and instructions, never fetched by lmm.
	CollectionItemExternal CollectionItemKind = "external"
	// CollectionItemBundled ships inside the collection's own archive.
	CollectionItemBundled CollectionItemKind = "bundled"
)

// CollectionItem is one entry of a resolved collection's list, as the
// source's API states it (the recipe, CollectionManifest, says how to
// install it).
type CollectionItem struct {
	// Kind is what the item is.
	Kind CollectionItemKind
	// ModID and FileID name a mod file on the source; FileID is the exact
	// file the curator pinned, which need not be the mod's primary one.
	ModID  string
	FileID string
	// Version is the pinned file's version, and Name the mod's display
	// name. FileName is the file's own name on the source, and
	// FileCategory the source's category for it (NexusMods: MAIN,
	// OPTIONAL, OLD_VERSION, ARCHIVED, ...).
	Version      string
	Name         string
	FileName     string
	FileCategory string
	// Optional is the curator's "recommended, not required".
	Optional bool
	// UpdatePolicy is the curator's policy for the pin: exact, prefer or
	// latest.
	UpdatePolicy string
	// URL is the file's page (a mod file) or where to get it (external).
	URL string
	// Instructions are the curator's words for an external item.
	Instructions string
	// SizeBytes is the file's size, when the source states it.
	SizeBytes int64
}

// Collection is one CollectionResolver or GameCollectionResolver answer: a
// published list of items on the source, in the order its author
// sequenced them. Name is best-effort (a source that cannot name a
// collection still resolves one); URL is the collection's own page, for a
// frontend that wants to link it.
type Collection struct {
	// ID is the collection's identity on the source: the Workshop's
	// published-file id, NexusMods' slug.
	ID      string
	Name    string
	URL     string
	ItemIDs []string
	// Revision is the revision resolved, 0 for a source without
	// revisions (the Workshop).
	Revision int
	// Author is the curator's name, when the source states it.
	Author string
	// Items is the structured list (GameCollectionResolver); the Workshop
	// leaves it nil.
	Items []CollectionItem
	// Handle is opaque to core: whatever the source needs to fetch this
	// revision's recipe again (NexusMods' download_link path, keyed by the
	// collection's and the revision's numeric ids).
	Handle string
}

// GameCollectionResolver resolves a collection FOR A GAME: NexusMods'
// collectionRevision query takes the game's domain name, and a reference
// naming a different game must be refused (ErrInvalidReference), never
// imported into the wrong one. gameSourceID is the game's id on this
// source (domain.Game.SourceIDs). ref is what the user typed - a slug, a
// URL, either with an "@<revision>" suffix - and the source decides what it
// recognises. No revision means the latest published one, and the answer's
// Revision says which that was.
type GameCollectionResolver interface {
	ResolveGameCollection(ctx context.Context, gameSourceID, ref string) (Collection, error)
}

// CollectionManifestFetcher is the recipe half. Optional, type-asserted,
// like CollectionResolver.
type CollectionManifestFetcher interface {
	// ManifestArchiveURL returns a short-lived URL for the revision's
	// recipe archive (c is a GameCollectionResolver answer). The URL is a
	// credential while it lives: never log or display it.
	ManifestArchiveURL(ctx context.Context, c Collection) (string, error)
	// ParseManifest reads an EXTRACTED recipe archive and returns the
	// source-neutral recipe. Pure: no network.
	ParseManifest(root fs.FS) (CollectionManifest, error)
}

// CollectionManifest is a collection's recipe, source-neutral: what each
// item needs to install as the curator installed it, and what the
// collection as a whole sets up. Everything the parser did not recognise,
// and does not know to be informational, is listed in Unknown, because a
// key lmm cannot read is a feature it cannot replay.
type CollectionManifest struct {
	// Name, Author, Description and InstallInstructions are the curator's.
	Name                string
	Author              string
	Description         string
	InstallInstructions string
	// GameSourceID is the game's id on the source the manifest names, and
	// GameVersions the game versions the curator tested.
	GameSourceID string
	GameVersions []string
	// Mods are the items, in the manifest's order.
	Mods []ManifestMod
	// ModRules order (before/after) or describe (requires, conflicts, ...)
	// the items.
	ModRules []ManifestModRule
	// Plugins is the curator's plugin list, in load order; PluginRules the
	// curator's sorting rules for it.
	Plugins     []ManifestPlugin
	PluginRules ManifestPluginRules
	// ExcludePluginRules is the curator's collectionConfig.excludePluginRules:
	// the collection's plugin rules are not to be applied (#611).
	ExcludePluginRules bool
	// Tools are the curator's tool shortcuts: listed, never created or run.
	Tools []ManifestTool
	// FileLoadOrders are the collection's file-based load orders, one per
	// load-order id, with how many entries each holds.
	FileLoadOrders []ManifestFileLoadOrder
	// Unknown is every non-empty key the parser did not recognise.
	Unknown []ManifestUnknownKey
}

// ManifestMod is one item of a recipe.
type ManifestMod struct {
	// Name, Version and Author are display facts.
	Name    string
	Version string
	Author  string
	// DomainName is the game the item itself says it is for (Vortex's
	// per-mod domainName); empty when it does not say. One that differs
	// from the collection's game names a mod of ANOTHER game, which the
	// collection's game has no business looking up.
	DomainName string
	// Optional is the curator's "recommended, not required".
	Optional bool
	// Phase orders installation: a mod in phase N installs after every
	// lower phase.
	Phase int
	// SourceType is where the item comes from: nexus, bundle, or one of
	// the external kinds (browse, direct, manual).
	SourceType string
	// ModID and FileID name the file on the source (SourceType nexus).
	ModID  string
	FileID string
	// MD5 and SizeBytes describe the file the curator installed.
	MD5       string
	SizeBytes int64
	// LogicalFilename and FileExpression name the file independently of
	// its version, for an update policy and for rule references.
	LogicalFilename string
	FileExpression  string
	// UpdatePolicy is exact, prefer or latest.
	UpdatePolicy string
	// URL and Instructions say where and how to get an external item, and
	// what the curator wants done with any item.
	URL          string
	Instructions string
	// ModType is the item's stated mod type (Vortex's details.type): where
	// the game deploys it. Empty is the game's default.
	ModType string
	// InstallerType names the installer the recorded choices are for
	// ("fomod"); empty when none are recorded. Choices are those choices,
	// in the shape domain.FomodChoices.Steps takes.
	InstallerType string
	Choices       []domain.FomodStepChoice
	// FileList is the exact file list the curator installed (Vortex's
	// replicate mode), Patches the binary patches to apply (file path ->
	// the baseline's CRC32), and FileOverrides the paths this mod must win.
	FileList      []ManifestFileHash
	Patches       map[string]string
	FileOverrides []string
}

// ManifestFileHash is one file of an item's exact file list.
type ManifestFileHash struct {
	Path string
	MD5  string
}

// ManifestModRef names a mod in a rule the way the manifest does: by file
// facts, not by item index.
type ManifestModRef struct {
	FileMD5         string
	LogicalFileName string
	VersionMatch    string
	FileExpression  string
	IDHint          string
	Tag             string
}

// ManifestModRule is one mod rule: Source <Type> Reference (before, after,
// requires, conflicts, recommends, provides).
type ManifestModRule struct {
	Type      string
	Source    ManifestModRef
	Reference ManifestModRef
	// Unknown lists the keys of the rule (and of its two sides, as
	// "source.<key>"/"reference.<key>") the parser did not recognise. They
	// matter only if the rule is applied, so they are the rule's, not the
	// collection's.
	Unknown []string
}

// ManifestPlugin is one entry of the curator's plugin list.
type ManifestPlugin struct {
	Name    string
	Enabled bool
}

// ManifestPluginRules is the curator's plugin sorting rules (a LOOT
// userlist): per-plugin groups and after-edges, and the groups' own order.
type ManifestPluginRules struct {
	Plugins []ManifestPluginRule
	Groups  []ManifestPluginGroup
}

// IsZero reports whether r holds no rule.
func (r ManifestPluginRules) IsZero() bool { return len(r.Plugins) == 0 && len(r.Groups) == 0 }

// ManifestPluginRule is one plugin's userlist entry. Both spellings of
// the rule keys land here (#611): LOOT's `req`/`inc` and Vortex's
// `requires`/`incompatible`.
type ManifestPluginRule struct {
	Name  string
	Group string
	// After names plugins this one loads after; Req plugins it requires
	// (and loads after); Inc plugins it is incompatible with, which orders
	// nothing.
	After []string
	Req   []string
	Inc   []string
}

// ManifestPluginGroup is one userlist group and the groups it loads after.
type ManifestPluginGroup struct {
	Name  string
	After []string
}

// ManifestTool is a curator's tool shortcut.
type ManifestTool struct {
	Name string
	Exe  string
}

// ManifestFileLoadOrder is one file-based load order and its size.
type ManifestFileLoadOrder struct {
	ID      string
	Entries int
}

// ManifestUnknownKey is one key the parser did not recognise: Key is its
// dotted path (a top-level key is its bare name, a mod's "mods.<key>"),
// Item the name of the mod it was found on and ModIndex that mod's index in
// Mods; a collection-level key has no Item and a ModIndex of -1.
type ManifestUnknownKey struct {
	Item     string
	ModIndex int
	Key      string
}

// ErrInvalidManifest is a collection recipe the source's parser refused:
// missing, malformed, or over its size cap. The collection cannot be
// gated without its recipe, so an import of it fails closed.
var ErrInvalidManifest = errors.New("invalid collection manifest")
