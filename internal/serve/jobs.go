// jobs.go implements Task 6's job registry
// (docs/plans/2026-08-30-serve-design.md §"Jobs and SSE"): "each job runs
// one Apply in a goroutine with an EventSink writing to a bounded ring
// buffer (~1024 events) with SSE fanout; subscribers joining late replay the
// buffer first. States: running, succeeded, failed (with the envelope). The
// registry keeps the last ~50 jobs, in memory only - a restart forgets
// history (the DB remains the truth about state)."
//
// The load-bearing rule here is whose context an Apply runs under. A job's
// context derives from the SERVER's root context, never from the HTTP
// request that started it: a user who closes the tab, hits stop, or loses
// the connection mid-deploy must not abort a mutation halfway through -
// that is exactly the split-write hazard v2 Phase 3's Ruling 16 exists to
// prevent, viewed from the HTTP side. The request only starts the job; the
// job owns its own lifetime from there.
package serve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

const (
	// defaultJobRingSize is how many of a job's most recent events the
	// registry retains for replay (design: "~1024 events").
	defaultJobRingSize = 1024

	// defaultJobRetention is how many jobs the registry remembers (design:
	// "the last ~50 jobs, in memory only").
	defaultJobRetention = 50

	// defaultSubscriberBuffer is the per-subscriber channel depth
	// job.subscribe uses when a caller doesn't pick one. A subscriber that
	// falls this far behind is disconnected rather than allowed to stall
	// the Apply - see job.emit.
	defaultSubscriberBuffer = 64

	// jobCancelGrace bounds how long shutdown waits AFTER cancelling the
	// jobs' context, so a wedged Apply cannot hold `lmm serve` open forever.
	jobCancelGrace = 5 * time.Second
)

// errRegistryClosing is Start's refusal sentinel: once shutdown has set
// jobRegistry.closing, no new job may be accepted, because shutdown is about
// to (or already did) call wg.Wait, and a job admitted after that point
// would either race sync.WaitGroup's Add-after-Wait rule (task-6-review.md
// Important 1) or run under a root context shutdown is about to cancel.
// Task 7's mutation handlers map this to a 503 envelope: the server is
// draining, and retrying against this same process cannot help.
var errRegistryClosing = errors.New("serve: job registry is shutting down")

// jobState is a job's lifecycle state, and its wire value in a job status
// document.
type jobState string

// The three states the design names: a job is running until its Apply
// returns, then succeeded (result stored) or failed (error envelope stored).
const (
	jobRunning   jobState = "running"
	jobSucceeded jobState = "succeeded"
	jobFailed    jobState = "failed"
)

// jobID is a job's opaque handle - random, like a planID, so one browser
// tab cannot enumerate another's jobs.
type jobID string

// jobStatus is the snapshot a job page and (Task 7) GET /api/v1/jobs/{id}
// render: identity, state, timings, and exactly one of Result (the core
// result document the CLI's --json would have printed) or Error (the
// {"error","details"} envelope every other failure in this package uses).
type jobStatus struct {
	ID        jobID     `json:"id"`
	Kind      string    `json:"kind"`
	State     jobState  `json:"state"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`

	// Result is the value the job's run function returned, unwrapped - the
	// same core document the equivalent CLI command emits under --json.
	Result any `json:"result,omitempty"`

	// Error is the failure envelope, typed details included (a failed
	// install carries details.conflicts, a failed profile switch carries
	// details.warnings). Nil unless State is jobFailed.
	Error *apiErrorEnvelope `json:"error,omitempty"`

	// EventCount is every event the job has emitted, including ones the
	// ring has since dropped; DroppedEvents is how many of those are no
	// longer replayable, so a late subscriber can say its history is
	// partial instead of silently showing a truncated run.
	EventCount    int `json:"event_count"`
	DroppedEvents int `json:"dropped_events,omitempty"`
}

// jobSub is one live event subscriber (an SSE stream, in Task 7). closed
// tracks whether ch has already been closed, so a lagging-subscriber drop,
// an explicit cancel, and job completion can never close it twice.
type jobSub struct {
	ch     chan core.Event
	closed bool
}

