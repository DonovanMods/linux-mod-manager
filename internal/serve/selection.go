package serve

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// gameParam and profileParam are the two query params every game/profile-
// scoped /api/v1 endpoint reads to pick which game and profile it answers
// for (docs/plans/2026-08-30-serve-impl.md Task 4 ruling on game/profile
// selection). The SPA holds the same pair in its URL path
// (/g/{game}/{profile}, docs/plans/2026-08-31-serve-spa-design.md
// §Information architecture) and passes them down as these params, so the
// browser and a script driving /api/v1 by hand resolve identically.
const (
	gameParam    = "game"
	profileParam = "profile"
)

// selection is resolveSelection's result: what a game/profile-scoped
// /api/v1 endpoint answers against, plus the choices a caller may switch
// between. ready reports whether both a game and a profile resolved - the
// signal an endpoint checks before calling a core query; otherwise it
// answers the valid Games/Profiles instead, with Warning explaining why an
// explicitly-named game or profile (or the configured default) could not be
// honoured.
type selection struct {
	Game     *domain.Game
	Profile  string
	Games    []core.GameListEntry
	Profiles []string
	Warning  string
}

// ready reports whether sel resolved both a game and a profile.
func (sel selection) ready() bool {
	return sel.Game != nil && sel.Profile != ""
}

// resolveSelection implements the shared "game"/"profile" parameter
// resolution (docs/plans/2026-08-30-serve-impl.md Task 4 ruling): "game"
// defaults to the configured default game (Service.DefaultGameInfo) when
// absent from the request, "profile" to that game's default profile
// (ProfileManager.GetDefault) when absent; either parameter overrides its
// default when present.
//
// Both are read with http.Request.FormValue, which takes them from the
// query string on a GET and from a urlencoded body on a POST - one
// resolution for the read endpoints and the plan endpoint alike, rather
// than a second, drifting copy for the mutation routes.
//
// An unresolvable game or profile - an unknown value, no configured
// default, no games at all - degrades the result rather than failing the
// request: only a genuine core failure (e.g. ListGameEntries) returns a
// non-nil error.
func (s *Server) resolveSelection(r *http.Request) (selection, error) {
	ctx := r.Context()
	var sel selection

	entries, err := s.svc.ListGameEntries(ctx)
	if err != nil {
		return sel, err
	}
	sel.Games = entries
	if len(entries) == 0 {
		return sel, nil
	}

	gameID := r.FormValue(gameParam)
	if gameID == "" {
		def, err := s.svc.DefaultGameInfo(ctx)
		if err != nil {
			return sel, err
		}
		if !def.Set {
			sel.Warning = "no default game is configured"
			return sel, nil
		}
		gameID = def.ID
	}

	game, err := s.svc.GetGame(gameID)
	if err != nil {
		sel.Warning = fmt.Sprintf("unknown game %q", gameID)
		return sel, nil
	}
	sel.Game = game

	listing, err := s.svc.ListProfiles(ctx, game.ID)
	if err != nil {
		return sel, err
	}
	for _, p := range listing.Profiles {
		sel.Profiles = append(sel.Profiles, p.Name)
	}

	profileName := r.FormValue(profileParam)
	if profileName == "" {
		active, err := s.svc.NewProfileManager().GetDefault(ctx, game.ID)
		if err != nil {
			sel.Warning = fmt.Sprintf("%s has no active profile", game.Name)
			return sel, nil
		}
		profileName = active.Name
	} else if !slices.Contains(sel.Profiles, profileName) {
		sel.Warning = fmt.Sprintf("unknown profile %q", profileName)
		return sel, nil
	}
	sel.Profile = profileName

	return sel, nil
}
