// Package thunderstore: this file is the HOLD store (#436, T3 review F3 and
// F9) - when lmm has decided not to ask Thunderstore, or one community on
// it, again before a given time, and why.
//
// A hold is PERSISTED beside the community indexes, because "not asking
// Thunderstore again until 18:10" is a promise about the host, not about
// one process. A script looping over `lmm search`, or a user who simply
// runs the command again, starts a new process each time, and each one used
// to ask at once. Every request now reads the file first; a change is made
// under an advisory lock and published with a rename, so two lmm processes
// recording failures at the same moment keep both, and a reader never sees
// half a file.
//
// A hold has one of two scopes (review F9, decided):
//
//   - the HOST: a throttle (429) or a server failure (5xx) comes from the
//     host every community shares, so it holds all of them;
//   - one COMMUNITY: a 404, or a document that is not a package list or
//     will not parse, belongs to that community alone, and holding every
//     other one for it would be wrong.
//
// Either scope is a hold for one of two reasons. The host NAMED a wait
// (Retry-After past what lmm sits through), which stands until it expires -
// an answer from some other request in the meantime does not lift it. Or
// lmm tripped its own breaker after breakerThreshold failures in a row,
// which the next success lifts.
//
// The file is lmm's own and only ever advisory: one that is missing,
// unreadable or malformed holds nothing, and a hold that ends further ahead
// than lmm ever sets one is ignored rather than obeyed - a damaged file must
// not lock lmm out of Thunderstore.
//
// The store itself is httpclient.FileHolds (#675 lifted it from here, so
// NexusMods' holds persist the same way); this file is Thunderstore's
// scopes - the host and its communities - over it, in the file format and
// place it has always had.
package thunderstore

import (
	"context"
	"path/filepath"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

const (
	// holdsFileName is the persisted hold state, in the index root. The "."
	// prefix keeps it out of the community listing: no slug starts with one.
	// Its lock is .holds.lock, beside it.
	holdsFileName = ".holds.json"
	// holdsScopesField is the file's member for the communities' holds.
	holdsScopesField = "communities"
	// maxHold is the longest lmm ever holds off (review F8): a Retry-After
	// asking for more is honoured for this long. A day is far past any
	// throttle window a real host names, and short enough that a bogus
	// header cannot shut lmm out of Thunderstore for a year.
	maxHold = 24 * time.Hour
)

// hold is one scope's state.
type hold struct{ httpclient.HoldRecord }

// activeAt reports whether the hold is in force at now.
func (h hold) activeAt(now time.Time) bool { return h.ActiveAt(now) }

// holdFile is the file's content: the host's hold and each community's.
type holdFile struct {
	Host        hold
	Communities map[string]hold
}

// holdStore reads and changes the hold state. With no index root it keeps
// the state in memory, for the life of the process, which is all a source
// with nowhere to write can promise.
type holdStore struct{ store *httpclient.FileHolds }

func newHoldStore(root string, now func() time.Time) *holdStore {
	path := ""
	if root != "" {
		path = filepath.Join(root, holdsFileName)
	}
	return &holdStore{store: httpclient.NewFileHolds(httpclient.FileHoldsConfig{
		Path:        path,
		ScopesField: holdsScopesField,
		ValidScope:  communityPattern.MatchString,
		Now:         now,
		Threshold:   breakerThreshold,
		Cooldown:    breakerCooldown,
		MaxHold:     maxHold,
	})}
}

// active reports the hold in force for a request about community - the
// host's, or community's own, whichever ends later - and the scope it
// belongs to ("" for the host). community "" asks about the host alone.
func (h *holdStore) active(community string) (hold, string, bool) {
	rec, scope, ok := h.store.Active(community)
	return hold{rec}, scope, ok
}

// failed records one failed request for community's scope ("" for the
// host), and trips the hold once breakerThreshold have failed in a row. It
// returns the scope's hold afterwards.
func (h *holdStore) failed(community, why string) hold {
	return hold{h.store.Failed(community, why)}
}

// named records a wait the host asked for, never SHORTENING a hold already
// in force: a server that asked for ten minutes is not re-asked after a
// later, shorter one.
func (h *holdStore) named(community string, until time.Time, why string) hold {
	return hold{h.store.Named(community, until, why)}
}

// succeeded clears community's failure streak, and a hold lmm set itself:
// one good answer means the host - or the community - is back. A wait the
// host NAMED stands until it expires (review F9): an answer to a request
// that was already in flight says nothing about the time it asked for.
func (h *holdStore) succeeded(community string) { h.store.Succeeded(community) }

// Holds implements source.HoldReporter: every hold in force, host first,
// then communities by name (T3 review F10) - what a frontend shows without
// asking the host anything.
func (s *Source) Holds(context.Context) []source.Hold { return s.holds.list() }

// list reports every hold in force, host first, then communities by name.
func (h *holdStore) list() []source.Hold {
	var out []source.Hold
	for _, held := range h.store.List() {
		out = append(out, source.Hold{Source: serviceName, GameID: held.Scope, Until: held.Until, Reason: held.Reason})
	}
	return out
}

// readHoldFile reads the file at path as a store judging it at now would:
// sanitised, anything wrong with it reading as no holds at all.
func readHoldFile(path string, now time.Time) holdFile {
	store := newHoldStore(filepath.Dir(path), func() time.Time { return now })
	host, scopes := store.store.Snapshot()
	out := holdFile{Host: hold{host}}
	for name, rec := range scopes {
		if out.Communities == nil {
			out.Communities = map[string]hold{}
		}
		out.Communities[name] = hold{rec}
	}
	return out
}