// job is one Apply running (or finished) in the registry: its state, its
// bounded event history, and its live subscribers. Every field after mu is
// guarded by it; id, kind, ring, started and finished are immutable after
// construction.
type job struct {
	id      jobID
	kind    string
	ring    int
	started time.Time
	// finished is closed exactly once, by finish. done() hands it out as
	// the "this job is over" signal, readable without taking mu.
	finished chan struct{}

	mu       sync.Mutex
	state    jobState
	ended    time.Time
	result   any
	err      error
	envelope *apiErrorEnvelope

	// events is the ring: at most cap ring entries, oldest at head once it
	// has wrapped. count is every event ever emitted, dropped the number
	// the ring has overwritten.
	events  []core.Event
	head    int
	count   int
	dropped int

	subs    map[int]*jobSub
	nextSub int
}

// done returns a channel closed once the job's Apply has returned and its
// result or error envelope is stored.
func (j *job) done() <-chan struct{} { return j.finished }

// isFinished reports whether the job has completed, without taking mu - the
// eviction pass needs it while already holding the registry's own lock.
func (j *job) isFinished() bool {
	select {
	case <-j.finished:
		return true
	default:
		return false
	}
}

// status returns a snapshot of the job. Result and Error are the stored
// values themselves (not copies): both are treated as immutable once
// finish has stored them.
func (j *job) status() jobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return jobStatus{
		ID:            j.id,
		Kind:          j.kind,
		State:         j.state,
		StartedAt:     j.started,
		EndedAt:       j.ended,
		Result:        j.result,
		Error:         j.envelope,
		EventCount:    j.count,
		DroppedEvents: j.dropped,
	}
}

// failure returns the raw error a failed Apply returned, so a caller can
// branch on it with errors.As/errors.Is. The stored envelope carries the
// same failure as WIRE data; this is the same failure as a Go value. Nil
// unless the job failed.
//
// It has no production caller, and after Unit 3 it is not expected to gain
// one. The carry-in it was left open for (docs/plans/unit3-carry.md) was
// the SPA's failure handling - a conflict answered by re-running Apply -
// and that landed on the other side of the wire: the browser branches on
// the ENVELOPE's typed details (spa/app/failures.js), because a frontend
// on the far end of an HTTP response has no Go value to branch on in the
// first place.
//
// It stays because it is what three tests use to prove the typed error
// SURVIVES the job registry unflattened - that a *core.ConflictError is
// still a *core.ConflictError, and core.ErrStalePlan still matches
// errors.Is, after a round trip through a goroutine and a mutex. Deleting
// it would delete that proof, and the envelope those tests would have to
// assert on instead cannot distinguish a typed error from a well-worded
// string.
func (j *job) failure() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.err
}

// emit is the core.EventSink handed to the job's Apply. Core calls sinks
// synchronously on the operation's goroutine, so this must never block:
// the event is recorded in the ring, then offered to each subscriber with a
// non-blocking send. A subscriber whose buffer is full is DISCONNECTED (its
// channel closed and dropped) rather than waited on - a browser that stops
// reading must not be able to stall a deploy. A disconnected subscriber
// sees its channel close while the job is still running, which is the
// signal to re-subscribe: the ring replay then closes the gap.
//
// One instant stays ambiguous (task-6-review.md Minor 2; Task 7 narrowed
// but did not close it - task-7-review.md Minor 2). While the job is still
// running when a subscriber lags, sse.go's finishStream tells that
// subscriber it was dropped ("lagged: reconnect...") so its EventSource
// reconnects and the ring replay closes the gap. But if the job finishes
// in the window between the lag-drop and finishStream's status() read, the
// stream instead gets a normal terminal "done" frame - correct (the result
// IS the truth), but silent about the dropped events in between, with
// nothing distinguishing it from a subscriber that stayed connected the
// whole time. A client can still detect this itself: EventCount on the
// "done" frame's job status document is the total the job ever emitted,
// so a stream that counts what it actually received and compares against
// that total learns its history was partial even without an explicit
// signal.
func (j *job) emit(e core.Event) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.recordLocked(e)
	for key, s := range j.subs {
		select {
		case s.ch <- e:
		default:
			s.closed = true
			close(s.ch)
			delete(j.subs, key)
		}
	}
}

