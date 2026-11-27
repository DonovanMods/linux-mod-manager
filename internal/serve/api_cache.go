// api_cache.go is Setup > Storage's read and its setting (#678): GET
// /api/v1/cache is `lmm cache status --json` for every game, and PUT
// /api/v1/settings/cache is `lmm cache keep`, a single-step config.yaml
// write with nothing to preview, answered with the status the section
// re-renders from. The prune itself is a job (kind_cache_prune.go).
package serve

import (
	"errors"
	"net/http"
)

// handleAPICacheStatus answers GET /api/v1/cache: core.CacheStatus for
// every configured game. It reads every cached file's size.
func (s *Server) handleAPICacheStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.svc.CacheStatus(r.Context(), nil)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, status)
}

// cacheSettingRequest is PUT /api/v1/settings/cache's body.
type cacheSettingRequest struct {
	// KeepPreviousVersions is cache.keep_previous_versions to write: a
	// count, or any negative number for unlimited. Required.
	KeepPreviousVersions *int `json:"keep_previous_versions"`
}

// handleAPICacheSetting answers PUT /api/v1/settings/cache.
func (s *Server) handleAPICacheSetting(w http.ResponseWriter, r *http.Request) {
	var req cacheSettingRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if req.KeepPreviousVersions == nil {
		s.writeAPIError(w, http.StatusBadRequest, errors.New(`the body must say {"keep_previous_versions": <count>}, -1 for unlimited`))
		return
	}
	if err := s.svc.SetCacheKeepPreviousVersions(r.Context(), *req.KeepPreviousVersions); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.handleAPICacheStatus(w, r)
}
