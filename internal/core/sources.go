// Package core: this file holds the registry-MUTATION half of the source
// surface - ReplaceSource/UnregisterSource - and the game/source
// cross-reference the two frontends refuse a source removal on
// (GamesUsingSource, SourceInUseError).
//
// Registration itself (RegisterSource/GetSource/ListSources) stays on
// service.go with the rest of the facade's construction-time API: those are
// what app.Open calls once, at startup, before any request exists. What is
// here is the RUNNING-process case #333 introduced - the web UI's custom
// source editor saving a definition, and a credential saved through `POST
// /api/v1/auth/{source}` re-keying an already-registered source - where a
// swap has to be serialized against whatever mutation might be in flight.
//
// Which is the whole reason these two are not just thin passes to
// source.Registry: the registry's own lock makes the map write atomic, but
// a mutation that resolved a source a moment ago is still holding (and
// using) the OLD value. Taking beginOp means a swap can only land between
// mutations, never inside one - so an install cannot download from one
// source object and record its identity against another.
package core

import (
	"context"
	"fmt"
	"sort"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// ReplaceSource swaps src in for whatever is currently registered under
// src.ID(), reporting whether it displaced an existing registration.
//
// It is a MUTATION in this Service's sense - it takes the single mutation
// slot - even though it writes nothing to disk: see the file comment for
// why serializing against in-flight mutations is the point. A caller whose
// ctx is done before the slot frees gets that ctx's error and the registry
// is left untouched.
func (s *Service) ReplaceSource(ctx context.Context, src source.ModSource) (bool, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	return s.registry.Replace(src), nil
}

// UnregisterSource removes the source registered under id, reporting
// whether one was there to remove. Removing an id nobody registered is not
// an error (source.Registry.Unregister's doc comment says why). Gated by
// the mutation slot for the same reason ReplaceSource is.
func (s *Service) UnregisterSource(ctx context.Context, id string) (bool, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	return s.registry.Unregister(id), nil
}

// GamesUsingSource returns the ids of configured games whose SourceIDs map
// sourceID, sorted. It reads the in-memory games snapshot, exactly as
// SourcesForGame does, and answers "" for no games at all with an empty
// slice rather than an error.
//
// It exists so neither frontend has to walk games.yaml itself to answer
// "may I delete this source definition?". A source is not referenced by any
// mod row - a game's SourceIDs map is the only place its id is written
// down - so this is the complete answer, and it is the list
// SourceInUseError puts in front of the user.
func (s *Service) GamesUsingSource(sourceID string) []string {
	return s.gamesUsingSource(sourceID)
}

// gamesUsingSource is GamesUsingSource's unexported implementation, shared
// with UnregisterSourceIfUnused so the gated re-check runs the exact same
// answer the ungated query does.
func (s *Service) gamesUsingSource(sourceID string) []string {
	var ids []string
	for _, g := range s.gamesSnapshot() {
		if _, mapped := g.SourceIDs[sourceID]; mapped {
			ids = append(ids, g.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// unregisterSourceIfUnusedBeforeCheck, when non-nil, runs once inside
// UnregisterSourceIfUnused after the gate is acquired but strictly before
// the in-use check - a test-only synchronization point (#333 fix-wave N1)
// letting a test force a specific interleaving against a concurrent
// mutator (e.g. AddGame) instead of hoping -race scheduling produces one,
// the way the probabilistic race test it replaced had to. Production
// leaves this nil, so it costs the hot path one nil check.
var unregisterSourceIfUnusedBeforeCheck func()

// UnregisterSourceIfUnused removes the source registered under id, refusing
// with *SourceInUseError when a configured game still maps it - id
// unchanged either way. The check and the removal run under ONE beginOp,
// closing the TOCTOU app.DeleteSourceDefinition's separate
// GamesUsingSource-then-UnregisterSource calls left open (#333 Minor #1):
// a POST /api/v1/games mapping this source could land in the gap between
// an ungated check and a later-gated unregister. Re-checking INSIDE the
// same gate AddGame's write takes means the two can no longer interleave -
// whichever caller's beginOp wins the slot decides the outcome for both.
func (s *Service) UnregisterSourceIfUnused(ctx context.Context, id string) (bool, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return false, err
	}
	defer release()

	if unregisterSourceIfUnusedBeforeCheck != nil {
		unregisterSourceIfUnusedBeforeCheck()
	}

	if games := s.gamesUsingSource(id); len(games) > 0 {
		return false, &SourceInUseError{SourceID: id, Games: games}
	}
	return s.registry.Unregister(id), nil
}

// SourceInUseError refuses a source removal because configured games still
// map the source. Games names them, so a frontend can say WHICH games to
// fix rather than "it is in use somewhere".
//
// This is a new refusal, not a codification of an old one: before #333 the
// only way to remove a custom source was to delete its YAML file by hand,
// which nothing checked - the games mapping it silently lost the source at
// the next start (SourcesForGame skips an unregistered id without a word,
// and a mod installed from it kept a row naming a source that no longer
// exists). Making the removal a command means making the check, and the
// check refuses rather than warns because the alternative leaves a game
// referencing a source that cannot be resolved.
type SourceInUseError struct {
	SourceID string   `json:"source_id"`
	Games    []string `json:"games"`
}

// Error implements error.
func (e *SourceInUseError) Error() string {
	return fmt.Sprintf("source %q is configured for %d game(s): %v", e.SourceID, len(e.Games), e.Games)
}

// Details returns the error itself for the --json error envelope's
// "details" field, so a caller reads source_id/games as data instead of
// parsing the message (Ruling 3, the same shape ConflictError uses).
func (e *SourceInUseError) Details() any { return e }
