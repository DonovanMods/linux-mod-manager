package adapter

import (
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// FileLoadOrderer is the optional capability of an adapter whose game loads
// a set of mod files in an order a list file decides (#620, design
// docs/plans/2026-11-10-collections-design.md §3 `file_load_order:<id>`):
// Cyberpunk 2077 reads archive/pc/mod/modlist.txt for the order of its
// .archive files. It is the file-based counterpart of PluginOrderer, and
// Vortex's name for the same idea is "file-based load order" (a
// collection's fbLoadOrders).
//
// It is PURE DATA, like PluginOrderer: a rule table keyed by load-order id,
// saying which files each order covers and where and how the order is
// written. Core does every side effect - it finds the profile's deployed
// files, works out the order from the profile's load_orders: list, keeps
// the entries lmm did not deploy, writes the list file with its original
// captured first, and puts that original back on purge.
type FileLoadOrderer interface {
	// FileLoadOrders returns g's file load orders, or none when the
	// adapter does not recognise which game g is.
	FileLoadOrders(g *domain.Game) []FileLoadOrderSpec
}

// FileListFormat is how a game's load-order list file is spelled.
type FileListFormat int

const (
	// FileListNone is the zero value: no list is written.
	FileListNone FileListFormat = iota
	// FileListLines is one file name per line, first loaded first, with no
	// comments and no other syntax (Cyberpunk 2077's modlist.txt).
	FileListLines
	// FileListModSettings is Baldur's Gate 3's modsettings.lsx (#646): an
	// LSX document whose Mods node lists a module per entry, by the UUID
	// and the other fields of the meta.lsx inside each .pak, first loaded
	// first. It ENABLES as well as orders: a .pak whose module it does not
	// list is not loaded, and a .pak with no meta.lsx (an override pak)
	// loads without an entry and is never listed. Core reads the modules
	// from the deployed paks (internal/larian).
	FileListModSettings
)

// String returns the format's diagnostic name.
func (f FileListFormat) String() string {
	switch f {
	case FileListNone:
		return "none"
	case FileListLines:
		return "lines"
	case FileListModSettings:
		return "modsettings"
	default:
		return "unknown"
	}
}

// FileLoadOrderSpec is one file load order of one game. Every value is a
// fact about the game, and each table that fills one cites where it came
// from.
type FileLoadOrderSpec struct {
	// ID is the load order's id, Vortex's spelling, which a collection's
	// fbLoadOrders and the profile's load_orders: key by
	// ("cyberpunk2077-archive").
	ID string
	// Label is the display name a frontend shows ("Archives").
	Label string
	// Dir is the directory the ordered files are in and, unless ListDir
	// says otherwise, the list file is written to. Its components are
	// matched case-insensitively.
	Dir RootedPath
	// ListDir is the directory the list file is in, when it is not Dir
	// (Baldur's Gate 3's PlayerProfiles/Public, beside its Mods folder).
	// The zero value means Dir. It must have Dir's Root.
	ListDir RootedPath
	// Extensions are the extensions of the files the order covers, lower
	// case with the dot. A file in Dir without one is not ordered.
	Extensions []string
	// ListFile is the list file's name in Dir ("modlist.txt").
	ListFile string
	// Format is how ListFile is spelled.
	Format FileListFormat
	// LineEnding is a new list file's line ending; an existing file keeps
	// its own.
	LineEnding string
	// FirstWins reports that a file listed earlier wins a conflict with
	// one listed later. Display only: a frontend says which end wins.
	FirstWins bool
	// CollectionLoadOrder reports that a Vortex collection carries this
	// order as its top-level loadOrder - the game's one load order, which
	// Vortex keys by the game rather than by an id - instead of as an
	// fbLoadOrders entry (Baldur's Gate 3, #646). At most one of a game's
	// orders says so.
	CollectionLoadOrder bool
}

// ListDirectory is the directory s's list file is in: ListDir, or Dir when
// ListDir is unset.
func (s FileLoadOrderSpec) ListDirectory() RootedPath {
	if s.ListDir.Path == "" {
		return s.Dir
	}
	return s.ListDir
}

// Enables reports that s's list enables the files as well as ordering
// them: a file it does not name is not loaded at all (FileListModSettings),
// where a FileListLines game loads an unlisted file after the listed ones.
func (s FileLoadOrderSpec) Enables() bool { return s.Format == FileListModSettings }

// Valid reports whether s is a spec core can act on: an id, a directory, a
// list file and a format, and a ListDir, when it has one, under Dir's root.
func (s FileLoadOrderSpec) Valid() bool {
	if s.ListDir.Path != "" && s.ListDir.Root != s.Dir.Root {
		return false
	}
	return s.ID != "" && s.Dir.Path != "" && s.ListFile != "" && s.Format != FileListNone && len(s.Extensions) > 0
}

// Covers reports whether a file named name is one s orders: its extension
// is one of s's, compared case-insensitively. The list file itself never
// is.
func (s FileLoadOrderSpec) Covers(name string) bool {
	if strings.EqualFold(name, s.ListFile) {
		return false
	}
	lower := strings.ToLower(name)
	for _, ext := range s.Extensions {
		if strings.HasSuffix(lower, ext) && len(lower) > len(ext) {
			return true
		}
	}
	return false
}

// FileLoadOrders returns the adapter's valid file load orders for g. An
// adapter without FileLoadOrderer has none.
func FileLoadOrders(a GameAdapter, g *domain.Game) []FileLoadOrderSpec {
	flo, ok := a.(FileLoadOrderer)
	if !ok {
		return nil
	}
	var out []FileLoadOrderSpec
	for _, s := range flo.FileLoadOrders(g) {
		if s.Valid() {
			out = append(out, s)
		}
	}
	return out
}

// FileLoadOrder returns the adapter's file load order id for g, and whether
// it has one.
func FileLoadOrder(a GameAdapter, g *domain.Game, id string) (FileLoadOrderSpec, bool) {
	for _, s := range FileLoadOrders(a, g) {
		if s.ID == id {
			return s, true
		}
	}
	return FileLoadOrderSpec{}, false
}
