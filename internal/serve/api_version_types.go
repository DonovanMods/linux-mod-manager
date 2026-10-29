// api_version_types.go is the read behind the game editor's CurseForge
// picker (#563): a source's version types for one game, by name, so the
// SPA never asks for a raw gameVersionTypeId. Saving the ones picked is
// still PUT /api/v1/games/{id}'s `curseforge` member, as ids.
package serve

import (
	"errors"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// handleAPISourceVersionTypes answers GET
// /api/v1/sources/{id}/version-types?game=... with the
// core.GameVersionTypesReport `lmm game version-types --json` emits.
//
// It is addressed like the index routes - the source, then the game whose
// mapping it is asked with - because the list belongs to that pair. An
// unknown source or game, and a source that publishes no version types,
// are 404; a game that does not map the source is its configuration (400);
// a missing key is 401, as for the catalog search; and any other failure
// of the source's own call is 502. The SPA falls back to an id field on
// every one of them.
func (s *Server) handleAPISourceVersionTypes(w http.ResponseWriter, r *http.Request) {
	sourceID, gameID, ok := s.indexRouteTarget(w, r)
	if !ok {
		return
	}
	report, err := s.svc.GameVersionTypes(r.Context(), gameID, sourceID)
	if err != nil {
		s.writeAPIError(w, versionTypesErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}

// versionTypesErrorStatus classifies a GameVersionTypes failure.
func versionTypesErrorStatus(err error) int {
	switch {
	case errors.Is(err, core.ErrNoGameVersionTypes), errors.Is(err, domain.ErrGameNotFound):
		return http.StatusNotFound
	case core.IsGameIdentifierInvalid(err):
		return http.StatusBadRequest
	case errors.Is(err, domain.ErrAuthRequired):
		return http.StatusUnauthorized
	default:
		return http.StatusBadGateway
	}
}
