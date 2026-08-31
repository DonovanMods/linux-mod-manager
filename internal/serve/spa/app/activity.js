// activity.js - the multiplexed job stream, applied to the store.
//
// One connection for the whole session (sse.js's followActivity), opened
// once at boot and never per-view: every job this process runs arrives
// here, whether this tab started it, another tab did, or it is still
// running from before this page loaded. That is what lets the activity
// tray, the morphing controls and the toasts all read from one place
// instead of each polling for its own idea of what is happening.
//
// It also owns the ORIGIN MOUNT REGISTRY, which is small but load-bearing:
// the design's toast rule is "job completion/failure when its origin isn't
// on-screen; never for things in view" (§Jobs), and the only honest way to
// answer "is it in view" is to ask the control itself. A control that
// renders a job's progress registers its origin while it is mounted; a
// completion for an origin nobody has mounted is the one that toasts.

import { followActivity } from "./sse.js";

/**
 * Merges one job summary into the index, newest first.
 *
 * A job already listed is REPLACED in place (its position preserved), so a
 * job_done frame updates the row the snapshot or job_started frame put
 * there rather than adding a second row for the same id. An unknown job is
 * prepended, which is where the server's own newest-first ordering puts it.
 */
export function upsertSummary(list, summary) {
  const rows = list ?? [];
  const at = rows.findIndex((row) => row.id === summary.id);
  if (at < 0) return [summary, ...rows];
  const next = rows.slice();
  next[at] = summary;
  return next;
}

// mountedOrigins counts, per origin key, how many controls are currently
// rendering it. A count rather than a set because two controls may
// legitimately show the same job (the top bar's Deploy and, later, a card's
// own affordance for the same mutation), and the first to unmount must not
// make the second invisible to the toast rule.
const mountedOrigins = new Map();

/**
 * Registers origin as on-screen and returns the function that unregisters
 * it - shaped for a Preact effect's cleanup, which is its only caller.
 */
export function registerOrigin(origin) {
  if (!origin) return () => {};
  mountedOrigins.set(origin, (mountedOrigins.get(origin) ?? 0) + 1);
  return () => {
    const left = (mountedOrigins.get(origin) ?? 1) - 1;
    if (left > 0) mountedOrigins.set(origin, left);
    else mountedOrigins.delete(origin);
  };
}

/** isOriginMounted reports whether any control is currently rendering
 * origin - the toast rule's "is it in view". */
export function isOriginMounted(origin) {
  return Boolean(origin) && (mountedOrigins.get(origin) ?? 0) > 0;
}

/**
 * Connects the session stream to store, and calls onJobDone for each job
 * that reaches a terminal state while connected.
 *
 * The store writes are deliberately narrow: this is the only writer of
 * jobsIndex and jobProgress, so no other code path can disagree with the
 * stream about what the machine is doing. Returns the disconnect function.
 */
export function connectActivity(store, { onJobDone } = {}) {
  return followActivity({
    onSnapshot: (index) =>
      store.set({ jobsIndex: index.jobs ?? [], activityError: null }),

    onStarted: (summary) =>
      store.set({
        jobsIndex: upsertSummary(store.get().jobsIndex, summary),
        activityError: null,
      }),

    onProgress: (frame) =>
      store.set({
        jobProgress: { ...store.get().jobProgress, [frame.job_id]: frame },
      }),

    onDone: (summary) => {
      store.set({ jobsIndex: upsertSummary(store.get().jobsIndex, summary) });
      onJobDone?.(summary);
    },

    onError: (message) => store.set({ activityError: message }),
  });
}