// recordLocked appends e to the ring, overwriting the OLDEST retained event
// once the ring is full (the defined overflow behaviour: a progress stream's
// newest events are the ones a late viewer needs; what was overwritten is
// still counted in dropped). The caller must hold mu.
func (j *job) recordLocked(e core.Event) {
	j.count++
	if len(j.events) < j.ring {
		j.events = append(j.events, e)
		return
	}
	j.events[j.head] = e
	j.head = (j.head + 1) % j.ring
	j.dropped++
}

// replayLocked returns the retained events, oldest first. The caller must
// hold mu.
func (j *job) replayLocked() []core.Event {
	if len(j.events) == 0 {
		return nil
	}
	out := make([]core.Event, 0, len(j.events))
	for i := range j.events {
		out = append(out, j.events[(j.head+i)%len(j.events)])
	}
	return out
}

// subscribe returns the job's retained event history and a channel carrying
// every event emitted after that snapshot - taken in one critical section,
// so a subscriber can neither miss an event nor see one twice. buf is the
// channel's depth; a subscriber that falls further behind than that is
// disconnected (see emit).
//
// The returned channel is closed when the job finishes, when the subscriber
// is disconnected for lagging, or when cancel is called; a caller that needs
// to tell those apart reads status().State. Subscribing to an
// already-finished job returns its history and an already-closed channel.
// cancel is idempotent and must be called when the subscriber goes away
// (an SSE client disconnecting), or the job holds the channel until it
// finishes. A non-positive buf takes defaultSubscriberBuffer.
func (j *job) subscribe(buf int) (replay []core.Event, live <-chan core.Event, cancel func()) {
	if buf < 1 {
		buf = defaultSubscriberBuffer
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	replay = j.replayLocked()
	ch := make(chan core.Event, buf)
	if j.state != jobRunning {
		close(ch)
		return replay, ch, func() {}
	}

	key := j.nextSub
	j.nextSub++
	j.subs[key] = &jobSub{ch: ch}
	return replay, ch, func() { j.unsubscribe(key) }
}

// unsubscribe drops the subscriber registered under key and closes its
// channel, if it is still registered.
func (j *job) unsubscribe(key int) {
	j.mu.Lock()
	defer j.mu.Unlock()

	s, ok := j.subs[key]
	if !ok {
		return
	}
	delete(j.subs, key)
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}

// subscriberCount reports how many live subscribers the job currently
// holds. Test-facing: it is how the SSE leak test proves a dropped client
// really released its subscription rather than leaving a channel attached
// to a still-running job.
func (j *job) subscriberCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.subs)
}

// finish records the Apply's outcome and ends the job: a nil error stores
// the result document, a non-nil one stores the {"error","details"}
// envelope (typed details preserved through errorDetails, the same
// convention /api/v1's failures use) alongside the raw error. Every
// subscriber's channel is closed, then finished.
func (j *job) finish(result any, err error, at time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.ended = at
	if err != nil {
		j.state = jobFailed
		j.err = err
		j.envelope = &apiErrorEnvelope{Error: err.Error(), Details: errorDetails(err)}
	} else {
		j.state = jobSucceeded
		j.result = result
	}

	for key, s := range j.subs {
		if !s.closed {
			s.closed = true
			close(s.ch)
		}
		delete(j.subs, key)
	}
	close(j.finished)
}

// jobRegistry owns every job this server process has run recently: it
// starts them, keeps the most recent ones addressable, and drains them at
// shutdown.
type jobRegistry struct {
	// rootCtx is every job's context. It is stored rather than passed
	// (against the usual rule that a context is never held in a struct)
	// because it is a LIFETIME, not a request scope: jobs outlive the
	// request that starts them by design, and the only alternative root
	// would be context.Background(), which this module's boundary rules cap
	// at its two sanctioned call sites.
	//
	// It derives from the serve command's root context via
	// context.WithoutCancel, so a job inherits that context's values while
	// the REGISTRY - not the root's own cancellation - decides when jobs are
	// cancelled. That is what lets shutdown give a running deploy a bounded
	// grace to finish after Ctrl-C instead of tearing it up instantly.
	rootCtx context.Context
	cancel  context.CancelFunc

	log    *slog.Logger
	ring   int
	retain int

	// mu guards everything below. LOCK ORDER: mu may be held while taking
	// a job's own mu (jobRegistry.list, and Start's own announcement), and
	// the reverse must NEVER happen - see list()'s doc comment, and run(),
	// which is careful to finish with job.emit before it publishes.
	mu sync.Mutex
	// closing is set at the top of shutdown, under mu, before wg.Wait is
	// called - see Start, where the same lock makes "is the registry
	// closing" and "add this job to wg" one atomic decision.
	closing bool
	jobs    map[jobID]*job
	// order is every retained job's id in start order - the eviction pass
	// walks it oldest-first.
	order []jobID
	// watchers are the multiplexed activity stream's subscribers
	// (activity.go). They are registry-level rather than per-job because
	// the stream they feed outlives any one job: it is opened once, for the
	// session, and carries every job's lifecycle.
	watchers    map[int]*activityWatcher
	nextWatcher int
	wg          sync.WaitGroup
}

