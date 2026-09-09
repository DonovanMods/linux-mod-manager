// game_edit.go is the game-EDIT flow `lmm game edit` and `PUT
// /api/v1/games/{id}` share (#326): rewriting which sources a configured
// game maps, the one part of games.yaml neither frontend could reach
// before - a custom source created in the Setup editor sat at "In use: -"
// forever unless the user stopped the server and hand-edited the file
// (epic live review, C-4).
//
// It is a settings-class single-step write, not a Plan/Apply pair: there
// is nothing to preview beyond the map itself and nothing that can go
// stale between a preview and a commit, exactly the shape mod_settings.go
// documents for lock/policy/convert.
package core

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// UpdateGameSources replaces gameID's source map with sources and returns
// the game's own `lmm game list --json` row (GameListEntry), re-read after
// the write, so a caller never needs a follow-up request to see what it
// changed.
//
// REPLACE, not merge: the map handed over is the map the game ends up
// with. A frontend editing one entry sends the whole map back, which is
// what makes "remove this source" expressible at all - `lmm game edit`'s
// --source/--remove-source flags are a delta the CLI resolves against the
// current map before calling this.
//
// Gated (beginOp) for AddGame's reason: it is a games.yaml write that must
// not interleave with a deploy job running on `lmm serve`'s own goroutine.
// Every check happens INSIDE the gate - the game must still exist, and
// every source id must still be registered - so a source unregistered
// between the check and the write cannot be mapped anyway
// (UnregisterSourceIfUnused takes the same gate).
//
// Rejections are GameSpecError with Field "sources", the wire key PUT
// /api/v1/games/{id} takes, so an SPA marks the offending input rather
// than parsing an English sentence; Value names the offending source id.
// An unknown game is domain.ErrGameNotFound, the 404 every other
// game-scoped route already answers with.
//
// An EMPTY map is refused: a game that maps no source can neither search
// nor install, and the only way to reach that state is to remove the last
// entry - a footgun, not a use case. An empty IDENTIFIER is fine (a
// directory source keyed by nothing else is exactly how the fixtures and
// several custom sources are configured); it is trimmed, like the id.
func (s *Service) UpdateGameSources(ctx context.Context, gameID string, sources map[string]string) (*GameListEntry, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	game, ok := s.game(gameID)
	if !ok {
		return nil, domain.ErrGameNotFound
	}

	cleaned, err := s.validatedSourceMap(sources)
	if err != nil {
		return nil, err
	}

	// A COPY: s.game returns the pointer the in-memory set holds, which
	// concurrent readers (GetGame, ListGames, SourcesForGame) are walking
	// right now. Mutating its map in place would be a data race no lock
	// here could close; saveGame publishes the replacement atomically.
	updated := *game
	updated.SourceIDs = cleaned
	if err := s.saveGame(ctx, &updated); err != nil {
		return nil, err
	}

	defaultGame, err := s.DefaultGame(ctx)
	if err != nil {
		return nil, err
	}
	entry := newGameListEntry(&updated, defaultGame)
	return &entry, nil
}

// validatedSourceMap trims and checks every entry of a proposed source
// map, returning the map to persist. Each id must be non-empty and must
// name a source registered with this Service - the same check AddGame
// runs inside its own gate, for the same reason: a game mapping a source
// that does not exist is unusable, and nothing later would say why.
func (s *Service) validatedSourceMap(sources map[string]string) (map[string]string, error) {
	if len(sources) == 0 {
		return nil, newGameSpecError("sources", "", "a game must map at least one source")
	}

	cleaned := make(map[string]string, len(sources))
	// Sorted so a map with two bad ids always reports the same one - an
	// error message that changes between identical requests is untestable
	// and unexplainable.
	for _, id := range slices.Sorted(maps.Keys(sources)) {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			return nil, newGameSpecError("sources", id, "a source id is required")
		}
		if _, err := s.GetSource(trimmed); err != nil {
			return nil, &GameSpecError{
				Field: "sources", Value: trimmed,
				Reason: "no source is registered with that id", Err: err,
			}
		}
		cleaned[trimmed] = strings.TrimSpace(sources[id])
	}
	return cleaned, nil
}
