// api_games.go is the Setup surface's game half
// (docs/plans/2026-08-31-serve-spa-design.md §Scope: "a real Setup
// surface: first-run flow in the game chooser, game add/detect UI"): the
// listing the game chooser renders, the catalog search behind the add
// form, the add itself, and the Steam detect scan in its two halves
// (listing, then apply).
//
// None of these routes is game-scoped - they are how a game comes to exist
// - so none resolves a ?game=/?profile= selection. Every response is a
// frozen core document: the same core.GameListEntry rows `lmm game list
// --json` emits, core.GameCatalogReport, core.GameDetectListing and
// core.GameDetectResult (Phase 3's wire rule).
//
// The two writes go through core's gated seams (AddGame, ApplyGameDetect),
// so adding a game cannot interleave with a deploy job running in the
// background on the server's own goroutine.
package serve

import (
	"errors"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// handleAPIGames answers GET /api/v1/games with the array `lmm game list
// --json` emits: every configured game ordered by ID, the default one
// marked. It is the game chooser's read - and, when empty, the first-run
// signal the SPA branches on.
func (s *Server) handleAPIGames(w http.ResponseWriter, r *http.Request) {
	games, err := s.svc.ListGameEntries(r.Context())
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, games)
}

// handleAPIGamesCatalog answers GET /api/v1/games/catalog?source=<id>&q=<query>
// with the core.GameCatalogReport `lmm game add --query --json` emits.
//
// Both parameters are required and both failures are the caller's (400): a
// source with no catalog (core.ErrNoGameCatalog) is not a missing thing -
// it exists and simply cannot be searched, which is exactly the signal the
// SPA needs to swap its search box for an identifier field. A source id
// that names nothing is 404, the treatment every other unknown-name route
// gives.
func (s *Server) handleAPIGamesCatalog(w http.ResponseWriter, r *http.Request) {
	sourceID := r.URL.Query().Get("source")
	if sourceID == "" {
		s.writeAPIError(w, http.StatusBadRequest, errors.New(`"source" is required`))
		return
	}

	report, err := s.svc.SearchGameCatalog(r.Context(), sourceID, r.URL.Query().Get("q"))
	if err != nil {
		s.writeAPIError(w, s.catalogErrorStatus(sourceID, err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}

// catalogErrorStatus classifies a SearchGameCatalog failure: an
// unregistered source is 404, a bad query or a source that cannot be
// searched is 400, and a source's own failure (auth, network) is 502 -
// this server is a proxy for that call and did not itself fail.
func (s *Server) catalogErrorStatus(sourceID string, err error) int {
	var specErr *core.GameSpecError
	switch {
	case errors.As(err, &specErr), errors.Is(err, core.ErrNoGameCatalog):
		return http.StatusBadRequest
	default:
		if _, lookupErr := s.svc.GetSource(sourceID); lookupErr != nil {
			return http.StatusNotFound
		}
		return http.StatusBadGateway
	}
}

// gameAddRequest is POST /api/v1/games' body: the same values `lmm game
// add`'s flags collect, named as core.GameSpec's wire fields so a
// core.GameSpecError's "field" member points straight at the offending
// input.
//
// GameID is the one member with no CLI flag of its own: it is the local
// games.yaml key, which the CLI derives from whichever of the identifier
// or the catalog match it has in hand. An SPA driving the catalog flow
// already holds the match, so it passes the game_id core suggested
// (GameCatalogMatch.GameID) - that is what keeps a CurseForge add keyed
// "minecraft" rather than "432". Omitted, core derives it from the
// identifier, which is the manual path's rule.
type gameAddRequest struct {
	SourceID    string `json:"source_id"`
	Identifier  string `json:"identifier"`
	Name        string `json:"name"`
	GameID      string `json:"game_id,omitempty"`
	InstallPath string `json:"install_path"`
	ModPath     string `json:"mod_path,omitempty"`
}

// spec converts the request into the core.GameSpec AddGame validates. No
// validation happens here on purpose: core owns every rule (which fields
// are required, whether the install path exists, what a legal id is), and
// a second copy in the HTTP layer would be a copy that drifts.
func (r *gameAddRequest) spec() core.GameSpec {
	return core.GameSpec{
		SourceID:    r.SourceID,
		Identifier:  r.Identifier,
		Name:        r.Name,
		ID:          r.GameID,
		InstallPath: r.InstallPath,
		ModPath:     r.ModPath,
	}
}

// handleAPIGameAdd answers POST /api/v1/games with the core.GameListEntry
// row the new game occupies in `lmm game list --json` - the same shape GET
// /api/v1/games returns, so the SPA splices the response straight into its
// list instead of re-reading.
//
// A rejected field is 400 carrying core.GameSpecError's {field, value,
// reason} in the envelope's details, so the form marks the offending input
// rather than showing a sentence beside nothing. An id already taken is
// 409 (core.ErrGameExists, detected inside the gate), matching how the
// profile routes classify the same collision.
func (s *Server) handleAPIGameAdd(w http.ResponseWriter, r *http.Request) {
	var req gameAddRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	entry, err := s.svc.AddGame(r.Context(), req.spec())
	if err != nil {
		s.writeAPIError(w, gameAddErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, entry)
}

// gameAddErrorStatus classifies an AddGame failure: a named field is the
// caller's input (400), a taken id is a collision with state the caller
// could not have known about (409), everything else is a real write
// failure (500).
func gameAddErrorStatus(err error) int {
	var specErr *core.GameSpecError
	switch {
	case errors.As(err, &specErr):
		return http.StatusBadRequest
	case errors.Is(err, core.ErrGameExists):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// handleAPIGamesDetect answers GET /api/v1/games/detect with the
// core.GameDetectListing document: every moddable Steam game the scan
// found, its 1-based index, whether games.yaml already holds it, and the
// scan's own warnings.
//
// This half has no CLI equivalent under --json - `lmm game detect` printed
// its listing to the terminal and emitted only the RESULT document, since
// Ruling 15 allows nothing else on stdout beside it. A browser selection
// happens between two requests, so the listing had to become a document
// (core.GameDetectListing, goldened with the rest).
func (s *Server) handleAPIGamesDetect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	detected, warnings, err := app.DetectGames(ctx, s.svc.ConfigDir())
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	listing, err := s.svc.GameDetectListing(ctx, detected, warnings)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, listing)
}

// gameDetectSelectRequest is POST /api/v1/games/detect's body: which of
// the listing's rows to add, each named by its 1-based index or its slug
// (core.SelectDetectedGames resolves both). Selecting an
// already-configured row is legal and overwrites it - that is `lmm game
// detect`'s documented repair path, and the listing's already_configured
// flag is what lets the SPA warn before offering it.
type gameDetectSelectRequest struct {
	Select []string `json:"select"`
}

// handleAPIGameDetectApply answers POST /api/v1/games/detect with the
// core.GameDetectResult document `lmm game detect --json` emits.
//
// It RE-RUNS the scan rather than trusting a client-supplied game list:
// the request names rows, and the rows themselves (install paths, source
// mappings) must come from the machine, never from the browser - a
// browser-supplied InstallPath would let a page choose what gets written
// into games.yaml. The scan's warnings lead the result's, the same order
// the CLI merges them in.
func (s *Server) handleAPIGameDetectApply(w http.ResponseWriter, r *http.Request) {
	var req gameDetectSelectRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	ctx := r.Context()
	detected, warnings, err := app.DetectGames(ctx, s.svc.ConfigDir())
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	selected, err := core.SelectDetectedGames(detected, req.Select)
	if err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	result, applyErr := s.svc.ApplyGameDetect(ctx, selected)
	result.Warnings = append(append([]string(nil), warnings...), result.Warnings...)
	if applyErr != nil {
		// ApplyGameDetect persists one game at a time and stops at the
		// first failure, so the partial result is real state the caller
		// must see - it travels in the envelope's details via
		// core.GameDetectPartialError, exactly as it does for the CLI.
		s.writeAPIError(w, http.StatusInternalServerError, &core.GameDetectPartialError{Err: applyErr, Result: result})
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}
