// sse.go implements Task 7's Server-Sent Events layer
// (docs/plans/2026-08-30-serve-design.md §"Jobs and SSE": "SSE frames: one
// JSON object per typed core event, event: set to the event type name;
// comment heartbeats every ~15s").
//
// The framing is deliberately hand-written and byte-exact. A browser's
// EventSource is a line parser, not a JSON parser: a frame is an optional
// `event:` line, one or more `data:` lines, and a blank line terminating
// it. Everything here therefore writes complete frames and flushes, and the
// payload of a data line is always ONE line - core.MarshalEvent (the
// {"type","data"} envelope internal/core/testdata/events/*.golden already
// freezes) for an event, and a compact json/v2 encoding for the one
// document frame the stream sends. No second encoder re-derives an event's
// shape here, for the same reason api.go has none: the CLI's stream and
// this one must never disagree about what an event looks like.
package serve

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

const (
	// sseContentType is the media type every SSE response carries.
	sseContentType = "text/event-stream"

	// sseHeartbeatInterval is how often an idle stream writes a comment
	// frame (design: "comment heartbeats every ~15s"). It exists so a
	// silent job - one parked on a slow download, or queued behind core's
	// mutation slot - does not look like a dead connection to the browser
	// or to anything between it and this process.
	sseHeartbeatInterval = 15 * time.Second

	// sseDoneEvent is the name of the one non-core frame this package
	// emits: the terminal frame carrying the job status document, sent
	// once the job's Apply has returned. It exists because an EventSource
	// reconnects automatically whenever a stream simply ends, so a stream
	// that closed silently on completion would be reopened forever against
	// a finished job. The payload is exactly the document
	// GET /api/v1/jobs/{id} answers with - a new frame NAME, not a new
	// document type.
	sseDoneEvent = "done"
)

// sseStream writes one Server-Sent Events response. It is not safe for
// concurrent use: one goroutine (the handler's) owns the response body for
// the life of the stream.
type sseStream struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// newSSEStream sets the streaming response headers on w, commits the 200
// status line, and returns the stream that frames onto it.
//
// X-Accel-Buffering: no is the explicit opt-out nginx (and several other
// reverse proxies) honour; without it a proxy will happily buffer a
// progress stream into a single response delivered at the end, which is
// indistinguishable from a broken stream. `lmm serve` binds loopback by
// default and has no proxy in front of it, but the header costs one line
// and the alternative is a bug nobody can reproduce locally.
func newSSEStream(w http.ResponseWriter) *sseStream {
	h := w.Header()
	h.Set("Content-Type", sseContentType)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	s := &sseStream{w: w, rc: http.NewResponseController(w)}
	// Flush the headers immediately: a client must see the 200 and the
	// media type before the first event, which may be seconds away.
	_ = s.flush()
	return s
}

// flush pushes everything written so far to the client. It reaches the real
// ResponseWriter through http.ResponseController, which walks the Unwrap
// chain - the reason middleware.go's statusRecorder implements Unwrap
// (task-3 review Important 2). Without that, every stream through wrap
// would buffer until the handler returned.
func (s *sseStream) flush() error {
	if err := s.rc.Flush(); err != nil {
		return fmt.Errorf("flushing SSE stream: %w", err)
	}
	return nil
}

// sendEvent writes one typed core event as a frame: `event:` is the event's
// own type name (core.Event.EventType), and the data line is
// core.MarshalEvent's {"type","data"} envelope. json.Marshal never emits a
// raw newline, so the payload is always the single line SSE needs.
func (s *sseStream) sendEvent(e core.Event) error {
	payload, err := core.MarshalEvent(e)
	if err != nil {
		return fmt.Errorf("encoding SSE event: %w", err)
	}
	return s.frame(e.EventType(), payload)
}

// sendDocument writes a non-event frame: name is the frame's event name and
// v is encoded compactly, so the payload stays on one line like every event
// frame's does. Used for the terminal sseDoneEvent frame.
func (s *sseStream) sendDocument(name string, v any) error {
	payload, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return fmt.Errorf("encoding SSE %s document: %w", name, err)
	}
	return s.frame(name, payload)
}

// frame writes one complete `event:`/`data:` frame and flushes it.
func (s *sseStream) frame(name string, payload []byte) error {
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, payload); err != nil {
		return fmt.Errorf("writing SSE %s frame: %w", name, err)
	}
	return s.flush()
}

