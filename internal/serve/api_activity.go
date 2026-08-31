// api_activity.go is the activity surface's HTTP half: GET /api/v1/jobs,
// the tray's index of the registry's retained jobs, and GET /api/v1/events,
// the ONE multiplexed lifecycle stream it follows for the whole session
// (docs/plans/2026-08-31-serve-spa-design.md §Jobs). The documents both
// answer with, the frame vocabulary, and the registry reads and
// subscriptions behind them are activity.go's.
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

// handleAPIEvents answers GET /api/v1/events with the multiplexed
// job-lifecycle stream: a snapshot frame carrying every retained job, then
// a frame per lifecycle transition of every job this process runs, until
// the client goes away or the server drains (activity.go's frame
// vocabulary).
//
// Subscribing BEFORE writing anything is the same discipline the per-job
// stream keeps, for the same reason: jobRegistry.watch snapshots the
// registry and registers the live channel in one critical section, so a job
// admitted while the snapshot frame is being written lands in the live
// channel instead of falling between the two. The seam therefore has no gap
// - not by ordering luck, but because the halves are taken atomically.
//
// This stream has no terminal frame of its own. A job's end is a job_done
// frame, not the stream's end; the stream lives as long as the tab does.
// Every exit path runs cancel, which is what releases the watcher: a closed
// tab must not leave a channel attached to the registry for every future
// job to be written into.
func (s *Server) handleAPIEvents(w http.ResponseWriter, r *http.Request) {
	snapshot, live, cancel := s.jobs.watch(activityWatcherBuffer)
	defer cancel()

	ticks, stopTicker := s.heartbeat(sseHeartbeatInterval)
	defer stopTicker()

	stream := newSSEStream(w)
	if err := stream.sendDocument(activitySnapshotEvent, jobsIndex{Jobs: snapshot}); err != nil {
		s.log.Debug("serve: activity snapshot write failed", "err", err)
		return
	}

	for {
		select {
		case ev, ok := <-live:
			if !ok {
				// The only way the channel closes with the watcher still
				// installed is the lagging drop (activity.go's
				// publishLocked). Say so and hang up: an EventSource
				// reconnects on its own, and the reconnection opens with a
				// fresh snapshot, so nothing is actually lost.
				s.log.Debug("serve: activity watcher lagged and was disconnected")
				_ = stream.comment("lagged: reconnect for a fresh snapshot")
				return
			}
			if err := stream.sendDocument(ev.Name, ev.Payload); err != nil {
				s.log.Debug("serve: activity write failed", "frame", ev.Name, "err", err)
				return
			}
		case <-ticks:
			if err := stream.comment("heartbeat"); err != nil {
				s.log.Debug("serve: activity heartbeat write failed", "err", err)
				return
			}
		case <-r.Context().Done():
			return
		case <-s.draining:
			return
		}
	}
}
