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
	"fmt"
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
// entry - a footgun, not a use case. An empty IDENTIFIER is refused for
// any source that says it needs one, and accepted for the sources that say
// they do not (a directory source keyed by nothing else is exactly how the
// fixtures and several custom sources are configured); either way it is
// trimmed, like the id.
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

	if err := s.refuseRemovingReferencedSources(ctx, game, cleaned); err != nil {
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

// SetGameAdapter rewrites gameID's `adapter:` key and returns the game's
// own `lmm game list --json` row, re-read after the write (#353).
//
// It is UpdateGameSources' sibling in every respect - a settings-class
// single-step write, gated for the same reason, with every check inside
// the gate - and it is the ONLY way a frontend changes the key, so the
// rules below cannot be bypassed by one of them.
//
// An EMPTY name clears the key, which is how a user returns a game to the
// generic-files identity. Any other name must be registered: an
// unregistered one is a GameSpecError on field "adapter", so an SPA marks
// the input rather than parsing a sentence, and the message names what IS
// registered. A `deploy_mode: compile` game may only be given an adapter
// that can compile - the one composition rule the two keys have (design
// §2) - refused by name for the same reason.
func (s *Service) SetGameAdapter(ctx context.Context, gameID, name string) (*GameListEntry, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	game, ok := s.game(gameID)
	if !ok {
		return nil, domain.ErrGameNotFound
	}

	// A COPY, for the reason UpdateGameSources documents: s.game returns
	// the pointer concurrent readers are walking right now.
	updated := *game
	updated.Adapter = strings.TrimSpace(name)

	if _, err := s.AdapterFor(&updated); err != nil {
		return nil, &GameSpecError{
			Field: "adapter", Value: updated.Adapter,
			Reason: err.Error(), Err: err,
		}
	}

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
		// M6 (epic review M-6): two distinct keys that trim to the same id
		// - {" nexusmods":"a","nexusmods":"b"}, say - are legal JSON but not
		// a legal source map: silently keeping whichever sorts last hides
		// which value the caller actually gets, and does not tell them
		// their input had a collision at all.
		if _, dup := cleaned[trimmed]; dup {
			return nil, newGameSpecError("sources", trimmed, "duplicate source id after trimming whitespace")
		}
		identifier := strings.TrimSpace(sources[id])
		// T1 review #3: the same question AddGame asks, on the only other
		// way into games.yaml. A source that does not declare its mapped
		// value meaningless REQUIRES one, and writing an empty mapping for
		// it produces a game that fails at first use - or worse, for a
		// source that keeps a local index, one that quietly indexes a
		// community the user never named.
		if identifier == "" && !s.sourceIgnoresGameIdentifier(trimmed) {
			return nil, newGameSpecError("sources", trimmed, "this source needs an identifier for the game")
		}
		cleaned[trimmed] = identifier
	}
	return cleaned, nil
}

// refuseRemovingReferencedSources refuses to drop a source id from game's
// map while any of game's profiles still has an installed mod that came
// from it (#326 fix wave, epic review M-4). Silently dropping the mapping
// left those mods degrading quietly: update checks skip them, a re-link to
// the source is refused ("source %q is not configured for %s",
// mod_edit.go), and archive imports warn the source isn't configured for
// the game. `lmm source remove` already refuses the mirror case
// (SourceInUseError, a GAME still mapping a source about to be
// unregistered entirely); this is the same rule one level down, for
// installed MODS a game's own edit would otherwise orphan.
//
// Only ids present in game.SourceIDs but absent from cleaned are checked -
// an id that stays mapped, or one newly added, references nothing to
// orphan. Sorted so a removal dropping two still-referenced sources always
// names the same one first, the same determinism validatedSourceMap's own
// field selection gives the caller.
func (s *Service) refuseRemovingReferencedSources(ctx context.Context, game *domain.Game, cleaned map[string]string) error {
	for _, sourceID := range slices.Sorted(maps.Keys(game.SourceIDs)) {
		if _, kept := cleaned[sourceID]; kept {
			continue
		}
		mods, err := s.modsReferencingSource(ctx, game.ID, sourceID)
		if err != nil {
			return err
		}
		if len(mods) > 0 {
			return &GameSourceInUseError{SourceID: sourceID, GameID: game.ID, Count: len(mods), Mods: mods}
		}
	}
	return nil
}

// modsReferencingSource returns every distinct "source:mod" key
// (domain.ModKey) across every profile of gameID whose installed row
// names sourceID, sorted - the population refuseRemovingReferencedSources
// refuses to orphan. A mod installed in more than one profile counts once.
func (s *Service) modsReferencingSource(ctx context.Context, gameID, sourceID string) ([]string, error) {
	profileNames, err := s.NewProfileManager().ListNames(ctx, gameID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}

	seen := map[string]bool{}
	for _, profileName := range profileNames {
		installed, err := s.GetInstalledMods(ctx, gameID, profileName)
		if err != nil {
			return nil, fmt.Errorf("getting installed mods: %w", err)
		}
		for _, im := range installed {
			if im.SourceID == sourceID {
				seen[domain.ModKey(im.SourceID, im.ID)] = true
			}
		}
	}

	keys := slices.Sorted(maps.Keys(seen))
	return keys, nil
}

// GameSourceInUseError refuses UpdateGameSources' removal of a source id
// because at least one profile of GameID still has an installed mod that
// came from it. Mods names them (ModKey form, "source:mod-id"), so a
// frontend can say WHICH mods to uninstall rather than "some mod,
// somewhere".
type GameSourceInUseError struct {
	SourceID string   `json:"source_id"`
	GameID   string   `json:"game_id"`
	Count    int      `json:"count"`
	Mods     []string `json:"mods"`
}

// Error implements error.
func (e *GameSourceInUseError) Error() string {
	return fmt.Sprintf("%d installed mod(s) still come from %q; uninstall them first", e.Count, e.SourceID)
}

// Details returns the error itself for the --json error envelope's
// "details" field (Ruling 3), the same shape SourceInUseError uses.
func (e *GameSourceInUseError) Details() any { return e }
