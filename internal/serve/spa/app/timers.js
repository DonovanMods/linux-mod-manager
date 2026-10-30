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

/** activityReopenMillis is how long a closed activity stream waits before
 * it is opened again - EventSource's own customary reconnection delay, so a
 * refused stream is retried no more eagerly than a dropped one. */
export const activityReopenMillis = 3000 * uiTimerScale;
