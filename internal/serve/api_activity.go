// api_activity.go is the activity surface's HTTP half: GET /api/v1/jobs,
// the tray's index of the registry's retained jobs
// (docs/plans/2026-08-31-serve-spa-design.md §Jobs). The documents it
// answers with, and the registry reads behind them, are activity.go's.
package serve

import "net/http"

// handleAPIJobs answers GET /api/v1/jobs with the jobs index (jobsIndex,
// goldened in testdata/json/jobs_index.golden): every job the registry
// still retains, newest first, each summarised without its result
// document.
//
// It is not game/profile-scoped, and deliberately so: the registry is one
// per process, a job is not owned by the selection that started it, and a
// tray that hid a running deploy because the user had switched profiles
// would be lying about what the machine is doing.
func (s *Server) handleAPIJobs(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, jobsIndex{Jobs: s.jobs.list()})
}
