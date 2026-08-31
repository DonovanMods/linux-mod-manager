// activity.go is the registry's ACTIVITY surface: the summary rows
// GET /api/v1/jobs indexes and (in the same file, because they are the same
// data seen live) the multiplexed lifecycle stream GET /api/v1/events
// carries (docs/plans/2026-08-31-serve-spa-design.md §Jobs: "an activity
// tray backed by the registry's retained jobs").
//
// Two documents, one truth. jobs.go's jobStatus is one job in full - its
// result document included - and is what a client reads when it has opened
// something. What the tray needs is every job at a glance, which is the
// same fields MINUS the result: fifty result documents in one poll would
// make the cheapest request in the application the most expensive one.
// That is the design's "quick path inline, full path one click away" in
// wire form, and it is why jobSummary is derived FROM jobStatus rather than
// gathered independently: the two can disagree about nothing.
package serve

import "time"

// jobSummary is one row of the jobs index and the payload of the stream's
// job_started / job_done frames: everything the activity tray renders
// without opening anything.
//
// It is jobStatus without Result. A failed job DOES carry its error
// envelope here, typed details included, because the tray's job is to offer
// the next step in place (a conflict's "Overwrite?"), and an envelope is
// small and bounded where a result document is neither.
//
// State is the registry's own word for the job, which is running,
// succeeded or failed - there is no "queued". core's beginOp serialises
// mutations by BLOCKING rather than rejecting (internal/core/ops.go), so a
// job waiting for the mutation slot is in state running while doing nothing
// at all, and the registry genuinely cannot tell it apart from the job that
// holds the slot (jobRegistry.QueueDepth's doc comment). The only signal
// available to a client that wants to render "queued" is EventCount: a
// running job that has emitted nothing yet has not started working. It is a
// heuristic, and it is named here so the tray does not invent a worse one.
type jobSummary struct {
	ID        jobID     `json:"id"`
	Kind      string    `json:"kind"`
	State     jobState  `json:"state"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`

	// Error is the failure envelope, identical to the one jobStatus
	// carries. Nil unless State is jobFailed.
	Error *apiErrorEnvelope `json:"error,omitempty"`

	// EventCount and DroppedEvents are jobStatus's counters unchanged: how
	// many events the job has emitted in total, and how many of those the
	// ring can no longer replay.
	EventCount    int `json:"event_count"`
	DroppedEvents int `json:"dropped_events,omitempty"`
}

// jobsIndex is GET /api/v1/jobs's document: every job the registry still
// retains, NEWEST FIRST, which is the order the tray renders top-down.
//
// An object rather than a bare array, for the reason every other document
// on this wire is one: a top-level array has nowhere to grow a sibling
// member later without breaking every client that parses it.
type jobsIndex struct {
	// Jobs is never null - "no jobs yet" is an empty array, so a client
	// never has to tell a missing member apart from an empty one.
	Jobs []jobSummary `json:"jobs"`
}

// summarizeJobStatus projects a full job status document onto its index
// row. It exists so the two documents are derived from ONE read of the
// job's state rather than assembled twice from its fields.
func summarizeJobStatus(status jobStatus) jobSummary {
	return jobSummary{
		ID:            status.ID,
		Kind:          status.Kind,
		State:         status.State,
		StartedAt:     status.StartedAt,
		EndedAt:       status.EndedAt,
		Error:         status.Error,
		EventCount:    status.EventCount,
		DroppedEvents: status.DroppedEvents,
	}
}

// summary is the job's own index row.
func (j *job) summary() jobSummary { return summarizeJobStatus(j.status()) }

// list returns a summary of every retained job, newest first.
//
// LOCK ORDER. This takes the registry's mu and then, per job, that job's
// own mu (through job.status). That direction is safe because the reverse
// never happens: no code path in this package holds a job's mu while
// reaching for the registry's. The one path that could - a running Apply's
// event sink, which touches both - deliberately finishes with the job's
// lock before it publishes to the registry (jobRegistry.run). Keep it that
// way; the two locks are only orderable in this direction.
func (r *jobRegistry) list() []jobSummary {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listLocked()
}

// listLocked is list's body for a caller that already holds mu - the
// activity stream's snapshot, which must be taken in the same critical
// section that registers the watcher. The caller must hold mu.
func (r *jobRegistry) listLocked() []jobSummary {
	summaries := make([]jobSummary, 0, len(r.order))
	for i := len(r.order) - 1; i >= 0; i-- {
		j, ok := r.jobs[r.order[i]]
		if !ok {
			continue
		}
		summaries = append(summaries, j.summary())
	}
	return summaries
}
