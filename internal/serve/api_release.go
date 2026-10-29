// api_release.go is lmm's own update notice in the web UI (#568): GET
// /api/v1/updates/lmm answers the core.ReleaseStatus `lmm version --json`
// emits, which the SPA's release banner reads, and a running server keeps
// that answer fresh itself.
package serve

import (
	"context"
	"net/http"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// releaseRefreshEvery is how often a running server offers the checker a
// refresh. The checker itself asks GitHub at most once a day; this only
// bounds how late after that day a long-running server notices.
const releaseRefreshEvery = time.Hour

// handleAPIReleaseStatus answers GET /api/v1/updates/lmm from the checker's
// cache - never a request of its own, so a page load cannot reach GitHub.
// A server without a checker reports the check off.
func (s *Server) handleAPIReleaseStatus(w http.ResponseWriter, r *http.Request) {
	if s.releases == nil {
		s.writeJSON(w, http.StatusOK, core.ReleaseStatus{Current: s.version})
		return
	}
	s.writeJSON(w, http.StatusOK, s.releases.Status(r.Context()))
}

// refreshReleases runs for as long as Serve does: an unforced check now,
// then one every releaseRefreshEvery. Each is a no-op inside the checker's
// 24 hours, when the user turned the check off, and for a dev build; a
// failure is cached by the checker and only logged here.
func (s *Server) refreshReleases(ctx context.Context) {
	tick := time.NewTicker(releaseRefreshEvery)
	defer tick.Stop()
	for {
		if _, err := s.releases.Check(ctx, false); err != nil && ctx.Err() == nil {
			s.log.Debug("lmm release check failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
