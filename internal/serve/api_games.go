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
// The two writes go through core's gated seams (AddGame,
// ApplyDetectSelection),
// so adding a game cannot interleave with a deploy job running in the
// background on the server's own goroutine.
package serve

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
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
// searched is 400, a source that refused for want of a credential is 401,
// and any other failure of the source's own call is 502 - this server is a
// proxy for that call and did not itself fail.
//
// The 401 is #333's split of what used to be one 502. Every reason a
// catalog call can fail looked alike on the wire, so a CurseForge search
// run before `auth login` reached the SPA as a generic upstream error with
// no next step in it. domain.ErrAuthRequired survives core's wrapping, so
// errors.Is can tell that one case apart and the SPA can offer the thing
// that actually fixes it - authenticate this source - the same way the
// CLI's authPromptError does. The message is core's own, which already
// names the source.
func (s *Server) catalogErrorStatus(sourceID string, err error) int {
	var specErr *core.GameSpecError
	switch {
	case errors.As(err, &specErr), errors.Is(err, core.ErrNoGameCatalog):
		return http.StatusBadRequest
	case errors.Is(err, domain.ErrAuthRequired):
		return http.StatusUnauthorized
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
//
// FromSteamAppID is #206's prefill: the app id of an installed Steam game
// (GET /api/v1/games/detect?all=1's steam_app_id). With it, core fills
// every field the detection knows - name, install path, game id, mod path,
// and a curated game's source map - and the members above become
// OVERRIDES, each winning only where it is non-empty. The SPA therefore
// never re-derives a slug, a mod path or a source map of its own: a known
// game can be added with this member alone, and an unknown one needs only
// the source pair on top.
type gameAddRequest struct {
	SourceID       string `json:"source_id"`
	Identifier     string `json:"identifier"`
	Name           string `json:"name"`
	GameID         string `json:"game_id,omitempty"`
	InstallPath    string `json:"install_path"`
	ModPath        string `json:"mod_path,omitempty"`
	FromSteamAppID string `json:"from_steam_app_id,omitempty"`
	// Loader is #359's mod-loader declaration, additive and optional: the
	// same four values `lmm game add --loader ...` collects, unparsed, so a
	// core.GameSpecError's "field" member points at "loader.runtime" and the
	// form marks that select. Omitted - which is nearly every game - the
	// game declares no loader.
	Loader *core.LoaderSpec `json:"loader,omitempty"`
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
		Loader:      r.Loader,
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

	ctx := r.Context()
	spec := req.spec()
	if req.FromSteamAppID != "" {
		// Re-scans rather than trusting a client-supplied row, for the same
		// reason POST /api/v1/games/detect does: the request names an app
		// id, and the install path and source map behind it must come from
		// the machine. core.Service.PrefillGameSpecFromDetected then applies
		// exactly the prefill `lmm game add --from-detected` applies,
		// duplicate-install-path rule included (#406 review F1).
		detected, _, err := app.DetectGames(ctx, s.svc.ConfigDir(), app.DetectOptions{IncludeUnknown: true, Logger: s.log})
		if err != nil {
			s.writeAPIError(w, http.StatusInternalServerError, err)
			return
		}
		candidate, err := core.FindDetectedGame(detected, req.FromSteamAppID)
		if err != nil {
			// A GameSpecError naming from_steam_app_id, so the form marks
			// the offending input like any other rejected field.
			s.writeAPIError(w, gameAddErrorStatus(err), err)
			return
		}
		if spec, err = s.svc.PrefillGameSpecFromDetected(candidate, spec); err != nil {
			s.writeAPIError(w, http.StatusInternalServerError, err)
			return
		}
	}

	entry, err := s.svc.AddGame(ctx, spec)
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

// gameSourcesRequest is PUT /api/v1/games/{id}'s body: the FULL source map
// the game should end up with, keyed by registered source id with that
// game's identifier for each ("skyrimspecialedition", "1704", or "" for a
// source that keys the game by nothing else).
//
// A replacement, not a patch: an id the map omits is removed, which is the
// only way a form can express "stop using this source" without a second
// verb. The CLI's `lmm game edit --source/--remove-source` flags are a
// delta it resolves into exactly this map before calling the same core
// seam.
type gameSourcesRequest struct {
	Sources map[string]string `json:"sources"`
	// Loader is #359's declaration, additive: a body carrying it edits the
	// LOADER instead of the source map.
	//
	// One request, one edit. Each is a complete statement on its own and
	// each is its own gated write, so handling both in one request would
	// make a partial failure - sources written, loader not - expressible
	// with no way to report it; a body carrying both is refused rather than
	// silently ordered, which is the same rule `lmm game edit` follows.
	//
	// Clearing needs the difference between "absent" and "null", so this is
	// a pointer to a pointer's worth of state: LoaderSet says the member was
	// present, Loader nil then means "remove the declaration".
	Loader    *core.LoaderSpec `json:"loader,omitempty"`
	LoaderSet bool             `json:"loader_set,omitzero"`
}

// handleAPIGameSources answers PUT /api/v1/games/{id} with the game's own
// core.GameListEntry row, re-read after the write - the same document GET
// /api/v1/games carries and `lmm game list --json` prints, so the Setup
// page's Games table splices the response straight back into its list.
//
// This is #326's C-4 fix: the sources map was the one part of games.yaml
// neither frontend could change, so a custom source created in the Setup
// editor sat at "In use: -" until the user stopped the server and edited
// the file by hand.
//
// Like the lock/policy routes it is a SINGLE-STEP mutation rather than a
// job: core.Service.UpdateGameSources is one gated write with nothing to
// preview and no progress to report (mod_settings.go's own rule for the
// settings class). An unregistered source id is 400 carrying
// core.GameSpecError's {field, value, reason} - field "sources" - so the
// form marks the offending row; an unknown game id is 404.
func (s *Server) handleAPIGameSources(w http.ResponseWriter, r *http.Request) {
	var req gameSourcesRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	editsLoader := req.Loader != nil || req.LoaderSet
	if editsLoader && len(req.Sources) > 0 {
		s.writeAPIError(w, http.StatusBadRequest,
			errors.New("edit the sources and the loader in separate requests: send \"sources\", or \"loader\", not both"))
		return
	}

	update := func() (*core.GameListEntry, error) {
		if editsLoader {
			return s.svc.UpdateGameLoader(r.Context(), r.PathValue("id"), req.Loader)
		}
		return s.svc.UpdateGameSources(r.Context(), r.PathValue("id"), req.Sources)
	}
	entry, err := update()
	if err != nil {
		s.writeAPIError(w, gameSourcesErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, entry)
}

// handleAPIGameDetail answers GET /api/v1/games/{id} with core.GameDetail -
// the game's own list row plus its loader report, the same document
// `lmm game show --json` prints.
//
// It is the web game page's hydrate, and it is where the Steam launch option
// comes from: lmm works out which bootstrap the game needs and hands the
// exact string over as data, because it will not write that string into
// Steam's own configuration (see internal/core/loader_status.go). An unknown
// game is 404, like every other game-scoped route.
func (s *Server) handleAPIGameDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := s.svc.GameDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, domain.ErrGameNotFound) {
			status = http.StatusNotFound
		}
		s.writeAPIError(w, status, err)
		return
	}
	s.writeJSON(w, http.StatusOK, detail)
}

// gameSourcesErrorStatus classifies an UpdateGameSources failure: an
// unknown game is 404, a rejected map is the caller's input (400), a
// removal a game's own installed mods still depend on is a 409 collision
// with state the caller could not have known about from the map alone
// (M5, epic review M-4 - GameSourceInUseError, the mirror of how a game
// collision on `lmm game add`/POST /api/v1/games is classified), and
// anything else is a real write failure (500).
func gameSourcesErrorStatus(err error) int {
	var specErr *core.GameSpecError
	var inUseErr *core.GameSourceInUseError
	switch {
	case errors.Is(err, domain.ErrGameNotFound):
		return http.StatusNotFound
	case errors.As(err, &specErr):
		return http.StatusBadRequest
	case errors.As(err, &inUseErr):
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
// The default document is domain.DetectedGame.Listable (#368): every
// curated game, plus an uncurated one whose Steam Workshop manifest
// declares items already downloaded - the row #269's Tier 1 exists for,
// which used to need ?all=1 nobody would guess to send.
//
// ?all=1 widens it to EVERY installed Steam game (#206), adding the rest of
// the rows no known-games entry covers - no `known` member at all (never a literal
// false), no sources, an empty mod path and no index, since a detect
// selection cannot name them. The
// document is the same one either way (the addition is additive, and the
// known rows keep the same numbering), so the SPA can render both from one
// decoder; an unknown row is added through POST /api/v1/games with
// from_steam_app_id, not through this route's POST half.
//
// This half has no CLI equivalent under --json - `lmm game detect` printed
// its listing to the terminal and emitted only the RESULT document, since
// Ruling 15 allows nothing else on stdout beside it. A browser selection
// happens between two requests, so the listing had to become a document
// (core.GameDetectListing, goldened with the rest). #206 gave the CLI its
// own way to that document: `lmm game detect --include-unknown --json`,
// where the listing is likewise the whole answer.
func (s *Server) handleAPIGamesDetect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	includeUnknown := queryFlag(r, "all")
	// The scan itself always goes WIDE, exactly as `lmm game detect`'s own
	// does (#206 Important 3, and #368): the default listing is
	// domain.DetectedGame.Listable, which includes an uncurated candidate
	// with Steam Workshop items, and a narrow scan would never have
	// produced that row for GameDetectListing to keep. ?all= still decides
	// what is PUBLISHED, one line below.
	detected, warnings, err := app.DetectGames(ctx, s.svc.ConfigDir(), app.DetectOptions{IncludeUnknown: true, Logger: s.log})
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	listing, err := s.svc.GameDetectListing(ctx, detected, warnings, core.GameDetectListingOptions{IncludeUnknown: includeUnknown})
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, listing)
}

// queryFlag reads a boolean query parameter. Only "1" and "true" are true;
// anything else - including an absent parameter and an empty value - is
// false, so a mistyped flag reads as "not asked for" rather than silently
// widening a scan.
func queryFlag(r *http.Request, name string) bool {
	switch r.URL.Query().Get(name) {
	case "1", "true":
		return true
	default:
		return false
	}
}

// gameDetectSelectRequest is POST /api/v1/games/detect's body: which of
// the listing's rows to add, each named by its 1-based index or its slug
// (core.SelectDetectedGames resolves both, and refuses a bare selector that
// is both an index and another row's slug - "#2" and "slug:2" name one
// axis each). Selecting an already-configured row is legal and overwrites
// it - that is `lmm game detect`'s documented repair path, and the
// listing's already_configured flag is what lets the SPA warn before
// offering it.
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
	// Scanned WIDE on purpose (#206): a selector naming an installed game
	// nothing can configure must be told what it hit and where to go
	// instead, and that is only possible if the scan saw the row at all.
	// core.SelectDetectedGames enforces the rule - since #368 a row
	// detection prefilled a source map for IS selectable, by slug - and this
	// handler only classifies the refusal and names the other route.
	detected, warnings, err := app.DetectGames(ctx, s.svc.ConfigDir(), app.DetectOptions{IncludeUnknown: true, Logger: s.log})
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	selected, err := core.SelectDetectedGames(detected, req.Select)
	if err != nil {
		if errors.Is(err, core.ErrUnknownDetectedGame) {
			s.writeAPIError(w, http.StatusBadRequest,
				fmt.Errorf("%w; add it with POST /api/v1/games, passing its from_steam_app_id", err))
			return
		}
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}

	// The same seam `lmm game detect`'s prompt applies its selection through
	// (#368 review Minor 8), so the two frontends cannot diverge about what a
	// selection DOES: one mutation slot for the whole selection, curated rows
	// configured from their known-games entry, an uncurated one added the way
	// POST /api/v1/games' from_steam_app_id adds it.
	_, result, applyErr := s.svc.ApplyDetectSelection(ctx, selected)
	result.Warnings = append(append([]string(nil), warnings...), result.Warnings...)
	if applyErr != nil {
		// It persists one game at a time and stops at the first failure, so
		// the partial result is real state the caller must see - it travels
		// in the envelope's details via core.GameDetectPartialError, exactly
		// as it does for the CLI. An already-configured UNCURATED row is
		// refused rather than overwritten (there is no curated entry to
		// repair it from), which is a collision with state the caller could
		// not know about from the listing alone - 409, as on POST
		// /api/v1/games.
		s.writeAPIError(w, gameDetectApplyErrorStatus(applyErr), &core.GameDetectPartialError{Err: applyErr, Result: result})
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

// gameDetectApplyErrorStatus classifies an ApplyDetectSelection failure: a
// taken game id is a collision (409, matching POST /api/v1/games), and
// anything else is a real write failure (500). A REJECTED selector never
// reaches here - SelectDetectedGames answered 400 above.
func gameDetectApplyErrorStatus(err error) int {
	if errors.Is(err, core.ErrGameExists) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// handleAPIGameSetDefault answers POST /api/v1/games/{id}/set-default with
// the core.SettingsResult document `lmm game set-default --json` emits -
// added on top of task A1's wire (coordinator direction, #333): the Setup
// page's Games table needs this and the clear route below, and both are
// thin wrappers over the CLI's own seams (core.Service.SetDefaultGame,
// domain.ErrGameNotFound), so no new wire TYPE and no new golden are
// needed - the response is byte-identical to a document already goldened
// under internal/core/testdata/json.
//
// An unknown game id is 404, matching `lmm game set-default`'s own
// "game not found" refusal (cmd/lmm/game.go's doGameSetDefault) rather than
// silently writing a default nothing in games.yaml resolves to.
func (s *Server) handleAPIGameSetDefault(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if _, err := s.svc.GetGame(id); err != nil {
		s.writeAPIError(w, http.StatusNotFound, err)
		return
	}
	if err := s.svc.SetDefaultGame(ctx, id); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, &core.SettingsResult{DefaultGame: id})
}

// handleAPIGameClearDefault answers DELETE /api/v1/games/default with the
// re-read core.SettingsResult (DefaultGame empty) `lmm game clear-default
// --json` emits. Unconditional, like the CLI: clearing an already-unset
// default is a no-op that still answers 200, not a 404 - there is no
// "which default" to have gotten wrong.
func (s *Server) handleAPIGameClearDefault(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.ClearDefaultGame(r.Context()); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, &core.SettingsResult{})
}
