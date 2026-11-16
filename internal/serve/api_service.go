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
	"sync"
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

// serviceStatusTTL is how long one status read answers the requests after
// it. A read runs several systemctl queries and up to two `--version`
// execs, and the page asks on every visit to Setup > Updates; a few
// seconds is long enough to absorb a burst and short enough that a change
// made from a terminal shows on the next look.
const serviceStatusTTL = 5 * time.Second

// serviceStatusCache shares status reads between requests (#649 review):
// a report younger than serviceStatusTTL is answered as is, and requests
// that arrive while a read is under way wait on that read. A failure is
// shared with the requests waiting on it but not kept.
type serviceStatusCache struct {
	mu     sync.Mutex
	report core.ServiceReport
	at     time.Time // when report was read; zero when there is none
	call   *serviceStatusCall
}

// serviceStatusCall is one status read under way.
type serviceStatusCall struct {
	done   chan struct{}
	report core.ServiceReport
	err    error
}

// read returns a cached report, the one being read, or a new read's. The
// read runs on its own bounded context, not the first request's, so a tab
// closed mid-read does not fail the others waiting on it; each request
// still stops waiting when its own ctx ends.
func (c *serviceStatusCache) read(ctx context.Context, status ServiceStatuser) (core.ServiceReport, error) {
	c.mu.Lock()
	if !c.at.IsZero() && time.Since(c.at) < serviceStatusTTL {
		r := c.report
		c.mu.Unlock()
		return r, nil
	}
	call := c.call
	if call == nil {
		call = &serviceStatusCall{done: make(chan struct{})}
		c.call = call
		go c.run(context.WithoutCancel(ctx), status, call)
	}
	c.mu.Unlock()

	select {
	case <-call.done:
		return call.report, call.err
	case <-ctx.Done():
		return core.ServiceReport{}, ctx.Err()
	}
}

// run performs one read and records it.
func (c *serviceStatusCache) run(ctx context.Context, status ServiceStatuser, call *serviceStatusCall) {
	ctx, cancel := context.WithTimeout(ctx, serviceStatusWait)
	defer cancel()
	call.report, call.err = status.Status(ctx)

	c.mu.Lock()
	if call.err == nil {
		c.report, c.at = call.report, time.Now()
	}
	c.call = nil
	c.mu.Unlock()
	close(call.done)
}

// handleAPIServiceStatus answers GET /api/v1/service. A server built
// without a ServiceStatuser answers 204, no document - nothing to report,
// which the page shows as no row rather than an error; a host without a
// systemd user manager answers 422, with the reason.
func (s *Server) handleAPIServiceStatus(w http.ResponseWriter, r *http.Request) {
	if s.serviceStatus == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	report, err := s.serviceCache.read(r.Context(), s.serviceStatus)
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
