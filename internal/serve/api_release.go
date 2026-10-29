// api_release.go is lmm's own update notice in the web UI (#568): GET
// /api/v1/updates/lmm answers the core.ReleaseStatus `lmm version --json`
// emits, which the SPA's release banner reads, and a running server keeps
// that answer fresh itself.
package serve

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// releaseRefreshEvery is how often a running server offers the checker a
// refresh. The checker itself asks GitHub at most once a day; this only
// bounds how late after that day a long-running server notices.
const releaseRefreshEvery = time.Hour

// releaseStatusInstallWait is the most a status request waits on install
// detection still under way (Serve starts it in the background); past it,
// the answer names the release page rather than a package's command.
const releaseStatusInstallWait = 250 * time.Millisecond

// releaseStatusVersionWait is the most a status request waits on running
// `<exe> --version`, which happens once per replacement of the binary
// (core.ReleaseChecker.InstalledNewer); past it, the banner waits for the
// next page load.
const releaseStatusVersionWait = 2 * time.Second

// handleAPIReleaseStatus answers GET /api/v1/updates/lmm from the checker's
// cache - never a request of its own, so a page load cannot reach GitHub.
// Its installed field is a newer lmm binary replaced under this server
// (#569), which it runs the old one of until it restarts. A server without
// a checker reports the check off.
func (s *Server) handleAPIReleaseStatus(w http.ResponseWriter, r *http.Request) {
	if s.releases == nil {
		s.writeJSON(w, http.StatusOK, core.ReleaseStatus{Current: s.version})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), releaseStatusInstallWait)
	defer cancel()
	st := s.releases.Status(ctx)
	vctx, vcancel := context.WithTimeout(r.Context(), releaseStatusVersionWait)
	defer vcancel()
	st.Installed = s.releases.InstalledNewer(vctx)
	s.writeJSON(w, http.StatusOK, st)
}

// refreshReleases runs for as long as Serve does: an unforced check now,
// then one every releaseRefreshEvery. Each is a no-op inside the checker's
// 24 hours, when the user turned the check off, and for a dev build; a
// failure is cached by the checker and only logged here. Before the first
// check this data dir ever makes, it writes the same one-time notice the
// CLI prints, so a web-only user is told too.
func (s *Server) refreshReleases(ctx context.Context) {
	tick := time.NewTicker(releaseRefreshEvery)
	defer tick.Stop()
	for {
		if s.releases.RefreshDue() && s.releases.FirstRunNoticeDue() {
			_, _ = fmt.Fprintln(s.notices, s.releases.FirstRunNotice())
			_ = s.releases.RecordFirstRunNotice()
		}
		if err := s.releases.Refresh(ctx); err != nil && ctx.Err() == nil {
			s.log.Debug("lmm release check failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