// newJobRegistry builds a registry whose jobs derive from ctx (see
// jobRegistry.rootCtx), retaining ring events per job and retain jobs
// overall. A non-positive ring or retain falls back to the package default.
func newJobRegistry(ctx context.Context, log *slog.Logger, ring, retain int) *jobRegistry {
	if ring < 1 {
		ring = defaultJobRingSize
	}
	if retain < 1 {
		retain = defaultJobRetention
	}
	rootCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &jobRegistry{
		rootCtx:  rootCtx,
		cancel:   cancel,
		log:      log,
		ring:     ring,
		retain:   retain,
		jobs:     map[jobID]*job{},
		watchers: map[int]*activityWatcher{},
	}
}

// Start runs run in its own goroutine as a job of the given kind
// ("install", "deploy", ...) and returns the id immediately, so the handler
// that started it can redirect to the job page while the Apply is still
// going. run receives the registry's root context - NEVER the request's -
// and an EventSink that feeds the job's ring buffer and its subscribers.
//
// Admitting a job also announces it on the multiplexed activity stream, in
// the same critical section (activity.go's frame vocabulary).
//
// Start refuses to admit a job once shutdown has begun, returning
// errRegistryClosing: the check against closing and the wg.Add that commits
// the registry to waiting for this job happen in the same critical section,
// so a Start racing shutdown either wins that section (and is drained by the
// very shutdown call it raced) or loses it (and never touches wg at all) -
// never both, and never neither (task-6-review.md Important 1).
func (r *jobRegistry) Start(kind string, run func(context.Context, core.EventSink) (any, error)) (jobID, error) {
	j := &job{
		id:       newJobID(),
		kind:     kind,
		ring:     r.ring,
		started:  time.Now(),
		finished: make(chan struct{}),
		state:    jobRunning,
		subs:     map[int]*jobSub{},
	}

	// Summarised BEFORE the registry lock is taken: j is not shared yet, so
	// this touches no contended lock, and the announcement below then needs
	// nothing but the registry's own.
	announcement := activityEvent{Name: activityStartedEvent, Payload: j.summary()}

	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return "", errRegistryClosing
	}
	r.jobs[j.id] = j
	r.order = append(r.order, j.id)
	r.evictLocked()
	r.wg.Add(1)
	// Admitting the job and announcing it are ONE critical section, which
	// is what makes a job appear exactly once to an activity subscriber:
	// a watcher registered before this point receives the frame, one
	// registered after finds the job in its snapshot, and none can do both
	// (activity.go's watch).
	r.publishLocked(announcement)
	r.mu.Unlock()

	go r.run(j, run)
	return j.id, nil
}

// run executes one job's Apply and records its outcome. A panic is
// converted into a failed job rather than left to kill the process: this
// goroutine is not one of net/http's, which recovers per-request panics, so
// an unrecovered panic in an Apply would take the whole server - and every
// other running job - down with it.
func (r *jobRegistry) run(j *job, apply func(context.Context, core.EventSink) (any, error)) {
	defer r.wg.Done()

	// The sink the Apply is handed does two things: it feeds this job's own
	// ring and per-job subscribers (j.emit), and it feeds the multiplexed
	// activity stream a SUMMARY of the same event (activity.go). The order
	// is load-bearing - j.emit finishes with the job's mutex before publish
	// reaches for the registry's, which is the lock order jobRegistry.mu
	// documents - and the gate is what keeps a download's per-read tick
	// storm off a stream that is open all session.
	gate := &activityProgressGate{}
	sink := func(e core.Event) {
		j.emit(e)
		if gate.allow(e) {
			r.publish(activityEvent{
				Name:    activityProgressEvent,
				Payload: summarizeJobEvent(j.id, j.kind, e),
			})
		}
	}

	var (
		result any
		err    error
	)
	func() {
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			r.log.Error("serve: job panicked",
				"job", j.id, "kind", j.kind, "panic", p, "stack", string(debug.Stack()))
			result, err = nil, fmt.Errorf("%s job panicked: %v", j.kind, p)
		}()
		result, err = apply(r.rootCtx, sink)
	}()

	j.finish(result, err, time.Now())
	r.publish(activityEvent{Name: activityDoneEvent, Payload: j.summary()})
}

