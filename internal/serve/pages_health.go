package serve

import (
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// healthPageData is "/health"'s template data. Verify is nil whenever
// resolveSelection didn't resolve a ready game+profile
// (docs/plans/2026-08-30-serve-impl.md Task 4 ruling on game/profile
// selection); Conflicts is nil in that same case, and may be empty (but
// non-nil) once a ready selection simply has none.
type healthPageData struct {
	pageChrome
	Verify    *core.VerifyReport
	Conflicts []core.ProfileConflict

	// Fixable is how many findings `verify --fix` would actually act on
	// (kind_verify_fix.go). The repair form renders ONLY when it is
	// non-zero: an action offered against findings the engine will not
	// touch promises a repair that cannot happen, and an action offered on
	// a clean profile is an invitation to run a mutation for nothing.
	Fixable int
}

// handleHealth renders "/health": verify findings (VerifyReport) and file
// conflicts (GetProfileConflicts) between the profile's enabled mods
// (docs/plans/2026-08-30-serve-design.md §HTTP surface). The "Fix issues"
// form targets POST /health/fix (Task 9, #322) and renders only when the
// report holds a finding a repair would act on. Verify runs at VerifyLocal
// (no network calls) - a page render must stay cheap and offline - which is
// also the tier the repair itself runs at, so the action acts on the very
// findings this page showed.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	sel, err := s.resolveSelection(r)
	if err != nil {
		s.renderError(w, err)
		return
	}

	data := healthPageData{pageChrome: s.chrome(r, "Health", &sel)}
	if sel.ready() {
		ctx := r.Context()
		report, err := s.svc.VerifyReport(ctx, sel.Game, sel.Profile, core.VerifyOptions{Tier: core.VerifyLocal}, nil)
		if err != nil {
			s.renderError(w, err)
			return
		}
		data.Verify = report
		data.Fixable = len(verifyFixableFindings(report))

		conflicts, err := s.svc.GetProfileConflicts(ctx, sel.Game, sel.Profile)
		if err != nil {
			s.renderError(w, err)
			return
		}
		data.Conflicts = conflicts
	}

	s.render(w, healthTemplate, data)
}
