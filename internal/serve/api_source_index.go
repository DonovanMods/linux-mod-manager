// api_source_index.go answers the local-index surface (#410, design §4.2):
// one game's index for a source that searches a local copy of its catalogue
// (Thunderstore), its rebuild, the listing of every index on disk, and the
// prune.
//
// They are settings-class single-step routes, not jobs - api_mod_settings.go's
// shape and reasoning. The worst rebuild measured is a few seconds, it is
// idempotent, and what it would report while running is one line; a job
// kind, a plan renderer and an SSE contract would buy nothing for it. What
// a refresh WAITS on (a throttle, a suspended host) is still reported: it
// is the error envelope's details when it fails, which is where a
// synchronous answer can carry it.
//
// Each answers with the goldened core document its `lmm source index` twin
// emits: core.IndexStatus, core.IndexReport, core.SourceIndexListing and
// core.IndexPruneReport. This file adds no wire type of its own.
package serve

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// sourceIndexRefreshRequest is POST /api/v1/sources/{id}/index's body.
// Refresh true rebuilds even an index that is current (`--refresh`); false
// brings a missing or stale one up to date and leaves a current one alone.
type sourceIndexRefreshRequest struct {
	Refresh bool `json:"refresh,omitzero"`
}

// indexPruneRequest is POST /api/v1/indexes/prune's body - core's
// IndexPruneOptions, minus the source scope the web never narrows. Only is
// how the setup card's confirm binds the removal to what its preview
// listed.
type indexPruneRequest struct {
	All    bool     `json:"all,omitzero"`
	DryRun bool     `json:"dry_run,omitzero"`
	Only   []string `json:"only,omitempty"`
}

// errNoIndexGame is the 400 for an index route called without ?game=: an
// index belongs to a game's mapping, and there is no default to guess.
var errNoIndexGame = errors.New("the game query parameter is required: an index belongs to one game's source mapping")

// handleAPISourceIndex answers GET /api/v1/sources/{id}/index?game=... with
// core.IndexStatus - what `lmm source index --json` emits - or 404 when the
// source keeps no index.
func (s *Server) handleAPISourceIndex(w http.ResponseWriter, r *http.Request) {
	sourceID, gameID, ok := s.indexRouteTarget(w, r)
	if !ok {
		return
	}
	status, err := s.svc.SourceIndexStatus(r.Context(), sourceID, gameID)
	if err != nil {
		s.writeAPIError(w, indexErrorStatus(err), err)
		return
	}
	if status == nil {
		s.writeAPIError(w, http.StatusNotFound, fmt.Errorf("source %q keeps no local index", sourceID))
		return
	}
	s.writeJSON(w, http.StatusOK, status)
}

// handleAPISourceIndexRefresh answers POST /api/v1/sources/{id}/index with
// core.IndexReport - `lmm source index --refresh --json`. A refresh that
// fails over a usable index is a 200 report whose status is "stale"; one
// that leaves no index at all is the 502 envelope.
func (s *Server) handleAPISourceIndexRefresh(w http.ResponseWriter, r *http.Request) {
	var req sourceIndexRefreshRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	sourceID, gameID, ok := s.indexRouteTarget(w, r)
	if !ok {
		return
	}
	// Asked first so a source that keeps no index is the same 404 the read
	// answers, rather than a refusal only core's sentinel could classify.
	status, err := s.svc.SourceIndexStatus(r.Context(), sourceID, gameID)
	if err != nil {
		s.writeAPIError(w, indexErrorStatus(err), err)
		return
	}
	if status == nil {
		s.writeAPIError(w, http.StatusNotFound, fmt.Errorf("source %q keeps no local index", sourceID))
		return
	}
	report, err := s.svc.RefreshSourceIndex(r.Context(), sourceID, gameID, req.Refresh, nil)
	if err != nil {
		s.writeAPIError(w, indexErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}

// handleAPIIndexes answers GET /api/v1/indexes with core.SourceIndexListing -
// `lmm source index --all --json`: every index on disk, and every mapped
// one not built yet, with the games that use each.
func (s *Server) handleAPIIndexes(w http.ResponseWriter, r *http.Request) {
	listing, err := s.svc.ListSourceIndexes(r.Context(), "")
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, listing)
}

// handleAPIIndexesPrune answers POST /api/v1/indexes/prune with
// core.IndexPruneReport - `lmm source index prune --json`. The setup card
// previews with dry_run, then confirms with only set to the preview's
// removal keys, so nothing it did not show can go.
func (s *Server) handleAPIIndexesPrune(w http.ResponseWriter, r *http.Request) {
	var req indexPruneRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	report, err := s.svc.PruneSourceIndexes(r.Context(), core.IndexPruneOptions{
		All: req.All, DryRun: req.DryRun, Only: req.Only,
	})
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}

// indexRouteTarget reads the {id} path value and the required ?game=, and
// answers 404 for a source the registry does not have.
func (s *Server) indexRouteTarget(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	sourceID := r.PathValue("id")
	if _, err := s.svc.GetSource(sourceID); err != nil {
		s.writeAPIError(w, http.StatusNotFound, err)
		return "", "", false
	}
	gameID := r.URL.Query().Get(gameParam)
	if gameID == "" {
		s.writeAPIError(w, http.StatusBadRequest, errNoIndexGame)
		return "", "", false
	}
	return sourceID, gameID, true
}

// indexErrorStatus classifies an index-surface failure (design §4.3): the
// game's own configuration being wrong is bad input, an unknown game is
// not found, and an index that cannot be had is the upstream failing.
func indexErrorStatus(err error) int {
	switch {
	case core.IsGameIdentifierInvalid(err):
		return http.StatusBadRequest
	case errors.Is(err, domain.ErrGameNotFound):
		return http.StatusNotFound
	case core.IsIndexUnavailable(err):
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}
