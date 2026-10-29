// game_version_types.go is the read behind picking a game's version types
// by name (#563): a source that sorts a game's files by version type
// (CurseForge's gameVersionTypeId, which #544's
// curseforge.game_version_types stores) lists them with its own names and
// slugs, so neither frontend asks a person for a raw id. games.yaml still
// stores ids; this only finds them.
package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// ErrNoGameVersionTypes is returned for a registered source that publishes
// no game version types (it implements no source.GameVersionTypeLister):
// every source but CurseForge today. A frontend falls back to taking ids.
var ErrNoGameVersionTypes = errors.New("source publishes no game version types")

// GameVersionType is one version type a source publishes for a game: the
// id a game's setting stores, with the source's name and slug for it.
type GameVersionType struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// GameVersionTypesReport is `lmm game version-types --json`'s document and
// GET /api/v1/sources/{id}/version-types' answer: the version types a
// source publishes for one game, sorted by name. Types is never nil.
type GameVersionTypesReport struct {
	GameID   string            `json:"game_id"`
	SourceID string            `json:"source_id"`
	Types    []GameVersionType `json:"types"`
}

// GameVersionTypes lists the version types sourceID publishes for gameID,
// sorted by name (then id). The game must map the source - its identifier
// there is what the source is asked with - so a game that does not is a
// *GameIdentifierError; a source with no such list is
// ErrNoGameVersionTypes; and the source's own failure (a missing key is
// domain.ErrAuthRequired) is returned wrapped but intact.
func (s *Service) GameVersionTypes(ctx context.Context, gameID, sourceID string) (*GameVersionTypesReport, error) {
	return s.gameVersionTypes(ctx, gameID, sourceID, nil)
}

// gameVersionTypes is GameVersionTypes asked with the source map sources in
// place of the game's saved one; nil means the saved one. It is how
// ResolveGameVersionTypes answers for an edit that changes the mapping in
// the same run.
func (s *Service) gameVersionTypes(ctx context.Context, gameID, sourceID string, sources map[string]string) (*GameVersionTypesReport, error) {
	src, err := s.GetSource(sourceID)
	if err != nil {
		return nil, fmt.Errorf("looking up source %s: %w", sourceID, err)
	}
	lister, ok := src.(source.GameVersionTypeLister)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoGameVersionTypes, sourceID)
	}
	game, ok := s.game(gameID)
	if !ok {
		return nil, fmt.Errorf("%w: %s", domain.ErrGameNotFound, gameID)
	}
	var identifier string
	if sources == nil {
		identifier, err = s.indexGameID(src, gameID)
	} else {
		identifier, err = editedGameIdentifier(src, game, sources)
	}
	if err != nil {
		return nil, err
	}

	rows, err := lister.ListGameVersionTypes(source.WithGame(ctx, game), identifier)
	if err != nil {
		return nil, fmt.Errorf("listing %s game version types for %s: %w", src.Name(), gameID, err)
	}
	report := &GameVersionTypesReport{GameID: gameID, SourceID: sourceID, Types: make([]GameVersionType, len(rows))}
	for i, r := range rows {
		report.Types[i] = GameVersionType{ID: r.ID, Name: r.Name, Slug: r.Slug}
	}
	slices.SortStableFunc(report.Types, func(a, b GameVersionType) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return report, nil
}

// GameVersionTypeReason says why a value named no single version type.
type GameVersionTypeReason string

const (
	// GameVersionTypeUnknown: no type's slug is the value.
	GameVersionTypeUnknown GameVersionTypeReason = "unknown"
	// GameVersionTypeAmbiguous: more than one type's slug is the value.
	GameVersionTypeAmbiguous GameVersionTypeReason = "ambiguous"
)

// GameVersionTypeError reports a version-type value that is neither an id
// nor exactly one type's slug. Types answers the question the user was
// asking: every valid type for an unknown value, or the types sharing the
// slug for an ambiguous one, each by name, slug and id.
//
// Details returns the error itself, so the --json envelope's "details" is
// {game_id, source_id, value, reason, types}.
type GameVersionTypeError struct {
	GameID   string                `json:"game_id"`
	SourceID string                `json:"source_id"`
	Value    string                `json:"value"`
	Reason   GameVersionTypeReason `json:"reason"`
	Types    []GameVersionType     `json:"types"`

	// sourceName is the source's display name, for the sentence.
	sourceName string
}

