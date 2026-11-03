package source

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// fetchedModObserverKey is the context key WithFetchedModObserver stores the
// observer under.
type fetchedModObserverKey struct{}

// FetchedModObserver receives a mod's fresh metadata as a source fetched it
// (see WithFetchedModObserver). It may be called from any goroutine, and
// must not keep mod past the call.
type FetchedModObserver func(mod domain.Mod)

// WithFetchedModObserver returns ctx carrying observe, which a source calls
// (through ReportFetchedMod) with each installed mod's fresh metadata that
// an update check fetched anyway - whether or not it offers an update for
// it (#592). The update check's result is only the updates, so this is how
// core learns of metadata it records on the installed row (the source's
// category name) without a fetch of its own.
//
// A context value rather than a ModSource method, for the reason WithGame
// gives: the interface stays the same for every source with nothing to
// report. A source need not report anything; core treats a mod it heard
// nothing about as unchanged. A nil observe stores nothing.
func WithFetchedModObserver(ctx context.Context, observe FetchedModObserver) context.Context {
	if observe == nil {
		return ctx
	}
	return context.WithValue(ctx, fetchedModObserverKey{}, observe)
}

// ReportFetchedMod hands mod to ctx's FetchedModObserver, if it has one. A
// source calls it from its update check with the mod as it just fetched it,
// ID and SourceID set as for GetMod.
func ReportFetchedMod(ctx context.Context, mod domain.Mod) {
	if observe, ok := ctx.Value(fetchedModObserverKey{}).(FetchedModObserver); ok {
		observe(mod)
	}
}
