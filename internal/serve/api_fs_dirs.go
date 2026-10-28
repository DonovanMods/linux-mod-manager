package serve

import (
	"errors"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// handleAPIFsDirs answers GET /api/v1/fs/dirs?path=…&hidden=1&nearest=1
// with the core.DirectoryListing document: the subfolders of one absolute
// path on the machine `lmm serve` runs on (#529).
//
// It exists because a browser cannot name a folder: <input type="file">
// yields a file's contents and a bare name, never a location, so the SPA's
// folder chooser browses the server's own filesystem through this route.
// That is safe to offer only because everything that makes the API
// local-only applies to it unchanged - the Host allow-list (the
// DNS-rebinding guard), the Origin check, the security headers - and
// because it adds nothing of its own: it is a GET with no CORS headers, so
// another origin's page cannot read an answer, it lists folders only (core
// never returns a regular file or reads one), and a symlinked folder is
// marked and never followed.
//
// ?hidden=1 includes dot-folders. ?nearest=1 answers for the closest
// existing folder when the path is empty, relative, missing or not a folder
// (the chooser's "open where the field points"); the document's `requested`
// names what was asked for when the answer differs. Refusals use the shared
// error envelope with core.DirectoryListError as its details: 400 for a
// path that is not absolute or not a folder, 404 for one that is not there,
// 403 for a folder this process may not read.
func (s *Server) handleAPIFsDirs(w http.ResponseWriter, r *http.Request) {
	listing, err := s.svc.ListDirectories(r.Context(), r.URL.Query().Get("path"), core.ListDirectoriesOptions{
		Hidden:  queryFlag(r, "hidden"),
		Nearest: queryFlag(r, "nearest"),
	})
	if err != nil {
		s.writeAPIError(w, fsDirsErrorStatus(err), err)
		return
	}
	s.writeJSON(w, http.StatusOK, listing)
}

// fsDirsErrorStatus classifies a ListDirectories failure: the caller's path
// is wrong (400, or 404 when merely absent), the folder is shut (403), and
// anything else is a real read failure (500).
func fsDirsErrorStatus(err error) int {
	var dirErr *core.DirectoryListError
	if !errors.As(err, &dirErr) {
		return http.StatusInternalServerError
	}
	switch dirErr.Reason {
	case core.DirectoryNotFound:
		return http.StatusNotFound
	case core.DirectoryPermissionDenied:
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}
