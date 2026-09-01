// api_mod_files.go answers the full mod page's two supplementary reads
// (docs/plans/2026-08-31-serve-spa-design.md §Full mod page: "files table,
// versions table") - both re-surfacing endpoints the deleted page layer
// merged directly into its mod-detail page (pages_mods.go, gone with Unit
// 1), now split out the way /api/v1/mods/{source}/{id} already kept
// ModDetail on its own (api_mods_test.go: "no ModFiles/AvailableModVersions
// merged in, unlike the page").
//
// GET .../versions is AvailableModVersions' first real consumer (#97):
// nothing called it before this route existed (core/service.go's own doc
// comment: "No cmd caller today ... lmm serve's intended consumer").
package serve

import (
	"errors"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// handleAPIModFiles answers GET /api/v1/mods/{source}/{id}/files with
// exactly the core.ModFilesReport document `lmm mod files --json` emits.
// Unlike ModDetail (a live source read that succeeds for any catalog mod),
// ModFiles is an INSTALLED-mod query - a mod that exists at the source but
// is not installed in the resolved profile answers 404, mirroring
// ModFiles' own "mod not found: %s" error for exactly that case.
func (s *Server) handleAPIModFiles(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("source")
	modID := r.PathValue("id")

	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	report, err := s.svc.ModFiles(r.Context(), sel.Game, sel.Profile, sourceID, modID)
	if err != nil {
		s.writeAPIError(w, http.StatusNotFound, err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}

// modVersionsDocument is GET /api/v1/mods/{source}/{id}/versions' document:
// AvailableModVersions' own []string, wrapped rather than answered as a bare
// top-level array (every other /api/v1 document follows the same rule - a
// bare array has nowhere to grow a sibling member later, jobsIndex's own doc
// comment). Supported distinguishes "this mod truly has one version" from
// "this source cannot report versions at all" - both render as an empty-
// looking Versions list otherwise, and the full mod page's versions table
// and its version-scoped lock affordance both need to tell those apart
// (an empty table would be a single-entry table either way, but only the
// former is actually one).
type modVersionsDocument struct {
	Versions  []string `json:"versions"`
	Supported bool     `json:"supported"`
}

// handleAPIModVersions answers GET /api/v1/mods/{source}/{id}/versions:
// AvailableModVersions' per-file version list for the CATALOG mod (a live
// source read, like ModDetail - not scoped to whether the mod happens to be
// installed). A source that cannot report versions at all
// (core.ErrSourceVersionsUnsupported, e.g. a directory source with no
// per-file version metadata) is not an error: it answers 200 with an empty
// list and Supported false, the same "not a failure, just nothing to say"
// treatment ModDetail's own changelog gives a source with no
// source.ChangelogProvider.
// Any other failure (an unknown mod, a genuine source outage) answers the
// ordinary {"error","details"} envelope.
//
// core.SourceMappedMod translates the mod's GameID the same way `lmm mod
// lock`'s doModLock does before its own ResolveModVersion call
// (cmd/lmm/mod.go): AvailableModVersions forwards straight to
// Service.GetModFiles, which - unlike Service.GetMod - does NOT translate a
// game ID itself, so a game whose source mapping differs from its own id
// would otherwise query the wrong upstream game.
func (s *Server) handleAPIModVersions(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("source")
	modID := r.PathValue("id")

	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}

	mod, err := s.svc.GetMod(r.Context(), sourceID, sel.Game.ID, modID)
	if err != nil {
		s.writeAPIError(w, http.StatusNotFound, err)
		return
	}

	versions, err := s.svc.AvailableModVersions(r.Context(), sourceID, core.SourceMappedMod(sel.Game, mod))
	if errors.Is(err, core.ErrSourceVersionsUnsupported) {
		s.writeJSON(w, http.StatusOK, modVersionsDocument{Versions: []string{}, Supported: false})
		return
	}
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, modVersionsDocument{Versions: versions, Supported: true})
}
