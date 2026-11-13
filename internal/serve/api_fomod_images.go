// api_fomod_images.go serves a FOMOD question's option images (#615): GET
// /api/v1/fomod/images?source=&mod=&config=&path=, the installer wizard's
// <img> source.
//
// The route is read-only and serves only what core kept for a question
// (core.Service.FomodImage): the images a question named, copied out of
// the extracted archive when it was asked, under hash names no path from
// the archive's XML can reach, each capped in size and sniffed as a raster
// image type (never SVG). The query is the question's own identity -
// source_id, mod_id, config_sha256 - and the option's image path exactly
// as the question carries it. Anything else is a 404: the route never
// opens a path built from its input.
package serve

import (
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// handleAPIFomodImage serves one kept option image.
func (s *Server) handleAPIFomodImage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	img, err := s.svc.FomodImage(q.Get("source"), q.Get("mod"), q.Get("config"), q.Get("path"))
	if err != nil {
		s.writeAPIError(w, http.StatusNotFound, core.ErrFomodImageNotFound)
		return
	}
	f, err := os.Open(img.Path)
	if err != nil {
		s.writeAPIError(w, http.StatusNotFound, core.ErrFomodImageNotFound)
		return
	}
	defer f.Close() //nolint:errcheck // read-only
	h := w.Header()
	h.Set("Content-Type", img.ContentType)
	h.Set("Content-Length", strconv.FormatInt(img.Size, 10))
	h.Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.CopyN(w, f, img.Size)
	}
}
