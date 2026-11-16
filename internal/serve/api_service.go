// api_service.go is GET /api/v1/service (#649): `lmm service status
// --json`'s document, read for the web UI's Setup > Updates, which names
// the lmm binary lmm-serve.service runs beside the one this server runs
// and warns when they differ. It is read-only: installing, restarting and
// switching the service stay `lmm service` commands, since a restart from
// a page this very service serves would cut that page's own request off.
package serve

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// ServiceStatuser is what GET /api/v1/service reads: *core.UserUnits,
// whose Status reports every unit lmm manages and the binary each runs.
type ServiceStatuser interface {
	// Status reports the units, the binary each runs and this server's.
	Status(ctx context.Context) (core.ServiceReport, error)
}

// serviceStatusWait bounds one status read: a handful of systemctl
// queries and one bounded `<binary> --version` per unit.
const serviceStatusWait = 15 * time.Second

// handleAPIServiceStatus answers GET /api/v1/service. A server built
// without a ServiceStatuser answers 204, no document - nothing to report,
// which the page shows as no row rather than an error; a host without a
// systemd user manager answers 422, with the reason.
func (s *Server) handleAPIServiceStatus(w http.ResponseWriter, r *http.Request) {
	if s.serviceStatus == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), serviceStatusWait)
	defer cancel()
	report, err := s.serviceStatus.Status(ctx)
	switch {
	case errors.Is(err, core.ErrSystemdUnavailable):
		s.writeAPIError(w, http.StatusUnprocessableEntity, err)
		return
	case err != nil:
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}
	s.writeJSON(w, http.StatusOK, report)
}
