// sse.js - one job's live progress, from GET /api/v1/jobs/{id}/events.
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
