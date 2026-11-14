package domain

import (
	"maps"
	"slices"
)

// The profile's file load orders (#620, design docs/plans/2026-11-10-
// collections-design.md §3, §12): a third ordering axis, beside the mod
// order and a Bethesda game's plugin list, for a game that loads a set of
// mod files in the order a list file gives - Cyberpunk 2077's .archive
// files, in archive/pc/mod/modlist.txt. Each order is keyed by its id, the
// spelling Vortex gives it and a collection's fbLoadOrders carries
// ("cyberpunk2077-archive"), and is the file names in order, first loaded
// first.
//
// The list is the user's INTENT, like the plugin list: core derives the
// order it writes from it and the files actually deployed, and never
// writes that result back here. A name whose file is not deployed is kept,
// so disabling and re-enabling a mod does not lose its files' places.

// FileLoadOrders is a profile's `load_orders:` block: per load-order id, the
// file names in load order.
type FileLoadOrders map[string][]string

// IsZero reports whether l orders nothing: no id with a name.
func (l FileLoadOrders) IsZero() bool {
	for _, names := range l {
		if len(names) > 0 {
			return false
		}
	}
	return true
}

// IDs are l's ids that order something, sorted.
func (l FileLoadOrders) IDs() []string {
	var out []string
	for id, names := range l {
		if len(names) > 0 {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// Equal reports whether l and other order the same names, an id with no
// names counting as absent.
func (l FileLoadOrders) Equal(other FileLoadOrders) bool {
	// Clone drops the empty ids on both sides first.
	return maps.EqualFunc(l.Clone(), other.Clone(), slices.Equal)
}

// Clone is a deep copy of l without its empty ids; nil when l orders
// nothing.
func (l FileLoadOrders) Clone() FileLoadOrders {
	if l.IsZero() {
		return nil
	}
	out := make(FileLoadOrders, len(l))
	for id, names := range l {
		if len(names) > 0 {
			out[id] = slices.Clone(names)
		}
	}
	return out
}