// job returns the job with the given id, if the registry still holds it.
// A missing id means it never existed or has aged out (see evictLocked) -
// the design's "a restart forgets history (the DB remains the truth about
// state)" applies within a run too.
func (r *jobRegistry) job(id jobID) (*job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	return j, ok
}

// QueueDepth reports how many jobs this registry currently holds in state
// running. beginOp serializes core's mutations to one in flight at a time
// (internal/core/ops.go), so it BLOCKS rather than rejects: a second
// mutation job started while one is running does not fail, it sits in state
// running while doing nothing, indistinguishable from the job that is
// actually working (task-6-review.md Minor 1). QueueDepth exists so a caller
// can tell the two apart without inspecting individual jobs.
//
// Task 7's ruled policy is to refuse a new mutation job with a 409 envelope
// ("an operation is already running") once QueueDepth() > 8; Task 6 defines
// no route to enforce that against, so it is exposed here undecided and
// unenforced - carried to Task 7, not built now.
func (r *jobRegistry) QueueDepth() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	depth := 0
	for _, j := range r.jobs {
		if !j.isFinished() {
			depth++
		}
	}
	return depth
}

// evictLocked forgets the oldest FINISHED jobs until at most retain remain.
// A running job is never evicted, however many jobs follow it: its
// subscribers still need it, and its result has nowhere else to land. The
// caller must hold mu.
func (r *jobRegistry) evictLocked() {
	excess := len(r.order) - r.retain
	if excess <= 0 {
		return
	}

	kept := make([]jobID, 0, len(r.order))
	for _, id := range r.order {
		j, ok := r.jobs[id]
		if excess > 0 && (!ok || j.isFinished()) {
			delete(r.jobs, id)
			excess--
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}

// shutdown drains the registry: it waits for every running job to finish
// until ctx's deadline (the caller's bounded grace - see serveGraceful),
// then cancels the jobs' root context and waits a further jobCancelGrace
// for them to unwind. Core's own Ruling-16 completions still run to the end
// under cancellation, so a cancelled job leaves no half-written pair behind.
// It is safe to call more than once.
//
// "Safe to call more than once" does not mean "leaks nothing": if a job
// never returns even after cancellation (task-6-review.md Minor 3), the
// goroutine this call spawns to wait on r.wg blocks forever - one such
// goroutine per shutdown call made while that job is wedged. This is
// unavoidable (a goroutine cannot be killed from outside) and bounded from
// the caller's side by jobCancelGrace logging the failure, but it is NOT
// covered by this package's goroutine-leak ratchet
// (TestJobRegistry_ShutdownWaitsForRunningJobsAndLeavesNoGoroutines), which
// only exercises jobs that do finish. A reader relying on that ratchet
// should not assume it proves a wedged job leaks nothing too.
func (r *jobRegistry) shutdown(ctx context.Context) {
	defer r.cancel()

	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-ctx.Done():
	}

	r.log.Warn("serve: shutdown grace expired with jobs still running; cancelling them")
	r.cancel()
	select {
	case <-done:
	case <-time.After(jobCancelGrace):
		r.log.Error("serve: jobs did not exit after cancellation", "grace", jobCancelGrace)
	}
}

// newJobID returns a fresh unguessable job id, from crypto/rand for the
// same reason plan ids are (GO.md: never math/rand for a value that gates
// access).
func newJobID() jobID {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("serve: generating job id: %w", err))
	}
	return jobID(hex.EncodeToString(b))
}
