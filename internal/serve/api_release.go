// api_release.go is lmm's own update notice in the web UI (#568): GET
// /api/v1/updates/lmm answers the core.ReleaseStatus `lmm version --json`
// emits, which the SPA's release banner and Setup > Updates read, and a
// running server keeps that answer fresh itself - and, since #586, installs
// the release it finds when auto_update is on. PUT
// /api/v1/settings/auto-update is Setup > Updates' switch.
package serve

import (
	"context"
	"errors"
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
	auto := s.releases.AutoUpdateStatus(st.Install)
	st.AutoUpdate = &auto
	s.writeJSON(w, http.StatusOK, st)
}

// autoUpdateRequest is PUT /api/v1/settings/auto-update's body.
type autoUpdateRequest struct {
	// Enabled is the auto_update to write. Required.
	Enabled *bool `json:"enabled"`
}

// handleAPIAutoUpdateSetting answers PUT /api/v1/settings/auto-update: a
// sanctioned single-step write of config.yaml's auto_update (#586), with
// nothing to preview, answered with the release status the switch's page
// re-renders from.
func (s *Server) handleAPIAutoUpdateSetting(w http.ResponseWriter, r *http.Request) {
	var req autoUpdateRequest
	if err := decodeAPIBody(w, r, &req); err != nil {
		s.writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	if req.Enabled == nil {
		s.writeAPIError(w, http.StatusBadRequest, errors.New(`the body must say {"enabled": true} or {"enabled": false}`))
		return
	}
	if err := s.svc.SetAutoUpdate(r.Context(), *req.Enabled); err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.handleAPIReleaseStatus(w, r)
}

// AutoUpdater is what `lmm serve` runs an automatic update through (#586):
// *core.SelfUpdater, built by the composition root with the restart of
// lmm-serve.service wired in.
type AutoUpdater interface {
	// AutoUpdateDue reports whether an automatic update to latest should
	// run now.
	AutoUpdateDue(latest string) bool
	// RunAuto runs one: verified, under the self-update lock, recorded.
	RunAuto(ctx context.Context) (core.AutoUpdateOutcome, error)
}

// autoUpdate runs an automatic update when the checker's cache knows of a
// newer release, the install is self-managed, the binary under this server
// has not already been replaced, and the updater says one is due. It runs
// on the refresh goroutine, never a request: when this server is
// lmm-serve.service, the update's restart of it is queued with systemd and
// arrives as the shutdown signal Serve already handles gracefully. A
// server started by hand keeps running, and its banner asks for a restart.
func (s *Server) autoUpdate(ctx context.Context) {
	if s.autoUpdater == nil {
		return
	}
	st, ok := s.releases.PendingUpdate(ctx)
	if !ok || st.Install.Method != core.InstallSelfManaged {
		return
	}
	if s.releases.InstalledNewer(ctx) != "" || !s.autoUpdater.AutoUpdateDue(st.Latest) {
		return
	}
	out, err := s.autoUpdater.RunAuto(ctx)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			s.log.Warn("automatic lmm update failed", "release", st.Latest, "error", err)
		}
	case out.Skipped != "":
		s.log.Debug("automatic lmm update skipped", "reason", out.Skipped)
	case out.Result.Updated:
		_, _ = fmt.Fprintf(s.notices, "lmm updated itself to v%s.\n", out.Result.Version)
		if out.Result.ServeRestartError != "" {
			s.log.Warn("restarting lmm-serve.service after the automatic update failed", "error", out.Result.ServeRestartError)
		}
	}
}

// refreshReleases runs for as long as Serve does: an unforced check now,
// then one every releaseRefreshEvery, each followed by an automatic update
// when one is due (autoUpdate). Each is a no-op inside the checker's
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
		s.autoUpdate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