// comment writes an SSE comment frame - a line beginning with ":", which
// every EventSource discards without dispatching an event. This is the
// heartbeat, and the only way to write to the stream without the client
// observing anything.
func (s *sseStream) comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return fmt.Errorf("writing SSE comment: %w", err)
	}
	return s.flush()
}

// heartbeatTicker is the clock seam the stream's heartbeat runs on: it
// returns the tick channel and the function that releases the underlying
// timer. Production uses realHeartbeatTicker; the tests drive a channel
// they send on by hand, so a heartbeat assertion never sleeps for a real
// 15 seconds (GO.md: "No sleeps; prefer fake clocks or synchronization").
type heartbeatTicker func(d time.Duration) (ticks <-chan time.Time, stop func())

// realHeartbeatTicker is heartbeatTicker over time.Ticker.
func realHeartbeatTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// sseSubscriberBuffer is how many events a stream may fall behind by before
// the job disconnects it (jobs.go's emit: a subscriber that cannot keep up
// is dropped rather than allowed to stall the Apply, because core calls
// event sinks synchronously on the mutation's own goroutine). It is
// deliberately generous - a quarter of the ring - since the only thing on
// the other side of this buffer is a socket write, and a browser slow
// enough to overflow it has almost certainly gone away.
const sseSubscriberBuffer = 256

// handleAPIJobEvents answers GET /api/v1/jobs/{id}/events with the job's
// event stream: everything the ring still holds, then everything emitted
// from that instant on. The 404 for an unknown job is written as the usual
// JSON envelope, before the response becomes a stream.
func (s *Server) handleAPIJobEvents(w http.ResponseWriter, r *http.Request) {
	j, ok := s.lookupJob(w, r)
	if !ok {
		return
	}
	s.streamJobEvents(w, r, j)
}

// streamJobEvents writes j's events to w until the job finishes, the client
// goes away, or the server starts draining.
//
// The replay/live seam is the whole point of subscribing BEFORE writing
// anything: job.subscribe snapshots the ring and registers the live channel
// in one critical section (jobs.go), so an event emitted while these replay
// frames are being written lands in the live channel rather than falling
// between the two. The stream therefore has no gap and no duplicate at the
// seam - not by ordering luck, but because the two halves are taken
// atomically.
//
// Every exit path runs cancel, which is what releases the subscription: a
// closed browser tab must not leave a channel attached to a running job for
// emit to keep writing into.
func (s *Server) streamJobEvents(w http.ResponseWriter, r *http.Request, j *job) {
	replay, live, cancel := j.subscribe(sseSubscriberBuffer)
	defer cancel()

	ticks, stopTicker := s.heartbeat(sseHeartbeatInterval)
	defer stopTicker()

	stream := newSSEStream(w)
	for _, e := range replay {
		if err := stream.sendEvent(e); err != nil {
			s.log.Debug("serve: SSE replay write failed", "job", j.id, "err", err)
			return
		}
	}

	for {
		select {
		case e, ok := <-live:
			if !ok {
				s.finishStream(stream, j)
				return
			}
			if err := stream.sendEvent(e); err != nil {
				s.log.Debug("serve: SSE write failed", "job", j.id, "err", err)
				return
			}
		case <-ticks:
			if err := stream.comment("heartbeat"); err != nil {
				s.log.Debug("serve: SSE heartbeat write failed", "job", j.id, "err", err)
				return
			}
		case <-r.Context().Done():
			return
		case <-s.draining:
			return
		}
	}
}

// finishStream writes whatever ends a stream whose live channel closed.
//
// The channel closes for two different reasons and they are answered
// differently. If the job is over, the terminal sseDoneEvent frame carries
// the final job status document, and the client stops. If the job is still
// running, this subscriber was DROPPED for lagging (jobs.go's emit), and
// the honest thing is to say so in a comment and hang up: an EventSource
// reconnects on its own, and the reconnection's ring replay closes the gap
// - at the cost of repeating events this stream already delivered, which is
// the right trade when the alternative is silently missing some.
func (s *Server) finishStream(stream *sseStream, j *job) {
	status := j.status()
	if status.State == jobRunning {
		s.log.Debug("serve: SSE subscriber lagged and was disconnected", "job", j.id)
		_ = stream.comment("lagged: reconnect to resume from the replay buffer")
		return
	}
	if err := stream.sendDocument(sseDoneEvent, status); err != nil {
		s.log.Debug("serve: SSE terminal frame write failed", "job", j.id, "err", err)
	}
}
