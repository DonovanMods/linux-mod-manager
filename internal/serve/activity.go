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

import (
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

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

// ---------------------------------------------------------------------------
// The multiplexed activity stream (GET /api/v1/events).
// ---------------------------------------------------------------------------

// THE FRAME VOCABULARY. GET /api/v1/events is ONE stream carrying the
// lifecycle of EVERY job this process runs, for the whole session. It is
// not the per-job stream in another shape: GET /api/v1/jobs/{id}/events
// still exists and still replays one job's full typed core events, which is
// what a viewer who has opened a job wants. This one is what the activity
// tray follows while it is closed - the design's "quick path inline, full
// path one click away", applied to progress.
//
// Four frame names, and no others. Every one is a `data:` line holding one
// JSON document, framed by sse.go exactly like the per-job stream's:
//
//	event: snapshot     data: a jobsIndex - EVERY retained job, newest
//	                    first. Sent once, as the FIRST frame, to every
//	                    subscriber however late it arrives, so a tray that
//	                    opens mid-deploy is caught up before it is told
//	                    anything new. A reconnecting EventSource gets a
//	                    fresh one, which is why this stream needs no
//	                    Last-Event-ID replay of its own.
//
//	event: job_started  data: a jobSummary. Published the instant a job is
//	                    admitted to the registry, in the same critical
//	                    section that admits it - so a job is either in a
//	                    subscriber's snapshot or in one of these, never
//	                    both and never neither.
//
//	event: job_progress data: a jobProgressFrame - a SUMMARY of one core
//	                    event (which job, which phase, which mod, how far),
//	                    not the event itself. The full typed event is one
//	                    click away on the per-job stream. Download ticks are
//	                    coalesced to whole percents when the total size is
//	                    known, or to a byte-delta threshold when it is not
//	                    (activityProgressGate) - a Content-Length-less
//	                    (chunked) download reports Percent 0 for its whole
//	                    duration, so Downloaded/TotalBytes are the only
//	                    fields a tray row can move on for one.
//
//	event: job_done     data: a jobSummary, terminal for that job id, its
//	                    {"error","details"} envelope included when it
//	                    failed. The result document is NOT here; read
//	                    GET /api/v1/jobs/{id} for it.
//
// The stream itself never ends of its own accord - there is no "done"
// frame for the stream, only for individual jobs. It ends when the client
// goes away or the server drains.
const (
	activitySnapshotEvent = "snapshot"
	activityStartedEvent  = "job_started"
	activityProgressEvent = "job_progress"
	activityDoneEvent     = "job_done"
)

// activityWatcherBuffer is how many frames a session stream may fall behind
// by before the registry drops it, for the same reason and with the same
// generosity as sseSubscriberBuffer: publishing happens on a running
// Apply's own goroutine and must never block, and a client that cannot
// drain 256 frames has gone away.
const activityWatcherBuffer = 256

// jobProgressFrame is one job_progress frame: a flat, renderable summary of
// one core event, carrying the job id so a multiplexed subscriber can route
// it to the right tray row.
//
// It is deliberately NOT core.MarshalEvent's {"type","data"} envelope. That
// envelope is the per-job stream's contract and stays frozen there; a tray
// row needs a phase, a mod name and a position, and re-deriving those from
// seven differently-shaped event types in the browser would put a second
// copy of core's event vocabulary in JavaScript. Type carries the core
// event's own name ("step", "download", "mod", "hook", "warning", "merge",
// "update_check") for the client that wants to style by kind.
//
// Numeric members use omitzero rather than omitempty: under encoding/json/v2
// omitempty omits empty JSON values (null, "", [], {}) and NOT the number
// zero, so an absent batch position would otherwise wire as "index": 0.
type jobProgressFrame struct {
	JobID jobID  `json:"job_id"`
	Kind  string `json:"kind"`

	// Type is core.Event.EventType(); Op is the event scope's operation.
	Type string `json:"type"`
	Op   string `json:"op,omitempty"`

	// Phase is core.FlowEvent.FlowPhase().String() - empty for the one
	// event type that carries no phase (UpdateCheckEvent).
	Phase string `json:"phase,omitempty"`

	// Detail is the event's human-readable line: a step/mod/hook event's
	// Detail, or a warning event's Message.
	Detail string `json:"detail,omitempty"`

	ModName string `json:"mod_name,omitempty"`
	Index   int    `json:"index,omitzero"`
	Total   int    `json:"total,omitzero"`

	// Percent is a download tick's completion, 0 when the total size is
	// unknown or the event is not a download. Downloaded and TotalBytes
	// are the byte counts behind it (both 0 for a non-download event);
	// TotalBytes stays 0 for the whole duration of a Content-Length-less
	// (chunked) download, which is when a tray row needs Downloaded
	// instead of Percent to show progress at all (task-2-review.md
	// Important 1).
	Percent    float64 `json:"percent,omitzero"`
	Downloaded int64   `json:"downloaded,omitzero"`
	TotalBytes int64   `json:"total_bytes,omitzero"`
}

// summarizeJobEvent projects one core event onto its progress frame.
// Everything it reads is optional by design: an event type that carries no
// scope, no phase or no detail simply leaves those members empty rather
// than needing a case of its own here.
func summarizeJobEvent(id jobID, kind string, e core.Event) jobProgressFrame {
	frame := jobProgressFrame{JobID: id, Kind: kind, Type: e.EventType()}
	if scoped, ok := e.(interface{ EventScope() core.Scope }); ok {
		scope := scoped.EventScope()
		frame.Op = string(scope.Op)
		frame.ModName = scope.ModName
		frame.Index = scope.Index
		frame.Total = scope.Total
	}
	if flow, ok := e.(core.FlowEvent); ok {
		frame.Phase = flow.FlowPhase().String()
	}
	switch ev := e.(type) {
	case core.StepEvent:
		frame.Detail = ev.Detail
	case core.ModEvent:
		frame.Detail = ev.Detail
	case core.HookEvent:
		frame.Detail = ev.Detail
	case core.WarningEvent:
		frame.Detail = ev.Message
	case core.DownloadEvent:
		frame.Percent = ev.Percent
		frame.Downloaded = ev.Downloaded
		frame.TotalBytes = ev.TotalBytes
	}
	return frame
}

// activityByteDeltaThreshold is the byte-delta gate's own "whole percent
// changed": how many additional bytes a download whose total size is
// unknown (TotalBytes 0 for its whole duration - downloader.go's contract
// for a Content-Length-less, chunked, response) must transfer before
// another tick is forwarded. int(Percent) is 0 for every such tick, so the
// percent gate below would otherwise forward exactly one frame for the
// entire download (task-2-review.md Important 1).
const activityByteDeltaThreshold = 1 << 20 // 1 MiB

// activityProgressGate decides which of a job's events reach the
// multiplexed stream.
//
// It exists for downloads. internal/core's progressReader emits a
// DownloadEvent on EVERY non-empty read (downloader.go), which is thousands
// of events for one large mod - fine for the per-job stream a viewer opened
// deliberately, and not fine at all for a stream that is open for the whole
// session and multiplexes every job at once. A download tick is therefore
// forwarded only when its WHOLE percent changes (at most 101 frames per
// download, and the tray's progress bar cannot render finer than that
// anyway) - or, when the total size is unknown and there is no percent to
// coalesce by, when Downloaded has grown by activityByteDeltaThreshold
// since the last forwarded tick. Every other event type passes through
// untouched.
//
// Coalescing by a threshold rather than by elapsed time is what keeps this
// clock-free and deterministic - there is no second timer to inject, and a
// test asserts an exact frame count.
//
// The two counters are keyed to one file: a change in the event's File or
// ModName resets both, so a second download in the same job cannot lose
// its first frame to the previous file's last whole percent (or last byte
// count).
type activityProgressGate struct {
	mu sync.Mutex

	seen      bool
	lastWhole int
	lastBytes int64

	lastFile    *domain.DownloadableFile
	lastModName string
}

// allow reports whether e should be published to the activity stream. It is
// safe for concurrent use: core documents its sinks as synchronous on the
// operation's goroutine, but a flow that ever fans out would otherwise make
// this a data race that only shows up under load.
func (g *activityProgressGate) allow(e core.Event) bool {
	download, ok := e.(core.DownloadEvent)
	if !ok {
		return true
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.seen && (download.File != g.lastFile || download.ModName != g.lastModName) {
		g.seen = false
	}
	g.lastFile, g.lastModName = download.File, download.ModName

	if download.TotalBytes <= 0 {
		if g.seen && download.Downloaded-g.lastBytes < activityByteDeltaThreshold {
			return false
		}
		g.seen, g.lastBytes = true, download.Downloaded
		return true
	}

	whole := int(download.Percent)
	if g.seen && whole == g.lastWhole {
		return false
	}
	g.seen, g.lastWhole = true, whole
	return true
}

// activityEvent is one frame on the bus: the SSE event name and the
// document to encode on its data line. It carries no json tags of its own -
// what goes on the wire is the Payload, which is one of the pinned
// documents above.
type activityEvent struct {
	Name    string
	Payload any
}

// activityWatcher is one subscriber to the multiplexed stream. closed
// tracks whether ch has already been closed, so a lagging-watcher drop and
// an explicit cancel can never close it twice - the same discipline jobSub
// keeps for the per-job streams.
type activityWatcher struct {
	ch     chan activityEvent
	closed bool
}

// watch returns the CURRENT set of retained jobs and a channel carrying
// every lifecycle frame published after that instant - both taken in one
// critical section, which is the whole point. A job admitted while these
// two were being assembled separately would land in neither, and a tray
// would then show a running deploy that never appears and never completes.
//
// buf is the channel's depth; a watcher that falls further behind is
// dropped (see publishLocked). cancel is idempotent and MUST be called when
// the subscriber goes away, or the registry writes into a channel nobody
// reads for the rest of the process's life. A non-positive buf takes
// activityWatcherBuffer.
func (r *jobRegistry) watch(buf int) (snapshot []jobSummary, live <-chan activityEvent, cancel func()) {
	if buf < 1 {
		buf = activityWatcherBuffer
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	ch := make(chan activityEvent, buf)
	key := r.nextWatcher
	r.nextWatcher++
	r.watchers[key] = &activityWatcher{ch: ch}
	return r.listLocked(), ch, func() { r.unwatch(key) }
}

// unwatch drops the watcher registered under key and closes its channel, if
// it is still registered.
func (r *jobRegistry) unwatch(key int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.watchers[key]
	if !ok {
		return
	}
	delete(r.watchers, key)
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
}

// watcherCount reports how many live activity watchers the registry holds.
// Test-facing: it is how the leak test proves a dropped client really
// released its subscription.
func (r *jobRegistry) watcherCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.watchers)
}

// publish fans ev out to every watcher.
//
// It is called from a RUNNING APPLY's goroutine (jobRegistry.run's sink),
// so it must never block and must never be reached while that job's own
// mutex is held - both halves of the lock order listed on list(). The
// caller therefore finishes with job.emit before calling this, and the
// sends below are non-blocking: a watcher whose buffer is full is dropped,
// exactly as a lagging per-job subscriber is, because a browser that
// stopped reading must not be able to stall a deploy.
func (r *jobRegistry) publish(ev activityEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishLocked(ev)
}

// publishLocked is publish for a caller that already holds mu - Start,
// which has to admit the job and announce it in the same critical section.
// The caller must hold mu.
func (r *jobRegistry) publishLocked(ev activityEvent) {
	for key, w := range r.watchers {
		select {
		case w.ch <- ev:
		default:
			w.closed = true
			close(w.ch)
			delete(r.watchers, key)
		}
	}
}
