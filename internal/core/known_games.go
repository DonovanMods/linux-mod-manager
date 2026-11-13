package core

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// VerifyStatusCatalogUnknown is the verify row for a game whose `catalog:`
// names no entry of lmm's game catalog (#606) - a hand-edited typo, say. It
// is a note, counted towards neither tally: the key is then ignored, which
// is how the game behaved before it was set, so nothing is broken that was
// not before. `lmm game edit --catalog` refuses an unknown slug up front.
const VerifyStatusCatalogUnknown = "catalog_unknown"

// KnownGame is one entry of lmm's curated game catalog (#606): the games
// `lmm game detect` knows (internal/source/steam's steam-games.yaml, plus
// the user's own override), as much of each as core uses. A game's
// `catalog:` names one by Slug.
//
// It is also GET /api/v1/games/known's row, which the web UI's game editor
// offers as the choices for `catalog:`.
type KnownGame struct {
	// Slug is the entry's id, the value `catalog:` takes ("skyrim-se").
	Slug string `json:"slug"`
	// Name is the game's display name.
	Name string `json:"name"`
	// SteamAppID is the Steam app the entry describes.
	SteamAppID string `json:"steam_app_id,omitempty"`
	// Adapter is the adapter the catalog sets the game up with, or "".
	Adapter string `json:"adapter,omitempty"`
}

// SetKnownGames registers how this Service reads lmm's game catalog. The
// composition root calls it (internal/app), because the catalog lives in a
// concrete source core may not import. Until it does, KnownGames is nil,
// nothing is suggested, and `catalog:` cannot be set.
func (s *Service) SetKnownGames(list func() []KnownGame) {
	if list == nil {
		s.knownGames.Store(nil)
		return
	}
	s.knownGames.Store(&list)
}

// KnownGames returns lmm's game catalog sorted by name (then slug), or nil
// when none is registered.
func (s *Service) KnownGames() []KnownGame {
	p := s.knownGames.Load()
	if p == nil {
		return nil
	}
	out := slices.Clone((*p)())
	slices.SortFunc(out, func(a, b KnownGame) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.Slug, b.Slug))
	})
	return out
}

// knownGameBySlug finds the catalog entry slug names.
func (s *Service) knownGameBySlug(slug string) (KnownGame, bool) {
	for _, k := range s.KnownGames() {
		if k.Slug == slug {
			return k, true
		}
	}
	return KnownGame{}, false
}

// knownGameByAppID finds the catalog entry for a Steam app id.
func (s *Service) knownGameByAppID(appID string) (KnownGame, bool) {
	for _, k := range s.KnownGames() {
		if k.SteamAppID == appID {
			return k, true
		}
	}
	return KnownGame{}, false
}

// knownGameFor is the catalog entry game is: the one its `catalog:` names,
// else the one for its Steam app id (what games.yaml states, then the
// library scan). A `catalog:` naming nothing is ignored here and reported
// by verify.
func (s *Service) knownGameFor(game *domain.Game) (KnownGame, bool) {
	if game.Catalog != "" {
		if k, ok := s.knownGameBySlug(game.Catalog); ok {
			return k, true
		}
	}
	if appID := s.gameSteamAppID(game); appID != "" {
		return s.knownGameByAppID(appID)
	}
	return KnownGame{}, false
}

// knownSlugs lists the catalog's slugs, sorted, for a message.
func knownSlugs(games []KnownGame) string {
	slugs := make([]string, len(games))
	for i, k := range games {
		slugs[i] = k.Slug
	}
	slices.Sort(slugs)
	return strings.Join(slugs, ", ")
}

// catalogValue is the `catalog:` an edit writes: "" to clear it, or a slug
// the registered catalog has.
func (s *Service) catalogValue(value string) (string, error) {
	slug := strings.TrimSpace(value)
	if slug == "" {
		return "", nil
	}
	games := s.KnownGames()
	if games == nil {
		return "", newGameSpecError("catalog", slug, "lmm has no game catalog loaded to check it against")
	}
	if _, ok := s.knownGameBySlug(slug); !ok {
		return "", newGameSpecError("catalog", slug,
			fmt.Sprintf("%q is not in lmm's game catalog; known entries: %s", slug, knownSlugs(games)))
	}
	return slug, nil
}

// catalogPass adds the catalog_unknown note to a verify run.
func (r *verifyRun) catalogPass() {
	slug := r.game.Catalog
	games := r.svc.KnownGames()
	if slug == "" || games == nil {
		return
	}
	if _, ok := r.svc.knownGameBySlug(slug); ok {
		return
	}
	r.finding(VerifyFinding{
		Status: VerifyStatusCatalogUnknown,
		Note: fmt.Sprintf("games.yaml says this game is catalog entry %q, which lmm's game catalog does not have, so lmm ignores it; known entries: %s",
			slug, knownSlugs(games)),
		Recorded: slug,
		FixableReason: fmt.Sprintf("lmm never edits games.yaml by itself; to fix it, run: lmm game edit %s --catalog <entry> (or --catalog \"\" to remove it)",
			r.game.ID),
	}, VerifyEvent{Recorded: slug})
}
