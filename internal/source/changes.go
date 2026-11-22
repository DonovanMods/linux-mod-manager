package source

import (
	"context"
	"time"
)

// ChangeTracker is implemented by a source that can say, more cheaply than
// checking a mod, whether the mod has changed since a given time (#672):
// NexusMods' updated-mods listing answers a whole game in one request. Core
// uses it to answer an update check or a verify from what it recorded the
// last time it checked a mod, rather than fetching the mod's file list
// again. Optional, and type-asserted like every other capability here; a
// source without it is checked in full every time.
//
// The answer is a cache's: a mod it wrongly reports CHANGED only costs a
// fetch, so a source must never report a mod unchanged that it cannot
// vouch for.
type ChangeTracker interface {
	// UnchangedSince reports which of the mods in since - keyed by mod
	// ID, each valued by when lmm last confirmed it - have not changed
	// since then. gameID is the source's own game identifier. The answer
	// maps each unchanged mod's ID to the time the source vouches for it
	// up to, which core records as its new confirmation time; a mod
	// missing from the answer is checked in full. An error means nothing
	// could be answered, and costs the same full check.
	UnchangedSince(ctx context.Context, gameID string, since map[string]time.Time) (map[string]time.Time, error)
}

// modCheckedObserverKey is the context key WithModCheckedObserver stores
// the observer under.
type modCheckedObserverKey struct{}

// ModCheckedObserver receives the ID of each mod a source's update check
// checked in full and without error (see WithModCheckedObserver). It may be
// called from any goroutine.
type ModCheckedObserver func(modID string)

// WithModCheckedObserver returns ctx carrying observe, which a source calls
// (through ReportModChecked) for each mod its update check finished without
// error (#672). An update check's result is only the updates and one joined
// error, so this is how core learns which mods' answers it may record -
// "up to date", or the update found - for a ChangeTracker to vouch for
// later. A source that reports nothing simply has nothing recorded. A nil
// observe stores nothing.
func WithModCheckedObserver(ctx context.Context, observe ModCheckedObserver) context.Context {
	if observe == nil {
		return ctx
	}
	return context.WithValue(ctx, modCheckedObserverKey{}, observe)
}

// ReportModChecked tells ctx's ModCheckedObserver, if it has one, that the
// update check finished modID without error: its update, if any, is in the
// check's result, and its absence there means it is up to date.
func ReportModChecked(ctx context.Context, modID string) {
	if observe, ok := ctx.Value(modCheckedObserverKey{}).(ModCheckedObserver); ok {
		observe(modID)
	}
}
