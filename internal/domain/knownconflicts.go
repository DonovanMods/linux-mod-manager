package domain

import "slices"

// Known conflicts (#668): a profile's record that two mods' file conflict
// is intended. The record is a directed mod pair, "Winner overrides Loser",
// and covers every path the two share - including a path they come to
// share after an update - the way a before/after rule between two mods
// resolves their conflict in Vortex. It names the winner, so a reorder that
// makes the loser win again no longer matches it, and the conflict is
// reported again: what was intended no longer holds.

// KnownConflictOrigin says who marked a known conflict.
type KnownConflictOrigin string

const (
	// KnownConflictUser is a pair the user marked (`lmm conflicts ack`, the
	// web UI's "Mark as intended").
	KnownConflictUser KnownConflictOrigin = "user"
	// KnownConflictCollection is a pair a collection marked: a conflict
	// between two of the collection's own mods, under the collection's
	// order. A collection update recomputes these and keeps the user's.
	KnownConflictCollection KnownConflictOrigin = "collection"
)

// KnownConflict is one directed pair: Winner's files override Loser's,
// and that is intended. Winner and Loser are domain.ModKey
// ("sourceID:modID").
type KnownConflict struct {
	Winner string              `yaml:"winner" json:"winner"`
	Loser  string              `yaml:"loser" json:"loser"`
	Origin KnownConflictOrigin `yaml:"origin" json:"origin"`
}

// KnownConflicts is a profile's `known_conflicts:` list. One pair of mods
// has at most one entry, in one direction.
type KnownConflicts []KnownConflict

// Has reports whether k marks winner over loser as known, in that
// direction.
func (k KnownConflicts) Has(winner, loser string) bool {
	for _, e := range k {
		if e.Winner == winner && e.Loser == loser {
			return true
		}
	}
	return false
}

// Index is the position of the entry for the pair a, b in either
// direction, or -1 when k has none.
func (k KnownConflicts) Index(a, b string) int {
	for i, e := range k {
		if (e.Winner == a && e.Loser == b) || (e.Winner == b && e.Loser == a) {
			return i
		}
	}
	return -1
}

// Clone is a copy of k; nil when k is empty.
func (k KnownConflicts) Clone() KnownConflicts {
	if len(k) == 0 {
		return nil
	}
	return slices.Clone(k)
}

// Equal reports whether k and other hold the same entries in the same
// order, an empty list counting as none.
func (k KnownConflicts) Equal(other KnownConflicts) bool {
	return slices.Equal(k, other)
}

// Keep is k without the entries naming a mod outside keys (domain.ModKey);
// nil when none is left.
func (k KnownConflicts) Keep(keys map[string]bool) KnownConflicts {
	var out KnownConflicts
	for _, e := range k {
		if keys[e.Winner] && keys[e.Loser] {
			out = append(out, e)
		}
	}
	return out
}

// Rename is a copy of k in which every entry naming the mod from names to
// instead: a re-link moves the mod's marks to its new identity. Nil when k
// is empty.
func (k KnownConflicts) Rename(from, to string) KnownConflicts {
	out := k.Clone()
	for i := range out {
		if out[i].Winner == from {
			out[i].Winner = to
		}
		if out[i].Loser == from {
			out[i].Loser = to
		}
	}
	return out
}

// KnownConflictSet indexes a KnownConflicts list for lookups: one map read
// per question instead of a scan of the list, which a collection's
// thousands of pairs make worth building once per query.
type KnownConflictSet map[[2]string]bool

// Set is k indexed, each entry by its (winner, loser) direction.
func (k KnownConflicts) Set() KnownConflictSet {
	set := make(KnownConflictSet, len(k))
	for _, e := range k {
		set[[2]string{e.Winner, e.Loser}] = true
	}
	return set
}

// Has reports whether the set marks winner over loser, in that direction.
func (s KnownConflictSet) Has(winner, loser string) bool {
	return s[[2]string{winner, loser}]
}

// HasPair reports whether the set holds the pair a, b in either direction.
func (s KnownConflictSet) HasPair(a, b string) bool {
	return s[[2]string{a, b}] || s[[2]string{b, a}]
}

// Add records winner over loser.
func (s KnownConflictSet) Add(winner, loser string) {
	s[[2]string{winner, loser}] = true
}

// KeepKnownConflicts drops every known-conflict entry, the profile's and
// its collection baseline's, that names a mod the profile no longer lists
// (#668): a removed mod takes its pairs with it. Every save of a profile
// runs it, so no flow that removes a mod has to remember to.
func (p *Profile) KeepKnownConflicts() {
	if p == nil || (len(p.KnownConflicts) == 0 && (p.CollectionBaseline == nil || len(p.CollectionBaseline.KnownConflicts) == 0)) {
		return
	}
	keys := make(map[string]bool, len(p.Mods))
	for _, ref := range p.Mods {
		keys[ModKey(ref.SourceID, ref.ModID)] = true
	}
	p.KnownConflicts = p.KnownConflicts.Keep(keys)
	if p.CollectionBaseline != nil {
		p.CollectionBaseline.KnownConflicts = p.CollectionBaseline.KnownConflicts.Keep(keys)
	}
}
