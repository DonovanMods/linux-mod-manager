// timers.js - the page's own UI timers, in one place (issue 576).
//
// Every timer a user can wait out - a toast clearing itself, a finished job
// handing its control back, a re-read after a request settled unread, the
// activity stream being reopened - is defined here, so the one thing that
// shortens them shortens all of them alike.
//
// That thing is uiTimerScale: a test-only factor the browser E2E harness
// sets on window (window.__lmmUITimerScale) BEFORE the page's own scripts
// run, so a scenario proving a dialog outlasts a timer need not wait it out
// in real time. Nothing the application serves sets it - no query parameter,
// flag or API reaches it - and anything but a finite number in (0, 1] is
// ignored, so a page nobody instrumented runs every timer at full length.
// Request deadlines (api.js, toggleack.js) are not UI timers and are not
// scaled here.

function readScale() {
  const scale =
    typeof window === "undefined" ? undefined : window.__lmmUITimerScale;
  if (typeof scale !== "number" || !Number.isFinite(scale)) return 1;
  return scale > 0 && scale <= 1 ? scale : 1;
}

/** uiTimerScale is the factor every timer below runs at: 1 in production. */
export const uiTimerScale = readScale();

/** toastDismissMillis is how long a success toast stays before it clears
 * itself. Failures, and sticky toasts, wait for the user (main.js#pushToast). */
export const toastDismissMillis = 8000 * uiTimerScale;

/** succeededOriginReleaseMillis is how long a succeeded job's readout keeps
 * its control before handing it back - unless the readout still carries a
 * next step (main.js#releaseSucceededOrigin, issue 531). */
export const succeededOriginReleaseMillis = 4000 * uiTimerScale;

/** unreadRereadDelayMillis is how long after a request settled unread the
 * page asks again (issue 454 N1). Long enough not to hammer a server that
 * is struggling, short enough that the row does not sit on a stale value
 * until some unrelated read. */
export const unreadRereadDelayMillis = 3_000 * uiTimerScale;

/** deferredHealthRetryMillis is how long the page waits to ask for health
 * again while the answer is deferred behind a running job (issue 588). A job
 * in this server re-reads it as it ends anyway; this is for a change no job
 * here announces - another lmm process holding the operation lock. */
export const deferredHealthRetryMillis = 5_000 * uiTimerScale;

/** activityReopenMillis is how long a closed activity stream waits before
 * it is opened again - EventSource's own customary reconnection delay, so a
 * refused stream is retried no more eagerly than a dropped one. */
export const activityReopenMillis = 3000 * uiTimerScale;

// The two loading-state timers below (issue 583) are deliberately NOT
// scaled: they are not something a user waits out but what keeps a fast
// read from flashing a loading line. The scale can only shorten them, and a
// shorter window is the one a slow runner's reads overrun; the E2E proving
// it instead checks each painted line against when its read answered.

/** loadingShowDelayMillis is how long a read must be in flight before its
 * loading state is painted - a read that answers sooner shows nothing. */
export const loadingShowDelayMillis = 180;

/** loadingMinShownMillis is how long a painted loading state stays at least,
 * so one that appears just before its read answers does not flicker. */
export const loadingMinShownMillis = 300;
