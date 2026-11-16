package serve

import (
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// handleAPISearchCollections answers GET /api/v1/search/collections (#651)
// with exactly the core.CollectionSearchReport `lmm search --collections
// --json` emits: the search page's Collections tab. Every parameter is
// optional, as the CLI's are - ?q= is the query (absent lists the game's
// collections), ?sort= is --sort, ?source= is --source, and ?page=/
// ?page_size= page through the source's matches (the CLI's --limit is a
// page size on page 0).
//
// A sort outside domain.SearchSorts, a search no source of the game can
// answer (core.IsCollectionSearchUnsupported: an unknown or collection-less
// source, a game without one), and a request out of core's bounds
// (core.IsInvalidCollectionSearch: a query over MaxCollectionQueryRunes, a
// page past MaxCollectionOffset) are the caller's input: 400. A page_size
// over core.MaxCollectionPageSize is clamped, as the CLI's --limit is.
// Anything else is the source failing upstream: 502.
func (s *Server) handleAPISearchCollections(w http.ResponseWriter, r *http.Request) {
	page, ok := s.parseOptionalIntParam(w, r, "page")
	if !ok {
		return
	}
	pageSize, ok := s.parseOptionalIntParam(w, r, "page_size")
	if !ok {
		return
	}
	sel, ok := s.resolveReadyAPISelection(w, r)
	if !ok {
		return
	}
	report, err := s.svc.SearchCollections(r.Context(), sel.Game, r.URL.Query().Get("q"), core.CollectionSearchOptions{
		SourceID: r.URL.Query().Get("source"),
		Sort:     domain.SearchSort(r.URL.Query().Get("sort")),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		status := http.StatusBadGateway
		if core.IsInvalidSearchSort(err) || core.IsCollectionSearchUnsupported(err) || core.IsInvalidCollectionSearch(err) {
			status = http.StatusBadRequest
		}
		s.writeAPIError(w, status, err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}