// Error names the value and lists Types, one per line.
func (e *GameVersionTypeError) Error() string {
	var b strings.Builder
	if e.Reason == GameVersionTypeAmbiguous {
		fmt.Fprintf(&b, "%q is the slug of more than one %s game version type for %s; give the one you mean by id:", e.Value, e.sourceName, e.GameID)
	} else {
		fmt.Fprintf(&b, "%q is not a %s game version type for %s; give one of these by slug or id:", e.Value, e.sourceName, e.GameID)
	}
	if len(e.Types) == 0 {
		b.WriteString(" (it publishes none)")
	}
	for _, t := range e.Types {
		fmt.Fprintf(&b, "\n  %s (slug %s, id %d)", t.Name, t.Slug, t.ID)
	}
	return b.String()
}

// Details implements the --json error envelope's extension point.
func (e *GameVersionTypeError) Details() any { return e }

// ResolveGameVersionTypes turns version-type values - each an id or a
// type's slug - into ids, in order. sources is the source map the game
// will have once the caller's edit is saved - an edit that adds, re-points
// or removes sourceID resolves its slugs against that, not the saved
// mapping - and nil means the game's saved map. A value of digits is an id as it stands
// and asks nothing of the source, so ids alone work offline and for a game
// that does not map it; whether an id is valid is EditGame's question, as
// it always was. A slug is matched exactly (case aside) against
// GameVersionTypes' list, fetched once: no match, or more than one, is a
// *GameVersionTypeError, and a failure to fetch the list is returned
// naming the slug that needed it.
func (s *Service) ResolveGameVersionTypes(ctx context.Context, gameID, sourceID string, sources map[string]string, values []string) ([]int, error) {
	var report *GameVersionTypesReport
	ids := make([]int, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if id, err := strconv.Atoi(v); err == nil {
			ids = append(ids, id)
			continue
		}
		if report == nil {
			var err error
			if report, err = s.gameVersionTypes(ctx, gameID, sourceID, sources); err != nil {
				return nil, fmt.Errorf("looking up game version type %q: %w", v, err)
			}
		}
		var matches []GameVersionType
		for _, t := range report.Types {
			if strings.EqualFold(t.Slug, v) {
				matches = append(matches, t)
			}
		}
		if len(matches) == 1 {
			ids = append(ids, matches[0].ID)
			continue
		}
		typeErr := &GameVersionTypeError{
			GameID: gameID, SourceID: sourceID, Value: v,
			Reason: GameVersionTypeUnknown, Types: report.Types,
		}
		if len(matches) > 1 {
			typeErr.Reason, typeErr.Types = GameVersionTypeAmbiguous, matches
		}
		if src, err := s.GetSource(sourceID); err == nil {
			typeErr.sourceName = src.Name()
		}
		return nil, typeErr
	}
	return ids, nil
}

// editedGameIdentifier is indexGameID for a game whose source map an edit
// is about to replace with sources: the identifier src will know the game
// by once it is saved. An edit that drops a mapping the game has is refused
// in its own words, since the saved mapping it would otherwise name is the
// one being removed.
func editedGameIdentifier(src source.ModSource, game *domain.Game, sources map[string]string) (string, error) {
	sourceID := src.ID()
	if _, mapped := sources[sourceID]; !mapped {
		reason := fmt.Sprintf("game %q does not use source %q; add it with 'lmm game edit %s --source %s=<identifier>'",
			game.ID, sourceID, game.ID, sourceID)
		if _, had := game.SourceIDs[sourceID]; had {
			reason = fmt.Sprintf("this edit removes source %q from game %q, so there is no %s game to look a version type up in; keep the mapping, or give the version types by id",
				sourceID, game.ID, src.Name())
		}
		return "", &GameIdentifierError{
			GameID: game.ID, Source: sourceID,
			Err: fmt.Errorf("%s: %w", reason, source.ErrGameIdentifierInvalid),
		}
	}
	edited := *game
	edited.SourceIDs = sources
	return gameSourceID(src, &edited)
}
