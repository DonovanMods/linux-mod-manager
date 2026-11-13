package domain

// The lmm collection format (#621, design docs/plans/2026-11-10-collections-
// design.md §12; the spec is docs/collection-format.md): the shareable,
// versioned form of a profile export. These are its pure types; the YAML
// codec is internal/storage/config's, and the reader, writer, container and
// import flow are internal/core's.

// CollectionFormatVersion is the version of the collection document
// `lmm profile export` writes, recorded under its `lmm_collection:` key. A
// document without the key is version 0, the profile export lmm wrote
// before the format existed, and stays importable exactly as before.
const CollectionFormatVersion = 1

// CollectionGame is a collection document's portable game identity: what
// the game is, rather than the author's local name for it (the profile's
// game_id). An importer matches it against its own games by Steam app id
// first, then by source game ids.
type CollectionGame struct {
	// Name is the game's display name, for a person reading the document.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
	// SteamAppID is the game's Steam app id, when the author's lmm knew it.
	SteamAppID string `yaml:"steam_app_id,omitempty" json:"steam_app_id,omitempty"`
	// Sources maps a source id to the game's id on that source
	// (nexusmods: skyrimspecialedition), as games.yaml's source_ids does.
	Sources map[string]string `yaml:"sources,omitempty" json:"sources,omitempty"`
	// Versions lists the game versions the author tested, informational.
	Versions []string `yaml:"versions,omitempty" json:"versions,omitempty"`
}

// CollectionOrigin records where a collection came from when it was
// converted or imported from elsewhere: the curator's collection on its
// source. It is carried through every re-export, so the curator stays
// credited.
type CollectionOrigin struct {
	// SourceID is the source the collection lives on (nexusmods), or empty
	// for a collection shared as a file.
	SourceID string `yaml:"source_id,omitempty" json:"source_id,omitempty"`
	// Slug and Revision identify the collection and the revision imported,
	// on a source that has them.
	Slug     string `yaml:"slug,omitempty" json:"slug,omitempty"`
	Revision int    `yaml:"revision,omitempty" json:"revision,omitzero"`
	// URL is the collection's page, and Author its curator.
	URL    string `yaml:"url,omitempty" json:"url,omitempty"`
	Author string `yaml:"author,omitempty" json:"author,omitempty"`
}

// IsZero reports whether o records nothing.
func (o CollectionOrigin) IsZero() bool {
	return o == CollectionOrigin{}
}

// CollectionExternalItem is a collection item lmm cannot fetch: a tool or
// file the curator says to get by hand, with where and what to do. It is
// listed, never downloaded.
type CollectionExternalItem struct {
	Name         string `yaml:"name" json:"name"`
	URL          string `yaml:"url,omitempty" json:"url,omitempty"`
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
}

// CollectionFileRef describes one source file of a collection item, by the
// source's file id, with the facts a recipient can check the download
// against when the source provides them.
type CollectionFileRef struct {
	ID     string `yaml:"id" json:"id"`
	MD5    string `yaml:"md5,omitempty" json:"md5,omitempty"`
	SHA256 string `yaml:"sha256,omitempty" json:"sha256,omitempty"`
	Size   int64  `yaml:"size,omitempty" json:"size,omitzero"`
}

// CollectionInfo is a profile's collection identity (#621): what a
// collection document says about itself, recorded on the profile imported
// from it (and on an author's own profile once it is exported), so a
// re-export keeps the collection's id and its origin's credit.
type CollectionInfo struct {
	// ID is the collection's stable id, the same across its versions.
	ID string `yaml:"id,omitempty" json:"id,omitempty"`
	// Version is the author's version number for this publish.
	Version int `yaml:"version,omitempty" json:"version,omitzero"`
	// Title is the collection's display name, when it is not the profile's.
	Title string `yaml:"title,omitempty" json:"title,omitempty"`
	// Author, Description and InstallInstructions are the author's own
	// words, shown in an import plan.
	Author              string `yaml:"author,omitempty" json:"author,omitempty"`
	Description         string `yaml:"description,omitempty" json:"description,omitempty"`
	InstallInstructions string `yaml:"install_instructions,omitempty" json:"install_instructions,omitempty"`
	// Origin is where the collection came from, when someone else curated
	// it.
	Origin *CollectionOrigin `yaml:"origin,omitempty" json:"origin,omitempty"`
}

// IsZero reports whether c records nothing.
func (c *CollectionInfo) IsZero() bool {
	if c == nil {
		return true
	}
	return c.ID == "" && c.Version == 0 && c.Title == "" && c.Author == "" &&
		c.Description == "" && c.InstallInstructions == "" && (c.Origin == nil || c.Origin.IsZero())
}

// Equal reports whether c and other record the same identity, nil and a
// zero value alike.
func (c *CollectionInfo) Equal(other *CollectionInfo) bool {
	if c.IsZero() || other.IsZero() {
		return c.IsZero() && other.IsZero()
	}
	a, b := *c, *other
	ao, bo := a.Origin, b.Origin
	a.Origin, b.Origin = nil, nil
	if a != b {
		return false
	}
	if ao == nil || ao.IsZero() || bo == nil || bo.IsZero() {
		return (ao == nil || ao.IsZero()) && (bo == nil || bo.IsZero())
	}
	return *ao == *bo
}

// CollectionItem is one mod of a collection document: the profile's own
// reference to it, plus what the document says about it for a recipient -
// its display name (readable even without the source) and its files.
type CollectionItem struct {
	Ref   ModReference
	Name  string
	Files []CollectionFileRef
}

// CollectionDocument is a version-1 collection document, decoded: the
// profile it describes and what the format adds around it. The YAML codec
// is internal/storage/config's (MarshalCollection, ParseCollection).
type CollectionDocument struct {
	// FormatVersion is the document's `lmm_collection:` value.
	FormatVersion int
	// Name is the profile name, and GameID the author's local game id: an
	// older lmm imports a version-1 document by these two, as it would a
	// version-0 one.
	Name   string
	GameID string
	// Info is the collection's identity and the author's metadata.
	Info CollectionInfo
	// Game is the portable game identity an importer matches.
	Game CollectionGame
	// Requires names the features the document needs an importer to
	// support (derived on export, gated on import).
	Requires []string
	// Signer is reserved for signed collections (#624); importers ignore
	// it until then.
	Signer string
	// Items are the mods, in load order; External the items lmm cannot
	// fetch.
	Items    []CollectionItem
	External []CollectionExternalItem
	// Hooks/HooksExplicit and Overrides are the profile's own, as an
	// ExportedProfile carries them.
	Hooks         GameHooks
	HooksExplicit GameHooksExplicit
	Overrides     map[string]string
}
