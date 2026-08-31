// sse.js - the two live streams this application follows.
//
// followJob is ONE job's full typed progress, from
// GET /api/v1/jobs/{id}/events - what a viewer who has opened a job wants.
// followActivity is the multiplexed session stream, GET /api/v1/events -
// every job's lifecycle at a glance, which is what the activity tray
// follows while it is closed. They are two streams on purpose (activity.go
// §THE FRAME VOCABULARY), and this module is where that distinction lives:
// no component decides which one it is on.
//
// The per-job stream's contract follows.
//
// The stream's contract (sse.go): one JSON frame per typed core progress
// event, with `event:` naming the event type; a comment heartbeat while the
// job is otherwise quiet; and a final `event: done` frame carrying the same
// job status document GET /api/v1/jobs/{id} answers with. That terminal
// frame is why a caller never has to race "the stream closed" against "go
// fetch the final status".

/**
 * Follows one job to its terminal frame.
 *
 * onEvent receives each progress event; onDone receives the final job
 * status. Returns a function that stops following - which a component MUST
 * call when it unmounts, or a closed view keeps an open request alive.
 *
 * Closing the stream never cancels the job: an Apply runs on the registry's
 * own context, never the request's (jobs.go), so a closed tab cannot
 * abandon a half-finished mutation.
 */
export function followJob(id, { onEvent, onDone, onError } = {}) {
  const source = new EventSource(
    `/api/v1/jobs/${encodeURIComponent(id)}/events`,
  );

  source.addEventListener("done", (frame) => {
    source.close();
    onDone?.(JSON.parse(frame.data));
  });

  // A typed core event always arrives under its OWN `event:` name (sse.go
  // sets it from Event.EventType()), never the default "message", and
  // EventSource has no wildcard - so each type is attached by name.
  const forward = (frame) => {
    onEvent?.({ type: frame.type, data: JSON.parse(frame.data) });
  };
  for (const type of jobEventTypes) {
    source.addEventListener(type, forward);
  }

  source.onerror = (err) => {
    // EventSource reconnects on its own; a stream that ended because the
    // job ended has already delivered its done frame and closed above.
    if (source.readyState === EventSource.CLOSED) {
      onError?.(err);
    }
  };

  return () => source.close();
}

/**
 * The core event types a job stream can name - internal/core/events.go's
 * Event.EventType() vocabulary, verbatim. EventSource cannot subscribe to
 * "any named event", so this list is what followJob attaches to; a type
 * missing here is not an error, it simply is not surfaced as progress.
 */
export const jobEventTypes = [
  "step",
  "download",
  "mod",
  "hook",
  "warning",
  "merge",
  "update_check",
];

/**
 * Follows the multiplexed session stream (GET /api/v1/events).
 *
 * The frame vocabulary is activity.go's, verbatim, and is closed - four
 * names and no others:
 *
 *   snapshot      a jobsIndex; the FIRST frame every subscriber gets,
 *                 however late it arrives, so a tray opened mid-deploy is
 *                 caught up before it is told anything new. A reconnecting
 *                 EventSource gets a fresh one, which is why this stream
 *                 needs no Last-Event-ID replay of its own - and why
 *                 onSnapshot REPLACES the index rather than merging into it.
 *   job_started   a jobSummary, published in the same critical section that
 *                 admits the job: a job is in the snapshot or in one of
 *                 these, never both and never neither.
 *   job_progress  a jobProgressFrame - a flat summary of one core event,
 *                 already coalesced server-side.
 *   job_done      a jobSummary, terminal for that job id, carrying its
 *                 error envelope when it failed.
 *
 * Returns a function that stops following. The stream never ends of its own
 * accord, so nothing else will close it.
 */
export function followActivity({
  onSnapshot,
  onStarted,
  onProgress,
  onDone,
  onError,
} = {}) {
  const source = new EventSource("/api/v1/events");

  const on = (name, handler) => {
    source.addEventListener(name, (frame) => handler?.(JSON.parse(frame.data)));
  };
  on("snapshot", onSnapshot);
  on("job_started", onStarted);
  on("job_progress", onProgress);
  on("job_done", onDone);

  source.onerror = () => {
    // CONNECTING means EventSource is retrying on its own and a reconnect
    // will re-deliver a full snapshot - not something to report as a
    // failure. CLOSED means it has given up, which the tray must say out
    // loud rather than quietly showing a frozen list.
    if (source.readyState === EventSource.CLOSED) {
      onError?.("lost the activity stream");
    }
  };

  return () => source.close();
}
