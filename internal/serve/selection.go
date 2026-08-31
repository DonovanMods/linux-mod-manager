package serve

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// gameParam and profileParam are the two query params every game/profile-
// scoped page - and Task 5's mirroring GET /api/v1 endpoints - read to pick
// which game and profile they render (docs/plans/2026-08-30-serve-impl.md
// Task 4 ruling on game/profile selection). resolveSelection is the single
// place both a page and its API mirror resolve them, so the two always
// agree.
const (
	gameParam    = "game"
	profileParam = "profile"
)

// selection is resolveSelection's result: what a game/profile-scoped page
// (or its /api/v1 mirror) renders against, plus the nav switcher's choices.
// ready reports whether both a game and a profile resolved - the signal a
// page checks before calling a core query; otherwise it renders a friendly
// empty state offering Games/Profiles as the valid choices, with Warning
// explaining why an explicitly-named game or profile (or the configured
// default) could not be honoured.
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

// resolveSelection implements the shared "game"/"profile" query-param
// resolution (docs/plans/2026-08-30-serve-impl.md Task 4 ruling): "game"
// defaults to the configured default game (Service.DefaultGameInfo) when
// absent from the request, "profile" to that game's default profile
// (ProfileManager.GetDefault) when absent; either query param overrides its
// default when present. An unresolvable game or profile - an unknown value,
// no configured default, no games at all - degrades the result rather than
// failing the request: only a genuine core failure (e.g. ListGameEntries)
// returns a non-nil error.
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

	gameID := r.URL.Query().Get(gameParam)
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

	profileName := r.URL.Query().Get(profileParam)
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

// pageChrome is the layout-level data every page's template data struct
// embeds: the CSRF token layout.gohtml's forms need (Global Constraints:
// "CSRF token in every form"), and - for a page scoped to one game+profile -
// the nav switcher's resolved selection (Nav is nil for a page with no
// single active game+profile, e.g. the status dashboard). Path is the
// switcher form's GET target, so switching game/profile reloads the same
// page it was submitted from.
type pageChrome struct {
	Title     string
	CSRFToken string
	Path      string
	Nav       *selection
}

// chrome builds the pageChrome common to every page. sel is nil for a page
// with no single active game+profile (the status dashboard); every other
// page passes the selection resolveSelection returned so layout.gohtml can
// render the switcher and any resolution Warning.
func (s *Server) chrome(r *http.Request, title string, sel *selection) pageChrome {
	return pageChrome{Title: title, CSRFToken: s.csrf.token, Path: r.URL.Path, Nav: sel}
}
